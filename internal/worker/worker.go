package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"syscall"
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
	// ErrResetRunning means a Reset was called while another one runs.
	ErrResetRunning = errors.New("a reset of the workers is already running")
	// ErrStopRequested means the worker asked to be replaced instead of answering.
	// The pool retries the job once on another worker.
	ErrStopRequested = errors.New("worker asked to be replaced instead of answering")
	// ErrStreamTooLarge means Exec got a streamed answer larger than
	// MaxFrameSize; ExecStream reads one frame at a time.
	ErrStreamTooLarge = errors.New("streamed answer too large")
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

// StdoutError reports bytes on a worker's stdout that are not a frame. Stdout
// carries the protocol, so the command is not a RoadRunner worker, or it
// printed something: an echo, a PHP warning with display_errors on.
type StdoutError struct {
	Output []byte // the start of what the worker printed, at most stdoutLimit bytes
	Err    error  // what the relay reported: a *relay.ProtocolError or a broken pipe
}

// stdoutLimit bounds the output kept in a StdoutError.
const stdoutLimit = 256

func (e *StdoutError) Error() string {
	more := ""
	if len(e.Output) >= stdoutLimit {
		more = "..."
	}
	return fmt.Sprintf("stdout is reserved for the RoadRunner protocol, but the worker printed %q%s", e.Output, more)
}

func (e *StdoutError) Unwrap() error {
	return e.Err
}

// failed kills p, waits for it to exit and describes the failure.
func failed(p process, op string, err error) *ProcessError {
	p.Kill()
	<-p.Done()
	if out := strayOutput(p, err); len(out) > 0 {
		err = &StdoutError{Output: out, Err: err}
	}
	return &ProcessError{Pid: p.Pid(), Op: op, Err: err, Exit: p.Err(), Stderr: p.Stderr()}
}

// strayOutput returns what the exited process p printed to stdout outside the
// protocol, if err is what such output causes: a header the relay rejected,
// or a broken pipe from a process that printed and exited before it read a
// frame. Other errors leave frame bytes in the pipe, not stray output.
func strayOutput(p process, err error) []byte {
	if pe, ok := errors.AsType[*relay.ProtocolError](err); ok {
		// pe.Data is the header the relay read: the first bytes printed.
		return slices.Concat(pe.Data, p.UnreadStdout(stdoutLimit-len(pe.Data)))
	}
	if errors.Is(err, syscall.EPIPE) {
		return p.UnreadStdout(stdoutLimit)
	}
	return nil
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

// execute sends one request and reads the first frame of the response; more
// reports that the worker streams its answer and frames follow
// (receiveFrame). ttl, if positive, bounds the exchange: when it expires the
// process is killed.
//
// A nil error or a *JobError leaves the process in service. ErrStopRequested
// leaves it alive but due for a graceful stop. Any other error is a
// *ProcessError, and the process is gone.
func execute(p process, req Payload, ttl time.Duration) (resp Payload, more bool, err error) {
	return bounded(p, ttl, func() (Payload, bool, error) { return exchange(p.Conn(), req) })
}

// receiveFrame reads the next frame of a streamed response, with the same
// bound and errors as execute: ttl bounds each frame, as in RoadRunner.
func receiveFrame(p process, ttl time.Duration) (resp Payload, more bool, err error) {
	return bounded(p, ttl, func() (Payload, bool, error) { return receive(p.Conn()) })
}

// bounded runs an exchange with the worker, killing it if ttl expires first.
func bounded(p process, ttl time.Duration, exchange func() (Payload, bool, error)) (Payload, bool, error) {
	var timer *time.Timer
	if ttl > 0 {
		timer = time.AfterFunc(ttl, p.Kill)
	}

	resp, more, err := exchange()
	var jobErr *JobError
	switch {
	case timer != nil && !timer.Stop():
		// The kill is under way, whatever the worker answered.
		return Payload{}, false, failed(p, "exec", ErrExecTimeout)
	case err == nil, errors.As(err, &jobErr), errors.Is(err, ErrStopRequested):
		return resp, more, err
	default:
		return Payload{}, false, failed(p, "exec", err)
	}
}

func exchange(conn *relay.Conn, req Payload) (Payload, bool, error) {
	payload := make([]byte, 0, len(req.Context)+len(req.Body))
	payload = append(append(payload, req.Context...), req.Body...)
	err := conn.Send(&relay.Frame{
		Flags:   req.Codec & relay.CodecMask,
		Options: []uint32{uint32(len(req.Context))}, //nolint:gosec // the frame size check covers it
		Payload: payload,
	})
	if err != nil {
		return Payload{}, false, err
	}
	resp, more, err := receive(conn)
	if err == nil && !more && len(resp.Body) == 0 && bytes.Equal(resp.Context, stopRequest) {
		return Payload{}, false, ErrStopRequested
	}
	return resp, more, err
}

// receive reads one frame of a response and reports whether more frames of
// it follow (the STREAM bit). A frame with the PING bit is answered with a
// PONG frame, as RoadRunner does: a streaming worker pings every few frames
// and then checks for a STOP.
func receive(conn *relay.Conn) (Payload, bool, error) {
	var f relay.Frame
	if err := conn.Receive(&f); err != nil {
		return Payload{}, false, err
	}
	switch {
	case f.Flags.Has(relay.FlagError):
		return Payload{}, false, &JobError{Message: string(f.Payload)}
	case f.Flags.Has(relay.FlagControl):
		return Payload{}, false, fmt.Errorf("unexpected control frame %q", clip(f.Payload))
	case len(f.Options) != 1 || int(f.Options[0]) > len(f.Payload):
		return Payload{}, false, fmt.Errorf("invalid response: options %v for a %d-byte payload", f.Options, len(f.Payload))
	}
	if f.Stream.Has(relay.StreamPing) {
		if err := conn.Send(&relay.Frame{Stream: relay.StreamPong}); err != nil {
			return Payload{}, false, err
		}
	}
	resp := Payload{
		Codec:   f.Flags & relay.CodecMask,
		Context: f.Payload[:f.Options[0]],
		Body:    f.Payload[f.Options[0]:],
	}
	return resp, f.Stream.Has(relay.StreamMore), nil
}

// stopStream asks a streaming worker to stop (a STOP frame) and drops the
// frames it still sends, up to the last one, within budget. A worker that
// doesn't end the stream in time is killed, and the error is a
// *ProcessError.
func stopStream(p process, budget time.Duration) error {
	timer := time.AfterFunc(budget, p.Kill)
	err := p.Conn().Send(&relay.Frame{Stream: relay.StreamStop})
	for more := true; err == nil && more; {
		_, more, err = receive(p.Conn())
	}
	if !timer.Stop() {
		return failed(p, "exec", fmt.Errorf("stream not stopped within %s", budget))
	}
	if _, ok := errors.AsType[*JobError](err); err != nil && !ok {
		return failed(p, "exec", err)
	}
	return nil
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
