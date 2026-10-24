//go:build !linux

package metrics

// fdDir lists the process's open file descriptors on macOS and the BSDs.
const fdDir = "/dev/fd"

// registerMemory adds nothing: without /proc, the current RSS needs cgo
// (task_info on macOS).
func registerMemory(*Registry) {}
