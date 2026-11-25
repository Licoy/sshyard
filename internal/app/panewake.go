package app

import (
	"sync/atomic"
	"time"
)

// A terminal's session asks for a frame whenever it prints or its state
// changes, and every frame builds the whole window. While the last frame
// of the main window drew the terminal, it gets one at once; while it did
// not (another tab, the settings, the lock screen), at most one every
// hiddenWake, so that output nobody sees, a build or a tail -f in a tab
// behind, does not rebuild the window frame after frame while another
// terminal is typed in. Its output shows in the main window alone, the
// only one it wakes.

const hiddenWake = 250 * time.Millisecond

// paneWake is how a terminal's session asks for frames: shown is set
// while the main window's last frame drew it, pending while a frame
// asked for later is yet to be.
type paneWake struct {
	shown, pending atomic.Bool
}

// ask asks for a frame with invalidate: at once while the terminal is
// shown, else once after hiddenWake, by after (time.AfterFunc), however
// many times it is asked meanwhile. Any goroutine may call it.
func (w *paneWake) ask(invalidate func(), after func(time.Duration, func())) {
	if w.shown.Load() {
		invalidate()
		return
	}
	if w.pending.CompareAndSwap(false, true) {
		after(hiddenWake, func() {
			w.pending.Store(false)
			invalidate()
		})
	}
}

// afterFunc is time.AfterFunc for paneWake.ask.
func afterFunc(d time.Duration, fn func()) { time.AfterFunc(d, fn) }

// markShown notes, at the end of the main window's frame, which terminals
// it drew (termElement sets their drawn to a.frame).
func (a *App) markShown() {
	for _, cn := range a.conns {
		if cn.tv != nil {
			cn.wake.shown.Store(cn.drawn == a.frame)
		}
	}
}

// invalidateMain asks for a frame of the main window alone.
func (a *App) invalidateMain() {
	a.mu.Lock()
	svc, live := a.svc, a.live
	a.mu.Unlock()
	if live {
		svc.Invalidate()
	}
}
