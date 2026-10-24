package requestid

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/httpx"
)

var generated = regexp.MustCompile(`^[0-9a-f]{32}$`)

// serve runs a request with the incoming header value (none if empty)
// through the filter and returns the ID the handler saw, the response's and
// the one in RequestInfo.
func serve(t *testing.T, cfg Config, incoming string) (seen, response, info string) {
	t.Helper()
	req := httpx.WithRequestInfo(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if incoming != "" {
		req.Header.Set(cfg.Header, incoming)
	}
	var got *http.Request
	h := New(cfg)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if req.Header.Get(cfg.Header) != incoming {
		t.Error("the filter changed the request it got")
	}
	return got.Header.Get(cfg.Header), rec.Header().Get(cfg.Header), httpx.Info(got).RequestID
}

func TestRequestID(t *testing.T) {
	trusting := DefaultConfig()
	trusting.TrustIncoming = true
	custom := Config{Header: "x-trace-id"}
	long := strings.Repeat("a", maxIncoming+1)

	for _, tt := range []struct {
		name     string
		cfg      Config
		incoming string
		want     string // "" means a generated ID
	}{
		{name: "generated", cfg: DefaultConfig()},
		{name: "incoming ignored by default", cfg: DefaultConfig(), incoming: "client-chosen"},
		{name: "incoming trusted", cfg: trusting, incoming: "abc-123", want: "abc-123"},
		{name: "invalid incoming", cfg: trusting, incoming: "has space"},
		{name: "too long incoming", cfg: trusting, incoming: long},
		{name: "custom header", cfg: custom},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seen, response, info := serve(t, tt.cfg, tt.incoming)
			if seen != response || seen != info {
				t.Errorf("IDs differ: handler %q, response %q, RequestInfo %q", seen, response, info)
			}
			if tt.want != "" && seen != tt.want {
				t.Errorf("ID = %q, want %q", seen, tt.want)
			}
			if tt.want == "" && !generated.MatchString(seen) {
				t.Errorf("ID = %q, want 32 generated hex digits", seen)
			}
		})
	}

	a, _, _ := serve(t, DefaultConfig(), "")
	b, _, _ := serve(t, DefaultConfig(), "")
	if a == b {
		t.Errorf("two requests got the same ID %s", a)
	}
}

func TestValidate(t *testing.T) {
	for header, ok := range map[string]bool{"X-Request-Id": true, "x_trace.id": true, "": false, "X Request": false,
		"X-Ünïcode": false, "X:Id": false} {
		c := Config{Header: header}
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("Validate(%q) = %v, want ok %v", header, err, ok)
		}
	}
}
