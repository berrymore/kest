package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
)

const memorySupported = true

// sampleRSS reads each process's resident set size from /proc/<pid>/statm.
// Processes that are gone are left out.
func sampleRSS(_ context.Context, pids []int) (map[int]int64, error) {
	page := int64(os.Getpagesize())
	out := make(map[int]int64, len(pids))
	for _, pid := range pids {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
		if err != nil {
			continue // the process exited
		}
		// statm: size resident shared text lib data dt, in pages.
		fields := bytes.Fields(data)
		if len(fields) < 2 {
			return nil, fmt.Errorf("unexpected /proc/%d/statm: %q", pid, data)
		}
		pages, err := strconv.ParseInt(string(fields[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected /proc/%d/statm: %q", pid, data)
		}
		out[pid] = pages * page
	}
	return out, nil
}
