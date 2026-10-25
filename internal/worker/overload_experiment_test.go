package worker

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

// TestOverloadExperiment measures overload control (plan M5 D4) in virtual
// time: Poisson arrivals at a share of a pool's capacity, exponential service
// times, clients that give up after 5 s, with and without CoDel. Not a test:
// run it with KEST_EXPERIMENT=1 go test -run OverloadExperiment -v.
func TestOverloadExperiment(t *testing.T) {
	if os.Getenv("KEST_EXPERIMENT") == "" {
		t.Skip("an experiment: set KEST_EXPERIMENT=1")
	}
	const (
		workers  = 4
		patience = 5 * time.Second
		warmup   = 10 * time.Second
		measured = 50 * time.Second
	)
	service := 20 * time.Millisecond // mean service time; KEST_EXPERIMENT_SERVICE changes it
	if s := os.Getenv("KEST_EXPERIMENT_SERVICE"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		service = d
	}
	t.Logf("%d workers, mean service time %s, clients give up after %s", workers, service, patience)
	type setup struct {
		name             string
		queueTimeout     time.Duration
		target, interval time.Duration
	}
	setups := []setup{
		{name: "off (allocate_timeout 60s)", queueTimeout: time.Minute},
		{name: "queue_timeout 1s", queueTimeout: time.Second},
		{name: "codel 5ms/100ms", queueTimeout: time.Minute, target: 5 * time.Millisecond, interval: 100 * time.Millisecond},
		{name: "codel 20ms/200ms", queueTimeout: time.Minute, target: 20 * time.Millisecond, interval: 200 * time.Millisecond},
		{name: "codel 50ms/500ms", queueTimeout: time.Minute, target: 50 * time.Millisecond, interval: 500 * time.Millisecond},
		{name: "codel 100ms/1s", queueTimeout: time.Minute, target: 100 * time.Millisecond, interval: time.Second},
		{name: "codel 200ms/2s", queueTimeout: time.Minute, target: 200 * time.Millisecond, interval: 2 * time.Second},
		{name: "codel 500ms/5s", queueTimeout: time.Minute, target: 500 * time.Millisecond, interval: 5 * time.Second},
		{name: "codel 1s/10s", queueTimeout: time.Minute, target: time.Second, interval: 10 * time.Second},
	}
	t.Logf("| load | setup | served/s | served | p50 | p99 | 503 | 503 after | gave up |")
	t.Logf("|---|---|---|---|---|---|---|---|---|")
	for _, load := range []float64{0.8, 0.95, 1.05, 1.2, 2} {
		for _, s := range setups {
			synctest.Test(t, func(t *testing.T) {
				var fake workertest.Fake
				cfg := testConfig(workers)
				cfg.AllocateTimeout = s.queueTimeout
				cfg.OverloadQueueTimeout, cfg.OverloadInterval = s.target, s.interval
				p := startTestPool(t, cfg, &fake)
				rng := rand.New(rand.NewPCG(1, 2))
				rate := load * float64(workers) / service.Seconds()

				type outcome struct {
					err     error
					latency time.Duration
				}
				var (
					mu      sync.Mutex
					results []outcome
					wg      sync.WaitGroup
				)
				start := time.Now()
				for time.Since(start) < warmup+measured {
					time.Sleep(time.Duration(rng.ExpFloat64() / rate * float64(time.Second)))
					cost := time.Duration(rng.ExpFloat64() * float64(service))
					counted := time.Since(start) >= warmup
					wg.Go(func() {
						ctx, cancel := context.WithTimeout(context.Background(), patience)
						defer cancel()
						began := time.Now()
						_, err := p.Exec(ctx, Payload{Body: fmt.Appendf(nil, "sleep %s", cost)})
						if counted {
							mu.Lock()
							results = append(results, outcome{err, time.Since(began)})
							mu.Unlock()
						}
					})
				}
				wg.Wait()

				var ok, rejected, gaveUp []time.Duration
				for _, r := range results {
					switch {
					case r.err == nil:
						ok = append(ok, r.latency)
					case errors.Is(r.err, context.DeadlineExceeded):
						gaveUp = append(gaveUp, r.latency)
					default:
						rejected = append(rejected, r.latency)
					}
				}
				slices.Sort(ok)
				slices.Sort(rejected)
				q := func(d []time.Duration, f float64) time.Duration {
					if len(d) == 0 {
						return 0
					}
					return d[int(f*float64(len(d)-1))].Round(time.Millisecond)
				}
				share := func(d []time.Duration) string {
					return fmt.Sprintf("%.0f %%", 100*float64(len(d))/float64(len(results)))
				}
				t.Logf("| %.0f %% | %s | %.0f | %s | %s | %s | %s | %s | %s |", load*100, s.name,
					float64(len(ok))/measured.Seconds(), share(ok), q(ok, 0.5), q(ok, 0.99), share(rejected),
					q(rejected, 0.5), share(gaveUp))
			})
		}
	}
}
