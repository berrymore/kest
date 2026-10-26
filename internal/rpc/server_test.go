package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/relay"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

// testPlugin answers the methods of the golden frames, and a few for tests.
type testPlugin struct {
	started chan struct{} // Wait sends once it runs
	release chan struct{} // Wait answers "done" once this is closed
	stopped chan error    // Wait sends its context's error when it gives up
}

func newTestPlugin() *testPlugin {
	return &testPlugin{started: make(chan struct{}, 1), release: make(chan struct{}), stopped: make(chan error, 1)}
}

func (p *testPlugin) Call(ctx context.Context, c plugin.Call) ([]byte, error) {
	switch c.Method {
	case "Echo":
		return c.Body, nil
	case "Raw":
		return append([]byte("raw:"), c.Body...), nil
	case "Fail":
		var s string
		if err := json.Unmarshal(c.Body, &s); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("boom: %s", s)
	case "Codec":
		return []byte(c.Codec.String()), nil
	case "Big":
		return make([]byte, 2<<10), nil
	case "Wait":
		p.started <- struct{}{}
		select {
		case <-p.release:
			return []byte("done"), nil
		case <-ctx.Done():
			p.stopped <- ctx.Err()
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%w %s", plugin.ErrUnknownMethod, c.Method)
}

func testConfig() Config {
	return Config{MaxConnections: 8, MaxFrameSize: 1 << 10, CallTimeout: time.Minute}
}

// startServer starts a server with the plugin "test" and stops it at the end
// of the test.
func startServer(t *testing.T, cfg Config, p plugin.Plugin) (*Server, *metrics.Registry) {
	t.Helper()
	reg := metrics.NewRegistry()
	s, err := New(cfg, map[string]plugin.Plugin{"test": p}, slog.New(slog.DiscardHandler), reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !stopped(s) {
			if err := s.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}
	})
	return s, reg
}

func stopped(s *Server) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// pipeServer serves one connection of the plugin "test" over net.Pipe,
// without a listener, so that it runs in a synctest bubble.
func pipeServer(t *testing.T, cfg Config, p plugin.Plugin) *client {
	t.Helper()
	s, err := New(cfg, map[string]plugin.Plugin{"test": p}, slog.New(slog.DiscardHandler), metrics.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	a, b := net.Pipe()
	if !s.track(b) {
		t.Fatal("track refused the connection")
	}
	s.done.Go(func() { s.serve(b) })
	t.Cleanup(func() {
		_ = a.Close()
		s.done.Wait()
		s.cancel()
	})
	return newClient(a)
}

type client struct {
	net.Conn
	conn *relay.Conn
	seq  uint32
}

func newClient(c net.Conn) *client {
	return &client{Conn: c, conn: relay.NewConn(c, c, 1<<20)}
}

func dial(t *testing.T, s *Server) *client {
	t.Helper()
	addr := s.addr // a unix listener's own address is the temporary name it was bound under
	if s.network == "tcp" {
		addr = s.ln.Addr().String()
	}
	var d net.Dialer
	c, err := d.DialContext(t.Context(), s.network, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return newClient(c)
}

// call sends a call frame and returns the answer.
func (c *client) call(t *testing.T, method string, flags relay.Flags, body string) *relay.Frame {
	t.Helper()
	c.send(t, method, flags, body)
	return c.receive(t)
}

func (c *client) send(t *testing.T, method string, flags relay.Flags, body string) {
	t.Helper()
	c.seq++
	err := c.conn.Send(&relay.Frame{Flags: flags, Options: []uint32{c.seq, uint32(len(method))},
		Payload: []byte(method + body)})
	if err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
}

func (c *client) receive(t *testing.T) *relay.Frame {
	t.Helper()
	var f relay.Frame
	if err := c.conn.Receive(&f); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(f.Options) != 2 || f.Options[0] != c.seq || int(f.Options[1]) > len(f.Payload) {
		t.Fatalf("answer options %v for seq %d, payload %q", f.Options, c.seq, f.Payload)
	}
	return &f
}

// closed reports whether the server closed the connection.
func (c *client) closed(t *testing.T) bool {
	t.Helper()
	var f relay.Frame
	err := c.conn.Receive(&f)
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

// body returns the answer after the method name, and whether it is an error.
func body(f *relay.Frame) (string, bool) {
	return string(f.Payload[f.Options[1]:]), f.Flags.Has(relay.FlagError)
}

// The answers match goridge's net/rpc codec byte for byte, for requests as
// PHP's goridge builds them.
func TestGoldenFrames(t *testing.T) {
	s, _ := startServer(t, testConfig(), newTestPlugin())
	f, err := os.Open("testdata/goridge_frames.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		name, req, want := fields[0], decodeHex(t, fields[1]), decodeHex(t, fields[2])
		t.Run(name, func(t *testing.T) {
			c := dial(t, s)
			if _, err := c.Write(req); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(want))
			if _, err := io.ReadFull(c, got); err != nil {
				t.Fatalf("read %d bytes: %v (got %x)", len(want), err, got)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("answer\n got %x\nwant %x", got, want)
			}
		})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCalls(t *testing.T) {
	s, reg := startServer(t, testConfig(), newTestPlugin())
	c := dial(t, s)
	tests := []struct {
		method string
		flags  relay.Flags
		body   string
		want   string
		failed bool
		answer relay.Flags // the codec bits of the answer
	}{
		{"test.Echo", relay.CodecJSON, `{"a":1}`, `{"a":1}`, false, relay.CodecJSON},
		{"test.Codec", relay.CodecProto | relay.CodecJSON, "", "protobuf", false, relay.CodecProto},
		{"test.Codec", relay.CodecJSON | relay.CodecRaw, "", "json", false, relay.CodecJSON},
		{"test.Codec", codecMsgpack, "", "msgpack", false, codecMsgpack},
		{"test.Codec", 0, "", "gob", false, 0x20},
		{"test.Fail", relay.CodecJSON, `"x"`, "boom: x", true, relay.CodecJSON},
		{"test.Nope", relay.CodecRaw, "", "rpc: can't find method test.Nope", true, relay.CodecRaw},
		{"nope.Echo", relay.CodecJSON, "", "rpc: can't find service nope.Echo", true, relay.CodecJSON},
		{"Echo", relay.CodecJSON, "", "rpc: service/method request ill-formed: Echo", true, relay.CodecJSON},
		{"test.Big", relay.CodecRaw, "", "rpc: the answer of test.Big is 2048 bytes, over rpc.max_frame_size", true,
			relay.CodecRaw},
	}
	for _, tt := range tests {
		f := c.call(t, tt.method, tt.flags, tt.body)
		got, failed := body(f)
		if got != tt.want || failed != tt.failed || f.Flags&relay.CodecMask != tt.answer {
			t.Errorf("%s with %#x = %q, error %v, flags %#x; want %q, error %v, codec %#x", tt.method, tt.flags, got,
				failed, f.Flags, tt.want, tt.failed, tt.answer)
		}
	}

	for _, m := range []struct {
		plugin, result string
		want           float64
	}{{"test", "ok", 5}, {"test", "error", 2}, {"test", "unknown", 1}, {"", "unknown", 2}} {
		if got := metricstest.Value(t, reg, "kest_rpc_calls_total", "plugin", m.plugin, "result", m.result); got != m.want {
			t.Errorf("kest_rpc_calls_total{plugin=%q,result=%q} = %v, want %v", m.plugin, m.result, got, m.want)
		}
	}
	if got := metricstest.Value(t, reg, "kest_rpc_connections"); got != 1 {
		t.Errorf("kest_rpc_connections = %v, want 1", got)
	}
}

// A frame that isn't a call, or is over max_frame_size, closes the
// connection, as in goridge; other connections go on.
func TestBadFrames(t *testing.T) {
	s, _ := startServer(t, testConfig(), newTestPlugin())
	for name, f := range map[string]*relay.Frame{
		"one option":          {Flags: relay.CodecJSON, Options: []uint32{1}, Payload: []byte("test.Echo")},
		"method past payload": {Flags: relay.CodecJSON, Options: []uint32{1, 20}, Payload: []byte("test.Echo")},
		"too large":           {Flags: relay.CodecRaw, Options: []uint32{1, 9}, Payload: make([]byte, 2<<10)},
	} {
		t.Run(name, func(t *testing.T) {
			c := dial(t, s)
			if err := c.conn.Send(f); err != nil {
				t.Fatal(err)
			}
			if !c.closed(t) {
				t.Error("the connection is still open")
			}
		})
	}
	if got, _ := body(dial(t, s).call(t, "test.Echo", relay.CodecRaw, "ok")); got != "ok" {
		t.Errorf("a new connection answers %q", got)
	}
}

func TestMaxConnections(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConnections = 1
	s, reg := startServer(t, cfg, newTestPlugin())
	first := dial(t, s)
	first.call(t, "test.Echo", relay.CodecRaw, "") // the server has it now
	if second := dial(t, s); !second.closed(t) {
		t.Error("a connection over max_connections stays open")
	}
	if got := metricstest.Value(t, reg, "kest_rpc_refused_connections_total"); got != 1 {
		t.Errorf("kest_rpc_refused_connections_total = %v, want 1", got)
	}
	if got, _ := body(first.call(t, "test.Echo", relay.CodecRaw, "still")); got != "still" {
		t.Errorf("the first connection answers %q", got)
	}
}

// A call over call_timeout answers an error, and the connection goes on.
func TestCallTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.CallTimeout = 2 * time.Second
		c := pipeServer(t, cfg, newTestPlugin())
		start := time.Now()
		got, failed := body(c.call(t, "test.Wait", relay.CodecRaw, ""))
		if want := "rpc: test.Wait timed out after 2s (rpc.call_timeout)"; got != want || !failed {
			t.Errorf("answer %q, error %v; want %q", got, failed, want)
		}
		if d := time.Since(start); d != 2*time.Second {
			t.Errorf("answered after %v, want 2s", d)
		}
		if got, _ := body(c.call(t, "test.Echo", relay.CodecRaw, "next")); got != "next" {
			t.Errorf("the next call answers %q", got)
		}
	})
}

// Stop closes idle connections at once, lets a call in progress answer,
// and removes the private socket and its directory.
func TestStopWaitsForCalls(t *testing.T) {
	p := newTestPlugin()
	s, _ := startServer(t, testConfig(), p)
	dir := s.privateDir
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != privateDirMode {
		t.Fatalf("private directory: %v, %v", info, err)
	}
	if want := "unix://" + filepath.Join(dir, privateSocket); s.Address() != want {
		t.Errorf("Address = %q, want %q", s.Address(), want)
	}

	busy, idle := dial(t, s), dial(t, s)
	idle.call(t, "test.Echo", relay.CodecRaw, "")
	busy.send(t, "test.Wait", relay.CodecRaw, "")
	<-p.started

	stopErr := make(chan error, 1)
	go func() { stopErr <- s.Stop(context.Background()) }()
	if !idle.closed(t) {
		t.Error("an idle connection stays open after Stop")
	}
	select {
	case err := <-stopErr:
		t.Fatalf("Stop returned during a call: %v", err)
	default:
	}
	close(p.release)
	if got, failed := body(busy.receive(t)); got != "done" || failed {
		t.Errorf("the call in progress answers %q, error %v", got, failed)
	}
	if !busy.closed(t) {
		t.Error("the busy connection stays open after its answer")
	}
	if err := <-stopErr; err != nil {
		t.Errorf("Stop: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the private directory is still there: %v", err)
	}
}

// When Stop's context ends first, the calls are canceled.
func TestStopDeadline(t *testing.T) {
	p := newTestPlugin()
	s, _ := startServer(t, testConfig(), p)
	c := dial(t, s)
	c.send(t, "test.Wait", relay.CodecRaw, "")
	<-p.started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := <-p.stopped; !errors.Is(err, context.Canceled) {
		t.Errorf("the plugin's context ended with %v, want canceled", err)
	}
}

func TestExplicitAddresses(t *testing.T) {
	t.Run("unix", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "kest-rpc-") //nolint:usetesting // t.TempDir's paths can pass the socket path limit on macOS
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "rpc.sock")
		cfg := testConfig()
		cfg.Address, cfg.SocketMode = "unix:"+path, 0o640
		s, _ := startServer(t, cfg, newTestPlugin())
		if s.Address() != "unix://"+path {
			t.Errorf("Address = %q", s.Address())
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("socket file: %v, %v", info, err)
		}
		dial(t, s).call(t, "test.Echo", relay.CodecRaw, "")
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the socket file is still there: %v", err)
		}
	})
	t.Run("tcp", func(t *testing.T) {
		cfg := testConfig()
		cfg.Address = "127.0.0.1:0"
		s, _ := startServer(t, cfg, newTestPlugin())
		if s.Address() != "tcp://127.0.0.1:0" {
			t.Errorf("Address = %q", s.Address())
		}
		if got, _ := body(dial(t, s).call(t, "test.Echo", relay.CodecRaw, "tcp")); got != "tcp" {
			t.Errorf("answer %q", got)
		}
	})
}

func TestNewErrors(t *testing.T) {
	for addr, want := range map[string]string{
		"unix:run/rpc.sock":  `socket path "run/rpc.sock" is not absolute`,
		"unix:/run/a:b.sock": `socket path "/run/a:b.sock" has a ':'`,
	} {
		cfg := testConfig()
		cfg.Address = addr
		_, err := New(cfg, nil, slog.New(slog.DiscardHandler), metrics.NewRegistry())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("New(%q) = %v, want %q", addr, err, want)
		}
	}
}
