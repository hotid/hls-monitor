//go:build unix

package selfstat

import (
	"syscall"
	"time"
)

// processCPU is the processor time the process has used so far.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
