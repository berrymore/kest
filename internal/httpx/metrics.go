package httpx

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/metrics"
)

// listenerMetrics holds a listener's HTTP metrics. Requests are counted by
// virtual host and cluster: routes have no names to label them with.
type listenerMetrics struct {
	listener string
	requests *metrics.CounterVec
	duration *metrics.HistogramVec
	inFlight *metrics.Gauge
	targets  map[[2]string]*targetMetrics // by virtual host and cluster, while building
}

func newListenerMetrics(reg *metrics.Registry, listener string) *listenerMetrics {
	return &listenerMetrics{
		listener: listener,
		requests: reg.NewCounterVec("kest_http_requests_total",
			"HTTP requests by listener, virtual host, cluster and status code. The cluster is set when the request "+
				"reached a route's cluster: it is empty for direct responses, redirects, answers of virtual host and "+
				"route filters, and requests that match no route; the virtual host is empty when none matches.",
			"listener", "virtual_host", "cluster", "code"),
		duration: reg.NewHistogramVec("kest_http_request_duration_seconds",
			"Time from choosing an HTTP request's virtual host to its handler's return, in seconds.",
			metrics.DurationBuckets, "listener", "virtual_host", "cluster"),
		inFlight: reg.NewGaugeVec("kest_http_requests_in_flight",
			"HTTP requests being served, by listener.", "listener").With(listener),
		targets: make(map[[2]string]*targetMetrics),
	}
}

// target returns the metrics of requests to a virtual host and cluster.
func (m *listenerMetrics) target(virtualHost, cluster string) *targetMetrics {
	key := [2]string{virtualHost, cluster}
	if t, ok := m.targets[key]; ok {
		return t
	}
	t := &targetMetrics{
		requests: m.requests,
		labels:   []string{m.listener, virtualHost, cluster},
		duration: m.duration.With(m.listener, virtualHost, cluster),
	}
	m.targets[key] = t
	return t
}

// entry wraps a listener's whole handler: it counts the request in flight and
// gives it a RequestInfo.
func (m *listenerMetrics) entry(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inFlight.Inc()
		defer m.inFlight.Dec()
		h.ServeHTTP(w, WithRequestInfo(r))
	})
}

// targetMetrics are the series of one virtual host and cluster. The counter of
// each status code is resolved on its first use and kept in codes, so the
// request path does no map lookup.
type targetMetrics struct {
	requests *metrics.CounterVec
	labels   []string // listener, virtual host, cluster
	duration *metrics.Histogram
	codes    [1000]atomic.Pointer[metrics.Counter] // net/http allows status codes 100–999
}

// instrument counts and times the requests that h serves: under the target
// of the cluster they were routed to, if any, or else under t. It records
// the virtual host in the request's RequestInfo. A request whose handler
// panics counts as a 500 unless it wrote another status.
func (t *targetMetrics) instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := Info(r)
		info.VirtualHost, info.Cluster, info.target = t.labels[1], t.labels[2], nil
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

// routed marks the requests that reach h, a route's cluster, so that
// instrument counts them under t and RequestInfo names the cluster. A
// request that a filter answers before it gets here counts without one.
func (t *targetMetrics) routed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := Info(r)
		info.Cluster, info.target = t.labels[2], t
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
		c = t.requests.With(append(t.labels[:3:3], strconv.Itoa(code))...)
		t.codes[code].Store(c)
	}
	c.Inc()
	t.duration.Observe(d.Seconds())
}
