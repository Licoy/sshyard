package app

import (
	"slices"
	"strconv"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
)

// TestTitleBarMultipleShells exercises shell-named tabs on every platform,
// using an installed shell so its local terminal sessions still run.
func TestTitleBarMultipleShells(t *testing.T) {
	first := session.Shells()[0]
	second := first
	first.ID, first.Name = "test-powershell", "PowerShell"
	second.ID, second.Name = "test-cmd", "CMD"
	withShells(t, first, second)
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"TitleBar", TestTitleBar},
		{"Tabs", TestTabs},
		{"TabsOverflow", TestTabsOverflow},
		{"TabMenu", TestTabMenu},
	} {
		t.Run(tc.name, tc.run)
	}
}

// TestTitleBar: its GitHub button opens the repository, its updates
// button checks without opening the settings, its settings button closes
// the settings, and the window's own buttons call the window.
func TestTitleBar(t *testing.T) {
	a, tt := newTestApp(t)
	mustClick(t, tt, "GitHub Repository")
	if got := tt.OpenedURLs(); !slices.Equal(got, []string{repoURL}) {
		t.Errorf("GitHub opened %q", got)
	}
	upd := &fakeUpdates{enabled: true}
	a.upd = upd
	mustClick(t, tt, "Check for Updates")
	if upd.checks != 1 || a.page == pageSettings {
		t.Errorf("updates: %d checks, page %v", upd.checks, a.page)
	}
	a.openSettings(prefAbout)
	tt.Frame()
	mustClick(t, tt, "Close Settings")
	if a.page == pageSettings {
		t.Error("the settings button did not close them")
	}

	var mins, maxes, restores, closes int
	a.SetWindow(WindowControls{
		Minimize:   func() { mins++ },
		Maximize:   func() { maxes++ },
		Unmaximize: func() { restores++ },
		Close:      func() { closes++ },
	})
	tt.Frame()
	mustClick(t, tt, "Minimize")
	mustClick(t, tt, "Maximize")
	a.SetMaximized(true)
	tt.Frame()
	mustClick(t, tt, "Restore")
	mustClick(t, tt, "Close Window")
	if mins != 1 || maxes != 1 || restores != 1 || closes != 1 {
		t.Errorf("minimize %d, maximize %d, restore %d, close %d", mins, maxes, restores, closes)
	}
}

// TestTabs switches between the tabs with a click.
func TestTabs(t *testing.T) {
	a, tt := newTestApp(t)
	local := a.localHost(localID).Label()
	for range 2 {
		if _, err := a.open(localID, layout.Terminal, openAt{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()
	tt.Frame()
	if len(a.ws.Tabs) != 2 || a.ws.Active != 1 {
		t.Fatalf("tabs %d, active %d", len(a.ws.Tabs), a.ws.Active)
	}
	first, ok := tt.Find(local)
	if !ok {
		t.Fatalf("no tabs in %q", tt.Texts())
	}
	tt.ClickAt(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()
	if a.ws.Active != 0 {
		t.Errorf("active %d after a click on the first tab", a.ws.Active)
	}
}

// TestHostActions: a host's row menu makes it a favorite, and the
// favorites' row menu edits it.
func TestHostActions(t *testing.T) {
	a, tt := newTestApp(t)
	h, err := a.Store().Add(assets.Host{Name: "web", Address: "10.0.0.5", User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store().Add(assets.Host{Name: "db", Address: "10.0.0.6", User: "root"}); err != nil {
		t.Fatal(err)
	}
	a.selected = h.ID
	tt.Move(600, 700) // over nothing
	tt.Frame()
	tt.Frame()
	mustClick(t, tt, "More Actions")
	mustClick(t, tt, "Favorite")
	tt.Frame()
	if got, _ := a.Store().Get(h.ID); !got.Favorite {
		t.Error("not a favorite")
	}
	// A favorite is listed twice; the chosen row is the favorites' one.
	a.selected = "fav:" + h.ID
	tt.Frame()
	mustClick(t, tt, "More Actions")
	mustClick(t, tt, "Edit")
	tt.Frame()
	if !a.editing || a.form == nil || a.form.orig.ID != h.ID {
		t.Error("editor not open")
	}
}

// TestTabsOverflow keeps the active tab among those shown, and the others
// in the popover of a button after them, where a row chooses its tab.
func TestTabsOverflow(t *testing.T) {
	a, tt := newTestApp(t)
	for range 12 {
		if _, err := a.open(localID, layout.Terminal, openAt{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()
	tt.Frame()
	tt.Frame()
	if k := a.tabRoomShown(); k == 0 || k >= 12 {
		t.Fatalf("%d tabs shown", k)
	}
	if a.ws.Active != 11 || a.tabStart+a.tabRoomShown() != 12 {
		t.Errorf("active %d not shown: from %d", a.ws.Active, a.tabStart)
	}
	more := mustFind(t, tt, strconv.Itoa(12-a.tabRoomShown())+" more tabs")
	// The wheel moves along the tabs, away from the active one, which
	// stays there until another is chosen.
	from := a.tabStart
	tt.Wheel(more.X+more.W/2, more.Y+more.H/2, 0, -40)
	tt.Frame()
	tt.Frame()
	if a.tabStart != from-1 || a.ws.Active != 11 {
		t.Errorf("wheel: from %d to %d, active %d", from, a.tabStart, a.ws.Active)
	}
	if err := tt.Click(strconv.Itoa(12-a.tabRoomShown()) + " more tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// The popover, not a native menu: a row for each tab not shown.
	if !a.tabsOpen || menuOf(a) != nil {
		t.Fatalf("no popover: open %v, menu %q", a.tabsOpen, menuOf(a))
	}
	// Its first row, under the button and level with its end.
	tt.ClickAt(more.X+more.W-moreTabWidth/2, more.Y+more.H+6+5+18)
	tt.Frame()
	if a.tabsOpen {
		t.Error("the popover stayed open")
	}
	if a.ws.Active != 0 || a.tabStart != 0 {
		t.Errorf("chose the first: active %d, from %d", a.ws.Active, a.tabStart)
	}
}

// tabRoomShown is how many tabs the title bar shows now.
func (a *App) tabRoomShown() int {
	_, k := tabsShown(len(a.ws.Tabs), a.ws.Active, a.tabStart, a.tabRoom)
	return k
}

func TestTabWheelStep(t *testing.T) {
	for _, c := range []struct {
		rest, d  float32
		precise  bool
		want     int
		wantRest float32
	}{
		{0, 40, false, 1, 0},                             // a notch: one
		{0, -3, false, -1, 0},                            // either way
		{0, 0, false, 0, 0},                              // nothing
		{0, tabWheelRoom - 1, true, 0, tabWheelRoom - 1}, // not yet
		{tabWheelRoom - 1, 2, true, 1, 1},                // added up
		{0, -2*tabWheelRoom - 1, true, -2, -1},           // two back
		{tabWheelRoom - 1, -tabWheelRoom, true, -1, 0},   // turned back
	} {
		n, rest := tabWheelStep(c.rest, c.d, c.precise)
		if n != c.want || rest != c.wantRest {
			t.Errorf("%+v: %d, %v", c, n, rest)
		}
	}
}

func TestTabsShown(t *testing.T) {
	room := func(n int) float32 { return 2*tabInset + float32(n)*(tabMinWidth+tabGap) - tabGap }
	for _, c := range []struct {
		n, active, start int
		room             float32
		wantStart, wantK int
	}{
		{5, 4, 0, 0, 0, 5},                      // not measured: all
		{5, 4, 0, room(5), 0, 5},                // all fit
		{10, 0, 0, room(5), 0, 4},               // 4, and the button
		{10, 9, 0, room(5), 6, 4},               // the active one last
		{10, 7, 6, room(5), 6, 4},               // kept where it was
		{10, 2, 6, room(5), 2, 4},               // back to the active one
		{10, 9, 0, 10, 9, 1},                    // at least one
		{3, 1, 7, room(2) + tabMoreWidth, 1, 2}, // a start past the end
	} {
		s, k := tabsShown(c.n, c.active, c.start, c.room)
		if s != c.wantStart || k != c.wantK {
			t.Errorf("%+v: %d, %d", c, s, k)
		}
	}
}

// TestTabMenu closes tabs round one, on its host and on others, from
// its menu.
func TestTabMenu(t *testing.T) {
	a, tt := newTestApp(t)
	local := a.localHost(localID).Label()
	h, err := a.Store().Add(assets.Host{Name: "web", Address: "127.0.0.1", Port: 1, User: "root", Auth: assets.AuthAgent})
	if err != nil {
		t.Fatal(err)
	}
	open := func(id string, n int) {
		t.Helper()
		for range n {
			if _, err := a.open(id, layout.Terminal, openAt{}, nil); err != nil {
				t.Fatal(err)
			}
		}
		tt.Frame()
		tt.Frame()
	}
	hosts := func() (s string) {
		for _, tb := range a.ws.Tabs {
			if a.tabHost(tb) == h.ID {
				s += "W"
			} else {
				s += "L"
			}
		}
		return s
	}
	menu := func(label, item string) {
		t.Helper()
		if err := tt.RightClick(label); err != nil {
			t.Fatal(err)
		}
		if err := chooseMenu(a, tt, item); err != nil {
			t.Fatalf("%v: %q", err, menuOf(a))
		}
		tt.Frame()
		tt.Frame()
	}
	open(localID, 3)
	open(h.ID, 2)
	if hosts() != "LLLWW" {
		t.Fatalf("tabs %s", hosts())
	}

	menu("web", "Close Tabs of Other Hosts")
	if hosts() != "WW" {
		t.Fatalf("other hosts closed: %s", hosts())
	}
	open(localID, 2) // WWLL
	menu("web", "Close Tabs to the Right")
	if hosts() != "W" {
		t.Fatalf("right of the first closed: %s", hosts())
	}
	open(localID, 2) // WLL
	open(h.ID, 1)    // WLLW
	menu(local, "Close All Tabs of This Host")
	if hosts() != "WW" {
		t.Fatalf("the host's closed: %s", hosts())
	}
	open(localID, 1) // WWL
	menu(local, "Close Tabs to the Left")
	if hosts() != "L" {
		t.Fatalf("left closed: %s", hosts())
	}
	open(localID, 2) // LLL
	menu(local, "Close Other Tabs")
	if len(a.ws.Tabs) != 1 {
		t.Fatalf("others closed: %s", hosts())
	}
	open(h.ID, 1)
	menu("web", "Close All Tabs")
	if len(a.ws.Tabs) != 0 {
		t.Fatalf("all closed: %s", hosts())
	}
}

// TestNewTabButton: the "+" opens a drop-down, never a tab of the
// focused session's host at once, and its Quick Connect starts one.
func TestNewTabButton(t *testing.T) {
	a, tt := newTestApp(t)
	if _, err := a.Store().Add(assets.Host{Name: "web", Address: "10.0.0.5", User: "root"}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := a.OpenLocalTerminal(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	mustClick(t, tt, "New Tab")
	if len(a.ws.Tabs) != 1 || a.menu.key != "newtab" {
		t.Fatalf("%d tabs, menu %q: the + did not open its menu", len(a.ws.Tabs), a.menu.key)
	}
	mustClick(t, tt, "Quick Connect…")
	tt.Frame()
	if a.quick == nil || a.menu.key != "" || len(a.ws.Tabs) != 1 {
		t.Fatalf("quick %v, menu %q, %d tabs", a.quick != nil, a.menu.key, len(a.ws.Tabs))
	}
}

// TestDuplicateTab: the tab's menu opens a new session on its host in a
// new tab, a files pane for a files pane.
func TestDuplicateTab(t *testing.T) {
	a, tt := newTestApp(t)
	h, err := a.Store().Add(assets.Host{Name: "web", Address: "127.0.0.1", Port: 1, User: "root", Auth: assets.AuthAgent})
	if err != nil {
		t.Fatal(err)
	}
	duplicate := func(i int) *conn {
		t.Helper()
		a.openContext(10, 10, func() []menuEntry { return a.tabEntries(locales.In("en"), i) })
		tt.Frame()
		if err := chooseMenu(a, tt, "Duplicate Session"); err != nil {
			t.Fatalf("%v: %q", err, menuOf(a))
		}
		tt.Frame()
		return a.focused()
	}
	if _, err := a.open(localID, layout.Terminal, openAt{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenFiles(h.ID, openAt{}, nil); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cn := duplicate(0); len(a.ws.Tabs) != 3 || a.ws.Active != 2 || cn.kind != layout.Terminal || cn.host.ID != localID {
		t.Fatalf("terminal: %d tabs, active %d, %+v", len(a.ws.Tabs), a.ws.Active, cn)
	}
	if cn := duplicate(1); len(a.ws.Tabs) != 4 || a.ws.Active != 3 || cn.kind != layout.Files || cn.host.ID != h.ID {
		t.Fatalf("files: %d tabs, active %d, %+v", len(a.ws.Tabs), a.ws.Active, cn)
	}
}

// TestCopyTabHostIP: the tab's menu copies its host's address, and has
// none to copy for a local terminal.
func TestCopyTabHostIP(t *testing.T) {
	a, tt := newTestApp(t)
	h, err := a.Store().Add(assets.Host{Name: "web", Address: "10.0.0.5", Port: 2222, User: "root", Auth: assets.AuthAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.open(localID, layout.Terminal, openAt{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.open(h.ID, layout.Terminal, openAt{}, nil); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.tabAddress(a.ws.Tabs[0]); got != "" {
		t.Errorf("local terminal has address %q", got)
	}
	a.openContext(10, 10, func() []menuEntry { return a.tabEntries(locales.In("en"), 1) })
	tt.Frame()
	if err := chooseMenu(a, tt, "Copy Host IP"); err != nil {
		t.Fatalf("%v: %q", err, menuOf(a))
	}
	tt.Frame()
	if tt.Clipboard() != "10.0.0.5" {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
}
