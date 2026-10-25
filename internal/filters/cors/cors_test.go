package cors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/config"
)

// serve sends a request through the filter (headers as "Name: value") and
// returns the response and whether the application saw the request.
func serve(t *testing.T, cfg Config, method string, headers ...string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	reached := false
	h := New(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Access-Control-Allow-Origin", "https://set-by-the-app.test")
		_, _ = w.Write([]byte("app"))
	}))
	req := httptest.NewRequestWithContext(t.Context(), method, "/api", http.NoBody)
	for _, line := range headers {
		name, value, _ := strings.Cut(line, ": ")
		req.Header.Add(name, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reached
}

func corsHeaders(rec *httptest.ResponseRecorder) string {
	var out []string
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers", "Access-Control-Allow-Credentials", "Access-Control-Expose-Headers",
		"Access-Control-Max-Age", "Vary"} {
		if v := rec.Header().Values(name); len(v) > 0 {
			out = append(out, name+": "+strings.Join(v, "; "))
		}
	}
	return strings.Join(out, "\n")
}

func TestCORS(t *testing.T) {
	sites := DefaultConfig()
	sites.AllowOrigins = []string{"https://app.example.com", "https://*.example.org"}
	sites.AllowMethods = []string{"GET", "PUT"}
	sites.AllowHeaders = []string{"Content-Type", "X-Token"}
	sites.ExposeHeaders = []string{"X-Total"}
	sites.AllowCredentials = true

	public := DefaultConfig()
	public.AllowOrigins = []string{"*"}
	public.AllowHeaders = []string{"*"}
	public.MaxAge = 0

	preflight := func(origin, method, headers string) []string {
		out := []string{"Origin: " + origin, "Access-Control-Request-Method: " + method}
		if headers != "" {
			out = append(out, "Access-Control-Request-Headers: "+headers)
		}
		return out
	}
	vary := "Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers"

	for _, tt := range []struct {
		name    string
		cfg     Config
		method  string
		headers []string
		reached bool
		want    string
	}{
		{"preflight allowed", sites, http.MethodOptions, preflight("https://app.example.com", "PUT", "content-type, X-Token"), false,
			"Access-Control-Allow-Origin: https://app.example.com\nAccess-Control-Allow-Methods: GET, PUT\n" +
				"Access-Control-Allow-Headers: content-type, x-token\nAccess-Control-Allow-Credentials: true\n" +
				"Access-Control-Max-Age: 300\n" + vary},
		{"preflight from a subdomain", sites, http.MethodOptions, preflight("https://a.b.example.org", "GET", ""), false,
			"Access-Control-Allow-Origin: https://a.b.example.org\nAccess-Control-Allow-Methods: GET, PUT\n" +
				"Access-Control-Allow-Credentials: true\nAccess-Control-Max-Age: 300\n" + vary},
		{"preflight from the wildcard's apex", sites, http.MethodOptions, preflight("https://example.org", "GET", ""), false, vary},
		{"preflight from another scheme", sites, http.MethodOptions, preflight("http://app.example.com", "GET", ""), false, vary},
		{"preflight with a method not allowed", sites, http.MethodOptions, preflight("https://app.example.com", "DELETE", ""), false, vary},
		{"preflight with a header not allowed", sites, http.MethodOptions, preflight("https://app.example.com", "GET", "X-Other"), false, vary},
		{"plain OPTIONS reaches the app", sites, http.MethodOptions, []string{"Origin: https://app.example.com"}, true,
			"Access-Control-Allow-Origin: https://app.example.com\nAccess-Control-Allow-Credentials: true\n" +
				"Access-Control-Expose-Headers: X-Total\nVary: Origin"},
		{"request from an allowed origin", sites, http.MethodGet, []string{"Origin: https://APP.example.com"}, true,
			"Access-Control-Allow-Origin: https://APP.example.com\nAccess-Control-Allow-Credentials: true\n" +
				"Access-Control-Expose-Headers: X-Total\nVary: Origin"},
		{"request from another origin", sites, http.MethodGet, []string{"Origin: https://evil.test"}, true,
			"Access-Control-Allow-Origin: https://set-by-the-app.test\nVary: Origin"},
		{"request without an origin", sites, http.MethodGet, nil, true,
			"Access-Control-Allow-Origin: https://set-by-the-app.test\nVary: Origin"},
		{"public preflight", public, http.MethodOptions, preflight("https://any.test", "POST", "X-Anything"), false,
			"Access-Control-Allow-Origin: *\nAccess-Control-Allow-Methods: GET, HEAD, POST\n" +
				"Access-Control-Allow-Headers: x-anything\n" + vary},
		{"public request", public, http.MethodGet, []string{"Origin: https://any.test"}, true, "Access-Control-Allow-Origin: *"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec, reached := serve(t, tt.cfg, tt.method, tt.headers...)
			if reached != tt.reached {
				t.Errorf("reached the application: %v, want %v", reached, tt.reached)
			}
			if !reached && rec.Code != http.StatusNoContent {
				t.Errorf("preflight status %d, want 204", rec.Code)
			}
			if got := corsHeaders(rec); got != tt.want {
				t.Errorf("headers:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	c := Config{
		AllowOrigins:     []string{"*", "app.example.com", "https://a.*.example.com", "https://x.test/path", "https://*.ok.test"},
		AllowMethods:     []string{"GET", "BAD METHOD"},
		AllowHeaders:     []string{"*", "X Bad"},
		ExposeHeaders:    []string{"*"},
		AllowCredentials: true,
		MaxAge:           config.Duration(-time.Second),
	}
	want := []string{
		`allow_origins[0]: "*" can't be used with allow_credentials: list the origins`,
		`allow_origins[1]: invalid origin "app.example.com": want scheme://host[:port], such as https://app.example.com`,
		`allow_origins[2]: invalid origin "https://a.*.example.com": a wildcard is only allowed as "*." at the start of the host`,
		`allow_origins[3]: invalid origin "https://x.test/path": want scheme://host[:port], such as https://app.example.com`,
		`allow_methods[1]: invalid method "BAD METHOD"`,
		`allow_headers[1]: invalid header name "X Bad"`,
		`expose_headers[0]: invalid header name "*"`,
		`max_age: must not be negative`,
	}
	if err := c.Validate(); err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Validate() =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
	empty := DefaultConfig()
	if err := empty.Validate(); err == nil || !strings.HasPrefix(err.Error(), "allow_origins: at least one origin") {
		t.Errorf("Validate() without origins = %v", err)
	}
}
