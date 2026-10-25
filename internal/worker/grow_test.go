package worker

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

// states counts the slots in each state.
func states(p *Pool) map[State]int {
	out := map[State]int{}
	for _, w := range p.Workers() {
		out[w.State]++
	}
	return out
}

func growConfig(workers, maxWorkers int) Config {
	cfg := testConfig(workers)
	cfg.MaxWorkers = maxWorkers
	cfg.ScaleDownAfter = time.Minute
	cfg.ExecTTL = 0
	return cfg
}

// A pool grows to MaxWorkers while jobs wait and shrinks back once its extra
// workers have been idle for ScaleDownAfter.
func TestPoolGrows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, growConfig(2, 6), &fake)
		synctest.Wait() // the slots mark themselves ready after Start returns
		if n, st := fake.Started(), states(p); n != 2 || st[StateParked] != 4 || st[StateReady] != 2 {
			t.Fatalf("after Start: %d started, states %v; want 2, and 4 parked", n, st)
		}

		start := time.Now()
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() { mustExec(t, p, "sleep 10s") })
		}
		synctest.Wait()
		if n, st := fake.Started(), states(p); n != 6 || st[StateWorking] != 6 {
			t.Errorf("under load: %d started, states %v; want 6 working", n, st)
		}
		wg.Wait()
		if elapsed := time.Since(start); elapsed != 20*time.Second {
			t.Errorf("10 ten-second jobs took %s, want 20s on 6 workers", elapsed)
		}
		if h := p.Health(); h != Healthy {
			t.Errorf("Health = %s", h)
		}

		// A steady trickle on the base workers lets the extra ones go idle.
		for range 6 {
			mustExec(t, p, "sleep 10s")
		}
		synctest.Wait()
		if st := states(p); st[StateParked] != 4 || len(fake.Running()) != 2 {
			t.Errorf("after a minute of light load: states %v, %d running; want 4 parked, 2 running", st,
				len(fake.Running()))
		}

		// And it grows again.
		for range 3 {
			wg.Go(func() { mustExec(t, p, "sleep 10s") })
		}
		synctest.Wait()
		if st := states(p); st[StateWorking] != 3 {
			t.Errorf("states %v, want 3 working", st)
		}
		wg.Wait()
	})
}

// At most max(1, Workers/4) extra workers boot at once, and only while jobs
// wait that no booting worker will take.
func TestPoolGrowBoots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(n int) workertest.Options {
			if n < 8 {
				return workertest.Options{}
			}
			return workertest.Options{BootTime: 5 * time.Second}
		}}
		p := startTestPool(t, growConfig(8, 16), &fake) // two boots at once

		var wg sync.WaitGroup
		defer wg.Wait()
		for range 11 { // 3 wait
			wg.Go(func() { mustExec(t, p, "sleep 1m") })
		}
		synctest.Wait()
		if st := states(p); st[StateStarting] != 2 {
			t.Errorf("states %v, want 2 starting", st)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if st := states(p); st[StateStarting] != 1 || st[StateWorking] != 10 {
			t.Errorf("after one boot time: states %v, want 10 working and 1 starting", st)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if st := states(p); st[StateStarting] != 0 || st[StateWorking] != 11 || st[StateParked] != 5 {
			t.Errorf("after two boot times: states %v, want 11 working and 5 parked", st)
		}
	})
}

// Jobs held back by their lane's MaxWorkers don't grow the pool.
func TestPoolGrowLaneLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := newTestPool(t, growConfig(1, 3), &fake)
		one := mustLane(t, p, LaneConfig{Name: "one", MaxWorkers: 1})
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })

		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				if _, err := one.Exec(t.Context(), Payload{Body: []byte("sleep 1s")}); err != nil {
					t.Errorf("Exec: %v", err)
				}
			})
		}
		synctest.Wait()
		if n := fake.Started(); n != 1 {
			t.Errorf("%d workers started for a lane limited to 1, want 1", n)
		}
		wg.Wait()
	})
}

// An extra worker that fails to start waits out the backoff before its slot
// can start another; the base workers keep the pool healthy.
func TestPoolGrowStartFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var broken atomic.Bool
		fake := workertest.Fake{Behave: func(int) workertest.Options {
			if broken.Load() {
				return workertest.Options{BootFailure: "no database"}
			}
			return workertest.Options{}
		}}
		rec := newRecorder()
		cfg := growConfig(1, 2)
		cfg.Observer = rec
		p := startTestPool(t, cfg, &fake)
		broken.Store(true)

		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 1m") })
		synctest.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 1s") }) // waits: the extra worker fails to start
		synctest.Wait()
		if st := states(p); st[StateBackoff] != 1 || p.Health() != Healthy {
			t.Errorf("states %v, health %s; want the extra slot in backoff, healthy", st, p.Health())
		}
		if rec.replacements()[ReasonStartFailed] != 1 {
			t.Errorf("replacements %v", rec.replacements())
		}
		broken.Store(false)
		time.Sleep(backoffBase) // the slot parks again and is woken: the job still waits
		synctest.Wait()
		if st := states(p); st[StateWorking] != 2 {
			t.Errorf("after the backoff: states %v, want 2 working", st)
		}
	})
}

func TestGrowConfig(t *testing.T) {
	for _, tt := range []struct {
		workers, max int
		scaleDown    time.Duration
		want         string
	}{
		{4, 2, time.Minute, "MaxWorkers: must be between the 4 workers and 1024, got 2"},
		{4, 2000, time.Minute, "MaxWorkers: must be between the 4 workers and 1024"},
		{4, 8, 0, "ScaleDownAfter: must be positive while the pool can grow"},
	} {
		cfg := testConfig(tt.workers)
		cfg.MaxWorkers, cfg.ScaleDownAfter = tt.max, tt.scaleDown
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Validate(%d, %d, %s) = %v, want %q", tt.workers, tt.max, tt.scaleDown, err, tt.want)
		}
	}
	for _, maxWorkers := range []int{0, 4} {
		cfg := testConfig(4)
		cfg.MaxWorkers, cfg.ScaleDownAfter = maxWorkers, 0
		if err := cfg.Validate(); err != nil {
			t.Errorf("a fixed size (max %d): %v", maxWorkers, err)
		}
	}
}
