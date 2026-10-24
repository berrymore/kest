package worker

import "syscall"

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		// Own process group: terminal signals don't reach the worker, and
		// killGroup can stop the processes it started.
		Setpgid: true,
		// The worker dies with kest, even if kest is killed with SIGKILL. The
		// signal fires when the OS thread that started the process exits; the
		// Go runtime only ends threads locked with LockOSThread, and nothing
		// here locks one.
		Pdeathsig: syscall.SIGKILL,
	}
}
