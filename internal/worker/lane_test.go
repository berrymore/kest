package worker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

func mustLane(t *testing.T, p *Pool, cfg LaneConfig) *Lane {
	t.Helper()
	l, err := p.Lane(cfg)
	if err != nil {
		t.Fatalf("Lane(%+v): %v", cfg, err)
	}
	return l
}

func laneInfo(t *testing.T, p *Pool, name string) LaneInfo {
	t.Helper()
	for _, l := range p.Lanes() {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no lane %q", name)
	return LaneInfo{}
}

// A lane with MaxWorkers never takes more workers than that, and the other
// lanes get the rest.
func TestLaneMaxWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(4)
		cfg.ExecTTL = 0
		p := newTestPool(t, cfg, &fake)
		slow := mustLane(t, p, LaneConfig{Name: "slow", MaxWorkers: 2})
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })

		start := time.Now()
		var slowWG sync.WaitGroup
		for range 10 {
			slowWG.Go(func() {
				if _, err := slow.Exec(t.Context(), Payload{Body: []byte("sleep 10s")}); err != nil {
					t.Errorf("slow Exec: %v", err)
				}
			})
		}
		synctest.Wait()
		if l := laneInfo(t, p, "slow"); l.Busy != 2 || l.Queued != 8 {
			t.Errorf("slow lane: %d busy, %d queued; want 2 and 8", l.Busy, l.Queued)
		}
		if n := p.Waiting(); n != 8 {
			t.Errorf("Waiting() = %d, want 8", n)
		}

		// The default lane has the two other workers.
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() { mustExec(t, p, "sleep 1s") })
		}
		wg.Wait()
		if elapsed := time.Since(start); elapsed != 10*time.Second {
			t.Errorf("20 one-second jobs on the 2 free workers took %s, want 10s", elapsed)
		}

		// With the pool free, the slow lane still runs 2 at a time.
		time.Sleep(time.Second)
		synctest.Wait()
		if l := laneInfo(t, p, "slow"); l.Busy != 2 {
			t.Errorf("slow lane on an idle pool: %d busy, want 2", l.Busy)
		}
		slowWG.Wait()
		if elapsed := time.Since(start); elapsed != 50*time.Second {
			t.Errorf("10 ten-second jobs, 2 at a time, took %s, want 50s", elapsed)
		}
		if l := laneInfo(t, p, "slow"); l.Busy != 0 || l.Queued != 0 {
			t.Errorf("slow lane after its jobs: %+v", l)
		}
	})
}

// A free worker takes the oldest job of the highest-priority lane.
func TestLanePriority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.AllocateTimeout = time.Hour
		p := newTestPool(t, cfg, &fake)
		lanes := map[string]*Lane{
			"high":  mustLane(t, p, LaneConfig{Name: "high", Priority: PriorityHigh}),
			"high2": mustLane(t, p, LaneConfig{Name: "high2", Priority: PriorityHigh}),
			"low":   mustLane(t, p, LaneConfig{Name: "low", Priority: PriorityLow}),
			"":      p.lanes[0],
		}
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })

		start := time.Now()
		var wg sync.WaitGroup
		wg.Go(func() { mustExec(t, p, "sleep 1s") }) // takes the worker
		synctest.Wait()

		var mu sync.Mutex
		done := map[string]time.Duration{}
		for _, job := range []struct{ name, lane string }{
			{"low A", "low"}, {"normal B", ""}, {"high C", "high"}, {"low D", "low"}, {"high E", "high2"},
			{"high F", "high"},
		} {
			wg.Go(func() {
				if _, err := lanes[job.lane].Exec(t.Context(), Payload{Body: []byte("sleep 1s")}); err != nil {
					t.Errorf("%s: %v", job.name, err)
				}
				mu.Lock()
				done[job.name] = time.Since(start)
				mu.Unlock()
			})
			synctest.Wait() // in this order
		}
		wg.Wait()
		for name, want := range map[string]time.Duration{
			"high C": 2 * time.Second, "high E": 3 * time.Second, "high F": 4 * time.Second, // high lanes, oldest first
			"normal B": 5 * time.Second, "low A": 6 * time.Second, "low D": 7 * time.Second,
		} {
			if done[name] != want {
				t.Errorf("%s done after %s, want %s", name, done[name], want)
			}
		}
	})
}

// A lane's queue timeout replaces the pool's allocate timeout.
func TestLaneQueueTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		rec := newRecorder()
		cfg := testConfig(1)
		cfg.AllocateTimeout = time.Minute
		cfg.ExecTTL = 0
		cfg.Observer = rec
		p := newTestPool(t, cfg, &fake)
		fast := mustLane(t, p, LaneConfig{Name: "fast", QueueTimeout: 5 * time.Second})
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })

		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 10m") })
		synctest.Wait()

		start := time.Now()
		if _, err := fast.Exec(t.Context(), Payload{Body: []byte("waits")}); !errors.Is(err, ErrNoWorker) {
			t.Errorf("fast Exec = %v, want %v", err, ErrNoWorker)
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Errorf("fast lane gave up after %s, want 5s", elapsed)
		}
		if _, err := execBody(t, p, "waits"); !errors.Is(err, ErrNoWorker) {
			t.Errorf("Exec = %v, want %v", err, ErrNoWorker)
		}
		if elapsed := time.Since(start); elapsed != 65*time.Second {
			t.Errorf("default lane gave up %s after the start, want 65s (5s, then 1m)", elapsed)
		}
		if rec.rejectedIn["fast no_worker"] != 1 || rec.rejectedIn[" no_worker"] != 1 {
			t.Errorf("rejections by lane: %v", rec.rejectedIn)
		}
		if n := p.Waiting(); n != 0 {
			t.Errorf("Waiting() = %d, want 0", n)
		}
	})
}

// MaxQueueSize bounds each lane's queue, so a full lane doesn't turn away
// the jobs of another.
func TestLaneQueueFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		rec := newRecorder()
		cfg := testConfig(1)
		cfg.MaxQueueSize = 2
		cfg.AllocateTimeout = time.Hour
		cfg.Observer = rec
		p := newTestPool(t, cfg, &fake)
		other := mustLane(t, p, LaneConfig{Name: "other"})
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })

		var wg sync.WaitGroup
		wg.Go(func() { mustExec(t, p, "sleep 30s") }) // runs
		synctest.Wait()
		for range 2 {
			wg.Go(func() { mustExec(t, p, "sleep 1s") })
			wg.Go(func() {
				if _, err := other.Exec(t.Context(), Payload{Body: []byte("sleep 1s")}); err != nil {
					t.Errorf("other Exec: %v", err)
				}
			})
		}
		synctest.Wait()
		if _, err := execBody(t, p, "one too many"); !errors.Is(err, ErrQueueFull) {
			t.Errorf("Exec with a full queue = %v, want %v", err, ErrQueueFull)
		}
		if _, err := other.Exec(t.Context(), Payload{Body: []byte("one too many")}); !errors.Is(err, ErrQueueFull) {
			t.Errorf("other Exec with a full queue = %v, want %v", err, ErrQueueFull)
		}
		if rec.rejectedIn[" queue_full"] != 1 || rec.rejectedIn["other queue_full"] != 1 {
			t.Errorf("rejections by lane: %v", rec.rejectedIn)
		}
		wg.Wait()
		if rec.waitedIn["other"] != 2 || rec.waitedIn[""] != 3 {
			t.Errorf("waits by lane: %v", rec.waitedIn)
		}
	})
}

func TestLaneConfig(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := newTestPool(t, testConfig(2), &fake)
		mustLane(t, p, LaneConfig{Name: "a", MaxWorkers: 2, Priority: PriorityLow, QueueTimeout: time.Second})
		for _, tt := range []struct {
			cfg  LaneConfig
			want string
		}{
			{LaneConfig{}, "Name: must not be empty"},
			{LaneConfig{Name: "a"}, `Name: duplicate lane "a"`},
			{LaneConfig{Name: "b", MaxWorkers: 3}, "MaxWorkers: must not exceed the pool's 2 workers, got 3"},
			{LaneConfig{Name: "b", MaxWorkers: -1}, "MaxWorkers: must not be negative"},
			{LaneConfig{Name: "b", Priority: 2}, "Priority: unknown priority 2"},
			{LaneConfig{Name: "b", QueueTimeout: -time.Second}, "QueueTimeout: must not be negative"},
		} {
			if _, err := p.Lane(tt.cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Lane(%+v) = %v, want %q", tt.cfg, err, tt.want)
			}
		}

		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })
		if _, err := p.Lane(LaneConfig{Name: "late"}); err == nil {
			t.Error("Lane after Start succeeded")
		}
		lanes := p.Lanes()
		if len(lanes) != 2 || lanes[0].Name != "" || lanes[1].Name != "a" || lanes[1].MaxWorkers != 2 ||
			lanes[1].Priority != PriorityLow || lanes[1].QueueTimeout != time.Second {
			t.Errorf("Lanes() = %+v", lanes)
		}
	})
}

func TestPriorityString(t *testing.T) {
	for p, want := range map[Priority]string{PriorityLow: "low", PriorityNormal: "normal", PriorityHigh: "high",
		5: "Priority(5)"} {
		if got := p.String(); got != want {
			t.Errorf("Priority(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

// A job may reach a parked worker just when an idle_ttl replaces it: the job
// still runs, on that worker or another.
func TestLaneHandOverRace(t *testing.T) {
	for i := range 20 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var fake workertest.Fake
				cfg := testConfig(1)
				cfg.IdleTTL = 10 * time.Minute
				p := startTestPool(t, cfg, &fake)
				time.Sleep(10 * time.Minute) // the job comes as the timer fires
				mustExec(t, p, "hello")
				if n := p.Waiting(); n != 0 {
					t.Errorf("Waiting() = %d, want 0", n)
				}
			})
		})
	}
}

// The dispatcher's hand-over, step by step, on a pool whose slots don't run.
func TestDispatcher(t *testing.T) {
	newJob := func(l *Lane, timeout time.Duration) *job {
		j := &job{lane: l, result: make(chan jobResult, 1), gone: make(chan struct{})}
		j.queued = time.Now()
		j.deadline = j.queued.Add(timeout)
		return j
	}
	setup := func(t *testing.T, cfg Config) (*Pool, *recorder) {
		t.Helper()
		rec := newRecorder()
		cfg.Observer = rec
		p := newPool(cfg, discard, nil)
		p.run, p.stopRun = context.WithCancel(t.Context())
		t.Cleanup(p.stopRun)
		return p, rec
	}
	enqueue := func(p *Pool, j *job) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.enqueueLocked(j)
		p.dispatchLocked()
	}
	// result returns what the dispatcher answered the job's caller, which
	// happens before unpark returns.
	result := func(t *testing.T, j *job) error {
		t.Helper()
		select {
		case r := <-j.result:
			return r.err
		default:
			t.Fatal("no result for the job")
			return nil
		}
	}
	held := func(s *slot) *job {
		select {
		case j := <-s.jobs:
			return j
		default:
			return nil
		}
	}

	t.Run("a job given back keeps its turn", func(t *testing.T) {
		p, _ := setup(t, testConfig(1))
		s := p.slots[0]
		p.park(s)
		first, second := newJob(p.lanes[0], time.Hour), newJob(p.lanes[0], time.Hour)
		enqueue(p, first)
		enqueue(p, second)
		if first.inQueue || !second.inQueue || p.lanes[0].busy != 1 || p.Waiting() != 1 {
			t.Fatalf("after the hand-over: first queued %v, second queued %v, busy %d, waiting %d",
				first.inQueue, second.inQueue, p.lanes[0].busy, p.Waiting())
		}
		p.unpark(s) // the slot was replaced instead of taking the job
		if !first.inQueue || p.lanes[0].queue.head != first || p.lanes[0].busy != 0 || p.Waiting() != 2 {
			t.Errorf("after unpark: the first job should be back at the head")
		}
		p.park(s)
		if j := held(s); j != first {
			t.Errorf("the slot took %p, want the first job %p", j, first)
		}
	})

	t.Run("a job given back after its deadline fails", func(t *testing.T) {
		p, rec := setup(t, testConfig(1))
		s := p.slots[0]
		p.park(s)
		j := newJob(p.lanes[0], -time.Second)
		enqueue(p, j)
		p.unpark(s)
		if err := result(t, j); !errors.Is(err, ErrNoWorker) || j.inQueue || p.Waiting() != 0 {
			t.Errorf("result %v, queued %v; want %v, out of the queue", err, j.inQueue, ErrNoWorker)
		}
		if rec.rejectedIn[" no_worker"] != 1 {
			t.Errorf("rejections: %v", rec.rejectedIn)
		}
	})

	t.Run("a job whose caller is gone is dropped", func(t *testing.T) {
		p, _ := setup(t, testConfig(1))
		s := p.slots[0]
		p.park(s)
		j := newJob(p.lanes[0], time.Hour)
		enqueue(p, j)
		close(j.gone)
		p.unpark(s)
		if j.inQueue || len(j.result) != 0 || p.lanes[0].busy != 0 {
			t.Errorf("queued %v, %d results, busy %d; want dropped", j.inQueue, len(j.result), p.lanes[0].busy)
		}
	})

	t.Run("no hand-over once the pool stops", func(t *testing.T) {
		p, _ := setup(t, testConfig(2))
		p.park(p.slots[0])
		j := newJob(p.lanes[0], time.Hour)
		enqueue(p, j)
		p.stopRun()
		p.unpark(p.slots[0])
		if err := result(t, j); !errors.Is(err, ErrStopped) {
			t.Errorf("a job given back after stop: %v, want %v", err, ErrStopped)
		}
		p.park(p.slots[1])
		late := newJob(p.lanes[0], time.Hour)
		enqueue(p, late)
		if held(p.slots[1]) != nil || !late.inQueue {
			t.Error("a job was handed out after stop")
		}
	})

	t.Run("a lane at its limit waits for its own jobs", func(t *testing.T) {
		p, _ := setup(t, testConfig(3))
		l := newLane(p, LaneConfig{Name: "one", MaxWorkers: 1})
		p.lanes = append(p.lanes, l)
		for _, s := range p.slots {
			p.park(s)
		}
		a, b := newJob(l, time.Hour), newJob(l, time.Hour)
		enqueue(p, a)
		enqueue(p, b)
		if a.inQueue || !b.inQueue || len(p.idle) != 2 {
			t.Fatalf("a queued %v, b queued %v, %d idle; want a handed out, b waiting, 2 idle",
				a.inQueue, b.inQueue, len(p.idle))
		}
		normal := newJob(p.lanes[0], time.Hour)
		enqueue(p, normal)
		if normal.inQueue {
			t.Error("a job of another lane waits behind a lane at its limit")
		}
		p.finish(a)
		if b.inQueue || l.busy != 1 {
			t.Errorf("after a finished: b queued %v, busy %d; want b handed out", b.inQueue, l.busy)
		}
	})
}

// overloadConfig is a pool of one worker with overload control: 500ms after
// 5s of an unbroken backlog, as by default.
func overloadConfig(rec *recorder) Config {
	cfg := testConfig(1)
	cfg.AllocateTimeout = time.Minute
	cfg.ExecTTL = 0
	cfg.OverloadQueueTimeout = 500 * time.Millisecond
	cfg.OverloadInterval = 5 * time.Second
	cfg.Observer = rec
	return cfg
}

// A lane with a backlog for OverloadInterval sheds: new jobs wait at most
// OverloadQueueTimeout, and the jobs at its head that waited longer are
// turned away when a worker comes free.
func TestOverload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		rec := newRecorder()
		p := startTestPool(t, overloadConfig(rec), &fake)

		var wg sync.WaitGroup
		wg.Go(func() { mustExec(t, p, "sleep 10s") }) // the worker is busy until 10s
		synctest.Wait()
		var early [3]error
		for i := range early {
			wg.Go(func() { _, early[i] = execBody(t, p, "early") }) // wait with the lane's minute
		}
		time.Sleep(6 * time.Second) // a backlog for 6s: overloaded

		start := time.Now()
		if _, err := execBody(t, p, "late"); !errors.Is(err, ErrOverloaded) {
			t.Errorf("Exec in an overloaded lane = %v, want %v", err, ErrOverloaded)
		}
		if elapsed := time.Since(start); elapsed != 500*time.Millisecond {
			t.Errorf("turned away after %s, want 500ms", elapsed)
		}

		// At 10s the worker is free: the early jobs have waited 10s, longer
		// than 500ms, and are shed instead of run.
		wg.Wait()
		for i, err := range early {
			if !errors.Is(err, ErrOverloaded) {
				t.Errorf("early job %d = %v, want %v", i, err, ErrOverloaded)
			}
		}
		if n := rec.rejectedIn[" overload"]; n != 4 {
			t.Errorf("%d overload rejections, want 4", n)
		}

		// A worker took the last job (nothing waits): no longer overloaded.
		var wg2 sync.WaitGroup
		wg2.Go(func() { mustExec(t, p, "sleep 2s") })
		synctest.Wait()
		mustExec(t, p, "waits 2s")
		wg2.Wait()
	})
}

// Only a worker that takes the last waiting job ends a backlog: jobs that
// time out or leave don't, so a lane can't escape overload control by
// turning its own queue away.
func TestOverloadBacklog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		rec := newRecorder()
		cfg := overloadConfig(rec)
		p := newTestPool(t, cfg, &fake)
		quick := mustLane(t, p, LaneConfig{Name: "quick", QueueTimeout: time.Second})
		capped := mustLane(t, p, LaneConfig{Name: "capped", MaxWorkers: 1})
		if err := p.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()) })

		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Go(func() { mustExec(t, p, "sleep 1h") })
		synctest.Wait()

		// A caller that leaves empties the queue, but the backlog goes on.
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		if _, err := p.Exec(ctx, Payload{Body: []byte("leaves")}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Exec = %v", err)
		}
		time.Sleep(2 * time.Second) // 6s since the backlog began
		if _, err := execBody(t, p, "late"); !errors.Is(err, ErrOverloaded) {
			t.Errorf("Exec after a backlog of 6s with an empty queue = %v, want %v", err, ErrOverloaded)
		}

		// Lanes with a queue timeout or a worker limit of their own keep their
		// waits, however long their backlog.
		for _, tt := range []struct {
			lane *Lane
			want time.Duration
		}{{quick, time.Second}, {capped, time.Minute}} {
			var mine sync.WaitGroup
			for range 2 {
				mine.Go(func() {
					start := time.Now()
					_, err := tt.lane.Exec(t.Context(), Payload{Body: []byte("x")})
					if elapsed := time.Since(start); !errors.Is(err, ErrNoWorker) || elapsed != tt.want {
						t.Errorf("%s Exec = %v after %s, want %v after %s", tt.lane.cfg.Name, err, elapsed, ErrNoWorker,
							tt.want)
					}
				})
				time.Sleep(6 * time.Second) // the second comes after a backlog of 6s
			}
			mine.Wait()
		}
		if rec.rejectedIn["quick overload"] != 0 || rec.rejectedIn["capped overload"] != 0 {
			t.Errorf("rejections: %v", rec.rejectedIn)
		}
	})
}
