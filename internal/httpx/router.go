package httpx

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/registry"
)

// Router selects a virtual host by the request's Host and runs its filters,
// then selects the first route whose match fits the request path and runs
// the route's filters and its action. Requests are counted by virtual host,
// route name and cluster.
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
	routes  []route
	handler http.Handler // the virtual host's filters, then dispatch; counted
}

type route struct {
	prefix  string
	path    string
	handler http.Handler // the route's filters, then its action
}

func (r *route) matches(path string) bool {
	if r.path != "" {
		return path == r.path
	}
	return strings.HasPrefix(path, r.prefix)
}

// newRouter builds a router from validated virtual hosts, counting requests
// in m and adding the configuration problems of their filters to errs. A
// route to a cluster that clusters cannot resolve answers 503.
func newRouter(vhosts []config.VirtualHost, clusters ClusterLookup, m *listenerMetrics, bc registry.BuildContext,
	errs *config.Errors) *Router {
	rt := &Router{
		exact:  make(map[string]*virtualHost),
		noHost: m.target("", "", "").instrument(http.NotFoundHandler()),
	}
	for i := range vhosts {
		cfg := &vhosts[i]
		vh := &virtualHost{}
		filters := buildFilters(cfg.Filters, bc, errs)
		for j := range cfg.Routes {
			rc := &cfg.Routes[j]
			r := newRoute(rc, clusters, errs)
			if rc.Route != nil {
				r.handler = m.target(cfg.Name, rc.Name, rc.Route.Cluster).routed(r.handler)
			}
			r.handler = Chain(r.handler, buildFilters(rc.Filters, bc, errs)...)
			if rc.Name != "" {
				r.handler = m.target(cfg.Name, rc.Name, "").chosen(r.handler)
			}
			vh.routes = append(vh.routes, r)
		}
		vh.handler = m.target(cfg.Name, "", "").instrument(Chain(http.HandlerFunc(vh.dispatch), filters...))
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

func newRoute(cfg *config.Route, clusters ClusterLookup, errs *config.Errors) route {
	r := route{prefix: cfg.Match.Prefix, path: cfg.Match.Path}
	switch {
	case cfg.DirectResponse != nil:
		r.handler = directResponse(cfg.DirectResponse.Status, cfg.DirectResponse.Body)
	case cfg.Redirect != nil:
		r.handler = redirect(cfg.Redirect, cfg.Match.Prefix)
	case cfg.Route != nil:
		a := cfg.Route
		var lane *Lane
		if a.HasLane() {
			lane = &Lane{Name: cfg.Name, MaxWorkers: a.MaxWorkers, Priority: a.Priority}
			if a.QueueTimeout != nil {
				lane.QueueTimeout = a.QueueTimeout.Std()
			}
		}
		h, err := clusters(a.Cluster, lane)
		if err != nil {
			errs.Append(fieldErrors(a, err))
		}
		if h == nil {
			h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "cluster unavailable", http.StatusServiceUnavailable)
			})
		}
		r.handler = withTimeout(h, a.Timeout.Std())
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
	vh.handler.ServeHTTP(w, r)
}

// dispatch runs the first route that matches the request's path; 404 when
// none does.
func (vh *virtualHost) dispatch(w http.ResponseWriter, r *http.Request) {
	for i := range vh.routes {
		if vh.routes[i].matches(r.URL.Path) {
			vh.routes[i].handler.ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
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

// fieldErrors locates what a cluster found wrong with a route's lane:
// *config.FieldError values (possibly joined) at their fields of the action.
func fieldErrors(a *config.RouteAction, err error) error {
	var list []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		list = joined.Unwrap()
	} else {
		list = []error{err}
	}
	var out config.Errors
	for _, e := range list {
		if fe, ok := errors.AsType[*config.FieldError](e); ok {
			out.Append(a.Errorf(fe.Field, "%s", fe.Msg))
		} else {
			out.Append(a.Errorf("", "%v", e))
		}
	}
	return out.Err()
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
