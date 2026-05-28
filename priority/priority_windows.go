//go:build windows

package priority

import "syscall"

// Windows priority-class constants from <winbase.h>.
const (
	idlePriorityClass        = 0x00000040
	belowNormalPriorityClass = 0x00004000
	normalPriorityClass      = 0x00000020
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procSetPriorityClass  = kernel32.NewProc("SetPriorityClass")
	procGetCurrentProcess = kernel32.NewProc("GetCurrentProcess")
)

// Set applies the requested priority class to the current process.
func Set(level Level) error {
	var class uintptr
	switch level {
	case Low:
		class = idlePriorityClass
	case Medium:
		class = belowNormalPriorityClass
	default:
		class = normalPriorityClass
	}
	h, _, _ := procGetCurrentProcess.Call()
	r, _, err := procSetPriorityClass.Call(h, class)
	if r == 0 {
		return err
	}
	return nil
}
