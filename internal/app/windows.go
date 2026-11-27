package app

import (
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The workspace may spread over several windows, each with tabs of its
// own, so that sessions can sit on different screens. All the windows
// share one App: the hosts, the connections, the secrets and the
// settings. The main window is the first, and the only one with the
// sidebar and the settings; the others show their tabs, the rail and the
// status bar. A tab or a pane moves between windows without its session
// noticing. The sheets of the app show in the window last focused.
//
// Every frame, and so everything it calls, works on the window it draws
// (App.winState). Between frames, the window last focused is the current
// one: what a tray action or a background result opens goes there, and
// code about a pane switches to the window holding it (enterOf).

// winState is what each workspace window keeps of its own.
type winState struct {
	// id numbers the window, 1 for the main one; key names its place and
	// size between launches (WindowSpec.StateKey).
	id  int
	key string
	ws  *layout.Workspace
	// page is what the window shows; only the main one shows the settings.
	page page
	// tabStart is the first tab the title bar shows when not all fit, the
	// others in its menu of more tabs; tabRoom the width they had
	tabStart int
	tabRoom  float32
	// tabWheeled is set once the wheel moves the tabs shown, which then
	// stay where it left them until another tab than tabKept is active;
	// tabWheel is what of a touchpad's scroll has yet to move them by one
	tabWheeled bool
	tabKept    int
	tabWheel   float32
	// tabsOpen shows the popover of the tabs that do not fit
	tabsOpen bool
	// menu is the drop-down or context menu open (menuButton,
	// contextMenu), ctx the context menu's place and entries
	menu dropMenu
	ctx  ctxMenu
	// field is the welcome page's dots.
	field welcomeField
	// finding shows the find bar of the focused terminal, query and match
	// what it looks for and the match shown.
	finding bool
	query   string
	match   int
	// focusReq is the session whose terminal takes the focus next frame
	focusReq int
	// paneBounds are where the panes were last frame, by session
	paneBounds map[int]ui.Rect
	// dropPreview is where a drag in progress would land, nil for none
	dropPreview *dropTarget
	// win is the window's buttons where the app draws them (see
	// WindowControls), maximized whether the window is
	win       *WindowControls
	maximized bool
	// native is another window once it is open; nil for the main one,
	// which main.go owns.
	native Window
	// hadTabs is set once the window showed a tab: another window closes
	// as its last tab goes.
	hadTabs bool
	// closeAsk asks what to do with the window's sessions as it closes.
	closeAsk bool
	// gone is set when the window closed as the app quits: its tabs stay
	// for the layout to keep, and come back should the quit be canceled.
	gone bool
	// held is set while the window has sessions (noteHeld), for the
	// system's thread to read as it closes.
	held atomic.Bool
}

func newWinState(id int, ws *layout.Workspace) *winState {
	key := "main"
	if id > 1 {
		key = "window-" + strconv.Itoa(id)
	}
	return &winState{id: id, key: key, ws: ws, paneBounds: map[int]ui.Rect{}}
}

// initWindows gives the app its main window's state.
func (a *App) initWindows() {
	a.main = newWinState(1, layout.New())
	a.wins = []*winState{a.main}
	a.winState, a.focusWin, a.nextWin = a.main, a.main, 1
}

// winOf is the window whose tabs show the pane with ID id, nil for none.
func (a *App) winOf(id int) *winState {
	for _, w := range a.wins {
		if t, _ := w.ws.Locate(id); t != nil {
			return w
		}
	}
	return nil
}

// enterOf makes the window showing the pane id the current one, until
// the returned func is called.
func (a *App) enterOf(id int) func() {
	prev := a.winState
	if w := a.winOf(id); w != nil {
		a.winState = w
	}
	return func() {
		if slices.Contains(a.wins, prev) {
			a.winState = prev
		} else {
			a.settle()
		}
	}
}

// settle makes the window last focused the current one, between frames.
func (a *App) settle() {
	if !slices.Contains(a.wins, a.focusWin) {
		a.focusWin = a.main
	}
	a.winState = a.focusWin
}

// sheetWin is the window the app's sheets and toasts show in.
func (a *App) sheetWin() *winState {
	if w := a.focusWin; w != nil && !w.gone && slices.Contains(a.wins, w) {
		return w
	}
	return a.main
}

// allTabs are the tabs of every window, the main one's first.
func (a *App) allTabs() []*layout.Tab {
	var out []*layout.Tab
	for _, w := range a.wins {
		out = append(out, w.ws.Tabs...)
	}
	return out
}

// allItems are the panes of every window.
func (a *App) allItems() []layout.Item {
	var out []layout.Item
	for _, w := range a.wins {
		out = append(out, w.ws.Items()...)
	}
	return out
}

// windowName names window w in a menu.
func (a *App) windowName(m locales.Messages, w *winState) string {
	if w == a.main {
		return m.MainWindow()
	}
	return m.WindowNumber(strconv.Itoa(slices.Index(a.wins, w) + 1))
}

// NewWindow opens a workspace window with no tab, placed by the system.
func (a *App) NewWindow() *winState { return a.newWindowAt(0, 0, false) }

// newWindowAt opens a workspace window with its top-left corner at x, y
// on the screen when placed.
func (a *App) newWindowAt(x, y int, placed bool) *winState {
	a.nextWin++
	w := newWinState(a.nextWin, a.main.ws.Sibling())
	a.wins = append(a.wins, w)
	a.openNative(w, x, y, placed)
	return w
}

// openNative opens the system's window of w, off the frame.
func (a *App) openNative(w *winState, x, y int, placed bool) {
	if a.p.OpenWindow == nil {
		return
	}
	spec := WindowSpec{
		Title:     "SSHYard",
		Width:     1200,
		Height:    760,
		StateKey:  w.key,
		Workspace: true,
		X:         x,
		Y:         y,
		Placed:    placed,
		View:      func(c *ui.Context) { a.windowView(c, w) },
		Closing:   func() bool { return a.windowClosing(w) },
		Closed: func() {
			quitting := a.quitting.Load()
			a.post(func() { a.windowClosed(w, quitting) })
		},
		Controls: func(ctl WindowControls) {
			a.post(func() { w.win = &ctl })
		},
		Maximized: func(on bool) { a.post(func() { w.maximized = on }) },
	}
	go func() {
		win := a.p.OpenWindow(spec)
		a.post(func() {
			if !slices.Contains(a.wins, w) {
				go win.Close() // closed while it opened
				return
			}
			w.native, w.gone = win, false
		})
	}()
}

// focusWindow brings window w to the front, off the frame.
func (a *App) focusWindow(w *winState) {
	a.focusWin = w
	if w == a.main {
		if a.p.Post != nil && a.p.ActivateMainWindow != nil {
			a.p.Post(a.p.ActivateMainWindow)
		}
		return
	}
	if win := w.native; win != nil {
		go win.Focus()
	}
}

// windowView builds another workspace window, as View builds the main one.
func (a *App) windowView(c *ui.Context, w *winState) {
	if !slices.Contains(a.wins, w) {
		return // closed: the window goes too
	}
	a.frameView(c, w)
}

// windowSessions counts the sessions of window w: its panes but the tool
// panes, which work on the sessions and have none of their own.
func (a *App) windowSessions(w *winState) int {
	n := 0
	for _, it := range w.ws.Items() {
		if c := a.conns[it.ID]; c != nil && !isTool(c.kind) {
			n++
		}
	}
	return n
}

// windowClosing says whether window w may close, as the user closes it:
// with sessions it stays and asks what to do with them.
func (a *App) windowClosing(w *winState) bool {
	if a.quitting.Load() || a.lock.resetting || !w.held.Load() {
		return true
	}
	a.post(func() {
		w.closeAsk = true
		a.focusWin = w
	})
	return false
}

// windowClosed forgets window w, gone: its panes close with it. While the
// app quits they stay, for the layout to keep.
func (a *App) windowClosed(w *winState, quitting bool) {
	w.native, w.closeAsk = nil, false
	if !slices.Contains(a.wins, w) {
		return
	}
	if quitting {
		w.gone = true
		return
	}
	defer a.enter(w)()
	for _, it := range w.ws.Items() {
		a.CloseSession(it.ID)
	}
	a.dropWin(w)
}

// enter makes w the current window until the returned func is called.
func (a *App) enter(w *winState) func() {
	prev := a.winState
	a.winState = w
	return func() {
		if slices.Contains(a.wins, prev) {
			a.winState = prev
		} else {
			a.settle()
		}
	}
}

// dropWin forgets window w, closed.
func (a *App) dropWin(w *winState) {
	if w == a.main {
		return
	}
	a.wins = slices.DeleteFunc(a.wins, func(x *winState) bool { return x == w })
	w.held.Store(false)
	a.mu.Lock()
	delete(a.wsSvc, w.id)
	a.mu.Unlock()
	if a.focusWin == w {
		a.focusWin = a.main
	}
	if a.winState == w {
		a.settle()
	}
	a.saveLayout()
	a.invalidate()
}

// closeWindow closes window w: at once when it has no sessions, else
// after asking. A window still opening closes as it opens.
func (a *App) closeWindow(w *winState) {
	if win := w.native; win != nil {
		go win.Close()
		return
	}
	a.windowClosed(w, a.quitting.Load())
}

// SetQuitting says the app is quitting (on), or that the quit was
// canceled: the windows that closed for it open again.
func (a *App) SetQuitting(on bool) {
	a.quitting.Store(on)
	if on {
		return
	}
	a.post(func() {
		for _, w := range a.wins {
			if w.gone && w.native == nil {
				a.openNative(w, 0, 0, false)
			}
		}
	})
}

// moveTab moves the tab of ID tabID from the window it is in to dst, and
// shows it there.
func (a *App) moveTab(tabID int, dst *winState) bool {
	var src *winState
	for _, w := range a.wins {
		if slices.ContainsFunc(w.ws.Tabs, func(t *layout.Tab) bool { return t.ID == tabID }) {
			src = w
		}
	}
	if src == nil || src == dst || !slices.Contains(a.wins, dst) {
		return false
	}
	t := src.ws.TakeTab(tabID)
	if t == nil || !dst.ws.AdoptTab(t) {
		return false
	}
	a.landed(dst, t)
	return true
}

// movePane moves the pane showing id from its window to a tab of its own
// in dst, and shows it there.
func (a *App) movePane(id int, dst *winState) bool {
	src := a.winOf(id)
	if src == nil || src == dst || !slices.Contains(a.wins, dst) {
		return false
	}
	if t, p := src.ws.Locate(id); t.Root == p {
		return a.moveTab(t.ID, dst)
	}
	item, ok := src.ws.Take(id)
	if !ok {
		return false
	}
	a.landed(dst, dst.ws.Open(item))
	return true
}

// landed shows tab t, just moved into window dst, and brings it there.
func (a *App) landed(dst *winState, t *layout.Tab) {
	dst.page = pageWorkspace
	dst.focusReq = t.Focused().ID
	for _, it := range t.Items() {
		if c := a.conns[it.ID]; c != nil {
			c.wid.Store(int32(dst.id))
		}
	}
	a.noteHeld()
	a.focusWindow(dst)
	a.saveLayout()
	a.invalidate()
}

// moveTabToNewWindow moves the tab of ID tabID to a new window, its
// top-left corner at x, y on the screen when placed.
func (a *App) moveTabToNewWindow(tabID int, x, y int, placed bool) {
	w := a.newWindowAt(x, y, placed)
	if !a.moveTab(tabID, w) {
		a.dropWin(w)
		a.closeWindow(w)
	}
}

// movePaneToNewWindow moves the pane showing id to a new window.
func (a *App) movePaneToNewWindow(id int) {
	w := a.NewWindow()
	if !a.movePane(id, w) {
		a.dropWin(w)
		a.closeWindow(w)
	}
}

// mergeWindow moves every tab of window w to the main window, then closes w.
func (a *App) mergeWindow(w *winState) {
	if w == a.main {
		return
	}
	for _, t := range slices.Clone(w.ws.Tabs) {
		a.moveTab(t.ID, a.main)
	}
	a.closeWindow(w)
}

// tornOut is what a tab or a pane header let go at p, a point of the
// current window, over no element taking it (DragMissed) does: outside
// the window, tear out into a window of its own, whose top-left corner
// goes at x, y on the screen when placed. It reports whether p is
// outside.
func (a *App) tornOut(c *ui.Context, p ui.Point) (x, y int, placed, out bool) {
	w, h := c.Size()
	if p.X >= 0 && p.Y >= 0 && p.X < w && p.Y < h {
		return 0, 0, false, false
	}
	var px, py int
	if a.winState == a.main {
		if pos := a.p.MainWindowPosition; pos != nil {
			px, py = pos()
			placed = true
		}
	} else if at, ok := a.winState.native.(interface{ Position() (int, int) }); ok {
		px, py = at.Position()
		placed = true
	}
	if !placed {
		return 0, 0, false, true
	}
	// The tab lands under the pointer.
	return px + int(p.X) - 60, py + int(p.Y) - 16, true, true
}

// tearTab tears the tab t, whose chip el is, out into a window of its
// own once it is let go outside the window: not the last tab of a window
// but the main one, which would only move.
func (a *App) tearTab(c *ui.Context, el ui.Element, t *layout.Tab) {
	p, ok := el.DragMissed()
	if !ok || a.winState != a.main && len(a.ws.Tabs) < 2 {
		return
	}
	if x, y, placed, out := a.tornOut(c, p); out {
		id := t.ID
		a.post(func() { a.moveTabToNewWindow(id, x, y, placed) })
	}
}

// tearPane tears the pane showing id, whose header el is, out into a
// window of its own once it is let go outside the window.
func (a *App) tearPane(c *ui.Context, el ui.Element, id int) {
	p, ok := el.DragMissed()
	if !ok {
		return
	}
	if x, y, placed, out := a.tornOut(c, p); out {
		a.post(func() {
			defer a.enterOf(id)()
			if t, n := a.ws.Locate(id); t != nil && t.Root == n {
				a.moveTabToNewWindow(t.ID, x, y, placed)
				return
			}
			w := a.newWindowAt(x, y, placed)
			if !a.movePane(id, w) {
				a.dropWin(w)
				a.closeWindow(w)
			}
		})
	}
}

// windowEntries are the menu entries moving the tab of ID tabID, or the
// pane id when tabID is 0, to a new window or another one.
func (a *App) windowEntries(m locales.Messages, tabID, id int) []menuEntry {
	cur := a.winState
	// After the frame, which may still be walking the tabs.
	toNew := func() {
		a.post(func() {
			if tabID != 0 {
				a.moveTabToNewWindow(tabID, 0, 0, false)
			} else {
				a.movePaneToNewWindow(id)
			}
		})
	}
	// The only pane of another window would only move.
	single := cur != a.main && len(cur.ws.Tabs) == 1 && len(cur.ws.Tabs[0].Panes()) == 1
	entries := []menuEntry{{Icon: icons.ToWindow, Label: m.MoveToNewWindow(), Disabled: single, Do: toNew}}
	var others []menuEntry
	for _, w := range a.wins {
		if w == cur || w.gone {
			continue
		}
		others = append(others, menuEntry{Label: a.windowName(m, w), Do: func() {
			a.post(func() {
				if tabID != 0 {
					a.moveTab(tabID, w)
				} else {
					a.movePane(id, w)
				}
			})
		}})
	}
	if len(others) > 0 {
		entries = append(entries, menuEntry{Label: m.MoveToWindow(), Sub: others})
	}
	return entries
}

// closeAskView asks, in window w closing with sessions, whether to close
// them or move them to the main window.
func (a *App) closeAskView(c *ui.Context, m locales.Messages, w *winState) {
	if !w.closeAsk {
		return
	}
	tok := currentTokens(c)
	n := a.windowSessions(w)
	sheet(ui.Modal(c.Key("closewindow"), &w.closeAsk, func() {
		ui.Column(c).Gap(18).Width(400).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, icons.Close, tok.Danger, 36)
				sheetTitle(c, m.CloseWindowTitle())
			})
			ui.Text(c, m.CloseWindowBody(strconv.Itoa(n))).FontSize(theme.Body).TextColor(tok.TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					w.closeAsk = false
				}
				if pill(c, m.CloseWindowSessions(), false, true, false) {
					w.closeAsk = false
					a.closeWindowSessions(w)
				}
				if pill(c, m.MoveToMainWindow(), true, false, false) {
					w.closeAsk = false
					a.mergeWindow(w)
				}
			})
		})
	}), tok)
}

// closeWindowSessions closes the sessions of window w, and so w.
func (a *App) closeWindowSessions(w *winState) {
	ids := w.ws.Items()
	a.post(func() {
		defer a.enter(w)()
		for _, it := range ids {
			a.CloseSession(it.ID)
		}
		if slices.Contains(a.wins, w) && len(w.ws.Tabs) == 0 {
			a.closeWindow(w)
		}
	})
}

// closeEmpty closes window w, not the main one, once its last tab went.
func (a *App) closeEmpty(w *winState) {
	if w == a.main || a.quitting.Load() || w.gone {
		return
	}
	if len(w.ws.Tabs) > 0 {
		w.hadTabs = true
		return
	}
	if w.hadTabs {
		w.hadTabs = false
		a.post(func() {
			if slices.Contains(a.wins, w) && len(w.ws.Tabs) == 0 {
				a.closeWindow(w)
			}
		})
	}
}

// invalidateWin asks for a frame of window id alone.
func (a *App) invalidateWin(id int) {
	if id <= 1 {
		a.invalidateMain()
		return
	}
	a.mu.Lock()
	svc, ok := a.wsSvc[id]
	a.mu.Unlock()
	if ok {
		svc.Invalidate()
		return
	}
	a.invalidate()
}

// noteHeld notes which windows hold sessions, for the system's thread to
// read as one closes (windowClosing).
func (a *App) noteHeld() {
	for _, w := range a.wins {
		w.held.Store(a.windowSessions(w) > 0)
	}
}
