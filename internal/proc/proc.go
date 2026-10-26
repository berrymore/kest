// Package proc has what kest's child processes share, PHP workers and
// external plugins: their own process group, death with kest where the OS
// allows it, and their output in the log line by line.
package proc

import (
	"errors"
	"os"
	"syscall"
)

// KillGroup sends SIGKILL to the process group that pid leads (SysProcAttr
// sets Setpgid), so that the processes it started die with it.
func KillGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
