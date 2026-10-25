//go:build e2e && integration

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// requirePHP skips the test without php or without the composer packages of
// the PHP project in dir (`make php-deps` installs them).
func requirePHP(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php is not in PATH")
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Skipf("%s has no vendor/: run `make php-deps`", dir)
	}
}

func post(t *testing.T, rawURL string, form url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestServePHPHelloExample(t *testing.T) {
	const dir = "../../examples/php-hello"
	requirePHP(t, dir)
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	k := serve(t, filepath.Join(dir, "kest.yaml"), adminAddr, "KEST_ADDRESS="+listenAddr, "KEST_ADMIN_ADDRESS="+adminAddr)
	base := "http://" + listenAddr

	if code, body, err := get(t, base+"/?name=e2e"); err != nil || code != http.StatusOK || !strings.HasPrefix(body, "Hello, e2e! (PHP ") {
		t.Errorf("GET /?name=e2e = %d %q, %v", code, body, err)
	}
	if code, body, err := get(t, base+"/healthz"); err != nil || code != http.StatusOK || body != "ok\n" {
		t.Errorf("GET /healthz = %d %q, %v", code, body, err)
	}
	if code, _, err := get(t, base+"/fail"); err != nil || code != http.StatusInternalServerError {
		t.Errorf("GET /fail = %d, %v; want 500", code, err)
	}
	if code, body, err := get(t, base+"/robots.txt"); err != nil || code != http.StatusOK || !strings.HasPrefix(body, "User-agent: *") {
		t.Errorf("GET /robots.txt = %d %q, %v; want the file of public/", code, body, err)
	}

	// request_id: PHP and the client see the same ID; access_log logs it.
	resp, err := http.Get(base + "/info") //nolint:noctx // a plain test request
	if err != nil {
		t.Fatal(err)
	}
	var withID struct {
		RequestID string `json:"request_id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&withID)
	resp.Body.Close()
	if id := resp.Header.Get("X-Request-Id"); err != nil || len(id) != 32 || withID.RequestID != id {
		t.Errorf("X-Request-Id %q, PHP saw %q (%v); want the same 32 hex digits", id, withID.RequestID, err)
	}

	code, body := post(t, base+"/info", url.Values{"a": {"1"}, "b[]": {"2", "3"}})
	var info struct {
		WorkerPid  int            `json:"worker_pid"`
		Method     string         `json:"method"`
		URI        string         `json:"uri"`
		Client     string         `json:"client"`
		ParsedBody map[string]any `json:"parsed_body"`
	}
	if err := json.Unmarshal([]byte(body), &info); code != http.StatusOK || err != nil {
		t.Fatalf("POST /info = %d %q, %v", code, body, err)
	}
	if info.Method != http.MethodPost || info.URI != base+"/info" || info.Client != "127.0.0.1" ||
		fmt.Sprint(info.ParsedBody) != "map[a:1 b:[2 3]]" {
		t.Errorf("POST /info: %+v", info)
	}

	// Concurrent load: every request succeeds; remember the worker pids.
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		pids = map[int]bool{}
	)
	for range 20 {
		wg.Go(func() {
			for range 25 {
				code, body, err := get(t, base+"/info")
				var got struct {
					WorkerPid int `json:"worker_pid"`
				}
				if err != nil || code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
					t.Errorf("GET /info = %d %q, %v", code, body, err)
					return
				}
				mu.Lock()
				pids[got.WorkerPid] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	pids[info.WorkerPid] = true
	if len(pids) < 2 {
		t.Errorf("requests went to %d worker(s), want several", len(pids))
	}

	_, body, err = get(t, "http://"+adminAddr+"/clusters")
	var clusters struct {
		Clusters []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Serving bool   `json:"serving"`
			Status  struct {
				Health  string `json:"health"`
				Workers []struct {
					State string `json:"state"`
					Pid   int    `json:"pid"`
					Jobs  int    `json:"jobs"`
				} `json:"workers"`
			} `json:"status"`
		} `json:"clusters"`
	}
	if err != nil || json.Unmarshal([]byte(body), &clusters) != nil || len(clusters.Clusters) != 1 {
		t.Fatalf("GET /clusters: %v\n%s", err, body)
	}
	c := clusters.Clusters[0]
	jobs := 0
	for _, w := range c.Status.Workers {
		jobs += w.Jobs
		if w.State != "ready" || !pids[w.Pid] {
			t.Errorf("/clusters worker %+v: want ready, with the pid of a worker that served requests", w)
		}
	}
	if c.Name != "hello" || c.Type != "php_workers" || !c.Serving || c.Status.Health != "healthy" ||
		len(c.Status.Workers) != 4 || jobs != 504 {
		t.Errorf("/clusters = %+v, want 4 ready workers that ran 504 jobs", c)
	}

	_, dump, err := get(t, "http://"+adminAddr+"/config_dump")
	if err != nil || strings.Contains(dump, "APP_ENV: dev") || !strings.Contains(dump, "APP_ENV: '[redacted]'") ||
		!strings.Contains(dump, "max_queue_size: 1000") || !strings.Contains(dump, "type: request_id") {
		t.Errorf("config_dump (%v), want defaults and env values redacted:\n%s", err, dump)
	}

	_, metrics, err := get(t, "http://"+adminAddr+"/metrics")
	// /robots.txt and /healthz were answered by kest: they count without a cluster.
	for _, want := range []string{
		`kest_http_requests_total{listener="main",virtual_host="default",cluster="hello",code="200"} 503`,
		`kest_http_requests_total{listener="main",virtual_host="default",cluster="",code="200"} 2`,
		`kest_http_requests_total{listener="main",virtual_host="default",cluster="hello",code="500"} 1`,
		`kest_pool_workers{cluster="hello",state="ready"} 4`,
		`kest_pool_wait_seconds_count{cluster="hello"} 504`,
		`kest_pool_queue_length{cluster="hello"} 0`,
	} {
		if err != nil || !strings.Contains(metrics, want) {
			t.Errorf("metrics don't contain %s (%v):\n%s", want, err, metrics)
		}
	}

	k.stop(t)
	for _, want := range []string{"workers started", "kest is ready", `msg="request failed" cluster=hello method=GET path=/fail status=500`,
		`msg=request listener=main component=access method=GET path=/info proto=HTTP/1.1 status=200`,
		"request_id=" + resp.Header.Get("X-Request-Id") + " virtual_host=default cluster=hello", "kest stopped"} {
		if !strings.Contains(k.logs.String(), want) {
			t.Errorf("logs do not contain %q:\n%s", want, k.logs.String())
		}
	}

	// No worker outlives kest.
	deadline := time.Now().Add(5 * time.Second)
	for pid := range pids {
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Errorf("worker %d still runs after kest exited", pid)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("worker %d: %v", pid, err)
		}
	}
}

// workerPids returns the pids of a cluster's workers from /clusters.
func workerPids(t *testing.T, adminAddr string) []int {
	t.Helper()
	_, body, err := get(t, "http://"+adminAddr+"/clusters")
	var clusters struct {
		Clusters []struct {
			Status struct {
				Workers []struct {
					Pid int `json:"pid"`
				} `json:"workers"`
			} `json:"status"`
		} `json:"clusters"`
	}
	if err != nil || json.Unmarshal([]byte(body), &clusters) != nil || len(clusters.Clusters) != 1 {
		t.Fatalf("GET /clusters: %v\n%s", err, body)
	}
	var pids []int
	for _, w := range clusters.Clusters[0].Status.Workers {
		pids = append(pids, w.Pid)
	}
	return pids
}

func allReplaced(before, after []int) bool {
	for i := range after {
		if after[i] == 0 || after[i] == before[i] {
			return false
		}
	}
	return len(after) == len(before)
}

// The M3 exit criterion: a rolling reset under load fails no request, through
// the admin API and through SIGHUP.
func TestRollingResetUnderLoad(t *testing.T) {
	const dir = "../../examples/php-hello"
	requirePHP(t, dir)
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	k := serve(t, filepath.Join(dir, "kest.yaml"), adminAddr, "KEST_ADDRESS="+listenAddr, "KEST_ADMIN_ADDRESS="+adminAddr)

	var (
		served, failed atomic.Int64
		stop           = make(chan struct{})
		wg             sync.WaitGroup
	)
	for range 16 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				code, _, err := get(t, "http://"+listenAddr+"/?name=load")
				served.Add(1)
				if err != nil || code != http.StatusOK {
					failed.Add(1)
				}
			}
		})
	}

	before := workerPids(t, adminAddr)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+adminAddr+"/clusters/hello/reset", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "reset done\n" {
		t.Errorf("POST /clusters/hello/reset = %d %q", resp.StatusCode, body)
	}
	afterAdmin := workerPids(t, adminAddr)
	if !allReplaced(before, afterAdmin) {
		t.Errorf("pids %v → %v: want every worker replaced", before, afterAdmin)
	}

	if err := k.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !allReplaced(afterAdmin, workerPids(t, adminAddr)) {
		if time.Now().After(deadline) {
			t.Fatalf("SIGHUP didn't replace the workers %v\n%s", afterAdmin, k.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // a little more load on the new workers
	close(stop)
	wg.Wait()

	if failed.Load() != 0 {
		t.Errorf("%d of %d requests failed during the resets", failed.Load(), served.Load())
	}
	t.Logf("%d requests served during two resets", served.Load())
	// Up to 8: under this load max_jobs (1000) replaces workers too, and a
	// slot whose worker is new already drops the reset's replacement.
	_, metrics, err := get(t, "http://"+adminAddr+"/metrics")
	var resets int
	for line := range strings.Lines(metrics) {
		if v, ok := strings.CutPrefix(line, `kest_pool_worker_restarts_total{cluster="hello",reason="reset"} `); ok {
			resets, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if err != nil || resets < 1 || resets > 8 {
		t.Errorf("reset restarts = %d (%v), want 1 to 8", resets, err)
	}
	k.stop(t)
	for _, want := range []string{`msg="workers reset" cluster=hello replaced=4`, `msg="resetting the workers" signal=hangup`} {
		if !strings.Contains(k.logs.String(), want) {
			t.Errorf("logs do not contain %q:\n%s", want, k.logs.String())
		}
	}
}
