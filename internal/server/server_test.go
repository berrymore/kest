package server_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/admin"
	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/server"
)

// gateCluster holds every request until its gate is opened (or the request
// context ends) and records its lifecycle calls.
type gateCluster struct {
	gate     chan struct{}
	entered  chan struct{}
	started  atomic.Bool
	stopped  atomic.Bool
	down     atomic.Bool  // Serving reports false
	resets   atomic.Int32 // Reset calls
	startErr error
}

type gateConfig struct {
	FailStart bool `yaml:"fail_start"`
}

var gates = map[string]*gateCluster{}

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

func init() {
	cluster.Types.Register(registry.Factory[cluster.Cluster]{
		Type:      "test_gate",
		NewConfig: func() any { return &gateConfig{} },
		Build: func(bc registry.BuildContext, cfg any) (cluster.Cluster, error) {
			g := &gateCluster{gate: make(chan struct{}), entered: make(chan struct{}, 16)}
			if cfg.(*gateConfig).FailStart {
				g.startErr = errors.New("boom")
			}
			gates[bc.Name] = g
			return g, nil
		},
	})
}

func (g *gateCluster) Start(context.Context) error {
	g.started.Store(true)
	return g.startErr
}

func (g *gateCluster) Stop(context.Context) error {
	g.stopped.Store(true)
	return nil
}

func (g *gateCluster) Serving() bool { return !g.down.Load() }

func (g *gateCluster) Reset(context.Context) error {
	g.resets.Add(1)
	return nil
}

func (g *gateCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.entered <- struct{}{}
	select {
	case <-g.gate:
		_, _ = io.WriteString(w, "released")
	case <-r.Context().Done():
		http.Error(w, "timeout", http.StatusGatewayTimeout)
	}
}

// serverConfig is the config of newServer, before its extra part.
const serverConfig = `version: v1
admin: {address: "127.0.0.1:0"}
server: {drain_timeout: 2s}
listeners:
  - name: main
    address: 127.0.0.1:0
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: {path: /ping}
              direct_response: {body: "pong"}
            - match: {prefix: /slow}
              route: {cluster: gate, timeout: 200ms}
            - match: {prefix: /}
              route: {cluster: gate}
clusters:
  - name: gate
    type: test_gate
`

func newServer(t *testing.T, extra string) *server.Server {
	t.Helper()
	src := serverConfig + extra
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := server.New(cfg, logging.Discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func run(t *testing.T, s *server.Server) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	select {
	case <-s.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("Run returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server not ready after 5s")
	}
	return cancel, errCh
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("GET %s: %v", url, err)
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRunServesAndDrains(t *testing.T) {
	s := newServer(t, "")
	cancel, done := run(t, s)
	defer cancel()

	listener := "http://" + s.ListenerAddr("main").String()
	adminURL := "http://" + s.AdminAddr().String()

	if code, body := get(t, listener+"/ping"); code != 200 || body != "pong" {
		t.Errorf("/ping = %d %q", code, body)
	}
	if code, body := get(t, adminURL+"/ready"); code != 200 || strings.TrimSpace(body) != "ready" {
		t.Errorf("/ready = %d %q", code, body)
	}
	g := gates["gate"]
	if !g.started.Load() {
		t.Error("cluster was not started")
	}

	// Route timeout: the cluster sees the deadline and answers 504.
	if code, _ := get(t, listener+"/slow"); code != http.StatusGatewayTimeout {
		t.Errorf("/slow = %d, want 504 after the 200ms route timeout", code)
	}
	<-g.entered

	// An in-flight request survives the shutdown signal and completes.
	type result struct {
		code int
		body string
	}
	inflight := make(chan result, 1)
	go func() {
		code, body := get(t, listener+"/")
		inflight <- result{code, body}
	}()
	<-g.entered

	cancel()
	waitFor(t, func() bool {
		code, body := get(t, adminURL+"/ready")
		return code == http.StatusServiceUnavailable && strings.TrimSpace(body) == "draining"
	})

	close(g.gate)
	if res := <-inflight; res.code != 200 || res.body != "released" {
		t.Errorf("in-flight request = %d %q, want 200 released", res.code, res.body)
	}
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	if !g.stopped.Load() {
		t.Error("cluster was not stopped")
	}
	if s.ListenerAddr("nope") != nil {
		t.Error("ListenerAddr for an unknown listener should be nil")
	}
}

func TestRunStartFailure(t *testing.T) {
	s := newServer(t, "    config: {fail_start: true}\n")
	err := s.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `start cluster "gate": boom`) {
		t.Errorf("Run = %v, want the cluster start error", err)
	}
	if gates["gate"].stopped.Load() {
		t.Error("a cluster that failed to start must not be stopped")
	}
}

func TestRunListenerBindFailure(t *testing.T) {
	first := newServer(t, "")
	cancel, done := run(t, first)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("first server: %v", err)
		}
	}()
	taken := first.ListenerAddr("main").String()

	src := fmt.Sprintf(`version: v1
admin: {address: "127.0.0.1:0"}
listeners:
  - name: main
    address: %q
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
`, taken)
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := server.New(cfg, logging.Discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = s.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `start listener "main"`) {
		t.Errorf("Run = %v, want a listener start error", err)
	}
	if s.AdminAddr() == nil {
		t.Error("admin should have been started before the listener failed")
	}
}

func TestNewReportsExtensionErrors(t *testing.T) {
	src := `version: v1
listeners:
  - name: main
    address: 127.0.0.1:0
    http:
      filters: [{type: nope}]
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, route: {cluster: a}}]
clusters:
  - {name: a, type: test_gate, config: {fail_start: maybe}}
  - {name: b, type: missing}
`
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = server.New(cfg, logging.Discard())
	want := []string{
		`kest.yaml:6:24: listeners[0].http.filters[0].type: unknown http filter type "nope"`,
		"kest.yaml:12:53: clusters[0].config.fail_start: cannot construct !!str `maybe` into bool",
		`kest.yaml:13:21: clusters[1].type: unknown cluster type "missing"`,
	}
	for _, w := range want {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("New error =\n%v\nwant it to contain %q", err, w)
		}
	}
}

// Certificate files are read when the graph is built, relative to the config
// file; a pair that doesn't load is reported at its path.
func TestNewLoadsCertificates(t *testing.T) {
	dir := t.TempDir()
	src := `version: v1
listeners:
  - name: secure
    address: 127.0.0.1:0
    tls:
      certificates:
        - {cert_file: missing.pem, key_file: missing.key}
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
`
	file := filepath.Join(dir, "kest.yaml")
	cfg, err := config.Parse([]byte(src), file, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = server.New(cfg, logging.Discard())
	want := file + ":7:11: listeners[0].tls.certificates[0]: open " + filepath.Join(dir, "missing.pem") +
		": no such file or directory"
	if err == nil || err.Error() != want {
		t.Errorf("New error =\n%v\nwant\n%s", err, want)
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

// The listeners and the runtime report to the server's registry.
func TestMetrics(t *testing.T) {
	s := newServer(t, "")
	cancel, done := run(t, s)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	if code, _ := get(t, "http://"+s.ListenerAddr("main").String()+"/ping"); code != http.StatusOK {
		t.Fatalf("GET /ping: %d", code)
	}
	if got := metricstest.Value(t, s.Metrics(), "kest_http_requests_total",
		"listener", "main", "virtual_host", "default", "route", "", "cluster", "", "code", "200"); got != 1 {
		t.Errorf("requests = %v, want 1", got)
	}
	if got := metricstest.Value(t, s.Metrics(), "go_goroutines"); got <= 0 {
		t.Errorf("go_goroutines = %v", got)
	}
}

// /ready fails while a cluster can't serve, and recovers with it.
func TestReadyFollowsClusters(t *testing.T) {
	s := newServer(t, "")
	cancel, done := run(t, s)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	ready := "http://" + s.AdminAddr().String() + "/ready"
	if code, body := get(t, ready); code != http.StatusOK {
		t.Fatalf("/ready = %d %q", code, body)
	}
	gates["gate"].down.Store(true)
	if code, body := get(t, ready); code != http.StatusServiceUnavailable || body != "not serving: gate\n" {
		t.Errorf("/ready with the cluster down = %d %q", code, body)
	}
	gates["gate"].down.Store(false)
	if code, _ := get(t, ready); code != http.StatusOK {
		t.Errorf("/ready after the cluster recovered = %d", code)
	}
}

func TestClusters(t *testing.T) {
	s := newServer(t, "")
	got := s.Clusters()
	if len(got) != 1 || got[0].Name != "gate" || got[0].Type != "test_gate" || got[0].Serving == nil ||
		!*got[0].Serving || got[0].Status != nil {
		t.Errorf("Clusters() = %+v", got)
	}
}

func TestResetCluster(t *testing.T) {
	s := newServer(t, "")
	if err := s.ResetCluster(t.Context(), "gate"); !errors.Is(err, admin.ErrNotReady) {
		t.Errorf("ResetCluster before Run = %v, want %v", err, admin.ErrNotReady)
	}
	hup := make(chan os.Signal, 1)
	s.ResetOn(hup)
	cancel, done := run(t, s)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	if err := s.ResetCluster(t.Context(), "nope"); !errors.Is(err, admin.ErrUnknownCluster) {
		t.Errorf("ResetCluster(nope) = %v, want %v", err, admin.ErrUnknownCluster)
	}
	if err := s.ResetCluster(t.Context(), "gate"); err != nil || gates["gate"].resets.Load() != 1 {
		t.Errorf("ResetCluster(gate) = %v, %d resets", err, gates["gate"].resets.Load())
	}
	hup <- syscall.SIGHUP
	waitFor(t, func() bool { return gates["gate"].resets.Load() == 2 })
}

// During drain_delay, /ready fails while the listeners still serve new
// requests; then the server drains and stops.
func TestDrainDelay(t *testing.T) {
	src := strings.Replace(serverConfig, "server: {drain_timeout: 2s}", "server: {drain_timeout: 2s, drain_delay: 1s}", 1)
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.New(cfg, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := run(t, s)
	listener := "http://" + s.ListenerAddr("main").String()
	adminURL := "http://" + s.AdminAddr().String()

	start := time.Now()
	cancel()
	waitFor(t, func() bool {
		code, _ := get(t, adminURL+"/ready")
		return code == http.StatusServiceUnavailable
	})
	if code, body := get(t, listener+"/ping"); code != http.StatusOK || body != "pong" {
		t.Errorf("/ping during the drain delay = %d %q, want 200", code, body)
	}
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("stopped after %s, want at least the 1s drain delay", elapsed)
	}
}
