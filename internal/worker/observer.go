package worker

import "time"

// Observer is told what a pool does, for metrics. Its methods are called from
// Exec callers and from the pool's goroutines, concurrently; they must be
// fast and must not block.
type Observer interface {
	// Waited reports how long a job waited for a free worker before it got one.
	Waited(d time.Duration)
	// Rejected reports a job that got no worker: ReasonQueueFull or
	// ReasonNoWorker.
	Rejected(reason Reason)
	// Replaced reports a worker process that ended, or failed to start, while
	// the pool runs; another one starts in its slot.
	Replaced(reason Reason)
}

// Reason says why a job was rejected or a worker replaced. The values are
// metric label values.
type Reason string

// Reasons for Observer.Rejected.
const (
	ReasonQueueFull Reason = "queue_full" // MaxQueueSize callers were waiting already
	ReasonNoWorker  Reason = "no_worker"  // no worker became free within AllocateTimeout
)

// Reasons for Observer.Replaced.
const (
	ReasonMaxJobs       Reason = "max_jobs"
	ReasonTTL           Reason = "ttl"
	ReasonIdleTTL       Reason = "idle_ttl"
	ReasonMemory        Reason = "memory"         // over MaxWorkerMemory
	ReasonExecTTL       Reason = "exec_ttl"       // killed in a job that ran too long
	ReasonCrash         Reason = "crash"          // exited, or broke the protocol
	ReasonStopRequested Reason = "stop_requested" // the worker asked to be replaced
	ReasonStartFailed   Reason = "start_failed"   // a new process didn't finish the handshake
	ReasonReset         Reason = "reset"          // Pool.Reset replaced it
)

// ReplaceReasons lists every reason for Observer.Replaced, for metrics that
// show all of them from the start.
var ReplaceReasons = []Reason{ReasonMaxJobs, ReasonTTL, ReasonIdleTTL, ReasonMemory, ReasonExecTTL, ReasonCrash,
	ReasonStopRequested, ReasonStartFailed, ReasonReset}

// nopObserver is the Observer of a pool without one.
type nopObserver struct{}

func (nopObserver) Waited(time.Duration) {}
func (nopObserver) Rejected(Reason)      {}
func (nopObserver) Replaced(Reason)      {}
