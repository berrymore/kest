package rpc

import (
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
)

// callBuckets are the buckets of call durations: plugins in memory answer in
// microseconds, external ones over a socket.
var callBuckets = []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}

type serverMetrics struct {
	connections *metrics.Gauge
	refused     *metrics.Counter
	plugins     map[string]*pluginMetrics // by plugin name; "" counts calls to no plugin
}

type pluginMetrics struct {
	ok, failed, unknown, timeout *metrics.Counter
	duration                     *metrics.Histogram
}

// Results of kest_rpc_calls_total.
const (
	resultOK      = "ok"      // the plugin answered
	resultError   = "error"   // the plugin, or kest, answered an error
	resultUnknown = "unknown" // no such plugin or method
	resultTimeout = "timeout" // rpc.call_timeout passed
)

func newServerMetrics(reg *metrics.Registry, plugins map[string]plugin.Plugin) *serverMetrics {
	calls := reg.NewCounterVec("kest_rpc_calls_total",
		"RPC calls by plugin (empty for a name no plugin has) and result.", "plugin", "result")
	duration := reg.NewHistogramVec("kest_rpc_call_duration_seconds",
		"How long plugins took to answer RPC calls.", callBuckets, "plugin")
	m := &serverMetrics{
		connections: reg.NewGauge("kest_rpc_connections", "Open RPC connections."),
		refused: reg.NewCounter("kest_rpc_refused_connections_total",
			"RPC connections closed at once because rpc.max_connections were open."),
		plugins: make(map[string]*pluginMetrics, len(plugins)+1),
	}
	names := []string{""}
	for name := range plugins {
		names = append(names, name)
	}
	for _, name := range names {
		m.plugins[name] = &pluginMetrics{
			ok:       calls.With(name, resultOK),
			failed:   calls.With(name, resultError),
			unknown:  calls.With(name, resultUnknown),
			timeout:  calls.With(name, resultTimeout),
			duration: duration.With(name),
		}
	}
	return m
}

// of returns the metrics of a plugin, or of calls to no plugin for "".
func (m *serverMetrics) of(name string) *pluginMetrics {
	return m.plugins[name]
}
