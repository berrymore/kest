package worker

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The pool with real processes (the fake worker as a helper process) under
// load, while workers get SIGKILL. Only jobs on a killed worker may fail.
func TestPoolSurvivesKilledWorkers(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real processes")
	}
	cfg := DefaultConfig()
	cfg.Command = []string{os.Args[0]}
	cfg.Env = []string{helperEnv + "=fake", helperGORACE}
	cfg.Workers = 3
	logs := &logBuffer{}
	p, err := New(cfg, logs.logger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(t.Context()) }()

	var (
		seenMu sync.Mutex
		seen   = map[int]bool{} // every worker pid, to check that none is left
	)
	record := func() {
		seenMu.Lock()
		defer seenMu.Unlock()
		for _, w := range p.Workers() {
			if w.Pid != 0 {
				seen[w.Pid] = true
			}
		}
	}
	record()

	var ok, failed atomic.Int64
	stop := make(chan struct{})
	var load sync.WaitGroup
	for g := range 8 {
		load.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				body := fmt.Sprintf("job %d-%d", g, i)
				resp, err := p.Exec(t.Context(), Payload{Context: []byte("ctx"), Body: []byte(body)})
				switch _, isProcessErr := errors.AsType[*ProcessError](err); {
				case err == nil && string(resp.Body) == body && string(resp.Context) == "ctx":
					ok.Add(1)
				case isProcessErr:
					failed.Add(1)
				default:
					t.Errorf("Exec %q = %q, %v", body, resp.Body, err)
					return
				}
			}
		})
	}

	kills := 0
	for range 20 {
		time.Sleep(20 * time.Millisecond)
		record()
		workers := p.Workers()
		if pid := workers[rand.IntN(len(workers))].Pid; pid != 0 {
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				kills++
			}
		}
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	load.Wait()

	if ok.Load() < 100 {
		t.Errorf("only %d jobs succeeded", ok.Load())
	}
	if failed.Load() > int64(kills) {
		t.Errorf("%d jobs failed for %d kills: only a job on a killed worker may fail", failed.Load(), kills)
	}
	t.Logf("%d jobs ok, %d failed, %d workers killed", ok.Load(), failed.Load(), kills)

	// The pool recovers all its workers.
	deadline := time.Now().Add(10 * time.Second)
	for p.Health() != Healthy {
		if time.Now().After(deadline) {
			t.Fatalf("the pool did not recover: %+v", p.Workers())
		}
		time.Sleep(10 * time.Millisecond)
	}
	record()

	if err := p.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for pid := range seen {
		waitGone(t, pid)
	}
}
