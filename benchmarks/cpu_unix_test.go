//go:build unix

package benchmarks

import (
	"syscall"
	"time"
)

// cpuTime returns the CPU time that the process has used, in all its threads.
func cpuTime() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return -1
	}

	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
