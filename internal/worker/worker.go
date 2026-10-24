package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/berrymore/kest/internal/relay"
)

// Payload is the input or the output of a job.
type Payload struct {
	Codec   relay.Flags // how Context is encoded: relay.CodecRaw, CodecJSON, CodecProto, or 0
	Context []byte      // metadata, such as an encoded HTTP request head
	Body    []byte
}

// Errors returned for jobs. M2 maps them to HTTP statuses.
var (
	// ErrQueueFull means MaxQueueSize callers are already waiting for a worker.
	ErrQueueFull = errors.New("too many requests are waiting for a worker")
	// ErrNoWorker means no worker became free within AllocateTimeout.
	ErrNoWorker = errors.New("no worker became free in time")
	// ErrStopped means the pool is stopping or stopped.
	ErrStopped = errors.New("worker pool is stopped")
	// ErrExecTimeout means the worker didn't answer within ExecTTL; it was killed.
	ErrExecTimeout = errors.New("worker did not answer in time")
	// ErrStartTimeout means a new worker didn't finish the handshake within StartTimeout.
	ErrStartTimeout = errors.New("worker did not finish the handshake in time")
	// ErrStopRequested means the worker asked to be replaced instead of answering.
	// The pool retries the job once on another worker.
	ErrStopRequested = errors.New("worker asked to be replaced instead of answering")
)

// JobError is an error the worker reported for a job (roadrunner/worker's
// $worker->error()). The worker stays in service.
type JobError struct {
	Message string
}

func (e *JobError) Error() string {
	return "worker error: " + e.Message
}

// ProcessError reports a worker process that failed: it broke the protocol,
// didn't answer in time, or died. The process is gone when this is returned.
type ProcessError struct {
	Pid    int
	Op     string // "handshake" or "exec"
	Err    error
	Exit   error  // how the process ended
	Stderr string // the last lines it wrote to stderr
}

func (e *ProcessError) Error() string {
	msg := fmt.Sprintf("worker %d: %s: %v", e.Pid, e.Op, e.Err)
	if e.Exit != nil {
		msg += fmt.Sprintf(" (%v)", e.Exit)
	}
	if e.Stderr != "" {
		msg += fmt.Sprintf("; stderr: %q", e.Stderr)
	}
	return msg
}

func (e *ProcessError) Unwrap() error {
	return e.Err
}

// failed kills p, waits for it to exit and describes the failure.
func failed(p process, op string, err error) *ProcessError {
	p.Kill()
	<-p.Done()
	return &ProcessError{Pid: p.Pid(), Op: op, Err: err, Exit: p.Err(), Stderr: p.Stderr()}
}

// handshake sends kest's pid and expects the worker's pid back, as RoadRunner
// does. The worker answers once the application has booted, so ctx (with
// StartTimeout) bounds the boot as well. It returns the pid the worker
// reported. On failure the process is killed and the error is a
// *ProcessError.
func handshake(ctx context.Context, p process, logger *slog.Logger) (int, error) {
	stop := context.AfterFunc(ctx, p.Kill)
	pid, err := exchangePids(p.Conn())
	if !stop() {
		// ctx ended and the process is being killed, whatever it answered.
		err = ErrStopped
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = ErrStartTimeout
		}
	}
	if err != nil {
		return 0, failed(p, "handshake", err)
	}
	if pid != p.Pid() {
		// Wrappers like "sh -c" or "docker exec" report another pid. Signals
		// and memory sampling use the process kest started.
		logger.Debug("worker reported another pid", "worker_pid", p.Pid(), "reported_pid", pid)
	}
	return pid, nil
}

func exchangePids(conn *relay.Conn) (int, error) {
	hello := fmt.Appendf(nil, `{"pid":%d}`, os.Getpid())
	if err := conn.Send(&relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: hello}); err != nil {
		return 0, err
	}
	var f relay.Frame
	if err := conn.Receive(&f); err != nil {
		return 0, err
	}
	if !f.Flags.Has(relay.FlagControl) {
		return 0, fmt.Errorf("expected a control frame with the pid, got flags %#x and %q", f.Flags, clip(f.Payload))
	}
	var reply struct {
		Pid int `json:"pid"`
	}
	if err := json.Unmarshal(f.Payload, &reply); err != nil || reply.Pid <= 0 {
		return 0, fmt.Errorf("invalid handshake reply %q", clip(f.Payload))
	}
	return reply.Pid, nil
}

// stopRequest is the header of a response by which the worker asks to be
// replaced (roadrunner/worker's $worker->stop()).
var stopRequest = []byte(`{"stop":true}`)

// execute sends one request and reads the response. ttl, if positive, bounds
// the exchange: when it expires the process is killed.
//
// A nil error or a *JobError leaves the process in service. ErrStopRequested
// leaves it alive but due for a graceful stop. Any other error is a
// *ProcessError, and the process is gone.
func execute(p process, req Payload, ttl time.Duration) (Payload, error) {
	var timer *time.Timer
	if ttl > 0 {
		timer = time.AfterFunc(ttl, p.Kill)
	}

	resp, err := exchange(p.Conn(), req)
	var jobErr *JobError
	switch {
	case timer != nil && !timer.Stop():
		// The kill is under way, whatever the worker answered.
		return Payload{}, failed(p, "exec", ErrExecTimeout)
	case err == nil, errors.As(err, &jobErr), errors.Is(err, ErrStopRequested):
		return resp, err
	default:
		return Payload{}, failed(p, "exec", err)
	}
}

func exchange(conn *relay.Conn, req Payload) (Payload, error) {
	payload := make([]byte, 0, len(req.Context)+len(req.Body))
	payload = append(append(payload, req.Context...), req.Body...)
	err := conn.Send(&relay.Frame{
		Flags:   req.Codec & relay.CodecMask,
		Options: []uint32{uint32(len(req.Context))}, //nolint:gosec // the frame size check covers it
		Payload: payload,
	})
	if err != nil {
		return Payload{}, err
	}

	var f relay.Frame
	if err := conn.Receive(&f); err != nil {
		return Payload{}, err
	}
	switch {
	case f.Flags.Has(relay.FlagError):
		return Payload{}, &JobError{Message: string(f.Payload)}
	case f.Flags.Has(relay.FlagControl):
		return Payload{}, fmt.Errorf("unexpected control frame %q", clip(f.Payload))
	case f.Stream != 0:
		return Payload{}, fmt.Errorf("streaming responses are not supported (stream bits %#x)", f.Stream)
	case len(f.Options) != 1 || int(f.Options[0]) > len(f.Payload):
		return Payload{}, fmt.Errorf("invalid response: options %v for a %d-byte payload", f.Options, len(f.Payload))
	}
	resp := Payload{
		Codec:   f.Flags & relay.CodecMask,
		Context: f.Payload[:f.Options[0]],
		Body:    f.Payload[f.Options[0]:],
	}
	if len(resp.Body) == 0 && bytes.Equal(resp.Context, stopRequest) {
		return Payload{}, ErrStopRequested
	}
	return resp, nil
}

// stopProcess asks the process to exit and gives it timeout to do so, then
// kills it. It returns once the process has exited, reporting whether it
// exited on its own.
func stopProcess(p process, timeout time.Duration) bool {
	select {
	case <-p.Done():
		return true
	default:
	}

	timer := time.AfterFunc(timeout, p.Kill)
	// The timer also ends a Send that blocks.
	err := p.Conn().Send(&relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: stopRequest})
	if err != nil {
		p.Kill()
	}
	<-p.Done()
	return timer.Stop() && err == nil
}

// clip shortens worker output quoted in error messages.
func clip(b []byte) []byte {
	const limit = 256
	if len(b) > limit {
		return b[:limit]
	}
	return b
}
