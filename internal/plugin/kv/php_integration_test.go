//go:build integration

package kv

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/plugin/kv/kvtest"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/rpc"
)

// PHP's roadrunner/kv, unchanged, uses the plugin through kest's RPC server.
// internal/plugin/external runs the same checks against kv as an external
// plugin.
func TestPHPCache(t *testing.T) {
	kvtest.PHPDir(t)
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
	kvtest.RunPHPChecks(t, srv.Address())
}
