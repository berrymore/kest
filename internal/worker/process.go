// Package worker runs pools of long-lived worker processes that speak the
// RoadRunner worker protocol (ADR-0003). It knows nothing about HTTP or PHP:
// a job is an opaque payload in, a payload out.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"

	"github.com/berrymore/kest/internal/relay"
)

// process is the OS side of a worker: a running command and the streams to
// it. execProcess runs a real command; workertest has an in-process fake.
type process interface {
	// Pid identifies the process in logs and for memory sampling.
	Pid() int
	// Conn exchanges frames with the process.
	Conn() *relay.Conn
	// Kill stops the process and the processes it started with SIGKILL. It
	// does nothing once the process has exited.
	Kill()
	// Done is closed once the process has exited.
	Done() <-chan struct{}
	// Err is the exit error, nil for exit status 0. Valid after Done.
	Err() error
	// Stderr returns the last lines the process wrote to stderr.
	Stderr() string
	// Close releases the streams. Call it after Done.
	Close()
}

// waitDelay bounds how long Wait waits for stderr after the process exits:
// a child of the worker may keep the pipe open.
const waitDelay = time.Second

// processConfig describes the command to run.
type processConfig struct {
	command    []string
	env        []string // the complete environment; RR_RELAY is added
	dir        string
	maxPayload int // largest frame payload accepted from the process
}

// execProcess is a real worker process. Its stdin and stdout are pipes that
// kest creates itself, so that exec.Cmd.Wait doesn't close stdout while a
// response may still be unread in it.
type execProcess struct {
	cmd    *exec.Cmd
	conn   *relay.Conn
	stdin  *os.File // write end
	stdout *os.File // read end
	stderr *stderrLog
	done   chan struct{}
	err    error // set before done is closed
}

// startProcess starts the command. It returns once the process is running;
// the handshake is up to the caller. The process is killed when ctx is done:
// ctx is the owner's lifetime, not a start timeout.
func startProcess(ctx context.Context, cfg processConfig, logger *slog.Logger) (*execProcess, error) {
	if len(cfg.command) == 0 {
		return nil, errors.New("empty command")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW)
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	stderr := newStderrLog(logger)
	cmd := exec.CommandContext(ctx, cfg.command[0], cfg.command[1:]...) //nolint:gosec // the command is the operator's config
	cmd.Env = slices.Concat(cfg.env, []string{"RR_RELAY=pipes"})
	cmd.Dir = cfg.dir
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = stderr
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = waitDelay

	if err := cmd.Start(); err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, fmt.Errorf("start %s: %w", cfg.command[0], err)
	}
	// The child has its own copies now.
	closeAll(inR, outW)
	stderr.setLogger(logger.With("worker_pid", cmd.Process.Pid))

	p := &execProcess{
		cmd:    cmd,
		conn:   relay.NewConn(outR, inW, cfg.maxPayload),
		stdin:  inW,
		stdout: outR,
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go p.wait()
	return p, nil
}

func (p *execProcess) wait() {
	err := p.cmd.Wait()
	p.stderr.flush()
	p.err = err
	close(p.done)
}

func (p *execProcess) Pid() int              { return p.cmd.Process.Pid }
func (p *execProcess) Conn() *relay.Conn     { return p.conn }
func (p *execProcess) Done() <-chan struct{} { return p.done }
func (p *execProcess) Stderr() string        { return p.stderr.tail() }

func (p *execProcess) Err() error {
	<-p.done
	return p.err
}

func (p *execProcess) Kill() {
	select {
	case <-p.done:
	default:
		_ = killGroup(p.cmd.Process.Pid)
	}
}

func (p *execProcess) Close() {
	closeAll(p.stdin, p.stdout)
}

// killGroup sends SIGKILL to the process group the worker leads (Setpgid),
// so that processes the worker started die with it.
func killGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}
