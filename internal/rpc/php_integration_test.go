//go:build integration

package rpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// PHP's roadrunner/goridge, unchanged, calls the server (testdata/php).
// `make test-integration` installs its composer packages.
func TestPHPClient(t *testing.T) {
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

	for _, network := range []string{"private unix socket", "tcp"} {
		t.Run(network, func(t *testing.T) {
			cfg := testConfig()
			if network == "tcp" {
				cfg.Address = "127.0.0.1:0"
			}
			s, _ := startServer(t, cfg, newTestPlugin())
			address := s.Address()
			if network == "tcp" {
				address = "tcp://" + s.ln.Addr().String()
			}

			cmd := exec.CommandContext(t.Context(), php, "client.php")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "RR_RPC="+address)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("client.php: %v\n%s", err, stderr.String())
			}

			want := map[string]string{
				"json":   `{"call":"json","ok":{"a":1,"b":[true,null]}}`,
				"prefix": `{"call":"prefix","ok":"6c6f7765722d63617365206d6574686f64"}`, // "lower-case method"
				"raw":    `{"call":"raw","ok":"7261773a006279746573"}`,                  // "raw:\0bytes"
				"error": `{"call":"error","error":"Spiral\\Goridge\\RPC\\Exception\\ServiceException",` +
					`"message":"Error 'boom: x' on ` + address + `"}`,
				"unknown method": `{"call":"unknown method","error":"Spiral\\Goridge\\RPC\\Exception\\ServiceException",` +
					`"message":"Error 'rpc: can't find method test.Nope' on ` + address + `"}`,
				"unknown service": `{"call":"unknown service","error":"Spiral\\Goridge\\RPC\\Exception\\ServiceException",` +
					`"message":"Error 'rpc: can't find service nope.Echo' on ` + address + `"}`,
				"after errors": `{"call":"after errors","ok":"7374696c6c2068657265"}`, // "still here"
				"multi":        `{"call":"multi","ok":"6d756c7469"}`,                  // "multi"
			}
			sc := bufio.NewScanner(bytes.NewReader(out))
			for sc.Scan() {
				var line struct{ Call string }
				if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
					t.Fatalf("client.php printed %q: %v", sc.Text(), err)
				}
				if got := sc.Text(); got != want[line.Call] {
					t.Errorf("%s:\n got %s\nwant %s", line.Call, got, want[line.Call])
				}
				delete(want, line.Call)
			}
			for call := range want {
				t.Errorf("client.php made no %q call:\n%s", call, out)
			}
		})
	}
}
