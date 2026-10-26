package kv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/registry"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

// newKV returns a started plugin with the storage "local" of maxSize bytes,
// stopped at the end of the test.
func newKV(t *testing.T, maxSize config.ByteSize) (*KV, *metrics.Registry) {
	t.Helper()
	reg := metrics.NewRegistry()
	cfg := &Config{Storages: []Storage{{Name: "local", MaxSize: maxSize}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	kv := New(cfg, registry.BuildContext{Name: "kv", Logger: slog.New(slog.DiscardHandler), Metrics: reg})
	if err := kv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kv.Stop(context.Background()) })
	return kv, reg
}

// call calls a method with a kv.v1.Request for the storage "local", and
// returns the items of the answer as "key=value@timeout" strings.
func call(t *testing.T, kv *KV, method string, items ...entry) ([]string, error) {
	t.Helper()
	body, err := kv.Call(t.Context(), plugin.Call{Method: method, Codec: plugin.CodecProto,
		Body: encodeRequest(&request{storage: "local", items: items})})
	if err != nil {
		return nil, err
	}
	var answer []entry
	err = fields(body, func(num uint64, typ int, v []byte) error {
		if num != 1 || typ != wireLen {
			return fmt.Errorf("field %d of type %d in a Response", num, typ)
		}
		e, err := decodeItem(v)
		answer = append(answer, e)
		return err
	})
	if err != nil {
		t.Fatalf("%s answered %x: %v", method, body, err)
	}
	var out []string
	for _, e := range answer {
		s := e.key
		if e.value != nil {
			s += "=" + string(e.value)
		}
		if e.timeout != "" {
			s += "@" + e.timeout
		}
		out = append(out, s)
	}
	return out, nil
}

func mustCall(t *testing.T, kv *KV, method string, items ...entry) []string {
	t.Helper()
	out, err := call(t, kv, method, items...)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func keys(names ...string) []entry {
	out := make([]entry, len(names))
	for i, n := range names {
		out[i] = entry{key: n}
	}
	return out
}

func set(key, value, timeout string) entry {
	return entry{key: key, value: []byte(value), timeout: timeout}
}

// in returns the RFC 3339 time d from now, as PHP's roadrunner/kv sends it.
func in(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

func check(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func TestMethods(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv, _ := newKV(t, DefaultMaxSize)
		deadline := in(time.Hour)
		mustCall(t, kv, "Set", set("a", "1", ""), set("b", "2", deadline), set("c", "", ""))
		check(t, "Has", mustCall(t, kv, "Has", keys("c", "x", "a")...), "c", "a")
		check(t, "MGet", mustCall(t, kv, "MGet", keys("a", "x", "b")...), "a=1", "b=2")
		check(t, "TTL", mustCall(t, kv, "TTL", keys("a", "b", "x")...), "a", "b@"+deadline)
		check(t, "Has of no keys", mustCall(t, kv, "Has"))

		mustCall(t, kv, "Delete", keys("a", "x")...)
		check(t, "MGet after Delete", mustCall(t, kv, "MGet", keys("a", "b")...), "b=2")
		mustCall(t, kv, "Clear")
		check(t, "Has after Clear", mustCall(t, kv, "Has", keys("b", "c")...))
	})
}

// A timeout makes an item invisible at once, and the sweeper frees it
// within a second.
func TestTimeouts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv, reg := newKV(t, DefaultMaxSize)
		mustCall(t, kv, "Set", set("short", "1", in(2*time.Second)), set("long", "2", in(time.Hour)))
		time.Sleep(2*time.Second - time.Nanosecond)
		check(t, "before the timeout", mustCall(t, kv, "Has", keys("short", "long")...), "short", "long")
		time.Sleep(time.Nanosecond)
		check(t, "at the timeout", mustCall(t, kv, "MGet", keys("short", "long")...), "long=2")
		check(t, "TTL at the timeout", mustCall(t, kv, "TTL", keys("short")...))

		time.Sleep(sweepEvery)
		synctest.Wait()
		if got := metricstest.Value(t, reg, "kest_kv_items", "plugin", "kv", "storage", "local"); got != 1 {
			t.Errorf("kest_kv_items = %v after the sweep, want 1", got)
		}
	})
}

// PSR-16: a TTL of zero or less deletes the item. RoadRunner's memory
// driver keeps it forever instead.
func TestPastTimeoutDeletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv, _ := newKV(t, DefaultMaxSize)
		mustCall(t, kv, "Set", set("a", "1", ""))
		mustCall(t, kv, "Set", set("a", "2", in(0)), set("b", "3", in(-time.Hour)))
		check(t, "Has", mustCall(t, kv, "Has", keys("a", "b")...))
	})
}

func TestMExpire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv, _ := newKV(t, DefaultMaxSize)
		mustCall(t, kv, "Set", set("a", "1", ""), set("b", "2", in(time.Hour)), set("c", "3", ""))
		later, soon := in(2*time.Hour), in(time.Second)
		mustCall(t, kv, "MExpire", set("a", "", soon), set("b", "", later), set("c", "", in(-time.Second)),
			set("missing", "", later))
		check(t, "TTL", mustCall(t, kv, "TTL", keys("a", "b", "c", "missing")...), "a@"+soon, "b@"+later)
		check(t, "values", mustCall(t, kv, "MGet", keys("a", "b")...), "a=1", "b=2")
		time.Sleep(time.Second)
		check(t, "after a's timeout", mustCall(t, kv, "Has", keys("a", "b")...), "b")

		if _, err := call(t, kv, "MExpire", set("b", "", "")); err == nil || !strings.Contains(err.Error(), "needs a timeout") {
			t.Errorf("MExpire without a timeout: %v", err)
		}
	})
}

// Over max_size, expired items go first, then the least recently used.
func TestEviction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		item := func(key string) entry { return set(key, strings.Repeat("v", 1000-itemOverhead-len(key)), "") }
		kv, reg := newKV(t, 3000) // three items of 1000 bytes
		mustCall(t, kv, "Set", item("a"), item("b"))
		exp := item("e")
		exp.timeout = in(time.Second)
		mustCall(t, kv, "Set", exp)
		time.Sleep(time.Second) // e expires, not swept yet

		mustCall(t, kv, "MGet", keys("a")...) // b is now the least recently used
		mustCall(t, kv, "Set", item("c"))     // e makes room
		check(t, "after c", mustCall(t, kv, "Has", keys("a", "b", "c", "e")...), "a", "b", "c")
		mustCall(t, kv, "Set", item("d")) // b makes room
		check(t, "after d", mustCall(t, kv, "Has", keys("a", "b", "c", "d")...), "a", "c", "d")

		if got := metricstest.Value(t, reg, "kest_kv_evictions_total", "plugin", "kv", "storage", "local"); got != 1 {
			t.Errorf("kest_kv_evictions_total = %v, want 1", got)
		}
		if got := metricstest.Value(t, reg, "kest_kv_bytes", "plugin", "kv", "storage", "local"); got != 3000 {
			t.Errorf("kest_kv_bytes = %v, want 3000", got)
		}

		_, err := call(t, kv, "Set", item("small"), set("big", strings.Repeat("v", 3000), ""))
		if err == nil || !strings.Contains(err.Error(), `key "big": the item takes 3099 bytes, more than the storage's max_size of 3000`) {
			t.Errorf("an item over max_size: %v", err)
		}
		check(t, "nothing stored by a failed Set", mustCall(t, kv, "Has", keys("small")...))
	})
}

func TestCallErrors(t *testing.T) {
	kv, _ := newKV(t, DefaultMaxSize)
	tests := []struct {
		name string
		c    plugin.Call
		want string
	}{
		{"unknown method", plugin.Call{Method: "Get", Codec: plugin.CodecProto}, "unknown method"},
		{"json", plugin.Call{Method: "Has", Codec: plugin.CodecJSON}, "Has takes a protobuf kv.v1.Request, not json"},
		{"bad message", plugin.Call{Method: "Has", Codec: plugin.CodecProto, Body: []byte{0x0a, 0x05}},
			"invalid kv.v1.Request: truncated message"},
		{"no storage", plugin.Call{Method: "Has", Codec: plugin.CodecProto}, "no storage provided"},
		{"no such storage", plugin.Call{Method: "Has", Codec: plugin.CodecProto,
			Body: encodeRequest(&request{storage: "nope"})}, "no such storage: nope"},
		{"empty key", plugin.Call{Method: "MGet", Codec: plugin.CodecProto,
			Body: encodeRequest(&request{storage: "local", items: keys(" ")})}, "empty key"},
		{"bad timeout", plugin.Call{Method: "Set", Codec: plugin.CodecProto,
			Body: encodeRequest(&request{storage: "local", items: []entry{set("a", "1", "tomorrow")}})},
			`key "a": invalid timeout "tomorrow"`},
	}
	for _, tt := range tests {
		_, err := kv.Call(t.Context(), tt.c)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	if _, err := kv.Call(t.Context(), tests[0].c); !errors.Is(err, plugin.ErrUnknownMethod) {
		t.Errorf("unknown method: %v, want ErrUnknownMethod", err)
	}
}

func TestValidate(t *testing.T) {
	cfg := &Config{Storages: []Storage{{Name: "a"}, {Name: "a", MaxSize: 10}, {MaxSize: 1 << 20}}}
	err := cfg.Validate()
	var got []string
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		if fe, ok := errors.AsType[*config.FieldError](e); ok {
			got = append(got, fmt.Sprintf("%s: %s", fe.Field, fe.Msg))
		}
	}
	check(t, "errors", got,
		`storages[1].name: duplicate storage name "a"`,
		"storages[1].max_size: must be at least 1KB, got 10B",
		"storages[2].name: required")
	if cfg.Storages[0].MaxSize != DefaultMaxSize {
		t.Errorf("default max_size = %v", cfg.Storages[0].MaxSize)
	}
	if err := (&Config{}).Validate(); err == nil || !strings.Contains(err.Error(), "at least one storage") {
		t.Errorf("no storages: %v", err)
	}
}
