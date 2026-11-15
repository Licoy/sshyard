package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// TestTabOfNumber: the first eight numbers show their tab, the ninth the
// last, and a number past the tabs, or none, shows none.
func TestTabOfNumber(t *testing.T) {
	for _, tc := range []struct{ n, count, want int }{
		{1, 3, 0}, {3, 3, 2}, {4, 3, -1}, {8, 8, 7}, {8, 12, 7},
		{9, 1, 0}, {9, 3, 2}, {9, 12, 11},
		{1, 0, -1}, {9, 0, -1}, {0, 3, -1}, {10, 12, -1},
	} {
		if got := tabOfNumber(tc.n, tc.count); got != tc.want {
			t.Errorf("tab %d of %d: %d, want %d", tc.n, tc.count, got, tc.want)
		}
	}
}

// TestTabShortcuts: appMods with 1 to 9 are the app's, recordable like
// any other, and run by their action; Ctrl with a digit alone still
// reaches the shell as the control character it types.
func TestTabShortcuts(t *testing.T) {
	for n := 1; n <= tabJumps; n++ {
		id := actTab + string(rune('0'+n))
		if tabNumber(id) != n {
			t.Errorf("%s: number %d", id, tabNumber(id))
		}
		if !isAppShortcut(nil, ui.Key0+ui.Key(n), appMods) {
			t.Errorf("%s: its default does not reach the app", id)
		}
	}
	for _, id := range []string{actTab, actTab + "0", actTab + "10", actNewTab} {
		if tabNumber(id) != 0 {
			t.Errorf("%s is not a tab action", id)
		}
	}
	if isAppShortcut(nil, ui.Key2, ui.Ctrl) {
		t.Error("Ctrl+2 must stay the shell's")
	}
	if b, ok := EncodeKey(ui.Key2, ui.Ctrl, vt.Modes{}); !ok || string(b) != "\x00" {
		t.Errorf("Ctrl+2 sends %q", b)
	}

	a, _ := newTestApp(t)
	h := savedHost(t, a, "saved", "127.0.0.1")
	for range 4 {
		cn, err := a.makeConn(h.ID, layout.Terminal, 0)
		if err != nil {
			t.Fatal(err)
		}
		a.ws.Open(cn.item())
	}
	a.page = pageSettings
	a.runAction(actTab + "2")
	if a.ws.Active != 1 || a.page != pageWorkspace || a.focusReq != a.ws.Tabs[1].Focused().ID {
		t.Fatalf("tab 2: active %d, page %v", a.ws.Active, a.page)
	}
	a.runAction(actTab + "9")
	if a.ws.Active != 3 {
		t.Fatalf("last tab: active %d", a.ws.Active)
	}
	a.runAction(actTab + "6")
	if a.ws.Active != 3 {
		t.Fatalf("a tab past the last moved to %d", a.ws.Active)
	}
}

// TestTabName: a tab's own name wins, then what its terminal reported
// for the setting, then its host's name.
func TestTabName(t *testing.T) {
	for _, tc := range []struct {
		manual, source, reported, want string
		homes                          []string
	}{
		{"Mine", settings.TabTitleProgram, "vim", "Mine", nil},
		{"Mine", settings.TabTitleCwd, "/srv/app", "Mine", nil},
		{"", "", "vim", "web", nil},
		{"", settings.TabTitleProgram, "  vim main.go ", "vim main.go", nil},
		{"", settings.TabTitleProgram, "   ", "web", nil},
		{"", settings.TabTitleCwd, "/srv/app/", "app", nil},
		{"", settings.TabTitleCwd, "/srv/app/../logs", "logs", nil},
		{"", settings.TabTitleCwd, "/home/root", "~", []string{"/root", "/home/root"}},
		{"", settings.TabTitleCwd, "/home/root/src", "src", []string{"/home/root"}},
		{"", settings.TabTitleCwd, "/C:/Users/me", "~", []string{`C:\Users\me`}},
		{"", settings.TabTitleCwd, "/", "/", []string{"/"}},
		{"", settings.TabTitleCwd, "relative/dir", "web", nil},
		{"", settings.TabTitleCwd, "", "web", nil},
	} {
		if got := tabName(tc.manual, tc.source, "web", tc.reported, tc.homes...); got != tc.want {
			t.Errorf("%q %q %q: %q, want %q", tc.manual, tc.source, tc.reported, got, tc.want)
		}
	}
}

// TestTabLabelSource: a tab takes what its terminal reports as the
// setting chooses, back to its host's name without a report, and keeps
// the name it was given.
func TestTabLabelSource(t *testing.T) {
	a, _ := newTestApp(t)
	h := savedHost(t, a, "saved", "127.0.0.1")
	cn, err := a.makeConn(h.ID, layout.Terminal, 0)
	if err != nil {
		t.Fatal(err)
	}
	tab := a.ws.Open(cn.item())
	a.settings.TabTitle = settings.TabTitleCwd
	if got := a.tabLabel(tab); got != "saved" {
		t.Fatalf("nothing reported: %q", got)
	}
	cn.s.Term.WriteString("\x1b]2;htop\x07\x1b]7;file://saved/root\x07")
	if got, full := a.tabLabel(tab), a.tabSource(cn); got != "~" || full != "/root" {
		t.Fatalf("directory: %q (%q)", got, full)
	}
	a.settings.TabTitle = settings.TabTitleProgram
	if got := a.tabLabel(tab); got != "htop" {
		t.Fatalf("program title: %q", got)
	}
	a.settings.TabTitle = ""
	if got := a.tabLabel(tab); got != "saved" {
		t.Fatalf("host name: %q", got)
	}
	a.settings.TabTitle = settings.TabTitleProgram
	a.RenameTab(tab.ID, "Mine")
	if got := a.tabLabel(tab); got != "Mine" {
		t.Fatalf("renamed: %q", got)
	}
}

// TestMiddleClickClosesTab: the middle button let go over a tab closes
// it, let go elsewhere does not; the other buttons go on to the tab.
func TestMiddleClickClosesTab(t *testing.T) {
	a, _ := newTestApp(t)
	h := savedHost(t, a, "saved", "127.0.0.1")
	for range 2 {
		cn, err := a.makeConn(h.ID, layout.Terminal, 0)
		if err != nil {
			t.Fatal(err)
		}
		a.ws.Open(cn.item())
	}
	first := a.ws.Tabs[0]
	in := a.middleClose(first, ui.Rect{W: 200, H: 36})
	for _, b := range []int{0, 1} {
		if in(ui.InputEvent{Kind: ui.InputPointerDown, Button: b, X: 10, Y: 10}) {
			t.Errorf("button %d taken", b)
		}
	}
	if !in(ui.InputEvent{Kind: ui.InputPointerDown, Button: 2, X: 10, Y: 10}) {
		t.Fatal("the middle press must be taken")
	}
	in(ui.InputEvent{Kind: ui.InputPointerUp, Button: 2, X: 10, Y: 80})
	if len(a.ws.Tabs) != 2 {
		t.Fatal("let go off the tab, it closed")
	}
	// A press the system took back commits nothing.
	in(ui.InputEvent{Kind: ui.InputPointerDown, Button: 2, X: 10, Y: 10})
	in(ui.InputEvent{Kind: ui.InputPointerUp, Button: 2, X: 20, Y: 12, Canceled: true})
	if len(a.ws.Tabs) != 2 {
		t.Fatal("a canceled middle press closed the tab")
	}
	in(ui.InputEvent{Kind: ui.InputPointerDown, Button: 2, X: 10, Y: 10})
	in(ui.InputEvent{Kind: ui.InputPointerUp, Button: 2, X: 20, Y: 12})
	if len(a.ws.Tabs) != 1 || a.ws.Tabs[0] == first {
		t.Fatalf("%d tabs: the middle click did not close it", len(a.ws.Tabs))
	}
}

// TestLayoutTabColorRoundTrip: a tab's color is saved with the layout and
// comes back with it; one the app does not know does not.
func TestLayoutTabColorRoundTrip(t *testing.T) {
	dir, backend := t.TempDir(), keychain.NewMemory()
	rememberLayoutIn(t, dir)
	a, _ := appIn(t, dir, backend)
	h := savedHost(t, a, "saved", "127.0.0.1")
	for range 3 {
		cn, err := a.makeConn(h.ID, layout.Terminal, 0)
		if err != nil {
			t.Fatal(err)
		}
		a.ws.Open(cn.item())
	}
	a.SetTabColor(a.ws.Tabs[0].ID, "teal")
	a.SetTabColor(a.ws.Tabs[1].ID, "chartreuse")
	if a.ws.Tabs[1].Color != "" {
		t.Fatalf("unknown color kept: %q", a.ws.Tabs[1].Color)
	}
	a.SetTabColor(a.ws.Tabs[2].ID, "red")
	a.Close()

	path := filepath.Join(dir, "layout.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"color": "teal"`) || strings.Count(string(raw), `"color"`) != 2 {
		t.Fatalf("colors not saved: %s", raw)
	}
	// A file edited to a color the app does not know.
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), `"red"`, `"chartreuse"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	again, _ := appIn(t, dir, backend)
	if len(again.ws.Tabs) != 3 {
		t.Fatalf("%d tabs", len(again.ws.Tabs))
	}
	for i, want := range []string{"teal", "", ""} {
		if got := again.ws.Tabs[i].Color; got != want {
			t.Errorf("tab %d: color %q, want %q", i, got, want)
		}
	}
}
