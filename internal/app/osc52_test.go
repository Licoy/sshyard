package app

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func TestClipGate(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	g := clipGate{now: func() time.Time { return now }}
	e := g.incoming(1, settings.OSC52Allow, "a", false, false)
	if !e.Write || !e.Fresh {
		t.Fatalf("first %+v", e)
	}
	g.noteUndo(1, "old")
	now = now.Add(500 * time.Millisecond)
	e = g.incoming(1, settings.OSC52Allow, "bbbb", false, false)
	if !e.Write || e.Fresh {
		t.Fatalf("merge %+v", e)
	}
	if s, _, ok := g.takeUndo(1); !ok || s != "old" {
		t.Fatalf("undo %q %v", s, ok)
	}
	now = now.Add(time.Second)
	e = g.incoming(1, settings.OSC52Allow, "c", false, false)
	if !e.Write || !e.Fresh {
		t.Fatalf("new burst %+v", e)
	}
	g.noteUndo(1, "mid")
	now = now.Add(30 * time.Second)
	if _, _, ok := g.takeUndo(1); ok {
		t.Fatal("undo lived 30s")
	}

	g2 := clipGate{now: func() time.Time { return now }}
	for i := 0; i < 10; i++ {
		if e := g2.incoming(1, settings.OSC52Allow, "x", false, false); !e.Write {
			t.Fatalf("write %d %+v", i, e)
		}
	}
	if e := g2.incoming(1, settings.OSC52Allow, "no", false, false); !e.Pause || e.Write {
		t.Fatalf("11th %+v", e)
	}
	if e := g2.incoming(1, settings.OSC52Allow, "no", false, false); e != (clipEffect{}) {
		t.Fatalf("quiet %+v", e)
	}
	g2.clearPauseTold(1)
	if e := g2.incoming(1, settings.OSC52Allow, "no", false, false); !e.Pause {
		t.Fatalf("told again %+v", e)
	}
	if e := g2.incoming(2, settings.OSC52Allow, "other", false, false); !e.Write {
		t.Fatalf("other pane %+v", e)
	}
	g2.resume(1)
	if e := g2.incoming(1, settings.OSC52Allow, "back", false, false); !e.Write {
		t.Fatalf("resumed %+v", e)
	}

	g3 := clipGate{now: func() time.Time { return now }}
	if e := g3.incoming(1, settings.OSC52Deny, "", true, false); !e.Query {
		t.Fatal("query")
	}
	if e := g3.incoming(1, settings.OSC52Deny, "", true, false); e != (clipEffect{}) {
		t.Fatalf("query again %+v", e)
	}
	now = now.Add(time.Minute)
	if e := g3.incoming(1, settings.OSC52Deny, "", true, false); !e.Query {
		t.Fatal("query after a minute")
	}
	if e := g3.incoming(1, settings.OSC52Deny, "x", false, false); e != (clipEffect{}) {
		t.Fatalf("deny %+v", e)
	}
	if e := g3.incoming(1, settings.OSC52Allow, "", false, false); e != (clipEffect{}) {
		t.Fatalf("empty %+v", e)
	}
	if e := g3.incoming(1, settings.OSC52Deny, "", false, true); e != (clipEffect{}) {
		t.Fatalf("deny drop %+v", e)
	}
	if e := g3.incoming(1, settings.OSC52Allow, "", false, true); !e.Limit {
		t.Fatalf("drop %+v", e)
	}

	g4 := clipGate{now: func() time.Time { return now }}
	if e := g4.incoming(1, settings.OSC52Ask, "hi", false, false); !e.Ask || e.Write {
		t.Fatalf("ask %+v", e)
	}
	if e := g4.allow(1, false); !e.Write {
		t.Fatalf("once %+v", e)
	}
	if e := g4.incoming(1, settings.OSC52Ask, "again", false, false); !e.Ask {
		t.Fatalf("asks again %+v", e)
	}
	if e := g4.allow(1, true); !e.Write {
		t.Fatalf("session %+v", e)
	}
	if e := g4.incoming(1, settings.OSC52Ask, "go", false, false); !e.Write || e.Ask {
		t.Fatalf("session write %+v", e)
	}
	if e := g4.incoming(2, settings.OSC52Ask, "other", false, false); !e.Ask {
		t.Fatalf("other pane still asks %+v", e)
	}
	g4.forget(1)
	if e := g4.incoming(1, settings.OSC52Ask, "fresh", false, false); !e.Ask {
		t.Fatalf("forgotten pane asks %+v", e)
	}

	g5 := clipGate{now: func() time.Time { return now }}
	for i := 0; i < 12; i++ {
		if e := g5.incoming(1, settings.OSC52Ask, "q", false, false); !e.Ask {
			t.Fatalf("ask %d %+v", i, e)
		}
	}
	if e := g5.allow(1, false); !e.Write {
		t.Fatalf("asks counted as writes: %+v", e)
	}
}

func TestClipPreview(t *testing.T) {
	if clipPreview("a\nb") != `a\nb` {
		t.Fatal(clipPreview("a\nb"))
	}
	long := strings.Repeat("x", 201)
	if got := clipPreview(long); got != strings.Repeat("x", 200)+"…" {
		t.Fatalf("%q", got)
	}
}

func connectOSC(t *testing.T, a *App, name string) *conn {
	t.Helper()
	h, err := a.Store().Add(assets.Host{Name: name, Address: "127.0.0.1", Port: 1, User: "u", Auth: assets.AuthAsk})
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	c := a.conns[id]
	if c == nil || c.s == nil || c.s.Term == nil {
		t.Fatal("no terminal")
	}
	return c
}

func writeOSC(t *testing.T, c *conn, text string) {
	t.Helper()
	b := []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a")
	if _, err := c.s.Term.Write(b); err != nil {
		t.Fatal(err)
	}
}

func clipNotStored(t *testing.T, a *App, secret string) {
	t.Helper()
	for _, name := range []string{"hosts.json", "settings.json"} {
		b, err := os.ReadFile(filepath.Join(a.p.DataDir, name))
		if err != nil {
			continue
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("%s holds the clipboard text", name)
		}
	}
}

func TestOSC52WriteToast(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Allow
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	writeOSC(t, c, "PAYLOAD-a")
	writeOSC(t, c, "PAYLOAD-bbbb")
	tt.Frame()
	if tt.Clipboard() != "PAYLOAD-bbbb" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	if tt.HasText("PAYLOAD-bbbb") {
		t.Fatalf("showed the clipboard text: %q", tt.Texts())
	}
	clipNotStored(t, a, "PAYLOAD-bbbb")
	mustClick(t, tt, "Undo")
	if tt.Clipboard() != "OLD" {
		t.Fatalf("undo %q", tt.Clipboard())
	}
}

func TestOSC52AskPaneLabel(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Ask
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	m := locales.In("en")
	// asked is the ask's origin, as its dialog names it.
	asked := func() string {
		if a.clipAsk == nil || !a.clipAsk.open {
			return ""
		}
		return m.Osc52AskFrom(a.clipAsk.host, a.oscPaneName(m, a.clipAsk.pane))
	}
	c1 := connectOSC(t, a, "box")
	if _, err := c1.s.Term.Write([]byte("\x1b]0;EVILTITLE\a")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenFiles(c1.host.ID, openAt{pane: c1.id, edge: layout.Right}, c1); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	writeOSC(t, c1, "one")
	tt.Frame()
	if asked() != "box · Terminal" {
		t.Fatalf("single pane: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()

	id2, err := a.Connect(c1.host.ID, openAt{pane: c1.id, edge: layout.Right})
	if err != nil {
		t.Fatal(err)
	}
	c2 := a.conns[id2]
	writeOSC(t, c2, "two")
	tt.Frame()
	if asked() != "box · Terminal 2" {
		t.Fatalf("second pane: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()
	writeOSC(t, c1, "one-b")
	tt.Frame()
	if asked() != "box · Terminal 1" {
		t.Fatalf("first pane: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()

	id3, err := a.Connect(c1.host.ID, openAt{pane: id2, edge: layout.Right})
	if err != nil {
		t.Fatal(err)
	}
	c3 := a.conns[id3]
	a.CloseSession(c1.id)
	tt.Frame()
	writeOSC(t, c2, "two-b")
	tt.Frame()
	if asked() != "box · Terminal 2" {
		t.Fatalf("after pane 1 closed: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()
	writeOSC(t, c3, "three")
	tt.Frame()
	if asked() != "box · Terminal 3" {
		t.Fatalf("pane 3: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()

	id4, err := a.Connect(c1.host.ID, openAt{pane: id3, edge: layout.Right})
	if err != nil {
		t.Fatal(err)
	}
	writeOSC(t, a.conns[id4], "four")
	tt.Frame()
	if asked() != "box · Terminal 4" {
		t.Fatalf("freed number reused: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()

	// Alone, the number is hidden. It is still 2, so the next pane is 5.
	a.CloseSession(id3)
	a.CloseSession(id4)
	tt.Frame()
	writeOSC(t, c2, "alone")
	tt.Frame()
	if asked() != "box · Terminal" {
		t.Fatalf("only pane left: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()
	id5, err := a.Connect(c1.host.ID, openAt{pane: id2, edge: layout.Right})
	if err != nil {
		t.Fatal(err)
	}
	writeOSC(t, c2, "back")
	tt.Frame()
	if asked() != "box · Terminal 2" {
		t.Fatalf("number was not kept: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()
	writeOSC(t, a.conns[id5], "five")
	tt.Frame()
	if asked() != "box · Terminal 5" {
		t.Fatalf("number reused: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()

	other := connectOSC(t, a, "other")
	writeOSC(t, other, "solo")
	tt.Frame()
	if asked() != "other · Terminal" {
		t.Fatalf("other host: %q", asked())
	}
	mustClick(t, tt, "Deny")
	tt.Frame()

	a.CloseSession(id2)
	a.CloseSession(id5)
	tt.Frame()
	fresh, err := a.Connect(c1.host.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	writeOSC(t, a.conns[fresh], "again")
	tt.Frame()
	if asked() != "box · Terminal" {
		t.Fatalf("after the last pane closed: %q", asked())
	}
}

// A remote title is not a pane name. OSC 0 and OSC 2 both land on the
// terminal, and the ask still shows only the local number.
func TestOSC52AskIgnoresRemoteTitle(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Ask
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c1 := connectOSC(t, a, "box")
	id2, err := a.Connect(c1.host.ID, openAt{pane: c1.id, edge: layout.Right})
	if err != nil {
		t.Fatal(err)
	}
	c2 := a.conns[id2]
	if _, err := c2.s.Term.Write([]byte("\x1b]0;EVIL-TITLE\a\x1b]2;OTHER-TITLE\x1b\\")); err != nil {
		t.Fatal(err)
	}
	if c2.s.Term.Title() != "OTHER-TITLE" {
		t.Fatalf("title %q", c2.s.Term.Title())
	}
	writeOSC(t, c2, "two")
	tt.Frame()
	m := locales.In("en")
	if a.clipAsk == nil || a.clipAsk.pane != id2 {
		t.Fatalf("ask %+v", a.clipAsk)
	}
	if got := m.Osc52AskFrom(a.clipAsk.host, a.oscPaneName(m, a.clipAsk.pane)); got != "box · Terminal 2" {
		t.Fatalf("ask named a remote title: %q", got)
	}
}

func hasExact(tt *ui.Tester, s string) bool {
	for _, got := range tt.Texts() {
		if got == s {
			return true
		}
	}
	return false
}

func TestOSC52Ask(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Ask
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	writeOSC(t, c, "hello-ask")
	tt.Frame()
	if tt.Clipboard() != "OLD" {
		t.Fatalf("asked write landed: %q", tt.Clipboard())
	}
	if a.clipAsk == nil || a.clipAsk.pane != c.id || a.clipAsk.data != "hello-ask" {
		t.Fatalf("ask %+v", a.clipAsk)
	}
	mustClick(t, tt, "Allow once")
	tt.Frame()
	if tt.Clipboard() != "hello-ask" {
		t.Fatalf("allow once: %q", tt.Clipboard())
	}
	writeOSC(t, c, "second")
	tt.Frame()
	if a.clipAsk == nil || a.clipAsk.data != "second" {
		t.Fatal("second write did not ask")
	}
	mustClick(t, tt, "Always allow this session")
	tt.Frame()
	if tt.Clipboard() != "second" {
		t.Fatalf("session: %q", tt.Clipboard())
	}
	writeOSC(t, c, "third")
	tt.Frame()
	if a.clipAsk != nil {
		t.Fatal("session still asks")
	}
	if tt.Clipboard() != "third" {
		t.Fatalf("third %q", tt.Clipboard())
	}
}

func TestOSC52AskWithAIPane(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Ask
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c := connectOSC(t, a, "box")
	tab := a.ws.ActiveTab()
	if _, err := a.OpenAI(c.host.ID, openAt{pane: tab.Focus, edge: layout.Right}, c.id); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var ai *conn
	for _, cn := range a.conns {
		if cn.kind == layout.AI {
			ai = cn
		}
	}
	if ai == nil || ai.s != nil {
		t.Fatal("AI pane should have no session")
	}
	tt.SetClipboard("OLD")
	writeOSC(t, c, "hello-ask")
	tt.Frame()
	if tt.Clipboard() != "OLD" {
		t.Fatalf("asked write landed: %q", tt.Clipboard())
	}
	if a.clipAsk == nil || !a.clipAsk.open || a.clipAsk.pane != c.id {
		t.Fatalf("ask %+v", a.clipAsk)
	}
	if ai.s != nil {
		t.Fatal("ask walk attached a session to the AI pane")
	}
}

func TestOSC52Pause(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Allow
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	for i := 0; i < 11; i++ {
		writeOSC(t, c, fmt.Sprintf("p%02d", i))
	}
	tt.Frame()
	if tt.Clipboard() != "p09" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	mustClick(t, tt, "Resume")
	writeOSC(t, c, "after")
	tt.Frame()
	if tt.Clipboard() != "after" {
		t.Fatalf("resumed %q", tt.Clipboard())
	}
}

func TestOSC52WriteQuiet(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write, s.OSC52Notify = settings.OSC52Allow, false
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	writeOSC(t, c, "PAYLOAD-q")
	tt.Frame()
	if tt.Clipboard() != "PAYLOAD-q" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	if tt.HasText("wrote to the clipboard") || tt.HasText("Undo") {
		t.Fatalf("notice shown: %q", tt.Texts())
	}
	if _, _, ok := a.clip.takeUndo(c.id); ok {
		t.Fatal("kept an undo snapshot without a notice")
	}
}

func TestOSC52Settings(t *testing.T) {
	a, tt := newTestApp(t)
	a.openSettings(prefTerminal)
	tt.Frame()
	scrollToSetting(t, tt, "Deny")
	mustClick(t, tt, "Deny")
	if a.Settings().OSC52Write != settings.OSC52Deny {
		t.Fatalf("policy %q", a.Settings().OSC52Write)
	}
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	writeOSC(t, c, "nope")
	tt.Frame()
	if tt.Clipboard() != "OLD" {
		t.Fatalf("deny wrote %q", tt.Clipboard())
	}
}

func TestOSC52LimitApplies(t *testing.T) {
	a, tt := newTestApp(t)
	c := connectOSC(t, a, "box")
	s := a.Settings()
	s.OSC52MaxBytes = 64 << 10
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	tt.SetClipboard("OLD")
	writeOSC(t, c, strings.Repeat("A", 70<<10))
	tt.Frame()
	if tt.Clipboard() != "OLD" {
		t.Fatal("over-limit paste was written")
	}
	if !tt.HasText("over the limit") {
		t.Fatalf("toast: %q", tt.Texts())
	}
}

func TestOSC52HostOverride(t *testing.T) {
	a, tt := newTestApp(t)
	tt.SetSize(1200, 1400)
	h, err := a.Store().Add(assets.Host{Name: "box", Address: "127.0.0.1", Port: 1, User: "u", Auth: assets.AuthAsk})
	if err != nil {
		t.Fatal(err)
	}
	a.openEditor(&h)
	tt.Frame()
	scrollEditorTo(t, tt, "Advanced")
	mustClick(t, tt, "Advanced")
	tt.Advance(250 * time.Millisecond)
	scrollEditorTo(t, tt, "Follow the global setting")
	mustClick(t, tt, "Follow the global setting")
	tt.Frame()
	mustClick(t, tt, "Deny")
	tt.Frame()
	mustClick(t, tt, "Save")
	got, ok := a.Store().Get(h.ID)
	if !ok || got.OSC52 != settings.OSC52Deny {
		t.Fatalf("saved %+v", got)
	}
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	tt.SetClipboard("OLD")
	writeOSC(t, a.conns[id], "secret-override")
	tt.Frame()
	if tt.Clipboard() != "OLD" {
		t.Fatalf("override wrote %q", tt.Clipboard())
	}
	clipNotStored(t, a, "secret-override")
}

func TestOSC52AllowOverride(t *testing.T) {
	a, tt := newTestApp(t)
	h, err := a.Store().Add(assets.Host{Name: "box", Address: "127.0.0.1", Port: 1, User: "u", Auth: assets.AuthAsk, OSC52: settings.OSC52Allow})
	if err != nil {
		t.Fatal(err)
	}
	s := a.Settings()
	s.OSC52Write = settings.OSC52Deny
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	tt.SetClipboard("OLD")
	writeOSC(t, a.conns[id], "hello")
	tt.Frame()
	if tt.Clipboard() != "hello" {
		t.Fatalf("host allow did not beat global deny: %q", tt.Clipboard())
	}
}

func TestOSC52UndoExpiryAndLocalCopy(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Allow
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	writeOSC(t, c, "hello")
	tt.Frame()
	if tt.Clipboard() != "hello" || !tt.HasText("Undo") {
		t.Fatalf("clipboard %q texts %q", tt.Clipboard(), tt.Texts())
	}
	// A second write inside the second replaces the toast. Undo still
	// ends 30s after the first write, not 30s after this one.
	tt.Advance(500 * time.Millisecond)
	writeOSC(t, c, "hello2")
	tt.Frame()
	if tt.Clipboard() != "hello2" || !tt.HasText("Undo") {
		t.Fatalf("merged clipboard %q texts %q", tt.Clipboard(), tt.Texts())
	}
	tt.Advance(29*time.Second + 600*time.Millisecond)
	if tt.HasText("Undo") {
		t.Fatalf("undo outlived the snapshot: %q", tt.Texts())
	}
	if tt.Clipboard() != "hello2" {
		t.Fatalf("expiry wrote the clipboard: %q", tt.Clipboard())
	}

	tt.SetClipboard("OLD")
	writeOSC(t, c, "hello")
	tt.Frame()
	mustClick(t, tt, "Undo")
	if tt.Clipboard() != "OLD" {
		t.Fatalf("undo %q", tt.Clipboard())
	}

	writeOSC(t, c, "remote")
	tt.Frame()
	tt.SetClipboard("MINE")
	mustClick(t, tt, "Undo")
	if tt.Clipboard() != "MINE" {
		t.Fatalf("undo overwrote a local copy: %q", tt.Clipboard())
	}
	tt.Frame()
	if !tt.HasText("The clipboard changed, so undo is no longer available") {
		t.Fatalf("toast: %q", tt.Texts())
	}
}

func TestOSC52OverLimitKeepsOutput(t *testing.T) {
	a, tt := newTestApp(t)
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	payload := strings.Repeat("A", 2<<20)
	b := []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(payload)) + "\aAFTER-LIMIT\r\n")
	if _, err := c.s.Term.Write(b); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.Clipboard() != "OLD" {
		t.Fatal("over-limit paste was written")
	}
	if !tt.HasText("The clipboard write from box is over the limit and was dropped.") {
		t.Fatalf("toast: %q", tt.Texts())
	}
	screen := c.s.Term.ScreenText()
	if !strings.Contains(screen, "AFTER-LIMIT") || strings.Contains(screen, "QUFB") {
		t.Fatalf("screen %q", screen)
	}
}

func TestOSC52AllowSessionSurvivesReconnect(t *testing.T) {
	srv := startServer(t)
	a, tt := newTestApp(t)
	s := a.Settings()
	s.OSC52Write = settings.OSC52Ask
	s.AutoReconnect = false
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	h := addHost(t, a, srv, "box", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	if _, err := a.Connect(h.ID, openAt{}); err != nil {
		t.Fatal(err)
	}
	c := a.focused()
	waitFor(t, tt, "connected", func() bool { return c.s.Status().State == session.Connected })
	id, term := c.id, c.s.Term
	tt.SetClipboard("OLD")
	writeOSC(t, c, "once")
	tt.Frame()
	mustClick(t, tt, "Always allow this session")
	tt.Frame()
	if tt.Clipboard() != "once" {
		t.Fatalf("session allow: %q", tt.Clipboard())
	}

	srv.Drop()
	waitFor(t, tt, "disconnected", func() bool { return c.s.Status().State == session.Disconnected })
	c.s.Reconnect()
	waitFor(t, tt, "reconnected", func() bool {
		st := c.s.Status()
		return st.State == session.Connected && st.Connects >= 2
	})
	// open() is the only c.s = session.New, and session.New is the only
	// place that builds a Terminal. Reconnect starts that session again.
	if a.conns[id] != c || c.id != id || c.s.Term != term || term.OnClipboard == nil || term.OnClipboardDrop == nil {
		t.Fatalf("reconnect replaced the pane: id %d/%d term %p/%p", id, c.id, term, c.s.Term)
	}
	writeOSC(t, c, "after")
	tt.Frame()
	if a.clipAsk != nil || tt.Clipboard() != "after" {
		t.Fatalf("session allow after reconnect: clipboard %q ask %+v", tt.Clipboard(), a.clipAsk)
	}

	if _, err := a.Connect(h.ID, openAt{}); err != nil {
		t.Fatal(err)
	}
	c2 := a.focused()
	if c2 == nil || c2.id == id {
		t.Fatalf("second pane %+v", c2)
	}
	waitFor(t, tt, "second pane", func() bool { return c2.s.Status().State == session.Connected })
	writeOSC(t, c2, "other")
	tt.Frame()
	if a.clipAsk == nil || a.clipAsk.pane != c2.id || tt.Clipboard() != "after" {
		t.Fatalf("second pane did not ask: clipboard %q ask %+v", tt.Clipboard(), a.clipAsk)
	}
}

func TestOSC52RefusedRead(t *testing.T) {
	a, tt := newTestApp(t)
	c := connectOSC(t, a, "box")
	tt.SetClipboard("OLD")
	if _, err := c.s.Term.Write([]byte("\x1b]52;c;?\a")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("box tried to read the clipboard. Refused.") {
		t.Fatalf("toast: %q", tt.Texts())
	}
	if tt.Clipboard() != "OLD" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
}
