package httpx

import (
	"cmp"
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/config"
)

// Router selects a virtual host by the request's Host, then the first route
// whose match fits the request path, and runs that route's action.
type Router struct {
	exact    map[string]*virtualHost
	suffixes []suffixHost // longest suffix first
	fallback *virtualHost // the "*" domain
	noHost   http.Handler // no virtual host matches: 404, counted
}

type suffixHost struct {
	suffix string // ".example.com" for "*.example.com"
	vh     *virtualHost
}

type virtualHost struct {
	name    string
	routes  []route
	noRoute http.Handler // no route matches: 404, counted
}

type route struct {
	prefix  string
	path    string
	handler http.Handler
}

func (r *route) matches(path string) bool {
	if r.path != "" {
		return path == r.path
	}
	return strings.HasPrefix(path, r.prefix)
}

// newRouter builds a router from validated virtual hosts, counting requests
// in m. A route to a cluster that clusters cannot resolve answers 503.
func newRouter(vhosts []config.VirtualHost, clusters ClusterLookup, m *listenerMetrics) *Router {
	rt := &Router{
		exact:  make(map[string]*virtualHost),
		noHost: m.target("", "").instrument(http.NotFoundHandler()),
	}
	for i := range vhosts {
		cfg := &vhosts[i]
		vh := &virtualHost{name: cfg.Name, noRoute: m.target(cfg.Name, "").instrument(http.NotFoundHandler())}
		for j := range cfg.Routes {
			r := newRoute(&cfg.Routes[j], clusters)
			cluster := ""
			if cfg.Routes[j].Route != nil {
				cluster = cfg.Routes[j].Route.Cluster
			}
			r.handler = m.target(cfg.Name, cluster).instrument(r.handler)
			vh.routes = append(vh.routes, r)
		}
		for _, d := range cfg.Domains {
			switch {
			case d == "*":
				rt.fallback = vh
			case strings.HasPrefix(d, "*."):
				rt.suffixes = append(rt.suffixes, suffixHost{suffix: d[1:], vh: vh})
			default:
				rt.exact[d] = vh
			}
		}
	}
	slices.SortStableFunc(rt.suffixes, func(a, b suffixHost) int {
		return cmp.Compare(len(b.suffix), len(a.suffix))
	})
	return rt
}

func newRoute(cfg *config.Route, clusters ClusterLookup) route {
	r := route{prefix: cfg.Match.Prefix, path: cfg.Match.Path}
	switch {
	case cfg.DirectResponse != nil:
		r.handler = directResponse(cfg.DirectResponse.Status, cfg.DirectResponse.Body)
	case cfg.Redirect != nil:
		r.handler = redirect(cfg.Redirect, cfg.Match.Prefix)
	case cfg.Route != nil:
		h, ok := clusters(cfg.Route.Cluster)
		if !ok {
			h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "cluster unavailable", http.StatusServiceUnavailable)
			})
		}
		r.handler = withTimeout(h, cfg.Route.Timeout.Std())
	default:
		r.handler = http.NotFoundHandler()
	}
	return r
}

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vh := rt.virtualHost(requestHost(r))
	if vh == nil {
		rt.noHost.ServeHTTP(w, r)
		return
	}
	for i := range vh.routes {
		if vh.routes[i].matches(r.URL.Path) {
			vh.routes[i].handler.ServeHTTP(w, r)
			return
		}
	}
	vh.noRoute.ServeHTTP(w, r)
}

func (rt *Router) virtualHost(host string) *virtualHost {
	if vh, ok := rt.exact[host]; ok {
		return vh
	}
	for _, s := range rt.suffixes {
		if len(host) > len(s.suffix) && strings.HasSuffix(host, s.suffix) {
			return s.vh
		}
	}
	return rt.fallback
}

// requestHost returns the request's host without the port, normalized like
// configured domains.
func requestHost(r *http.Request) string {
	h := r.Host
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i >= 0 {
			h = h[:i+1]
		}
	} else if i := strings.LastIndexByte(h, ':'); i >= 0 && strings.Count(h, ":") == 1 {
		h = h[:i]
	}
	return config.NormalizeHost(h)
}

func directResponse(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if body != "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// withTimeout bounds the request context; the cluster answers 504 when it expires.
func withTimeout(h http.Handler, d time.Duration) http.Handler {
	if d <= 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
