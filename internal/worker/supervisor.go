package worker

import (
	"context"
	"time"
)

// memorySampler returns the RSS in bytes of each pid it could measure.
type memorySampler func(ctx context.Context, pids []int) (map[int]int64, error)

// supervise samples worker memory every WatchTick until ctx ends. Other
// limits (ttl, idle_ttl, exec_ttl, max_jobs) are timers and counters in the
// slots; memory is the one that needs polling.
func (p *Pool) supervise(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.WatchTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.checkMemory(ctx)
		}
	}
}

// checkMemory samples the live workers and tells the slots of those over
// MaxWorkerMemory. A ready worker is replaced at once, a busy one after its
// job.
func (p *Pool) checkMemory(ctx context.Context) {
	var pids []int
	for _, s := range p.slots {
		if w := s.snapshot(); w.Pid != 0 && (w.State == StateReady || w.State == StateWorking) {
			pids = append(pids, w.Pid)
		}
	}
	if len(pids) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.WatchTick)
	defer cancel()
	rss, err := p.sample(ctx, pids)
	if err != nil {
		p.logger.Warn("could not sample worker memory", "err", err)
		return
	}
	for _, s := range p.slots {
		s.recordMemory(rss, p.cfg.MaxWorkerMemory, p.logger)
	}
}
