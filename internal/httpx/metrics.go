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
			"HTTP requests by listener, virtual host, cluster and status code. The cluster is empty for direct "+
				"responses and requests that match no route; the virtual host is empty when none matches.",
			"listener", "virtual_host", "cluster", "code"),
		duration: reg.NewHistogramVec("kest_http_request_duration_seconds",
			"Time from routing an HTTP request to its handler's return, in seconds.",
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

// countInFlight wraps a listener's whole handler.
func (m *listenerMetrics) countInFlight(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inFlight.Inc()
		defer m.inFlight.Dec()
		h.ServeHTTP(w, r)
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

// instrument counts and times the requests that h serves.
func (t *targetMetrics) instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		t.observe(sw.status(), time.Since(start))
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

// statusWriter records the status code of a response.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	// 1xx responses other than 101 are informational: the final one follows.
	if w.code == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush passes through to the connection, for handlers that stream.
func (w *statusWriter) Flush() {
	if w.code == 0 {
		w.code = http.StatusOK // flushing sends the header
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// status is the response's status code: 200 if the handler wrote nothing,
// as net/http then sends.
func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}
