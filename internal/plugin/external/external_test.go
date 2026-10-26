package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/registry"
	sdk "github.com/berrymore/kest/pkg/plugin"
)

// The test binary is also the plugin: with testPluginEnv set, TestMain runs
// a plugin instead of the tests.
const testPluginEnv = "KEST_TEST_PLUGIN"

func TestMain(m *testing.M) {
	if mode := os.Getenv(testPluginEnv); mode != "" {
		os.Exit(runPlugin(mode))
	}
	leaktest.VerifyTestMain(m)
}

// testPlugin answers the tests' methods.
type testPlugin struct {
	setup    sdk.Setup
	canceled atomic.Int32
}

func (p *testPlugin) Call(ctx context.Context, c sdk.Call) ([]byte, error) {
	switch c.Method {
	case "Echo":
		return c.Body, nil
	case "Codec":
		return []byte(c.Codec.String()), nil
	case "Setup":
		return json.Marshal(map[string]any{"name": p.setup.Name, "config": p.setup.Config})
	case "Env":
		return []byte(os.Getenv(string(c.Body))), nil
	case "Fail":
		return nil, errors.New("boom")
	case "Panic":
		panic("oops")
	case "Sleep":
		d, err := time.ParseDuration(string(c.Body))
		if err != nil {
			return nil, err
		}
		select {
		case <-time.After(d):
			return []byte("slept " + d.String()), nil
		case <-ctx.Done():
			p.canceled.Add(1)
			return nil, ctx.Err()
		}
	case "Canceled":
		return fmt.Appendf(nil, "%d", p.canceled.Load()), nil
	case "Stdout":
		fmt.Println("hello from stdout")
		return nil, nil
	case "Big":
		return make([]byte, 2<<10), nil
	case "Exit":
		os.Exit(3)
	}
	return nil, sdk.ErrUnknownMethod
}

// stoppingPlugin writes a file when it stops, or hangs there.
type stoppingPlugin struct {
	testPlugin
	hang bool
}

func (p *stoppingPlugin) Stop(context.Context) error {
	if p.hang {
		time.Sleep(time.Hour) // a blocked select would be a deadlock the runtime kills
	}
	return os.WriteFile(os.Getenv("KEST_TEST_STOPPED"), []byte("stopped"), 0o600)
}

func runPlugin(mode string) int {
	var err error
	switch mode {
	case "echo":
		err = sdk.Serve(func(s sdk.Setup) (sdk.Plugin, error) { return &testPlugin{setup: s}, nil })
	case "stop", "hang-stop":
		err = sdk.Serve(func(s sdk.Setup) (sdk.Plugin, error) {
			return &stoppingPlugin{setup: s, hang: mode == "hang-stop"}, nil
		})
	case "refuse":
		err = sdk.Serve(func(sdk.Setup) (sdk.Plugin, error) { return nil, errors.New("bad config: x") })
	case "exit":
		fmt.Fprintln(os.Stderr, "cannot start: missing thing")
		return 2
	case "hang":
		time.Sleep(time.Hour)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// logBuffer collects JSON log records.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newExternal builds the plugin "ext" running this binary in mode.
func newExternal(t *testing.T, mode string, mutate func(*Config)) (*External, *metrics.Registry, *logBuffer) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Command = []string{exe}
	// A binary built with -race sleeps a second at exit unless told not to.
	cfg.Env = map[string]string{testPluginEnv: mode, "GORACE": "atexit_sleep_ms=0"}
	cfg.StartTimeout = config.Duration(5 * time.Second)
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	reg := metrics.NewRegistry()
	e, err := New(cfg, registry.BuildContext{Name: "ext", Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		ConfigDir: t.TempDir(), Metrics: reg}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return e, reg, logs
}

// startExternal starts it and stops it at the end of the test.
func startExternal(t *testing.T, mode string, mutate func(*Config)) (*External, *metrics.Registry, *logBuffer) {
	t.Helper()
	e, reg, logs := newExternal(t, mode, mutate)
	if err := e.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v\n%s", err, logs)
	}
	t.Cleanup(func() {
		select {
		case <-e.stop:
		default:
			_ = e.Stop(context.Background())
		}
	})
	return e, reg, logs
}

func call(t *testing.T, e *External, method, body string) (string, error) {
	t.Helper()
	out, err := e.Call(t.Context(), plugin.Call{Method: method, Codec: plugin.CodecRaw, Body: []byte(body)})
	return string(out), err
}

func TestCalls(t *testing.T) {
	e, _, _ := startExternal(t, "echo", func(c *Config) {
		c.PluginConfig = map[string]any{"a": 1, "list": []any{"x", map[string]any{"b": true}}}
		c.Env["FOO"] = "bar"
		c.MaxFrameSize = 1 << 10
	})
	tests := []struct {
		method, body, want, err string
	}{
		{method: "Echo", body: "hello", want: "hello"},
		{method: "Setup", want: `{"config":{"a":1,"list":["x",{"b":true}]},"name":"ext"}`},
		{method: "Env", body: "FOO", want: "bar"},
		{method: "Env", body: "KEST_PLUGIN_FD", want: "3"},
		{method: "Env", body: "KEST_PLUGIN_PROTOCOL", want: "1"},
		{method: "Fail", err: "boom"},
		{method: "Panic", err: "the plugin panicked in Panic: oops"},
		{method: "Big", err: "the answer is 2048 bytes"},
		{method: "Echo", body: "after a panic", want: "after a panic"},
	}
	for _, tt := range tests {
		got, err := call(t, e, tt.method, tt.body)
		switch {
		case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
			t.Errorf("%s: %q, %v; want error %q", tt.method, got, err, tt.err)
		case tt.err == "" && (err != nil || got != tt.want):
			t.Errorf("%s(%q) = %q, %v; want %q", tt.method, tt.body, got, err, tt.want)
		}
	}
	if _, err := call(t, e, "Nope", ""); !errors.Is(err, plugin.ErrUnknownMethod) {
		t.Errorf("unknown method: %v, want ErrUnknownMethod", err)
	}
	out, err := e.Call(t.Context(), plugin.Call{Method: "Codec", Codec: plugin.CodecProto})
	if string(out) != "protobuf" || err != nil {
		t.Errorf("Codec = %q, %v", out, err)
	}
}

// Calls share the plugin's connection and are answered as they finish.
func TestConcurrentCalls(t *testing.T) {
	e, _, _ := startExternal(t, "echo", nil)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			d := time.Duration(20-i) * 5 * time.Millisecond
			if got, err := call(t, e, "Sleep", d.String()); err != nil || got != "slept "+d.String() {
				t.Errorf("Sleep %v = %q, %v", d, got, err)
			}
		})
	}
	wg.Wait()
}

// A call whose context ends is canceled in the plugin too.
func TestCancel(t *testing.T) {
	e, _, _ := startExternal(t, "echo", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := e.Call(ctx, plugin.Call{Method: "Sleep", Codec: plugin.CodecRaw, Body: []byte("1h")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Sleep = %v, want the deadline", err)
	}
	waitFor(t, func() bool { got, _ := call(t, e, "Canceled", ""); return got == "1" })
}

func TestMaxConcurrentCalls(t *testing.T) {
	e, _, _ := startExternal(t, "echo", func(c *Config) { c.MaxConcurrentCalls = 1 })
	busy := make(chan error, 1)
	go func() { _, err := call(t, e, "Sleep", "500ms"); busy <- err }()
	waitFor(t, func() bool { return len(e.slots) == 1 })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := e.Call(ctx, plugin.Call{Method: "Echo", Codec: plugin.CodecRaw})
	if err == nil || !strings.Contains(err.Error(), "no place among max_concurrent_calls") {
		t.Errorf("a call over the limit: %v", err)
	}
	if err := <-busy; err != nil {
		t.Errorf("the first call: %v", err)
	}
}

// A plugin that dies fails its calls, is down for a moment, and is
// restarted.
func TestRestart(t *testing.T) {
	e, reg, logs := startExternal(t, "echo", nil)
	if _, err := call(t, e, "Exit", ""); err == nil || !strings.Contains(err.Error(), "plugin ext exited during the call") {
		t.Errorf("Exit: %v", err)
	}
	waitFor(t, func() bool {
		return metricstest.Value(t, reg, "kest_plugin_restarts_total", "plugin", "ext") == 1 && e.Serving()
	})
	if got, err := call(t, e, "Echo", "again"); err != nil || got != "again" {
		t.Errorf("after the restart: %q, %v", got, err)
	}
	if got := metricstest.Value(t, reg, "kest_plugin_restarts_total", "plugin", "ext"); got != 1 {
		t.Errorf("kest_plugin_restarts_total = %v", got)
	}
	if got := metricstest.Value(t, reg, "kest_plugin_up", "plugin", "ext"); got != 1 {
		t.Errorf("kest_plugin_up = %v", got)
	}
	if !strings.Contains(logs.String(), `"msg":"plugin exited; restarting it"`) {
		t.Errorf("no log of the exit:\n%s", logs)
	}
}

func TestStartErrors(t *testing.T) {
	tests := []struct {
		mode   string
		mutate func(*Config)
		want   string
	}{
		{"refuse", nil, "the plugin refused to start: bad config: x"},
		{"exit", nil, "the plugin exited during the handshake: exit status 2; stderr: cannot start: missing thing"},
		{"hang", func(c *Config) { c.StartTimeout = config.Duration(100 * time.Millisecond) },
			"no answer within start_timeout"},
		{"echo", func(c *Config) { c.Command = []string{"./no-such-plugin"} }, "start ./no-such-plugin"},
	}
	for _, tt := range tests {
		e, _, _ := newExternal(t, tt.mode, tt.mutate)
		err := e.Start(t.Context())
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Start = %v, want %q", tt.mode, err, tt.want)
		}
	}
}

// Stop lets the plugin stop itself; one that doesn't is killed after
// stop_timeout.
func TestStop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stopped")
	e, reg, _ := startExternal(t, "stop", func(c *Config) { c.Env["KEST_TEST_STOPPED"] = marker })
	if err := e.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "stopped" {
		t.Errorf("the plugin's Stop didn't run: %q, %v", b, err)
	}
	if e.Serving() || metricstest.Value(t, reg, "kest_plugin_up", "plugin", "ext") != 0 {
		t.Error("serving after Stop")
	}

	e, _, logs := startExternal(t, "hang-stop", func(c *Config) { c.StopTimeout = config.Duration(100 * time.Millisecond) })
	start := time.Now()
	if err := e.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Stop took %v", d)
	}
	if !strings.Contains(logs.String(), "plugin still running after stop_timeout; killing it") {
		t.Errorf("no log of the kill:\n%s", logs)
	}
}

// What the plugin prints goes to kest's log; it doesn't break the protocol.
func TestStdout(t *testing.T) {
	e, _, logs := startExternal(t, "echo", nil)
	if _, err := call(t, e, "Stdout", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := call(t, e, "Echo", "fine"); err != nil || got != "fine" {
		t.Errorf("after printing: %q, %v", got, err)
	}
	if err := e.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"plugin stdout"`) || !strings.Contains(logs.String(), `"line":"hello from stdout"`) {
		t.Errorf("stdout not logged:\n%s", logs)
	}
}

func TestValidate(t *testing.T) {
	cfg := defaultConfig()
	cfg.Env = map[string]string{"A=B": "x"}
	cfg.PluginConfig = map[string]any{"ok": map[any]any{1: "x"}}
	cfg.MaxConcurrentCalls = 0
	cfg.MaxFrameSize = 10
	cfg.StartTimeout, cfg.StopTimeout = 0, -1
	err := cfg.Validate()
	for _, want := range []string{
		"command: required",
		`env: invalid variable name "A=B"`,
		"plugin_config: key 1 is not a string",
		"max_concurrent_calls: must be positive, got 0",
		"max_frame_size: must be between 1KB and 1GB, got 10B",
		"start_timeout: must be positive",
		"stop_timeout: must be positive",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate = %v, want %q", err, want)
		}
	}

	cfg = defaultConfig()
	cfg.Env = map[string]string{"TOKEN": "secret"}
	cfg.PluginConfig = map[string]any{"password": "secret"}
	if r := cfg.Redacted().(*Config); r.Env["TOKEN"] != config.Redacted || r.PluginConfig != config.Redacted {
		t.Errorf("Redacted = %+v", r)
	}
}

func TestBackoff(t *testing.T) {
	for failures, want := range map[int]time.Duration{1: backoffBase, 3: 4 * backoffBase, 12: backoffMax, 40: backoffMax} {
		if got := backoff(failures); got > want || got < want-want/5 {
			t.Errorf("backoff(%d) = %v, want %v less up to 20 %%", failures, got, want)
		}
	}
}

// waitFor polls cond: the plugin is another process.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
