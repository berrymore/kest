package phpworkers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

// fakePool answers every job with exec and records the last request.
type fakePool struct {
	exec     func(ctx context.Context, req worker.Payload) (worker.Payload, error)
	stream   *fakeStream // answers instead of exec when set
	startErr error
	health   worker.Health
	workers  []worker.Info
	waiting  int
	resetErr error
	laneErr  error
	lanes    []worker.LaneConfig // added with Lane
	last     worker.Payload
	lastLane string // the lane of the last request, "" for the default one
	calls    int
	started  bool
	stopped  bool
}

func (p *fakePool) Start(context.Context) error { p.started = true; return p.startErr }
func (p *fakePool) Stop(context.Context) error  { p.stopped = true; return nil }
func (p *fakePool) Health() worker.Health       { return p.health }
func (p *fakePool) Workers() []worker.Info      { return p.workers }
func (p *fakePool) Waiting() int                { return p.waiting }
func (p *fakePool) Reset(context.Context) error { return p.resetErr }

func (p *fakePool) Lane(cfg worker.LaneConfig) (lane, error) {
	if p.laneErr != nil {
		return nil, p.laneErr
	}
	p.lanes = append(p.lanes, cfg)
	return fakeLane{p: p, name: cfg.Name}, nil
}

func (p *fakePool) Lanes() []worker.LaneInfo {
	out := []worker.LaneInfo{{Queued: p.waiting}}
	for _, cfg := range p.lanes {
		out = append(out, worker.LaneInfo{LaneConfig: cfg, Busy: 1})
	}
	return out
}

func (p *fakePool) ExecStream(ctx context.Context, req worker.Payload) (worker.Payload, frames, error) {
	return p.execIn(ctx, "", req)
}

// fakeLane runs its requests on its pool, marked with its name.
type fakeLane struct {
	p    *fakePool
	name string
}

func (l fakeLane) ExecStream(ctx context.Context, req worker.Payload) (worker.Payload, frames, error) {
	return l.p.execIn(ctx, l.name, req)
}

func (p *fakePool) execIn(ctx context.Context, lane string, req worker.Payload) (worker.Payload, frames, error) {
	p.calls++
	p.last, p.lastLane = req, lane
	if p.stream != nil {
		return p.stream.first, p.stream, nil
	}
	resp, err := p.exec(ctx, req)
	return resp, nil, err
}

// fakeStream is a streamed answer: first, then rest, then err (or the end).
type fakeStream struct {
	first  worker.Payload
	rest   []worker.Payload
	err    error
	closed atomic.Bool // set by the server's goroutine
}

func (s *fakeStream) Next(context.Context) (worker.Payload, bool, error) {
	if len(s.rest) > 0 {
		f := s.rest[0]
		s.rest = s.rest[1:]
		return f, true, nil
	}
	return worker.Payload{}, false, s.err
}

func (s *fakeStream) Close() { s.closed.Store(true) }

func respond(codec relay.Flags, header, body string) func(context.Context, worker.Payload) (worker.Payload, error) {
	return func(context.Context, worker.Payload) (worker.Payload, error) {
		return worker.Payload{Codec: codec, Context: []byte(header), Body: []byte(body)}, nil
	}
}

func fail(err error) func(context.Context, worker.Payload) (worker.Payload, error) {
	return func(context.Context, worker.Payload) (worker.Payload, error) { return worker.Payload{}, err }
}

func newTestCluster(tb testing.TB, mutate func(*Config), p *fakePool) *Cluster {
	tb.Helper()
	cfg := DefaultConfig()
	cfg.Command = []string{"php"}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		tb.Fatal(err)
	}
	return newCluster(&cfg, "", p, logging.Discard())
}

func serve(c *Cluster, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, r)
	return rec
}

func TestServeHTTP(t *testing.T) {
	p := &fakePool{exec: respond(relay.CodecJSON, `{"status":201,"headers":{"X-App":["php"],"Set-Cookie":["a=1","b=2"]}}`, "created")}
	c := newTestCluster(t, nil, p)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com/items?x=1", strings.NewReader("a=1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := serve(c, r)

	if rec.Code != http.StatusCreated || rec.Body.String() != "created" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 2 || rec.Header().Get("X-App") != "php" {
		t.Errorf("headers %v", rec.Header())
	}
	if p.last.Codec != relay.CodecJSON || string(p.last.Body) != `{"a":"1"}` {
		t.Errorf("request payload: codec %#x body %q", p.last.Codec, p.last.Body)
	}
	var ctx map[string]any
	if err := json.Unmarshal(p.last.Context, &ctx); err != nil {
		t.Fatal(err)
	}
	if ctx["method"] != "POST" || ctx["uri"] != "http://example.com/items?x=1" || ctx["parsed"] != true {
		t.Errorf("request context %v", ctx)
	}
}

func jsonFrame(header, body string) worker.Payload {
	return worker.Payload{Codec: relay.CodecJSON, Context: []byte(header), Body: []byte(body)}
}

// A streamed answer: early hints as a 1xx response, then the first final
// frame's status and headers, then every chunk, flushed.
func TestServeHTTPStream(t *testing.T) {
	stream := &fakeStream{
		first: jsonFrame(`{"status":103,"headers":{"Link":["</app.css>; rel=preload"]}}`, ""),
		rest: []worker.Payload{
			jsonFrame(`{"status":200,"headers":{"Content-Type":["text/event-stream"],"Content-Length":["5"]}}`, "data: 1\n\n"),
			jsonFrame(`{"status":500}`, "data: 2\n\n"), // later heads are ignored
			jsonFrame("", "data: 3\n\n"),
		},
	}
	c := newTestCluster(t, nil, &fakePool{stream: stream})
	srv := httptest.NewServer(c)
	defer srv.Close()

	var hints []string
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		hints = append(hints, fmt.Sprintf("%d %s", code, h.Get("Link")))
		return nil
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "data: 1\n\ndata: 2\n\ndata: 3\n\n" {
		t.Errorf("response %d %q, %v", resp.StatusCode, body, err)
	}
	if resp.ContentLength != -1 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Link") != "" {
		t.Errorf("headers %v (length %d): want the final frame's, chunked", resp.Header, resp.ContentLength)
	}
	if len(hints) != 1 || hints[0] != "103 </app.css>; rel=preload" {
		t.Errorf("informational responses %q, want the early hints", hints)
	}
	if !stream.closed.Load() {
		t.Error("the stream was not closed")
	}
}

func TestServeHTTPStreamErrors(t *testing.T) {
	head := jsonFrame(`{"status":200}`, "partial")
	for _, tt := range []struct {
		name   string
		stream *fakeStream
		status int // before the header went out
		abort  bool
	}{
		{"error before the final frame", &fakeStream{first: jsonFrame(`{"status":103}`, ""),
			err: &worker.ProcessError{Pid: 1, Op: "exec", Err: io.EOF}}, http.StatusBadGateway, false},
		{"only informational frames", &fakeStream{first: jsonFrame(`{"status":103}`, "")}, http.StatusBadGateway, false},
		{"invalid status", &fakeStream{first: jsonFrame(`{"status":101}`, "")}, http.StatusBadGateway, false},
		{"error after the header", &fakeStream{first: head, err: &worker.ProcessError{Pid: 1, Op: "exec", Err: io.EOF}}, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(newTestCluster(t, nil, &fakePool{stream: tt.stream}))
			defer srv.Close()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if aborted := err != nil; aborted != tt.abort {
				t.Errorf("cut off = %v (%v), want %v", aborted, err, tt.abort)
			}
			if !tt.abort && resp.StatusCode != tt.status {
				t.Errorf("status %d, want %d", resp.StatusCode, tt.status)
			}
			if !tt.stream.closed.Load() {
				t.Error("the stream was not closed")
			}
		})
	}
}

func TestServeHTTPStreamHead(t *testing.T) {
	stream := &fakeStream{first: jsonFrame(`{"status":200}`, "chunk"), rest: []worker.Payload{jsonFrame("", "more")}}
	c := newTestCluster(t, nil, &fakePool{stream: stream})
	rec := serve(c, httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/", http.NoBody))
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

// What a trusted proxy said about the client (httpx.RequestInfo, from
// client_ip) reaches PHP as remoteAddr and the URI's scheme.
func TestServeHTTPClientFromProxy(t *testing.T) {
	p := &fakePool{exec: respond(relay.CodecJSON, `{"status":200}`, "")}
	c := newTestCluster(t, nil, p)
	r := httpx.WithRequestInfo(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/a", http.NoBody))
	info := httpx.Info(r)
	info.ClientIP, info.Scheme = "198.51.100.7", "https"
	serve(c, r)
	var ctx map[string]any
	if err := json.Unmarshal(p.last.Context, &ctx); err != nil {
		t.Fatal(err)
	}
	if ctx["remoteAddr"] != "198.51.100.7" || ctx["uri"] != "https://example.com/a" {
		t.Errorf("remoteAddr %v, uri %v; want the client's IP and an https URI", ctx["remoteAddr"], ctx["uri"])
	}
}

func TestServeHTTPProto(t *testing.T) {
	p := &fakePool{exec: respond(relay.CodecProto, "\x08\xc8\x01", "ok")} // status 200
	c := newTestCluster(t, func(c *Config) { c.Codec = CodecProto }, p)
	rec := serve(c, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if p.last.Codec != relay.CodecProto || !strings.Contains(string(p.last.Context), "GET") {
		t.Errorf("request payload: codec %#x context %q", p.last.Codec, p.last.Context)
	}
}

func TestServeHTTPErrors(t *testing.T) {
	tests := []struct {
		name   string
		exec   func(context.Context, worker.Payload) (worker.Payload, error)
		body   string
		ctype  string
		status int
		calls  int
	}{
		{"queue full", fail(worker.ErrQueueFull), "", "", 503, 1},
		{"no worker", fail(worker.ErrNoWorker), "", "", 503, 1},
		{"stopped", fail(worker.ErrStopped), "", "", 503, 1},
		{"exec timeout", fail(&worker.ProcessError{Op: "exec", Err: worker.ErrExecTimeout}), "", "", 504, 1},
		{"crash", fail(&worker.ProcessError{Op: "exec", Err: io.EOF}), "", "", 502, 1},
		{"stop requested twice", fail(worker.ErrStopRequested), "", "", 502, 1},
		{"soft error", fail(&worker.JobError{Message: "Uncaught Exception"}), "", "", 500, 1},
		{"invalid response", respond(relay.CodecJSON, `{"status":"x"}`, ""), "", "", 502, 1},
		{"invalid status", respond(relay.CodecJSON, `{"status":101}`, ""), "", "", 502, 1},
		{"body too large", respond(relay.CodecJSON, `{"status":200}`, ""), strings.Repeat("x", 11), "text/plain", 413, 0},
		{"bad form", respond(relay.CodecJSON, `{"status":200}`, ""), "a=%zz", "application/x-www-form-urlencoded", 400, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePool{exec: tt.exec}
			c := newTestCluster(t, func(c *Config) { c.MaxRequestBody = 10 }, p)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.ctype != "" {
				r.Header.Set("Content-Type", tt.ctype)
			}
			rec := serve(c, r)
			if rec.Code != tt.status || p.calls != tt.calls {
				t.Errorf("status %d after %d calls, want %d after %d", rec.Code, p.calls, tt.status, tt.calls)
			}
			if want := http.StatusText(tt.status) + "\n"; rec.Body.String() != want {
				t.Errorf("body %q, want %q", rec.Body.String(), want)
			}
		})
	}
}

// waitPool blocks every job until the caller's context ends, as the real pool
// does when its caller gives up.
func waitPool() *fakePool {
	return &fakePool{exec: func(ctx context.Context, _ worker.Payload) (worker.Payload, error) {
		<-ctx.Done()
		return worker.Payload{}, ctx.Err()
	}}
}

func TestServeHTTPRouteTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCluster(t, nil, waitPool())
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		rec := serve(c, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody))
		if rec.Code != http.StatusGatewayTimeout {
			t.Errorf("status %d, want 504", rec.Code)
		}
	})
}

// recorder notes whether anything was written.
type recorder struct {
	header http.Header
	wrote  bool
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { r.wrote = true; return len(b), nil }
func (r *recorder) WriteHeader(int)             { r.wrote = true }

func TestServeHTTPClientGone(t *testing.T) {
	c := newTestCluster(t, nil, waitPool())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := &recorder{header: http.Header{}}
	c.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody))
	if rec.wrote {
		t.Error("wrote a response to a client that went away")
	}
}

func TestStartStop(t *testing.T) {
	p := &fakePool{}
	c := newTestCluster(t, nil, p)
	if err := c.Start(t.Context()); err != nil || !p.started {
		t.Errorf("Start: %v, started %v", err, p.started)
	}
	if err := c.Stop(t.Context()); err != nil || !p.stopped {
		t.Errorf("Stop: %v, stopped %v", err, p.stopped)
	}
}

func TestStartErrors(t *testing.T) {
	printed := &worker.ProcessError{Pid: 1, Op: "handshake", Err: &worker.StdoutError{Output: []byte("<!DOCTYPE html>")}}
	tests := []struct {
		name     string
		err      error
		wantHint bool
	}{
		{name: "output on stdout", err: fmt.Errorf("start workers: %w", printed), wantHint: true},
		{name: "other", err: errors.New("start workers: boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCluster(t, nil, &fakePool{startErr: tt.err})
			err := c.Start(t.Context())
			if !errors.Is(err, tt.err) {
				t.Fatalf("Start = %v, want it to wrap %v", err, tt.err)
			}
			if hint := strings.Contains(err.Error(), "; "+stdoutHint); hint != tt.wantHint {
				t.Errorf("Start = %v, hint %v, want %v", err, hint, tt.wantHint)
			}
		})
	}
}

func TestServing(t *testing.T) {
	for health, want := range map[worker.Health]bool{worker.Healthy: true, worker.Degraded: true, worker.Unhealthy: false} {
		c := newTestCluster(t, nil, &fakePool{health: health})
		if got := c.Serving(); got != want {
			t.Errorf("Serving() with a %s pool = %v, want %v", health, got, want)
		}
	}
}

func TestReport(t *testing.T) {
	started := time.Now().Add(-90 * time.Second)
	c := newTestCluster(t, nil, &fakePool{health: worker.Degraded, waiting: 3, workers: []worker.Info{
		{Slot: 0, State: worker.StateWorking, Pid: 4242, Jobs: 7, Started: started, Memory: 30 << 20},
		{Slot: 1, State: worker.StateBackoff},
	}})
	b, err := json.Marshal(c.Report())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"health":"degraded","queue_length":3,"lanes":[{"route":"","priority":"normal","queue_length":3,"busy_workers":0}],"workers":[` +
		`{"slot":0,"state":"working","pid":4242,"jobs":7,"started_at":"` + started.UTC().Format(time.RFC3339) +
		`","age_seconds":90,"memory_bytes":31457280},{"slot":1,"state":"backoff","jobs":0}]}`
	if string(b) != want {
		t.Errorf("Report:\n%s\nwant:\n%s", b, want)
	}
}

func TestReset(t *testing.T) {
	printed := &worker.ProcessError{Op: "handshake", Err: &worker.StdoutError{Output: []byte("Warning")}}
	for _, tt := range []struct {
		err      error
		want     error
		wantHint bool
	}{
		{err: nil},
		{err: worker.ErrResetRunning, want: cluster.ErrResetRunning},
		{err: printed, want: printed, wantHint: true},
	} {
		err := newTestCluster(t, nil, &fakePool{resetErr: tt.err}).Reset(t.Context())
		if !errors.Is(err, tt.want) || (err == nil) != (tt.want == nil) {
			t.Errorf("Reset with %v = %v, want %v", tt.err, err, tt.want)
		}
		if hint := err != nil && strings.Contains(err.Error(), stdoutHint); hint != tt.wantHint {
			t.Errorf("Reset with %v = %v: hint %v, want %v", tt.err, err, hint, tt.wantHint)
		}
	}
}

// The route timeout bounds reading the body: a client that sends it slowly
// gets 408 when it expires.
func TestSlowBody(t *testing.T) {
	c := newTestCluster(t, nil, &fakePool{exec: respond(relay.CodecJSON, "{}", "")})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond) // the route timeout
		defer cancel()
		c.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	_, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\nonly a part")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout || time.Since(start) > 3*time.Second {
		t.Errorf("status %d after %s, want 408 after about 200ms", resp.StatusCode, time.Since(start))
	}
}

// gatedPool holds every job until gate closes; unlike fakePool it is safe for
// concurrent requests.
type gatedPool struct {
	*fakePool
	gate    chan struct{}
	entered chan struct{}
}

func (p *gatedPool) ExecStream(context.Context, worker.Payload) (worker.Payload, frames, error) {
	p.entered <- struct{}{}
	<-p.gate
	return worker.Payload{Codec: relay.CodecJSON, Context: []byte(`{"status":200}`)}, nil, nil
}

// Bodies held at once stay within max_buffered_bodies; a request that
// doesn't fit gets 503 at once.
func TestBodyBudget(t *testing.T) {
	p := &gatedPool{fakePool: &fakePool{}, gate: make(chan struct{}), entered: make(chan struct{}, 8)}
	cfg := DefaultConfig()
	cfg.Command = []string{"php"}
	cfg.MaxRequestBody, cfg.MaxBufferedBodies = 100, 250
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c := newCluster(&cfg, "", p, logging.Discard())

	send := func(body io.Reader) chan int {
		done := make(chan int, 1)
		go func() {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", body)
			r.Header.Set("Content-Type", "text/plain")
			done <- serve(c, r).Code
		}()
		return done
	}
	first, second := send(strings.NewReader(strings.Repeat("a", 100))), send(strings.NewReader(strings.Repeat("b", 100)))
	<-p.entered
	<-p.entered
	third := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(strings.Repeat("c", 100)))
	if rec := serve(c, third); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("a third 100-byte body = %d with Retry-After %q, want 503 with 1", rec.Code,
			rec.Header().Get("Retry-After"))
	}
	if code := <-send(io.MultiReader(strings.NewReader("unknown length"))); code != http.StatusServiceUnavailable {
		t.Errorf("a body of unknown length (reserves max_request_body) = %d, want 503", code)
	}
	small := send(strings.NewReader(strings.Repeat("d", 50))) // fits: 250 of 250
	<-p.entered
	if used := c.bodies.used.Load(); used != 250 {
		t.Errorf("bytes reserved = %d, want 250", used)
	}
	close(p.gate)
	for _, done := range []chan int{first, second, small} {
		if code := <-done; code != http.StatusOK {
			t.Errorf("a request within the budget = %d, want 200", code)
		}
	}
	if used, rejected := c.bodies.used.Load(), c.metrics.lanes[""].rejected[reasonBodyBudget].Value(); used != 0 ||
		rejected != 2 {
		t.Errorf("after the requests: %d bytes reserved, %d rejected; want 0 and 2", used, rejected)
	}
}

// A route with lane settings gets a handler that serves it in its own lane.
func TestLane(t *testing.T) {
	p := &fakePool{exec: respond(relay.CodecJSON, `{"status":200}`, "ok")}
	c := newTestCluster(t, nil, p)
	h, err := c.Lane(httpx.Lane{Name: "slow", MaxWorkers: 2, Priority: "low", QueueTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Lane: %v", err)
	}
	want := worker.LaneConfig{Name: "slow", MaxWorkers: 2, Priority: worker.PriorityLow, QueueTimeout: 5 * time.Second}
	if len(p.lanes) != 1 || p.lanes[0] != want {
		t.Errorf("lanes %+v, want %+v", p.lanes, want)
	}
	if _, err := c.Lane(httpx.Lane{Name: "vip", Priority: "high"}); err != nil || p.lanes[1].Priority != worker.PriorityHigh {
		t.Errorf("Lane(vip) = %v, priority %s", err, p.lanes[1].Priority)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/export", http.NoBody))
	if rec.Code != http.StatusOK || p.lastLane != "slow" {
		t.Errorf("lane handler: %d in lane %q, want 200 in slow", rec.Code, p.lastLane)
	}
	serve(c, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if p.lastLane != "" {
		t.Errorf("ServeHTTP ran in lane %q, want the default one", p.lastLane)
	}

	var r report
	if err := json.Unmarshal(mustJSON(t, c.Report()), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Lanes) != 3 || r.Lanes[1] != (laneReport{Route: "slow", Priority: "low", MaxWorkers: 2, QueueTimeout: "5s",
		BusyWorkers: 1}) || r.Lanes[0].Priority != "normal" {
		t.Errorf("lanes in the report: %+v", r.Lanes)
	}

	p.laneErr = errors.Join(&worker.FieldError{Field: "MaxWorkers", Msg: "must not exceed the pool's 4 workers, got 9"},
		errors.New("worker pool already started"))
	_, err = c.Lane(httpx.Lane{Name: "big", MaxWorkers: 9})
	if fe, ok := errors.AsType[*config.FieldError](err); !ok || fe.Field != "max_workers" ||
		!strings.Contains(err.Error(), "already started") {
		t.Errorf("Lane error = %v, want a config.FieldError for max_workers and the other error", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The pool's 503s tell the client when to try again.
func TestRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		retryAfter time.Duration
		err        error
		want       string
	}{
		{time.Second, worker.ErrQueueFull, "1"},
		{1500 * time.Millisecond, worker.ErrNoWorker, "2"},
		{0, worker.ErrQueueFull, ""},
		{time.Second, worker.ErrExecTimeout, ""}, // 504: no
	} {
		c := newTestCluster(t, func(c *Config) { c.RetryAfter = config.Duration(tt.retryAfter) }, &fakePool{exec: fail(tt.err)})
		rec := serve(c, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
		if got := rec.Header().Get("Retry-After"); got != tt.want {
			t.Errorf("retry_after %s, %v: %d with Retry-After %q, want %q", tt.retryAfter, tt.err, rec.Code, got, tt.want)
		}
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	cfg := DefaultConfig()
	_, err := New(&cfg, registry.BuildContext{Logger: logging.Discard()}, nil)
	if fe, ok := errors.AsType[*worker.FieldError](err); !ok || fe.Field != "Command" {
		t.Errorf("err = %v, want a worker.FieldError for Command", err)
	}
}

func TestPoolMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	cfg := DefaultConfig()
	cfg.Command = []string{"php", "worker.php"}
	cfg.Pool.Workers = 3
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := New(&cfg, registry.BuildContext{Name: "app", Logger: logging.Discard(), Metrics: reg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lane(httpx.Lane{Name: "slow", MaxWorkers: 2, Priority: "low"}); err != nil {
		t.Fatalf("Lane: %v", err)
	}
	// Before Start, every slot is stopped; every reason has a series at 0.
	if got := metricstest.Value(t, reg, "kest_pool_workers", "cluster", "app", "state", "stopped"); got != 3 {
		t.Errorf("stopped workers = %v, want 3", got)
	}
	fams := metricstest.Scrape(t, reg)
	if n := len(fams["kest_pool_worker_restarts_total"].Metric); n != len(worker.ReplaceReasons) {
		t.Errorf("%d restart reasons exposed, want %d", n, len(worker.ReplaceReasons))
	}
	for name, want := range map[string]int{"kest_pool_queue_length": 2, "kest_pool_lane_workers": 2,
		"kest_pool_wait_seconds": 2, "kest_pool_rejected_total": 2 * len(rejectReasons)} {
		if n := len(fams[name].Metric); n != want {
			t.Errorf("%s: %d series, want %d (the default lane and slow)", name, n, want)
		}
	}

	c.metrics.Waited("", 20*time.Millisecond)
	c.metrics.Waited("slow", 20*time.Millisecond)
	c.metrics.Waited("slow", 20*time.Millisecond)
	c.metrics.Rejected("slow", worker.ReasonQueueFull)
	c.metrics.Rejected("unknown", worker.ReasonQueueFull) // ignored
	c.metrics.Replaced(worker.ReasonMaxJobs)
	c.metrics.Replaced(worker.ReasonMaxJobs)
	for _, tt := range []struct {
		name   string
		labels []string
		want   float64
	}{
		{"kest_pool_wait_seconds", []string{"cluster", "app", "route", ""}, 1},
		{"kest_pool_wait_seconds", []string{"cluster", "app", "route", "slow"}, 2},
		{"kest_pool_rejected_total", []string{"cluster", "app", "route", "slow", "reason", "queue_full"}, 1},
		{"kest_pool_rejected_total", []string{"cluster", "app", "route", "", "reason", "queue_full"}, 0},
		{"kest_pool_queue_length", []string{"cluster", "app", "route", "slow"}, 0},
		{"kest_pool_worker_restarts_total", []string{"cluster", "app", "reason", "max_jobs"}, 2},
	} {
		if got := metricstest.Value(t, reg, tt.name, tt.labels...); got != tt.want {
			t.Errorf("%s%v = %v, want %v", tt.name, tt.labels, got, tt.want)
		}
	}
}
