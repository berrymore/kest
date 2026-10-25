package worker

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"time"
)

// Config configures a Pool. Start from DefaultConfig: a zero duration or size
// means "off" where that is allowed, not "default".
type Config struct {
	Command []string // the worker command and its arguments
	Env     []string // the complete worker environment; RR_RELAY=pipes is added
	Dir     string   // working directory; empty means kest's

	Workers         int           // number of worker processes
	MaxJobs         uint64        // jobs per worker before it is replaced (+0…14 % jitter); 0 = unlimited
	MaxQueueSize    int           // callers that may wait for a free worker
	AllocateTimeout time.Duration // how long a caller waits for a free worker
	StartTimeout    time.Duration // spawn + handshake, which includes the application's boot
	DestroyTimeout  time.Duration // how long a worker may take to exit after the stop frame
	MaxFrameSize    int           // largest payload accepted from a worker, in bytes

	TTL             time.Duration // a worker's maximum lifetime; 0 = unlimited
	IdleTTL         time.Duration // a worker idle this long is replaced; 0 = never
	ExecTTL         time.Duration // a job's maximum duration; the worker is killed after it; 0 = unlimited
	MaxWorkerMemory int64         // RSS in bytes over which a worker is replaced; 0 = unlimited
	WatchTick       time.Duration // how often worker memory is sampled

	Observer Observer // told about waits, rejections and replacements, for metrics; nil = nobody
}

// Limits on Config values.
const (
	MaxWorkers = 1024
)

// DefaultConfig returns the defaults. RoadRunner leaves the queue, the job
// duration and the frame size unbounded; kest doesn't (plan D4).
func DefaultConfig() Config {
	return Config{
		Workers:         runtime.NumCPU(),
		MaxQueueSize:    1000,
		AllocateTimeout: 60 * time.Second,
		StartTimeout:    60 * time.Second,
		DestroyTimeout:  10 * time.Second,
		MaxFrameSize:    64 << 20,
		ExecTTL:         60 * time.Second,
		WatchTick:       5 * time.Second,
	}
}

// FieldError is an invalid Config field. Callers with a config file
// translate Field, the Go field name, to their own path.
type FieldError struct {
	Field string // e.g. "Workers"
	Msg   string
}

func (e *FieldError) Error() string {
	return e.Field + ": " + e.Msg
}

// Validate reports every invalid field as a *FieldError, joined.
func (c *Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}

	if len(c.Command) == 0 || c.Command[0] == "" {
		add("Command", "must not be empty")
	}
	if c.Workers < 1 || c.Workers > MaxWorkers {
		add("Workers", "must be between 1 and %d, got %d", MaxWorkers, c.Workers)
	}
	if c.MaxQueueSize < 1 {
		add("MaxQueueSize", "must be at least 1, got %d", c.MaxQueueSize)
	}
	if c.MaxFrameSize < 1 || c.MaxFrameSize > math.MaxUint32 {
		add("MaxFrameSize", "must be between 1 byte and 4 GiB, got %d", c.MaxFrameSize)
	}
	for _, d := range []struct {
		field string
		value time.Duration
	}{
		{"AllocateTimeout", c.AllocateTimeout},
		{"StartTimeout", c.StartTimeout},
		{"DestroyTimeout", c.DestroyTimeout},
	} {
		if d.value <= 0 {
			add(d.field, "must be positive, got %s", d.value)
		}
	}
	for _, d := range []struct {
		field string
		value time.Duration
	}{
		{"TTL", c.TTL},
		{"IdleTTL", c.IdleTTL},
		{"ExecTTL", c.ExecTTL},
	} {
		if d.value < 0 {
			add(d.field, "must not be negative, got %s", d.value)
		}
	}
	if c.MaxWorkerMemory < 0 {
		add("MaxWorkerMemory", "must not be negative, got %d", c.MaxWorkerMemory)
	}
	if c.MaxWorkerMemory > 0 && !memorySupported {
		add("MaxWorkerMemory", "is not supported on %s", runtime.GOOS)
	}
	if c.MaxWorkerMemory > 0 && c.WatchTick <= 0 {
		add("WatchTick", "must be positive when MaxWorkerMemory is set, got %s", c.WatchTick)
	}
	return errors.Join(errs...)
}
