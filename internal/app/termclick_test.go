package app

import (
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// keptClipboard is a clipboard that keeps what is copied.
type keptClipboard struct{ text string }

func (k *keptClipboard) ReadClipboard() string   { return k.text }
func (k *keptClipboard) WriteClipboard(s string) { k.text = s }

// shownText waits until the terminal of c shows s on its first line.
func shownText(t *testing.T, tt *ui.Tester, c *conn, s string) {
	t.Helper()
	waitFor(t, tt, "the output "+s, func() bool {
		return strings.Contains(c.s.Term.Text(vt.Pos{}, vt.Pos{Col: 79}), s)
	})
	tt.Frame()
}

// TestTerminalRightClick: a right click opens the menu by default; set
// to paste it pastes, and set to copy or paste it copies a selection,
// clearing it, or pastes without one. Shift keeps the menu.
func TestTerminalRightClick(t *testing.T) {
	srv, sh := startScript(t, "hello world")
	a, tt := newTestApp(t)
	c := connected(t, a, tt, srv)
	shownText(t, tt, c, "hello world")
	x, y := cellPoint(t, tt, c, 1, 0)

	tt.RightClickAt(x, y)
	if menuOf(a) == nil {
		t.Fatal("the default right click opened no menu")
	}
	a.menu = dropMenu{}
	tt.Frame()

	set := func(mode string) {
		s := a.Settings()
		s.TerminalRightClick = mode
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
	}
	set(settings.RightClickPaste)
	sh.take()
	tt.SetClipboard("pasted")
	tt.RightClickAt(x, y)
	expectRead(t, tt, sh, "pasted")
	if menuOf(a) != nil {
		t.Error("a pasting right click opened the menu")
	}

	set(settings.RightClickCopyPaste)
	r, _ := tt.Find("Terminal box")
	lx, ly := x-r.X, y-r.Y
	cb := &keptClipboard{}
	c.tv.input(ui.InputEvent{Kind: ui.InputPointerDown, X: lx, Y: ly, Clicks: 2}, cb)
	c.tv.input(ui.InputEvent{Kind: ui.InputPointerUp, X: lx, Y: ly}, cb)
	if c.tv.SelectedText() != "hello" {
		t.Fatalf("double click selected %q", c.tv.SelectedText())
	}
	tt.SetClipboard("")
	sh.take()
	tt.RightClickAt(x, y)
	tt.Frame()
	if tt.Clipboard() != "hello" || c.tv.selected {
		t.Errorf("copy or paste with a selection: clipboard %q, still selected %v", tt.Clipboard(), c.tv.selected)
	}
	tt.RightClickAt(x, y)
	expectRead(t, tt, sh, "hello")
	if c.tv.input(ui.InputEvent{Kind: ui.InputPointerDown, Button: 1, X: lx, Y: ly, Mods: ui.Shift, Clicks: 1}, cb) {
		t.Error("Shift+right click taken: no menu")
	}
}

// TestCopyOnSelect: with the setting on, a selection is copied as it is
// made; off, the clipboard is left alone.
func TestCopyOnSelect(t *testing.T) {
	srv, _ := startScript(t, "hello world")
	a, tt := newTestApp(t)
	c := connected(t, a, tt, srv)
	shownText(t, tt, c, "hello world")
	x, y := cellPoint(t, tt, c, 7, 0)
	r, _ := tt.Find("Terminal box")
	lx, ly := x-r.X, y-r.Y
	selectWord := func() string {
		cb := &keptClipboard{}
		c.tv.input(ui.InputEvent{Kind: ui.InputPointerDown, X: lx, Y: ly, Clicks: 2}, cb)
		c.tv.input(ui.InputEvent{Kind: ui.InputPointerUp, X: lx, Y: ly}, cb)
		return cb.text
	}
	if got := selectWord(); got != "" {
		t.Errorf("copied %q with the setting off", got)
	}

	a.openSettings(prefTerminal)
	tt.SetSize(1200, 760)
	tt.Frame()
	scrollToSetting(t, tt, "Copy or paste")
	mustClick(t, tt, "Copy or paste")
	tt.Frame()
	if a.settings.TerminalRightClick != settings.RightClickCopyPaste {
		t.Errorf("chose %q, want copy or paste", a.settings.TerminalRightClick)
	}
	s := a.Settings()
	s.CopyOnSelect = true
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.page = pageWorkspace
	tt.SetSize(1200, 760)
	tt.Frame()
	// The pane takes the settings with its next event.
	tt.Move(x, y)
	tt.Frame()
	if !c.tv.copyOnSelect {
		t.Fatal("the terminal did not take the setting")
	}
	if got := selectWord(); got != "world" {
		t.Errorf("copied %q, want world", got)
	}
}

// TestOpenLink: ⌘-click (Ctrl-click off macOS) on a web address opens it
// in the browser; a plain click, or one off the address, does not.
func TestOpenLink(t *testing.T) {
	srv, _ := startScript(t, "docs https://example.com/x?a=1. end")
	a, tt := newTestApp(t)
	c := connected(t, a, tt, srv)
	shownText(t, tt, c, "end")
	x, y := cellPoint(t, tt, c, 12, 0)
	tt.ClickAt(x, y)
	tt.Frame()
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Fatalf("a plain click opened %q", got)
	}
	ox, _ := cellPoint(t, tt, c, 1, 0)
	tt.ClickAtWith(linkMods(), ox, y)
	tt.Frame()
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Fatalf("a click off the link opened %q", got)
	}
	tt.ClickAtWith(linkMods(), x, y)
	tt.Frame()
	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/x?a=1" {
		t.Fatalf("opened %q", got)
	}
}
