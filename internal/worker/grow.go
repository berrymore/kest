package worker

import "context"

// A pool with MaxWorkers above Workers grows: its extra slots run no process
// until jobs wait that their lanes would let through, and then start one, at
// most maxBoots at a time, since booting takes CPU from the workers that
// serve. RoadRunner's dynamic allocator grows only after a job has waited
// the whole allocate timeout. An extra worker idle for ScaleDownAfter stops,
// and its slot parks again; the dispatcher gives jobs to base slots first, so
// the extra ones go idle when the load drops.

// reasonScaleDown is logged when an extra worker stops for being idle. It is
// not a replacement: Observer.Replaced doesn't hear it.
const reasonScaleDown = "scale_down"

// pendingLocked counts the queued jobs that a new worker could take: those of
// lanes under their MaxWorkers.
func (p *Pool) pendingLocked() int {
	n := 0
	for _, l := range p.lanes {
		q := l.queue.n
		if l.cfg.MaxWorkers > 0 {
			q = min(q, max(0, l.cfg.MaxWorkers-l.busy))
		}
		n += q
	}
	return n
}

// growLocked wakes parked extra slots while jobs wait that no booting worker
// will take, up to maxBoots booting at once.
func (p *Pool) growLocked() {
	if p.run.Err() != nil {
		return
	}
	for p.booting < p.maxBoots && len(p.parked) > 0 && p.pendingLocked() > p.booting {
		s := p.parked[len(p.parked)-1]
		p.parked = p.parked[:len(p.parked)-1]
		p.booting++
		s.booting = true
		s.wake <- struct{}{} // the slot is parked and was not woken since: never blocks
	}
}

// bootedLocked counts a slot's boot as done: when its new worker parks (and
// takes a job, which no longer waits), or when the boot fails.
func (p *Pool) bootedLocked(s *slot) bool {
	if !s.booting {
		return false
	}
	s.booting = false
	p.booting--
	return true
}

// bootFailed counts a failed boot as done, which may let another start.
func (p *Pool) bootFailed(s *slot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bootedLocked(s) {
		p.growLocked()
	}
}

// repark puts an extra slot without a process back among the parked ones,
// and wakes it again at once if jobs wait.
func (p *Pool) repark(s *slot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parked = append(p.parked, s)
	p.growLocked()
}

// runExtra owns an extra slot until ctx, the pool's run context, ends: parked
// without a process until growLocked wakes it, then serving until its worker
// has been idle for ScaleDownAfter or its process ends (a limit, a crash),
// then parked again. A worker that fails to start, or fails early, waits for
// the backoff before the slot can be woken again.
func (s *slot) runExtra(ctx context.Context, first chan<- error) {
	p := s.pool
	defer s.update(func(i *Info) { *i = Info{Slot: i.Slot, State: StateStopped} })
	first <- nil
	failures := 0
	for {
		s.update(func(i *Info) { *i = Info{Slot: i.Slot, State: StateParked} })
		select {
		case <-s.wake:
		case <-ctx.Done():
			return
		}
		proc, err := s.start(ctx)
		if err != nil {
			p.bootFailed(s)
		}
		failed := err != nil
		switch {
		case err == nil:
			p.logger.Info("worker added under load", "slot", s.id, "worker_pid", proc.Pid())
			for {
				next, failedEarly := s.serve(ctx, proc)
				if next == nil {
					failed = failedEarly
					break
				}
				s.adopt(next) // a reset handed over a booted process
				proc = next.proc
			}
		case ctx.Err() == nil:
			p.obs.Replaced(ReasonStartFailed)
			p.logger.Error("worker failed to start", "slot", s.id, "err", err)
		}
		if ctx.Err() != nil {
			return
		}
		if !failed {
			failures = 0
		} else {
			failures++
			s.update(func(i *Info) { i.State = StateBackoff })
			if !sleep(ctx, backoff(failures)) {
				return
			}
		}
		p.repark(s)
	}
}
