package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/plugin/wire"
	"github.com/berrymore/kest/internal/relay"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

func TestServeNotUnderKest(t *testing.T) {
	t.Setenv(wire.EnvFD, "")
	if err := Serve(nil); !errors.Is(err, ErrNotUnderKest) {
		t.Errorf("Serve = %v, want ErrNotUnderKest", err)
	}
	t.Setenv(wire.EnvFD, "x")
	if err := Serve(nil); err == nil || !strings.Contains(err.Error(), "not a file descriptor") {
		t.Errorf("Serve = %v", err)
	}
}

// echo answers Echo; Wait waits for its context.
type echo struct {
	setup   Setup
	started bool
	stopped atomic.Bool
	waiting chan struct{}
}

func (e *echo) Start(context.Context) error { e.started = true; return nil }
func (e *echo) Stop(context.Context) error  { e.stopped.Store(true); return nil }

func (e *echo) Call(ctx context.Context, c Call) ([]byte, error) {
	switch c.Method {
	case "Echo":
		return append([]byte(c.Codec.String()+":"), c.Body...), nil
	case "Wait":
		close(e.waiting)
		<-ctx.Done()
		return nil, ctx.Err()
	case "Big":
		return make([]byte, 100), nil
	}
	return nil, ErrUnknownMethod
}

// kest is the kest end of a pipe to serve.
type kest struct {
	t    *testing.T
	conn *relay.Conn
	done chan error // serve's result
}

func startServe(t *testing.T, hello wire.Hello, newPlugin func(Setup) (Plugin, error)) *kest {
	t.Helper()
	a, b := net.Pipe()
	k := &kest{t: t, conn: relay.NewConn(a, a, 1<<20), done: make(chan error, 1)}
	go func() { k.done <- serve(t.Context(), b, newPlugin) }()
	t.Cleanup(func() { _ = a.Close() })
	k.control(hello)
	return k
}

func (k *kest) control(v any) {
	k.t.Helper()
	f, err := wire.ControlFrame(v)
	if err == nil {
		err = k.conn.Send(f)
	}
	if err != nil {
		k.t.Fatal(err)
	}
}

func (k *kest) receive() *relay.Frame {
	k.t.Helper()
	var f relay.Frame
	if err := k.conn.Receive(&f); err != nil {
		k.t.Fatal(err)
	}
	return &f
}

func (k *kest) call(id uint32, method, body string) {
	k.t.Helper()
	if err := k.conn.Send((&wire.Call{ID: id, Method: method, Codec: relay.CodecRaw, Body: []byte(body)}).Frame()); err != nil {
		k.t.Fatal(err)
	}
}

func (k *kest) answer() *wire.Call {
	k.t.Helper()
	c, err := wire.ParseCall(k.receive())
	if err != nil {
		k.t.Fatal(err)
	}
	return c
}

func TestServe(t *testing.T) {
	p := &echo{waiting: make(chan struct{})}
	k := startServe(t, wire.Hello{Protocol: wire.Version, Name: "ext", Config: json.RawMessage(`{"a":1}`),
		MaxFrameSize: 64}, func(s Setup) (Plugin, error) { p.setup = s; return p, nil })

	if f := k.receive(); !f.Flags.Has(relay.FlagControl) || string(f.Payload) != `{"protocol":1}` {
		t.Fatalf("handshake answer %+v %q", f, f.Payload)
	}
	if p.setup.Name != "ext" || string(p.setup.Config) != `{"a":1}` || !p.started {
		t.Errorf("setup %+v, started %v", p.setup, p.started)
	}

	k.call(7, "Wait", "")
	<-p.waiting
	k.call(8, "Echo", "hi")
	if a := k.answer(); a.ID != 8 || a.Failed || string(a.Body) != "raw:hi" || a.Method != "Echo" || a.Codec != relay.CodecRaw {
		t.Errorf("Echo answer %+v", a)
	}
	k.call(9, "Nope", "")
	if a := k.answer(); a.ID != 9 || !a.Failed || a.Code != wire.CodeUnknownMethod {
		t.Errorf("unknown method answer %+v", a)
	}
	k.call(10, "Big", "")
	if a := k.answer(); !a.Failed || !strings.Contains(string(a.Body), "the answer is 100 bytes, over the max_frame_size of 64") {
		t.Errorf("Big answer %+v %q", a, a.Body)
	}
	id := uint32(7)
	k.control(wire.Control{Cancel: &id})
	if a := k.answer(); a.ID != 7 || !a.Failed || string(a.Body) != context.Canceled.Error() {
		t.Errorf("canceled answer %+v %q", a, a.Body)
	}

	k.control(wire.Control{Stop: true})
	if err := <-k.done; err != nil {
		t.Errorf("serve = %v after stop", err)
	}
	if !p.stopped.Load() {
		t.Error("the plugin's Stop didn't run")
	}
}

func TestServeRefuses(t *testing.T) {
	tests := []struct {
		name      string
		hello     wire.Hello
		newPlugin func(Setup) (Plugin, error)
		want      string
	}{
		{"old kest", wire.Hello{Protocol: 0}, nil, "kest speaks plugin protocol 0, this plugin needs 1"},
		{"bad config", wire.Hello{Protocol: 1}, func(Setup) (Plugin, error) { return nil, errors.New("bad config") },
			"bad config"},
	}
	for _, tt := range tests {
		k := startServe(t, tt.hello, tt.newPlugin)
		f := k.receive()
		if !f.Flags.Has(relay.FlagControl|relay.FlagError) || string(f.Payload) != tt.want {
			t.Errorf("%s: answer %+v %q, want an error %q", tt.name, f, f.Payload, tt.want)
		}
		if err := <-k.done; err == nil || err.Error() != tt.want {
			t.Errorf("%s: serve = %v", tt.name, err)
		}
	}
}

// When kest goes away, serve ends with an error and the plugin stops.
func TestServeKestGone(t *testing.T) {
	p := &echo{waiting: make(chan struct{})}
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(t.Context(), b, func(Setup) (Plugin, error) { return p, nil }) }()
	conn := relay.NewConn(a, a, 1<<20)
	f, _ := wire.ControlFrame(wire.Hello{Protocol: 1, MaxFrameSize: 1 << 10})
	if err := conn.Send(f); err != nil {
		t.Fatal(err)
	}
	var ready relay.Frame
	if err := conn.Receive(&ready); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "kest closed the connection") {
		t.Errorf("serve = %v", err)
	}
	if !p.stopped.Load() {
		t.Error("the plugin's Stop didn't run")
	}
}
