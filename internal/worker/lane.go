package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Priority orders a pool's lanes: a free worker takes the oldest job of the
// highest-priority lane that may take one. The zero value is PriorityNormal.
type Priority int

// Priorities.
const (
	PriorityLow    Priority = -1
	PriorityNormal Priority = 0
	PriorityHigh   Priority = 1
)

func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	default:
		return fmt.Sprintf("Priority(%d)", int(p))
	}
}

// LaneConfig configures a lane: a queue of its own in a pool.
type LaneConfig struct {
	Name         string        // required; labels the lane in Observer calls and LaneInfo
	MaxWorkers   int           // at most this many workers run the lane's jobs at once; 0 = no limit
	Priority     Priority      // PriorityLow, PriorityNormal or PriorityHigh
	QueueTimeout time.Duration // the longest wait for a worker; 0 = the pool's AllocateTimeout
}

// Lane is a queue of its own in a pool, for jobs that must not wait behind
// others, or must not take more than a few of the pool's workers. Each lane
// holds up to MaxQueueSize waiting jobs. Pool.Exec uses the pool's default
// lane, which has no limit, normal priority and AllocateTimeout.
type Lane struct {
	pool    *Pool
	cfg     LaneConfig
	timeout time.Duration // QueueTimeout, or the pool's AllocateTimeout

	// Guarded by pool.mu.
	queue jobQueue
	busy  int // jobs handed to workers and not finished yet
}

// LaneInfo describes a lane and what it is doing.
type LaneInfo struct {
	LaneConfig
	Queued int // jobs waiting for a worker
	Busy   int // workers running the lane's jobs
}

// Lane adds a lane to the pool. Lanes are added before Start; the name must
// be unique and not empty, which is the default lane's.
func (p *Pool) Lane(cfg LaneConfig) (*Lane, error) {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	switch {
	case cfg.Name == "":
		add("Name", "must not be empty")
	case slices.ContainsFunc(p.lanes, func(l *Lane) bool { return l.cfg.Name == cfg.Name }):
		add("Name", "duplicate lane %q", cfg.Name)
	}
	if cfg.MaxWorkers < 0 || cfg.MaxWorkers > p.cfg.Workers {
		add("MaxWorkers", "must be between 0 and the pool's %d workers, got %d", p.cfg.Workers, cfg.MaxWorkers)
	}
	if cfg.Priority < PriorityLow || cfg.Priority > PriorityHigh {
		add("Priority", "unknown priority %d", cfg.Priority)
	}
	if cfg.QueueTimeout < 0 {
		add("QueueTimeout", "must not be negative, got %s", cfg.QueueTimeout)
	}
	if p.run != nil {
		errs = append(errs, errors.New("worker pool already started"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	l := newLane(p, cfg)
	p.lanes = append(p.lanes, l)
	return l, nil
}

func newLane(p *Pool, cfg LaneConfig) *Lane {
	l := &Lane{pool: p, cfg: cfg, timeout: cfg.QueueTimeout}
	if l.timeout == 0 {
		l.timeout = p.cfg.AllocateTimeout
	}
	return l
}

// Lanes describes the pool's lanes, the default one first.
func (p *Pool) Lanes() []LaneInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]LaneInfo, len(p.lanes))
	for i, l := range p.lanes {
		out[i] = LaneInfo{LaneConfig: l.cfg, Queued: l.queue.n, Busy: l.busy}
	}
	return out
}

// Exec runs a job on a free worker, as Pool.Exec does, in this lane: it
// waits for a worker in the lane's queue, up to the lane's queue timeout.
func (l *Lane) Exec(ctx context.Context, req Payload) (Payload, error) {
	resp, stream, err := l.ExecStream(ctx, req)
	if err != nil || stream == nil {
		return resp, err
	}
	defer stream.Close()
	body := resp.Body
	for {
		f, ok, err := stream.Next(ctx)
		if err != nil {
			return Payload{}, err
		}
		if !ok {
			resp.Body = body
			return resp, nil
		}
		if len(body)+len(f.Body) > l.pool.cfg.MaxFrameSize {
			return Payload{}, ErrStreamTooLarge
		}
		body = append(body, f.Body...)
	}
}

// ExecStream runs a job as Pool.ExecStream does, in this lane.
func (l *Lane) ExecStream(ctx context.Context, req Payload) (resp Payload, stream *Stream, err error) {
	resp, stream, err = l.exec(ctx, req)
	if errors.Is(err, ErrStopRequested) {
		// The worker didn't run the job: give it to another one, once.
		resp, stream, err = l.exec(ctx, req)
	}
	return resp, stream, err
}

func (l *Lane) exec(ctx context.Context, req Payload) (Payload, *Stream, error) {
	p := l.pool
	if !p.running.Load() {
		return Payload{}, nil, ErrStopped
	}
	if err := ctx.Err(); err != nil {
		return Payload{}, nil, err
	}

	j := &job{payload: req, lane: l, result: make(chan jobResult, 1), gone: make(chan struct{})}
	p.mu.Lock()
	if l.queue.n >= p.cfg.MaxQueueSize {
		p.mu.Unlock()
		p.obs.Rejected(l.cfg.Name, ReasonQueueFull)
		return Payload{}, nil, ErrQueueFull
	}
	j.queued = time.Now()
	j.deadline = j.queued.Add(l.timeout)
	p.enqueueLocked(j)
	p.dispatchLocked()
	queued := j.inQueue
	p.mu.Unlock()

	var timeout <-chan time.Time
	if queued {
		timer := time.NewTimer(l.timeout)
		defer timer.Stop()
		timeout = timer.C
	}
	stopped := p.run.Done()
	for {
		select {
		case r := <-j.result:
			return r.payload, r.stream, r.err
		case <-timeout:
			timeout = nil
			if p.dequeue(j) {
				p.obs.Rejected(l.cfg.Name, ReasonNoWorker)
				return Payload{}, nil, ErrNoWorker
			}
			// A worker has the job: wait for its answer.
		case <-ctx.Done():
			if !p.dequeue(j) {
				close(j.gone) // a stream that comes now is stopped
			}
			return Payload{}, nil, ctx.Err()
		case <-stopped:
			stopped = nil
			if p.dequeue(j) {
				return Payload{}, nil, ErrStopped
			}
			// A running job finishes.
		}
	}
}

// The dispatcher. A slot that is free parks: it takes the next job a lane
// may hand out, or joins the idle list. A job that comes when a slot is idle
// goes to that slot at once. Jobs reach slots on the slots' own channels,
// under p.mu, so a slot that leaves the idle list (a limit, a reset, stop)
// can tell whether a job was handed to it, and gives it back.

// enqueueLocked puts a new job at the end of its lane's queue.
func (p *Pool) enqueueLocked(j *job) {
	p.seq++
	j.seq = p.seq
	j.lane.queue.pushBack(j)
	p.waiting.Add(1)
}

// dequeue takes a job out of its queue, and reports whether it was there
// (rather than handed to a worker).
func (p *Pool) dequeue(j *job) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !j.inQueue {
		return false
	}
	j.lane.queue.remove(j)
	p.waiting.Add(-1)
	return true
}

// nextLocked returns the job a free worker takes next: the oldest of the
// highest-priority lane that is under its MaxWorkers, or nil.
func (p *Pool) nextLocked() *job {
	var best *job
	for _, l := range p.lanes {
		j := l.queue.head
		if j == nil || (l.cfg.MaxWorkers > 0 && l.busy >= l.cfg.MaxWorkers) {
			continue
		}
		if best == nil || l.cfg.Priority > best.lane.cfg.Priority ||
			(l.cfg.Priority == best.lane.cfg.Priority && j.seq < best.seq) {
			best = j
		}
	}
	return best
}

// assignLocked hands a queued job to a slot that isn't on the idle list.
func (p *Pool) assignLocked(j *job, s *slot) {
	j.lane.queue.remove(j)
	p.waiting.Add(-1)
	j.lane.busy++
	s.jobs <- j // the slot holds no other job: never blocks
}

// dispatchLocked hands queued jobs to idle slots while both are there.
// Once the pool stops, no job is handed out.
func (p *Pool) dispatchLocked() {
	if p.run.Err() != nil {
		return
	}
	for len(p.idle) > 0 {
		j := p.nextLocked()
		if j == nil {
			return
		}
		s := p.idle[0]
		p.idle = slices.Delete(p.idle, 0, 1)
		p.assignLocked(j, s)
	}
}

// park gives a free slot the next job, or puts it on the idle list. Either
// way the slot then waits on its channel, among its other events.
func (p *Pool) park(s *slot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run.Err() == nil {
		if j := p.nextLocked(); j != nil {
			p.assignLocked(j, s)
			return
		}
	}
	p.idle = append(p.idle, s)
}

// unpark takes a parked slot off the idle list because it has something
// else to do. If a job was handed to it meanwhile, the job goes back.
func (p *Pool) unpark(s *slot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i := slices.Index(p.idle, s); i >= 0 {
		p.idle = slices.Delete(p.idle, i, i+1)
		return
	}
	p.requeueLocked(<-s.jobs) // not idle: a job was handed to it
}

// requeueLocked gives back a job that a slot took but didn't run: to the head
// of its lane, so it keeps its turn. A job whose caller is gone is dropped;
// one past its queue timeout, or of a pool that stops, fails as it would
// have in the queue. Its caller may have seen the timeout already while the
// slot had the job, which is why the deadline is checked here.
func (p *Pool) requeueLocked(j *job) {
	j.lane.busy--
	select {
	case <-j.gone:
		return
	default:
	}
	switch {
	case p.run.Err() != nil:
		j.result <- jobResult{err: ErrStopped}
	case !time.Now().Before(j.deadline):
		p.obs.Rejected(j.lane.cfg.Name, ReasonNoWorker)
		j.result <- jobResult{err: ErrNoWorker}
	default:
		j.lane.queue.pushFront(j)
		p.waiting.Add(1)
		p.dispatchLocked()
	}
}

// finish counts a job as done in its lane, which may let the lane's next
// job go to an idle slot.
func (p *Pool) finish(j *job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j.lane.busy--
	p.dispatchLocked()
}

// jobQueue is a FIFO of jobs, linked through the jobs, so a job that leaves
// early (timeout, caller gone) is removed in O(1).
type jobQueue struct {
	head, tail *job
	n          int
}

func (q *jobQueue) pushBack(j *job) {
	j.prev, j.next, j.inQueue = q.tail, nil, true
	if q.tail != nil {
		q.tail.next = j
	} else {
		q.head = j
	}
	q.tail = j
	q.n++
}

func (q *jobQueue) pushFront(j *job) {
	j.prev, j.next, j.inQueue = nil, q.head, true
	if q.head != nil {
		q.head.prev = j
	} else {
		q.tail = j
	}
	q.head = j
	q.n++
}

func (q *jobQueue) remove(j *job) {
	if j.prev != nil {
		j.prev.next = j.next
	} else {
		q.head = j.next
	}
	if j.next != nil {
		j.next.prev = j.prev
	} else {
		q.tail = j.prev
	}
	j.prev, j.next, j.inQueue = nil, nil, false
	q.n--
}
