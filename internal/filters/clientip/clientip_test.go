package clientip

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/httpx"
)

// resolve runs a request from peer with the given headers (name: value
// lines) through the filter and returns what it recorded, and the client IP
// and scheme the handler sees.
func resolve(t *testing.T, cfg Config, peer string, headers ...string) (ip, scheme string) {
	t.Helper()
	f, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httpx.WithRequestInfo(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	req.RemoteAddr = peer
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		req.Header.Add(name, value)
	}
	f(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ip, scheme = httpx.ClientIP(r), httpx.Scheme(r)
	})).ServeHTTP(httptest.NewRecorder(), req)
	return ip, scheme
}

func TestClientIP(t *testing.T) {
	lb := Config{TrustedProxies: []string{"10.0.0.0/8", "2001:db8:1::/48", "192.0.2.1"}, Header: xff}
	fwd := Config{TrustedProxies: lb.TrustedProxies, Header: "forwarded"}
	realIP := Config{TrustedProxies: lb.TrustedProxies, Header: "X-Real-Ip"}

	for _, tt := range []struct {
		name    string
		cfg     Config
		peer    string
		headers []string
		ip      string
		scheme  string
	}{
		{"untrusted peer", lb, "203.0.113.9:4000", []string{"X-Forwarded-For: 198.51.100.1", "X-Forwarded-Proto: https"},
			"203.0.113.9", "http"},
		{"trusted peer, no header", lb, "10.1.2.3:4000", nil, "10.1.2.3", "http"},
		{"one hop", lb, "10.1.2.3:4000", []string{"X-Forwarded-For: 198.51.100.1"}, "198.51.100.1", "http"},
		{"spoofed by the client", lb, "10.1.2.3:4000", []string{"X-Forwarded-For: 1.1.1.1, 198.51.100.1"},
			"198.51.100.1", "http"},
		{"chain of trusted proxies", lb, "10.1.2.3:4000", []string{"X-Forwarded-For: 198.51.100.1, 10.9.9.9", "X-Forwarded-For: 192.0.2.1"},
			"198.51.100.1", "http"},
		{"all hops trusted", lb, "10.1.2.3:4000", []string{"X-Forwarded-For: 10.0.0.1, 10.0.0.2"}, "10.0.0.1", "http"},
		{"garbage stops the walk", lb, "10.1.2.3:4000", []string{"X-Forwarded-For: 198.51.100.1, nonsense, 10.0.0.5"},
			"10.0.0.5", "http"},
		{"addresses with ports and IPv6", lb, "[2001:db8:1::5]:443", []string{"X-Forwarded-For: [2001:db8:2::7]:1234"},
			"2001:db8:2::7", "http"},
		{"v4-mapped peer", lb, "[::ffff:10.1.2.3]:4000", []string{"X-Forwarded-For: 198.51.100.1"}, "198.51.100.1", "http"},
		{"scheme", lb, "10.1.2.3:4000", []string{"X-Forwarded-Proto: HTTPS"}, "10.1.2.3", "https"},
		{"ambiguous scheme ignored", lb, "10.1.2.3:4000", []string{"X-Forwarded-Proto: https, http"}, "10.1.2.3", "http"},
		{"forwarded", fwd, "10.1.2.3:4000", []string{`Forwarded: for=1.1.1.1, for="[2001:db8:2::7]:4711";proto=https`},
			"2001:db8:2::7", "http"},
		{"forwarded, obfuscated", fwd, "10.1.2.3:4000", []string{"Forwarded: for=_hidden;by=10.0.0.1"}, "10.1.2.3", "http"},
		{"one-address header", realIP, "10.1.2.3:4000", []string{"X-Real-Ip: 198.51.100.4"}, "198.51.100.4", "http"},
		{"one-address header, last line", realIP, "10.1.2.3:4000", []string{"X-Real-Ip: 1.1.1.1", "X-Real-Ip: 198.51.100.4"},
			"198.51.100.4", "http"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ip, scheme := resolve(t, tt.cfg, tt.peer, tt.headers...)
			if ip != tt.ip || scheme != tt.scheme {
				t.Errorf("client %s %s, want %s %s", ip, scheme, tt.ip, tt.scheme)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	c := Config{TrustedProxies: []string{"10.0.0.0/8", "::1", "10.0.0.0/33", "localhost"}, Header: "X Real"}
	want := []string{
		`trusted_proxies[2]: invalid network "10.0.0.0/33"`,
		`trusted_proxies[3]: invalid address "localhost": want an IP address or a network such as 10.0.0.0/8`,
		`header: must be a valid HTTP header name, got "X Real"`,
	}
	if err := c.Validate(); err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Validate() =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
	empty := DefaultConfig()
	if err := empty.Validate(); err == nil || !strings.HasPrefix(err.Error(), "trusted_proxies: at least one") {
		t.Errorf("Validate() without trusted proxies = %v", err)
	}
}
