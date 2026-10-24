package phpworkers

import (
	"strconv"
	"time"

	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/worker"
)

// poolMetrics counts a pool's events (it is the pool's worker.Observer). The
// counters exist for every reason from the start; the maps are read only.
type poolMetrics struct {
	wait     *metrics.Histogram
	rejected map[worker.Reason]*metrics.Counter
	replaced map[worker.Reason]*metrics.Counter
}

func newPoolMetrics(reg *metrics.Registry, cluster string) *poolMetrics {
	m := &poolMetrics{
		wait: reg.NewHistogramVec("kest_pool_wait_seconds",
			"Time a request waited for a free worker, in seconds.", metrics.DurationBuckets, "cluster").With(cluster),
		rejected: make(map[worker.Reason]*metrics.Counter),
		replaced: make(map[worker.Reason]*metrics.Counter),
	}
	rejected := reg.NewCounterVec("kest_pool_rejected_total",
		"Requests that got no worker: queue_full (max_queue_size callers waiting) or no_worker "+
			"(none free within allocate_timeout).", "cluster", "reason")
	for _, r := range []worker.Reason{worker.ReasonQueueFull, worker.ReasonNoWorker} {
		m.rejected[r] = rejected.With(cluster, string(r))
	}
	replaced := reg.NewCounterVec("kest_pool_worker_restarts_total",
		"Worker processes replaced while the pool runs, by reason.", "cluster", "reason")
	for _, r := range worker.ReplaceReasons {
		m.replaced[r] = replaced.With(cluster, string(r))
	}
	return m
}

func (m *poolMetrics) Waited(d time.Duration) { m.wait.Observe(d.Seconds()) }

func (m *poolMetrics) Rejected(reason worker.Reason) {
	if c := m.rejected[reason]; c != nil {
		c.Inc()
	}
}

func (m *poolMetrics) Replaced(reason worker.Reason) {
	if c := m.replaced[reason]; c != nil {
		c.Inc()
	}
}

// workerStates are the states kest_pool_workers reports, all of them always.
var workerStates = []worker.State{worker.StateStarting, worker.StateReady, worker.StateWorking, worker.StateStopping,
	worker.StateBackoff, worker.StateStopped}

// collectPool registers the gauges read from the pool at scrape time.
func collectPool(reg *metrics.Registry, cluster string, p *worker.Pool) {
	reg.NewCollector("kest_pool_workers", "Worker slots by state.", metrics.TypeGauge, []string{"cluster", "state"},
		func(emit func(float64, ...string)) {
			counts := make(map[worker.State]int, len(workerStates))
			for _, w := range p.Workers() {
				counts[w.State]++
			}
			for _, s := range workerStates {
				emit(float64(counts[s]), cluster, s.String())
			}
		})
	reg.NewCollector("kest_pool_queue_length", "Requests waiting for a free worker.", metrics.TypeGauge,
		[]string{"cluster"}, func(emit func(float64, ...string)) { emit(float64(p.Waiting()), cluster) })
	reg.NewCollector("kest_pool_worker_memory_bytes",
		"Resident memory of each worker, as last sampled (every supervisor.watch_tick when max_worker_memory is set).",
		metrics.TypeGauge, []string{"cluster", "slot"}, func(emit func(float64, ...string)) {
			for _, w := range p.Workers() {
				if w.Memory > 0 {
					emit(float64(w.Memory), cluster, strconv.Itoa(w.Slot))
				}
			}
		})
}
