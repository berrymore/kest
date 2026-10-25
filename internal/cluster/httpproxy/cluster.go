// Package httpproxy implements the http_proxy cluster type: requests go to
// remote HTTP endpoints, with load balancing, active health checks and
// retries, through net/http/httputil.ReverseProxy.
package httpproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the cluster type name in configuration.
const Type = "http_proxy"

func init() {
	cluster.Types.Register(registry.Factory[cluster.Cluster]{
		Type: Type,
		NewConfig: func() any {
			c := DefaultConfig()
			return &c
		},
		Build: func(bc registry.BuildContext, cfg any) (cluster.Cluster, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("http_proxy: unexpected config type")
			}
			return New(c, bc)
		},
	})
}

// endpoint is one upstream server.
type endpoint struct {
	url     *url.URL // scheme and host
	name    string   // the configured URL, for metrics and logs
	healthy atomic.Bool
	active  atomic.Int64 // requests in flight

	codes    [600]atomic.Pointer[metrics.Counter] // kest_upstream_requests_total by status, resolved on first use
	errors   map[string]*metrics.Counter          // kest_upstream_errors_total by reason
	requests *metrics.CounterVec
	cluster  string
}

func (e *endpoint) countStatus(code int) {
	if code < 100 || code >= len(e.codes) {
		code = 0
	}
	c := e.codes[code].Load()
	if c == nil {
		c = e.requests.With(e.cluster, e.name, strconv.Itoa(code))
		e.codes[code].Store(c)
	}
	c.Inc()
}

// Cluster forwards requests to remote endpoints.
type Cluster struct {
	name      string
	cfg       Config
	endpoints []*endpoint
	next      atomic.Uint64 // round robin position
	transport *http.Transport
	proxy     *httputil.ReverseProxy
	logger    *slog.Logger
	retries   *metrics.Counter
	retryOn   map[string]bool

	// The health checks, while running: a context canceled by Stop.
	stopChecks context.CancelFunc
	checks     sync.WaitGroup
}

// New builds a cluster from a validated config. It reads tls.ca_file,
// relative to bc.ConfigDir; nothing connects before Start.
func New(cfg *Config, bc registry.BuildContext) (*Cluster, error) {
	c := &Cluster{name: bc.Name, cfg: *cfg, logger: bc.Logger, retryOn: map[string]bool{}}
	for _, on := range cfg.Retries.On {
		c.retryOn[on] = true
	}
	reg := bc.MetricsRegistry()
	requests := reg.NewCounterVec("kest_upstream_requests_total",
		"Responses from http_proxy endpoints by status code, retried attempts included.", "cluster", "endpoint", "code")
	errs := reg.NewCounterVec("kest_upstream_errors_total",
		"Requests to http_proxy endpoints that got no response: connect_failure, timeout or reset.",
		"cluster", "endpoint", "reason")
	c.retries = reg.NewCounterVec("kest_upstream_retries_total",
		"Requests that http_proxy sent again to another endpoint.", "cluster").With(bc.Name)
	for _, raw := range cfg.Endpoints {
		u, err := parseEndpoint(raw)
		if err != nil {
			return nil, err
		}
		e := &endpoint{url: u, name: raw, requests: requests, cluster: bc.Name, errors: map[string]*metrics.Counter{}}
		for _, reason := range []string{reasonConnect, reasonTimeout, reasonReset} {
			e.errors[reason] = errs.With(bc.Name, raw, reason)
		}
		e.healthy.Store(true)
		c.endpoints = append(c.endpoints, e)
	}
	reg.NewCollector("kest_upstream_endpoint_healthy",
		"1 while an http_proxy endpoint passes its health checks (always 1 without them).", metrics.TypeGauge,
		[]string{"cluster", "endpoint"}, func(emit func(float64, ...string)) {
			for _, e := range c.endpoints {
				emit(boolValue(e.healthy.Load()), bc.Name, e.name)
			}
		})
	reg.NewCollector("kest_upstream_requests_active", "Requests in flight to each http_proxy endpoint.",
		metrics.TypeGauge, []string{"cluster", "endpoint"}, func(emit func(float64, ...string)) {
			for _, e := range c.endpoints {
				emit(float64(e.active.Load()), bc.Name, e.name)
			}
		})

	t, err := newTransport(cfg, bc.ConfigDir)
	if err != nil {
		return nil, err
	}
	c.transport = t
	c.proxy = &httputil.ReverseProxy{
		Rewrite:      c.rewrite,
		Transport:    roundTripper{c},
		ErrorHandler: c.fail,
		ErrorLog:     log.New(logWriter{bc.Logger}, "", 0),
	}
	return c, nil
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// newTransport returns the connection pool to the endpoints.
func newTransport(cfg *Config, configDir string) (*http.Transport, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.TLS.ServerName,
		InsecureSkipVerify: cfg.TLS.InsecureSkipVerify, //nolint:gosec // the operator's explicit choice, documented as for development
	}
	if cfg.TLS.CAFile != "" {
		name := cfg.TLS.CAFile
		if configDir != "" && !filepath.IsAbs(name) {
			name = filepath.Join(configDir, name)
		}
		pem, err := os.ReadFile(name) //nolint:gosec // the operator's file
		if err != nil {
			return nil, &config.FieldError{Field: "tls.ca_file", Msg: err.Error()}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, &config.FieldError{Field: "tls.ca_file", Msg: "no PEM certificates in " + name}
		}
		tlsConfig.RootCAs = pool
	}
	timeout := cfg.ConnectTimeout.Std()
	t := &http.Transport{
		Proxy:                 nil, // never through HTTP_PROXY: the endpoints are the configured ones
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   timeout,
		MaxConnsPerHost:       cfg.MaxConnections,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnections,
		MaxIdleConns:          cfg.MaxIdleConnections * len(cfg.Endpoints),
		IdleConnTimeout:       cfg.IdleTimeout.Std(),
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true, // Accept-Encoding passes through as the client sent it
	}
	var p http.Protocols
	if cfg.HTTP2 {
		p.SetHTTP2(true)
		p.SetUnencryptedHTTP2(true)
	} else {
		p.SetHTTP1(true)
	}
	t.Protocols = &p
	return t, nil
}

// rewrite prepares the outgoing request; roundTripper picks the endpoint.
func (c *Cluster) rewrite(pr *httputil.ProxyRequest) {
	pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "upstream" // replaced per attempt
	if c.cfg.Host != "" {
		pr.Out.Host = c.cfg.Host
	}
	// Each proxy appends the address it got the request from.
	pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
	pr.SetXForwarded()
	pr.Out.Header.Set("X-Forwarded-Proto", httpx.Scheme(pr.In))
}

// fail answers a request that got no response: 503 when no connection could
// be made, 504 at the route timeout, 502 otherwise. Nothing is written for a
// client that went away.
func (c *Cluster) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(r.Context().Err(), context.Canceled) {
		c.logger.Debug("client went away", "method", r.Method, "path", r.URL.Path, "err", err)
		return
	}
	status := http.StatusBadGateway
	switch {
	case errors.Is(r.Context().Err(), context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case classify(err) == reasonConnect:
		status = http.StatusServiceUnavailable
	}
	c.logger.Warn("upstream request failed", "method", r.Method, "path", r.URL.Path, "status", status, "err", err)
	http.Error(w, http.StatusText(status), status)
}

// logWriter takes ReverseProxy's own log lines.
type logWriter struct {
	logger *slog.Logger
}

func (w logWriter) Write(p []byte) (int, error) {
	w.logger.Warn(string(trimNewline(p)))
	return len(p), nil
}

func trimNewline(p []byte) []byte {
	if n := len(p); n > 0 && p[n-1] == '\n' {
		return p[:n-1]
	}
	return p
}

// ServeHTTP forwards the request.
func (c *Cluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.proxy.ServeHTTP(w, r)
}

// Start runs one round of health checks, if configured, and keeps checking
// in the background. It doesn't fail when endpoints are down: they may come
// up later.
func (c *Cluster) Start(ctx context.Context) error {
	if c.cfg.HealthCheck.Path == "" {
		return nil
	}
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.stopChecks = cancel
	var first sync.WaitGroup
	for _, e := range c.endpoints {
		first.Go(func() { e.healthy.Store(c.check(hctx, e)) })
	}
	first.Wait()
	for _, e := range c.endpoints {
		if !e.healthy.Load() {
			c.logger.Warn("endpoint unhealthy", "endpoint", e.name)
		}
		c.checks.Go(func() { c.watch(hctx, e) })
	}
	return nil
}

// Stop stops the health checks and closes idle connections; requests in
// flight finish on their own.
func (c *Cluster) Stop(context.Context) error {
	if c.stopChecks != nil {
		c.stopChecks()
		c.checks.Wait()
	}
	c.transport.CloseIdleConnections()
	return nil
}

// Report implements cluster.Reporter: the policy and every endpoint.
func (c *Cluster) Report() any {
	r := report{LoadBalancing: c.cfg.LoadBalancing, Endpoints: make([]endpointReport, len(c.endpoints))}
	for i, e := range c.endpoints {
		r.Endpoints[i] = endpointReport{Endpoint: e.name, Healthy: e.healthy.Load(), Active: e.active.Load()}
	}
	return r
}

type report struct {
	LoadBalancing string           `json:"load_balancing"`
	Endpoints     []endpointReport `json:"endpoints"`
}

type endpointReport struct {
	Endpoint string `json:"endpoint"`
	Healthy  bool   `json:"healthy"`
	Active   int64  `json:"active"`
}

// pick chooses an endpoint for a request, avoiding those in tried. Unhealthy
// endpoints are skipped, unless none is healthy: then all are used (Envoy's
// panic mode), since failing every request helps nobody.
func (c *Cluster) pick(tried []*endpoint) *endpoint {
	candidates := make([]*endpoint, 0, len(c.endpoints))
	for _, healthyOnly := range []bool{true, false} {
		for _, e := range c.endpoints {
			if (!healthyOnly || e.healthy.Load()) && !slices.Contains(tried, e) {
				candidates = append(candidates, e)
			}
		}
		if len(candidates) > 0 {
			break
		}
	}
	if len(candidates) == 0 {
		candidates = c.endpoints // every one was tried: start over
	}
	switch c.cfg.LoadBalancing {
	case Random:
		return candidates[rand.IntN(len(candidates))] //nolint:gosec // load balancing, not security
	case LeastRequest:
		start := c.turn(len(candidates))
		best := candidates[start]
		for i := range candidates {
			if e := candidates[(start+i)%len(candidates)]; e.active.Load() < best.active.Load() {
				best = e
			}
		}
		return best
	default:
		return candidates[c.turn(len(candidates))]
	}
}

// turn advances the round robin position and returns it modulo n.
func (c *Cluster) turn(n int) int {
	return int(c.next.Add(1) % uint64(n)) //nolint:gosec // the result is below n, an int
}

// describe is for error messages: the endpoint an attempt went to.
func describe(e *endpoint, err error) error {
	return fmt.Errorf("%s: %w", e.name, err)
}
