//go:build !windows

package priority

import "syscall"

// Set applies the requested priority via setpriority(2).
//
// nice values: -20 (highest priority) to 19 (lowest). VibeNet wants to be
// the lowest-priority thing on the box at Low so the IDE / AI tool / etc.
// never feels the CPU competition.
func Set(level Level) error {
	var nice int
	switch level {
	case Low:
		nice = 19
	case Medium:
		nice = 10
	default:
		nice = 0
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, 0, nice)
}
