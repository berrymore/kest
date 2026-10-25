package httpx

import (
	"context"
	"net"
	"net/http"
)

// RequestInfo is what kest learns about a request while it serves it, for
// filters that report on it (access_log). The listener puts one in every
// request's context before the filters run, so a filter sees what the router
// and later filters fill in once the inner handler returns, whatever the
// filter order. Only the request's own goroutine uses it.
type RequestInfo struct {
	RequestID   string // set by the request_id filter
	VirtualHost string // set by the router; empty if none matched
	Route       string // the name of the route the router chose; empty if it has none or none matched
	Cluster     string // set by the router when the request reaches a route's cluster; empty otherwise
	// ClientIP and Scheme are what a trusted proxy said about the client
	// (the client_ip filter); empty when nothing did. Read them through
	// ClientIP and Scheme.
	ClientIP string
	Scheme   string // "http" or "https"

	target  *targetMetrics // the metrics of the chosen route, then of its cluster; nil until a route is chosen
	counted bool           // the router counts the request: it reached a virtual host, or none matched
}

type infoKey struct{}

// Info returns the request's RequestInfo. Outside a listener (in tests of a
// single filter, without WithRequestInfo) it returns a new one that nothing
// else sees.
func Info(r *http.Request) *RequestInfo {
	if info, ok := r.Context().Value(infoKey{}).(*RequestInfo); ok {
		return info
	}
	return &RequestInfo{}
}

// WithRequestInfo returns r with a new RequestInfo in its context, as a
// listener gives every request.
func WithRequestInfo(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), infoKey{}, &RequestInfo{}))
}

// ClientIP returns the IP of the client: what a trusted proxy reported, or
// else the peer's address without the port.
func ClientIP(r *http.Request) string {
	if ip := Info(r).ClientIP; ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// Scheme returns the scheme the client used: what a trusted proxy reported,
// or else "https" on a TLS connection and "http" otherwise.
func Scheme(r *http.Request) string {
	switch {
	case Info(r).Scheme != "":
		return Info(r).Scheme
	case r.TLS != nil:
		return "https"
	default:
		return "http"
	}
}

// ResponseWriter records the status code and the body size of a response.
// It passes Flush and http.ResponseController calls through.
type ResponseWriter struct {
	http.ResponseWriter
	code  int
	bytes int64
}

// NewResponseWriter wraps w.
func NewResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{ResponseWriter: w}
}

// WriteHeader records the first final status code.
func (w *ResponseWriter) WriteHeader(code int) {
	// 1xx responses other than 101 are informational: the final one follows.
	if w.code == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *ResponseWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush passes through to the connection, for handlers that stream.
func (w *ResponseWriter) Flush() {
	if w.code == 0 {
		w.code = http.StatusOK // flushing sends the header
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *ResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Written reports whether the status line has been sent.
func (w *ResponseWriter) Written() bool {
	return w.code != 0
}

// Status is the response's status code: 200 if the handler wrote nothing,
// as net/http then sends.
func (w *ResponseWriter) Status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// BytesWritten is the size of the body written so far.
func (w *ResponseWriter) BytesWritten() int64 {
	return w.bytes
}
