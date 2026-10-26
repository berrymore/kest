//go:build integration

package kv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/rpc"
)

// PHP's roadrunner/kv, unchanged, uses the plugin through kest's RPC server
// (testdata/php). `make test-integration` installs its composer packages.
func TestPHPCache(t *testing.T) {
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

	logger := slog.New(slog.DiscardHandler)
	cfg := &Config{Storages: []Storage{{Name: "local"}, {Name: "other"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	kv := New(cfg, registry.BuildContext{Name: "kv", Logger: logger})
	srv, err := rpc.New(rpc.Config{MaxConnections: 8, MaxFrameSize: 1 << 20, CallTimeout: 5 * time.Second},
		map[string]plugin.Plugin{"kv": kv}, logger, metrics.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface {
		Start(context.Context) error
		Stop(context.Context) error
	}{kv, srv} {
		if err := c.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}

	cmd := exec.CommandContext(t.Context(), php, "client.php")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "RR_RPC="+srv.Address())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("client.php: %v\n%s", err, stderr.String())
	}

	want := map[string]string{
		"set and get":      `{"check":"set and get","got":{"x":1,"y":[true,null]}}`,
		"missing":          `{"check":"missing","got":"default"}`,
		"has":              `{"check":"has","got":[true,false]}`,
		"multiple":         `{"check":"multiple","got":{"m1":1,"m2":"two","m3":"none"}}`,
		"delete":           `{"check":"delete","got":[false,false]}`,
		"ttl":              `{"check":"ttl","got":[true,null]}`,
		"ttl interval":     `{"check":"ttl interval","got":true}`,
		"zero ttl deletes": `{"check":"zero ttl deletes","got":false}`,
		"expires":          `{"check":"expires","got":"gone"}`,
		"large value":      `{"check":"large value","got":100000}`,
		"clear":            `{"check":"clear","got":[false,false]}`,
		"other storage":    `{"check":"other storage","got":["other","local"]}`,
		"no such storage": `{"check":"no such storage","error":"Spiral\\RoadRunner\\KeyValue\\Exception\\StorageException",` +
			`"message":"Storage \"nope\" has not been defined. Please make sure your RoadRunner \"kv\" configuration ` +
			`contains a storage key named \"nope\""}`,
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var line struct{ Check string }
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("client.php printed %q: %v", sc.Text(), err)
		}
		if got := sc.Text(); got != want[line.Check] {
			t.Errorf("%s:\n got %s\nwant %s", line.Check, got, want[line.Check])
		}
		delete(want, line.Check)
	}
	for check := range want {
		t.Errorf("client.php made no %q check:\n%s", check, out)
	}
}
