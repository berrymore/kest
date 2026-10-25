package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker/workertest"
)

// fakeSampler reports the memory the fake workers claim ("memory n").
func fakeSampler(fake *workertest.Fake) memorySampler {
	return func(_ context.Context, pids []int) (map[int]int64, error) {
		out := map[int]int64{}
		for _, pid := range pids {
			if p := fake.Process(pid); p != nil {
				out[pid] = p.RSS()
			}
		}
		return out, nil
	}
}

func memoryPool(t *testing.T, fake *workertest.Fake, sample memorySampler) *Pool {
	t.Helper()
	cfg := testConfig(1)
	cfg.MaxWorkerMemory = 100 << 20
	cfg.WatchTick = 5 * time.Second
	p := newTestPool(t, cfg, fake)
	p.sample = sample
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	return p
}

func TestSupervisorReplacesIdleWorkerOverLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := memoryPool(t, &fake, fakeSampler(&fake))

		mustExec(t, p, "memory 50000000")
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if w := p.Workers()[0]; w.Memory != 50_000_000 || fake.Started() != 1 {
			t.Fatalf("under the limit: %+v, %d started", w, fake.Started())
		}

		mustExec(t, p, "memory 200000000")
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if fake.Started() != 2 {
			t.Fatalf("a worker over the limit was not replaced: %d started", fake.Started())
		}
		if err := fake.Process(10000).Err(); err != nil {
			t.Errorf("the replaced worker: %v, want a graceful stop", err)
		}
		if w := p.Workers()[0]; w.Pid != 10001 || w.Memory != 0 {
			t.Errorf("worker %+v, want the new process without a sample yet", w)
		}
	})
}

func TestSupervisorWaitsForTheJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.MaxWorkerMemory = 100 << 20
		cfg.WatchTick = 5 * time.Second
		cfg.ExecTTL = 0
		p := newTestPool(t, cfg, &fake)
		p.sample = fakeSampler(&fake)
		if err := p.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Stop(context.Background()) }()

		mustExec(t, p, "memory 200000000")
		var wg sync.WaitGroup
		wg.Go(func() { mustExec(t, p, "sleep 1m") }) // busy across several ticks
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if fake.Started() != 1 {
			t.Fatal("a busy worker was replaced before its job ended")
		}
		wg.Wait()
		synctest.Wait()
		if fake.Started() != 2 {
			t.Errorf("a worker over the limit was not replaced after its job: %d started", fake.Started())
		}
	})
}

func TestSupervisorSamplerError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		var calls atomic.Int64
		p := memoryPool(t, &fake, func(context.Context, []int) (map[int]int64, error) {
			calls.Add(1)
			return nil, errors.New("ps failed")
		})
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls := calls.Load(); calls != 6 || fake.Started() != 1 || p.Health() != Healthy {
			t.Errorf("%d samples, %d started, %s; want 6, 1, healthy", calls, fake.Started(), p.Health())
		}
	})
}

func TestSupervisorIgnoresReplacedProcess(t *testing.T) {
	s := &slot{id: 0, memory: make(chan struct{}, 1), info: Info{Pid: 10001, State: StateReady}}
	s.recordMemory(map[int]int64{10000: 1 << 30}, 100, discard)
	if w := s.snapshot(); w.Memory != 0 || len(s.memory) != 0 {
		t.Errorf("a sample of another pid was applied: %+v", w)
	}
	s.recordMemory(map[int]int64{10001: 1 << 30}, 100, discard)
	s.recordMemory(map[int]int64{10001: 1 << 30}, 100, discard) // a second signal doesn't block
	if w := s.snapshot(); w.Memory != 1<<30 || len(s.memory) != 1 {
		t.Errorf("sample not applied: %+v, %d signals", w, len(s.memory))
	}
}

// sampleRSS against a real process that grows its memory.
func TestSampleRSS(t *testing.T) {
	p := startHelper(t, helperConfig("fake"), discard)
	conn := p.Conn()
	if _, err := handshake(t.Context(), p, discard); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	before := sampleOne(t, p.Pid())
	const grow = 64 << 20
	if _, _, err := execute(p, Payload{Body: []byte("memory 67108864")}, 10*time.Second); err != nil {
		t.Fatalf("grow: %v", err)
	}
	after := sampleOne(t, p.Pid())
	if after-before < grow*9/10 {
		t.Errorf("RSS went from %d to %d, want it to grow by about %d", before, after, grow)
	}

	_ = conn.Send(&relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: stopRequest})
	_ = waitExit(t, p)
	rss, err := sampleRSS(t.Context(), []int{p.Pid()})
	if err != nil || len(rss) != 0 {
		t.Errorf("sample of an exited process = %v, %v; want empty, nil", rss, err)
	}
}

func sampleOne(t *testing.T, pid int) int64 {
	t.Helper()
	rss, err := sampleRSS(t.Context(), []int{pid})
	if err != nil {
		t.Fatalf("sampleRSS: %v", err)
	}
	n, ok := rss[pid]
	if !ok || n <= 0 {
		t.Fatalf("sampleRSS = %v, want an entry for %d", rss, pid)
	}
	return n
}
