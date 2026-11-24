package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// withShells stands in shells for this computer's, for apps made after.
func withShells(t *testing.T, shells ...session.Shell) {
	t.Helper()
	old := detectShells
	detectShells = func() []session.Shell { return shells }
	t.Cleanup(func() { detectShells = old })
}

// threeShells are shells of names as on Windows, of programs this
// computer runs.
var threeShells = []session.Shell{
	{ID: "pwsh", Name: "PowerShell", Path: "/bin/sh"},
	{ID: "gitbash", Name: "Git Bash", Path: "/bin/sh"},
	{ID: "wsl:Ubuntu", Name: "WSL · Ubuntu", Path: "/bin/sh"},
}

// shellPane is the conn of the pane id.
func shellPane(t *testing.T, a *App, id int, err error) *conn {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	c := a.conns[id]
	if c == nil {
		t.Fatalf("no pane %d", id)
	}
	return c
}

// TestLocalShells: with several shells, the local terminal entry of the
// "+" menu has a submenu of them, the default marked; a pane is named for
// its shell, the palette opens each, and a shell gone or none asked for
// is the default one.
func TestLocalShells(t *testing.T) {
	withShells(t, threeShells...)
	a, tt := newTestApp(t)
	mustClick(t, tt, "Add")
	// The status bar has a "Local terminal" too: the menu's is two rows
	// below quick connect.
	group, quick := mustFind(t, tt, "New group"), mustFind(t, tt, "Quick Connect…")
	tt.Move(quick.X+quick.W/2, quick.Y+quick.H/2+2*(quick.Y-group.Y))
	tt.Frame()
	ps, def := mustFind(t, tt, "PowerShell"), mustFind(t, tt, "Default")
	if def.Y > ps.Y+ps.H || def.Y+def.H < ps.Y {
		t.Errorf("Default at %v, not on PowerShell's row at %v", def, ps)
	}
	mustFind(t, tt, "WSL · Ubuntu")
	mustClick(t, tt, "Git Bash")
	tt.Frame()
	if len(a.conns) != 1 || a.menu.key != "" {
		t.Fatalf("%d panes, menu %q", len(a.conns), a.menu.key)
	}
	for _, c := range a.conns {
		if c.host.ID != localID+"gitbash" || c.host.Name != "Git Bash" {
			t.Fatalf("opened %q named %q", c.host.ID, c.host.Name)
		}
	}
	if !tt.HasText("Git Bash") {
		t.Errorf("the pane is not named for its shell: %q", tt.Texts())
	}

	id, err := a.OpenLocalTerminal()
	if c := shellPane(t, a, id, err); c.host.ID != localID+"pwsh" {
		t.Errorf("the default opened %q, want PowerShell, the first", c.host.ID)
	}
	a.settings.LocalShell = "wsl:Ubuntu"
	id, err = a.OpenLocalTerminal()
	if c := shellPane(t, a, id, err); c.host.ID != localID+"wsl:Ubuntu" || c.host.Name != "WSL · Ubuntu" {
		t.Errorf("the default opened %q named %q, want the chosen WSL", c.host.ID, c.host.Name)
	}
	for _, id := range []string{localID, localID + "gone"} {
		if sh := a.shell(id); sh.ID != "wsl:Ubuntu" {
			t.Errorf("%q runs %q, want the default", id, sh.ID)
		}
	}
	a.settings.LocalShell = "gone"
	if sh := a.shell(localID); sh.ID != "pwsh" {
		t.Errorf("a default gone runs %q, want the first", sh.ID)
	}

	var titles []string
	for _, it := range a.paletteItems(locales.In("en"), theme.LightTokens()) {
		titles = append(titles, it.title)
	}
	for _, want := range []string{"Local terminal", "Local terminal: PowerShell", "Local terminal: Git Bash", "Local terminal: WSL · Ubuntu"} {
		if !slices.Contains(titles, want) {
			t.Errorf("no %q in the palette", want)
		}
	}
}

// TestDefaultShellSetting: the terminal settings choose the default shell
// among several; with one there is nothing to choose, and the local
// terminal entry has no submenu.
func TestDefaultShellSetting(t *testing.T) {
	withShells(t, threeShells...)
	a, tt := newTestApp(t)
	a.openSettings(prefTerminal)
	tt.SetSize(1200, 2600)
	tt.Frame()
	mustFind(t, tt, "What a new local terminal runs. The other shells are in the local terminal menu.")
	mustClick(t, tt, "PowerShell")
	mustClick(t, tt, "Git Bash")
	tt.Frame()
	if a.settings.LocalShell != "gitbash" {
		t.Fatalf("chose %q, want Git Bash", a.settings.LocalShell)
	}

	withShells(t, session.Shell{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"})
	a, tt = newTestApp(t)
	a.openSettings(prefTerminal)
	tt.SetSize(1200, 2600)
	tt.Frame()
	if tt.HasText("Default shell") {
		t.Error("a default shell to choose among one")
	}
	if e := a.localEntry(locales.In("en")); e.Sub != nil {
		t.Errorf("a submenu of one shell: %+v", e.Sub)
	}
	id, err := a.OpenLocalTerminal()
	if c := shellPane(t, a, id, err); c.host.Name != "Local terminal" {
		t.Errorf("one shell's pane is named %q", c.host.Name)
	}
}

// TestOpenLocalOnStart: off by default, the app starts with no tab; on,
// it starts with a local terminal, and the terminal settings say so even
// with one shell.
func TestOpenLocalOnStart(t *testing.T) {
	withShells(t, session.Shell{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"})
	a, tt := newTestApp(t)
	if len(a.ws.Tabs) != 0 {
		t.Fatalf("started with %d tabs", len(a.ws.Tabs))
	}
	a.openSettings(prefTerminal)
	tt.SetSize(1200, 2600)
	tt.Frame()
	mustFind(t, tt, "Open a local terminal when SSHYard starts")

	dir := t.TempDir()
	s := settings.Default()
	s.OpenLocalOnStart = true
	if err := settings.Save(filepath.Join(dir, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	a, tt = newTestAppIn(t, dir)
	tt.Frame()
	if len(a.ws.Tabs) != 1 || len(a.conns) != 1 {
		t.Fatalf("%d tabs, %d panes at launch", len(a.ws.Tabs), len(a.conns))
	}
	for _, c := range a.conns {
		if !isLocal(c.host.ID) {
			t.Errorf("opened %q, want a local terminal", c.host.ID)
		}
	}
}

// TestLocalTerminalTabOrSplit: the local terminal of the tabs' "+" opens
// in a new tab, the one of the status bar splits the focused pane.
func TestLocalTerminalTabOrSplit(t *testing.T) {
	withShells(t, session.Shell{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"})
	a, tt := newTestApp(t)
	if _, err := a.OpenLocalTerminal(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	mustClick(t, tt, "New Tab")
	qc, nh := mustFind(t, tt, "Quick Connect…"), mustFind(t, tt, "New Host")
	tt.ClickAt(nh.X+nh.W/2, nh.Y+nh.H/2+(nh.Y-qc.Y))
	tt.Frame()
	if len(a.ws.Tabs) != 2 || len(a.conns) != 2 || a.menu.key != "" {
		t.Fatalf("%d tabs, %d panes, menu %q: the + did not open a tab", len(a.ws.Tabs), len(a.conns), a.menu.key)
	}

	th := mustFind(t, tt, "Theme")
	tt.ClickAt(th.X-float32(theme.Space(1))-th.W/2, th.Y+th.H/2)
	tt.Frame()
	if len(a.ws.Tabs) != 2 || len(a.conns) != 3 {
		t.Fatalf("%d tabs, %d panes: the status bar did not split", len(a.ws.Tabs), len(a.conns))
	}
}

// TestOpenLocalTerminalAt: a local terminal opens in the folder asked for,
// and a path that is not a folder is refused.
func TestOpenLocalTerminalAt(t *testing.T) {
	withShells(t, session.Shell{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"})
	a, _ := newTestApp(t)
	dir := t.TempDir()
	id, err := a.OpenLocalTerminalAt(dir)
	c := shellPane(t, a, id, err)
	if c.s.Dir() != dir {
		t.Errorf("opened in %q, want %q", c.s.Dir(), dir)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenLocalTerminalAt(file); !errors.Is(err, errLocalDirNotDir) {
		t.Errorf("opening at a file: %v, want errLocalDirNotDir", err)
	}
	if _, err := a.OpenLocalTerminalAt(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("opening at a missing folder succeeded")
	}
}

// TestLaunchDir: the folder a launch asks for opens a local terminal once
// the app is ready, even with OpenLocalOnStart off.
func TestLaunchDirOpensLocalTerminal(t *testing.T) {
	withShells(t, session.Shell{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"})
	data, dir := t.TempDir(), t.TempDir()
	a, err := New(Platform{DataDir: data, Secrets: keychain.NewMemory(), HomeDir: data, CacheDir: filepath.Join(data, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.SetLaunchDir(dir)
	a.Start()
	a.withSecrets(nil)
	tt := ui.NewTester(a.View, 1200, 760)
	tt.SetLocale("en")
	tt.Frame()
	waitFor(t, tt, "the launched local terminal", func() bool { return len(a.conns) == 1 })
	for _, c := range a.conns {
		if got := c.s.Dir(); got != dir {
			t.Errorf("opened in %q, want %q", got, dir)
		}
	}

	other := t.TempDir()
	a.SetLaunchDir(other)
	waitFor(t, tt, "the second launched local terminal", func() bool { return len(a.conns) == 2 })
	for _, c := range a.conns {
		if got := c.s.Dir(); got != dir && got != other {
			t.Errorf("opened in %q, want %q or %q", got, dir, other)
		}
	}
}
