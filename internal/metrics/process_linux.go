package metrics

import (
	"bytes"
	"os"
	"strconv"
)

const fdDir = "/proc/self/fd"

// registerMemory adds the process's memory from /proc/self/statm: total and
// resident pages.
func registerMemory(r *Registry) {
	read := func(field int) CollectFunc {
		return func(emit func(float64, ...string)) {
			b, err := os.ReadFile("/proc/self/statm")
			if err != nil {
				return
			}
			fields := bytes.Fields(b)
			if len(fields) <= field {
				return
			}
			pages, err := strconv.ParseUint(string(fields[field]), 10, 64)
			if err == nil {
				emit(float64(pages * uint64(os.Getpagesize())))
			}
		}
	}
	r.NewCollector("process_virtual_memory_bytes", "Virtual memory size in bytes.", TypeGauge, nil, read(0))
	r.NewCollector("process_resident_memory_bytes", "Resident memory size in bytes.", TypeGauge, nil, read(1))
}
