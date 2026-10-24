//go:build e2e

// Package e2e runs the real kest binary against real configs.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var kestBin string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "kest-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	kestBin = filepath.Join(dir, "kest")

	// Build with the race detector: a data race makes the binary exit with
	// a non-zero code, which fails the test that started it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-race",
		"-ldflags", "-X github.com/berrymore/kest/internal/buildinfo.version=e2e-test",
		"-o", kestBin, "github.com/berrymore/kest/cmd/kest")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build kest:", err)
		return 1
	}

	return m.Run()
}

// syncBuffer collects process output while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func kest(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(t.Context(), kestBin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run kest: %v", err)
	}
	return out.String(), errOut.String(), code
}

func get(t *testing.T, url string) (int, string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

func TestServeHelloExample(t *testing.T) {
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)

	example, err := os.ReadFile("../../examples/hello/kest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	config := strings.Replace(string(example), "address: 127.0.0.1:9901", "address: "+adminAddr, 1)
	configPath := filepath.Join(t.TempDir(), "kest.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs syncBuffer
	cmd := exec.CommandContext(t.Context(), kestBin, "serve", "-c", configPath)
	cmd.Env = append(os.Environ(), "KEST_ADDRESS="+listenAddr)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if code, _, err := get(t, "http://"+adminAddr+"/ready"); err == nil && code == http.StatusOK {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("kest exited early: %v\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("kest not ready after 10s\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, tc := range []struct{ path, want string }{
		{"/", "hello from kest\n"},
		{"/ping", "pong\n"},
		{"/anything/else", "hello from kest\n"},
	} {
		code, body, err := get(t, "http://"+listenAddr+tc.path)
		if err != nil || code != http.StatusOK || body != tc.want {
			t.Errorf("GET %s = %d %q, %v; want 200 %q", tc.path, code, body, err, tc.want)
		}
	}

	if _, body, err := get(t, "http://"+adminAddr+"/server_info"); err != nil || !strings.Contains(body, `"version": "e2e-test"`) {
		t.Errorf("server_info = %q, %v", body, err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("kest exited with %v after SIGTERM\n%s", err, logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("kest did not exit within 10s after SIGTERM\n%s", logs.String())
	}

	for _, want := range []string{"kest is ready", "shutting down", "kest stopped"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs do not contain %q:\n%s", want, logs.String())
		}
	}
}

func TestValidateCommand(t *testing.T) {
	stdout, stderr, code := kest(t, "validate", "-c", "../../examples/hello/kest.yaml")
	if code != 0 || !strings.Contains(stdout, "configuration is valid") {
		t.Errorf("validate example: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("version: v1\nlisteners:\n  - name: main\n    listen_on: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = kest(t, "validate", "-c", bad)
	if code != 1 || !strings.Contains(stderr, "bad.yaml:4:5: listeners[0].listen_on: unknown field") {
		t.Errorf("validate bad config: code %d, stderr %q", code, stderr)
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, _, code := kest(t, "version")
	if code != 0 || !strings.HasPrefix(stdout, "kest e2e-test\n") {
		t.Errorf("version: code %d, stdout %q", code, stdout)
	}
}
