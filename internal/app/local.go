package app

import (
	"errors"
	"os"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// localID is the host of local terminal panes: this computer, never
// saved. Its panes split, move and close as any others, but it has no
// files, AI, forwards or metrics: there is no SSH connection under them.
// A shell's panes are of localID followed by the shell's ID; localID
// alone is the default shell.
const localID = "local:"

func isLocal(id string) bool { return strings.HasPrefix(id, localID) }

// detectShells finds this computer's shells; a variable so that tests
// can stand in.
var detectShells = session.Shells

// shell is the shell of the local host id: its own, else the default one
// (settings.LocalShell, else the first found) when it is gone.
func (a *App) shell(id string) session.Shell {
	want := strings.TrimPrefix(id, localID)
	if want == "" {
		want = a.settings.LocalShell
	}
	for _, sh := range a.shells {
		if sh.ID == want {
			return sh
		}
	}
	if want != a.settings.LocalShell {
		return a.shell(localID)
	}
	if len(a.shells) > 0 {
		return a.shells[0]
	}
	return session.Shell{}
}

// errLocalTerminalOnly is opening anything but a terminal, or an
// assistant that types into it, on this computer.
var errLocalTerminalOnly = errors.New("app: this computer opens terminals only")

// errLocalDirNotDir is opening a local terminal at a file rather than a
// folder.
var errLocalDirNotDir = errors.New("app: the local terminal folder is not a folder")

// localHost is the local host id: named in the app's language, or for
// its shell when there are several.
func (a *App) localHost(id string) assets.Host {
	name := a.localName
	if name == "" {
		name = locales.In("en").LocalTerminal()
	}
	if len(a.shells) > 1 {
		name = a.shell(id).Name
	}
	return assets.Host{ID: id, Name: name}
}

// OpenLocalTerminal opens the default shell of this computer under the
// focused pane, split from it in its tab, or in a new tab when there is
// none.
func (a *App) OpenLocalTerminal() (int, error) { return a.OpenLocalShell(a.shell(localID).ID) }

// OpenLocalShell opens the shell of id as OpenLocalTerminal does.
func (a *App) OpenLocalShell(id string) (int, error) { return a.openShellAt(id, "", true) }

// OpenLocalTerminalAt opens the default shell of this computer in dir, in
// a new tab, and says why it could not when dir is not a folder.
func (a *App) OpenLocalTerminalAt(dir string) (int, error) {
	info, err := os.Stat(dir)
	if err == nil && !info.IsDir() {
		err = errLocalDirNotDir
	}
	if err != nil {
		a.toast(func(m locales.Messages) ui.Toast {
			return ui.Toast{Title: m.LocalTerminalDirFailed(dir), Type: "error"}
		})
		return 0, err
	}
	return a.openShellAt(a.shell(localID).ID, dir, false)
}

// SetLaunchDir queues a local terminal in dir from any goroutine.
// Requests open in order once startup is complete and the app is unlocked.
func (a *App) SetLaunchDir(dir string) {
	if dir == "" {
		return
	}
	a.post(func() {
		a.launchDirs = append(a.launchDirs, dir)
		a.openLaunchDirs()
	})
}

func (a *App) openLaunchDirs() {
	if !a.launchReady || a.appLocked() {
		return
	}
	dirs := a.launchDirs
	a.launchDirs = nil
	for _, dir := range dirs {
		a.OpenLocalTerminalAt(dir)
	}
}

// openShellAt opens the shell of id, in dir when set, in a new tab, or
// with split under the focused pane when there is one.
func (a *App) openShellAt(id, dir string, split bool) (int, error) {
	var at openAt
	if t := a.ws.ActiveTab(); split && t != nil && t.Focus != 0 {
		at = openAt{pane: t.Focus, edge: layout.Bottom}
	}
	return a.openIn(localID+id, layout.Terminal, at, nil, dir)
}

// openLocal opens the shell of id, "" the default one, in a new tab or
// with split under the focused pane, and says why it could not.
func (a *App) openLocal(m locales.Messages, id string, split bool) {
	if id == "" {
		id = a.shell(localID).ID
	}
	if _, err := a.openShellAt(id, "", split); err != nil {
		a.toastError(m.ErrGeneric(err.Error()))
	}
}

// localEntry is the menu entry of local terminals: it opens the default
// shell in a new tab, or with several shells has a submenu of them.
func (a *App) localEntry(m locales.Messages) menuEntry {
	e := menuEntry{Icon: icons.LocalShell, Label: m.LocalTerminal(), Do: func() { a.openLocal(m, "", false) }}
	if len(a.shells) < 2 {
		return e
	}
	def := a.shell(localID).ID
	for _, sh := range a.shells {
		sub := menuEntry{Icon: icons.LocalShell, Label: sh.Name, Do: func() { a.openLocal(m, sh.ID, false) }}
		if sh.ID == def {
			sub.InlineNote = m.ShellDefault()
		}
		e.Sub = append(e.Sub, sub)
	}
	return e
}

// remoteOnly says a pane is on an SSH server, where files can open.
func remoteOnly(cn *conn) bool { return cn != nil && hasSSH(cn.host) }

// canAI says an assistant can open for a pane's host: any host. Over SSH
// it has files and commands; on a Telnet, serial or local terminal it
// types into the terminal (aiTarget).
func canAI(cn *conn) bool { return cn != nil && !isDesktop(cn.host) }
