package httpx_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/registry"
)

func init() {
	// header_trace appends its name to the X-Trace request header, so tests can see the chain order.
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type:      "header_trace",
		NewConfig: func() any { return &traceConfig{} },
		Build: func(_ registry.BuildContext, cfg any) (httpx.Filter, error) {
			name := cfg.(*traceConfig).Name
			return func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.Header.Add("X-Trace", name)
					next.ServeHTTP(w, r)
				})
			}, nil
		},
	})
}

type traceConfig struct {
	Name string `yaml:"name"`
}

func loadHTTP(t *testing.T, listener string) *config.HTTP {
	t.Helper()
	src := "version: v1\nlisteners:\n" + listener + "clusters:\n  - {name: app, type: fake}\n  - {name: gone, type: fake}\n"
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg.Listeners[0].HTTP
}

// echoCluster answers "cluster <path>" and reports whether the request had a deadline.
func echoCluster(w http.ResponseWriter, r *http.Request) {
	if deadline, ok := r.Context().Deadline(); ok {
		w.Header().Set("X-Timeout", time.Until(deadline).Round(time.Second).String())
	}
	w.Header().Set("X-Trace", strings.Join(r.Header.Values("X-Trace"), ","))
	_, _ = io.WriteString(w, "cluster "+r.URL.Path)
}

func lookup(name string) (http.Handler, bool) {
	if name == "app" {
		return http.HandlerFunc(echoCluster), true
	}
	return nil, false
}

// get serves a GET request for host and path through h.
func get(t *testing.T, h http.Handler, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+host+path, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const routerConfig = `
  - name: main
    address: 127.0.0.1:0
    http:
      virtual_hosts:
        - name: exact
          domains: ["api.example.com"]
          routes:
            - match: {prefix: /}
              direct_response: {body: "exact"}
        - name: eu
          domains: ["*.eu.example.com"]
          routes:
            - match: {prefix: /}
              direct_response: {body: "eu"}
        - name: wildcard
          domains: ["*.example.com"]
          routes:
            - match: {prefix: /}
              direct_response: {body: "wildcard"}
        - name: ipv6
          domains: ["[::1]"]
          routes:
            - match: {prefix: /}
              direct_response: {body: "ipv6"}
        - name: default
          domains: ["*"]
          routes:
            - match: {path: /ping}
              direct_response: {status: 202, body: "pong"}
            - match: {path: /empty}
              direct_response: {status: 204}
            - match: {prefix: /api/}
              route: {cluster: app, timeout: 5s}
            - match: {prefix: /gone}
              route: {cluster: gone}
            - match: {prefix: /unbounded}
              route: {cluster: app, timeout: 0s}
            - match: {prefix: /}
              route: {cluster: app}
`

func TestRouterVirtualHosts(t *testing.T) {
	h, err := httpx.NewHandler(loadHTTP(t, routerConfig), lookup, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	tests := []struct {
		host string
		want string
	}{
		{"api.example.com", "exact"},
		{"API.Example.com:8080", "exact"},
		{"api.example.com.", "exact"},
		{"x.eu.example.com", "eu"},
		{"a.b.eu.example.com", "eu"},
		{"www.example.com", "wildcard"},
		{"eu.example.com", "wildcard"},
		{"example.com", "cluster /"},
		{"[::1]:8080", "ipv6"},
		{"other.org", "cluster /"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := get(t, h, tt.host, "/").Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRouterRoutes(t *testing.T) {
	h, err := httpx.NewHandler(loadHTTP(t, routerConfig), lookup, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	tests := []struct {
		path        string
		wantStatus  int
		wantBody    string
		wantTimeout string
	}{
		{path: "/ping", wantStatus: 202, wantBody: "pong"},
		{path: "/ping/x", wantStatus: 200, wantBody: "cluster /ping/x", wantTimeout: "30s"},
		{path: "/empty", wantStatus: 204, wantBody: ""},
		{path: "/api/users", wantStatus: 200, wantBody: "cluster /api/users", wantTimeout: "5s"},
		{path: "/gone", wantStatus: 503, wantBody: "cluster unavailable\n"},
		{path: "/unbounded", wantStatus: 200, wantBody: "cluster /unbounded", wantTimeout: ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := get(t, h, "localhost", tt.path)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("X-Timeout"); got != tt.wantTimeout {
				t.Errorf("route timeout = %q, want %q", got, tt.wantTimeout)
			}
		})
	}
}

func TestDirectResponseHeaders(t *testing.T) {
	h, err := httpx.NewHandler(loadHTTP(t, routerConfig), lookup, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := get(t, h, "localhost", "/ping")
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "4" {
		t.Errorf("Content-Length = %q", cl)
	}
}

func TestRouterNotFound(t *testing.T) {
	h, err := httpx.NewHandler(loadHTTP(t, `
  - name: main
    address: 127.0.0.1:0
    http:
      virtual_hosts:
        - name: only
          domains: ["example.com"]
          routes:
            - match: {prefix: /app/}
              direct_response: {}
`), lookup, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	for _, tc := range []struct{ host, path string }{
		{"other.org", "/app/"},
		{"example.com", "/"},
	} {
		if rec := get(t, h, tc.host, tc.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s%s: status = %d, want 404", tc.host, tc.path, rec.Code)
		}
	}
}

func TestFilterChainOrder(t *testing.T) {
	h, err := httpx.NewHandler(loadHTTP(t, `
  - name: main
    address: 127.0.0.1:0
    http:
      filters:
        - {type: header_trace, config: {name: first}}
        - {type: header_trace, config: {name: second}}
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: {prefix: /}
              route: {cluster: app}
`), lookup, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if got := get(t, h, "localhost", "/").Header().Get("X-Trace"); got != "first,second" {
		t.Errorf("filter order = %q, want first,second", got)
	}
}

func TestNewHandlerErrors(t *testing.T) {
	_, err := httpx.NewHandler(loadHTTP(t, `
  - name: main
    address: 127.0.0.1:0
    http:
      filters:
        - {type: nope}
        - {type: header_trace, config: {title: x}}
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
`), lookup, registry.BuildContext{Logger: logging.Discard()})
	want := []string{
		`kest.yaml:8:18: listeners[0].http.filters[0].type: unknown http filter type "nope"`,
		"kest.yaml:9:41: listeners[0].http.filters[1].config.title: unknown field",
	}
	for _, w := range want {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("error =\n%v\nwant it to contain %q", err, w)
		}
	}
}

func TestMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	var inFlight float64
	clusters := func(name string) (http.Handler, bool) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inFlight = metricstest.Value(t, reg, "kest_http_requests_in_flight", "listener", "main")
			if r.URL.Path == "/api/fail" {
				w.WriteHeader(http.StatusEarlyHints) // informational: the final status follows
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			echoCluster(w, r)
		}), name == "app"
	}
	h, err := httpx.NewHandler(loadHTTP(t, `
  - name: main
    address: 127.0.0.1:0
    http:
      virtual_hosts:
        - name: site
          domains: ["localhost"]
          routes:
            - match: {path: /ping}
              direct_response: {status: 202}
            - match: {prefix: /api/}
              route: {cluster: app}
            - match: {prefix: /gone}
              route: {cluster: gone}
`), clusters, registry.BuildContext{Name: "main", Logger: logging.Discard(), Metrics: reg})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	for _, req := range []struct{ host, path string }{
		{"localhost", "/ping"},
		{"localhost", "/api/a"},
		{"localhost", "/api/b"},
		{"localhost", "/api/fail"},
		{"localhost", "/gone"},
		{"localhost", "/nothing"},
		{"other.org", "/"},
	} {
		get(t, h, req.host, req.path)
	}
	if inFlight != 1 {
		t.Errorf("requests in flight inside a request = %v, want 1", inFlight)
	}

	for _, tt := range []struct {
		vhost, cluster, code string
		want                 float64
	}{
		{"site", "", "202", 1},     // direct_response
		{"site", "app", "200", 2},  // two requests to /api/
		{"site", "app", "502", 1},  // after a 103
		{"site", "gone", "503", 1}, // unknown cluster
		{"site", "", "404", 1},     // no route in the virtual host
		{"", "", "404", 1},         // no virtual host
	} {
		got := metricstest.Value(t, reg, "kest_http_requests_total",
			"listener", "main", "virtual_host", tt.vhost, "cluster", tt.cluster, "code", tt.code)
		if got != tt.want {
			t.Errorf("requests{%s, %s, %s} = %v, want %v", tt.vhost, tt.cluster, tt.code, got, tt.want)
		}
	}
	if got := metricstest.Value(t, reg, "kest_http_request_duration_seconds",
		"listener", "main", "virtual_host", "site", "cluster", "app"); got != 3 {
		t.Errorf("duration observations = %v, want 3", got)
	}
	if got := metricstest.Value(t, reg, "kest_http_requests_in_flight", "listener", "main"); got != 0 {
		t.Errorf("requests in flight after the requests = %v, want 0", got)
	}
}

func TestPathNormalization(t *testing.T) {
	listener := func(options string) string {
		return `
  - name: main
    address: 127.0.0.1:0
    http:
` + options + `
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: {prefix: /admin/}
              direct_response: {status: 403, body: "denied"}
            - match: {prefix: /}
              route: {cluster: app}
`
	}
	raw := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "cluster "+r.URL.EscapedPath())
	}
	lookupRaw := func(string) (http.Handler, bool) { return http.HandlerFunc(raw), true }

	on, err := httpx.NewHandler(loadHTTP(t, listener("")), lookupRaw, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	off, err := httpx.NewHandler(loadHTTP(t, listener("      normalize_path: false\n      merge_slashes: false")),
		lookupRaw, registry.BuildContext{Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path    string
		on, off string
	}{
		{"/public/../admin/x", "denied", "cluster /public/../admin/x"},
		{"/public/%2e%2e/admin/x", "denied", "cluster /public/%2e%2e/admin/x"},
		{"/public%2F..%2Fadmin/x", "denied", "cluster /public%2F..%2Fadmin/x"},
		{"//admin/x", "denied", "cluster //admin/x"},
		{"/admin/./x", "denied", "denied"},
		{"/a/./b//c", "cluster /a/b/c", "cluster /a/./b//c"},
		// An unchanged path keeps its encoding.
		{"/files/a%2Fb", "cluster /files/a%2Fb", "cluster /files/a%2Fb"},
	} {
		if got := get(t, on, "localhost", tt.path).Body.String(); got != tt.on {
			t.Errorf("normalized: %s → %q, want %q", tt.path, got, tt.on)
		}
		if got := get(t, off, "localhost", tt.path).Body.String(); got != tt.off {
			t.Errorf("not normalized: %s → %q, want %q", tt.path, got, tt.off)
		}
	}
}
