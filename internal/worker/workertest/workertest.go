// Package workertest provides fake worker processes that speak the worker
// side of the RoadRunner protocol, for tests of the worker package.
//
// A fake runs either in-process (Fake.Start, over net.Pipe, usable inside
// testing/synctest bubbles) or as a real process (RunProcess, over stdin and
// stdout). Both share one behavior:
//
//   - Options control the start: boot time, output before the handshake, a
//     failed boot, the reported pid, ignoring the stop frame.
//   - The body of each request is a command: "sleep 2s", "hang", "error msg",
//     "stop", "crash", "exit", "garbage", "stream", "big 1024", "memory 1024",
//     "no-options". Any other body is echoed: the response has the request's
//     context as header and its body as body.
package workertest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/relay"
)

// Options control how a fake worker starts.
type Options struct {
	BootTime    time.Duration `json:"boot_time,omitempty"`    // delay before answering the handshake
	BootOutput  string        `json:"boot_output,omitempty"`  // written to stdout before the handshake reply
	BootFailure string        `json:"boot_failure,omitempty"` // written to stderr; then exit 1 instead of the handshake
	BadPid      bool          `json:"bad_pid,omitempty"`      // report pid 0 in the handshake
	IgnoreStop  bool          `json:"ignore_stop,omitempty"`  // keep running after the stop frame, until killed
	AskStop     bool          `json:"ask_stop,omitempty"`     // answer every job with a request to be replaced
	Stderr      string        `json:"stderr,omitempty"`       // written to stderr at start
}

// Garbage is what the "garbage" command writes to stdout: what a PHP fatal
// error looks like with display_errors=STDOUT.
const Garbage = "PHP Fatal error:  Uncaught Error: Call to undefined function foo()\n"

// host is what a fake worker needs from its environment.
type host struct {
	pid    int
	out    io.Writer // raw stdout, for output that is not a frame
	stderr io.Writer
	sleep  func(time.Duration) // ends early when the process is killed
	hang   func()              // blocks until the process is killed
	setRSS func(int64)
}

// serve runs a fake worker on conn and returns its exit status.
func serve(conn *relay.Conn, opts Options, h host) int {
	if opts.Stderr != "" {
		fmt.Fprint(h.stderr, opts.Stderr)
	}
	h.sleep(opts.BootTime)
	if opts.BootFailure != "" {
		fmt.Fprintln(h.stderr, opts.BootFailure)
		return 1
	}

	// The handshake, as roadrunner/worker does it: a control frame with a
	// non-zero pid in, our pid out.
	var f relay.Frame
	if err := conn.Receive(&f); err != nil {
		return exitOnError(err, h)
	}
	if opts.BootOutput != "" {
		// A real worker prints this while booting, before it reads the
		// handshake; OS pipes buffer it. net.Pipe doesn't buffer, so the fake
		// reads the handshake first. kest sees the same bytes either way.
		_, _ = io.WriteString(h.out, opts.BootOutput)
		h.hang()
		return 1
	}
	var hello struct {
		Pid int `json:"pid"`
	}
	if !f.Flags.Has(relay.FlagControl) || json.Unmarshal(f.Payload, &hello) != nil || hello.Pid == 0 {
		fmt.Fprintf(h.stderr, "invalid handshake frame: flags %#x, payload %q\n", f.Flags, f.Payload)
		return 1
	}
	pid := h.pid
	if opts.BadPid {
		pid = 0
	}
	if err := conn.Send(&relay.Frame{Flags: relay.FlagControl, Payload: fmt.Appendf(nil, `{"pid":%d}`, pid)}); err != nil {
		return exitOnError(err, h)
	}

	for {
		var f relay.Frame
		if err := conn.Receive(&f); err != nil {
			return exitOnError(err, h)
		}
		if f.Flags.Has(relay.FlagControl) {
			if !strings.Contains(string(f.Payload), `"stop":true`) {
				fmt.Fprintf(h.stderr, "unknown control frame %q\n", f.Payload)
				return 1
			}
			if opts.IgnoreStop {
				h.hang()
			}
			return 0
		}
		if len(f.Options) != 1 || int(f.Options[0]) > len(f.Payload) {
			fmt.Fprintf(h.stderr, "invalid request options %v\n", f.Options)
			return 1
		}
		header, body := f.Payload[:f.Options[0]], f.Payload[f.Options[0]:]

		if opts.AskStop {
			body = []byte("stop")
		}
		resp, code, exit := handle(header, body, h)
		if exit {
			return code
		}
		if resp == nil {
			continue
		}
		if err := conn.Send(resp); err != nil {
			return exitOnError(err, h)
		}
	}
}

// handle runs one request. It returns the response to send, or exit = true
// with the exit status.
func handle(header, body []byte, h host) (resp *relay.Frame, code int, exit bool) {
	cmd, arg, _ := strings.Cut(string(body), " ")
	switch cmd {
	case "sleep":
		d, _ := time.ParseDuration(arg)
		h.sleep(d)
	case "hang":
		h.hang()
		return nil, 0, true
	case "error":
		return &relay.Frame{Flags: relay.FlagError, Payload: []byte(arg)}, 0, false
	case "stop":
		return response([]byte(`{"stop":true}`), nil), 0, false
	case "crash":
		fmt.Fprintln(h.stderr, "fake worker crashed")
		return nil, 1, true
	case "exit":
		return nil, 0, true
	case "garbage":
		_, _ = io.WriteString(h.out, Garbage)
		h.hang()
		return nil, 0, true
	case "stream":
		f := response(header, []byte("first chunk"))
		f.Stream = relay.StreamMore
		return f, 0, false
	case "big":
		n, _ := strconv.Atoi(arg)
		return response(header, bytes.Repeat([]byte{'x'}, n)), 0, false
	case "memory":
		n, _ := strconv.ParseInt(arg, 10, 64)
		h.setRSS(n)
	case "no-options":
		return &relay.Frame{Payload: body}, 0, false
	}
	return response(header, body), 0, false
}

func response(header, body []byte) *relay.Frame {
	return &relay.Frame{
		Options: []uint32{uint32(len(header))}, //nolint:gosec // test payloads are small
		Payload: append(bytes.Clone(header), body...),
	}
}

func exitOnError(err error, h host) int {
	if errors.Is(err, io.EOF) {
		return 0 // the server went away: exit quietly, like a PHP worker
	}
	fmt.Fprintf(h.stderr, "relay error: %v\n", err)
	return 1
}

// ErrKilled is the exit error of a fake process stopped with Kill.
var ErrKilled = errors.New("signal: killed")

// ExitError is the exit error of a fake process that exited with a non-zero status.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Fake starts in-process fake workers and keeps track of them.
type Fake struct {
	// Behave returns the options for the n-th process started, counting
	// from 0. Nil means default options for every process.
	Behave func(n int) Options

	mu      sync.Mutex
	started int
	procs   []*Process
}

// Start starts a fake worker process. maxPayload limits the frames the
// server side accepts from it.
func (f *Fake) Start(maxPayload int) *Process {
	f.mu.Lock()
	n := f.started
	f.started++
	var opts Options
	if f.Behave != nil {
		opts = f.Behave(n)
	}
	server, worker := net.Pipe()
	p := &Process{
		pid:    10000 + n,
		conn:   relay.NewConn(server, server, maxPayload),
		server: server,
		worker: worker,
		killed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	f.procs = append(f.procs, p)
	f.mu.Unlock()

	go p.run(opts)
	return p
}

// Started returns how many processes were started.
func (f *Fake) Started() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

// Running returns the processes that have not exited.
func (f *Fake) Running() []*Process {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Process
	for _, p := range f.procs {
		select {
		case <-p.done:
		default:
			out = append(out, p)
		}
	}
	return out
}

// Process returns the process with the given pid, or nil.
func (f *Fake) Process(pid int) *Process {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := pid - 10000; i >= 0 && i < len(f.procs) {
		return f.procs[i]
	}
	return nil
}

// Process is an in-process fake worker. Its method set matches the worker
// package's process interface.
type Process struct {
	pid    int
	conn   *relay.Conn
	server net.Conn // kest's side
	worker net.Conn // the fake's side
	rss    atomic.Int64

	killOnce sync.Once
	killed   chan struct{}
	done     chan struct{}
	err      error // set before done is closed

	mu     sync.Mutex
	stderr bytes.Buffer
}

func (p *Process) run(opts Options) {
	h := host{
		pid:    p.pid,
		out:    p.worker,
		stderr: writerFunc(p.writeStderr),
		sleep: func(d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-p.killed:
			}
		},
		hang:   func() { <-p.killed },
		setRSS: p.rss.Store,
	}
	code := serve(relay.NewConn(p.worker, p.worker, 64<<20), opts, h)

	select {
	case <-p.killed:
		p.err = ErrKilled
	default:
		if code != 0 {
			p.err = &ExitError{Code: code}
		}
	}
	// kest sees EOF on stdout, as with a real process.
	_ = p.worker.Close()
	close(p.done)
}

// Pid returns the fake pid, 10000 + the start index.
func (p *Process) Pid() int { return p.pid }

// Conn returns kest's side of the connection.
func (p *Process) Conn() *relay.Conn { return p.conn }

// Kill stops the fake at its next blocking point.
func (p *Process) Kill() {
	p.killOnce.Do(func() {
		close(p.killed)
		_ = p.worker.Close()
	})
}

// Done is closed once the fake has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err returns the exit error once the fake has exited.
func (p *Process) Err() error {
	<-p.done
	return p.err
}

// Stderr returns what the fake wrote to stderr.
func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(p.stderr.String())
}

// Close releases kest's side of the connection.
func (p *Process) Close() { _ = p.server.Close() }

// RSS returns the memory the fake reports, set by the "memory" command.
func (p *Process) RSS() int64 { return p.rss.Load() }

// SetRSS sets the memory the fake reports.
func (p *Process) SetRSS(n int64) { p.rss.Store(n) }

func (p *Process) writeStderr(b []byte) (int, error) {
	select {
	case <-p.killed:
		return len(b), nil // a killed process writes nothing more
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr.Write(b)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// OptionsEnv is the environment variable that passes Options to RunProcess,
// as JSON.
const OptionsEnv = "KEST_FAKE_WORKER_OPTIONS"

// RunProcess runs a fake worker on stdin and stdout and returns its exit
// status. It reads Options from OptionsEnv. "memory n" allocates and touches
// n bytes, so that the process's RSS grows for real.
func RunProcess() int {
	var opts Options
	if s := os.Getenv(OptionsEnv); s != "" {
		if err := json.Unmarshal([]byte(s), &opts); err != nil {
			fmt.Fprintf(os.Stderr, "bad %s: %v\n", OptionsEnv, err)
			return 2
		}
	}
	var ballast [][]byte
	h := host{
		pid:    os.Getpid(),
		out:    os.Stdout,
		stderr: os.Stderr,
		sleep:  time.Sleep,
		hang: func() {
			for {
				time.Sleep(time.Hour)
			}
		},
		setRSS: func(n int64) {
			b := make([]byte, n)
			for i := 0; i < len(b); i += 4096 {
				b[i] = 1
			}
			ballast = append(ballast, b)
		},
	}
	return serve(relay.NewConn(os.Stdin, os.Stdout, 64<<20), opts, h)
}

// EnvFor returns the OptionsEnv entry for opts.
func EnvFor(opts Options) string {
	b, err := json.Marshal(opts)
	if err != nil {
		panic(err)
	}
	return OptionsEnv + "=" + string(b)
}
