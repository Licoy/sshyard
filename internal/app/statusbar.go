package app

import (
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

const statusBarHeight = 30

// statusBar is the band along the bottom of the content: on the left the
// focused session (its state, time connected, directory) and
// what runs (sessions, forwards, an update); on the right IconButtons to
// a local terminal, the theme, privacy, the command palette and the
// shortcuts. The settings
// are in the title bar.
func (a *App) statusBar(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	a.backgroundSeparator(c, tok.GlassEdge, a.chromeFill(tok.Glass)).Height(1).Shrink(0).FillWidth()
	ui.Row(c.Key("statusbar")).Height(statusBarHeight).Shrink(0).FillWidth().Padding(0, 6, 0, 12).Gap(theme.Space(3)).
		AlignItems(ui.Center).Background(a.chromeFill(tok.Glass)).Children(func() {
		if a.sidebarHidden() && (IconButton{Icon: icons.ShowSidebar, Label: m.ShowSidebar(), Size: Small}).Clicked(c, "showsidebar") {
			a.setSidebarHidden(false)
		}
		ui.Row(c.Key("info")).Grow(1).Shrink(1).MinWidth(0).Gap(theme.Space(3)).AlignItems(ui.Center).Clip().Children(func() {
			a.statusSession(c, m, tok)
			a.statusCounts(c, m, tok)
		})
		a.statusActions(c, m, tok)
	})
}

// statusItem is an icon and a caption, labeled for assistive technology.
func statusItem(c *ui.Context, key string, ic *icons.Icon, text, label string, col ui.Color) ui.Element {
	el := ui.Row(c.Key(key)).Gap(4).Shrink(0).AlignItems(ui.Center).Label(label).Tooltip(label)
	el.Children(func() {
		ui.Icon(c, ic.SVG).Size(12, 12).TextColor(col)
		ui.Text(c, text).FontSize(theme.Caption).TextColor(col).SingleLine()
	})
	return el
}

func (a *App) statusSession(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	cn := a.focused()
	if cn == nil {
		ui.Text(c, m.StatusNoSession()).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		return
	}
	h := a.liveHost(cn.host)
	var st session.Status
	if cn.s != nil {
		st = cn.s.Status()
	}
	ui.Row(c.Key("who")).Gap(6).Shrink(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
		if state, text, ok := a.paneStatus(m, cn, c.Now()); ok {
			dot(c, stateColor(tok, state)).Label(a.pv(text))
		}
		ui.Text(c, a.pv(h.Label())).FontSize(theme.Caption).FontWeight(600).SingleLine().Shrink(0)
		ui.Text(c, a.pv(h.Target())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
	})
	if cn.s == nil || st.State != session.Connected {
		return
	}
	if tc := a.monitorConn(cn); tc != nil && !tc.up.IsZero() {
		since := c.Now().Sub(tc.up)
		statusItem(c, "up", icons.Uptime, fmtDur(m, since), m.SysConnectedFor()+" "+fmtDur(m, since), tok.TextMuted)
		tickDur(c, since)
	}
	if cn.kind == layout.Terminal && cn.cwd != "" {
		el := ui.Row(c.Key("cwd")).Gap(4).Shrink(1).MinWidth(0).AlignItems(ui.Center).Label(m.StatusWorkingDir()).Tooltip(cn.cwd)
		el.Children(func() {
			ui.Icon(c, icons.Folder.SVG).Size(12, 12).Shrink(0).TextColor(tok.TextMuted)
			ui.Text(c, cn.cwd).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
		})
	}
}

func (a *App) statusCounts(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	n := 0
	for _, o := range a.conns {
		if o.fw == nil && !isTool(o.kind) {
			n++
		}
	}
	if n > 0 {
		s := m.StatusSessions(n)
		statusItem(c, "sessions", icons.Terminal, s, s, tok.TextMuted)
	}
	if f := len(a.fwds); f > 0 {
		s := m.StatusForwards(f)
		statusItem(c, "forwards", icons.Forward, s, s, tok.TextMuted)
	}
	if v := a.upd.Available(); v != "" {
		tip := m.UpdateAvailable(v)
		b := ui.ButtonBase(c.Key("update")).Label(tip).Tooltip(tip).Shrink(0).Padding(1, 8).Radius(theme.Capsule).
			Background(tok.Accent.Alpha(0.14)).Cursor(ui.CursorPointer)
		if b.Hovered() {
			b.Background(tok.Accent.Alpha(0.24))
		}
		b.Children(func() {
			ui.Text(c, m.StatusUpdate()).FontSize(theme.Caption).FontWeight(600).TextColor(tok.Accent).SingleLine()
		})
		if b.Clicked() {
			a.upd.Check()
		}
	}
}

// themeIcon is the icon of an appearance mode.
func themeIcon(md theme.Mode) *icons.Icon {
	switch md {
	case theme.Light:
		return icons.Light
	case theme.Dark:
		return icons.Dark
	}
	return icons.LocalDisk
}

func (a *App) statusActions(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ui.Row(c.Key("actions")).Shrink(0).Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
		a.outputEncodingChip(c, m)
		if (IconButton{Icon: icons.LocalShell, Label: m.LocalTerminal(), Size: Small}).Clicked(c, "localterm") {
			a.openLocal(m, "", true)
		}
		a.menuButton(c, "theme", IconButton{Icon: themeIcon(a.settings.Theme), Label: m.Theme(), Size: Small}, func() []menuEntry {
			var entries []menuEntry
			for i, md := range theme.Modes {
				entries = append(entries, menuEntry{
					Icon:    themeIcon(md),
					Label:   []string{m.ThemeAuto(), m.ThemeLight(), m.ThemeDark()}[i],
					Checked: a.settings.Theme == md,
					Do: func() {
						if a.settings.Theme == md {
							return
						}
						s := a.settings
						s.Theme = md
						if err := a.ApplySettings(s); err != nil {
							c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
						}
					},
				})
			}
			return entries
		})
		if (IconButton{Icon: icons.Privacy, On: icons.PrivacyOn, Label: m.PrivacyMode(), Size: Small, Active: a.settings.Privacy, Toggle: true}).Clicked(c, "privacy") {
			if err := a.TogglePrivacy(); err != nil {
				c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
			}
		}
		if (IconButton{Icon: icons.Command, Label: m.CommandPalette(), Size: Small}).Clicked(c, "palette") {
			a.openPalette()
		}
		short := a.page == pageSettings && a.prefNow() == prefShortcuts
		if (IconButton{Icon: icons.Shortcuts, Label: m.ShortcutsSection(), Size: Small, Active: short}).Clicked(c, "shortcuts") {
			a.openSettings(prefShortcuts)
		}
	})
}
