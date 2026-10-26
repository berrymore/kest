//go:build e2e && integration

package e2e

import (
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The php-kv example: workers keep a counter and notes in the kv plugin,
// through RPC, and every worker sees the same data.
func TestServePHPKVExample(t *testing.T) {
	const dir = "../../examples/php-kv"
	requirePHP(t, dir)
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	k := serve(t, filepath.Join(dir, "kest.yaml"), adminAddr, "KEST_ADDRESS="+listenAddr, "KEST_ADMIN_ADDRESS="+adminAddr)
	base := "http://" + listenAddr

	visitRE := regexp.MustCompile(`^visits: (\d+) \(worker (\d+)\)\n$`)
	pids := map[string]bool{}
	for i := 1; i <= 20; i++ {
		code, body, err := get(t, base+"/")
		m := visitRE.FindStringSubmatch(body)
		if err != nil || code != http.StatusOK || m == nil || m[1] != strconv.Itoa(i) {
			t.Fatalf("GET / #%d = %d %q, %v; want visits: %d", i, code, body, err, i)
		}
		pids[m[2]] = true
	}
	if len(pids) < 2 {
		t.Logf("one worker answered every request (%v): the count is still kest's", pids)
	}

	put := func(path, body string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("/notes/a", "hello"); code != http.StatusNoContent {
		t.Errorf("PUT /notes/a = %d", code)
	}
	if code, body, err := get(t, base+"/notes/a"); err != nil || code != http.StatusOK || body != "hello" {
		t.Errorf("GET /notes/a = %d %q, %v", code, body, err)
	}
	if code, _, err := get(t, base+"/notes/b"); err != nil || code != http.StatusNotFound {
		t.Errorf("GET /notes/b = %d, %v; want 404", code, err)
	}

	_, metrics, err := get(t, "http://"+adminAddr+"/metrics")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`kest_kv_items{plugin="kv",storage="local"} 2`,
		`kest_rpc_calls_total{plugin="kv",result="ok"} 44`, // 20 × (MGet, Set), Set, MGet + Has, Has
		`kest_rpc_connections 2`,
	} {
		if !strings.Contains(metrics, line+"\n") {
			t.Errorf("/metrics has no %q", line)
		}
	}
	k.stop(t)
}
