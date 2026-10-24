package worker

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

// recorder is an Observer that keeps what it is told.
type recorder struct {
	mu       sync.Mutex
	waits    []time.Duration
	rejected map[Reason]int
	replaced map[Reason]int
}

func newRecorder() *recorder {
	return &recorder{rejected: map[Reason]int{}, replaced: map[Reason]int{}}
}

func (r *recorder) Waited(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waits = append(r.waits, d)
}

func (r *recorder) Rejected(reason Reason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejected[reason]++
}

func (r *recorder) Replaced(reason Reason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replaced[reason]++
}

func (r *recorder) replacements() map[Reason]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.replaced)
}

func observedPool(t *testing.T, cfg Config, fake *workertest.Fake) (*Pool, *recorder) {
	t.Helper()
	rec := newRecorder()
	cfg.Observer = rec
	return startTestPool(t, cfg, fake), rec
}

func TestObserverWaitsAndRejections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.MaxQueueSize = 1
		cfg.AllocateTimeout = time.Minute
		cfg.ExecTTL = 0
		p, rec := observedPool(t, cfg, &fake)

		mustExec(t, p, "free worker")
		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 90s") })
		synctest.Wait()
		wg.Go(func() {
			// Waits for the worker, longer than AllocateTimeout.
			if _, err := execBody(t, p, "gives up"); !errors.Is(err, ErrNoWorker) {
				t.Errorf("Exec = %v, want %v", err, ErrNoWorker)
			}
		})
		synctest.Wait()
		if _, err := execBody(t, p, "queue full"); !errors.Is(err, ErrQueueFull) {
			t.Errorf("Exec = %v, want %v", err, ErrQueueFull)
		}
		if p.Waiting() != 1 {
			t.Errorf("Waiting() = %d, want 1", p.Waiting())
		}
		wg.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 30s") })
		synctest.Wait()
		mustExec(t, p, "waits 30s")

		rec.mu.Lock()
		defer rec.mu.Unlock()
		if want := []time.Duration{0, 0, 0, 30 * time.Second}; !slices.Equal(rec.waits, want) {
			t.Errorf("waits %v, want %v", rec.waits, want)
		}
		if rec.rejected[ReasonQueueFull] != 1 || rec.rejected[ReasonNoWorker] != 1 {
			t.Errorf("rejected %v, want one queue_full and one no_worker", rec.rejected)
		}
	})
}

func TestObserverReplacements(t *testing.T) {
	tests := []struct {
		reason Reason
		cfg    func(*Config)
		run    func(t *testing.T, p *Pool)
	}{
		{ReasonMaxJobs, func(c *Config) { c.MaxJobs = 1 }, func(t *testing.T, p *Pool) {
			t.Helper()
			mustExec(t, p, "x")
		}},
		{ReasonTTL, func(c *Config) { c.TTL = time.Minute }, func(*testing.T, *Pool) { time.Sleep(time.Minute) }},
		{ReasonIdleTTL, func(c *Config) { c.IdleTTL = time.Minute }, func(*testing.T, *Pool) { time.Sleep(time.Minute) }},
		{ReasonExecTTL, func(c *Config) { c.ExecTTL = time.Minute }, func(t *testing.T, p *Pool) {
			t.Helper()
			if _, err := execBody(t, p, "sleep 2m"); !errors.Is(err, ErrExecTimeout) {
				t.Errorf("Exec = %v, want %v", err, ErrExecTimeout)
			}
		}},
		{ReasonCrash, nil, func(t *testing.T, p *Pool) {
			t.Helper()
			if _, err := execBody(t, p, "crash"); err == nil {
				t.Error("crash: no error")
			}
		}},
		{ReasonStopRequested, nil, func(t *testing.T, p *Pool) {
			t.Helper()
			// The fake asks again on the retry: two replacements.
			if _, err := execBody(t, p, "stop"); !errors.Is(err, ErrStopRequested) {
				t.Errorf("Exec = %v, want %v", err, ErrStopRequested)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var fake workertest.Fake
				cfg := testConfig(1)
				if tt.cfg != nil {
					tt.cfg(&cfg)
				}
				p, rec := observedPool(t, cfg, &fake)
				tt.run(t, p)
				synctest.Wait()
				want := 1
				if tt.reason == ReasonStopRequested {
					want = 2
				}
				if got := rec.replacements(); got[tt.reason] != want || len(got) != 1 {
					t.Errorf("replaced %v, want %d × %s", got, want, tt.reason)
				}
			})
		})
	}
}

func TestObserverStartFailedAndExited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(n int) workertest.Options {
			if n == 1 {
				return workertest.Options{BootFailure: "no database"}
			}
			return workertest.Options{}
		}}
		p, rec := observedPool(t, testConfig(1), &fake)
		fake.Process(10000).Kill() // exits while idle
		synctest.Wait()
		time.Sleep(time.Minute) // the backoff after the failed start
		synctest.Wait()
		mustExec(t, p, "third process")
		if got := rec.replacements(); got[ReasonCrash] != 1 || got[ReasonStartFailed] != 1 || len(got) != 2 {
			t.Errorf("replaced %v, want one crash and one start_failed", got)
		}
	})
}

func TestObserverMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		rec := newRecorder()
		cfg := testConfig(1)
		cfg.MaxWorkerMemory = 100 << 20
		cfg.Observer = rec
		p := newTestPool(t, cfg, &fake)
		p.sample = fakeSampler(&fake)
		if err := p.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Stop(context.Background()) }()

		mustExec(t, p, "memory 200000000")
		time.Sleep(cfg.WatchTick)
		synctest.Wait()
		if got := rec.replacements(); got[ReasonMemory] != 1 || len(got) != 1 {
			t.Errorf("replaced %v, want one memory", got)
		}
	})
}

// Stopping the pool replaces nothing.
func TestObserverPoolStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		rec := newRecorder()
		cfg := testConfig(2)
		cfg.Observer = rec
		p := newTestPool(t, cfg, &fake)
		if err := p.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := p.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := rec.replacements(); len(got) != 0 {
			t.Errorf("replaced %v on Stop, want nothing", got)
		}
	})
}
