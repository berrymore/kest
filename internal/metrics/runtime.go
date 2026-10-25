package metrics

import (
	"os"
	"runtime"
	rtmetrics "runtime/metrics"
	"syscall"
	"time"
)

// processStart approximates the process's start time: the package is
// initialized right after the binary starts.
var processStart = time.Now()

// RegisterRuntime adds Go runtime and process metrics to r, under the names
// and with the meaning that prometheus/client_golang gives them (ADR-0007).
// process_resident_memory_bytes and process_virtual_memory_bytes exist on
// Linux only.
func RegisterRuntime(r *Registry) {
	r.NewCollector("go_info", "Information about the Go environment.", TypeGauge, []string{"version"},
		func(emit func(float64, ...string)) { emit(1, runtime.Version()) })
	for _, m := range []struct{ name, help, sample string }{
		{"go_goroutines", "Number of goroutines that currently exist.", "/sched/goroutines:goroutines"},
		{"go_threads", "Number of OS threads owned by the Go runtime.", "/sched/threads/total:threads"},
		{"go_sched_gomaxprocs_threads", "The current runtime.GOMAXPROCS setting.", "/sched/gomaxprocs:threads"},
		{"go_memstats_alloc_bytes", "Number of bytes allocated in heap and currently in use.",
			"/memory/classes/heap/objects:bytes"},
		{"go_memstats_sys_bytes", "Number of bytes obtained from the system.", "/memory/classes/total:bytes"},
		{"go_memstats_next_gc_bytes", "Heap size target for the end of the next GC cycle.", "/gc/heap/goal:bytes"},
		{"go_gc_gogc_percent", "Heap growth target set by GOGC.", "/gc/gogc:percent"},
		{"go_gc_gomemlimit_bytes", "Go runtime memory limit set by GOMEMLIMIT.", "/gc/gomemlimit:bytes"},
	} {
		r.NewCollector(m.name, m.help, TypeGauge, nil, runtimeSample(m.sample))
	}

	r.NewCollector("process_cpu_seconds_total", "Total user and system CPU time spent in seconds.", TypeCounter, nil,
		func(emit func(float64, ...string)) {
			var ru syscall.Rusage
			if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
				emit(time.Duration(ru.Utime.Nano() + ru.Stime.Nano()).Seconds())
			}
		})
	r.NewCollector("process_open_fds", "Number of open file descriptors.", TypeGauge, nil,
		func(emit func(float64, ...string)) {
			if entries, err := os.ReadDir(fdDir); err == nil {
				emit(float64(len(entries)))
			}
		})
	r.NewCollector("process_max_fds", "Maximum number of open file descriptors.", TypeGauge, nil,
		func(emit func(float64, ...string)) {
			var rl syscall.Rlimit
			if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl) == nil {
				emit(float64(rl.Cur))
			}
		})
	r.NewCollector("process_start_time_seconds", "Start time of the process since unix epoch in seconds.",
		TypeGauge, nil, func(emit func(float64, ...string)) {
			emit(float64(processStart.UnixNano()) / 1e9)
		})
	registerMemory(r)
}

// runtimeSample reads one uint64 metric of runtime/metrics.
func runtimeSample(name string) CollectFunc {
	return func(emit func(float64, ...string)) {
		s := []rtmetrics.Sample{{Name: name}}
		rtmetrics.Read(s)
		if s[0].Value.Kind() == rtmetrics.KindUint64 {
			emit(float64(s[0].Value.Uint64()))
		}
	}
}
