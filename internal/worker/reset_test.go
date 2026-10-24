package worker

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

func pids(p *Pool) []int {
	var out []int
	for _, w := range p.Workers() {
		out = append(out, w.Pid)
	}
	return out
}

// A reset under load replaces every worker, fails no request, and never
// lowers the number of live workers.
func TestPoolReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(int) workertest.Options {
			return workertest.Options{BootTime: 2 * time.Second}
		}}
		cfg := testConfig(3)
		cfg.ExecTTL = 0
		p, rec := observedPool(t, cfg, &fake)
		before := pids(p)

		stop := make(chan struct{})
		var load sync.WaitGroup
		for range 6 {
			load.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					mustExec(t, p, "sleep 300ms")
				}
			})
		}
		var lowest = len(p.slots)
		watch := make(chan struct{})
		go func() {
			defer close(watch)
			for {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
				live := 0
				for _, w := range p.Workers() {
					if w.State == StateReady || w.State == StateWorking {
						live++
					}
				}
				lowest = min(lowest, live)
			}
		}()

		if err := p.Reset(t.Context()); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		time.Sleep(time.Second)
		close(stop)
		load.Wait()
		<-watch

		after := pids(p)
		for i := range after {
			if after[i] == before[i] {
				t.Errorf("slot %d still runs process %d", i, before[i])
			}
		}
		for _, pid := range before {
			if err := fake.Process(pid).Err(); err != nil {
				t.Errorf("old worker %d: %v, want a graceful stop", pid, err)
			}
		}
		if lowest < len(p.slots) {
			t.Errorf("live workers dropped to %d during the reset, want %d", lowest, len(p.slots))
		}
		if got := rec.replacements(); got[ReasonReset] != 3 || len(got) != 1 {
			t.Errorf("replaced %v, want 3 × reset", got)
		}
	})
}

// A replacement that doesn't boot stops the reset; the old workers left
// keep serving.
func TestPoolResetBootFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(n int) workertest.Options {
			if n == 4 { // the replacement of the second slot
				return workertest.Options{BootFailure: "Parse error in the new code"}
			}
			return workertest.Options{}
		}}
		p, rec := observedPool(t, testConfig(3), &fake)
		before := pids(p)

		err := p.Reset(t.Context())
		if err == nil || !strings.Contains(err.Error(), "1 of 3 workers replaced") ||
			!strings.Contains(err.Error(), "Parse error in the new code") {
			t.Fatalf("Reset = %v, want the boot failure after 1 of 3", err)
		}
		synctest.Wait()
		after := pids(p)
		if after[0] == before[0] || after[1] != before[1] || after[2] != before[2] {
			t.Errorf("pids %v → %v, want only slot 0 replaced", before, after)
		}
		for range 6 {
			mustExec(t, p, "still serving")
		}
		if got := rec.replacements(); got[ReasonReset] != 1 || len(got) != 1 {
			t.Errorf("replaced %v, want 1 × reset", got)
		}
	})
}

// A busy worker finishes its job before its replacement takes over.
func TestPoolResetWaitsForTheJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.ExecTTL = 0
		p := startTestPool(t, cfg, &fake)

		var wg sync.WaitGroup
		wg.Go(func() { mustExec(t, p, "sleep 30s") })
		synctest.Wait()
		start := time.Now()
		if err := p.Reset(t.Context()); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Errorf("Reset took %s, want 30s: the job first", elapsed)
		}
		wg.Wait()
		if pid := pids(p)[0]; pid != 10001 {
			t.Errorf("slot runs %d, want the replacement 10001", pid)
		}
	})
}

func TestPoolResetConcurrentAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(n int) workertest.Options {
			if n > 0 {
				return workertest.Options{BootTime: time.Hour} // replacements boot slowly
			}
			return workertest.Options{}
		}}
		p := newTestPool(t, testConfig(1), &fake)
		if err := p.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		first := make(chan error, 1)
		go func() { first <- p.Reset(t.Context()) }()
		synctest.Wait()
		if err := p.Reset(t.Context()); !errors.Is(err, ErrResetRunning) {
			t.Errorf("second Reset = %v, want %v", err, ErrResetRunning)
		}
		mustExec(t, p, "the old worker serves")

		if err := p.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-first; !errors.Is(err, ErrStopped) {
			t.Errorf("Reset during Stop = %v, want %v", err, ErrStopped)
		}
		if err := p.Reset(t.Context()); !errors.Is(err, ErrStopped) {
			t.Errorf("Reset after Stop = %v, want %v", err, ErrStopped)
		}
		synctest.Wait()
		if n := len(fake.Running()); n != 0 {
			t.Errorf("%d processes left after Stop", n)
		}
	})
}

// A slot that replaced its worker on its own while the reset booted one keeps
// the new worker and drops the offered one.
func TestPoolResetSkipsNewWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(n int) workertest.Options {
			if n == 1 {
				return workertest.Options{BootTime: 10 * time.Second} // the reset's replacement
			}
			return workertest.Options{}
		}}
		cfg := testConfig(1)
		cfg.MaxJobs = 1
		p, rec := observedPool(t, cfg, &fake)

		done := make(chan error, 1)
		go func() { done <- p.Reset(t.Context()) }()
		synctest.Wait()
		mustExec(t, p, "replaced after this job") // max_jobs: process 2 starts, after the reset began
		if err := <-done; err != nil {
			t.Fatalf("Reset: %v", err)
		}
		synctest.Wait()
		if pid := pids(p)[0]; pid != 10002 {
			t.Errorf("slot runs %d, want 10002, the one max_jobs started", pid)
		}
		if err := fake.Process(10001).Err(); err != nil {
			t.Errorf("the dropped replacement: %v, want a graceful stop", err)
		}
		if got := rec.replacements(); got[ReasonMaxJobs] != 1 || got[ReasonReset] != 0 {
			t.Errorf("replaced %v, want 1 × max_jobs and no reset", got)
		}
	})
}
