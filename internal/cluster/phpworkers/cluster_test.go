package phpworkers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
	startErr error
	last     worker.Payload
	calls    int
	started  bool
	stopped  bool
}

func (p *fakePool) Start(context.Context) error { p.started = true; return p.startErr }
func (p *fakePool) Stop(context.Context) error  { p.stopped = true; return nil }

func (p *fakePool) Exec(ctx context.Context, req worker.Payload) (worker.Payload, error) {
	p.calls++
	p.last = req
	return p.exec(ctx, req)
}

func respond(codec relay.Flags, header, body string) func(context.Context, worker.Payload) (worker.Payload, error) {
	return func(context.Context, worker.Payload) (worker.Payload, error) {
		return worker.Payload{Codec: codec, Context: []byte(header), Body: []byte(body)}, nil
	}
}

func fail(err error) func(context.Context, worker.Payload) (worker.Payload, error) {
	return func(context.Context, worker.Payload) (worker.Payload, error) { return worker.Payload{}, err }
}

func newTestCluster(t *testing.T, mutate func(*Config), p *fakePool) *Cluster {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Command = []string{"php"}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
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
	if _, err := New(&cfg, registry.BuildContext{Name: "app", Logger: logging.Discard(), Metrics: reg}, nil); err != nil {
		t.Fatal(err)
	}
	// Before Start, every slot is stopped; every reason has a series at 0.
	if got := metricstest.Value(t, reg, "kest_pool_workers", "cluster", "app", "state", "stopped"); got != 3 {
		t.Errorf("stopped workers = %v, want 3", got)
	}
	if got := metricstest.Value(t, reg, "kest_pool_queue_length", "cluster", "app"); got != 0 {
		t.Errorf("queue length = %v, want 0", got)
	}
	fams := metricstest.Scrape(t, reg)
	if n := len(fams["kest_pool_worker_restarts_total"].Metric); n != len(worker.ReplaceReasons) {
		t.Errorf("%d restart reasons exposed, want %d", n, len(worker.ReplaceReasons))
	}

	m := newPoolMetrics(reg, "app") // the same series as the cluster's
	m.Waited(20 * time.Millisecond)
	m.Rejected(worker.ReasonQueueFull)
	m.Replaced(worker.ReasonMaxJobs)
	m.Replaced(worker.ReasonMaxJobs)
	for _, tt := range []struct {
		name   string
		labels []string
		want   float64
	}{
		{"kest_pool_wait_seconds", []string{"cluster", "app"}, 1},
		{"kest_pool_rejected_total", []string{"cluster", "app", "reason", "queue_full"}, 1},
		{"kest_pool_rejected_total", []string{"cluster", "app", "reason", "no_worker"}, 0},
		{"kest_pool_worker_restarts_total", []string{"cluster", "app", "reason", "max_jobs"}, 2},
	} {
		if got := metricstest.Value(t, reg, tt.name, tt.labels...); got != tt.want {
			t.Errorf("%s%v = %v, want %v", tt.name, tt.labels, got, tt.want)
		}
	}
}
