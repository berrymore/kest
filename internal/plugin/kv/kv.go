// Package kv is the kv plugin: named in-memory storages that PHP's
// roadrunner/kv (PSR-16 Cache) reaches through RPC, as RoadRunner's kv plugin
// with its memory driver (.ai/research/roadrunner.md § KV).
package kv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/registry"
)

// DefaultMaxSize bounds a storage without max_size.
const DefaultMaxSize = config.ByteSize(64 << 20)

// minMaxSize is the smallest max_size.
const minMaxSize = config.ByteSize(1 << 10)

// sweepEvery is how often expired items are removed. Reads don't see them
// before that; removing them frees their memory.
const sweepEvery = time.Second

func init() {
	plugin.Types.Register(registry.Factory[plugin.Plugin]{
		Type:      "kv",
		NewConfig: func() any { return &Config{} },
		Build: func(bc registry.BuildContext, cfg any) (plugin.Plugin, error) {
			return New(cfg.(*Config), bc), nil //nolint:forcetypeassert // NewConfig's type
		},
	})
}

// Config is the kv plugin's config. The JSON tags let it run as an external
// plugin too, which gets its config as JSON.
type Config struct {
	Storages []Storage `yaml:"storages" json:"storages"`
}

// Storage is one named storage; PHP selects it by name:
// (new Factory($rpc))->select('local').
type Storage struct {
	Name string `yaml:"name" json:"name"`
	// MaxSize bounds keys and values, plus about 100 bytes per item. Over
	// it, expired items go first, then the least recently used.
	MaxSize config.ByteSize `yaml:"max_size" json:"max_size"`
}

// Validate checks the config and fills in the defaults of storages, which
// NewConfig can't: they are list entries.
func (c *Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &config.FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	if len(c.Storages) == 0 {
		add("storages", "at least one storage is required")
	}
	seen := make(map[string]bool, len(c.Storages))
	for i := range c.Storages {
		s := &c.Storages[i]
		field := fmt.Sprintf("storages[%d]", i)
		switch {
		case s.Name == "":
			add(field+".name", "required")
		case seen[s.Name]:
			add(field+".name", "duplicate storage name %q", s.Name)
		}
		seen[s.Name] = true
		if s.MaxSize == 0 {
			s.MaxSize = DefaultMaxSize
		}
		if s.MaxSize < minMaxSize {
			add(field+".max_size", "must be at least %s, got %s", minMaxSize, s.MaxSize)
		}
	}
	return errors.Join(errs...)
}

// KV is the kv plugin.
type KV struct {
	storages map[string]*storage
	logger   *slog.Logger

	stop chan struct{}
	done sync.WaitGroup
}

// New builds the plugin; bc.Name labels its metrics.
func New(cfg *Config, bc registry.BuildContext) *KV {
	kv := &KV{storages: make(map[string]*storage, len(cfg.Storages)), logger: bc.Logger, stop: make(chan struct{})}
	for _, s := range cfg.Storages {
		kv.storages[s.Name] = newStorage(s.Name, int64(s.MaxSize))
	}
	kv.collect(bc.MetricsRegistry(), bc.Name)
	return kv
}

// Start starts sweeping expired items.
func (kv *KV) Start(context.Context) error {
	kv.done.Go(kv.sweep)
	return nil
}

// Stop stops sweeping. The data goes with the process.
func (kv *KV) Stop(context.Context) error {
	close(kv.stop)
	kv.done.Wait()
	return nil
}

func (kv *KV) sweep() {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			now := time.Now()
			for _, s := range kv.storages {
				s.sweep(now)
			}
		case <-kv.stop:
			return
		}
	}
}

// Call serves roadrunner/kv's methods: Has, Set, MGet, MExpire, TTL, Delete
// and Clear, each a kv.v1.Request answered with a kv.v1.Response.
func (kv *KV) Call(_ context.Context, c plugin.Call) ([]byte, error) {
	switch c.Method {
	case "Has", "Set", "MGet", "MExpire", "TTL", "Delete", "Clear": //nolint:usestdlibvars // kv's methods, not HTTP's
	default:
		return nil, plugin.ErrUnknownMethod
	}
	if c.Codec != plugin.CodecProto {
		return nil, fmt.Errorf("%s takes a protobuf kv.v1.Request, not %s", c.Method, c.Codec)
	}
	req, err := decodeRequest(c.Body)
	if err != nil {
		return nil, err
	}
	if req.storage == "" {
		return nil, errors.New("no storage provided")
	}
	s, ok := kv.storages[req.storage]
	if !ok {
		// roadrunner/kv turns "no such storage" into a StorageException.
		return nil, fmt.Errorf("no such storage: %s", req.storage)
	}

	now := time.Now()
	var out []entry
	switch c.Method {
	case "Has":
		var keys []string
		keys, err = s.has(req.keys(), now)
		for _, k := range keys {
			out = append(out, entry{key: k})
		}
	case "Set":
		err = s.set(req.items, now)
	case "MGet":
		out, err = s.mget(req.keys(), now)
	case "MExpire":
		err = s.mexpire(req.items, now)
	case "TTL":
		out, err = s.ttl(req.keys(), now)
	case "Delete": //nolint:usestdlibvars // kv's method, not HTTP's
		err = s.delete(req.keys())
	case "Clear":
		s.clear()
	}
	if err != nil {
		return nil, err
	}
	return encodeResponse(out), nil
}

func (kv *KV) collect(reg *metrics.Registry, name string) {
	each := func(fn func(s *storage, emit func(float64, ...string))) metrics.CollectFunc {
		return func(emit func(float64, ...string)) {
			for _, s := range kv.storages {
				fn(s, emit)
			}
		}
	}
	labels := []string{"plugin", "storage"}
	reg.NewCollector("kest_kv_items", "Items in a kv storage, expired ones not swept yet included.",
		metrics.TypeGauge, labels, each(func(s *storage, emit func(float64, ...string)) {
			items, _, _ := s.stats()
			emit(float64(items), name, s.name)
		}))
	reg.NewCollector("kest_kv_bytes", "What a kv storage holds, counted as against its max_size.",
		metrics.TypeGauge, labels, each(func(s *storage, emit func(float64, ...string)) {
			_, size, _ := s.stats()
			emit(float64(size), name, s.name)
		}))
	reg.NewCollector("kest_kv_max_bytes", "The max_size of a kv storage.",
		metrics.TypeGauge, labels, each(func(s *storage, emit func(float64, ...string)) {
			emit(float64(s.max), name, s.name)
		}))
	reg.NewCollector("kest_kv_evictions_total",
		"Items removed from a kv storage before they expired, to stay within max_size.",
		metrics.TypeCounter, labels, each(func(s *storage, emit func(float64, ...string)) {
			_, _, evicted := s.stats()
			emit(float64(evicted), name, s.name)
		}))
}
