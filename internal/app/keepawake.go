package app

// While files transfer, in a files pane's queue or over a terminal
// (ZMODEM, trzsz), the computer is kept from sleeping on its own
// (settings.KeepAwake, Platform.Awake): each queue and each terminal is
// an owner of the app's awake.Keeper, busy while it has a transfer
// running or queued. The display may still sleep.

// newFiles is the state of a files pane, whose transfers keep the
// computer awake while they run.
func (a *App) newFiles() *filesState {
	fs := newFilesState(nil)
	ts := fs.transfers
	ts.OnChange = func() {
		a.awake.Set(ts, ts.Busy())
		a.invalidate()
	}
	return fs
}
