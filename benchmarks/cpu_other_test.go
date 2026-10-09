//go:build !unix

package benchmarks

import "time"

// cpuTime returns -1, because only Unix has getrusage, so the benchmarks report only the wall time.
func cpuTime() time.Duration {
	return -1
}
