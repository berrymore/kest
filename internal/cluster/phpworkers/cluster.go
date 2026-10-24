// Package phpworkers implements the php_workers cluster type: a pool of
// PHP worker processes (roadrunner/http PSR-7 workers) behind HTTP routes.
package phpworkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

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
			return New(c, bc, os.Environ())
		},
	})
}

// pool is what the cluster needs from *worker.Pool.
type pool interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Exec(ctx context.Context, req worker.Payload) (worker.Payload, error)
	Health() worker.Health
	Workers() []worker.Info
	Waiting() int
	Reset(ctx context.Context) error
}

// Cluster serves HTTP requests with a pool of PHP workers.
type Cluster struct {
	pool    pool
	opts    rrhttp.Options
	maxBody int64
	logger  *slog.Logger
}

// New builds a cluster from a validated config. bc.ConfigDir resolves
// relative paths, and bc.Name labels the pool's metrics; environ is kest's
// environment. Nothing starts before Start.
func New(cfg *Config, bc registry.BuildContext, environ []string) (*Cluster, error) {
	reg := bc.MetricsRegistry()
	wc := cfg.worker(bc.ConfigDir, environ)
	wc.Observer = newPoolMetrics(reg, bc.Name)
	p, err := worker.New(wc, bc.Logger)
	if err != nil {
		return nil, err
	}
	collectPool(reg, bc.Name, p)
	return newCluster(cfg, bc.ConfigDir, p, bc.Logger), nil
}

func newCluster(cfg *Config, configDir string, p pool, logger *slog.Logger) *Cluster {
	return &Cluster{
		pool:    p,
		opts:    cfg.requestOptions(configDir),
		maxBody: int64(cfg.MaxRequestBody),
		logger:  logger,
	}
}

// Start starts the workers and returns once every one of them has booted.
func (c *Cluster) Start(ctx context.Context) error {
	return withHint(c.pool.Start(ctx))
}

// Reset implements cluster.Resetter with a rolling replacement of the workers
// (worker.Pool.Reset): a replacement that doesn't boot stops it, and the
// workers it didn't reach keep serving.
func (c *Cluster) Reset(ctx context.Context) error {
	err := c.pool.Reset(ctx)
	if errors.Is(err, worker.ErrResetRunning) {
		return cluster.ErrResetRunning
	}
	return withHint(err)
}

// withHint adds stdoutHint to an error caused by a worker printing to stdout.
func withHint(err error) error {
	if _, ok := errors.AsType[*worker.StdoutError](err); ok {
		return fmt.Errorf("%w; %s", err, stdoutHint)
	}
	return err
}

// stdoutHint explains the usual causes of a worker printing to stdout.
const stdoutHint = "hint: the command must start a RoadRunner worker and print nothing to stdout " +
	"(display_errors=stderr sends PHP errors to stderr); " +
	"Symfony reads APP_RUNTIME before .env: " +
	"set it in the cluster's env or as extra.runtime.class in composer.json"

// Serving implements cluster.Prober: false while no worker is alive. A
// degraded pool, with some workers being replaced, still serves.
func (c *Cluster) Serving() bool {
	return c.pool.Health() != worker.Unhealthy
}

// Report implements cluster.Reporter: the pool's health and queue, and every
// worker slot.
func (c *Cluster) Report() any {
	now := time.Now()
	workers := c.pool.Workers()
	r := report{Health: c.pool.Health().String(), QueueLength: c.pool.Waiting(), Workers: make([]workerReport, len(workers))}
	for i, w := range workers {
		wr := workerReport{Slot: w.Slot, State: w.State.String(), Pid: w.Pid, Jobs: w.Jobs, MemoryBytes: w.Memory}
		if w.Pid != 0 && !w.Started.IsZero() {
			wr.StartedAt = w.Started.UTC().Format(time.RFC3339)
			wr.AgeSeconds = int64(now.Sub(w.Started).Seconds())
		}
		r.Workers[i] = wr
	}
	return r
}

type report struct {
	Health      string         `json:"health"` // healthy, degraded or unhealthy
	QueueLength int            `json:"queue_length"`
	Workers     []workerReport `json:"workers"`
}

type workerReport struct {
	Slot        int    `json:"slot"`
	State       string `json:"state"`
	Pid         int    `json:"pid,omitempty"`
	Jobs        uint64 `json:"jobs"`
	StartedAt   string `json:"started_at,omitempty"`
	AgeSeconds  int64  `json:"age_seconds,omitempty"`
	MemoryBytes int64  `json:"memory_bytes,omitempty"`
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
	defer c.cleanup(req)
	for _, err := range req.UploadErrors() {
		c.logger.Warn("upload not stored", "method", r.Method, "path", r.URL.Path, "err", err)
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

// cleanup removes the request's upload temp files. If the caller gave up
// while the worker still runs the job, they go away under it; its answer is
// dropped anyway.
func (c *Cluster) cleanup(req *rrhttp.Request) {
	if err := req.Close(); err != nil {
		c.logger.Warn("remove upload temp files", "err", err)
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
