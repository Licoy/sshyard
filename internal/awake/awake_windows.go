//go:build windows

package awake

import "golang.org/x/sys/windows"

// System is the hold of Windows: the execution state of the keeper's
// thread requires the system (ES_SYSTEM_REQUIRED), not the display, until
// it is set back. The state belongs to the thread that set it, which is
// why the keeper calls from one locked thread.
func System() Backend { return &winHold{} }

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var setThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

type winHold struct{ held bool }

func (h *winHold) Hold() error {
	if h.held {
		return nil
	}
	if err := setThreadExecutionState.Find(); err != nil {
		return err
	}
	if r, _, err := setThreadExecutionState.Call(esContinuous | esSystemRequired); r == 0 {
		return err
	}
	h.held = true
	return nil
}

func (h *winHold) Release() error {
	if !h.held {
		return nil
	}
	h.held = false
	if r, _, err := setThreadExecutionState.Call(esContinuous); r == 0 {
		return err
	}
	return nil
}
