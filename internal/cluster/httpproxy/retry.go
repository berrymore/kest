package httpproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
)

// Reasons a request got no response, as metric labels.
const (
	reasonConnect = "connect_failure"
	reasonTimeout = "timeout"
	reasonReset   = "reset"
)

// classify tells why a round trip failed.
func classify(err error) string {
	var opErr *net.OpError
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.As(err, &opErr) && opErr.Op == "dial", errors.As(err, &certErr), errors.As(err, &recordErr):
		return reasonConnect
	case errors.Is(err, context.DeadlineExceeded):
		return reasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return reasonTimeout
	}
	return reasonReset
}

// roundTripper sends each attempt of a request to an endpoint that the
// cluster picks, and retries on another one as Retries allows.
type roundTripper struct {
	c *Cluster
}

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c := rt.c
	var body *retryBody
	if req.Body != nil && req.Body != http.NoBody {
		body = &retryBody{ReadCloser: req.Body}
	}
	var tried []*endpoint
	for attempt := 0; ; attempt++ {
		e := c.pick(tried)
		tried = append(tried, e)
		out := *req
		u := *req.URL
		u.Scheme, u.Host = e.url.Scheme, e.url.Host
		out.URL = &u
		if body != nil {
			out.Body = body
		}

		e.active.Add(1)
		resp, err := c.transport.RoundTrip(&out)
		last := attempt >= c.cfg.Retries.Count || req.Context().Err() != nil
		if err != nil {
			e.active.Add(-1)
			if req.Context().Err() != nil {
				return nil, describe(e, err) // the client went away or the route timed out
			}
			reason := classify(err)
			e.errors[reason].Inc()
			if !last && c.retryable(req, body, reason) {
				c.retries.Inc()
				c.logger.Debug("retrying on another endpoint", "endpoint", e.name, "reason", reason, "err", err)
				continue
			}
			return nil, describe(e, err)
		}
		e.countStatus(resp.StatusCode)
		if !last && c.retryOn[RetryGatewayError] && gatewayError(resp.StatusCode) && replayable(req, body) {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			e.active.Add(-1)
			c.retries.Inc()
			c.logger.Debug("retrying on another endpoint", "endpoint", e.name, "status", resp.StatusCode)
			continue
		}
		if resp.StatusCode == http.StatusSwitchingProtocols {
			// An upgraded connection (WebSocket): ReverseProxy needs the
			// body's Write, and the connection is no longer a request.
			e.active.Add(-1)
			return resp, nil
		}
		resp.Body = &countedBody{ReadCloser: resp.Body, e: e}
		return resp, nil
	}
}

// retryable reports whether a failed attempt may go to another endpoint: a
// connection that couldn't be made sent nothing, so any request can be
// retried if its body wasn't read; a connection that broke only for requests
// that are safe to repeat.
func (c *Cluster) retryable(req *http.Request, body *retryBody, reason string) bool {
	switch reason {
	case reasonConnect:
		return c.retryOn[RetryConnectFailure] && (body == nil || !body.read.Load())
	case reasonReset:
		return c.retryOn[RetryReset] && replayable(req, body)
	default:
		return false
	}
}

func gatewayError(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// replayable reports a request that is safe to send twice: an idempotent
// method and no body.
func replayable(req *http.Request, body *retryBody) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return body == nil
	default:
		return false
	}
}

// retryBody is the request body, read by one attempt at a time. The
// transport closes a body when a round trip fails; this one stays open for
// the next attempt, and net/http closes the original when the request ends.
type retryBody struct {
	io.ReadCloser
	read atomic.Bool // some of it was sent: no attempt can start over
}

func (b *retryBody) Read(p []byte) (int, error) {
	b.read.Store(true)
	return b.ReadCloser.Read(p)
}

func (b *retryBody) Close() error { return nil }

// countedBody ends an attempt's count of requests in flight when the
// response body is closed, for least_request.
type countedBody struct {
	io.ReadCloser
	e    *endpoint
	done atomic.Bool
}

func (b *countedBody) Close() error {
	if b.done.CompareAndSwap(false, true) {
		b.e.active.Add(-1)
	}
	return b.ReadCloser.Close()
}
