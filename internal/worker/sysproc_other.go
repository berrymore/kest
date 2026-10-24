//go:build !linux

package worker

import "syscall"

func sysProcAttr() *syscall.SysProcAttr {
	// Own process group: terminal signals don't reach the worker, and
	// killGroup can stop the processes it started. There is no Pdeathsig
	// here; if kest dies, the worker sees EOF on stdin and exits.
	return &syscall.SysProcAttr{Setpgid: true}
}
