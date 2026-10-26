//go:build !linux

package proc

import "syscall"

// SysProcAttr returns the attributes of a child process kest supervises.
func SysProcAttr() *syscall.SysProcAttr {
	// Own process group: terminal signals don't reach the child, and
	// KillGroup can stop the processes it started. There is no Pdeathsig
	// here; if kest dies, the child sees EOF on stdin and exits.
	return &syscall.SysProcAttr{Setpgid: true}
}
