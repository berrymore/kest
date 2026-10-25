package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

// testConfig returns the defaults with n workers and a placeholder command,
// without overload control, which the tests of other features keep queues
// long enough to set off (lane_test.go tests it).
func testConfig(n int) Config {
	cfg := DefaultConfig()
	cfg.Command = []string{"fake-worker"}
	cfg.Workers = n
	cfg.OverloadQueueTimeout = 0
	return cfg
}

// newTestPool builds a pool of in-process fake workers, without max_jobs
// jitter. Use it inside a synctest bubble.
func newTestPool(t *testing.T, cfg Config, fake *workertest.Fake) *Pool {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	spawn := func(context.Context) (process, error) { return fake.Start(cfg.MaxFrameSize), nil }
	p := newPool(cfg, discard, spawn)
	p.jitter = func(n uint64) uint64 { return n }
	return p
}

// startTestPool starts a fake pool and stops it when the test ends.
func startTestPool(t *testing.T, cfg Config, fake *workertest.Fake) *Pool {
	t.Helper()
	p := newTestPool(t, cfg, fake)
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	return p
}

func execBody(t *testing.T, p *Pool, body string) (Payload, error) {
	t.Helper()
	return p.Exec(t.Context(), Payload{Context: []byte("ctx"), Body: []byte(body)})
}

func mustExec(t *testing.T, p *Pool, body string) {
	t.Helper()
	resp, err := execBody(t, p, body)
	if err != nil {
		t.Fatalf("Exec %q: %v", body, err)
	}
	if string(resp.Context) != "ctx" || string(resp.Body) != body {
		t.Fatalf("Exec %q = %q %q, want the echo", body, resp.Context, resp.Body)
	}
}

func TestPoolLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := newTestPool(t, testConfig(2), &fake)
		if _, err := execBody(t, p, "early"); !errors.Is(err, ErrStopped) {
			t.Errorf("Exec before Start = %v, want %v", err, ErrStopped)
		}
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := p.Start(t.Context()); err == nil {
			t.Error("second Start succeeded")
		}

		for i := range 10 {
			mustExec(t, p, fmt.Sprintf("job %d", i))
		}
		synctest.Wait()
		var jobs uint64
		pids := map[int]bool{}
		for _, w := range p.Workers() {
			if w.State != StateReady || w.Pid == 0 {
				t.Errorf("worker %+v, want a ready process", w)
			}
			jobs += w.Jobs
			pids[w.Pid] = true
		}
		if jobs != 10 || len(pids) != 2 {
			t.Errorf("%d jobs on %d processes, want 10 on 2", jobs, len(pids))
		}
		if h := p.Health(); h != Healthy {
			t.Errorf("Health = %s, want healthy", h)
		}

		if err := p.Stop(t.Context()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if n := len(fake.Running()); n != 0 || fake.Started() != 2 {
			t.Errorf("after Stop: %d running of %d started, want 0 of 2", n, fake.Started())
		}
		for _, proc := range []int{10000, 10001} {
			if err := fake.Process(proc).Err(); err != nil {
				t.Errorf("worker %d exit: %v, want a graceful exit", proc, err)
			}
		}
		for _, w := range p.Workers() {
			if w.State != StateStopped || w.Pid != 0 {
				t.Errorf("after Stop: worker %+v", w)
			}
		}
		if h := p.Health(); h != Unhealthy {
			t.Errorf("Health after Stop = %s, want unhealthy", h)
		}
		if _, err := execBody(t, p, "late"); !errors.Is(err, ErrStopped) {
			t.Errorf("Exec after Stop = %v, want %v", err, ErrStopped)
		}
		if err := p.Stop(t.Context()); err != nil {
			t.Errorf("second Stop: %v", err)
		}
	})
}

func TestPoolRunsJobsInParallel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(4), &fake)

		start := time.Now()
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() { mustExec(t, p, "sleep 1s") })
		}
		wg.Wait()
		if elapsed := time.Since(start); elapsed != 25*time.Second {
			t.Errorf("100 one-second jobs on 4 workers took %s, want 25s", elapsed)
		}
	})
}

func TestPoolQueueLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.MaxQueueSize = 2
		cfg.AllocateTimeout = time.Hour
		p := startTestPool(t, cfg, &fake)

		var wg sync.WaitGroup
		wg.Go(func() { mustExec(t, p, "sleep 30s") }) // runs
		synctest.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 30s") }) // waits
		wg.Go(func() { mustExec(t, p, "sleep 30s") }) // waits
		synctest.Wait()

		if _, err := execBody(t, p, "one too many"); !errors.Is(err, ErrQueueFull) {
			t.Errorf("Exec with a full queue = %v, want %v", err, ErrQueueFull)
		}
		wg.Wait()
		mustExec(t, p, "fits again")
	})
}

func TestPoolAllocateTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.AllocateTimeout = time.Minute
		cfg.ExecTTL = 0
		p := startTestPool(t, cfg, &fake)

		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 10m") })
		synctest.Wait()

		start := time.Now()
		if _, err := execBody(t, p, "waits"); !errors.Is(err, ErrNoWorker) {
			t.Errorf("Exec = %v, want %v", err, ErrNoWorker)
		}
		if elapsed := time.Since(start); elapsed != time.Minute {
			t.Errorf("gave up after %s, want 1m", elapsed)
		}
		if n := p.waiting.Load(); n != 0 {
			t.Errorf("%d callers counted as waiting, want 0", n)
		}
	})
}

func TestPoolCallerGivesUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(1), &fake)
		start := time.Now()

		// While the job runs: the caller leaves, the worker finishes the job.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if _, err := p.Exec(ctx, Payload{Body: []byte("sleep 30s")}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Exec = %v, want %v", err, context.DeadlineExceeded)
		}
		if elapsed := time.Since(start); elapsed != 10*time.Second {
			t.Errorf("Exec returned after %s, want 10s", elapsed)
		}

		// While waiting for a worker: the caller leaves the queue.
		ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if _, err := p.Exec(ctx, Payload{Body: []byte("queued")}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("queued Exec = %v, want %v", err, context.DeadlineExceeded)
		}

		mustExec(t, p, "next")
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Errorf("the next job ran after %s, want 30s: the worker should finish the abandoned job", elapsed)
		}
		if fake.Started() != 1 {
			t.Errorf("%d workers started, want 1: abandoning a job must not kill the worker", fake.Started())
		}
		if n := p.waiting.Load(); n != 0 {
			t.Errorf("%d callers counted as waiting, want 0", n)
		}

		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := p.Exec(canceled, Payload{}); !errors.Is(err, context.Canceled) {
			t.Errorf("Exec with a canceled context = %v", err)
		}
	})
}

func TestPoolMaxJobs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.MaxJobs = 3
		p := startTestPool(t, cfg, &fake)

		for i := range 7 {
			mustExec(t, p, fmt.Sprintf("job %d", i))
		}
		synctest.Wait()
		if fake.Started() != 3 {
			t.Errorf("%d workers started for 7 jobs with max_jobs 3, want 3", fake.Started())
		}
		if w := p.Workers()[0]; w.Jobs != 1 || w.Pid != 10002 {
			t.Errorf("worker %+v, want pid 10002 with 1 job", w)
		}
		for _, pid := range []int{10000, 10001} {
			if err := fake.Process(pid).Err(); err != nil {
				t.Errorf("recycled worker %d: %v, want a graceful exit", pid, err)
			}
		}
	})
}

func TestMaxJobsJitter(t *testing.T) {
	if got := maxJobsJitter(0); got != 0 {
		t.Errorf("jitter(0) = %d, want 0", got)
	}
	seen := map[uint64]bool{}
	for range 2000 {
		got := maxJobsJitter(100)
		if got < 100 || got > 114 {
			t.Fatalf("jitter(100) = %d, want 100…114", got)
		}
		seen[got] = true
	}
	if len(seen) < 10 {
		t.Errorf("jitter(100) took %d distinct values, want most of 15", len(seen))
	}
}

func TestPoolTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.TTL = time.Hour
		cfg.ExecTTL = 0
		p := startTestPool(t, cfg, &fake)

		time.Sleep(59 * time.Minute)
		synctest.Wait()
		if fake.Started() != 1 {
			t.Fatalf("replaced before the ttl")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if fake.Started() != 2 {
			t.Fatalf("an idle worker past its ttl was not replaced: %d started", fake.Started())
		}

		// A busy worker finishes its job first.
		start := time.Now()
		mustExec(t, p, "sleep 90m")
		synctest.Wait()
		if fake.Started() != 3 {
			t.Errorf("a worker past its ttl after a job was not replaced: %d started", fake.Started())
		}
		if elapsed := time.Since(start); elapsed != 90*time.Minute {
			t.Errorf("the job took %s, want 90m: the ttl must not cut it", elapsed)
		}
	})
}

func TestPoolIdleTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.IdleTTL = 10 * time.Minute
		cfg.ExecTTL = 0
		p := startTestPool(t, cfg, &fake)

		// Jobs keep the worker.
		for range 3 {
			time.Sleep(9 * time.Minute)
			mustExec(t, p, "keepalive")
		}
		synctest.Wait()
		if fake.Started() != 1 {
			t.Fatalf("a worker in use was replaced")
		}
		// A long job is not idle time.
		mustExec(t, p, "sleep 15m")
		time.Sleep(9 * time.Minute)
		synctest.Wait()
		if fake.Started() != 1 {
			t.Fatalf("a worker was replaced during or right after a long job")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if fake.Started() != 2 {
			t.Errorf("a worker idle for idle_ttl was not replaced: %d started", fake.Started())
		}
		// A worker that never got a job is idle too.
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if fake.Started() != 3 {
			t.Errorf("an unused worker was not replaced after idle_ttl: %d started", fake.Started())
		}
	})
}

func TestPoolExecTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.ExecTTL = time.Minute
		p := startTestPool(t, cfg, &fake)

		start := time.Now()
		_, err := execBody(t, p, "sleep 2m")
		if !errors.Is(err, ErrExecTimeout) {
			t.Fatalf("Exec = %v, want %v", err, ErrExecTimeout)
		}
		if elapsed := time.Since(start); elapsed != time.Minute {
			t.Errorf("timed out after %s, want 1m", elapsed)
		}
		mustExec(t, p, "after the timeout")
		if fake.Started() != 2 || !errors.Is(fake.Process(10000).Err(), workertest.ErrKilled) {
			t.Errorf("the timed-out worker should be killed and replaced: %d started", fake.Started())
		}
	})
}

func TestPoolWorkerFailures(t *testing.T) {
	for _, body := range []string{"crash", "exit", "garbage", "no-options", "big 100000"} {
		t.Run(body, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var fake workertest.Fake
				cfg := testConfig(1)
				cfg.MaxFrameSize = 1024
				p := startTestPool(t, cfg, &fake)

				_, err := execBody(t, p, body)
				if pe, ok := errors.AsType[*ProcessError](err); !ok || pe.Pid != 10000 || pe.Op != "exec" {
					t.Fatalf("Exec = %v, want an exec *ProcessError for worker 10000", err)
				}
				mustExec(t, p, "on the replacement")
				if fake.Started() != 2 {
					t.Errorf("%d workers started, want 2", fake.Started())
				}
			})
		})
	}
}

func TestPoolSoftError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(1), &fake)

		_, err := execBody(t, p, "error invalid input")
		if je, ok := errors.AsType[*JobError](err); !ok || je.Message != "invalid input" {
			t.Fatalf("Exec = %v, want a *JobError", err)
		}
		mustExec(t, p, "same worker")
		if fake.Started() != 1 {
			t.Errorf("a soft error replaced the worker")
		}
	})
}

func TestPoolStopRequest(t *testing.T) {
	t.Run("retried on another worker", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := workertest.Fake{Behave: func(n int) workertest.Options {
				return workertest.Options{AskStop: n == 0}
			}}
			p := startTestPool(t, testConfig(1), &fake)
			mustExec(t, p, "hello")
			if fake.Started() != 2 || fake.Process(10000).Err() != nil {
				t.Errorf("the worker that asked should stop gracefully and be replaced: %d started", fake.Started())
			}
		})
	})

	t.Run("retried once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := workertest.Fake{Behave: func(int) workertest.Options { return workertest.Options{AskStop: true} }}
			p := startTestPool(t, testConfig(1), &fake)
			if _, err := execBody(t, p, "hello"); !errors.Is(err, ErrStopRequested) {
				t.Errorf("Exec = %v, want %v", err, ErrStopRequested)
			}
			synctest.Wait()
			if fake.Started() != 3 {
				t.Errorf("%d workers started, want 3: two asked, one replaces them", fake.Started())
			}
		})
	})
}

func TestPoolReplacesExitedIdleWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(2), &fake)
		before := p.Workers()

		fake.Process(before[1].Pid).Kill()
		synctest.Wait() // the slot notices and waits for the backoff
		if h := p.Health(); h != Degraded {
			t.Errorf("Health = %s, want degraded", h)
		}
		time.Sleep(backoffBase)
		synctest.Wait()
		if fake.Started() != 3 || p.Health() != Healthy {
			t.Errorf("after the backoff: %d started, %s; want 3, healthy", fake.Started(), p.Health())
		}
		after := p.Workers()
		if after[0].Pid != before[0].Pid || after[1].Pid != 10002 {
			t.Errorf("pids %d %d → %d %d, want slot 1 to get 10002", before[0].Pid, before[1].Pid, after[0].Pid, after[1].Pid)
		}
	})
}

func TestPoolStartFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workertest.Fake{Behave: func(n int) workertest.Options {
			if n == 1 {
				return workertest.Options{BootFailure: "no database"}
			}
			return workertest.Options{}
		}}
		p := newTestPool(t, testConfig(3), &fake)
		err := p.Start(t.Context())
		if pe, ok := errors.AsType[*ProcessError](err); !ok || pe.Stderr != "no database" {
			t.Fatalf("Start = %v, want a *ProcessError with the worker's stderr", err)
		}
		if n := len(fake.Running()); n != 0 {
			t.Errorf("%d workers still running after a failed Start", n)
		}
		if _, err := execBody(t, p, "x"); !errors.Is(err, ErrStopped) {
			t.Errorf("Exec after a failed Start = %v, want %v", err, ErrStopped)
		}
	})
}

func TestPoolStartTimeouts(t *testing.T) {
	slow := func(int) workertest.Options { return workertest.Options{BootTime: 2 * time.Minute} }

	t.Run("start timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := workertest.Fake{Behave: slow}
			cfg := testConfig(2)
			cfg.StartTimeout = time.Minute
			p := newTestPool(t, cfg, &fake)
			start := time.Now()
			if err := p.Start(t.Context()); !errors.Is(err, ErrStartTimeout) {
				t.Errorf("Start = %v, want %v", err, ErrStartTimeout)
			}
			if elapsed := time.Since(start); elapsed != time.Minute {
				t.Errorf("Start failed after %s, want 1m", elapsed)
			}
		})
	})

	t.Run("caller deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := workertest.Fake{Behave: slow}
			p := newTestPool(t, testConfig(2), &fake)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if err := p.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Start = %v, want %v", err, context.DeadlineExceeded)
			}
			if n := len(fake.Running()); n != 0 {
				t.Errorf("%d workers still running", n)
			}
		})
	})
}

func TestPoolRespawnBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var broken atomic.Bool
		fake := workertest.Fake{Behave: func(int) workertest.Options {
			if broken.Load() {
				return workertest.Options{BootFailure: "broken deploy"}
			}
			return workertest.Options{}
		}}
		p := startTestPool(t, testConfig(1), &fake)

		broken.Store(true)
		fake.Process(10000).Kill()
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		// Exponential backoff from 100ms to 30s with jitter in [d/2, d): about
		// 25–51s to reach the cap, then one start every 15–30s.
		if n := fake.Started(); n < 15 || n > 60 {
			t.Errorf("%d starts in 10 minutes of failures, want 15…60", n)
		}
		if h := p.Health(); h != Unhealthy {
			t.Errorf("Health = %s, want unhealthy", h)
		}
		if st := p.Workers()[0].State; st != StateBackoff {
			t.Errorf("state = %s, want backoff", st)
		}

		broken.Store(false)
		time.Sleep(backoffMax)
		synctest.Wait()
		if h := p.Health(); h != Healthy {
			t.Errorf("Health after the fix = %s, want healthy", h)
		}
		mustExec(t, p, "recovered")
	})
}

func TestBackoff(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 9: 25600 * time.Millisecond,
		10: backoffMax, 100: backoffMax,
	} {
		for range 100 {
			if got := backoff(failures); got < want/2 || got >= want {
				t.Fatalf("backoff(%d) = %s, want [%s, %s)", failures, got, want/2, want)
			}
		}
	}
}

func TestPoolStop(t *testing.T) {
	t.Run("running jobs finish", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var fake workertest.Fake
			p := startTestPool(t, testConfig(1), &fake)

			var wg sync.WaitGroup
			wg.Go(func() { mustExec(t, p, "sleep 30s") })
			synctest.Wait()
			var queuedErr error
			wg.Go(func() { _, queuedErr = execBody(t, p, "queued") })
			synctest.Wait()

			start := time.Now()
			if err := p.Stop(t.Context()); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			wg.Wait()
			if elapsed := time.Since(start); elapsed != 30*time.Second {
				t.Errorf("Stop took %s, want 30s (the running job)", elapsed)
			}
			if !errors.Is(queuedErr, ErrStopped) {
				t.Errorf("queued Exec = %v, want %v", queuedErr, ErrStopped)
			}
			if err := fake.Process(10000).Err(); err != nil {
				t.Errorf("worker exit: %v, want graceful", err)
			}
		})
	})

	t.Run("deadline kills", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var fake workertest.Fake
			cfg := testConfig(2)
			cfg.ExecTTL = 0
			p := startTestPool(t, cfg, &fake)

			var wg sync.WaitGroup
			var hangErr error
			wg.Go(func() { _, hangErr = execBody(t, p, "hang") })
			synctest.Wait()

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			start := time.Now()
			if err := p.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			wg.Wait()
			if elapsed := time.Since(start); elapsed != 10*time.Second {
				t.Errorf("Stop took %s, want 10s", elapsed)
			}
			if _, ok := errors.AsType[*ProcessError](hangErr); !ok {
				t.Errorf("the killed job's Exec = %v, want a *ProcessError", hangErr)
			}
			if n := len(fake.Running()); n != 0 {
				t.Errorf("%d workers still running", n)
			}
		})
	})

	t.Run("destroy timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := workertest.Fake{Behave: func(int) workertest.Options { return workertest.Options{IgnoreStop: true} }}
			cfg := testConfig(3)
			cfg.DestroyTimeout = 10 * time.Second
			p := startTestPool(t, cfg, &fake)

			start := time.Now()
			if err := p.Stop(t.Context()); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if elapsed := time.Since(start); elapsed != 10*time.Second {
				t.Errorf("Stop took %s, want 10s: workers stop in parallel", elapsed)
			}
			for _, w := range fake.Running() {
				t.Errorf("worker %d still running", w.Pid())
			}
		})
	})
}

func TestConfigValidate(t *testing.T) {
	cfg := testConfig(4)
	if err := cfg.Validate(); err != nil {
		t.Errorf("the defaults are invalid: %v", err)
	}

	bad := Config{
		Workers:         WorkerLimit + 1,
		MaxQueueSize:    0,
		MaxFrameSize:    0,
		AllocateTimeout: 0,
		StartTimeout:    -1,
		DestroyTimeout:  0,
		TTL:             -1,
		IdleTTL:         -1,
		ExecTTL:         -1,
		MaxWorkerMemory: 1,
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("Validate accepted an invalid config")
	}
	for _, field := range []string{
		"Command:", "Workers:", "MaxQueueSize:", "MaxFrameSize:", "AllocateTimeout:", "StartTimeout:",
		"DestroyTimeout:", "TTL:", "IdleTTL:", "ExecTTL:", "WatchTick:",
	} {
		if !strings.Contains(err.Error(), "\n"+field) && !strings.HasPrefix(err.Error(), field) {
			t.Errorf("no error for %s in:\n%v", strings.TrimSuffix(field, ":"), err)
		}
	}
	if _, err := New(bad, discard); err == nil {
		t.Error("New accepted an invalid config")
	}
}
