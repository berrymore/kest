package phpworkers

import (
	"strconv"
	"time"

	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/worker"
)

// reasonBodyBudget labels requests that max_buffered_bodies turned away.
const reasonBodyBudget worker.Reason = "body_budget"

// rejectReasons are the reasons of kest_pool_rejected_total, all of them
// exposed for every lane from the start.
var rejectReasons = []worker.Reason{worker.ReasonQueueFull, worker.ReasonNoWorker, reasonBodyBudget}

// poolMetrics counts a pool's events (it is the pool's worker.Observer).
// The series of a lane exist from when the lane is added, before the pool
// starts; after that the maps are read only, so the observer needs no lock.
type poolMetrics struct {
	cluster  string
	waitVec  *metrics.HistogramVec
	rejVec   *metrics.CounterVec
	lanes    map[string]*laneMetrics // by lane name, "" for the default lane
	replaced map[worker.Reason]*metrics.Counter
}

type laneMetrics struct {
	wait     *metrics.Histogram
	rejected map[worker.Reason]*metrics.Counter
}

func newPoolMetrics(reg *metrics.Registry, cluster string) *poolMetrics {
	m := &poolMetrics{
		cluster: cluster,
		waitVec: reg.NewHistogramVec("kest_pool_wait_seconds",
			"Time a request waited for a free worker, in seconds, by lane: route is the name of a route with a lane "+
				"of its own, empty for the pool's default lane.", metrics.DurationBuckets, "cluster", "route"),
		rejVec: reg.NewCounterVec("kest_pool_rejected_total",
			"Requests turned away with 503, by lane and reason: queue_full (max_queue_size requests waiting in the "+
				"lane), no_worker (none free within the lane's queue timeout) or body_budget (max_buffered_bodies "+
				"full).", "cluster", "route", "reason"),
		lanes:    make(map[string]*laneMetrics),
		replaced: make(map[worker.Reason]*metrics.Counter),
	}
	m.addLane("")
	replaced := reg.NewCounterVec("kest_pool_worker_restarts_total",
		"Worker processes replaced while the pool runs, by reason.", "cluster", "reason")
	for _, r := range worker.ReplaceReasons {
		m.replaced[r] = replaced.With(cluster, string(r))
	}
	return m
}

// addLane creates the series of a lane. Only before the pool starts.
func (m *poolMetrics) addLane(name string) *laneMetrics {
	l := &laneMetrics{wait: m.waitVec.With(m.cluster, name), rejected: make(map[worker.Reason]*metrics.Counter)}
	for _, r := range rejectReasons {
		l.rejected[r] = m.rejVec.With(m.cluster, name, string(r))
	}
	m.lanes[name] = l
	return l
}

func (m *poolMetrics) Waited(lane string, d time.Duration) {
	if l := m.lanes[lane]; l != nil {
		l.wait.Observe(d.Seconds())
	}
}

func (m *poolMetrics) Rejected(lane string, reason worker.Reason) {
	if l := m.lanes[lane]; l != nil {
		if c := l.rejected[reason]; c != nil {
			c.Inc()
		}
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
	reg.NewCollector("kest_pool_queue_length", "Requests waiting for a free worker, by lane.", metrics.TypeGauge,
		[]string{"cluster", "route"}, func(emit func(float64, ...string)) {
			for _, l := range p.Lanes() {
				emit(float64(l.Queued), cluster, l.Name)
			}
		})
	reg.NewCollector("kest_pool_lane_workers", "Workers running the requests of each lane now.", metrics.TypeGauge,
		[]string{"cluster", "route"}, func(emit func(float64, ...string)) {
			for _, l := range p.Lanes() {
				emit(float64(l.Busy), cluster, l.Name)
			}
		})
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
