package app

import (
	"slices"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// TrayAction identifies an action handed back to the desktop window owner.
type TrayAction string

const (
	TrayOpenMain      TrayAction = "open-main"
	TraySettings      TrayAction = "settings"
	TrayQuickConnect  TrayAction = "quick-connect"
	TrayLocalTerminal TrayAction = "local-terminal"
	TrayNewHost       TrayAction = "new-host"
	TrayNewWindow     TrayAction = "new-window"
	TrayLock          TrayAction = "lock"
	TrayQuit          TrayAction = "quit"
)

// TrayPanel owns ephemeral UI state, independently of the main sidebar.
type TrayPanel struct {
	app              *App
	dispatch         func(TrayAction)
	search, selected string
	shown            hostsView // the hosts it shows
	sections         map[string]*bool
	ResizeHandle     func(*ui.Context)
	SetMaterial      func(ui.Color)
}

func (a *App) NewTrayPanel(dispatch func(TrayAction)) *TrayPanel {
	return &TrayPanel{app: a, dispatch: dispatch, sections: map[string]*bool{}}
}

// View reads host metadata only; opening the panel never unlocks secrets.
func (p *TrayPanel) View(c *ui.Context) {
	a := p.app
	a.applyZoom(c, false)
	base := *c.Theme()
	base.Font = a.fonts.ui
	tok := theme.For(a.settings.Theme, base.Dark)
	c.SetTheme(tok.Apply(base))
	fill := tok.Sidebar
	if c.Vibrancy() && a.settings.TrayMaterial != settings.MaterialOpaque {
		fill = ui.Transparent
	}
	c.Root().Background(fill)
	if p.SetMaterial != nil {
		p.SetMaterial(fill)
	}
	// Store edits can happen in another window; schedule only while this view lives.
	c.After(500 * time.Millisecond)
	m := locales.In(c.Locale())
	c.Root().ObserveInput(a.noteInput)
	locked := a.lockGate(c)
	ui.Column(c).Fill().MinWidth(0).MinHeight(0).Background(fill).TextColor(tok.Text).Children(func() {
		p.header(c, m, tok)
		if locked {
			ui.Text(c, m.AppLockedTitle()).FontSize(theme.Body).TextColor(tok.TextMuted).Padding(theme.Space(3), theme.Space(4))
			ui.Spacer(c)
		} else if a.ready() {
			p.searchField(c, m, tok)
			ui.Box(c.Key("hostviewport")).FillWidth().Grow(1).Shrink(1).MinWidth(0).MinHeight(0).Clip().Children(func() { p.hosts(c, m, tok) })
		} else {
			ui.Text(c, m.SecretsUnlockFailed()).FontSize(theme.Body).TextColor(tok.TextMuted).Padding(theme.Space(3), theme.Space(4))
			ui.Spacer(c)
		}
		ui.Box(c).FillWidth().Height(1).Shrink(0).Background(tok.Line)
		ui.Column(c).FillWidth().Shrink(0).Padding(theme.Space(2)).Gap(theme.Space(1)).Children(func() {
			if locked {
				p.actionRow(c, "open", icons.Lock, m.AppLockShowMain(), TrayOpenMain, tok)
				return
			}
			p.actionRow(c, "open", icons.OpenInTab, m.TrayOpenPanel(), TrayOpenMain, tok)
			p.actionRow(c, "settings", icons.Settings, m.Settings(), TraySettings, tok)
		})
		if p.ResizeHandle != nil {
			p.ResizeHandle(c)
		}
	})
}

func (p *TrayPanel) header(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ui.Row(c.Key("header")).FillWidth().MinWidth(0).Shrink(0).Padding(theme.Space(2), theme.Space(4)).Gap(theme.Space(.5)).AlignItems(ui.Center).Children(func() {
		ui.Image(c, p.app.logo).Size(theme.Space(5), theme.Space(5)).Radius(theme.RadiusSmall).Shrink(0)
		ui.Text(c, m.AppName()).FontSize(theme.Body).FontWeight(600).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		ui.Row(c.Key("actions")).Shrink(0).Gap(theme.Space(.5)).AlignItems(ui.Center).Children(func() {
			p.headerAction(c, "quick", icons.Quick, m.QuickConnect(), TrayQuickConnect)
			p.headerAction(c, "local", icons.LocalShell, m.LocalTerminal(), TrayLocalTerminal)
			p.headerAction(c, "new", icons.Add, m.NewHost(), TrayNewHost)
			if (IconButton{Icon: icons.GitHub, Label: m.GitHubRepo(), Size: Small, InstantTooltip: true}).Clicked(c, "github") {
				c.OpenURL(repoURL)
			}
			p.headerAction(c, "settings", icons.Settings, m.Settings(), TraySettings)
		})
	})
}

func (p *TrayPanel) headerAction(c *ui.Context, key string, icon *icons.Icon, label string, action TrayAction) {
	disabled := trayDisabled(p.app, action)
	if (IconButton{Icon: icon, Label: label, Size: Small, Disabled: disabled, InstantTooltip: true}).Clicked(c, key) {
		p.app.post(func() { p.dispatch(action) })
	}
}

func (p *TrayPanel) searchField(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	// FillWidth is 100% of the parent's content box; keep outer insets in
	// the wrapper's padding so they cannot add to the search row's width.
	ui.Box(c.Key("searchinset")).FillWidth().MinWidth(0).Shrink(0).Padding(0, theme.Space(2), theme.Space(2)).Children(func() {
		ui.Row(c.Key("search")).FillWidth().MinWidth(0).Shrink(0).Height(theme.Space(8)).Padding(theme.Space(1), theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).Radius(theme.RadiusControl).Background(tok.Fill).Children(func() {
			ui.Icon(c, icons.Search.SVG).Size(theme.Space(4), theme.Space(4)).Shrink(0).TextColor(tok.TextMuted)
			in := ui.TextInputBase(c.Key("query"), &p.search).AutoFocus().Placeholder(m.SearchHosts()).Label(m.SearchHosts()).Grow(1).Shrink(1).MinWidth(0)
			if p.search != "" {
				if (IconButton{Icon: icons.ClearText, Label: m.ClearSearch(), Size: Small, InstantTooltip: true}).Clicked(c, "clear") || in.Shortcut(0, ui.KeyEscape) {
					p.search = ""
					in.Focus()
				}
			}
		})
	})
}

func (p *TrayPanel) section(key string) *bool {
	if b, ok := p.sections[key]; ok {
		return b
	}
	b := true
	p.sections[key] = &b
	return &b
}

func (p *TrayPanel) hosts(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	favs, groups, byGroup := p.shown.get(p.app.store, p.search)
	ui.Sidebar(c.Key("hosts"), &p.selected, func() {
		if len(favs) > 0 {
			ui.SidebarSection(c.Key("favorites"), m.Favorites(), p.section("fav"), func() {
				for _, h := range favs {
					p.host(c, h, "fav:", tok)
				}
			})
		}
		count := len(byGroup[""])
		for _, g := range groups {
			count += len(byGroup[g])
			// A group holding only subgroups shows as their sections.
			if len(byGroup[g]) == 0 && slices.ContainsFunc(groups, func(sub string) bool { return assets.GroupParent(sub) == g }) {
				continue
			}
			ui.SidebarSection(c.Key("group:"+g), p.app.pv(g), p.section("g:"+g), func() {
				for _, h := range byGroup[g] {
					p.host(c, h, "", tok)
				}
				if len(byGroup[g]) == 0 {
					ui.Text(c, m.EmptyGroup()).FontSize(theme.Caption).TextColor(tok.TextMuted).Padding(theme.Space(2))
				}
			})
		}
		if hs := byGroup[""]; len(hs) > 0 {
			ui.SidebarSection(c.Key("ungrouped"), m.Ungrouped(), p.section("none"), func() {
				for _, h := range hs {
					p.host(c, h, "", tok)
				}
			})
		}
		if count == 0 {
			label := m.NoHosts()
			if p.search != "" {
				label = m.PaletteNoResults()
			}
			ui.Text(c, label).FontSize(theme.Body).TextColor(tok.TextMuted).Padding(theme.Space(3), theme.Space(2))
		}
	}).Fill().MinWidth(0).MinHeight(0).Shrink(1).Padding(theme.Space(2.5), theme.Space(2)).Background(ui.Transparent)
}

func (p *TrayPanel) host(c *ui.Context, h assets.Host, prefix string, tok theme.Tokens) {
	row := ui.ButtonBase(c.Key(prefix+h.ID)).FillWidth().Justify(ui.Start).AlignItems(ui.Center).Padding(theme.Space(2), theme.Space(2)).Gap(theme.Space(2)).Radius(theme.RadiusControl).Label(p.app.pv(h.Label())).Cursor(ui.CursorPointer)
	if row.Hovered() || row.FocusVisible() {
		row.Background(tok.Hover)
	}
	if row.Pressed() {
		row.Background(tok.Press)
	}
	row.Children(func() {
		hostAvatar(c, tok, h, theme.Space(6))
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, p.app.pv(h.Label())).FontSize(theme.Body).SingleLine()
			ui.Text(c, p.app.pv(h.Target())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		})
	})
	if row.Clicked() {
		p.connect(h.ID)
	}
}

func (p *TrayPanel) actionRow(c *ui.Context, key string, icon *icons.Icon, label string, action TrayAction, tok theme.Tokens) {
	disabled := trayDisabled(p.app, action)
	row := ui.ButtonBase(c.Key(key)).Disabled(disabled).FillWidth().Justify(ui.Start).Padding(theme.Space(2)).Gap(theme.Space(2)).Radius(theme.RadiusControl).Label(label).Cursor(ui.CursorPointer)
	if row.Hovered() || row.FocusVisible() {
		row.Background(tok.Hover)
	}
	row.Children(func() {
		ui.Icon(c, icon.SVG).Size(theme.Space(4), theme.Space(4)).TextColor(tok.TextMuted)
		ui.Text(c, label).FontSize(theme.Body).SingleLine()
	})
	if row.Clicked() && !disabled {
		p.app.post(func() { p.dispatch(action) })
	}
}

func (p *TrayPanel) connect(hostID string) {
	p.app.post(func() {
		p.dispatch(TrayOpenMain)
		if err := p.app.ConnectFromTray(hostID); err != nil {
			p.app.trayError(err)
		}
	})
}

// trayDisabled reports whether action cannot run now: only the main
// window opens while the app is locked.
func trayDisabled(a *App, action TrayAction) bool {
	if a.appLocked() {
		return action != TrayOpenMain
	}
	return !a.ready() && (action == TrayQuickConnect || action == TrayLocalTerminal || action == TrayNewHost)
}

// ConnectFromTray always resolves a saved host afresh and follows normal secret gates.
func (a *App) ConnectFromTray(hostID string) error {
	if !a.ready() {
		return keychain.ErrLocked
	}
	if a.checkIdle(); a.appLocked() {
		return nil
	}
	if _, ok := a.store.Get(hostID); !ok {
		return assets.ErrNotFound
	}
	_, err := a.Connect(hostID, openAt{})
	return err
}

// RunTrayAction runs after the desktop owner has shown the main window,
// which it works in; a new window shows no other.
func (a *App) RunTrayAction(action TrayAction) {
	a.focusWin = a.main
	a.settle()
	// The main window shows the lock screen; nothing runs behind it.
	if a.checkIdle(); a.appLocked() {
		a.invalidate()
		return
	}
	if action == TrayLock {
		a.runAction(actLock)
		a.invalidate()
		return
	}
	if !a.ready() && (action == TrayQuickConnect || action == TrayLocalTerminal || action == TrayNewHost || action == TrayNewWindow) {
		a.trayError(keychain.ErrLocked)
		return
	}
	switch action {
	case TrayOpenMain:
		a.closeSettings()
	case TraySettings:
		a.openSettings(a.prefNow())
	case TrayQuickConnect:
		a.OpenQuickConnect()
	case TrayLocalTerminal:
		if _, err := a.OpenLocalTerminal(); err != nil {
			a.trayError(err)
		}
	case TrayNewHost:
		a.openEditor(nil)
	case TrayNewWindow:
		a.NewWindow()
	}
	a.invalidate()
}

func (a *App) trayError(err error) {
	a.toast(func(m locales.Messages) ui.Toast {
		return ui.Toast{Title: m.ErrGeneric(secretStoreErrorText(m, err)), Type: "error"}
	})
}
