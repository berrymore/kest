package phpworkers

import (
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/rrhttp"
	"github.com/berrymore/kest/internal/worker"
)

// parse loads a file whose only cluster has the given config block, indented
// by six spaces.
func parse(t *testing.T, block string) *config.Config {
	t.Helper()
	src := `version: v1
listeners:
  - name: main
    address: 127.0.0.1:0
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, route: {cluster: app}}]
clusters:
  - name: app
    type: php_workers
    config:
` + block
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func decode(t *testing.T, block string) *Config {
	t.Helper()
	c := DefaultConfig()
	if err := parse(t, block).Clusters[0].DecodeConfig(&c); err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return &c
}

func build(t *testing.T, block string) (cluster.Cluster, error) {
	t.Helper()
	cfg := parse(t, block)
	bc := registry.BuildContext{Name: "app", Logger: logging.Discard(), ConfigDir: "/srv/app"}
	return registry.Build(cluster.Types, &cfg.Clusters[0].Extension, bc)
}

func TestConfigDefaults(t *testing.T) {
	c := decode(t, "      command: [php, worker.php]\n")
	got := c.worker("/srv/app", []string{"PATH=/bin", "HOME=/root"})
	want := worker.DefaultConfig()
	want.Command = []string{"php", "worker.php"}
	want.Dir = "/srv/app"
	want.Env = []string{"PATH=/bin", "HOME=/root", "RR_MODE=http"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("worker config:\n got %+v\nwant %+v", got, want)
	}
	if got.Workers != runtime.NumCPU() {
		t.Errorf("workers = %d, want NumCPU", got.Workers)
	}
	opts := c.requestOptions("/srv/app")
	if opts.Codec != relay.CodecJSON || opts.RawBody || c.MaxRequestBody != 16<<20 || !reflect.DeepEqual(opts.Uploads, rrhttp.Uploads{}) {
		t.Errorf("request options %+v, max_request_body %v", opts, c.MaxRequestBody)
	}
}

func TestConfigFull(t *testing.T) {
	c := decode(t, `      command: [/usr/bin/php, -d, memory_limit=256M, public/worker.php]
      dir: app
      env: {APP_ENV: prod, DEBUG: false, WORKERS: 4}
      inherit_env: false
      codec: proto
      max_request_body: 100MB
      max_response_size: 8MB
      raw_body: true
      uploads:
        dir: tmp/uploads
        forbid: [.PHP, .exe]
        allow: [.jpg]
      pool:
        workers: 3
        max_jobs: 500
        max_queue_size: 20
        allocate_timeout: 2s
        start_timeout: 30s
        destroy_timeout: 5s
        overload: {queue_timeout: 200ms, interval: 2s}
      supervisor:
        ttl: 1h
        idle_ttl: 10m
        exec_ttl: 90s
        max_worker_memory: 256MB
        watch_tick: 1s
`)
	got := c.worker("/srv", []string{"SECRET=x"})
	want := worker.Config{
		Command:         []string{"/usr/bin/php", "-d", "memory_limit=256M", "public/worker.php"},
		Env:             []string{"APP_ENV=prod", "DEBUG=false", "WORKERS=4", "RR_MODE=http"},
		Dir:             "/srv/app",
		Workers:         3,
		MaxJobs:         500,
		MaxQueueSize:    20,
		AllocateTimeout: 2 * time.Second,
		StartTimeout:    30 * time.Second,
		DestroyTimeout:  5 * time.Second,
		MaxFrameSize:    8 << 20,

		OverloadQueueTimeout: 200 * time.Millisecond,
		OverloadInterval:     2 * time.Second,

		TTL:             time.Hour,
		IdleTTL:         10 * time.Minute,
		ExecTTL:         90 * time.Second,
		MaxWorkerMemory: 256 << 20,
		WatchTick:       time.Second,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("worker config:\n got %+v\nwant %+v", got, want)
	}
	opts := c.requestOptions("/srv")
	if opts.Codec != relay.CodecProto || !opts.RawBody {
		t.Errorf("request options %+v", opts)
	}
	wantUploads := rrhttp.Uploads{Dir: "/srv/tmp/uploads", Forbid: []string{".php", ".exe"}, Allow: []string{".jpg"}}
	if !reflect.DeepEqual(opts.Uploads, wantUploads) {
		t.Errorf("uploads %+v, want %+v", opts.Uploads, wantUploads)
	}
	if c.MaxRequestBody != 100<<20 {
		t.Errorf("max_request_body = %v", c.MaxRequestBody)
	}
}

func TestConfigErrors(t *testing.T) {
	_, err := build(t, `      command: []
      env: {"": x, "A=B": y}
      codec: msgpack
      max_request_body: 0
      max_response_size: 5GB
      uploads: {forbid: [php, ".tar.gz"], allow: ["."]}
      pool:
        workers: 0
        max_queue_size: 0
        allocate_timeout: 0s
        start_timeout: 0s
        destroy_timeout: -1s
        overload: {queue_timeout: 1s, interval: 0s}
      supervisor:
        ttl: -1s
        idle_ttl: -1s
        exec_ttl: -1s
        max_worker_memory: 1MB
        watch_tick: 0s
      retry_after: -1s
`)
	if err == nil {
		t.Fatal("Build accepted an invalid config")
	}
	want := []string{
		`kest.yaml:14:16: clusters[0].config.command: must not be empty`,
		`kest.yaml:15:12: clusters[0].config.env: invalid variable name ""`,
		`kest.yaml:15:12: clusters[0].config.env: invalid variable name "A=B"`,
		`kest.yaml:16:14: clusters[0].config.codec: unknown codec "msgpack"; use json or proto`,
		`kest.yaml:17:25: clusters[0].config.max_request_body: must be positive, got 0B`,
		`kest.yaml:18:26: clusters[0].config.max_response_size: must be between 1 byte and 4 GiB`,
		`kest.yaml:19:26: clusters[0].config.uploads.forbid[0]: invalid extension "php": want a dot and a name, like .php`,
		`kest.yaml:19:31: clusters[0].config.uploads.forbid[1]: invalid extension ".tar.gz"`,
		`kest.yaml:19:51: clusters[0].config.uploads.allow[0]: invalid extension "."`,
		`kest.yaml:21:18: clusters[0].config.pool.workers: must be between 1 and 1024, got 0`,
		`kest.yaml:22:25: clusters[0].config.pool.max_queue_size: must be at least 1, got 0`,
		`kest.yaml:23:27: clusters[0].config.pool.allocate_timeout: must be positive, got 0s`,
		`kest.yaml:24:24: clusters[0].config.pool.start_timeout: must be positive, got 0s`,
		`kest.yaml:25:26: clusters[0].config.pool.destroy_timeout: must be positive, got -1s`,
		`kest.yaml:26:49: clusters[0].config.pool.overload.interval: must be positive while overload control is on, got 0s`,
		`kest.yaml:28:14: clusters[0].config.supervisor.ttl: must not be negative, got -1s`,
		`kest.yaml:29:19: clusters[0].config.supervisor.idle_ttl: must not be negative, got -1s`,
		`kest.yaml:30:19: clusters[0].config.supervisor.exec_ttl: must not be negative, got -1s`,
		`kest.yaml:32:21: clusters[0].config.supervisor.watch_tick: must be positive when MaxWorkerMemory is set, got 0s`,
		`kest.yaml:33:20: clusters[0].config.retry_after: must not be negative, got -1s`,
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error =\n%v\nwant it to contain\n%s", err, w)
		}
	}
}

func TestConfigUnknownField(t *testing.T) {
	_, err := build(t, "      command: [php]\n      workers: 4\n")
	if err == nil || !strings.Contains(err.Error(), "kest.yaml:15:7: clusters[0].config.workers: unknown field") {
		t.Errorf("err = %v", err)
	}
}

func TestBuild(t *testing.T) {
	c, err := build(t, "      command: [php, worker.php]\n      pool: {workers: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*Cluster); !ok {
		t.Errorf("Build returned %T", c)
	}
}

func TestEnvironment(t *testing.T) {
	environ := []string{"PATH=/bin", "RR_MODE=jobs", "APP_ENV=dev"}
	tests := []struct {
		name    string
		inherit bool
		env     map[string]string
		want    []string
	}{
		{"inherit", true, nil, []string{"PATH=/bin", "RR_MODE=jobs", "APP_ENV=dev", "RR_MODE=http"}},
		{"override", true, map[string]string{"APP_ENV": "prod", "B": "2", "A": "1"},
			[]string{"PATH=/bin", "RR_MODE=jobs", "APP_ENV=dev", "A=1", "APP_ENV=prod", "B=2", "RR_MODE=http"}},
		{"explicit only", false, map[string]string{"A": "1"}, []string{"A=1", "RR_MODE=http"}},
		{"rr_mode always wins", false, map[string]string{"RR_MODE": "grpc"}, []string{"RR_MODE=grpc", "RR_MODE=http"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{InheritEnv: tt.inherit, Env: tt.env}
			got := c.environment(environ)
			if !slices.Equal(got, tt.want) {
				t.Errorf("environment = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	tests := []struct{ dir, path, want string }{
		{"/srv/app", "", "/srv/app"},
		{"/srv/app", "public", "/srv/app/public"},
		{"/srv/app", "../other", "/srv/other"},
		{"/srv/app", "/abs", "/abs"},
		{"", "rel", "rel"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := resolve(tt.dir, tt.path); got != tt.want {
			t.Errorf("resolve(%q, %q) = %q, want %q", tt.dir, tt.path, got, tt.want)
		}
	}
}

func TestDump(t *testing.T) {
	cfg := parse(t, "      command: [php, worker.php]\n")
	bc := registry.BuildContext{Name: "app", Logger: logging.Discard()}
	if _, err := registry.Build(cluster.Types, &cfg.Clusters[0].Extension, bc); err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"inherit_env: true", "codec: json", "max_request_body: 16MB", "max_response_size: 64MB",
		"allocate_timeout: 1m0s", "exec_ttl: 1m0s", "max_worker_memory: 0B"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("dump has no %q:\n%s", want, out)
		}
	}
}

func TestRedacted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Env = map[string]string{"DB_PASSWORD": "hunter2"}
	r, ok := cfg.Redacted().(*Config)
	if !ok || r.Env["DB_PASSWORD"] != config.Redacted || cfg.Env["DB_PASSWORD"] != "hunter2" {
		t.Errorf("Redacted = %+v, config %+v", r, cfg.Env)
	}
}

func TestConfigBodyBudget(t *testing.T) {
	_, err := build(t, `      command: [php]
      max_request_body: 32MB
      max_buffered_bodies: 16MB
`)
	want := `kest.yaml:16:28: clusters[0].config.max_buffered_bodies: must be at least max_request_body (32MB), got 16MB`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want %s", err, want)
	}
}
