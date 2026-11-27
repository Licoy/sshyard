package app

import (
	"sync/atomic"
	"time"
)

// A terminal's session asks for a frame whenever it prints or its state
// changes, and every frame builds the whole window. While the last frame
// of its window drew the terminal, it gets one at once; while it did
// not (another tab, the settings, the lock screen), at most one every
// hiddenWake, so that output nobody sees, a build or a tail -f in a tab
// behind, does not rebuild the window frame after frame while another
// terminal is typed in. Its output wakes only the window it is in.

const hiddenWake = 250 * time.Millisecond

// paneWake is how a terminal's session asks for frames: shown is set
// while the last frame of its window drew it, pending while a frame
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

// markShown notes, at the end of the frame of window w, which of its
// terminals it drew (termElement sets their drawn to a.frame, which
// counts the frames of every window), and that their sessions wake w.
func (a *App) markShown(w *winState) {
	for _, it := range w.ws.Items() {
		cn := a.conns[it.ID]
		if cn == nil {
			continue
		}
		cn.wid.Store(int32(w.id))
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
