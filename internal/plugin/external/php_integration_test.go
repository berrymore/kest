//go:build integration

package external

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/plugin/kv/kvtest"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/rpc"
)

// The kv plugin built as an external one (testdata/kvplugin) passes the
// same roadrunner/kv checks as the built-in one.
func TestPHPCacheExternal(t *testing.T) {
	kvtest.PHPDir(t)
	bin := filepath.Join(t.TempDir(), "kvplugin")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./testdata/kvplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cfg := defaultConfig()
	cfg.Command = []string{bin}
	cfg.PluginConfig = map[string]any{"storages": []any{map[string]any{"name": "local"}, map[string]any{"name": "other", "max_size": "1MB"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	e, err := New(cfg, registry.BuildContext{Name: "kv", Logger: logger, ConfigDir: t.TempDir()}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rpc.New(rpc.Config{MaxConnections: 8, MaxFrameSize: 1 << 20, CallTimeout: 5 * time.Second},
		map[string]plugin.Plugin{"kv": e}, logger, metrics.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface {
		Start(context.Context) error
		Stop(context.Context) error
	}{e, srv} {
		if err := c.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	kvtest.RunPHPChecks(t, srv.Address())
}
