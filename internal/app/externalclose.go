package app

import "github.com/Licoy/sshyard/internal/layout"

// askExternalClose puts the confirmation on a files pane with unsynced edits.
func (a *App) askExternalClose(items []layout.Item, close func()) bool {
	if a.lock.resetting {
		return false
	}
	for _, item := range items {
		cn := a.conns[item.ID]
		if cn == nil || cn.fs == nil || len(cn.fs.extUnsynced()) == 0 {
			continue
		}
		if w := a.winOf(cn.id); w != nil {
			defer a.enter(w)()
			a.showPane(cn)
			w.tabsOpen = false
			a.focusWindow(w)
		} else {
			focusEditor(cn)
		}
		cn.fs.extCloseAsk, cn.fs.extCloseDo = true, close
		a.invalidate()
		return true
	}
	return false
}

func focusEditor(cn *conn) {
	if cn.ed != nil && cn.ed.win != nil {
		go cn.ed.win.Focus()
	}
}

func (a *App) extConfirmClose(cn *conn) {
	if a.conns[cn.id] != cn || cn.fs == nil || !cn.fs.extCloseAsk {
		return
	}
	close := cn.fs.extCloseDo
	cn.fs.extCloseAsk, cn.fs.extCloseDo = false, nil
	if close != nil {
		a.post(close)
	}
}

func (a *App) closeItems(items []layout.Item) {
	close := func() {
		for _, it := range items {
			a.closeSession(it.ID)
		}
	}
	if !a.askExternalClose(items, close) {
		close()
	}
}

// AllowQuit checks the workspace before the desktop closes any windows.
// Confirmation retries the native quit once with the user's approval.
func (a *App) AllowQuit(retry func()) bool {
	if a.lock.resetting {
		return true
	}
	var items []layout.Item
	for _, w := range a.wins {
		items = append(items, w.ws.Items()...)
	}
	for _, cn := range a.conns {
		if cn.ed == nil {
			continue
		}
		if cn.ed.dirty.Load() {
			a.editorClosing(cn)
			focusEditor(cn)
			return false
		}
		items = append(items, cn.item())
	}
	if a.extQuitApproved {
		return true
	}
	return !a.askExternalClose(items, func() {
		a.extQuitApproved = true
		retry()
	})
}
