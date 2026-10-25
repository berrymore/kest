package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Pool runs a fixed number of worker processes and hands jobs to free ones.
//
// Each worker slot is owned by one goroutine that runs it for the pool's
// lifetime: start a process (with backoff after failures), serve jobs, replace
// the process when a limit is hit or it dies. Jobs wait in lanes (lane.go):
// queues with their own limits and priorities; a free worker takes the next
// job a lane may hand out, so waiting callers of a lane are served in order.
type Pool struct {
	cfg    Config
	obs    Observer
	logger *slog.Logger
	spawn  spawnFunc
	sample memorySampler       // replaced in tests
	jitter func(uint64) uint64 // max_jobs jitter, replaced in tests
	slots  []*slot

	// The dispatcher's state (lane.go). lanes is fixed once the pool starts;
	// the default lane is first.
	mu    sync.Mutex
	lanes []*Lane
	idle  []*slot // parked slots without a job, longest idle first
	seq   uint64  // arrival order of jobs, across lanes

	waiting    atomic.Int64 // jobs in the lanes' queues
	running    atomic.Bool
	resetting  atomic.Bool
	generation atomic.Uint64 // incremented when a reset begins; processes remember theirs

	// The pool's own contexts, created in Start. run ends when Stop begins:
	// no new jobs, idle workers stop, starts and backoffs are abandoned. procs
	// ends when Stop returns and kills any process left (it shouldn't be).
	run, procs         context.Context
	stopRun, stopProcs context.CancelFunc
	wg                 sync.WaitGroup
}

// spawnFunc starts a worker process. ctx is the process's lifetime.
type spawnFunc func(ctx context.Context) (process, error)

type job struct {
	payload Payload
	lane    *Lane
	result  chan jobResult // buffered: the worker never blocks on it
	gone    chan struct{}  // closed when the caller stopped waiting for the result

	// Set when the job joins its lane's queue, under Pool.mu.
	queued, deadline time.Time // deadline: queued plus the lane's queue timeout
	seq              uint64
	prev, next       *job
	inQueue          bool
}

type jobResult struct {
	payload Payload
	stream  *Stream // the rest of a streamed answer; nil if there is none
	err     error
}

// New validates cfg and returns a pool that runs cfg.Command. logger gets
// the pool's events and the workers' stderr. Nothing starts before Start.
func New(cfg Config, logger *slog.Logger) (*Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	pc := processConfig{
		command:    slices.Clone(cfg.Command),
		env:        slices.Clone(cfg.Env),
		dir:        cfg.Dir,
		maxPayload: cfg.MaxFrameSize,
	}
	spawn := func(ctx context.Context) (process, error) {
		p, err := startProcess(ctx, pc, logger)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	return newPool(cfg, logger, spawn), nil
}

func newPool(cfg Config, logger *slog.Logger, spawn spawnFunc) *Pool {
	p := &Pool{
		cfg:    cfg,
		obs:    cfg.Observer,
		logger: logger,
		spawn:  spawn,
		sample: sampleRSS,
		jitter: maxJobsJitter,
	}
	if p.obs == nil {
		p.obs = nopObserver{}
	}
	p.lanes = []*Lane{newLane(p, LaneConfig{})}
	for i := range cfg.Workers {
		p.slots = append(p.slots, &slot{
			pool:   p,
			id:     i,
			info:   Info{Slot: i, State: StateStopped},
			jobs:   make(chan *job, 1),
			memory: make(chan struct{}, 1),
			swap:   make(chan handoff),
		})
	}
	return p
}

// Start starts every worker and returns once all of them have finished the
// handshake. If one fails, or ctx ends first, Start stops the others and
// returns the error: a worker that can't start is a configuration problem.
func (p *Pool) Start(ctx context.Context) error {
	if p.run != nil {
		return errors.New("worker pool already started")
	}
	// The pool outlives the start: its contexts keep ctx's values, not its deadline.
	p.run, p.stopRun = context.WithCancel(context.WithoutCancel(ctx))
	p.procs, p.stopProcs = context.WithCancel(context.WithoutCancel(ctx))

	first := make(chan error, len(p.slots))
	for _, s := range p.slots {
		p.wg.Go(func() { s.run(p.run, first) })
	}
	var err error
	for range p.slots {
		select {
		case err = <-first:
		case <-ctx.Done():
			err = context.Cause(ctx)
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		_ = p.Stop(ctx)
		return fmt.Errorf("start workers: %w", err)
	}

	if p.cfg.MaxWorkerMemory > 0 {
		p.wg.Go(func() { p.supervise(p.run) })
	}
	p.running.Store(true)
	p.logger.Info("workers started", "workers", len(p.slots))
	return nil
}

// Stop stops the pool. New and waiting Exec calls fail with ErrStopped;
// running jobs finish; then every worker gets the stop frame and
// DestroyTimeout to exit. If ctx ends first, the remaining workers are
// killed. Stop returns once every worker has exited.
func (p *Pool) Stop(ctx context.Context) error {
	if p.run == nil {
		return nil
	}
	p.running.Store(false)
	p.stopRun()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		p.logger.Warn("worker pool did not stop in time, killing the workers")
		for _, s := range p.slots {
			s.kill()
		}
		<-done
	}
	p.stopProcs()
	return nil
}

// Exec runs a job on a free worker, in the pool's default lane. It waits for
// one up to AllocateTimeout, and fails at once with ErrQueueFull if
// MaxQueueSize callers are waiting in the lane already. ctx bounds the
// caller's wait, not the job: if ctx ends while the job runs, Exec returns
// ctx's error and the worker still finishes the job, within ExecTTL. Killing
// it instead would let clients that disconnect kill workers.
//
// Errors: ErrQueueFull, ErrNoWorker, ErrStopped, ctx's error, a *JobError
// from the worker, ErrStopRequested if two workers in a row asked to be
// replaced instead of answering, and a *ProcessError (wrapping
// ErrExecTimeout on a timeout) if the worker failed.
//
// A streamed answer is returned whole: the bodies of its frames joined, up to
// MaxFrameSize (ErrStreamTooLarge beyond). ExecStream returns it frame by
// frame.
func (p *Pool) Exec(ctx context.Context, req Payload) (Payload, error) {
	return p.lanes[0].Exec(ctx, req)
}

// ExecStream runs a job as Exec does. If the worker streams its answer, resp
// is the first frame and stream delivers the others; the caller must Close
// it. Otherwise stream is nil. ExecTTL bounds the wait for each frame.
func (p *Pool) ExecStream(ctx context.Context, req Payload) (resp Payload, stream *Stream, err error) {
	return p.lanes[0].ExecStream(ctx, req)
}

// Reset replaces every live worker with a new process, one slot at a time: a
// rolling restart, for deploys. Each replacement boots while the old worker
// keeps serving and takes over between two jobs; the old one then stops in
// the background. So the pool keeps its capacity and runs at most
// Workers + 1 processes, plus old ones finishing their stop.
//
// If a replacement fails to boot, Reset stops there and returns the error:
// the slots it didn't reach keep their old workers, so a deploy that doesn't
// boot leaves the pool serving. Slots without a live worker are skipped,
// since they start new processes anyway. Reset fails with ErrResetRunning
// while another Reset runs and with ErrStopped once the pool stops.
func (p *Pool) Reset(ctx context.Context) error {
	if !p.running.Load() {
		return ErrStopped
	}
	if !p.resetting.CompareAndSwap(false, true) {
		return ErrResetRunning
	}
	defer p.resetting.Store(false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(p.run, cancel)() //nolint:contextcheck // Stop ends the reset too

	gen := p.generation.Add(1)
	replaced := 0
	for _, s := range p.slots {
		if st := s.snapshot().State; st != StateReady && st != StateWorking {
			continue
		}
		h, err := p.boot(ctx)
		if err == nil {
			h.gen = gen
			select {
			case s.swap <- h:
				replaced++
				continue
			case <-ctx.Done():
				h.proc.Kill()
				<-h.proc.Done()
				h.proc.Close()
				err = ctx.Err()
			}
		}
		if p.run.Err() != nil {
			err = ErrStopped
		}
		p.logger.Error("reset stopped", "replaced", replaced, "workers", len(p.slots), "err", err)
		return fmt.Errorf("reset: %d of %d workers replaced, then: %w", replaced, len(p.slots), err)
	}
	p.logger.Info("workers reset", "replaced", replaced)
	return nil
}

// handoff is a booted process that Reset gives to a slot.
type handoff struct {
	proc    process
	spawned time.Time
	gen     uint64 // the reset's generation: a process of this one or later is new already
}

// boot spawns a process and waits for its handshake, within StartTimeout.
// On failure the process is gone.
func (p *Pool) boot(ctx context.Context) (handoff, error) {
	proc, err := p.spawn(p.procs) //nolint:contextcheck // processes live until Stop ends, longer than ctx
	if err != nil {
		return handoff{}, err
	}
	spawned := time.Now()
	hctx, cancel := context.WithTimeout(ctx, p.cfg.StartTimeout)
	defer cancel()
	if _, err := handshake(hctx, proc, p.logger); err != nil {
		proc.Close()
		return handoff{}, err
	}
	return handoff{proc: proc, spawned: spawned}, nil
}

// Waiting returns the number of callers waiting for a free worker.
func (p *Pool) Waiting() int {
	return int(p.waiting.Load())
}

// Workers describes every worker slot.
func (p *Pool) Workers() []Info {
	out := make([]Info, len(p.slots))
	for i, s := range p.slots {
		out[i] = s.snapshot()
	}
	return out
}

// Health reports how many slots have a live worker.
func (p *Pool) Health() Health {
	live := 0
	for _, s := range p.slots {
		if st := s.snapshot().State; st == StateReady || st == StateWorking {
			live++
		}
	}
	switch live {
	case len(p.slots):
		return Healthy
	case 0:
		return Unhealthy
	default:
		return Degraded
	}
}

// State is what a worker slot is doing.
type State int

// Slot states.
const (
	StateStopped  State = iota // no process: the pool is not running
	StateStarting              // a process is starting and booting
	StateReady                 // waiting for a job
	StateWorking               // running a job
	StateStopping              // the process is being stopped
	StateBackoff               // waiting to start a process after failures
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateReady:
		return "ready"
	case StateWorking:
		return "working"
	case StateStopping:
		return "stopping"
	case StateBackoff:
		return "backoff"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Info describes a worker slot and its current process.
type Info struct {
	Slot    int
	State   State
	Pid     int       // 0 when no process runs
	Jobs    uint64    // jobs run by the current process
	Started time.Time // when the current process started
	Memory  int64     // last sampled RSS in bytes, 0 if not sampled
}

// Health summarizes a pool's live workers.
type Health int

// Health values.
const (
	Unhealthy Health = iota // no live worker
	Degraded                // some slots have no live worker
	Healthy                 // every slot has a live worker
)

func (h Health) String() string {
	switch h {
	case Unhealthy:
		return "unhealthy"
	case Degraded:
		return "degraded"
	case Healthy:
		return "healthy"
	default:
		return fmt.Sprintf("Health(%d)", int(h))
	}
}

// Respawn backoff after failed or short-lived workers.
const (
	backoffBase  = 100 * time.Millisecond
	backoffMax   = 30 * time.Second
	backoffReset = 10 * time.Second // a worker that lived this long, or completed a job, resets the backoff
)

// slot is one worker position in the pool. Its goroutine (run) owns the
// process; other goroutines only read the snapshot, signal memory, or kill.
type slot struct {
	pool   *Pool
	id     int
	jobs   chan *job     // the job the dispatcher hands to the parked slot, capacity 1
	memory chan struct{} // the supervisor's "over MaxWorkerMemory", capacity 1

	swap chan handoff // processes booted by Reset, taken between jobs
	gen  uint64       // the generation the current process started in; owned by run

	mu   sync.Mutex
	proc process // the current process, nil if none
	info Info
}

// run owns the slot until ctx, the pool's run context, ends. The result of
// the first start goes to first; if it failed, the slot gives up and Start
// stops the pool.
func (s *slot) run(ctx context.Context, first chan<- error) {
	p := s.pool
	defer s.update(func(i *Info) { *i = Info{Slot: i.Slot, State: StateStopped} })

	proc, err := s.start(ctx)
	first <- err
	if err != nil {
		return
	}
	failures := 0
	for {
		next, failedEarly := s.serve(ctx, proc)
		if next != nil {
			// A reset handed over a booted process.
			s.adopt(next)
			proc, failures = next.proc, 0
			continue
		}
		if failedEarly {
			failures++
		} else {
			failures = 0
		}
		for {
			if ctx.Err() != nil {
				return
			}
			if failures > 0 {
				s.update(func(i *Info) { i.State = StateBackoff })
				if !sleep(ctx, backoff(failures)) {
					return
				}
			}
			if proc, err = s.start(ctx); err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			p.obs.Replaced(ReasonStartFailed)
			failures++
			p.logger.Error("worker failed to start", "slot", s.id, "err", err)
		}
	}
}

// start spawns a process and waits for its handshake.
func (s *slot) start(ctx context.Context) (process, error) {
	p := s.pool
	s.update(func(i *Info) { *i = Info{Slot: i.Slot, State: StateStarting} })
	s.gen = p.generation.Load()
	proc, err := p.spawn(p.procs) //nolint:contextcheck // processes live until Stop ends, longer than ctx
	if err != nil {
		return nil, err
	}
	started := time.Now()
	s.mu.Lock()
	s.proc = proc
	s.info.Pid, s.info.Started = proc.Pid(), started
	s.mu.Unlock()

	hctx, cancel := context.WithTimeout(ctx, p.cfg.StartTimeout)
	defer cancel()
	if _, err := handshake(hctx, proc, p.logger); err != nil {
		s.release(proc)
		return nil, err
	}
	// A memory signal for a previous process doesn't apply to this one.
	select {
	case <-s.memory:
	default:
	}
	p.logger.Debug("worker started", "slot", s.id, "worker_pid", proc.Pid(), "boot", time.Since(started))
	return proc, nil
}

// serve runs jobs on proc until the process is replaced or the pool stops.
// It returns a process that a reset handed over, if that is why it ended, or
// whether the process failed early, which calls for a backoff.
func (s *slot) serve(ctx context.Context, proc process) (next *handoff, failedEarly bool) {
	p := s.pool
	logger := p.logger.With("slot", s.id, "worker_pid", proc.Pid())
	started := s.snapshot().Started
	limit := p.jitter(p.cfg.MaxJobs)
	var jobs, succeeded uint64

	var ttlLeft time.Duration
	if p.cfg.TTL > 0 {
		ttlLeft = max(p.cfg.TTL-time.Since(started), time.Nanosecond) // the boot may have used it all
	}
	ttl := newOptionalTimer(ttlLeft)
	defer ttl.stop()
	idle := newOptionalTimer(p.cfg.IdleTTL)
	defer idle.stop()

	for {
		s.update(func(i *Info) { i.State = StateReady })
		if ctx.Err() != nil {
			s.retire(proc, logger, "pool stopped")
			return nil, false
		}
		// Parked, the slot may get a job at any time. Any other event takes
		// it off the idle list first (unpark), which gives back a job that
		// came meanwhile.
		p.park(s)
		select {
		case j := <-s.jobs:
			idle.stop()
			s.update(func(i *Info) { i.State = StateWorking })
			p.obs.Waited(j.lane.cfg.Name, time.Since(j.queued))
			resp, more, err := execute(proc, j.payload, p.cfg.ExecTTL)
			if err == nil && more {
				st := newStream()
				j.result <- jobResult{payload: resp, stream: st}
				err = s.stream(proc, j, st)
			} else {
				j.result <- jobResult{payload: resp, err: err}
			}
			p.finish(j)
			jobs++
			s.update(func(i *Info) { i.Jobs = jobs })

			if _, ok := errors.AsType[*ProcessError](err); ok {
				logger.Warn("worker failed", "err", err)
				s.release(proc)
				reason := ReasonCrash
				if errors.Is(err, ErrExecTimeout) {
					reason = ReasonExecTTL
				}
				p.obs.Replaced(reason)
				return nil, succeeded == 0 && time.Since(started) < backoffReset
			}
			if errors.Is(err, ErrStopRequested) {
				s.replace(proc, logger, ReasonStopRequested)
				return nil, false
			}
			succeeded++
			if reason := s.limitReached(jobs, limit, started); reason != "" {
				s.replace(proc, logger, reason)
				return nil, false
			}
			idle.reset(p.cfg.IdleTTL)
		case <-idle.c():
			p.unpark(s)
			s.replace(proc, logger, ReasonIdleTTL)
			return nil, false
		case <-ttl.c():
			p.unpark(s)
			s.replace(proc, logger, ReasonTTL)
			return nil, false
		case <-s.memory:
			p.unpark(s)
			s.replace(proc, logger, ReasonMemory)
			return nil, false
		case <-proc.Done():
			p.unpark(s)
			logger.Warn("worker exited", "err", proc.Err(), "stderr", proc.Stderr())
			s.release(proc)
			p.obs.Replaced(ReasonCrash)
			return nil, succeeded == 0 && time.Since(started) < backoffReset
		case h := <-s.swap:
			p.unpark(s)
			if s.gen >= h.gen {
				// This process started after the reset began: it is new already.
				p.stopInBackground(h.proc, logger)
				continue
			}
			p.stopInBackground(proc, logger)
			p.obs.Replaced(ReasonReset)
			return &h, false
		case <-ctx.Done():
			p.unpark(s)
			s.retire(proc, logger, "pool stopped")
			return nil, false
		}
	}
}

// limitReached checks the limits after a job and returns the one reached.
func (s *slot) limitReached(jobs, limit uint64, started time.Time) Reason {
	cfg := s.pool.cfg
	switch {
	case limit > 0 && jobs >= limit:
		return ReasonMaxJobs
	case cfg.TTL > 0 && time.Since(started) >= cfg.TTL:
		return ReasonTTL
	}
	select {
	case <-s.memory:
		return ReasonMemory
	default:
		return ""
	}
}

// recordMemory stores the sampled RSS of the current process and signals the
// slot if it is at or over limit, as RoadRunner does. Checking the pid under
// the lock ignores a sample of a process that has been replaced since.
func (s *slot) recordMemory(rss map[int]int64, limit int64, logger *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := rss[s.info.Pid]
	if s.info.Pid == 0 || !ok {
		return
	}
	s.info.Memory = n
	if n < limit {
		return
	}
	select {
	case s.memory <- struct{}{}:
		logger.Info("worker over the memory limit", "slot", s.id, "worker_pid", s.info.Pid, "rss", n, "limit", limit)
	default: // signaled already
	}
}

// adopt makes a process that a reset booted the slot's current one.
func (s *slot) adopt(h *handoff) {
	s.gen = h.gen
	s.mu.Lock()
	s.proc = h.proc
	s.info = Info{Slot: s.id, State: StateReady, Pid: h.proc.Pid(), Started: h.spawned}
	s.mu.Unlock()
	// A memory signal for the previous process doesn't apply to this one.
	select {
	case <-s.memory:
	default:
	}
}

// stopInBackground stops a process that no slot uses any more, gracefully
// within DestroyTimeout, while the slot goes on with another one. Stop waits
// for it.
func (p *Pool) stopInBackground(proc process, logger *slog.Logger) {
	p.wg.Go(func() {
		if stopProcess(proc, p.cfg.DestroyTimeout) {
			logger.Debug("worker stopped", "reason", ReasonReset)
		} else {
			logger.Warn("worker did not exit in time, killed", "reason", ReasonReset, "timeout", p.cfg.DestroyTimeout)
		}
		proc.Close()
	})
}

// replace retires proc for reason and tells the observer.
func (s *slot) replace(proc process, logger *slog.Logger, reason Reason) {
	s.retire(proc, logger, string(reason))
	s.pool.obs.Replaced(reason)
}

// retire stops proc gracefully, within DestroyTimeout.
func (s *slot) retire(proc process, logger *slog.Logger, reason string) {
	s.update(func(i *Info) { i.State = StateStopping })
	switch {
	case !stopProcess(proc, s.pool.cfg.DestroyTimeout):
		logger.Warn("worker did not exit in time, killed", "reason", reason, "timeout", s.pool.cfg.DestroyTimeout)
	case reason == string(ReasonMemory) || reason == string(ReasonStopRequested):
		logger.Info("worker stopped", "reason", reason)
	default:
		logger.Debug("worker stopped", "reason", reason)
	}
	s.release(proc)
}

// release forgets a process that has exited.
func (s *slot) release(proc process) {
	proc.Close()
	s.mu.Lock()
	s.proc = nil
	s.info.Pid, s.info.Jobs, s.info.Memory = 0, 0, 0
	s.mu.Unlock()
}

// kill kills the current process, if any.
func (s *slot) kill() {
	s.mu.Lock()
	proc := s.proc
	s.mu.Unlock()
	if proc != nil {
		proc.Kill()
	}
}

func (s *slot) update(f func(*Info)) {
	s.mu.Lock()
	f(&s.info)
	s.mu.Unlock()
}

func (s *slot) snapshot() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// maxJobsJitter adds 0…14 % to a max_jobs limit, as RoadRunner does, so that
// workers started together don't all restart together.
func maxJobsJitter(n uint64) uint64 {
	return n + n*uint64(rand.IntN(15))/100 //nolint:gosec // jitter, not security
}

// backoff returns the wait before the next start after failures consecutive
// failures: exponential from backoffBase to backoffMax, with jitter.
func backoff(failures int) time.Duration {
	d := backoffMax
	if failures < 20 {
		d = min(backoffBase<<(failures-1), backoffMax)
	}
	return d/2 + rand.N(d/2) //nolint:gosec // jitter, not security
}

// sleep waits for d or until ctx ends, and reports whether d elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// optionalTimer is a timer that never fires when created with d <= 0.
type optionalTimer struct {
	t *time.Timer
}

func newOptionalTimer(d time.Duration) *optionalTimer {
	if d <= 0 {
		return &optionalTimer{}
	}
	return &optionalTimer{t: time.NewTimer(d)}
}

func (o *optionalTimer) c() <-chan time.Time {
	if o.t == nil {
		return nil
	}
	return o.t.C
}

func (o *optionalTimer) stop() {
	if o.t != nil {
		o.t.Stop()
	}
}

func (o *optionalTimer) reset(d time.Duration) {
	if o.t != nil {
		o.t.Reset(d)
	}
}
