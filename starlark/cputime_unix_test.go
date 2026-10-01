//go:build unix

package starlark

import "syscall"

// cpuNanos is the CPU time of the process (user and system), in nanoseconds:
// on a loaded machine it is the figure that says what a program costs.
func cpuNanos() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return ru.Utime.Nano() + ru.Stime.Nano()
}
