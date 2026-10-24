package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/metrics"
)

type fakeSource struct {
	state      State
	notServing []string
	metrics    *metrics.Registry
}

func (f *fakeSource) State() State         { return f.state }
func (f *fakeSource) StartedAt() time.Time { return time.Now().Add(-90 * time.Second) }
func (f *fakeSource) ConfigFile() string   { return "/etc/kest/kest.yaml" }
func (f *fakeSource) NotServing() []string { return f.notServing }

func (f *fakeSource) Metrics() *metrics.Registry {
	if f.metrics == nil {
		f.metrics = metrics.NewRegistry()
	}
	return f.metrics
}

func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

func TestReady(t *testing.T) {
	src := &fakeSource{}
	h := NewHandler(src)

	for _, tc := range []struct {
		state State
		code  int
	}{
		{StateStarting, http.StatusServiceUnavailable},
		{StateReady, http.StatusOK},
		{StateDraining, http.StatusServiceUnavailable},
	} {
		src.state = tc.state
		rec := serve(t, h, http.MethodGet, "/ready")
		if rec.Code != tc.code || strings.TrimSpace(rec.Body.String()) != string(tc.state) {
			t.Errorf("%s: /ready = %d %q, want %d", tc.state, rec.Code, rec.Body.String(), tc.code)
		}
	}

	if rec := serve(t, h, http.MethodPost, "/ready"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /ready = %d, want 405", rec.Code)
	}

	// Ready, but a cluster has no live worker.
	src.state, src.notServing = StateReady, []string{"app", "reports"}
	if rec := serve(t, h, http.MethodGet, "/ready"); rec.Code != http.StatusServiceUnavailable ||
		rec.Body.String() != "not serving: app, reports\n" {
		t.Errorf("/ready with clusters down = %d %q, want 503", rec.Code, rec.Body.String())
	}
}

func TestMetrics(t *testing.T) {
	src := &fakeSource{}
	src.Metrics().NewCounter("kest_test_total", "A counter.").Add(3)
	h := NewHandler(src)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := serve(t, h, method, "/metrics")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != metrics.ContentType {
			t.Errorf("%s /metrics = %d, Content-Type %q", method, rec.Code, rec.Header().Get("Content-Type"))
		}
		if method == http.MethodGet && !strings.Contains(rec.Body.String(), "\nkest_test_total 3\n") {
			t.Errorf("GET /metrics:\n%s", rec.Body.String())
		}
	}
}

func TestServerInfo(t *testing.T) {
	rec := serve(t, NewHandler(&fakeSource{state: StateReady}), http.MethodGet, "/server_info")
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var info serverInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if info.State != StateReady || info.ConfigFile != "/etc/kest/kest.yaml" || info.Version == "" || info.GoVersion == "" {
		t.Errorf("server_info = %+v", info)
	}
	if info.UptimeSeconds < 89 || info.UptimeSeconds > 95 {
		t.Errorf("uptime_seconds = %d, want about 90", info.UptimeSeconds)
	}
}

func TestIndex(t *testing.T) {
	h := NewHandler(&fakeSource{})
	rec := serve(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/server_info") {
		t.Errorf("GET / = %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(t, h, http.MethodGet, "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", rec.Code)
	}
}
