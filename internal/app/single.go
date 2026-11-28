package app

import (
	"errors"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// A tab has at most one files pane and one AI pane. Opening either on a
// pane of a tab that has one already shows that one: on the same host it
// is focused; on another it takes the new host in its place, unless it is
// busy (transferring, or running a turn), when it is only focused.

var errPaneBusy = errors.New("pane busy")

// single reports whether a tab holds at most one pane of kind k.
func single(k layout.Kind) bool { return k == layout.Files || k == layout.AI }

// tabPane is the pane of kind k in tab t, nil for none.
func (a *App) tabPane(t *layout.Tab, k layout.Kind) *conn {
	if t == nil {
		return nil
	}
	for _, p := range t.Panes() {
		if p.Item.Kind == k {
			if c := a.conns[p.Item.ID]; c != nil {
				return c
			}
		}
	}
	return nil
}

// singleAt is the pane of kind k already in the tab at opens on, nil when
// at is a new tab or the tab has none.
func (a *App) singleAt(k layout.Kind, at openAt) *conn {
	if at.pane == 0 || !single(k) {
		return nil
	}
	return a.tabPane(a.ws.TabOf(at.pane), k)
}

// paneBusy reports whether c, a files or AI pane, must not be replaced:
// it has transfers going, or a turn running.
func paneBusy(c *conn) bool {
	switch {
	case c.fs != nil:
		return countTransfers(c.fs.transfers.List()).active > 0
	case c.ai != nil:
		return c.ai.running || c.ai.pending != nil
	}
	return false
}

// showPane focuses c's pane.
func (a *App) showPane(c *conn) {
	a.ws.Activate(c.id)
	a.focusReq = c.id
	a.page = pageWorkspace
}

// reuse is what opening a pane of ex's kind on hostID gives when ex is
// in that tab already: ex, focused, on the same host; else a new pane on
// hostID in ex's place, or errPaneBusy, with ex focused, when ex is busy.
func (a *App) reuse(ex *conn, hostID string, link int) (int, error) {
	if ex.host.ID == hostID {
		if ex.ai != nil && link != 0 {
			ex.ai.link = link
		}
		a.showPane(ex)
		return ex.id, nil
	}
	if paneBusy(ex) {
		a.showPane(ex)
		a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
			if ex.kind == layout.AI {
				return ui.Toast{Title: m.AiPaneBusy(), Type: "error"}
			}
			return ui.Toast{Title: m.FilesPaneBusy(), Type: "error"}
		})
		return ex.id, errPaneBusy
	}
	if ex.fs != nil && len(ex.fs.extUnsynced()) > 0 {
		a.showPane(ex)
		a.toastIn(ex, func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.ExternalPaneBusy(), Type: "error"} })
		return ex.id, errPaneBusy
	}
	if ex.kind == layout.AI {
		a.StopAI(ex.id)
		if err := a.saveChat(ex); err != nil {
			a.showPane(ex)
			return ex.id, err
		}
	}
	c, err := a.makeConn(hostID, ex.kind, link)
	if err != nil {
		return 0, err
	}
	a.ws.Replace(ex.id, c.item())
	a.finishCloseSession(ex)
	a.shown(c)
	return c.id, nil
}

// ToggleFiles closes the active tab's files pane, or opens one beside the
// session the tab works on when it has none, under one of the rail's
// once the tab shows enough of them, or, with the rail's panes as tabs,
// in the place of the one the tab shows.
func (a *App) ToggleFiles() {
	t := a.ws.ActiveTab()
	if c := a.tabPane(t, layout.Files); c != nil {
		a.CloseSession(c.id)
		return
	}
	// Beside the session, not a pane of the rail's that took the focus.
	if p := a.railAnchor(t); p != nil && remoteOnly(a.conns[p.Item.ID]) {
		t.Focus = p.ID
	}
	if remoteOnly(a.focused()) {
		old := a.railOld(t, layout.Files)
		var under *conn
		if a.filesEdge() == layout.Right {
			under = a.railUnder(t, layout.Files)
		}
		a.OpenFilesBeside()
		c := a.tabPane(t, layout.Files)
		a.settleRail(c, old, under)
	}
}

// ToggleAI closes the active tab's AI pane, or opens one beside the
// session the tab works on when it has none, under one of the rail's
// once the tab shows enough of them, or, with the rail's panes as tabs,
// in the place of the one the tab shows.
func (a *App) ToggleAI() {
	t := a.ws.ActiveTab()
	if c := a.tabPane(t, layout.AI); c != nil {
		a.CloseSession(c.id)
		return
	}
	// Beside the session, not a pane of the rail's that took the focus.
	if p := a.railAnchor(t); p != nil && canAI(a.conns[p.Item.ID]) {
		t.Focus = p.ID
	}
	if canAI(a.focused()) {
		old, under := a.railOld(t, layout.AI), a.railUnder(t, layout.AI)
		a.OpenAIBeside()
		c := a.tabPane(t, layout.AI)
		a.settleRail(c, old, under)
	}
}

// fits reports whether moving the item id onto pane n at edge leaves
// every tab with at most one files pane and one AI pane. At the center
// the two items swap tabs.
func (a *App) fits(id int, n *layout.Node, edge layout.Edge) bool {
	st, sp := a.ws.Locate(id)
	tt := a.ws.TabOf(n.ID)
	if st == nil || tt == nil || st == tt {
		return true
	}
	swap := edge == layout.Center
	if !fitsIn(tt, sp.Item.Kind, n, swap) {
		return false
	}
	return !swap || fitsIn(st, n.Item.Kind, sp, true)
}

// fitsIn reports whether tab t takes one more pane of kind k, leaving
// taken out of it when swap.
func fitsIn(t *layout.Tab, k layout.Kind, leaving *layout.Node, swap bool) bool {
	if !single(k) {
		return true
	}
	for _, p := range t.Panes() {
		if p.Item.Kind == k && !(swap && p == leaving) {
			return false
		}
	}
	return true
}
