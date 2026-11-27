package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func disableAutoReconnect(t *testing.T, a *App) {
	t.Helper()
	st := a.Settings()
	st.AutoReconnect = false
	if err := a.ApplySettings(st); err != nil {
		t.Fatal(err)
	}
}

// TestReconnectEnterOnDisconnectedTerminal verifies that pressing Enter on a
// focused disconnected terminal pane reconnects it, while modifiers, IME
// preedit, and live sessions are left untouched.
func TestReconnectEnterOnDisconnectedTerminal(t *testing.T) {
	srv, sh := startScript(t, "ready\r\n")
	a, tt := newTestApp(t)
	disableAutoReconnect(t, a)
	cn := connected(t, a, tt, srv)
	waitFor(t, tt, "initial output", func() bool { return strings.Contains(cn.s.Term.ScreenText(), "ready") })

	// While connected, Enter goes to the shell and does not reconnect.
	sh.take()
	if a.canReconnect(cn) {
		t.Fatal("canReconnect must be false while connected")
	}
	if a.disconnectedTermKey(cn, ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}) {
		t.Fatal("disconnectedTermKey must ignore Enter while connected")
	}
	tt.Key(0, ui.KeyEnter)
	expectRead(t, tt, sh, "\r")
	if got := cn.s.Status().Connects; got != 1 {
		t.Fatalf("connected Enter reconnected: connects=%d", got)
	}

	// Disconnect the session.
	srv.Drop()
	waitFor(t, tt, "disconnected", func() bool { return cn.s.Status().State == session.Disconnected })
	if !a.canReconnect(cn) {
		t.Fatal("canReconnect must be true while disconnected")
	}

	// Modifiers and IME preedit must not trigger reconnect.
	if a.disconnectedTermKey(cn, ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter, Mods: ui.Shift}) {
		t.Fatal("Shift+Enter must not trigger disconnected reconnect")
	}
	cn.tv.preedit = "zhong"
	if a.disconnectedTermKey(cn, ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}) {
		t.Fatal("Enter during IME composition must not trigger reconnect")
	}
	cn.tv.preedit = ""
	if cn.s.Status().State != session.Disconnected {
		t.Fatalf("unexpected state after ignored keys: %v", cn.s.Status().State)
	}

	// Plain Enter on the focused disconnected terminal reconnects.
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "reconnected via Enter", func() bool {
		st := cn.s.Status()
		return st.State == session.Connected && st.Connects >= 2
	})
}

// TestReconnectEnterDoesNotBroadcast ensures pressing Enter on a disconnected
// terminal pane while live broadcast is enabled reconnects that pane without
// forwarding Enter to other connected broadcast targets.
func TestReconnectEnterDoesNotBroadcast(t *testing.T) {
	srv1, _ := startScript(t, "s1\r\n")
	srv2, sh2 := startScript(t, "s2\r\n")
	a, tt := newTestApp(t)
	disableAutoReconnect(t, a)

	h1 := addHost(t, a, srv1, "srv1", "secret")
	h2 := addHost(t, a, srv2, "srv2", "secret")
	a.known.Add(srv1.Addr, srv1.HostKey.PublicKey())
	a.known.Add(srv2.Addr, srv2.HostKey.PublicKey())

	a.Connect(h1.ID, openAt{})
	c1 := a.focused()
	waitFor(t, tt, "srv1 connected", func() bool { return c1.s.Status().State == session.Connected })
	a.Connect(h2.ID, openAt{pane: a.paneNode(c1.id), edge: layout.Right})
	c2 := a.focused()
	waitFor(t, tt, "srv2 connected", func() bool { return c2.s.Status().State == session.Connected })

	a.ToggleTool(layout.Broadcast)
	a.bc.live = true
	a.ws.Activate(c1.id)
	a.focusReq = c1.id
	tt.Frame()
	sh2.take()

	srv1.Drop()
	waitFor(t, tt, "srv1 disconnected", func() bool { return c1.s.Status().State == session.Disconnected })
	tt.Frame()

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "srv1 reconnected", func() bool {
		st := c1.s.Status()
		return st.State == session.Connected && st.Connects >= 2
	})
	if got := sh2.take(); got != "" {
		t.Fatalf("broadcast leaked %q to connected peer when reconnecting with Enter", got)
	}
}

// TestReconnectShortcutAction verifies that the global actReconnect shortcut
// is registered as an app shortcut, reconnects disconnected terminal and files
// panes, and is a no-op while connected.
func TestReconnectShortcutAction(t *testing.T) {
	if !isAppShortcut(nil, ui.KeyR, appMods) {
		t.Fatal("default reconnect shortcut must be recognized as an app shortcut")
	}
	srv, _ := startScript(t, "ok\r\n")
	a, tt := newTestApp(t)
	disableAutoReconnect(t, a)
	cn := connected(t, a, tt, srv)

	// No-op while connected.
	tt.Key(appMods, ui.KeyR)
	tt.Frame()
	if got := cn.s.Status().Connects; got != 1 {
		t.Fatalf("shortcut reconnected live terminal: connects=%d", got)
	}

	// Reconnects disconnected terminal via shortcut.
	srv.Drop()
	waitFor(t, tt, "terminal disconnected", func() bool { return cn.s.Status().State == session.Disconnected })
	tt.Key(appMods, ui.KeyR)
	waitFor(t, tt, "terminal reconnected via shortcut", func() bool {
		st := cn.s.Status()
		return st.State == session.Connected && st.Connects >= 2
	})

	// Also works on a disconnected files pane.
	a.OpenFiles(cn.host.ID, openAt{pane: a.paneNode(cn.id), edge: layout.Right}, cn)
	fc := a.focused()
	if fc == nil || fc.kind != layout.Files {
		t.Fatalf("expected focused files pane, got %+v", fc)
	}
	waitFor(t, tt, "files connected", func() bool { return fc.s.Status().State == session.Connected })
	srv.Drop()
	waitFor(t, tt, "files disconnected", func() bool { return fc.s.Status().State == session.Disconnected })
	if a.disconnectedTermKey(fc, ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}) {
		t.Fatal("disconnectedTermKey must not handle files panes")
	}
	a.runAction(actReconnect)
	waitFor(t, tt, "files reconnected via action", func() bool {
		st := fc.s.Status()
		return st.State == session.Connected && st.Connects >= 2
	})
}

// TestReconnectSecurityGates checks that quick reconnect refuses to bypass
// changed host-key review and respects the locked secret store.
func TestReconnectSecurityGates(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	a, tt := newTestApp(t)
	h := addHost(t, a, srv, "vps", "secret")
	a.known.Add(srv.Addr, sshtest.NewHostKey().PublicKey())
	a.Connect(h.ID, openAt{})
	c := a.focused()
	waitFor(t, tt, "host key changed failure", func() bool {
		return c.s.Status().State == session.Failed && session.Classify(c.s.Status().Err) == session.KindHostChanged
	})

	if a.canReconnect(c) {
		t.Fatal("canReconnect must be false for unreviewed changed host key")
	}
	if a.reconnectPane(c) {
		t.Fatal("reconnectPane must refuse unreviewed changed host key")
	}
	if a.disconnectedTermKey(c, ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter}) {
		t.Fatal("Enter must not bypass changed host key review")
	}

	// Waiting-secrets pane must unlock before starting, and must not trigger
	// while an unlock is already in flight.
	dir := t.TempDir()
	lockedApp, err := New(Platform{DataDir: dir, Secrets: keychain.NewMemory(), HomeDir: dir, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// This test marks an unlock busy without starting its cleanup worker:
		// Close would leave the store, and on Windows its lock file, open.
		lockedApp.unlock.mu.Lock()
		lockedApp.unlock.busy = false
		lockedApp.unlock.mu.Unlock()
		lockedApp.Close()
	})
	waitConn := &conn{
		id:             1,
		kind:           layout.Terminal,
		host:           assets.Host{ID: "h1", Name: "locked", Address: "127.0.0.1", Port: 22, User: "demo", Auth: assets.AuthPassword},
		s:              session.New(session.Config{}, session.Options{}, 80, 24),
		waitingSecrets: true,
	}
	if !lockedApp.canReconnect(waitConn) {
		t.Fatal("waitingSecrets pane should be actionable when unlock is idle")
	}
	lockedApp.unlock.busy = true
	if lockedApp.canReconnect(waitConn) {
		t.Fatal("waitingSecrets pane must not be actionable while unlock is busy")
	}
}

// TestReconnectBannerSaveOutput verifies that the disconnected terminal banner
// exposes Save Terminal Output and writes the terminal buffer via SaveFile.
func TestReconnectBannerSaveOutput(t *testing.T) {
	srv, _ := startScript(t, "banner-line-1\r\nbanner-line-2\r\n")
	a, tt := newTestApp(t)
	disableAutoReconnect(t, a)
	a.setLogDir(filepath.Join(t.TempDir(), "logs"))
	cn := connected(t, a, tt, srv)
	waitFor(t, tt, "output", func() bool { return strings.Contains(cn.s.Term.ScreenText(), "banner-line-2") })

	dest := filepath.Join(t.TempDir(), "saved.txt")
	a.p.SaveFile = func(title, path string) (string, error) {
		return dest, nil
	}

	srv.Drop()
	waitFor(t, tt, "disconnected", func() bool { return cn.s.Status().State == session.Disconnected })
	tt.Frame()

	m := locales.In("en")
	mustClick(t, tt, m.LogSaveOutput())
	waitFor(t, tt, "saved file", func() bool {
		b, _ := os.ReadFile(dest)
		return strings.Contains(string(b), "banner-line-2")
	})
}
