// Package phpworkers implements the php_workers cluster type: a pool of
// PHP worker processes (roadrunner/http PSR-7 workers) behind HTTP routes.
package phpworkers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/rrhttp"
	"github.com/berrymore/kest/internal/worker"
)

// Type is the cluster type name in configuration.
const Type = "php_workers"

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
				return nil, errors.New("php_workers: unexpected config type")
			}
			return New(c, bc.ConfigDir, os.Environ(), bc.Logger)
		},
	})
}

// pool is what the cluster needs from *worker.Pool.
type pool interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Exec(ctx context.Context, req worker.Payload) (worker.Payload, error)
}

// Cluster serves HTTP requests with a pool of PHP workers.
type Cluster struct {
	pool    pool
	opts    rrhttp.Options
	maxBody int64
	logger  *slog.Logger
}

// New builds a cluster from a validated config. configDir resolves relative
// paths; environ is kest's environment. Nothing starts before Start.
func New(cfg *Config, configDir string, environ []string, logger *slog.Logger) (*Cluster, error) {
	p, err := worker.New(cfg.worker(configDir, environ), logger)
	if err != nil {
		return nil, err
	}
	return newCluster(cfg, p, logger), nil
}

func newCluster(cfg *Config, p pool, logger *slog.Logger) *Cluster {
	return &Cluster{
		pool:    p,
		opts:    cfg.requestOptions(),
		maxBody: int64(cfg.MaxRequestBody),
		logger:  logger,
	}
}

// Start starts the workers and returns once every one of them has booted.
func (c *Cluster) Start(ctx context.Context) error {
	return c.pool.Start(ctx)
}

// Stop lets running requests finish, then stops the workers.
func (c *Cluster) Stop(ctx context.Context) error {
	return c.pool.Stop(ctx)
}

// ServeHTTP passes the request to a worker and writes its response.
func (c *Cluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, c.maxBody)
	req, err := rrhttp.NewRequest(r, c.opts)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	payload, err := req.Payload(c.opts.Codec)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	out, err := c.pool.Exec(r.Context(), payload)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	resp, err := rrhttp.DecodeResponse(out)
	if err != nil {
		c.fail(w, r, err)
		return
	}

	dropped, err := rrhttp.WriteResponse(w, r, resp)
	if dropped > 0 {
		c.logger.Debug("dropped invalid response headers", "method", r.Method, "path", r.URL.Path, "count", dropped)
	}
	if err != nil {
		c.logger.Debug("write response", "method", r.Method, "path", r.URL.Path, "err", err)
	}
}

// fail answers with the status for err and logs it. Nothing is written once
// the client has gone.
func (c *Cluster) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(r.Context().Err(), context.Canceled) {
		c.logger.Debug("client went away", "method", r.Method, "path", r.URL.Path, "err", err)
		return
	}
	status := rrhttp.WriteError(w, err)
	level := slog.LevelError
	switch {
	case status < http.StatusInternalServerError:
		level = slog.LevelDebug // the client's problem
	case status == http.StatusServiceUnavailable, status == http.StatusGatewayTimeout:
		level = slog.LevelWarn
	}
	c.logger.Log(r.Context(), level, "request failed", "method", r.Method, "path", r.URL.Path, "status", status, "err", err)
}
