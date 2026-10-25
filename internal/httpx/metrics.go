package httpx

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/metrics"
)

// listenerMetrics holds a listener's HTTP metrics. Requests are counted by
// virtual host, route name and cluster.
type listenerMetrics struct {
	listener string
	requests *metrics.CounterVec
	duration *metrics.HistogramVec
	inFlight *metrics.Gauge
	targets  map[[3]string]*targetMetrics // by virtual host, route and cluster, while building
}

func newListenerMetrics(reg *metrics.Registry, listener string) *listenerMetrics {
	return &listenerMetrics{
		listener: listener,
		requests: reg.NewCounterVec("kest_http_requests_total",
			"HTTP requests by listener, virtual host, route, cluster and status code. The route is the name of the "+
				"route the router chose, empty if it has none. The cluster is set when the request reached a route's "+
				"cluster: it is empty for direct responses, redirects, answers of filters, and requests that match no "+
				"route; the virtual host is empty when none matches or a listener filter answered.",
			"listener", "virtual_host", "route", "cluster", "code"),
		duration: reg.NewHistogramVec("kest_http_request_duration_seconds",
			"Time from choosing an HTTP request's virtual host to its handler's return, in seconds; for requests "+
				"that a listener filter answers, from the listener.",
			metrics.DurationBuckets, "listener", "virtual_host", "route", "cluster"),
		inFlight: reg.NewGaugeVec("kest_http_requests_in_flight",
			"HTTP requests being served, by listener.", "listener").With(listener),
		targets: make(map[[3]string]*targetMetrics),
	}
}

// target returns the metrics of requests to a virtual host, route and cluster.
func (m *listenerMetrics) target(virtualHost, route, cluster string) *targetMetrics {
	key := [3]string{virtualHost, route, cluster}
	if t, ok := m.targets[key]; ok {
		return t
	}
	t := &targetMetrics{
		requests: m.requests,
		labels:   []string{m.listener, virtualHost, route, cluster},
		duration: m.duration.With(m.listener, virtualHost, route, cluster),
	}
	m.targets[key] = t
	return t
}

// entry wraps a listener's whole handler: it counts the request in flight and
// gives it a RequestInfo. With listener filters, which may answer a request
// before the router sees it (a CORS preflight), it counts such requests
// without a virtual host, route or cluster, timed from here.
func (m *listenerMetrics) entry(h http.Handler, filters bool) http.Handler {
	if !filters {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.inFlight.Inc()
			defer m.inFlight.Dec()
			h.ServeHTTP(w, WithRequestInfo(r))
		})
	}
	unrouted := m.target("", "", "")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inFlight.Inc()
		defer m.inFlight.Dec()
		r = WithRequestInfo(r)
		start := time.Now()
		rw := NewResponseWriter(w)
		h.ServeHTTP(rw, r)
		if !Info(r).counted {
			unrouted.observe(rw.Status(), time.Since(start))
		}
	})
}

// targetMetrics are the series of one virtual host, route and cluster. The
// counter of each status code is resolved on its first use and kept in codes,
// so the request path does no map lookup.
type targetMetrics struct {
	requests *metrics.CounterVec
	labels   []string // listener, virtual host, route, cluster
	duration *metrics.Histogram
	codes    [1000]atomic.Pointer[metrics.Counter] // net/http allows status codes 100–999
}

// instrument counts and times the requests that h serves: under the target
// of the route they were routed to (or of its cluster), if any, or else under
// t. It records the virtual host in the request's RequestInfo. A request
// whose handler panics counts as a 500 unless it wrote another status.
func (t *targetMetrics) instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := Info(r)
		info.VirtualHost, info.Route, info.Cluster, info.target = t.labels[1], t.labels[2], t.labels[3], nil
		info.counted = true
		start := time.Now()
		rw := NewResponseWriter(w)
		observe := func(code int) {
			target := t
			if info.target != nil {
				target = info.target
			}
			target.observe(code, time.Since(start))
		}
		defer func() {
			if v := recover(); v != nil {
				code := http.StatusInternalServerError
				if rw.Written() {
					code = rw.Status()
				}
				observe(code)
				panic(v)
			}
		}()
		h.ServeHTTP(rw, r)
		observe(rw.Status())
	})
}

// chosen marks the requests that reach h, a route's filters and action, so
// that instrument counts them under t and RequestInfo names the route.
func (t *targetMetrics) chosen(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := Info(r)
		info.Route, info.target = t.labels[2], t
		h.ServeHTTP(w, r)
	})
}

// routed marks the requests that reach h, a route's cluster, so that
// instrument counts them under t and RequestInfo names the cluster. A
// request that a filter answers before it gets here counts without one.
func (t *targetMetrics) routed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := Info(r)
		info.Cluster, info.target = t.labels[3], t
		h.ServeHTTP(w, r)
	})
}

func (t *targetMetrics) observe(code int, d time.Duration) {
	if code < 100 || code >= len(t.codes) {
		code = http.StatusOK // unreachable: net/http rejects other codes
	}
	c := t.codes[code].Load()
	if c == nil {
		// Concurrent first requests resolve the same counter.
		c = t.requests.With(append(t.labels[:4:4], strconv.Itoa(code))...)
		t.codes[code].Store(c)
	}
	c.Inc()
	t.duration.Observe(d.Seconds())
}
