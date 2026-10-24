package worker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker/workertest"
)

// helperEnv makes the test binary act as a worker process instead of running
// tests (the os/exec helper-process pattern). The value selects the behavior.
const helperEnv = "KEST_TEST_PROCESS"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(runHelper(mode))
	}
	leaktest.VerifyTestMain(m)
}

func runHelper(mode string) int {
	switch mode {
	case "fake":
		return workertest.RunProcess()
	case "env":
		wd, _ := os.Getwd()
		fmt.Println("CWD=" + wd)
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
	case "stderr":
		fmt.Fprint(os.Stderr, "first\nsecond\r\n\n")
		fmt.Fprint(os.Stderr, "no newline at the end")
	case "exit3":
		return 3
	case "sleep":
		time.Sleep(time.Hour)
	case "echo":
		conn := relay.NewConn(os.Stdin, os.Stdout, 1<<20)
		for {
			var f relay.Frame
			if err := conn.Receive(&f); err != nil {
				return 0
			}
			if err := conn.Send(&f); err != nil {
				return 1
			}
		}
	case "grandchild", "hold-stderr":
		// Start a sleeping child in our process group. With hold-stderr it
		// keeps our stderr open after we exit.
		child := exec.CommandContext(context.Background(), os.Args[0])
		child.Env = []string{helperEnv + "=sleep"}
		if mode == "hold-stderr" {
			child.Stderr = os.Stderr
		}
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(child.Process.Pid)
		if mode == "grandchild" {
			time.Sleep(time.Hour)
		}
	case "parent":
		// Start a worker process the way kest does, then sleep, so that the
		// test can kill us and check that the worker dies too.
		p, err := startProcess(context.Background(), helperConfig("sleep"), slog.New(slog.DiscardHandler))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(p.Pid())
		time.Sleep(time.Hour)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
		return 2
	}
	return 0
}

// helperGORACE stops the race detector from sleeping for a second before a
// helper exits.
const helperGORACE = "GORACE=atexit_sleep_ms=0"

func helperConfig(mode string) processConfig {
	return processConfig{
		command:    []string{os.Args[0]},
		env:        []string{helperEnv + "=" + mode, helperGORACE},
		maxPayload: 1 << 20,
	}
}

// startHelper starts the test binary in the given helper mode. The process is
// killed and released when the test ends.
func startHelper(t *testing.T, cfg processConfig, logger *slog.Logger) *execProcess {
	t.Helper()
	p, err := startProcess(t.Context(), cfg, logger)
	if err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	t.Cleanup(func() {
		p.Kill()
		<-p.Done()
		p.Close()
	})
	return p
}

func waitExit(t *testing.T, p process) error {
	t.Helper()
	select {
	case <-p.Done():
		return p.Err()
	case <-time.After(10 * time.Second):
		t.Fatalf("process %d did not exit", p.Pid())
		return nil
	}
}

func TestProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	cfg := helperConfig("env")
	cfg.env = append(cfg.env, "APP_ENV=test", "RR_RELAY=tcp://ignored")
	cfg.dir = dir
	p := startHelper(t, cfg, slog.New(slog.DiscardHandler))

	out, err := io.ReadAll(p.stdout)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := waitExit(t, p); err != nil {
		t.Fatalf("exit: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	wd, env := strings.TrimPrefix(lines[0], "CWD="), lines[1:]
	slices.Sort(env)
	want := []string{"APP_ENV=test", helperGORACE, helperEnv + "=env", "RR_RELAY=pipes"}
	if !slices.Equal(env, want) {
		t.Errorf("environment = %q, want exactly %q", env, want)
	}
	if got, want := realPath(t, wd), realPath(t, dir); got != want {
		t.Errorf("working directory = %s, want %s", got, want)
	}
}

func TestProcessStderr(t *testing.T) {
	logs := &logBuffer{}
	p := startHelper(t, helperConfig("stderr"), logs.logger())
	if err := waitExit(t, p); err != nil {
		t.Fatalf("exit: %v", err)
	}

	got := logs.stderrLines()
	if want := []string{"first", "second", "no newline at the end"}; !slices.Equal(got, want) {
		t.Errorf("logged lines = %q, want %q", got, want)
	}
	for _, r := range logs.records("worker stderr") {
		if r["worker_pid"] != float64(p.Pid()) {
			t.Errorf("record %v, want worker_pid %d", r, p.Pid())
		}
	}
	if got, want := p.Stderr(), "first\nsecond\r\n\nno newline at the end"; got != want {
		t.Errorf("Stderr() = %q, want %q", got, want)
	}
}

func TestProcessExitStatus(t *testing.T) {
	p := startHelper(t, helperConfig("exit3"), slog.New(slog.DiscardHandler))
	err := waitExit(t, p)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("exit error = %v, want exit status 3", err)
	}
}

func TestProcessKillStopsGroup(t *testing.T) {
	p := startHelper(t, helperConfig("grandchild"), slog.New(slog.DiscardHandler))
	grandchild := readPid(t, p.stdout)

	p.Kill()
	err := waitExit(t, p)
	if !killedBySignal(err, syscall.SIGKILL) {
		t.Errorf("exit error = %v, want killed by SIGKILL", err)
	}
	waitGone(t, grandchild)

	p.Kill() // a no-op after exit
}

func TestProcessWaitDelay(t *testing.T) {
	p := startHelper(t, helperConfig("hold-stderr"), slog.New(slog.DiscardHandler))
	grandchild := readPid(t, p.stdout)
	t.Cleanup(func() {
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		waitGone(t, grandchild)
	})

	start := time.Now()
	err := waitExit(t, p)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Errorf("exit error = %v, want %v", err, exec.ErrWaitDelay)
	}
	if elapsed := time.Since(start); elapsed > waitDelay+5*time.Second {
		t.Errorf("Done after %s, want about %s", elapsed, waitDelay)
	}
}

func TestProcessConn(t *testing.T) {
	p := startHelper(t, helperConfig("echo"), slog.New(slog.DiscardHandler))
	conn := p.Conn()

	frames := []relay.Frame{
		{Flags: relay.FlagControl | relay.CodecJSON, Payload: []byte(`{"pid":1}`)},
		{Flags: relay.CodecRaw, Options: []uint32{3}, Payload: []byte(strings.Repeat("payload ", 10_000))},
		{Stream: relay.StreamStop},
	}
	for _, want := range frames {
		if err := conn.Send(&want); err != nil {
			t.Fatalf("Send: %v", err)
		}
		var got relay.Frame
		if err := conn.Receive(&got); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if got.Flags != want.Flags || got.Stream != want.Stream || !slices.Equal(got.Options, want.Options) ||
			!bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("echoed frame differs: %.60v, want %.60v", got, want)
		}
	}

	// EOF on stdin ends the worker, as it does when kest dies on macOS.
	if err := p.stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	if err := waitExit(t, p); err != nil {
		t.Errorf("exit after EOF on stdin: %v", err)
	}
	var f relay.Frame
	if err := conn.Receive(&f); !errors.Is(err, io.EOF) {
		t.Errorf("Receive after exit = %v, want io.EOF", err)
	}
}

// The fake worker also runs as a real process, for tests that need one.
func TestProcessFakeWorker(t *testing.T) {
	cfg := helperConfig("fake")
	cfg.env = append(cfg.env, workertest.EnvFor(workertest.Options{Stderr: "booted\n"}))
	logs := &logBuffer{}
	p := startHelper(t, cfg, logs.logger())
	conn := p.Conn()

	exchange := func(f *relay.Frame) *relay.Frame {
		t.Helper()
		if err := conn.Send(f); err != nil {
			t.Fatalf("Send: %v", err)
		}
		var got relay.Frame
		if err := conn.Receive(&got); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		return &got
	}
	reply := exchange(&relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: []byte(`{"pid":1}`)})
	if want := fmt.Sprintf(`{"pid":%d}`, p.Pid()); string(reply.Payload) != want {
		t.Errorf("handshake reply %q, want %q", reply.Payload, want)
	}
	resp := exchange(&relay.Frame{Flags: relay.CodecRaw, Options: []uint32{3}, Payload: []byte("ctxbody")})
	if string(resp.Payload) != "ctxbody" || resp.Options[0] != 3 {
		t.Errorf("echo %v %q", resp.Options, resp.Payload)
	}

	if err := conn.Send(&relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: []byte(`{"stop":true}`)}); err != nil {
		t.Fatalf("Send stop: %v", err)
	}
	if err := waitExit(t, p); err != nil {
		t.Errorf("exit after stop: %v", err)
	}
	if got := logs.stderrLines(); !slices.Equal(got, []string{"booted"}) {
		t.Errorf("stderr lines = %q", got)
	}
}

func TestProcessContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	p, err := startProcess(ctx, helperConfig("sleep"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	defer p.Close()

	cancel()
	if err := waitExit(t, p); !killedBySignal(err, syscall.SIGKILL) {
		t.Errorf("exit error = %v, want killed by SIGKILL", err)
	}
}

func TestProcessStartErrors(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if _, err := startProcess(t.Context(), processConfig{maxPayload: 1}, logger); err == nil {
		t.Error("startProcess with an empty command succeeded")
	}
	cfg := processConfig{command: []string{filepath.Join(t.TempDir(), "missing")}, maxPayload: 1}
	_, err := startProcess(t.Context(), cfg, logger)
	if err == nil || !strings.Contains(err.Error(), "start "+cfg.command[0]) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("startProcess with a missing binary: %v", err)
	}
}

func TestProcessDiesWithParent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig exists only on Linux")
	}
	parent := startHelper(t, helperConfig("parent"), slog.New(slog.DiscardHandler))
	child := readPid(t, parent.stdout)
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	// SIGKILL the parent alone: the child is in a group of its own.
	if err := syscall.Kill(parent.Pid(), syscall.SIGKILL); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	_ = waitExit(t, parent)
	waitGone(t, child)
}

func readPid(t *testing.T, r io.Reader) int {
	t.Helper()
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	return pid
}

// waitGone waits until no process has the pid. It polls: the process is
// not ours to wait for.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func killedBySignal(err error, sig syscall.Signal) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == sig
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return p
}
