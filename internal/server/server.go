// Package server builds the component graph from configuration and runs it:
// ordered start, readiness, graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/admin"
	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/listener"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/registry"
)

// adminStopTimeout bounds the admin server shutdown, which runs last.
const adminStopTimeout = time.Second

type lifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type component struct {
	kind string // "cluster", "listener", "admin"
	name string
	lifecycle
}

// Server is a built component graph. Building has no side effects; Run
// starts everything.
type Server struct {
	cfg       *config.Config
	logger    *slog.Logger
	startedAt time.Time
	metrics   *metrics.Registry

	clusters  []component
	listeners []*listener.HTTP
	admin     *listener.HTTP

	state  atomic.Value // admin.State
	ready  chan struct{}
	failed chan error
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

	handlers := make(map[string]http.Handler, len(cfg.Clusters))
	for i := range cfg.Clusters {
		c := &cfg.Clusters[i]
		bc := registry.BuildContext{Name: c.Name, Logger: logger.With("cluster", c.Name), ConfigDir: configDir,
			Metrics: s.metrics}
		cl, err := registry.Build(cluster.Types, &c.Extension, bc)
		if err != nil {
			errs.Append(err)
			continue
		}
		handlers[c.Name] = cl
		s.clusters = append(s.clusters, component{kind: "cluster", name: c.Name, lifecycle: cl})
	}
	lookup := func(name string) (http.Handler, bool) {
		h, ok := handlers[name]
		return h, ok
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
		s.listeners = append(s.listeners, listener.NewHTTP(l.Name, l.Address, h, log, s.fail))
	}

	s.admin = listener.NewHTTP("admin", cfg.Admin.Address, admin.NewHandler(s), logger.With("component", "admin"), s.fail)

	if err := errs.Err(); err != nil {
		return nil, err
	}
	return s, nil
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

// Run starts clusters, the admin server and listeners, in that order, and
// serves until ctx is canceled or a component fails. Then it drains and
// stops everything: listeners first, then clusters, then the admin server.
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

	var runErr error
	select {
	case <-ctx.Done():
		s.logger.Info("shutting down", "drain_timeout", s.drainTimeout().String())
	case runErr = <-s.failed:
		s.logger.Error("shutting down after a fatal error", "err", runErr)
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
	for _, c := range s.clusters {
		if p, ok := c.lifecycle.(cluster.Prober); ok && !p.Serving() {
			down = append(down, c.name)
		}
	}
	return down
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
	order := make([]component, 0, len(s.clusters)+len(s.listeners)+1)
	order = append(order, s.clusters...)
	order = append(order, component{kind: "admin", name: "admin", lifecycle: s.admin})
	for _, l := range s.listeners {
		order = append(order, component{kind: "listener", name: l.Name(), lifecycle: l})
	}
	return order
}

// shutdown stops the started components in phases: listeners drain within
// the drain timeout, then clusters stop within the same budget, then the
// admin server, which reports "draining" until the end.
func (s *Server) shutdown(ctx context.Context, started []component) error {
	s.state.Store(admin.StateDraining)

	var phases [3][]component
	for _, c := range started {
		switch c.kind {
		case "listener":
			phases[0] = append(phases[0], c)
		case "cluster":
			phases[1] = append(phases[1], c)
		default:
			phases[2] = append(phases[2], c)
		}
	}

	base := context.WithoutCancel(ctx)
	budgets := [3]time.Duration{s.drainTimeout(), s.drainTimeout(), adminStopTimeout}
	var errs []error
	for i, phase := range phases {
		errs = append(errs, stopAll(base, budgets[i], phase)...)
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
