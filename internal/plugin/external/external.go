// Package external is the external plugin type: a binary that kest starts
// and talks to over a socket pair, with the protocol of internal/plugin/wire
// (ADR-0010). Plugins are written with the SDK, pkg/plugin, or in any
// language that speaks the protocol (docs/plugins.md).
package external

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/plugin/wire"
	"github.com/berrymore/kest/internal/registry"
)

func init() {
	plugin.Types.Register(registry.Factory[plugin.Plugin]{
		Type:      "external",
		NewConfig: func() any { return defaultConfig() },
		Build: func(bc registry.BuildContext, cfg any) (plugin.Plugin, error) {
			return New(cfg.(*Config), bc, os.Environ()) //nolint:forcetypeassert // NewConfig's type
		},
	})
}

// Restart backoff after a plugin dies or fails to start, as for workers.
const (
	backoffBase  = 100 * time.Millisecond
	backoffMax   = 30 * time.Second
	backoffReset = 10 * time.Second // a plugin that lived this long restarts at once
)

// External is a plugin run as an external process.
type External struct {
	name   string
	launch *launch
	stopTO time.Duration
	logger *slog.Logger
	slots  chan struct{} // max_concurrent_calls

	up       *metrics.Gauge
	restarts *metrics.Counter

	mu      sync.Mutex
	client  *client         // nil while the plugin is down
	stopCtx context.Context //nolint:containedctx // Stop's, handed to the supervisor
	stop    chan struct{}   // closed by Stop
	done    sync.WaitGroup  // the supervisor
}

// New builds the plugin; bc.Name is its name, environ kest's environment.
// Nothing starts before Start.
func New(cfg *Config, bc registry.BuildContext, environ []string) (*External, error) {
	pluginConfig, err := cfg.pluginConfigJSON() // validated
	if err != nil {
		return nil, err
	}
	hello, err := wire.ControlFrame(wire.Hello{Protocol: wire.Version, Name: bc.Name, Config: pluginConfig,
		MaxFrameSize: int(cfg.MaxFrameSize)})
	if err != nil {
		return nil, err
	}
	var env []string
	if cfg.InheritEnv {
		env = slices.Clone(environ)
	}
	for _, k := range slices.Sorted(maps.Keys(cfg.Env)) {
		env = append(env, k+"="+cfg.Env[k])
	}
	env = append(env, wire.EnvFD+"="+strconv.Itoa(wire.FD), wire.EnvProtocol+"="+strconv.Itoa(wire.Version))

	dir := cfg.Dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(bc.ConfigDir, dir)
	}
	reg := bc.MetricsRegistry()
	return &External{
		name: bc.Name,
		launch: &launch{command: slices.Clone(cfg.Command), dir: dir, env: env, hello: hello,
			maxFrame: int(cfg.MaxFrameSize), timeout: cfg.StartTimeout.Std()},
		stopTO: cfg.StopTimeout.Std(),
		logger: bc.Logger,
		slots:  make(chan struct{}, cfg.MaxConcurrentCalls),
		up: reg.NewGaugeVec("kest_plugin_up", "1 while an external plugin's process serves, 0 while it is down.",
			"plugin").With(bc.Name),
		restarts: reg.NewCounterVec("kest_plugin_restarts_total",
			"External plugins restarted after their process exited or broke the protocol.", "plugin").With(bc.Name),
		stop: make(chan struct{}),
	}, nil
}

// Start starts the plugin and waits for its handshake: a plugin that can't
// start fails kest's start. Then a supervisor restarts it when it dies.
func (e *External) Start(context.Context) error {
	c, err := start(e.launch, e.logger)
	if err != nil {
		return err
	}
	e.logger.Info("plugin started", "plugin_pid", c.cmd.Process.Pid)
	e.setClient(c)
	e.done.Go(func() { e.supervise(c) })
	return nil
}

// Stop asks the plugin to finish its calls and exit, within stop_timeout or
// ctx, whichever ends first; then it is killed.
func (e *External) Stop(ctx context.Context) error {
	e.mu.Lock()
	e.stopCtx = ctx
	e.mu.Unlock()
	close(e.stop)
	e.done.Wait()
	return nil
}

// Serving implements plugin.Prober: false while the plugin is down.
func (e *External) Serving() bool {
	e.mu.Lock()
	c := e.client
	e.mu.Unlock()
	return c != nil && c.alive()
}

// Call forwards a call to the plugin. It waits for a place among
// max_concurrent_calls within ctx, and fails at once while the plugin is
// down.
func (e *External) Call(ctx context.Context, c plugin.Call) ([]byte, error) {
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("plugin %s: no place among max_concurrent_calls: %w", e.name, ctx.Err())
	}
	defer func() { <-e.slots }()

	e.mu.Lock()
	cl := e.client
	e.mu.Unlock()
	if cl == nil {
		return nil, fmt.Errorf("plugin %s is down, restarting", e.name)
	}
	return cl.call(ctx, e.name, c)
}

func (e *External) setClient(c *client) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.client = c
	if c != nil {
		e.up.Set(1)
	} else {
		e.up.Set(0)
	}
}

// supervise waits for the plugin to exit, and restarts it with backoff,
// until Stop.
func (e *External) supervise(c *client) {
	failures := 0
	started := time.Now()
	for {
		select {
		case <-e.stop:
			e.setClient(nil)
			e.mu.Lock()
			ctx := e.stopCtx
			e.mu.Unlock()
			c.stop(ctx, e.stopTO)
			return
		case <-c.exited:
		}
		e.setClient(nil)
		c.kill()
		e.restarts.Inc()
		attrs := []any{"plugin_pid", c.cmd.Process.Pid, "err", c.waitErr}
		if tail := c.stderr.Tail(); tail != "" {
			attrs = append(attrs, "stderr", tail)
		}
		e.logger.Warn("plugin exited; restarting it", attrs...)
		// A plugin that dies soon after its start counts as a failure, so
		// that one that can't stay up isn't restarted in a tight loop.
		if time.Since(started) >= backoffReset {
			failures = 0
		} else {
			failures++
		}

		for c = nil; c == nil; {
			if failures > 0 && !e.sleep(backoff(failures)) {
				return
			}
			select {
			case <-e.stop:
				return
			default:
			}
			var err error
			if c, err = start(e.launch, e.logger); err != nil {
				failures++
				e.logger.Error("plugin failed to start", "err", err, "retry_in", backoff(failures).String())
			}
		}
		started = time.Now()
		e.logger.Info("plugin restarted", "plugin_pid", c.cmd.Process.Pid)
		e.setClient(c)
	}
}

// sleep waits d, or until Stop, and reports whether d passed.
func (e *External) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-e.stop:
		return false
	}
}

// backoff returns the pause before attempt n+1 after n failures: from
// backoffBase, doubling up to backoffMax, with up to 20 % jitter.
func backoff(failures int) time.Duration {
	d := backoffMax
	if failures < 20 {
		d = min(backoffBase<<(failures-1), backoffMax)
	}
	return d - time.Duration(rand.Int64N(int64(d/5)+1)) //nolint:gosec // jitter, not security
}
