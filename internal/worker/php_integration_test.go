//go:build integration

package worker

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Integration tests against an unmodified roadrunner/worker PHP worker
// (testdata/php). `make test-integration` installs its composer packages.

func phpPool(t *testing.T, workers int, mutate func(*Config)) (*Pool, *logBuffer) {
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not in PATH")
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "php"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Skip("testdata/php has no vendor/: run `make test-integration`, which runs composer install")
	}

	cfg := DefaultConfig()
	cfg.Command = []string{php, "worker.php"}
	cfg.Dir = dir
	cfg.Workers = workers
	cfg.StartTimeout = 20 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}
	logs := &logBuffer{}
	p, err := New(cfg, logs.logger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(t.Context()) })
	return p, logs
}

func phpExec(t *testing.T, p *Pool, body string) string {
	t.Helper()
	resp, err := p.Exec(t.Context(), Payload{Context: []byte(`{"ctx":1}`), Body: []byte(body)})
	if err != nil {
		t.Fatalf("Exec %q: %v", body, err)
	}
	if string(resp.Context) != `{"ctx":1}` {
		t.Fatalf("Exec %q: header %q, want the request context", body, resp.Context)
	}
	return string(resp.Body)
}

func phpPid(t *testing.T, p *Pool) int {
	t.Helper()
	pid, err := strconv.Atoi(phpExec(t, p, "pid"))
	if err != nil {
		t.Fatalf("pid: %v", err)
	}
	return pid
}

func TestPHPWorkerEcho(t *testing.T) {
	p, logs := phpPool(t, 4, nil)

	for i := range 20 {
		body := fmt.Sprintf("hello %d", i)
		if got := phpExec(t, p, body); got != body {
			t.Errorf("echo %q = %q", body, got)
		}
	}
	pid := phpPid(t, p)
	found := false
	for _, w := range p.Workers() {
		found = found || w.Pid == pid
	}
	if !found {
		t.Errorf("the worker reported pid %d, not one of %+v", pid, p.Workers())
	}
	if got := len(logs.stderrLines()); got != 4 {
		t.Errorf("%d boot lines on stderr, want 4", got)
	}
	if r := logs.records("worker stderr"); len(r) > 0 && r[0]["worker_pid"] == nil {
		t.Errorf("stderr record without worker_pid: %v", r[0])
	}
}

func TestPHPWorkerSoftError(t *testing.T) {
	p, _ := phpPool(t, 1, nil)
	before := phpPid(t, p)
	_, err := p.Exec(t.Context(), Payload{Body: []byte("error")})
	if je, ok := errors.AsType[*JobError](err); !ok || je.Message != "php error" {
		t.Fatalf("Exec = %v, want a *JobError", err)
	}
	if after := phpPid(t, p); after != before {
		t.Errorf("pid %d → %d: a soft error must keep the worker", before, after)
	}
}

func TestPHPWorkerStopRequest(t *testing.T) {
	p, _ := phpPool(t, 1, nil)
	before := phpPid(t, p)
	// Every worker answers "stop" with $worker->stop(): the retry asks too.
	if _, err := p.Exec(t.Context(), Payload{Body: []byte("stop")}); !errors.Is(err, ErrStopRequested) {
		t.Fatalf("Exec = %v, want %v", err, ErrStopRequested)
	}
	if after := phpPid(t, p); after == before {
		t.Error("the worker that asked to be replaced still serves")
	}
}

func TestPHPWorkerMaxJobs(t *testing.T) {
	p, _ := phpPool(t, 1, func(c *Config) { c.MaxJobs = 5 }) // jitter adds 0 to 5
	pids := map[int]int{}
	for range 12 {
		pids[phpPid(t, p)]++
	}
	if len(pids) != 3 {
		t.Errorf("12 jobs with max_jobs 5 ran on %d processes, want 3: %v", len(pids), pids)
	}
}

func TestPHPWorkerExecTTL(t *testing.T) {
	p, _ := phpPool(t, 1, func(c *Config) { c.ExecTTL = 300 * time.Millisecond })
	_, err := p.Exec(t.Context(), Payload{Body: []byte("sleep 5000")})
	if !errors.Is(err, ErrExecTimeout) {
		t.Fatalf("Exec = %v, want %v", err, ErrExecTimeout)
	}
	if got := phpExec(t, p, "sleep 10"); got != "sleep 10" {
		t.Errorf("after the timeout: %q", got)
	}
}

func TestPHPWorkerMemoryLimit(t *testing.T) {
	p, logs := phpPool(t, 1, func(c *Config) {
		c.MaxWorkerMemory = 64 << 20
		c.WatchTick = 50 * time.Millisecond
	})
	before := phpPid(t, p)
	phpExec(t, p, "grow 100")

	deadline := time.Now().Add(10 * time.Second)
	for w := p.Workers()[0]; w.Pid == before || w.State != StateReady; w = p.Workers()[0] {
		if time.Now().After(deadline) {
			t.Fatalf("a worker with 100 MiB allocated was not replaced: %+v", w)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after := phpPid(t, p); after == before {
		t.Error("the same worker answered after the replacement")
	}
	if len(logs.records("worker over the memory limit")) == 0 {
		t.Error("the replacement was not logged")
	}
}

// The M1 exit criterion: N PHP workers keep serving under concurrent load
// while random workers get kill -9.
func TestPHPPoolSurvivesKill9(t *testing.T) {
	p, logs := phpPool(t, 4, nil)

	var seenMu sync.Mutex
	seen := map[int]bool{}
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

	const goroutines, duration = 32, 2 * time.Second
	stop := time.Now().Add(duration)
	var next, ok, failed atomic.Int64
	var load sync.WaitGroup
	for range goroutines {
		load.Go(func() {
			for time.Now().Before(stop) {
				n := next.Add(1)
				body := "job " + strconv.FormatInt(n, 10)
				resp, err := p.Exec(t.Context(), Payload{Context: []byte("c"), Body: []byte(body)})
				switch _, isProcessErr := errors.AsType[*ProcessError](err); {
				case err == nil && string(resp.Body) == body:
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
	done := make(chan struct{})
	go func() {
		load.Wait()
		close(done)
	}()

	kills := 0
	for running := true; running; {
		select {
		case <-done:
			running = false
		case <-time.After(50 * time.Millisecond):
			record()
			workers := p.Workers()
			if pid := workers[rand.IntN(len(workers))].Pid; pid != 0 && syscall.Kill(pid, syscall.SIGKILL) == nil {
				kills++
			}
		}
	}

	t.Logf("%d jobs ok, %d failed, %d workers killed in %s", ok.Load(), failed.Load(), kills, duration)
	if ok.Load()+failed.Load() != next.Load() {
		t.Fatalf("%d of %d jobs finished", ok.Load()+failed.Load(), next.Load())
	}
	if kills < 10 {
		t.Errorf("only %d workers killed", kills)
	}
	if failed.Load() > int64(kills) {
		t.Errorf("%d jobs failed for %d kills: only a job on a killed worker may fail", failed.Load(), kills)
	}

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
	if r := logs.records("worker did not exit in time, killed"); len(r) != 0 {
		t.Errorf("PHP workers did not stop gracefully: %v", r)
	}
}
