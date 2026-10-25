package phpworkers

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/rrhttp"
	"github.com/berrymore/kest/internal/worker"
)

// Codecs for the request context.
const (
	CodecJSON  = "json"
	CodecProto = "proto"
)

// DefaultMaxRequestBody bounds request bodies. RoadRunner allows 1000 MB.
const DefaultMaxRequestBody = 16 << 20

// DefaultMaxBufferedBodies bounds the request bodies a cluster holds at once.
const DefaultMaxBufferedBodies = 256 << 20

// Config is the config block of a php_workers cluster.
type Config struct {
	// Command runs one worker: the executable and its arguments.
	Command []string `yaml:"command"`
	// Dir is the workers' working directory, relative to the config file;
	// empty means the config file's directory.
	Dir string `yaml:"dir,omitempty"`
	// Env is added to the workers' environment.
	Env map[string]string `yaml:"env,omitempty"`
	// InheritEnv passes kest's own environment to the workers.
	InheritEnv bool `yaml:"inherit_env"`
	// Codec encodes the request context: "json" or "proto".
	Codec string `yaml:"codec"`
	// MaxRequestBody bounds request bodies; larger ones get 413.
	MaxRequestBody config.ByteSize `yaml:"max_request_body"`
	// MaxBufferedBodies bounds the bytes of request bodies that the cluster
	// holds at once; a request that doesn't fit gets 503.
	MaxBufferedBodies config.ByteSize `yaml:"max_buffered_bodies"`
	// MaxResponseSize bounds a worker's response (headers and body); a
	// worker that sends more is replaced and the request gets 502.
	MaxResponseSize config.ByteSize `yaml:"max_response_size"`
	// RawBody sends form and multipart bodies raw instead of parsed.
	RawBody bool `yaml:"raw_body"`

	Uploads    Uploads    `yaml:"uploads"`
	Pool       Pool       `yaml:"pool"`
	Supervisor Supervisor `yaml:"supervisor"`
	Reload     Reload     `yaml:"reload"`
}

// Reload replaces the workers when files change, for development (K-072).
type Reload struct {
	// Dirs are watched recursively, relative to the config file; none means
	// off. Directories whose names start with a dot are skipped.
	Dirs []string `yaml:"dirs,omitempty"`
	// Patterns match the names of the files to watch (path.Match).
	Patterns []string `yaml:"patterns"`
	// Interval is how often the directories are scanned.
	Interval config.Duration `yaml:"interval"`
	// MaxFiles bounds a scan; past it, watching stops with an error.
	MaxFiles int `yaml:"max_files"`
}

// Uploads controls the temp files of uploaded files.
type Uploads struct {
	// Dir holds the temp files, relative to the config file; empty means the
	// OS temp dir.
	Dir string `yaml:"dir,omitempty"`
	// Forbid lists extensions (".php") whose files are not stored; PHP sees
	// UPLOAD_ERR_EXTENSION.
	Forbid []string `yaml:"forbid,omitempty"`
	// Allow, if set, lists the only extensions that are stored.
	Allow []string `yaml:"allow,omitempty"`
}

// Pool sizes the pool and bounds waiting for workers.
type Pool struct {
	Workers         int             `yaml:"workers"`
	MaxJobs         uint64          `yaml:"max_jobs"`
	MaxQueueSize    int             `yaml:"max_queue_size"`
	AllocateTimeout config.Duration `yaml:"allocate_timeout"`
	StartTimeout    config.Duration `yaml:"start_timeout"`
	DestroyTimeout  config.Duration `yaml:"destroy_timeout"`
}

// Supervisor limits the lifetime and resources of workers.
type Supervisor struct {
	TTL             config.Duration `yaml:"ttl"`
	IdleTTL         config.Duration `yaml:"idle_ttl"`
	ExecTTL         config.Duration `yaml:"exec_ttl"`
	MaxWorkerMemory config.ByteSize `yaml:"max_worker_memory"`
	WatchTick       config.Duration `yaml:"watch_tick"`
}

// DefaultConfig returns the defaults; pool and supervisor values come from
// worker.DefaultConfig.
func DefaultConfig() Config {
	w := worker.DefaultConfig()
	return Config{
		InheritEnv:        true,
		Codec:             CodecJSON,
		MaxRequestBody:    DefaultMaxRequestBody,
		MaxBufferedBodies: DefaultMaxBufferedBodies,
		MaxResponseSize:   config.ByteSize(w.MaxFrameSize),
		Pool: Pool{
			Workers:         w.Workers,
			MaxJobs:         w.MaxJobs,
			MaxQueueSize:    w.MaxQueueSize,
			AllocateTimeout: config.Duration(w.AllocateTimeout),
			StartTimeout:    config.Duration(w.StartTimeout),
			DestroyTimeout:  config.Duration(w.DestroyTimeout),
		},
		Reload: Reload{Patterns: []string{"*.php"}, Interval: config.Duration(time.Second), MaxFiles: 20000},
		Supervisor: Supervisor{
			TTL:             config.Duration(w.TTL),
			IdleTTL:         config.Duration(w.IdleTTL),
			ExecTTL:         config.Duration(w.ExecTTL),
			MaxWorkerMemory: config.ByteSize(w.MaxWorkerMemory),
			WatchTick:       config.Duration(w.WatchTick),
		},
	}
}

// yamlFields maps worker.Config fields to their YAML paths.
var yamlFields = map[string]string{
	"Command":         "command",
	"Workers":         "pool.workers",
	"MaxQueueSize":    "pool.max_queue_size",
	"AllocateTimeout": "pool.allocate_timeout",
	"StartTimeout":    "pool.start_timeout",
	"DestroyTimeout":  "pool.destroy_timeout",
	"MaxFrameSize":    "max_response_size",
	"TTL":             "supervisor.ttl",
	"IdleTTL":         "supervisor.idle_ttl",
	"ExecTTL":         "supervisor.exec_ttl",
	"MaxWorkerMemory": "supervisor.max_worker_memory",
	"WatchTick":       "supervisor.watch_tick",
}

// Redacted implements config.Redactor: values in env may be secrets.
func (c *Config) Redacted() any {
	out := *c
	if len(c.Env) > 0 {
		out.Env = make(map[string]string, len(c.Env))
		for k := range c.Env {
			out.Env[k] = config.Redacted
		}
	}
	return &out
}

// Validate reports every invalid field as a *config.FieldError.
func (c *Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &config.FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}

	for i, arg := range c.Command {
		if strings.IndexByte(arg, 0) >= 0 {
			add(fmt.Sprintf("command[%d]", i), "must not contain NUL bytes")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		switch {
		case k == "" || strings.ContainsAny(k, "=\x00"):
			add("env", "invalid variable name %q", k)
		case strings.IndexByte(c.Env[k], 0) >= 0:
			add("env."+k, "must not contain NUL bytes")
		}
	}
	if _, err := codecFlag(c.Codec); err != nil {
		add("codec", "%v", err)
	}
	if c.MaxRequestBody < 1 {
		add("max_request_body", "must be positive, got %s", c.MaxRequestBody)
	}
	for i, p := range c.Reload.Patterns {
		if _, err := path.Match(p, ""); err != nil || p == "" {
			add(fmt.Sprintf("reload.patterns[%d]", i), "invalid pattern %q", p)
		}
	}
	if len(c.Reload.Dirs) > 0 && len(c.Reload.Patterns) == 0 {
		add("reload.patterns", "must not be empty when reload.dirs is set")
	}
	if c.Reload.Interval.Std() < 10*time.Millisecond {
		add("reload.interval", "must be at least 10ms, got %s", c.Reload.Interval)
	}
	if c.Reload.MaxFiles < 1 {
		add("reload.max_files", "must be positive, got %d", c.Reload.MaxFiles)
	}
	if c.MaxBufferedBodies < c.MaxRequestBody {
		add("max_buffered_bodies", "must be at least max_request_body (%s), got %s", c.MaxRequestBody, c.MaxBufferedBodies)
	}
	for field, list := range map[string][]string{"uploads.forbid": c.Uploads.Forbid, "uploads.allow": c.Uploads.Allow} {
		for i, ext := range list {
			if len(ext) < 2 || ext[0] != '.' || strings.ContainsAny(ext[1:], "./\\\x00") {
				add(fmt.Sprintf("%s[%d]", field, i), "invalid extension %q: want a dot and a name, like .php", ext)
			}
		}
	}

	wc := c.worker("", nil)
	if err := wc.Validate(); err != nil {
		for _, e := range unwrapAll(err) {
			if fe, ok := errors.AsType[*worker.FieldError](e); ok {
				add(yamlFields[fe.Field], "%s", fe.Msg)
			} else {
				errs = append(errs, e)
			}
		}
	}
	return errors.Join(errs...)
}

func unwrapAll(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

func codecFlag(codec string) (relay.Flags, error) {
	switch codec {
	case CodecJSON:
		return relay.CodecJSON, nil
	case CodecProto:
		return relay.CodecProto, nil
	default:
		return 0, fmt.Errorf("unknown codec %q; use %s or %s", codec, CodecJSON, CodecProto)
	}
}

// requestOptions returns how requests are encoded. Call it on a valid
// config; configDir resolves a relative uploads.dir.
func (c *Config) requestOptions(configDir string) rrhttp.Options {
	codec, _ := codecFlag(c.Codec)
	dir := ""
	if c.Uploads.Dir != "" {
		dir = resolve(configDir, c.Uploads.Dir)
	}
	return rrhttp.Options{
		Codec:   codec,
		RawBody: c.RawBody,
		Uploads: rrhttp.Uploads{Dir: dir, Forbid: lower(c.Uploads.Forbid), Allow: lower(c.Uploads.Allow)},
	}
}

func lower(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = strings.ToLower(s)
	}
	return out
}

// worker returns the pool configuration. configDir resolves a relative
// Dir; environ is kest's environment, inherited if InheritEnv is set.
func (c *Config) worker(configDir string, environ []string) worker.Config {
	return worker.Config{
		Command:         slices.Clone(c.Command),
		Env:             c.environment(environ),
		Dir:             resolve(configDir, c.Dir),
		Workers:         c.Pool.Workers,
		MaxJobs:         c.Pool.MaxJobs,
		MaxQueueSize:    c.Pool.MaxQueueSize,
		AllocateTimeout: c.Pool.AllocateTimeout.Std(),
		StartTimeout:    c.Pool.StartTimeout.Std(),
		DestroyTimeout:  c.Pool.DestroyTimeout.Std(),
		MaxFrameSize:    clampInt(int64(c.MaxResponseSize)),
		TTL:             c.Supervisor.TTL.Std(),
		IdleTTL:         c.Supervisor.IdleTTL.Std(),
		ExecTTL:         c.Supervisor.ExecTTL.Std(),
		MaxWorkerMemory: int64(c.Supervisor.MaxWorkerMemory),
		WatchTick:       c.Supervisor.WatchTick.Std(),
	}
}

// environment returns the workers' environment: environ if inherited, then
// Env, then RR_MODE=http (the pool adds RR_RELAY=pipes). For a repeated
// name, the last value wins.
func (c *Config) environment(environ []string) []string {
	var env []string
	if c.InheritEnv {
		env = slices.Clone(environ)
	}
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		env = append(env, k+"="+c.Env[k])
	}
	return append(env, "RR_MODE=http")
}

// resolve makes a relative name relative to dir.
func resolve(dir, name string) string {
	switch {
	case name == "":
		return dir
	case filepath.IsAbs(name) || dir == "":
		return name
	default:
		return filepath.Join(dir, name)
	}
}

// clampInt keeps an out-of-range size out of range for Validate.
func clampInt(n int64) int {
	if n > int64(^uint(0)>>1) {
		return -1
	}
	return int(n)
}
