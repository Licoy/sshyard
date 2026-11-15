package app

import (
	"path"
	"strings"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
)

// tabName is what a tab is called: the name the user gave it (manual);
// else what its terminal reported for source (settings.TabTitle), the
// title its program set or, in short (shortDir, home among homes), the
// directory its shell is in; else its host's name.
func tabName(manual, source, host, reported string, homes ...string) string {
	if manual != "" {
		return manual
	}
	switch source {
	case settings.TabTitleProgram:
		if s := strings.TrimSpace(reported); s != "" {
			return s
		}
	case settings.TabTitleCwd:
		if d := followDir(reported); d != "" {
			return shortDir(d, homes...)
		}
	}
	return host
}

// shortDir is how a tab shows the directory d: "~" for a home among
// homes, "/" for the root, else its last component.
func shortDir(d string, homes ...string) string {
	for _, h := range homes {
		// A Windows home (C:\Users\me) reports as /C:/Users/me.
		if h = strings.TrimSuffix(strings.ReplaceAll(h, `\`, "/"), "/"); h != "" && strings.TrimPrefix(d, "/") == strings.TrimPrefix(h, "/") {
			return "~"
		}
	}
	return path.Base(d) // "/" for the root
}

// tabSource is what the tab title setting takes from terminal pane cn,
// in full: the title its program set or the directory its shell
// reported; "" for any other pane, the host's name, or nothing reported.
func (a *App) tabSource(cn *conn) string {
	if cn == nil || cn.kind != layout.Terminal || cn.s == nil || cn.s.Term == nil {
		return ""
	}
	switch a.settings.TabTitle {
	case settings.TabTitleProgram:
		return a.pv(strings.TrimSpace(cn.s.Term.Title()))
	case settings.TabTitleCwd:
		if d := followDir(cn.s.Term.Cwd()); d != "" {
			return d
		}
		return cn.cwd
	}
	return ""
}

// termHomes are where terminal pane cn's shell may start: this
// computer's home for a local terminal, else the home the server most
// likely gives its user.
func (a *App) termHomes(cn *conn) []string {
	if isLocal(cn.host.ID) {
		return []string{a.localHome()}
	}
	switch u := a.liveHost(cn.host).User; u {
	case "":
		return nil
	case "root":
		return []string{"/root"}
	default:
		return []string{"/home/" + u, "/Users/" + u}
	}
}
