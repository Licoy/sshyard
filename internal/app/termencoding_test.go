package app

import (
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func TestOutputEncodingToolbar(t *testing.T) {
	srv, sh := startScript(t, "ready")
	a, tt := newTestApp(t)
	m := locales.In("en")
	cn := connected(t, a, tt, srv)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingUTF8()))
	mustClick(t, tt, m.OutputEncodingGBKLabel())
	if cn.s.Term.OutputEncoding() != vt.EncodingGBK {
		t.Fatal("GBK choice was not applied")
	}
	// Selection returns focus to the terminal, with input still UTF-8.
	sh.take()
	tt.Type("中文")
	expectRead(t, tt, sh, "中文")
	tt.SetClipboard("粘贴")
	tt.Command("paste")
	expectRead(t, tt, sh, "粘贴")
	snippet, err := a.saveSnippet(assets.Snippet{Name: "snippet"}, "片段")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.sendSnippet(snippet.ID, false); err != nil {
		t.Fatal(err)
	}
	expectRead(t, tt, sh, "片段")
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingGBK()))
	mustClick(t, tt, m.OutputEncodingUTF8())
	if cn.s.Term.OutputEncoding() != vt.EncodingUTF8 {
		t.Fatal("UTF-8 choice was not applied")
	}
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingUTF8()))
	mustClick(t, tt, "Settings")
	if a.menu.key != "" {
		t.Fatal("encoding menu remained open in settings")
	}
	mustClick(t, tt, "Close Settings")
	if a.menu.key != "" {
		t.Fatal("encoding menu reopened after settings")
	}
}

// TestOutputEncodingMenuChoices: the menu lists every encoding in order,
// the keyboard chooses across a separator, and a choice reached by
// scrolling applies with a single checkmark.
func TestOutputEncodingMenuChoices(t *testing.T) {
	a := &App{winState: &winState{menu: dropMenu{focus: 0, sub: -1}}}
	cn := &conn{id: 1, s: &session.Session{Term: vt.New(80, 24)}}
	m := locales.In("en")
	open := true
	var menu ui.Rect
	var entries []menuEntry
	tt := ui.NewTester(func(c *ui.Context) {
		anchor := ui.Box(c).Absolute().Left(700).Top(550).Size(80, 24)
		ui.PopoverBase(c, anchor, &open, func(panel ui.Element) {
			panel.Margin(4, 0, 0, 0)
			entries = a.outputEncodingEntries(c, cn, m)
			a.menuPanel(c, currentTokens(c), panel, entries, 0, menuLayout{compact: true, maxHeight: menuHeightAt(c, anchor.Bounds())})
			menu = panel.Bounds()
		})
	}, 900, 600)
	tt.SetLocale("en")
	tt.Frame()
	encodings := vt.OutputEncodings()
	var labels []string
	for _, e := range entries {
		if !e.Sep {
			labels = append(labels, e.Label)
		}
	}
	if len(labels) != 20 || len(entries) != 27 {
		t.Fatalf("choices/separators: %d/%d", len(labels), len(entries)-len(labels))
	}
	for i, encoding := range encodings {
		if labels[i] != outputEncodingLabel(m, encoding) {
			t.Fatalf("choice %d: %q, want %s", i, labels[i], encoding)
		}
	}
	tt.Key(0, ui.KeyHome)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if cn.s.Term.OutputEncoding() != vt.EncodingGB2312 || a.focusReq != cn.id || a.menu.key != "" {
		t.Fatal("keyboard did not choose GB2312 across separator")
	}
	// Reopen the real full menu and scroll to its last choice.
	a.menu.focus = 0
	tt.Frame()
	tt.Move(menu.X+menu.W/2, menu.Y+menu.H/2)
	tt.Scroll(menu.X+menu.W/2, menu.Y+menu.H/2, 0, 1200)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingCP866())
	tt.Frame()
	if cn.s.Term.OutputEncoding() != vt.EncodingCP866 || a.focusReq != cn.id {
		t.Fatal("scrolled CP866 choice was not applied")
	}
	checked := 0
	for _, e := range entries {
		if e.Checked {
			checked++
			if e.Label != m.OutputEncodingCP866() {
				t.Fatalf("wrong checked encoding: %q", e.Label)
			}
		}
	}
	if checked != 1 {
		t.Fatalf("checked choices: %d, want 1", checked)
	}
}

func TestOutputEncodingPaneScope(t *testing.T) {
	srv, _ := startScript(t, "ready")
	a, tt := newTestApp(t)
	first := connected(t, a, tt, srv)
	a.Connect(first.host.ID, openAt{pane: first.id, edge: layout.Right})
	second := a.focused()
	waitFor(t, tt, "second terminal", func() bool { return second.s.Status().State == session.Connected })
	tt.Frame()
	m := locales.In("en")
	if first.s.Term.OutputEncoding() != vt.EncodingUTF8 || second.s.Term.OutputEncoding() != vt.EncodingUTF8 {
		t.Fatal("new terminal not UTF-8")
	}
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingUTF8()))
	key := a.menu.key
	// Capture a menu callback while it belongs to the second pane, then change focus.
	var entries []menuEntry
	capture := ui.NewTester(func(c *ui.Context) { entries = a.outputEncodingEntries(c, second, m) }, 1, 1)
	capture.Frame()
	gbkIndex := slices.IndexFunc(entries, func(e menuEntry) bool { return e.Label == m.OutputEncodingGBKLabel() })
	if gbkIndex < 0 {
		t.Fatal("GBK missing from full menu")
	}
	if !entries[0].Checked || entries[gbkIndex].Checked {
		t.Fatal("default check is not UTF-8")
	}
	a.ws.Activate(first.id)
	a.focusReq = first.id
	tt.Frame()
	if a.menu.key != "" {
		t.Fatal("second pane menu leaked to first pane")
	}
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingUTF8()))
	if a.menu.key == key {
		t.Fatal("menu key shared across panes")
	}
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingUTF8()))
	entries[gbkIndex].Do()
	capture.Frame()
	if entries[0].Checked || !entries[gbkIndex].Checked {
		t.Fatal("GBK check did not follow selection")
	}
	if first.s.Term.OutputEncoding() != vt.EncodingUTF8 || second.s.Term.OutputEncoding() != vt.EncodingGBK || a.focusReq != second.id {
		t.Fatal("callback did not retain its pane")
	}
	cp866 := slices.IndexFunc(entries, func(e menuEntry) bool { return e.Label == m.OutputEncodingCP866() })
	if cp866 < 0 {
		t.Fatal("CP866 missing from full menu")
	}
	entries[cp866].Do()
	capture.Frame()
	if !entries[cp866].Checked || entries[gbkIndex].Checked || entries[0].Checked ||
		first.s.Term.OutputEncoding() != vt.EncodingUTF8 || second.s.Term.OutputEncoding() != vt.EncodingCP866 || a.focusReq != second.id {
		t.Fatal("CP866 choice or checkmark leaked across panes")
	}
	// A later terminal starts at UTF-8 even while another pane uses CP866.
	a.Connect(first.host.ID, openAt{})
	fresh := a.focused()
	waitFor(t, tt, "fresh terminal", func() bool { return fresh.s.Status().State == session.Connected })
	if fresh.s.Term.OutputEncoding() != vt.EncodingUTF8 {
		t.Fatal("new terminal inherited another pane's encoding")
	}
	a.CloseSession(fresh.id)
	a.ws.Activate(second.id)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingTip("CP866"))
	if a.menu.key != key {
		t.Fatal("menu key changed for the same pane")
	}
	// Switching panes by pointer dismisses the previous pane's menu.
	if a.menu.key == "" {
		t.Fatal("encoding menu did not open")
	}
	left := mustFind(t, tt, "Terminal box")
	tt.ClickAt(left.X+20, left.Y+20)
	tt.Frame()
	if a.menu.key != "" {
		t.Fatal("previous pane menu remained open")
	}
	if a.focused() != first {
		t.Fatal("click did not focus the first pane")
	}
	if _, err := a.OpenAI(first.host.ID, openAt{pane: first.id, edge: layout.Bottom}, first.id); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.ws.Activate(first.id)
	a.focusReq = first.id
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingTip(m.OutputEncodingUTF8()))
	mustClick(t, tt, "Open Files on the Right")
	tt.Frame()
	if a.focused().kind != layout.Files || a.menu.key != "" {
		t.Fatal("encoding menu stayed open on Files")
	}
}

func TestOutputEncodingLongMenu(t *testing.T) {
	srv, sh := startScript(t, "ready")
	a, tt := newTestApp(t)
	settings := a.Settings()
	settings.AutoReconnect = false
	if err := a.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	cn := connected(t, a, tt, srv)
	m := locales.In("en")
	tt.SetSize(900, 600)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingTip("UTF-8"))
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.menu.key != "" {
		t.Fatal("Escape did not close encoding menu")
	}
	mustClick(t, tt, m.OutputEncodingTip("UTF-8"))
	r := mustFind(t, tt, m.OutputEncodingGBKLabel())
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Wheel(r.X+r.W/2, r.Y+r.H/2, 0, 1200)
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingCP866())
	if cn.s.Term.OutputEncoding() != vt.EncodingCP866 || a.menu.key != "" {
		t.Fatal("last choice did not apply and dismiss menu")
	}
	sh.take()
	tt.Type("中文")
	expectRead(t, tt, sh, "中文")
	srv.Drop()
	waitFor(t, tt, "disconnected", func() bool { return cn.s.Status().State == session.Disconnected })
	cn.s.Reconnect()
	waitFor(t, tt, "reconnected", func() bool {
		st := cn.s.Status()
		return st.State == session.Connected && st.Connects >= 2
	})
	tt.Frame()
	if cn.s.Term.OutputEncoding() != vt.EncodingCP866 {
		t.Fatal("reconnect lost output encoding")
	}
	mustClick(t, tt, m.OutputEncodingTip("CP866"))
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	tt.Key(0, ui.KeyHome)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if cn.s.Term.OutputEncoding() != vt.EncodingUTF8 || a.menu.key != "" {
		t.Fatal("keyboard did not restore UTF-8")
	}
}

func TestHostEncodingEditor(t *testing.T) {
	srv, _ := startScript(t, "ready")
	a, tt := newTestApp(t)
	tt.SetSize(1200, 2000)
	m := locales.In("en")
	h := addHost(t, a, srv, "box", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	a.openEditor(&h)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingUTF8())
	mustClick(t, tt, m.OutputEncodingGBKLabel())
	tt.Frame()
	if a.form == nil || a.form.encoding != vt.EncodingGBK {
		t.Fatalf("form encoding %v", a.form)
	}
	mustClick(t, tt, m.Save())
	tt.Frame()
	if got, _ := a.Store().Get(h.ID); got.Encoding != string(vt.EncodingGBK) {
		t.Fatalf("saved %q", got.Encoding)
	}
	h, _ = a.Store().Get(h.ID)
	a.openEditor(&h)
	tt.Frame()
	if a.form.encoding != vt.EncodingGBK {
		t.Fatalf("reopened %q", a.form.encoding)
	}
	a.form, a.editing = nil, false

	a.Connect(h.ID, openAt{})
	cn := a.focused()
	waitFor(t, tt, "connected", func() bool { return cn.s.Status().State == session.Connected })
	if cn.s.Term.OutputEncoding() != vt.EncodingGBK {
		t.Fatalf("new terminal encoding %q", cn.s.Term.OutputEncoding())
	}

	// Back to UTF-8 is saved as the default, and new terminals follow it.
	a.openEditor(&h)
	tt.Frame()
	mustClick(t, tt, m.OutputEncodingGBKLabel())
	mustClick(t, tt, m.OutputEncodingUTF8())
	mustClick(t, tt, m.Save())
	tt.Frame()
	if got, _ := a.Store().Get(h.ID); got.Encoding != "" {
		t.Fatalf("UTF-8 saved as %q", got.Encoding)
	}
	if cn.s.Term.OutputEncoding() != vt.EncodingGBK {
		t.Fatal("saving changed an open terminal")
	}
}

func TestHostEncodingUnknown(t *testing.T) {
	srv, _ := startScript(t, "ready")
	a, tt := newTestApp(t)
	h := addHost(t, a, srv, "box", "secret")
	h.Encoding = "EBCDIC"
	h, err := a.Store().Update(h)
	if err != nil {
		t.Fatal(err)
	}
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	a.openEditor(&h)
	tt.Frame()
	if a.form.encoding != vt.EncodingUTF8 {
		t.Fatalf("form encoding %q", a.form.encoding)
	}
	a.form, a.editing = nil, false
	a.Connect(h.ID, openAt{})
	cn := a.focused()
	waitFor(t, tt, "connected", func() bool { return cn.s.Status().State == session.Connected })
	if cn.s.Term.OutputEncoding() != vt.EncodingUTF8 {
		t.Fatalf("terminal encoding %q", cn.s.Term.OutputEncoding())
	}
}
