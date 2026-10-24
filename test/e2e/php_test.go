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
	"strings"
	"sync"
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

	k.stop(t)
	for _, want := range []string{"workers started", "kest is ready", `msg="request failed" cluster=hello method=GET path=/fail status=500`, "kest stopped"} {
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
