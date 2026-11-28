package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/egoist/mygo/ui"
)

// testWin is the main window's state for an App a test builds by hand.
func testWin() *winState { return newWinState(1, layout.New()) }

// openLocal opens a local terminal in a tab of its own of the current
// window and returns its ID.
func openLocal(t *testing.T, a *App) int {
	t.Helper()
	id, err := a.OpenLocalTerminal()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// openedWin waits for window w to have its native window, the n-th the
// app opened.
func openedWin(t *testing.T, a *App, tt *ui.Tester, ws *testWindows, w *winState, n int) *testWindow {
	t.Helper()
	waitFor(t, tt, "window opened", func() bool { return w.native != nil && len(ws.list()) >= n })
	tw := ws.list()[n-1]
	if !tw.spec.Workspace || tw.spec.StateKey != w.key {
		t.Fatalf("opened %+v for %q", tw.spec, w.key)
	}
	return tw
}

// A tab and a pane move to another window and back, their sessions kept;
// the window left with no tab closes.
func TestWindowMovesKeepSessions(t *testing.T) {
	a, tt := newTestApp(t)
	ws := fakeWindows(a)
	one, two := openLocal(t, a), openLocal(t, a) // two splits one's tab
	s2 := a.conns[two].s
	a.movePaneToNewWindow(two)
	if len(a.wins) != 2 {
		t.Fatalf("windows %d", len(a.wins))
	}
	w := a.wins[1]
	if a.winOf(two) != w || a.winOf(one) != a.main || len(a.main.ws.Tabs) != 1 {
		t.Fatal("tab not moved")
	}
	if a.conns[two] == nil || a.conns[two].s != s2 || w.focusReq != two {
		t.Fatal("session not kept")
	}
	tw := openedWin(t, a, tt, ws, w, 1)
	if !a.movePane(one, w) || len(a.main.ws.Tabs) != 0 || len(w.ws.Tabs) != 2 {
		t.Fatal("pane not moved")
	}
	if a.windowSessions(w) != 2 || a.windowSessions(a.main) != 0 {
		t.Fatal("sessions counted")
	}
	a.mergeWindow(w)
	waitFor(t, tt, "merged window closed", func() bool { return len(a.wins) == 1 })
	if len(a.wins) != 1 || len(a.main.ws.Tabs) != 2 || a.conns[one] == nil || a.conns[two].s != s2 {
		t.Fatalf("merge: windows %d tabs %d", len(a.wins), len(a.main.ws.Tabs))
	}
	if _, _, closed, _ := tw.state(); !closed {
		t.Fatal("merged window still open")
	}
	// A window whose last tab leaves closes.
	a.moveTabToNewWindow(a.main.ws.Tabs[0].ID, 0, 0, false)
	w = a.wins[1]
	tw = openedWin(t, a, tt, ws, w, 2)
	a.closeEmpty(w)
	a.moveTab(w.ws.Tabs[0].ID, a.main)
	a.closeEmpty(w)
	waitFor(t, tt, "empty window closed", func() bool { return len(a.wins) == 1 })
	if _, _, closed, _ := tw.state(); !closed || len(a.main.ws.Tabs) != 2 {
		t.Fatal("empty window kept")
	}
}

// A pane split off, or a tab separated, makes tabs in the window it is in,
// from outside its frames too, keeping the sessions.
func TestSeparateIntoTabsInItsWindow(t *testing.T) {
	a, _ := newTestApp(t)
	fakeWindows(a)
	one, two := openLocal(t, a), openLocal(t, a) // two splits one's tab
	a.movePaneToNewWindow(two)
	w := a.wins[1]
	a.winState = w
	three := openLocal(t, a) // splits two's tab in w
	if len(w.ws.Tabs) != 1 || len(w.ws.Tabs[0].Panes()) != 2 {
		t.Fatalf("w tabs %d", len(w.ws.Tabs))
	}
	s3 := a.conns[three].s
	a.winState = a.main
	if a.paneToNewTab(one) {
		t.Fatal("split off a lone pane")
	}
	if !a.paneToNewTab(three) || len(w.ws.Tabs) != 2 || len(a.main.ws.Tabs) != 1 || a.winState != a.main {
		t.Fatalf("split off: w tabs %d, main tabs %d", len(w.ws.Tabs), len(a.main.ws.Tabs))
	}
	if w.ws.Active != 1 || w.ws.ActiveTab().Focused().ID != three || w.focusReq != three || a.conns[three].s != s3 {
		t.Fatal("split-off pane not shown with its session")
	}
	// Back in one tab, then separated.
	a.winState = w
	if !a.ws.Move(three, w.ws.Tabs[0].Root.ID, layout.Bottom) || len(w.ws.Tabs) != 1 {
		t.Fatal("regroup")
	}
	tab := w.ws.Tabs[0].ID
	a.winState = a.main
	if !a.separateTab(tab) || len(w.ws.Tabs) != 2 || a.winState != a.main {
		t.Fatalf("separate: w tabs %d", len(w.ws.Tabs))
	}
	if a.winOf(two) != w || a.winOf(three) != w || a.conns[two] == nil || a.conns[three].s != s3 {
		t.Fatal("sessions not kept in w")
	}
	if a.separateTab(tab) || a.separateTab(-1) {
		t.Fatal("separated a lone pane or a missing tab")
	}
}

// A pane's header dropped on a tab takes the pane out to a tab of its own
// beside it; a pane alone in its tab takes its tab there.
func TestPaneDroppedOnTabBar(t *testing.T) {
	a, _ := newTestApp(t)
	one, two := openLocal(t, a), openLocal(t, a) // two splits one's tab
	three := openLocal(t, a)
	if !a.ws.Detach(three) || len(a.ws.Tabs) != 2 {
		t.Fatal("setup")
	}
	order := func() []int {
		var out []int
		for _, x := range a.ws.Tabs {
			out = append(out, x.Root.Item.ID)
		}
		return out
	}
	first, last := a.ws.Tabs[0], a.ws.Tabs[1]
	if a.tabTakes(dragSession{ID: three}, last) || !a.tabTakes(dragSession{ID: three}, first) || !a.tabTakes(dragSession{ID: two}, first) {
		t.Fatal("what a tab takes")
	}
	// Two, split with one, dropped after the last tab.
	s2 := a.conns[two].s
	if !a.paneToTab(two, last.ID, true) || !slices.Equal(order(), []int{one, three, two}) {
		t.Fatalf("tabs %v", order())
	}
	if a.ws.ActiveTab().Focused().ID != two || a.focusReq != two || a.conns[two].s != s2 {
		t.Fatal("dropped pane not shown with its session")
	}
	// Three, alone in its tab, takes it before the first.
	if !a.paneToTab(three, first.ID, false) || !slices.Equal(order(), []int{three, one, two}) || len(a.ws.Tabs) != 3 {
		t.Fatalf("tab moved %v", order())
	}
	if a.paneToTab(-1, first.ID, false) {
		t.Fatal("moved a missing pane")
	}
}

// Closing a window with sessions asks first; closing them ends them.
func TestWindowCloseAsks(t *testing.T) {
	a, tt := newTestApp(t)
	ws := fakeWindows(a)
	id := openLocal(t, a)
	w := a.NewWindow()
	tw := openedWin(t, a, tt, ws, w, 1)
	a.movePane(id, w)
	if tw.userClose() {
		t.Fatal("closed with a session")
	}
	tt.Frame()
	if !w.closeAsk || a.conns[id] == nil {
		t.Fatal("did not ask")
	}
	a.closeWindowSessions(w)
	waitFor(t, tt, "window closed", func() bool { return len(a.wins) == 1 })
	if a.conns[id] != nil {
		t.Fatal("session left running")
	}
	if _, _, closed, _ := tw.state(); !closed {
		t.Fatal("window left open")
	}
}

// A window closed as the app quits keeps its tabs for the layout, and
// opens again should the quit be canceled.
func TestWindowQuitKeepsTabs(t *testing.T) {
	a, tt := newTestApp(t)
	ws := fakeWindows(a)
	id := openLocal(t, a)
	w := a.NewWindow()
	tw := openedWin(t, a, tt, ws, w, 1)
	a.movePane(id, w)
	a.SetQuitting(true)
	if !tw.userClose() {
		t.Fatal("asked while quitting")
	}
	waitFor(t, tt, "window gone", func() bool { return w.gone })
	if !w.gone || a.winOf(id) != w || a.conns[id] == nil {
		t.Fatal("tabs not kept")
	}
	snap := a.layoutSnapshot()
	if len(snap.Windows) != 1 || snap.Windows[0].Key != w.key || len(snap.Windows[0].Tabs) != 1 {
		t.Fatalf("snapshot windows %+v", snap.Windows)
	}
	a.SetQuitting(false)
	openedWin(t, a, tt, ws, w, 2)
	if w.gone {
		t.Fatal("window still gone")
	}
}

// The tool panes stay one of each in the app: toggled in another window,
// one open elsewhere comes over.
func TestWindowToolPaneComesOver(t *testing.T) {
	a, tt := newTestApp(t)
	fakeWindows(a)
	a.ToggleTool(layout.Snippets)
	c := a.toolPane(layout.Snippets)
	if c == nil || a.winOf(c.id) != a.main {
		t.Fatal("snippets not opened")
	}
	w := a.NewWindow()
	back := a.enter(w)
	a.ToggleTool(layout.Snippets)
	back()
	tt.Frame()
	if a.toolPane(layout.Snippets) != c || a.winOf(c.id) != w || len(a.main.ws.Tabs) != 0 {
		t.Fatal("snippets did not come over")
	}
}

// The windows' tabs are saved beside the main window's and restored in
// windows of their own; a damaged window list blocks the layout.
func TestLayoutWindowsRestore(t *testing.T) {
	dir := t.TempDir()
	rememberLayoutIn(t, dir)
	raw := []byte(`{"version":1,"tabs":[{"focus":1,"root":{"id":1,"kind":"snippets"}}],
		"windows":[{"key":"window-3","active":1,"tabs":[
			{"focus":2,"root":{"id":2,"kind":"history"}},
			{"focus":3,"root":{"id":3,"kind":"monitor"}}]}]}`)
	if err := os.WriteFile(filepath.Join(dir, "layout.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ := appIn(t, dir, nil)
	if a.layoutBlocked || len(a.wins) != 2 || len(a.main.ws.Tabs) != 1 {
		t.Fatalf("blocked=%v windows=%d", a.layoutBlocked, len(a.wins))
	}
	w := a.wins[1]
	if w.key != "window-3" || len(w.ws.Tabs) != 2 || w.ws.Active != 1 || w.ws.Tabs[0].Root.Item.Kind != layout.History {
		t.Fatalf("window %q tabs %d active %d", w.key, len(w.ws.Tabs), w.ws.Active)
	}
	if n := a.NewWindow(); n.id <= w.id {
		t.Fatalf("new window %d not after %d", n.id, w.id)
	}
	b, err := a.layoutBytes(true)
	if err != nil {
		t.Fatal(err)
	}
	var snap layoutSnap
	if err := json.Unmarshal(b, &snap); err != nil || len(snap.Windows) != 1 || snap.Windows[0].Key != "window-3" || snap.Windows[0].Active != 1 {
		t.Fatalf("saved %s", b)
	}
	for _, bad := range []layoutSnap{
		{Version: 1, Windows: []windowSnap{{Key: "main"}}},
		{Version: 1, Windows: []windowSnap{{Key: "window-2"}, {Key: "window-2"}}},
		{Version: 1, Tabs: []tabSnap{{Root: &nodeSnap{ID: 1, Kind: "logs"}}}, Windows: []windowSnap{{Key: "window-2", Tabs: []tabSnap{{Root: &nodeSnap{ID: 1, Kind: "history"}}}}}},
	} {
		if validLayout(bad) {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
