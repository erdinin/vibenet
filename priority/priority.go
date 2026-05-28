// Package priority sets the host OS scheduling priority of the current
// process so VibeNet's worker goroutines yield to user-facing apps. The
// concrete syscall lives in priority_windows.go and priority_unix.go;
// this file defines the cross-platform surface.
//
// The intent is "PC stays responsive": at Low priority, every other
// process — IDE, browser, AI assistant — gets CPU first, and VibeNet
// soaks up only the cycles nobody else wants.
package priority

import "fmt"

// Level is a coarse process-priority class.
type Level int

const (
	// Low schedules the process so it defers to anything else competing
	// for CPU. On Windows: IDLE_PRIORITY_CLASS. On Unix: nice 19.
	Low Level = iota
	// Medium runs slightly below interactive workloads. On Windows:
	// BELOW_NORMAL_PRIORITY_CLASS. On Unix: nice 10.
	Medium
	// High leaves the default OS scheduling untouched.
	High
)

// Parse maps a flag string (low/medium/high) to a Level. Empty string
// or unknown value returns Low and a non-nil error so the caller can
// decide whether to surface the warning or fall back silently.
func Parse(s string) (Level, error) {
	switch s {
	case "low", "":
		return Low, nil
	case "medium":
		return Medium, nil
	case "high":
		return High, nil
	default:
		return Low, fmt.Errorf("unknown intensity %q (want low|medium|high)", s)
	}
}

func (l Level) String() string {
	switch l {
	case Low:
		return "low"
	case Medium:
		return "medium"
	case High:
		return "high"
	default:
		return "unknown"
	}
}
