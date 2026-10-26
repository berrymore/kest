// Package phpworkers implements the php_workers cluster type: a pool of
// PHP worker processes (roadrunner/http PSR-7 workers) behind HTTP routes.
package phpworkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/metrics"
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
	ExecStream(ctx context.Context, req worker.Payload) (worker.Payload, frames, error)
	Health() worker.Health
	Workers() []worker.Info
	Waiting() int
	Reset(ctx context.Context) error
	Lane(cfg worker.LaneConfig) (lane, error)
	Lanes() []worker.LaneInfo
}

// lane is what the cluster needs from *worker.Lane (and from the pool, for
// its default lane).
type lane interface {
	ExecStream(ctx context.Context, req worker.Payload) (worker.Payload, frames, error)
}

// frames is what the cluster needs from *worker.Stream.
type frames interface {
	Next(ctx context.Context) (worker.Payload, bool, error)
	Close()
}

// workerPool adapts *worker.Pool to pool.
type workerPool struct {
	*worker.Pool
}

func (p workerPool) ExecStream(ctx context.Context, req worker.Payload) (worker.Payload, frames, error) {
	return withFrames(p.Pool.ExecStream(ctx, req))
}

func (p workerPool) Lane(cfg worker.LaneConfig) (lane, error) {
	l, err := p.Pool.Lane(cfg)
	if err != nil {
		return nil, err
	}
	return workerLane{l}, nil
}

// workerLane adapts *worker.Lane to lane.
type workerLane struct {
	*worker.Lane
}

func (l workerLane) ExecStream(ctx context.Context, req worker.Payload) (worker.Payload, frames, error) {
	return withFrames(l.Lane.ExecStream(ctx, req))
}

func withFrames(resp worker.Payload, stream *worker.Stream, err error) (worker.Payload, frames, error) {
	if stream == nil {
		return resp, nil, err // not a nil *worker.Stream in a non-nil interface
	}
	return resp, stream, err
}

// Cluster serves HTTP requests with a pool of PHP workers. Routes with lane
// settings get handlers of their own (Lane); the rest share the pool's
// default lane through ServeHTTP.
type Cluster struct {
	pool       pool
	opts       rrhttp.Options
	maxBody    int64
	bodies     bodyBudget
	retryAfter string // the Retry-After value of the pool's 503s; empty for none
	logger     *slog.Logger
	metrics    *poolMetrics

	watcher   *watcher // nil without reload.dirs
	stopWatch context.CancelFunc
	watchDone chan struct{}
}

// bodyBudget bounds the bytes of request bodies held in memory at once
// (K-070). Requests reserve before reading and don't wait: one that doesn't
// fit is turned away.
type bodyBudget struct {
	limit int64
	used  atomic.Int64
}

func (b *bodyBudget) reserve(n int64) bool {
	for {
		used := b.used.Load()
		if used+n > b.limit {
			return false
		}
		if b.used.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

func (b *bodyBudget) release(n int64) {
	b.used.Add(-n)
}

// New builds a cluster from a validated config. bc.ConfigDir resolves
// relative paths, bc.Name labels the pool's metrics, and bc.RPCAddress is the
// workers' RR_RPC; environ is kest's environment. Nothing starts before Start.
func New(cfg *Config, bc registry.BuildContext, environ []string) (*Cluster, error) {
	reg := bc.MetricsRegistry()
	pm := newPoolMetrics(reg, bc.Name)
	wc := cfg.worker(bc.ConfigDir, environ, bc.RPCAddress)
	wc.Observer = pm
	p, err := worker.New(wc, bc.Logger)
	if err != nil {
		return nil, err
	}
	c := newCluster(cfg, bc.ConfigDir, workerPool{p}, bc.Logger)
	c.metrics = pm
	collectPool(reg, bc.Name, p)
	reg.NewCollector("kest_pool_buffered_body_bytes",
		"Request body bytes the cluster holds now, out of max_buffered_bodies.", metrics.TypeGauge,
		[]string{"cluster"}, func(emit func(float64, ...string)) { emit(float64(c.bodies.used.Load()), bc.Name) })
	return c, nil
}

func newCluster(cfg *Config, configDir string, p pool, logger *slog.Logger) *Cluster {
	c := &Cluster{
		pool:    p,
		opts:    cfg.requestOptions(configDir),
		maxBody: int64(cfg.MaxRequestBody),
		bodies:  bodyBudget{limit: int64(cfg.MaxBufferedBodies)},
		logger:  logger,
		metrics: newPoolMetrics(metrics.NewRegistry(), ""),
	}
	if d := cfg.RetryAfter.Std(); d > 0 {
		c.retryAfter = strconv.FormatInt(int64(math.Ceil(d.Seconds())), 10)
	}
	c.watcher = newWatcher(cfg.Reload, configDir, c.Reset, logger)
	return c
}

// Lane implements cluster.Laner: a lane of the pool for a route with lane
// settings, and the handler that serves the route's requests in it.
func (c *Cluster) Lane(l httpx.Lane) (http.Handler, error) {
	cfg := worker.LaneConfig{Name: l.Name, MaxWorkers: l.MaxWorkers, QueueTimeout: l.QueueTimeout}
	switch l.Priority {
	case config.PriorityHigh:
		cfg.Priority = worker.PriorityHigh
	case config.PriorityLow:
		cfg.Priority = worker.PriorityLow
	}
	wl, err := c.pool.Lane(cfg)
	if err != nil {
		return nil, laneFieldErrors(err)
	}
	lm := c.metrics.addLane(l.Name)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.serve(w, r, wl, lm)
	}), nil
}

// laneFields maps worker.LaneConfig fields to the fields of a route action.
var laneFields = map[string]string{"MaxWorkers": "max_workers", "Priority": "priority", "QueueTimeout": "queue_timeout"}

// laneFieldErrors turns the errors of worker.Pool.Lane into
// *config.FieldError values for the fields of the route action.
func laneFieldErrors(err error) error {
	var list []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		list = joined.Unwrap()
	} else {
		list = []error{err}
	}
	out := make([]error, 0, len(list))
	for _, e := range list {
		if fe, ok := errors.AsType[*worker.FieldError](e); ok {
			out = append(out, &config.FieldError{Field: laneFields[fe.Field], Msg: fe.Msg})
		} else {
			out = append(out, e)
		}
	}
	return errors.Join(out...)
}

// Start starts the workers and returns once every one of them has booted,
// then the file watcher of reload, if configured.
func (c *Cluster) Start(ctx context.Context) error {
	if err := c.pool.Start(ctx); err != nil {
		return withHint(err)
	}
	if c.watcher == nil {
		return nil
	}
	// The baseline is taken now: a change right after Start counts.
	if base, ok := c.watcher.baseline(); ok {
		wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c.stopWatch, c.watchDone = cancel, make(chan struct{})
		go func() {
			defer close(c.watchDone)
			c.watcher.run(wctx, base)
		}()
	}
	return nil
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
	for _, l := range c.pool.Lanes() {
		lr := laneReport{Route: l.Name, Priority: l.Priority.String(), MaxWorkers: l.MaxWorkers, QueueLength: l.Queued,
			BusyWorkers: l.Busy}
		if l.QueueTimeout > 0 {
			lr.QueueTimeout = l.QueueTimeout.String()
		}
		r.Lanes = append(r.Lanes, lr)
	}
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
	Lanes       []laneReport   `json:"lanes"` // the default lane first
	Workers     []workerReport `json:"workers"`
}

type laneReport struct {
	Route        string `json:"route"` // empty for the default lane
	Priority     string `json:"priority"`
	MaxWorkers   int    `json:"max_workers,omitempty"`
	QueueTimeout string `json:"queue_timeout,omitempty"` // empty: the pool's allocate_timeout
	QueueLength  int    `json:"queue_length"`
	BusyWorkers  int    `json:"busy_workers"`
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

// Stop stops the file watcher, lets running requests finish, then stops
// the workers.
func (c *Cluster) Stop(ctx context.Context) error {
	if c.stopWatch != nil {
		c.stopWatch()
		<-c.watchDone
	}
	return c.pool.Stop(ctx)
}

// ServeHTTP passes the request to a worker of the pool's default lane and
// writes its response.
func (c *Cluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.serve(w, r, c.pool, c.metrics.lanes[""])
}

// serve passes the request to a worker of the lane and writes its response.
func (c *Cluster) serve(w http.ResponseWriter, r *http.Request, l lane, lm *laneMetrics) {
	// Reserve the body's memory before reading it: its length, or the
	// largest allowed body when the length is unknown.
	n := c.maxBody
	if r.ContentLength >= 0 {
		n = min(r.ContentLength, c.maxBody)
	}
	if !c.bodies.reserve(n) {
		lm.rejected[reasonBodyBudget].Inc()
		c.logger.Warn("request turned away: max_buffered_bodies is full", "method", r.Method, "path", r.URL.Path,
			"body_bytes", n)
		c.setRetryAfter(w)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	defer c.bodies.release(n)

	r.Body = http.MaxBytesReader(w, r.Body, c.maxBody)
	if deadline, ok := r.Context().Deadline(); ok {
		// The route timeout bounds reading the body too: a client that sends
		// it slowly can't hold the request longer.
		_ = http.NewResponseController(w).SetReadDeadline(deadline)
	}
	req, err := rrhttp.NewRequest(r, c.opts)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	defer c.cleanup(req)
	// What a trusted proxy said about the client (client_ip), as
	// RoadRunner's proxy_ip_parser passes it: in remoteAddr.
	info := httpx.Info(r)
	if info.ClientIP != "" {
		req.RemoteAddr = info.ClientIP
	}
	if info.Scheme != "" {
		req.SetScheme(info.Scheme)
	}
	for _, err := range req.UploadErrors() {
		c.logger.Warn("upload not stored", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	payload, err := req.Payload(c.opts.Codec)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	out, stream, err := l.ExecStream(r.Context(), payload)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	if stream != nil {
		defer stream.Close()
		c.writeStream(w, r, out, stream)
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

// writeStream writes a streamed response (RoadRunner's STREAM frames):
// informational frames as 1xx responses (103 Early Hints), then the status
// and headers of the first final frame, then each body chunk as it comes,
// flushed. Later frames repeat status and headers; they are ignored. A
// failure after the header went out aborts the connection, so that the client
// sees a cut-off response rather than a complete-looking one.
func (c *Cluster) writeStream(w http.ResponseWriter, r *http.Request, first worker.Payload, stream frames) {
	rc := http.NewResponseController(w)
	frame, started := first, false
	bodyAllowed := r.Method != http.MethodHead
	for {
		if !started {
			resp, err := rrhttp.DecodeStreamFrame(frame)
			if err != nil {
				c.fail(w, r, err)
				return
			}
			var dropped int
			if resp.Informational() {
				dropped = rrhttp.WriteInformational(w, resp)
			} else {
				dropped = rrhttp.WriteStreamHeader(w, resp)
				started = true
				bodyAllowed = bodyAllowed && resp.Status != http.StatusNoContent && resp.Status != http.StatusNotModified
			}
			if dropped > 0 {
				c.logger.Debug("dropped invalid response headers", "method", r.Method, "path", r.URL.Path, "count", dropped)
			}
		}
		if started && bodyAllowed && len(frame.Body) > 0 {
			_, err := w.Write(frame.Body)
			if err == nil {
				err = rc.Flush()
			}
			if err != nil {
				c.logger.Debug("client went away during a stream", "method", r.Method, "path", r.URL.Path, "err", err)
				return
			}
		}

		next, ok, err := stream.Next(r.Context())
		switch {
		case err != nil && !started:
			c.fail(w, r, err)
			return
		case errors.Is(err, context.Canceled):
			c.logger.Debug("client went away during a stream", "method", r.Method, "path", r.URL.Path)
			return
		case err != nil:
			c.logger.Warn("stream cut off", "method", r.Method, "path", r.URL.Path, "err", err)
			panic(http.ErrAbortHandler)
		case !ok && !started:
			c.fail(w, r, &rrhttp.ResponseError{Err: errors.New("the stream ended without a final response")})
			return
		case !ok:
			return
		}
		frame = next
	}
}

// setRetryAfter tells the client of a 503 when to try again (retry_after).
func (c *Cluster) setRetryAfter(w http.ResponseWriter) {
	if c.retryAfter != "" {
		w.Header().Set("Retry-After", c.retryAfter)
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
	// A read deadline cancels the request context too, but that client is
	// still there: it gets its 408.
	timedOut := rrhttp.ErrorStatus(err) == http.StatusRequestTimeout
	if errors.Is(r.Context().Err(), context.Canceled) && !timedOut {
		c.logger.Debug("client went away", "method", r.Method, "path", r.URL.Path, "err", err)
		return
	}
	if rrhttp.ErrorStatus(err) == http.StatusServiceUnavailable {
		c.setRetryAfter(w)
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
