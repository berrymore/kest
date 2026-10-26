// Package server builds the component graph from configuration and runs it:
// ordered start, readiness, graceful shutdown.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/admin"
	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/listener"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/rpc"
)

// adminStopTimeout bounds the admin server shutdown, which runs last.
const adminStopTimeout = time.Second

type lifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Kinds of components.
const (
	kindListener = "listener"
	kindCluster  = "cluster"
	kindRPC      = "rpc"
	kindPlugin   = "plugin"
	kindAdmin    = "admin"
)

type component struct {
	kind string // kindListener, …
	name string
	typ  string // the extension type of a cluster or a plugin
	lifecycle
}

// noLifecycle is the lifecycle of a plugin that has none.
type noLifecycle struct{}

func (noLifecycle) Start(context.Context) error { return nil }
func (noLifecycle) Stop(context.Context) error  { return nil }

// Server is a built component graph. Building has no side effects; Run
// starts everything.
type Server struct {
	cfg       *config.Config
	logger    *slog.Logger
	startedAt time.Time
	metrics   *metrics.Registry

	plugins   []component
	rpc       *rpc.Server // nil when rpc is off
	clusters  []component
	listeners []*listener.HTTP
	admin     *listener.HTTP

	state  atomic.Value // admin.State
	ready  chan struct{}
	failed chan error
	resets <-chan os.Signal // each signal resets every cluster; nil: none
}

// New builds the server graph from a validated configuration. Extension
// configs are decoded here, so configuration problems are returned as
// config.Errors. New does not bind ports or start anything.
func New(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	s := &Server{
		cfg:       cfg,
		logger:    logger,
		startedAt: time.Now(),
		metrics:   metrics.NewRegistry(),
		ready:     make(chan struct{}),
		failed:    make(chan error, 1),
	}
	s.state.Store(admin.StateStarting)
	metrics.RegisterRuntime(s.metrics)

	var errs config.Errors

	configDir, err := configDir(cfg.File())
	if err != nil {
		return nil, err
	}

	plugins := make(map[string]plugin.Plugin, len(cfg.Plugins))
	for i := range cfg.Plugins {
		p := &cfg.Plugins[i]
		bc := registry.BuildContext{Name: p.Name, Logger: logger.With("plugin", p.Name), ConfigDir: configDir,
			Metrics: s.metrics}
		pl, err := registry.Build(plugin.Types, &p.Extension, bc)
		if err != nil {
			errs.Append(err)
			continue
		}
		plugins[p.Name] = pl
		var lc lifecycle = noLifecycle{}
		if l, ok := pl.(lifecycle); ok {
			lc = l
		}
		s.plugins = append(s.plugins, component{kind: kindPlugin, name: p.Name, typ: p.Type, lifecycle: lc})
	}

	var rpcAddress string
	if r := cfg.RPC; r != nil {
		srv, err := newRPC(r, configDir, plugins, logger.With("component", "rpc"), s.metrics)
		if err != nil {
			errs.Append(cfg.ErrorAt("rpc", err.Error()))
		} else {
			s.rpc, rpcAddress = srv, srv.Address()
		}
	}

	handlers := make(map[string]http.Handler, len(cfg.Clusters))
	for i := range cfg.Clusters {
		c := &cfg.Clusters[i]
		bc := registry.BuildContext{Name: c.Name, Logger: logger.With("cluster", c.Name), ConfigDir: configDir,
			Metrics: s.metrics, RPCAddress: rpcAddress}
		cl, err := registry.Build(cluster.Types, &c.Extension, bc)
		if err != nil {
			errs.Append(err)
			continue
		}
		handlers[c.Name] = cl
		s.clusters = append(s.clusters, component{kind: kindCluster, name: c.Name, typ: c.Type, lifecycle: cl})
	}
	lookup := func(name string, lane *httpx.Lane) (http.Handler, error) {
		h, ok := handlers[name]
		if !ok || lane == nil {
			return h, nil
		}
		l, ok := h.(cluster.Laner)
		if !ok {
			return nil, fmt.Errorf("cluster %q can't give routes lanes of their own: max_workers, priority and "+
				"queue_timeout need a php_workers cluster", name)
		}
		return l.Lane(*lane)
	}

	for i := range cfg.Listeners {
		l := &cfg.Listeners[i]
		log := logger.With("listener", l.Name)
		bc := registry.BuildContext{Name: l.Name, Logger: log, ConfigDir: configDir, Metrics: s.metrics}
		h, err := httpx.NewHandler(l.HTTP, lookup, bc)
		if err != nil {
			errs.Append(err)
			continue
		}
		opts := listener.Options{
			ReadHeaderTimeout: l.HTTP.ReadHeaderTimeout.Std(),
			ReadTimeout:       l.HTTP.ReadTimeout.Std(),
			WriteTimeout:      l.HTTP.WriteTimeout.Std(),
			IdleTimeout:       l.HTTP.IdleTimeout.Std(),
			MaxHeaderBytes:    int(l.HTTP.MaxHeaderBytes),
			HTTP1:             slices.Contains(l.HTTP.Protocols, config.ProtocolHTTP1),
			HTTP2:             slices.Contains(l.HTTP.Protocols, config.ProtocolHTTP2),
			Metrics:           s.metrics,
		}
		if l.TLS != nil {
			certs, err := loadCertificates(cfg, i, configDir)
			if err != nil {
				errs.Append(err)
				continue
			}
			opts.TLS = certs
		}
		address := l.Address
		if path, unix := config.UnixPath(address); unix {
			address = "unix:" + resolve(configDir, path)
			opts.SocketMode, err = config.ParseSocketMode(l.SocketMode) // validated
			if err != nil {
				errs.Append(err)
				continue
			}
		}
		s.listeners = append(s.listeners, listener.NewHTTP(l.Name, address, h, opts, log, s.fail))
	}

	s.admin = listener.NewHTTP("admin", cfg.Admin.Address, admin.NewHandler(s),
		listener.Options{Limits: listener.DefaultLimits()}, logger.With("component", "admin"), s.fail)

	if err := errs.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// newRPC builds the RPC server of r; a relative socket path resolves
// against configDir.
func newRPC(r *config.RPC, configDir string, plugins map[string]plugin.Plugin, logger *slog.Logger,
	reg *metrics.Registry) (*rpc.Server, error) {
	cfg := rpc.Config{
		Address:        r.Address,
		MaxConnections: r.MaxConnections,
		MaxFrameSize:   int(r.MaxFrameSize),
		CallTimeout:    r.CallTimeout.Std(),
	}
	if path, unix := config.UnixPath(r.Address); unix {
		cfg.Address = "unix:" + resolve(configDir, path)
		mode, err := config.ParseSocketMode(r.SocketMode) // validated
		if err != nil {
			return nil, err
		}
		cfg.SocketMode = mode
	}
	return rpc.New(cfg, plugins, logger, reg)
}

// loadCertificates loads the certificates of listener i, with paths relative
// to configDir. A pair that doesn't load is a config error at its path.
func loadCertificates(cfg *config.Config, i int, configDir string) (*listener.Certificates, error) {
	t := cfg.Listeners[i].TLS
	files := make([]listener.CertFiles, len(t.Certificates))
	for j, c := range t.Certificates {
		files[j] = listener.CertFiles{Cert: resolve(configDir, c.CertFile), Key: resolve(configDir, c.KeyFile)}
	}
	version := uint16(tls.VersionTLS12)
	if t.MinVersion == config.TLS13 {
		version = tls.VersionTLS13
	}
	certs, err := listener.LoadCertificates(files, version)
	if err == nil {
		return certs, nil
	}
	var errs config.Errors
	for _, e := range unjoin(err) {
		if ce, ok := errors.AsType[*listener.CertError](e); ok {
			errs.Append(cfg.ErrorAt(fmt.Sprintf("listeners[%d].tls.certificates[%d]", i, ce.Index), ce.Err.Error()))
		} else {
			errs.Append(e)
		}
	}
	return nil, errs.Err()
}

// unjoin returns the errors joined in err, or err alone.
func unjoin(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// resolve makes a path relative to dir absolute; dir is "" without a config file.
func resolve(dir, path string) string {
	if filepath.IsAbs(path) || dir == "" {
		return path
	}
	return filepath.Join(dir, path)
}

// configDir returns the absolute directory of the config file, or "" without one.
func configDir(file string) (string, error) {
	if file == "" {
		return "", nil
	}
	dir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return "", fmt.Errorf("config directory: %w", err)
	}
	return dir, nil
}

// Run starts plugins, the RPC server, clusters, the admin server and
// listeners, in that order, and serves until ctx is canceled or a component
// fails. Then it drains and stops everything in the reverse order, except the
// admin server, which stops last.
func (s *Server) Run(ctx context.Context) error {
	var started []component
	for _, c := range s.startOrder() {
		if err := c.Start(ctx); err != nil {
			stopErr := s.shutdown(ctx, started)
			return errors.Join(fmt.Errorf("start %s %q: %w", c.kind, c.name, err), stopErr)
		}
		started = append(started, c)
	}

	s.state.Store(admin.StateReady)
	close(s.ready)
	s.logger.Info("kest is ready", "admin", s.admin.Addr().String(), "listeners", len(s.listeners))

	// Resets run until shutdown begins; the pools stop them at once then.
	resetCtx, cancelResets := context.WithCancel(context.WithoutCancel(ctx))
	var resets sync.WaitGroup
	var runErr error
loop:
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("shutting down", "drain_timeout", s.drainTimeout().String())
			break loop
		case runErr = <-s.failed:
			s.logger.Error("shutting down after a fatal error", "err", runErr)
			break loop
		case sig := <-s.resets:
			s.logger.Info("resetting the workers", "signal", sig.String())
			resets.Go(func() { s.resetAll(resetCtx) })
		}
	}
	cancelResets()
	resets.Wait()

	if delay := s.cfg.Server.DrainDelay.Std(); runErr == nil && delay > 0 {
		// /ready fails at once; the listeners serve on while load balancers
		// take the instance out.
		s.state.Store(admin.StateDraining)
		s.logger.Info("waiting before draining", "drain_delay", delay.String())
		time.Sleep(delay)
	}
	stopErr := s.shutdown(ctx, started)
	if stopErr == nil {
		s.logger.Info("kest stopped")
	}
	return errors.Join(runErr, stopErr)
}

// Ready is closed once all components have started.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// State implements admin.Source.
func (s *Server) State() admin.State {
	return s.state.Load().(admin.State) //nolint:forcetypeassert // only admin.State is ever stored
}

// StartedAt implements admin.Source.
func (s *Server) StartedAt() time.Time {
	return s.startedAt
}

// ConfigFile implements admin.Source.
func (s *Server) ConfigFile() string {
	return s.cfg.File()
}

// NotServing implements admin.Source.
func (s *Server) NotServing() []string {
	var down []string
	for _, c := range s.plugins {
		if p, ok := c.lifecycle.(plugin.Prober); ok && !p.Serving() {
			down = append(down, "plugin "+c.name)
		}
	}
	for _, c := range s.clusters {
		if p, ok := c.lifecycle.(cluster.Prober); ok && !p.Serving() {
			down = append(down, c.name)
		}
	}
	return down
}

// ResetOn makes Run reset every cluster's workers on each value from c
// (SIGHUP). Call it before Run.
func (s *Server) ResetOn(c <-chan os.Signal) {
	s.resets = c
}

// ResetCluster implements admin.Source.
func (s *Server) ResetCluster(ctx context.Context, name string) error {
	if s.State() != admin.StateReady {
		return admin.ErrNotReady
	}
	for _, c := range s.clusters {
		if c.name != name {
			continue
		}
		r, ok := c.lifecycle.(cluster.Resetter)
		if !ok {
			return admin.ErrCantReset
		}
		return r.Reset(ctx)
	}
	return admin.ErrUnknownCluster
}

// resetAll resets every cluster that can, one after the other, and logs the
// outcome; the pools log their own progress.
func (s *Server) resetAll(ctx context.Context) {
	for _, c := range s.clusters {
		r, ok := c.lifecycle.(cluster.Resetter)
		if !ok {
			continue
		}
		if err := r.Reset(ctx); err != nil {
			s.logger.Error("reset failed", "cluster", c.name, "err", err)
		}
	}
}

// Clusters implements admin.Source.
func (s *Server) Clusters() []admin.ClusterInfo {
	out := make([]admin.ClusterInfo, 0, len(s.clusters))
	for _, c := range s.clusters {
		info := admin.ClusterInfo{Name: c.name, Type: c.typ}
		if p, ok := c.lifecycle.(cluster.Prober); ok {
			serving := p.Serving()
			info.Serving = &serving
		}
		if r, ok := c.lifecycle.(cluster.Reporter); ok {
			info.Status = r.Report()
		}
		out = append(out, info)
	}
	return out
}

// ConfigDump implements admin.Source.
func (s *Server) ConfigDump() ([]byte, error) {
	return s.cfg.DumpRedacted()
}

// Metrics implements admin.Source: the server's metrics registry.
func (s *Server) Metrics() *metrics.Registry {
	return s.metrics
}

// ListenerAddr returns the bound address of a listener, or nil if it is not running.
func (s *Server) ListenerAddr(name string) net.Addr {
	for _, l := range s.listeners {
		if l.Name() == name {
			return l.Addr()
		}
	}
	return nil
}

// AdminAddr returns the bound address of the admin server, or nil if it is not running.
func (s *Server) AdminAddr() net.Addr {
	return s.admin.Addr()
}

// fail reports a fatal error from a running component; the first one wins.
func (s *Server) fail(err error) {
	select {
	case s.failed <- err:
	default:
	}
}

func (s *Server) drainTimeout() time.Duration {
	return s.cfg.Server.DrainTimeout.Std()
}

func (s *Server) startOrder() []component {
	order := make([]component, 0, len(s.plugins)+len(s.clusters)+len(s.listeners)+2)
	order = append(order, s.plugins...)
	if s.rpc != nil {
		order = append(order, component{kind: kindRPC, name: "rpc", lifecycle: s.rpc})
	}
	order = append(order, s.clusters...)
	order = append(order, component{kind: kindAdmin, name: "admin", lifecycle: s.admin})
	for _, l := range s.listeners {
		order = append(order, component{kind: kindListener, name: l.Name(), lifecycle: l})
	}
	return order
}

// stopPhases are the kinds of components in the order they stop: listeners
// drain first; the RPC server and plugins outlive the workers, which call
// them until they exit; the admin server reports "draining" until the end.
var stopPhases = []string{kindListener, kindCluster, kindRPC, kindPlugin, kindAdmin}

// shutdown stops the started components in phases (stopPhases), the
// components of a phase together. Each phase has the drain timeout; the admin
// server has adminStopTimeout.
func (s *Server) shutdown(ctx context.Context, started []component) error {
	s.state.Store(admin.StateDraining)

	base := context.WithoutCancel(ctx)
	var errs []error
	for _, kind := range stopPhases {
		var phase []component
		for _, c := range started {
			if c.kind == kind {
				phase = append(phase, c)
			}
		}
		budget := s.drainTimeout()
		if kind == kindAdmin {
			budget = adminStopTimeout
		}
		errs = append(errs, stopAll(base, budget, phase)...)
	}
	return errors.Join(errs...)
}

// stopAll stops components concurrently within timeout.
func stopAll(ctx context.Context, timeout time.Duration, components []component) []error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, c := range components {
		wg.Go(func() {
			if err := c.Stop(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("stop %s %q: %w", c.kind, c.name, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errs
}
