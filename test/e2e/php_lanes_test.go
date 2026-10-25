//go:build e2e && integration

package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A slow route limited to 2 of a pool's 4 PHP workers and flooded doesn't
// slow down the pool's other routes (plan M5, exit criterion 1). Without the
// lane, the same flood takes every worker and the fast route waits behind it.
func TestSlowRouteLane(t *testing.T) {
	worker, err := filepath.Abs("../../internal/cluster/phpworkers/testdata/php")
	if err != nil {
		t.Fatal(err)
	}
	requirePHP(t, worker)

	start := func(t *testing.T, lane string) (addr, adminAddr string, k *running) {
		t.Helper()
		addr, adminAddr = freeAddr(t), freeAddr(t)
		configPath := filepath.Join(t.TempDir(), "kest.yaml")
		writeFile(t, configPath, `version: v1
log: {level: warn}
admin: {address: "`+adminAddr+`"}
listeners:
  - name: main
    address: "`+addr+`"
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - name: slow
              match: {prefix: /sleep/}
              route: {cluster: app`+lane+`}
            - name: fast
              match: {prefix: /}
              route: {cluster: app}
clusters:
  - name: app
    type: php_workers
    config:
      command: [php, worker.php]
      dir: "`+worker+`"
      pool: {workers: 4}
`)
		return addr, adminAddr, serve(t, configPath, adminAddr)
	}

	// flood keeps 8 clients on the slow route, and returns the p99 latency of
	// two clients on the fast route meanwhile.
	flood := func(t *testing.T, addr string, during func()) time.Duration {
		t.Helper()
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					_, _, _ = get(t, "http://"+addr+"/sleep/500")
				}
			})
		}
		time.Sleep(200 * time.Millisecond) // the flood fills the queue
		var (
			mu    sync.Mutex
			times []time.Duration
			fast  sync.WaitGroup
		)
		for range 2 {
			fast.Go(func() {
				for end := time.Now().Add(2 * time.Second); time.Now().Before(end); {
					began := time.Now()
					code, _, err := get(t, "http://"+addr+"/echo")
					if err != nil || code != http.StatusOK {
						t.Errorf("fast route: %d %v", code, err)
						return
					}
					mu.Lock()
					times = append(times, time.Since(began))
					mu.Unlock()
				}
			})
		}
		if during != nil {
			during()
		}
		fast.Wait()
		close(stop)
		wg.Wait()
		slices.Sort(times)
		if len(times) == 0 {
			t.Fatal("no fast requests")
		}
		return times[(len(times)*99+99)/100-1]
	}

	t.Run("with a lane", func(t *testing.T) {
		addr, adminAddr, k := start(t, ", max_workers: 2, priority: low")
		type lane struct {
			Route       string `json:"route"`
			MaxWorkers  int    `json:"max_workers"`
			QueueLength int    `json:"queue_length"`
			BusyWorkers int    `json:"busy_workers"`
		}
		var lanes []lane
		var metrics string
		p99 := flood(t, addr, func() {
			time.Sleep(500 * time.Millisecond)
			_, body, err := get(t, "http://"+adminAddr+"/clusters")
			var clusters struct {
				Clusters []struct {
					Status struct {
						Lanes []lane `json:"lanes"`
					} `json:"status"`
				} `json:"clusters"`
			}
			if err == nil {
				err = json.Unmarshal([]byte(body), &clusters)
			}
			if err != nil || len(clusters.Clusters) != 1 {
				t.Errorf("/clusters (%v): %s", err, body)
			} else {
				lanes = clusters.Clusters[0].Status.Lanes
			}
			_, metrics, _ = get(t, "http://"+adminAddr+"/metrics")
		})
		t.Logf("fast route p99 with the slow route flooded, in a lane of its own: %s", p99)
		if p99 > 250*time.Millisecond {
			t.Errorf("fast route p99 %s, want well under the slow route's 500ms", p99)
		}
		if l := lanes; len(l) != 2 || l[1].Route != "slow" || l[1].MaxWorkers != 2 || l[1].BusyWorkers != 2 ||
			l[1].QueueLength == 0 {
			t.Errorf("lanes in /clusters during the flood: %+v, want slow with 2 busy workers and a queue", l)
		}
		for _, want := range []string{
			`kest_pool_lane_workers{cluster="app",route="slow"} 2`,
			`kest_http_requests_total{listener="main",virtual_host="default",route="fast",cluster="app",code="200"}`,
			`kest_pool_wait_seconds_count{cluster="app",route="slow"}`,
			`kest_pool_wait_seconds_count{cluster="app",route=""}`,
		} {
			if !strings.Contains(metrics, want) {
				t.Errorf("metrics during the flood don't contain %s:\n%s", want, metrics)
			}
		}
		k.stop(t)
	})

	t.Run("without a lane", func(t *testing.T) {
		addr, _, k := start(t, "")
		p99 := flood(t, addr, nil)
		t.Logf("fast route p99 with the slow route flooded, sharing the pool: %s", p99)
		if p99 < 400*time.Millisecond {
			t.Errorf("fast route p99 %s; without a lane it should wait behind the slow requests", p99)
		}
		k.stop(t)
	})
}
