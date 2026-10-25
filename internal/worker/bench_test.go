package worker

import (
	"context"
	"testing"

	"github.com/berrymore/kest/internal/worker/workertest"
)

// BenchmarkPoolExec measures the pool's own cost per job: in-process fake
// workers that echo, more callers than workers.
func BenchmarkPoolExec(b *testing.B) {
	var fake workertest.Fake
	cfg := testConfig(8)
	spawn := func(context.Context) (process, error) { return fake.Start(cfg.MaxFrameSize), nil }
	p := newPool(cfg, discard, spawn)
	if err := p.Start(b.Context()); err != nil {
		b.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()
	req := Payload{Context: []byte("ctx"), Body: []byte("hello")}

	b.SetParallelism(4) // 4 × GOMAXPROCS callers
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := p.Exec(context.Background(), req); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
