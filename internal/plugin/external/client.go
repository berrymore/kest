package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/plugin/wire"
	"github.com/berrymore/kest/internal/proc"
	"github.com/berrymore/kest/internal/relay"
)

// waitDelay bounds how long Wait waits for the plugin's output after it
// exits: a child of the plugin may keep the pipes open.
const waitDelay = time.Second

// launch describes how to start the plugin.
type launch struct {
	command  []string
	dir      string
	env      []string // complete, the protocol's variables included
	hello    *relay.Frame
	maxFrame int
	timeout  time.Duration // of the start, handshake included
}

// client is one running plugin process and the connection to it.
type client struct {
	cmd    *exec.Cmd
	conn   net.Conn
	rc     *relay.Conn
	stderr *proc.LineLog
	logger *slog.Logger

	wmu     sync.Mutex // rc.Send
	mu      sync.Mutex
	nextID  uint32
	pending map[uint32]chan *wire.Call // a nil value delivered: the connection broke
	broken  error

	exited  chan struct{} // closed when the process has exited
	waitErr error         // set before exited is closed
	wg      sync.WaitGroup
}

// start runs the plugin and does the handshake. On error nothing is left
// running.
func start(l *launch, logger *slog.Logger) (*client, error) {
	kestEnd, pluginEnd, err := socketPair()
	if err != nil {
		return nil, fmt.Errorf("make the plugin's socket: %w", err)
	}
	stderr := proc.NewLineLog(logger, "plugin stderr")
	stdout := proc.NewLineLog(logger, "plugin stdout")
	cmd := exec.Command(l.command[0], l.command[1:]...) //nolint:gosec,noctx // the operator's command; stopped by the protocol, then killed
	cmd.Env = l.env
	cmd.Dir = l.dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.ExtraFiles = []*os.File{pluginEnd} // descriptor wire.FD
	cmd.SysProcAttr = proc.SysProcAttr()
	cmd.WaitDelay = waitDelay
	err = cmd.Start()
	_ = pluginEnd.Close()
	if err != nil {
		_ = kestEnd.Close()
		return nil, fmt.Errorf("start %s: %w", l.command[0], err)
	}
	logger = logger.With("plugin_pid", cmd.Process.Pid)
	stderr.SetLogger(logger)
	stdout.SetLogger(logger)

	c := &client{cmd: cmd, stderr: stderr, logger: logger, pending: make(map[uint32]chan *wire.Call),
		exited: make(chan struct{})}
	c.wg.Go(func() {
		c.waitErr = cmd.Wait()
		stderr.Flush()
		stdout.Flush()
		close(c.exited)
	})
	conn, err := net.FileConn(kestEnd)
	_ = kestEnd.Close()
	if err != nil {
		c.kill()
		return nil, fmt.Errorf("the plugin's socket: %w", err)
	}
	c.conn = conn
	c.rc = relay.NewConn(conn, conn, l.maxFrame)

	if err := c.handshake(l); err != nil {
		c.kill()
		return nil, err
	}
	c.wg.Go(c.read)
	return c, nil
}

// socketPair returns the two ends of a unix socket pair, close-on-exec, so
// that no other child of kest inherits them while this one starts: the
// plugin would then never see kest's end close.
func socketPair() (kestEnd, pluginEnd *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "kest end"), os.NewFile(uintptr(fds[1]), "plugin end"), nil
}

// handshake sends Hello and waits for Ready within the start timeout.
func (c *client) handshake(l *launch) error {
	if err := c.conn.SetDeadline(time.Now().Add(l.timeout)); err != nil {
		return err
	}
	if err := c.rc.Send(l.hello); err != nil {
		return c.startError("send the handshake", err)
	}
	var f relay.Frame
	if err := c.rc.Receive(&f); err != nil {
		return c.startError("wait for the handshake", err)
	}
	if !f.Flags.Has(relay.FlagControl) {
		return errors.New("the plugin's first frame is not a CONTROL frame: is it a kest plugin?")
	}
	if f.Flags.Has(relay.FlagError) {
		return fmt.Errorf("the plugin refused to start: %s", f.Payload)
	}
	var ready wire.Ready
	if err := json.Unmarshal(f.Payload, &ready); err != nil {
		return fmt.Errorf("the plugin's handshake: %w", err)
	}
	if ready.Protocol < 1 || ready.Protocol > wire.Version {
		return fmt.Errorf("the plugin speaks protocol %d, kest %d", ready.Protocol, wire.Version)
	}
	return c.conn.SetDeadline(time.Time{})
}

// startError explains a failed handshake: a timeout, or a plugin that
// exited, with what it wrote to stderr.
func (c *client) startError(what string, err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%s: no answer within start_timeout", what)
	}
	select {
	case <-c.exited:
	case <-time.After(waitDelay):
		return fmt.Errorf("%s: %w", what, err)
	}
	msg := fmt.Sprintf("the plugin exited during the handshake: %v", c.waitErr)
	if c.waitErr == nil {
		msg = "the plugin exited during the handshake"
	}
	if tail := c.stderr.Tail(); tail != "" {
		msg += "; stderr: " + tail
	}
	return errors.New(msg)
}

// read delivers answers to their calls until the connection ends; then
// every call waiting fails.
func (c *client) read() {
	var err error
	for {
		var f relay.Frame
		if err = c.rc.Receive(&f); err != nil {
			break
		}
		var ans *wire.Call
		if ans, err = wire.ParseCall(&f); err != nil {
			break
		}
		c.mu.Lock()
		ch := c.pending[ans.ID]
		delete(c.pending, ans.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- ans
		} // else a call that was canceled
	}
	c.mu.Lock()
	c.broken = err
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- nil
	}
	c.mu.Unlock()
}

// alive reports whether the connection still works.
func (c *client) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.broken == nil
}

// call sends a call and waits for its answer.
func (c *client) call(ctx context.Context, name string, call plugin.Call) ([]byte, error) {
	ch := make(chan *wire.Call, 1)
	c.mu.Lock()
	if c.broken != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("plugin %s is down", name)
	}
	id := c.nextID
	for c.pending[id] != nil {
		id++
	}
	c.nextID = id + 1
	c.pending[id] = ch
	c.mu.Unlock()

	frame := (&wire.Call{ID: id, Method: call.Method, Codec: relay.Flags(call.Codec), Body: call.Body}).Frame()
	if err := c.send(frame); err != nil {
		c.forget(id)
		return nil, fmt.Errorf("plugin %s: %w", name, err)
	}
	select {
	case ans := <-ch:
		switch {
		case ans == nil:
			return nil, fmt.Errorf("plugin %s exited during the call", name)
		case !ans.Failed:
			return ans.Body, nil
		case ans.Code == wire.CodeUnknownMethod:
			return nil, fmt.Errorf("%w: %s", plugin.ErrUnknownMethod, ans.Body)
		default:
			return nil, errors.New(string(ans.Body))
		}
	case <-ctx.Done():
		if c.forget(id) {
			if f, err := wire.ControlFrame(wire.Control{Cancel: &id}); err == nil {
				_ = c.send(f)
			}
		}
		return nil, ctx.Err()
	}
}

// forget removes a call that no longer waits, and reports whether it was
// still pending.
func (c *client) forget(id uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.pending[id]
	delete(c.pending, id)
	return ok
}

func (c *client) send(f *relay.Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.rc.Send(f)
}

// stop asks the plugin to stop and waits for it to exit, until ctx ends or
// timeout passes; then it kills it.
func (c *client) stop(ctx context.Context, timeout time.Duration) {
	if f, err := wire.ControlFrame(wire.Control{Stop: true}); err == nil {
		_ = c.send(f)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.exited:
	case <-timer.C:
		c.logger.Warn("plugin still running after stop_timeout; killing it")
	case <-ctx.Done():
		c.logger.Warn("plugin still running at the stop deadline; killing it")
	}
	c.kill()
}

// kill kills the plugin's process group, waits for it, and closes the
// connection; the read loop ends then.
func (c *client) kill() {
	select {
	case <-c.exited:
	default:
		_ = proc.KillGroup(c.cmd.Process.Pid)
	}
	<-c.exited
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.wg.Wait()
}
