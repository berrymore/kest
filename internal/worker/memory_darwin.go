package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const memorySupported = true

// sampleRSS runs ps once for all pids: macOS has no /proc, and its libproc
// needs cgo. ps reports RSS in KiB. Processes that are gone are left out.
func sampleRSS(ctx context.Context, pids []int) (map[int]int64, error) {
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	cmd := exec.CommandContext(ctx, "/bin/ps", "-o", "pid=,rss=", "-p", strings.Join(list, ",")) //nolint:gosec // pids are integers
	stdout, err := cmd.Output()
	if _, exited := errors.AsType[*exec.ExitError](err); err != nil && !exited {
		return nil, fmt.Errorf("run ps: %w", err)
	}
	// ps exits with status 1 when none of the processes exist; the output
	// is what counts.
	out := make(map[int]int64, len(pids))
	for line := range bytes.Lines(stdout) {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(string(fields[0]))
		kib, err2 := strconv.ParseInt(string(fields[1]), 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("unexpected ps output %q", line)
		}
		out[pid] = kib << 10
	}
	return out, nil
}
