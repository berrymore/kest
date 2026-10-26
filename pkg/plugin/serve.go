package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/berrymore/kest/internal/plugin/wire"
	"github.com/berrymore/kest/internal/relay"
)

// Setup is what a plugin starts with.
type Setup struct {
	Name   string          // the plugin's name in kest's config: what PHP calls
	Config json.RawMessage // plugin_config from kest's config, as JSON; empty without one
}

// ErrNotUnderKest is what Serve returns in a program that kest didn't start.
var ErrNotUnderKest = errors.New("this program is a kest plugin: kest starts it (plugins[].type: external), " +
	"it doesn't run on its own")

// readLimit bounds the frames the plugin reads. kest sends at most the
// max_frame_size it announces, so this only guards against a broken stream.
const readLimit = 1 << 30

// Serve runs the plugin that newPlugin makes, on the socket kest passed,
// until kest stops it; then it returns nil. Call it from main, and exit with
// a non-zero status when it returns an error.
//
// newPlugin gets the plugin's name and config; an error it returns fails the
// start, and kest shows the message. If the plugin implements Start(ctx)
// error and Stop(ctx) error, Start runs before the first call and Stop after
// the last. Calls run concurrently.
//
// Write logs to stderr, or to stdout: kest logs both, line by line.
func Serve(newPlugin func(Setup) (Plugin, error)) error {
	s := os.Getenv(wire.EnvFD)
	if s == "" {
		return ErrNotUnderKest
	}
	fd, err := strconv.Atoi(s)
	if err != nil || fd < 3 {
		return fmt.Errorf("%s=%q is not a file descriptor", wire.EnvFD, s)
	}
	f := os.NewFile(uintptr(fd), "kest")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("the socket from kest: %w", err)
	}
	return serve(context.Background(), conn, newPlugin)
}

type starter interface {
	Start(ctx context.Context) error
}

type stopper interface {
	Stop(ctx context.Context) error
}

// server answers kest's calls on one connection.
type server struct {
	conn     *relay.Conn
	plugin   Plugin
	maxFrame int

	wmu   sync.Mutex // conn.Send
	mu    sync.Mutex
	calls map[uint32]context.CancelFunc // the calls in progress
	wg    sync.WaitGroup
}

func serve(ctx context.Context, rwc io.ReadWriteCloser, newPlugin func(Setup) (Plugin, error)) error {
	defer func() { _ = rwc.Close() }()
	conn := relay.NewConn(rwc, rwc, readLimit)

	var f relay.Frame
	if err := conn.Receive(&f); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	var hello wire.Hello
	if !f.Flags.Has(relay.FlagControl) {
		return errors.New("handshake: the first frame from kest is not a CONTROL frame")
	}
	if err := json.Unmarshal(f.Payload, &hello); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	refuse := func(err error) error {
		_ = conn.Send(wire.ErrorFrame(err.Error()))
		return err
	}
	if hello.Protocol < wire.Version {
		return refuse(fmt.Errorf("kest speaks plugin protocol %d, this plugin needs %d", hello.Protocol, wire.Version))
	}
	p, err := newPlugin(Setup{Name: hello.Name, Config: hello.Config})
	if err != nil {
		return refuse(err)
	}
	if s, ok := p.(starter); ok {
		if err := s.Start(ctx); err != nil {
			return refuse(err)
		}
	}
	ready, err := wire.ControlFrame(wire.Ready{Protocol: wire.Version})
	if err != nil {
		return err
	}
	if err := conn.Send(ready); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	s := &server{conn: conn, plugin: p, maxFrame: hello.MaxFrameSize, calls: make(map[uint32]context.CancelFunc)}
	err = s.loop(ctx)
	s.wg.Wait()
	if st, ok := p.(stopper); ok {
		err = errors.Join(err, st.Stop(context.WithoutCancel(ctx)))
	}
	return err
}

// loop reads kest's frames until kest stops the plugin (nil) or the
// connection fails.
func (s *server) loop(ctx context.Context) error {
	defer s.cancelAll()
	for {
		var f relay.Frame
		if err := s.conn.Receive(&f); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("kest closed the connection")
			}
			return err
		}
		if f.Flags.Has(relay.FlagControl) {
			var c wire.Control
			if err := json.Unmarshal(f.Payload, &c); err != nil {
				return fmt.Errorf("a control frame from kest: %w", err)
			}
			if c.Stop {
				return nil
			}
			if c.Cancel != nil {
				s.cancel(*c.Cancel)
			}
			continue
		}
		c, err := wire.ParseCall(&f)
		if err != nil {
			return err
		}
		callCtx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.calls[c.ID] = cancel
		s.mu.Unlock()
		s.wg.Go(func() {
			s.answer(callCtx, c)
			s.mu.Lock()
			delete(s.calls, c.ID)
			s.mu.Unlock()
			cancel()
		})
	}
}

func (s *server) cancel(id uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.calls[id]; cancel != nil {
		cancel()
	}
}

func (s *server) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.calls {
		cancel()
	}
}

// answer runs a call and sends its answer. A panic in the plugin fails the
// call, with the stack on stderr.
func (s *server) answer(ctx context.Context, c *wire.Call) {
	out := &wire.Call{ID: c.ID, Method: c.Method, Codec: c.Codec}
	body, err := s.call(ctx, c)
	switch {
	case errors.Is(err, ErrUnknownMethod):
		out.Failed, out.Code, out.Body = true, wire.CodeUnknownMethod, []byte(err.Error())
	case err != nil:
		out.Failed, out.Body = true, []byte(err.Error())
	case len(c.Method)+len(body) > s.maxFrame:
		out.Failed, out.Body = true, fmt.Appendf(nil, "the answer is %d bytes, over the max_frame_size of %d",
			len(body), s.maxFrame)
	default:
		out.Body = body
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.Send(out.Frame()) // a broken connection ends the loop
}

func (s *server) call(ctx context.Context, c *wire.Call) (body []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic in %s: %v\n%s", c.Method, r, debug.Stack())
			err = fmt.Errorf("the plugin panicked in %s: %v", c.Method, r)
		}
	}()
	return s.plugin.Call(ctx, Call{Method: c.Method, Codec: Codec(c.Codec), Body: c.Body})
}
