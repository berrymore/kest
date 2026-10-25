package httpproxy

import (
	"bufio"
	"context"
	"encoding/pem"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/registry"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

// backend answers its name, and records the last request's headers.
type backend struct {
	*httptest.Server
	name    string
	hits    atomic.Int64
	mu      sync.Mutex
	last    http.Header
	host    string
	status  atomic.Int64 // answer with this status if set
	healthy atomic.Bool  // /healthz answers 200 if set, else 500
}

func newBackend(t *testing.T, name string) *backend {
	t.Helper()
	b := &backend{name: name}
	b.healthy.Store(true)
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			if !b.healthy.Load() {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
		b.hits.Add(1)
		b.mu.Lock()
		b.last, b.host = r.Header.Clone(), r.Host
		b.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if s := b.status.Load(); s != 0 {
			w.WriteHeader(int(s))
		}
		_, _ = io.WriteString(w, name+" "+string(body))
	}))
	t.Cleanup(b.Close)
	return b
}

// deadEndpoint returns an address where nothing listens.
func deadEndpoint(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr
}

func newProxy(t *testing.T, reg *metrics.Registry, mutate func(*Config)) (*Cluster, *httptest.Server) {
	t.Helper()
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := New(&cfg, registry.BuildContext{Name: "api", Logger: logging.Discard(), Metrics: reg})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c)
	t.Cleanup(func() {
		srv.Close()
		_ = c.Stop(context.Background())
	})
	return c, srv
}

func call(t *testing.T, method, url, body string, header http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRoundRobinAndHeaders(t *testing.T) {
	a, b := newBackend(t, "a"), newBackend(t, "b")
	_, srv := newProxy(t, nil, func(c *Config) { c.Endpoints = []string{a.URL, b.URL} })

	for range 4 {
		if code, _ := call(t, http.MethodGet, srv.URL+"/x?q=1", "", http.Header{"X-Forwarded-For": {"1.1.1.1"}}); code != 200 {
			t.Fatalf("status %d", code)
		}
	}
	if a.hits.Load() != 2 || b.hits.Load() != 2 {
		t.Errorf("hits a=%d b=%d, want 2 each", a.hits.Load(), b.hits.Load())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if got := a.last.Get("X-Forwarded-For"); got != "1.1.1.1, 127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want the client's chain and the peer", got)
	}
	if a.host != strings.TrimPrefix(srv.URL, "http://") || a.last.Get("X-Forwarded-Host") != a.host ||
		a.last.Get("X-Forwarded-Proto") != "http" {
		t.Errorf("Host %q, X-Forwarded-Host %q, -Proto %q", a.host, a.last.Get("X-Forwarded-Host"), a.last.Get("X-Forwarded-Proto"))
	}
}

func TestHostOverride(t *testing.T) {
	a := newBackend(t, "a")
	_, srv := newProxy(t, nil, func(c *Config) { c.Endpoints, c.Host = []string{a.URL}, "api.internal" })
	call(t, http.MethodGet, srv.URL, "", nil)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.host != "api.internal" {
		t.Errorf("Host = %q, want api.internal", a.host)
	}
}

func TestLeastRequest(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "slow")
	}))
	defer slow.Close()
	fast := newBackend(t, "fast")
	_, srv := newProxy(t, nil, func(c *Config) {
		c.Endpoints, c.LoadBalancing = []string{slow.URL, fast.URL}, LeastRequest
	})

	// Until one request occupies slow, requests may go either way.
	done := make(chan string)
	go func() {
		for {
			_, body := call(t, http.MethodGet, srv.URL, "", nil)
			if body == "slow" {
				done <- body
				return
			}
		}
	}()
	<-entered
	before := fast.hits.Load()
	for range 5 {
		if _, body := call(t, http.MethodGet, srv.URL, "", nil); body != "fast " {
			t.Errorf("body %q, want the idle endpoint", body)
		}
	}
	if fast.hits.Load()-before != 5 {
		t.Errorf("fast got %d of 5", fast.hits.Load()-before)
	}
	close(release)
	<-done
}

func TestRetries(t *testing.T) {
	reg := metrics.NewRegistry()
	a := newBackend(t, "a")
	dead := deadEndpoint(t)
	_, srv := newProxy(t, reg, func(c *Config) { c.Endpoints = []string{dead, a.URL} })

	// A connection that can't be made sent nothing: a POST is retried too.
	for range 2 {
		if code, body := call(t, http.MethodPost, srv.URL, "payload", nil); code != 200 || body != "a payload" {
			t.Errorf("POST = %d %q, want the live endpoint with the body", code, body)
		}
	}
	if got := metricstest.Value(t, reg, "kest_upstream_retries_total", "cluster", "api"); got != 1 {
		t.Errorf("retries = %v, want 1 (round robin tried the dead endpoint once)", got)
	}
	if got := metricstest.Value(t, reg, "kest_upstream_errors_total", "cluster", "api", "endpoint", dead, "reason", "connect_failure"); got != 1 {
		t.Errorf("connect failures = %v, want 1", got)
	}
	if got := metricstest.Value(t, reg, "kest_upstream_requests_total", "cluster", "api", "endpoint", a.URL, "code", "200"); got != 2 {
		t.Errorf("requests to a = %v, want 2", got)
	}
}

func TestRetryGatewayError(t *testing.T) {
	a, b := newBackend(t, "a"), newBackend(t, "b")
	a.status.Store(http.StatusServiceUnavailable)
	_, srv := newProxy(t, nil, func(c *Config) {
		c.Endpoints = []string{a.URL, b.URL}
		c.Retries.On = []string{RetryGatewayError}
	})
	for range 2 {
		if code, body := call(t, http.MethodGet, srv.URL, "", nil); code != 200 || body != "b " {
			t.Errorf("GET = %d %q, want b after a's 503", code, body)
		}
	}
	// A POST with a body isn't repeated: one of the two gets a's 503.
	codes := map[int]int{}
	for range 2 {
		code, _ := call(t, http.MethodPost, srv.URL, "x", nil)
		codes[code]++
	}
	if codes[503] != 1 || codes[200] != 1 {
		t.Errorf("POST statuses %v, want one 503 and one 200", codes)
	}
}

func TestErrors(t *testing.T) {
	_, srv := newProxy(t, nil, func(c *Config) { c.Endpoints = []string{deadEndpoint(t)} })
	if code, _ := call(t, http.MethodGet, srv.URL, "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("dead endpoint: %d, want 503", code)
	}

	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	c, _ := newProxy(t, nil, func(c *Config) { c.Endpoints = []string{hang.URL} })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("route timeout: %d, want 504", rec.Code)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthChecks(t *testing.T) {
	reg := metrics.NewRegistry()
	a, b := newBackend(t, "a"), newBackend(t, "b")
	a.healthy.Store(false)
	c, srv := newProxy(t, reg, func(c *Config) {
		c.Endpoints = []string{a.URL, b.URL}
		c.HealthCheck.Path = "/healthz"
		c.HealthCheck.Interval /= 500 // 20ms
		c.HealthCheck.Timeout = c.HealthCheck.Interval
	})
	// The first round of checks ran in Start.
	if got := metricstest.Value(t, reg, "kest_upstream_endpoint_healthy", "cluster", "api", "endpoint", a.URL); got != 0 {
		t.Errorf("a healthy = %v after Start, want 0", got)
	}
	for range 4 {
		if _, body := call(t, http.MethodGet, srv.URL, "", nil); body != "b " {
			t.Errorf("body %q, want only the healthy endpoint", body)
		}
	}

	a.healthy.Store(true)
	waitFor(t, func() bool { return c.endpoints[0].healthy.Load() })
	call(t, http.MethodGet, srv.URL, "", nil)
	call(t, http.MethodGet, srv.URL, "", nil)
	if a.hits.Load() == 0 {
		t.Error("a got no requests after it became healthy")
	}

	// With every endpoint down, requests still go somewhere.
	a.healthy.Store(false)
	b.healthy.Store(false)
	waitFor(t, func() bool { return !c.endpoints[0].healthy.Load() && !c.endpoints[1].healthy.Load() })
	if code, _ := call(t, http.MethodGet, srv.URL, "", nil); code != 200 {
		t.Errorf("all unhealthy: %d, want the request served anyway", code)
	}
	r, ok := c.Report().(report)
	if !ok || len(r.Endpoints) != 2 || r.Endpoints[0].Healthy || r.LoadBalancing != RoundRobin {
		t.Errorf("report %+v", c.Report())
	}
}

// An Upgrade (WebSocket) passes through: after the 101, bytes flow both ways.
func TestUpgrade(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "want an upgrade", http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = brw.Flush()
		line, _ := brw.ReadString('\n')
		_, _ = brw.WriteString("echo: " + line)
		_ = brw.Flush()
	}))
	defer echo.Close()
	_, srv := newProxy(t, nil, func(c *Config) { c.Endpoints = []string{echo.URL} })

	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() // a 101 has no body: the connection carries on
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response %d", resp.StatusCode)
	}
	_, _ = io.WriteString(conn, "hello\n")
	if line, err := r.ReadString('\n'); err != nil || line != "echo: hello\n" {
		t.Errorf("echo = %q, %v", line, err)
	}
}

func TestTLSEndpoint(t *testing.T) {
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer secure.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	_, srv := newProxy(t, nil, func(c *Config) { c.Endpoints, c.TLS.CAFile = []string{secure.URL}, ca })
	if code, body := call(t, http.MethodGet, srv.URL, "", nil); code != 200 || body != "secure" {
		t.Errorf("with ca_file: %d %q", code, body)
	}
	_, srv = newProxy(t, nil, func(c *Config) { c.Endpoints = []string{secure.URL} })
	if code, _ := call(t, http.MethodGet, srv.URL, "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("an untrusted certificate: %d, want 503", code)
	}

	cfg := DefaultConfig()
	cfg.Endpoints, cfg.TLS.CAFile = []string{secure.URL}, "missing.pem"
	if _, err := New(&cfg, registry.BuildContext{Name: "x", Logger: logging.Discard(), ConfigDir: t.TempDir()}); err == nil ||
		!strings.HasPrefix(err.Error(), "tls.ca_file: open ") {
		t.Errorf("New with a missing ca_file: %v", err)
	}
}

func TestValidate(t *testing.T) {
	c := DefaultConfig()
	c.Endpoints = []string{"10.0.0.1:80", "ftp://x", "http://a:8080/path", "http://a:8080", "http://a:8080"}
	c.LoadBalancing = "fastest"
	c.MaxIdleConnections = 2000
	c.HealthCheck.Path = "healthz"
	c.HealthCheck.Timeout = c.HealthCheck.Interval * 2
	c.Retries.Count = 11
	c.Retries.On = []string{"5xx"}
	want := []string{
		`endpoints[0]: invalid endpoint "10.0.0.1:80"`,
		`endpoints[1]: invalid endpoint "ftp://x": want http:// or https://`,
		`endpoints[2]: invalid endpoint "http://a:8080/path": want scheme://host[:port], without a path`,
		`endpoints[4]: duplicate endpoint "http://a:8080"`,
		`load_balancing: unknown policy "fastest"; use round_robin, least_request or random`,
		`max_idle_connections: must be between 0 and max_connections (1024), got 2000`,
		`health_check.path: must start with /`,
		`health_check.timeout: must not be longer than interval (10s)`,
		`retries.count: must be between 0 and 10, got 11`,
		`retries.on[0]: unknown condition "5xx"; use connect_failure, gateway_error or reset`,
	}
	err := c.Validate()
	for _, w := range want {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("Validate() =\n%v\nwant it to contain %q", err, w)
		}
	}
	empty := DefaultConfig()
	if err := empty.Validate(); err == nil || !strings.HasPrefix(err.Error(), "endpoints: at least one endpoint") {
		t.Errorf("Validate() without endpoints = %v", err)
	}
}
