package httpx

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/berrymore/kest/internal/config"
)

// redirect answers with a redirect to the request's URL with the parts that
// cfg sets replaced. prefix is the route's match.prefix, which
// cfg.PrefixRewrite replaces.
func redirect(cfg *config.Redirect, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The host comes from the request unless cfg sets one, as in nginx's
		// "return 301 https://$host$request_uri": a client can only send
		// itself elsewhere, and only to hosts its virtual host matched.
		http.Redirect(w, r, redirectURL(cfg, prefix, r), cfg.Status) //nolint:gosec // see above
	})
}

func redirectURL(cfg *config.Redirect, prefix string, r *http.Request) string {
	scheme := Scheme(r)
	host, port := splitHost(r.Host)
	if cfg.Scheme != "" {
		scheme, port = cfg.Scheme, ""
	}
	if cfg.Host != "" {
		host, port = cfg.Host, ""
	}
	if cfg.Port != 0 {
		port = strconv.Itoa(cfg.Port)
	}
	if port == defaultPort(scheme) {
		port = ""
	}
	u := url.URL{Scheme: scheme, Host: host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	if port != "" {
		u.Host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	switch {
	case cfg.Path != "":
		u.Path, u.RawPath = cfg.Path, ""
	case cfg.PrefixRewrite != "":
		u.Path, u.RawPath = cfg.PrefixRewrite+strings.TrimPrefix(r.URL.Path, prefix), ""
	}
	if cfg.StripQuery {
		u.RawQuery = ""
	}
	return u.String()
}

// splitHost splits a Host header into host and port; the port may be "".
func splitHost(hostport string) (host, port string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		return h, p
	}
	return hostport, ""
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}
