package app

import (
	"runtime"
	"slices"
	"strconv"

	"github.com/Licoy/sshyard/internal/fonts"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keymap"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// languageNames are the languages offered, by their own names.
var languageNames = map[string]string{"en": "English", "zh-Hans": "简体中文", "zh-Hant": "繁體中文", "ko": "한국어", "ja": "日本語"}

// repoURL is where SSHYard's source and issues live.
const repoURL = "https://github.com/Licoy/sshyard"

// issuesURL is where feedback goes on GitHub, qqGroupURL the users' QQ group.
const (
	issuesURL  = repoURL + "/issues"
	qqGroupURL = "https://qm.qq.com/q/OcPq9MNkeO"
)

// prefPage is a page of the settings, chosen in their sidebar.
type prefPage string

const (
	prefGeneral     prefPage = "general"
	prefAppearance  prefPage = "appearance"
	prefTerminal    prefPage = "terminal"
	prefMonitoring  prefPage = "monitoring"
	prefFiles       prefPage = "files"
	prefAI          prefPage = "ai"
	prefAgents      prefPage = "agents"
	prefCredentials prefPage = "credentials"
	prefProxies     prefPage = "proxies"
	prefKnownHosts  prefPage = "knownhosts"
	prefMigration   prefPage = "migration"
	prefBackup      prefPage = "backup"
	prefShortcuts   prefPage = "shortcuts"
	prefAbout       prefPage = "about"
)

// prefItem is one page in the settings' sidebar: its icon, title and
// the line under the title on the page.
type prefItem struct {
	page        prefPage
	icon        *icons.Icon
	title, desc string
}

// prefGroup is a titled group of pages in the settings' sidebar.
type prefGroup struct {
	title string
	items []prefItem
}

func prefGroups(m locales.Messages) []prefGroup {
	return []prefGroup{
		{m.SettingsGroupPreferences(), []prefItem{
			{prefGeneral, icons.Settings, m.General(), m.GeneralDesc()},
			{prefAppearance, icons.Appearance, m.Appearance(), m.AppearanceDesc()},
			{prefTerminal, icons.Terminal, m.TerminalSection(), m.TerminalDesc()},
			{prefMonitoring, icons.Monitor, m.MonitoringSection(), m.MonitoringDesc()},
			{prefFiles, icons.Folder, m.FilesSection(), m.FilesDesc()},
			{prefAI, icons.Robot, m.AiSettings(), m.AiDesc()},
			{prefAgents, icons.MCP, m.AgentsPage(), m.AgentsDesc()},
		}},
		{m.SettingsGroupManage(), []prefItem{
			{prefCredentials, icons.Key, m.Credentials(), m.CredentialsDesc()},
			{prefProxies, icons.Proxy, m.Proxies(), m.ProxiesDesc()},
			{prefKnownHosts, icons.HostKey, m.KnownHosts(), m.KnownHostsDesc()},
			{prefMigration, icons.Import, m.Migration(), m.MigrationDesc()},
			{prefBackup, icons.Backup, m.BackupSection(), m.BackupDesc()},
		}},
		{m.SettingsGroupHelp(), []prefItem{
			{prefShortcuts, icons.Shortcuts, m.ShortcutsSection(), m.ShortcutsDesc()},
			{prefAbout, icons.Info, m.AboutSection(), m.AboutDesc()},
		}},
	}
}

// prefNow is the settings page showing, the first one when none was
// chosen.
func (a *App) prefNow() prefPage {
	if a.pref == "" {
		return prefGeneral
	}
	return a.pref
}

// openSettings shows the settings on page p.
func (a *App) openSettings(p prefPage) {
	a.clearOutputEncodingMenu("")
	a.page, a.pref = pageSettings, p
}

// closeSettings goes back to the sessions, the focused terminal taking
// the keyboard again.
func (a *App) closeSettings() {
	a.setBackgroundPreview(nil)
	a.page = pageWorkspace
	if cn := a.focused(); cn != nil {
		a.focusReq = cn.id
	}
}

// settingsNav is the sidebar while the settings show: the pages in
// groups, as System Settings lists them; the way back is in the title bar.
func (a *App) settingsNav(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	cur := a.prefNow()
	hits := settingsMatches(m, a.prefSearch.query)
	searching := a.prefSearch.query != ""
	found := map[prefPage]bool{}
	for _, h := range hits {
		found[h.item.page] = true
	}
	ui.Column(c).Fill().Children(func() {
		a.settingsSearchField(c, m, tok, hits)
		ui.Scroll(c.Key("setnav")).Grow(1).Shrink(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(theme.Space(1), theme.Space(2.5), theme.Space(3), theme.Space(2.5)).Gap(2).Children(func() {
				for gi, g := range prefGroups(m) {
					top := theme.Space(4)
					if gi == 0 {
						top = theme.Space(1)
					}
					ui.Text(c, g.title).FontSize(theme.Caption).FontWeight(600).TextColor(tok.TextMuted).
						Padding(top, theme.Space(2), theme.Space(1), theme.Space(2)).SingleLine()
					for _, it := range g.items {
						a.prefNavItem(c, tok, it, !searching && it.page == cur, searching && !found[it.page])
					}
				}
			})
		})
		v := a.p.Version
		if v == "" {
			v = "dev"
		}
		ui.Row(c).Shrink(0).Padding(theme.Space(2.5), theme.Space(4)).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.AppName()).FontSize(theme.Caption).FontWeight(600).TextColor(tok.TextMuted)
			ui.Text(c, m.CurrentVersion(v)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		})
	})
}

// prefNavItem is one page's row in the settings' sidebar: its tile and
// title, filled with a tint of the accent while it shows, muted (dim)
// while the search finds nothing on it.
func (a *App) prefNavItem(c *ui.Context, tok theme.Tokens, it prefItem, on, dim bool) {
	b := ui.ButtonBase(c.Key("pref:"+string(it.page))).Label(it.title).Selected(on).FillWidth().
		Padding(5, 8).Gap(10).Radius(theme.RadiusControl).AlignItems(ui.Center).Justify(ui.Start).Cursor(ui.CursorPointer)
	strong := float32(0.12)
	if tok.Dark {
		strong = 0.22
	}
	switch {
	case on:
		b.Background(tok.Accent.Alpha(strong))
	case b.Pressed():
		b.Background(tok.Press)
	case b.Hovered():
		b.Background(tok.Hover)
	}
	b.Children(func() {
		col := tok.TextMuted
		if on {
			col = tok.Accent
		}
		glyph(c, it.icon, col, 22)
		tx := ui.Text(c, it.title).FontSize(theme.Body).SingleLine().Shrink(1)
		switch {
		case on:
			tx.FontWeight(600).TextColor(tok.Accent)
		case dim:
			tx.TextColor(tok.TextMuted)
		}
	})
	if b.Clicked() {
		a.openSetting(it.page, "")
	}
}

// settingsGroup is one group of settings, as in System Settings: a tinted
// tile and a title over a card of rows split by hairlines.
type settingsGroup struct {
	c   *ui.Context
	tok theme.Tokens
	// a scrolls to the row the search found; none outside the pages
	a    *App
	rows int
}

// row adds one setting: its label (and a muted note) on the left, its
// control on the right.
func (g *settingsGroup) row(label, note string, control func(), lead ...func()) {
	c, tok := g.c, g.tok
	if g.rows > 0 {
		ui.Box(c).FillWidth().Padding(0, 0, 0, 14).Children(func() { ui.Box(c).Height(1).FillWidth().Background(tok.Line) })
	}
	g.rows++
	r := ui.Row(c).MinHeight(48).Padding(8, 14).Gap(16).AlignItems(ui.Center).FillWidth()
	if g.a != nil {
		g.a.revealRow(c, tok, label, r, r)
	}
	r.Children(func() {
		if len(lead) > 0 {
			lead[0]()
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(theme.Body).FontWeight(500)
			if note != "" {
				ui.Text(c, note).FontSize(theme.Caption).TextColor(tok.TextMuted)
			}
		})
		ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Children(control)
	})
}

// divider is the hairline between rows, before what is not a row.
func (g *settingsGroup) divider() {
	c, tok := g.c, g.tok
	ui.Box(c).FillWidth().Padding(0, 0, 0, 14).Children(func() { ui.Box(c).Height(1).FillWidth().Background(tok.Line) })
}

// settingsSection is a card of settings under an icon and a title; with
// no title, the card alone.
func (a *App) settingsSection(c *ui.Context, tok theme.Tokens, ic *icons.Icon, title string, fn func(g *settingsGroup)) {
	sec := ui.Column(c).Gap(8).FillWidth()
	sec.Children(func() {
		if title != "" {
			head := ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 4)
			a.revealRow(c, tok, title, sec, head)
			head.Children(func() {
				glyph(c, ic, tok.Accent, 20)
				ui.Text(c, title).FontSize(theme.Body).FontWeight(600)
			})
		}
		a.contentCard(c, tok).FillWidth().Children(func() {
			fn(&settingsGroup{c: c, tok: tok, a: a})
		})
	})
}

func (a *App) settingsPage(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	if !a.unlocked() {
		a.secretsBanner(c, m, tok)
	}
	s := a.settings
	changed := false
	cur := a.prefNow()
	if cur != prefAppearance {
		a.setBackgroundPreview(nil)
	}
	if a.prefSearch.query != "" {
		a.settingsResults(c, m, tok, settingsMatches(m, a.prefSearch.query))
		return
	}
	item := prefItemOf(prefGroups(m), cur)
	ui.Scroll(c.Key("prefpage:" + string(cur))).Fill().Children(func() {
		ui.Column(c).Padding(theme.Space(7), theme.Space(8), theme.Space(10), theme.Space(8)).Gap(theme.Space(6)).MaxWidth(720).FillWidth().Children(func() {
			ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
				glyph(c, item.icon, tok.Accent, 40)
				ui.Column(c).Gap(3).Shrink(1).Children(func() {
					ui.Text(c, item.title).FontWeight(700).FontSize(theme.Title1)
					ui.Text(c, item.desc).FontSize(theme.Body).TextColor(tok.TextMuted)
				})
			})
			switch cur {
			case prefGeneral:
				a.prefGeneral(c, m, tok, &s, &changed)
			case prefAppearance:
				a.prefAppearance(c, m, tok, &s, &changed)
			case prefTerminal:
				a.prefTerminal(c, m, tok, &s, &changed)
			case prefMonitoring:
				a.prefMonitoring(c, m, tok, &s, &changed)
			case prefFiles:
				a.prefFiles(c, m, tok, &s, &changed)
			case prefAI:
				a.aiSettings(c, m, tok, &s, &changed)
			case prefAgents:
				a.prefAgents(c, m, tok, &s, &changed)
			case prefCredentials:
				a.credentialsSection(c, m, tok)
				a.localKeysSection(c, m, tok, &s, &changed)
			case prefProxies:
				a.proxiesSection(c, m, tok)
			case prefKnownHosts:
				a.knownHostsPage(c, m, tok)
			case prefMigration:
				a.migrationSection(c, m, tok)
			case prefBackup:
				a.backupSection(c, m, tok)
			case prefShortcuts:
				a.shortcutsPage(c, m, tok)
			case prefAbout:
				a.aboutPage(c, m, tok)
			}
		})
	})
	// a row the search found that the page did not draw is not waited for
	if ps := &a.prefSearch; ps.row != "" {
		ps.row, ps.flash = "", ""
	}
	if changed {
		if err := a.ApplySettings(s); err != nil {
			c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
		}
	}
}

// themeSwitch chooses the appearance: a capsule track of the modes, each
// with its icon, the one chosen raised on it.
func (a *App) themeSwitch(c *ui.Context, m locales.Messages) (theme.Mode, bool) {
	a.themeIdx = 0
	for i, md := range theme.Modes {
		if md == a.settings.Theme {
			a.themeIdx = i
		}
	}
	icon := []*icons.Icon{icons.LocalDisk, icons.Light, icons.Dark}
	labels := []string{m.ThemeAuto(), m.ThemeLight(), m.ThemeDark()}
	track := segmentedControl(c.Key("theme"), &a.themeIdx, false, labels, theme.Space(3), func(i int, col ui.Color) {
		ui.Icon(c, icon[i].SVG).Size(14, 14).TextColor(col)
		ui.Text(c, labels[i]).FontSize(theme.Footnote).FontWeight(500).TextColor(col).SingleLine()
	}).Label(m.Appearance())
	if !track.Changed() {
		return a.settings.Theme, false
	}
	return theme.Modes[a.themeIdx], true
}

// prefGeneral is what applies to all of SSHYard rather than to how it
// looks: the language, the window and the tray, reminders and updates.
func (a *App) prefGeneral(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		g.row(m.Language(), "", func() {
			opts := []string{m.LanguageAuto()}
			for _, l := range settings.Languages {
				opts = append(opts, languageNames[l])
			}
			cur := opts[0]
			if n, ok := languageNames[s.Language]; ok {
				cur = n
			}
			if formSelect(c.Key("lang"), &cur, opts).Label(m.Language()).Width(180).Changed() {
				s.Language = ""
				for _, l := range settings.Languages {
					if languageNames[l] == cur {
						s.Language = l
					}
				}
				*changed = true
			}
		})
		g.row(m.RememberLayout(), m.RememberLayoutNote(), func() {
			if formSwitch(c.Key("rememberlayout"), &s.RememberLayout).Label(m.RememberLayout()).Changed() {
				*changed = true
			}
		})
		g.row(m.ToolPanes(), m.ToolPanesNote(), func() {
			modes := []string{"", settings.ToolPanesSplit}
			a.toolPanesChoice = max(slices.Index(modes, s.ToolPanes), 0)
			if formSegmented(c.Key("toolpanes"), &a.toolPanesChoice, false, m.ToolPanesTabs(), m.ToolPanesSplit()).Label(m.ToolPanes()).Changed() {
				if a.toolPanesChoice < len(modes) {
					s.ToolPanes, *changed = modes[a.toolPanesChoice], true
				}
			}
		})
		g.row(m.RenewalNotify(), m.RenewalNotifyNote(), func() {
			if formSwitch(c.Key("renewalnotify"), &s.RenewalNotify).Label(m.RenewalNotify()).Changed() {
				*changed = true
			}
		})
	})
	a.settingsSection(c, tok, icons.AppWindow, m.WindowTraySection(), func(g *settingsGroup) {
		g.row(m.CloseBehavior(), m.CloseBehaviorNote(), func() {
			cur := m.CloseToTray()
			if s.CloseBehavior == settings.CloseQuit {
				cur = m.CloseQuit()
			}
			if formSelect(c.Key("closebehavior"), &cur, []string{m.CloseToTray(), m.CloseQuit()}).Label(m.CloseBehavior()).Width(180).Changed() {
				s.CloseBehavior = settings.CloseToTray
				if cur == m.CloseQuit() {
					s.CloseBehavior = settings.CloseQuit
				}
				*changed = true
			}
		})
		g.row(m.TrayIconStyle(), m.TrayIconStyleNote(), func() {
			cur := m.TrayIconMono()
			if s.TrayIcon == settings.TrayIconColor {
				cur = m.TrayIconColor()
			}
			if formSelect(c.Key("trayicon"), &cur, []string{m.TrayIconMono(), m.TrayIconColor()}).Label(m.TrayIconStyle()).Width(180).Changed() {
				s.TrayIcon = settings.TrayIconMono
				if cur == m.TrayIconColor() {
					s.TrayIcon = settings.TrayIconColor
				}
				*changed = true
			}
		})
		a.trayPreferences(c, m, g, s, changed)
	})
	a.updatesSection(c, m, tok, s, changed)
}

func (a *App) prefAppearance(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		g.row(m.Theme(), "", func() {
			if md, ok := a.themeSwitch(c, m); ok {
				s.Theme, *changed = md, true
			}
		})
		g.row(m.UiZoom(), m.UiZoomDesc(), func() {
			opts := []string{m.ZoomSystem()}
			for _, z := range settings.UIZooms {
				opts = append(opts, zoomLabel(m, z))
			}
			cur := zoomLabel(m, s.UIZoom)
			if formSelect(c.Key("uizoom"), &cur, opts).Label(m.UiZoom()).Width(180).Changed() {
				s.UIZoom = 0
				for _, z := range settings.UIZooms {
					if zoomLabel(m, z) == cur {
						s.UIZoom = z
					}
				}
				*changed = true
			}
		})
	})
	a.backgroundSection(c, m, tok, s, changed)
	if !a.hasBackground() {
		a.materialSection(c, m, tok, s, changed)
	}
	a.settingsSection(c, tok, icons.Edit, m.FontsSection(), func(g *settingsGroup) {
		g.row(m.InterfaceFont(), "", func() {
			if f, ok := a.fontPicker(c, "uifont", m.InterfaceFont(), m.SystemDefault(), &a.fonts.uiSel, s.UIFont, a.fonts.list); ok {
				s.UIFont, *changed = f, true
			}
		})
		g.divider()
		a.uiFontPreview(c, m, tok)
	})
}

// materials are the materials a part of the window may have, as the
// segments of its choice show them.
var materials = []string{settings.MaterialTranslucent, settings.MaterialOpaque}

// materialSection chooses how the sidebar with the title bar, and the
// content, show the window's material: translucent or opaque, each on its
// own. Without a material (Linux, Windows) the choice cannot be made.
func (a *App) materialSection(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	off := !c.Vibrancy()
	a.settingsSection(c, tok, icons.Appearance, m.MaterialSection(), func(g *settingsGroup) {
		choose := func(key, label, note string, idx *int, cur *string) {
			*idx = max(slices.Index(materials, *cur), 0)
			if off {
				note = m.MaterialUnsupported()
			}
			g.row(label, note, func() {
				seg := formSegmented(c.Key(key), idx, off, m.MaterialTranslucent(), m.MaterialOpaque()).Label(label)
				if seg.Changed() && !off {
					*cur, *changed = materials[*idx], true
				}
			})
		}
		choose("matchrome", m.MaterialChrome(), "", &a.chromeMat, &s.ChromeMaterial)
		choose("matcontent", m.MaterialContent(), m.MaterialContentNote(), &a.contentMat, &s.ContentMaterial)
	})
}

func (a *App) prefTerminal(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.schemeSection(c, m, tok, s, changed)
	a.settingsSection(c, tok, icons.Edit, m.FontsSection(), func(g *settingsGroup) {
		g.row(m.CursorShape(), m.CursorShapeNote(), func() {
			codes := []string{settings.CursorBlock, settings.CursorBar, settings.CursorUnderline}
			names := []string{m.CursorBlock(), m.CursorBar(), m.CursorUnderline()}
			cur := names[max(slices.Index(codes, s.TerminalCursorStyle), 0)]
			if formSelect(c.Key("cursorstyle"), &cur, names).Label(m.CursorShape()).Width(180).Changed() {
				s.TerminalCursorStyle, *changed = codes[slices.Index(names, cur)], true
			}
		})
		g.row(m.TerminalCursorBlink(), m.TerminalCursorBlinkNote(), func() {
			if formSwitch(c.Key("cursorblink"), &s.TerminalCursorBlink).Label(m.TerminalCursorBlink()).Changed() {
				*changed = true
			}
		})
		g.row(m.TerminalLineNumbers(), m.TerminalLineNumbersNote(), func() {
			if formSwitch(c.Key("linenumbers"), &s.TerminalLineNumbers).Label(m.TerminalLineNumbers()).Changed() {
				*changed = true
			}
		})
		g.row(m.TerminalTimestamps(), m.TerminalTimestampsNote(), func() {
			if formSwitch(c.Key("timestamps"), &s.TerminalTimestamps).Label(m.TerminalTimestamps()).Changed() {
				*changed = true
			}
		})
		g.row(m.TerminalFont(), "", func() {
			if f, ok := a.fontPicker(c, "termfont", m.TerminalFont(), m.SystemDefault(), &a.fonts.termSel, s.TerminalFont, fonts.ForTerminal(a.fonts.list, s.MonospacedOnly)); ok {
				s.TerminalFont, *changed = f, true
			}
		})
		g.row(m.TerminalFallbackFont(), m.TerminalFallbackFontNote(), func() {
			if f, ok := a.fontPicker(c, "termfallback", m.TerminalFallbackFont(), m.SystemDefault(), &a.fonts.fallbackSel, s.TerminalFallbackFont, a.fonts.list); ok {
				s.TerminalFallbackFont, *changed = f, true
			}
		})
		g.row(m.MonospacedOnly(), m.MonospacedOnlyNote(), func() {
			mono := s.MonospacedOnly
			if formSwitch(c.Key("monoonly"), &mono).Label(m.MonospacedOnly()).Changed() {
				s.MonospacedOnly, *changed = mono, true
			}
		})
		g.row(m.FontSize(), m.FontSizeNote(), func() {
			if ui.NumberInput(c.Key("font"), &a.fontChoice, settings.MinFontSize, settings.MaxFontSize, 1).Changed() {
				s.TerminalFontSize = int(a.fontChoice)
				*changed = true
			}
		})
		g.row(m.TerminalLineHeight(), m.TerminalLineHeightNote(), func() {
			if ui.NumberInput(c.Key("lineheight"), &a.lineChoice, settings.MinLineHeight, settings.MaxLineHeight, 5).Label(m.TerminalLineHeight()).Changed() {
				s.TerminalLineHeight, *changed = int(a.lineChoice), true
			}
		})
		g.row(m.TerminalCharWidth(), m.TerminalCharWidthNote(), func() {
			if ui.NumberInput(c.Key("charwidth"), &a.widthChoice, settings.MinCharWidth, settings.MaxCharWidth, 5).Label(m.TerminalCharWidth()).Changed() {
				s.TerminalCharWidth, *changed = int(a.widthChoice), true
			}
		})
		a.termFontPreview(c, tok)
	})
	a.highlightSection(c, m, tok, s, changed)
	a.settingsSection(c, tok, icons.LocalShell, m.LocalTerminal(), func(g *settingsGroup) {
		g.row(m.OpenLocalOnStart(), m.OpenLocalOnStartNote(), func() {
			on := s.OpenLocalOnStart
			if formSwitch(c.Key("localstart"), &on).Label(m.OpenLocalOnStart()).Changed() {
				s.OpenLocalOnStart, *changed = on, true
			}
		})
		if len(a.shells) < 2 {
			return
		}
		g.row(m.DefaultShell(), m.DefaultShellNote(), func() {
			names := make([]string, len(a.shells))
			for i, sh := range a.shells {
				names[i] = sh.Name
			}
			cur := names[0]
			for _, sh := range a.shells {
				if sh.ID == s.LocalShell {
					cur = sh.Name
				}
			}
			if formSelect(c.Key("localshell"), &cur, names).Label(m.DefaultShell()).Width(220).Changed() {
				if i := slices.Index(names, cur); i >= 0 {
					s.LocalShell, *changed = a.shells[i].ID, true
				}
			}
		})
	})
	a.settingsSection(c, tok, icons.Reconnect, m.ConnectionSection(), func(g *settingsGroup) {
		g.row(m.KeepAlive(), "", func() {
			if ui.NumberInput(c.Key("keep"), &a.keepChoice, 0, 600, 5).Changed() {
				s.KeepAliveSeconds = int(a.keepChoice)
				*changed = true
			}
		})
		g.row(m.AutoReconnect(), "", func() {
			a.autoRecon = s.AutoReconnect
			if formSwitch(c.Key("recon"), &a.autoRecon).Label(m.AutoReconnect()).Changed() {
				s.AutoReconnect = a.autoRecon
				*changed = true
			}
		})
		g.row(m.TermType(), m.TermTypeNote(), func() {
			a.termTypeRow(c, m, tok, s, changed)
		})
	})
	a.settingsSection(c, tok, icons.Paste, m.ClipboardSection(), func(g *settingsGroup) {
		g.row(m.RightClick(), m.RightClickNote(), func() {
			codes := []string{settings.RightClickMenu, settings.RightClickPaste, settings.RightClickCopyPaste}
			a.rightClickChoice = max(slices.Index(codes, s.TerminalRightClick), 0)
			if formSegmented(c.Key("rightclick"), &a.rightClickChoice, false, m.RightClickMenu(), m.Paste(), m.RightClickCopyPaste()).Label(m.RightClick()).Changed() {
				if a.rightClickChoice >= 0 && a.rightClickChoice < len(codes) {
					s.TerminalRightClick, *changed = codes[a.rightClickChoice], true
				}
			}
		})
		g.row(m.CopyOnSelect(), m.CopyOnSelectNote(), func() {
			on := s.CopyOnSelect
			if formSwitch(c.Key("copyonselect"), &on).Label(m.CopyOnSelect()).Changed() {
				s.CopyOnSelect, *changed = on, true
			}
		})
		g.row(m.Osc52Title(), m.Osc52Note(), func() {
			codes := []string{settings.OSC52Allow, settings.OSC52Ask, settings.OSC52Deny}
			a.osc52Choice = 0
			for i, code := range codes {
				if s.OSC52Write == code {
					a.osc52Choice = i
				}
			}
			if formSegmented(c.Key("osc52"), &a.osc52Choice, false, m.Osc52Allow(), m.Osc52Ask(), m.Osc52Deny()).Label(m.Osc52Title()).Changed() {
				if a.osc52Choice >= 0 && a.osc52Choice < len(codes) {
					s.OSC52Write = codes[a.osc52Choice]
					*changed = true
				}
			}
		})
		g.row(m.Osc52Limit(), m.Osc52LimitHint(), func() {
			if ui.NumberInput(c.Key("osc52max"), &a.osc52KiB, 64, 8192, 64).Changed() {
				s.OSC52MaxBytes = int(a.osc52KiB) << 10
				*changed = true
			}
		})
		g.row(m.Osc52Notify(), m.Osc52NotifyNote(), func() {
			on := s.OSC52Notify
			if formSwitch(c.Key("osc52notify"), &on).Label(m.Osc52Notify()).Changed() {
				s.OSC52Notify, *changed = on, true
			}
		})
	})
	a.settingsSection(c, tok, icons.MoveTo, m.TermXferSection(), func(g *settingsGroup) {
		g.row(m.TermXferSetting(), m.TermXferSettingNote(), func() {
			on := s.TerminalTransfers
			if formSwitch(c.Key("termxfer"), &on).Label(m.TermXferSetting()).Changed() {
				s.TerminalTransfers, *changed = on, true
			}
		})
	})
}

func (a *App) prefMonitoring(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		g.row(m.MetricsBar(), m.MetricsBarNote(), func() {
			a.metOn = s.MetricsEnabled
			if formSwitch(c.Key("meton"), &a.metOn).Label(m.MetricsBar()).Changed() {
				s.MetricsEnabled = a.metOn
				*changed = true
			}
		})
		g.row(m.MetricsInterval(), "", func() {
			a.metEvery = intervalIndex(s.MetricsInterval)
			if formSegmented(c.Key("metevery"), &a.metEvery, false, m.Seconds("1"), m.Seconds("2"), m.Seconds("5"), m.Seconds("10")).Label(m.MetricsInterval()).Changed() {
				s.MetricsInterval = []int{1, 2, 5, 10}[a.metEvery]
				*changed = true
			}
		})
		g.row(m.MetricsMount(), m.MetricsMountNote(), func() {
			in := formTextInput(c.Key("metmount"), &a.metMount).Label(m.MetricsMount())
			focused := in.Focused()
			// Enter, or the caret leaving the field. Typing restarts
			// the remote command, so it waits for one of those.
			if in.Submitted() || (a.metMountFocus && !focused) {
				if s.MetricsMount != a.metMount {
					s.MetricsMount = a.metMount
					*changed = true
				}
			}
			a.metMountFocus = focused
		})
	})
}

func (a *App) prefFiles(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		g.row(m.TransferConcurrency(), m.TransferConcurrencyHint(), func() {
			if ui.NumberInput(c.Key("conc"), &a.concChoice, 1, 10, 1).Changed() {
				s.TransferConcurrency = int(a.concChoice)
				*changed = true
			}
		})
		g.row(m.OpenConfirmMB(), m.OpenConfirmHint(), func() {
			if ui.NumberInput(c.Key("openmb"), &a.openMBChoice, 1, 1024, 1).Changed() {
				s.OpenConfirmMB = int(a.openMBChoice)
				*changed = true
			}
		})
		g.row(m.FilesEdge(), m.FilesEdgeNote(), func() {
			edges := []string{settings.FilesRight, settings.FilesBelow}
			a.filesEdgeChoice = max(slices.Index(edges, s.FilesEdge), 0)
			if formSegmented(c.Key("filesedge"), &a.filesEdgeChoice, false, m.FilesEdgeRight(), m.FilesEdgeBelow()).Label(m.FilesEdge()).Changed() {
				if a.filesEdgeChoice >= 0 && a.filesEdgeChoice < len(edges) {
					s.FilesEdge, *changed = edges[a.filesEdgeChoice], true
				}
			}
		})
		g.row(m.FilesZoom(), m.FilesZoomNote(), func() {
			opts := make([]string, len(settings.FilesZooms))
			for i, z := range settings.FilesZooms {
				opts[i] = zoomLabel(m, z)
			}
			cur := zoomLabel(m, int(s.FilesScale()*100+0.5))
			if formSelect(c.Key("fileszoom"), &cur, opts).Label(m.FilesZoom()).Width(180).Changed() {
				if i := slices.Index(opts, cur); i >= 0 {
					s.FilesZoom, *changed = settings.FilesZooms[i], true
				}
			}
		})
	})
}

// keyCaps are the caps of a shortcut: the modifiers as macOS draws them
// there, by name elsewhere, then the key.
func keyCaps(goos string, mods ui.Modifiers, key ui.Key) []string {
	var out []string
	mac := goos == "darwin"
	for _, md := range []struct {
		mod       ui.Modifiers
		mac, name string
	}{{ui.Ctrl, "⌃", "Ctrl"}, {ui.Alt, "⌥", "Alt"}, {ui.Shift, "⇧", "Shift"}, {ui.Super, "⌘", "Super"}} {
		if mods&md.mod == 0 {
			continue
		}
		if mac {
			out = append(out, md.mac)
		} else {
			out = append(out, md.name)
		}
	}
	switch {
	case key >= ui.KeyA && key <= ui.KeyZ:
		out = append(out, string(rune('A'+key-ui.KeyA)))
	case key >= ui.Key0 && key <= ui.Key9:
		out = append(out, string(rune('0'+key-ui.Key0)))
	case key >= ui.KeyF1 && key <= ui.KeyF12:
		out = append(out, "F"+strconv.Itoa(int(key-ui.KeyF1)+1))
	case key == ui.KeySpace:
		out = append(out, "Space")
	case printable[key][0] != 0:
		out = append(out, string(printable[key][0]))
	case key == ui.KeyDelete && mac:
		out = append(out, "⌦")
	case key == ui.KeyDelete:
		out = append(out, "Del")
	case key == ui.KeyBackspace && mac:
		out = append(out, "⌫")
	case key == ui.KeyBackspace:
		out = append(out, "Backspace")
	}
	return out
}

// shortcutsPage lists the actions with their shortcuts: each records a
// new one, or goes back to its own.
func (a *App) shortcutsPage(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	custom := false
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		for _, ac := range actions {
			k := a.keyOf(ac.id)
			label := ac.label(m)
			recording := a.recording == ac.id
			note := ""
			if recording {
				note = m.ShortcutRecordHint()
			}
			custom = custom || k != ac.def
			g.row(label, note, func() {
				if recording {
					a.shortcutRecorder(c, m, tok, label)
					return
				}
				for _, cap := range keyCaps(runtime.GOOS, k.Mods, k.Key) {
					keyCap(c, tok, cap)
				}
				if (IconButton{Icon: icons.Record, Label: m.ShortcutRecord(label), Size: Small}).Clicked(c, "sc:rec:"+ac.id) {
					a.recording, a.recordErr = ac.id, ""
				}
				if k != ac.def && (IconButton{Icon: icons.Reset, Label: m.ShortcutReset(label), Size: Small}).Clicked(c, "sc:reset:"+ac.id) {
					if err := a.setShortcut(ac.id, ac.def); err != nil {
						a.toastError(m.ErrGeneric(err.Error()))
					}
				}
			})
		}
	})
	if custom {
		ui.Row(c).FillWidth().Children(func() {
			ui.Spacer(c)
			if pill(c, m.ShortcutsResetAll(), false, false, false) {
				s := a.settings
				s.Shortcuts = nil
				a.recording, a.recordErr = "", ""
				if err := a.ApplySettings(s); err != nil {
					a.toastError(m.ErrGeneric(err.Error()))
				}
			}
		})
	}
}

// shortcutRecorder takes the keys while a shortcut is recorded: the next
// one pressed with its modifiers becomes it, Esc gives up.
func (a *App) shortcutRecorder(c *ui.Context, m locales.Messages, tok theme.Tokens, label string) {
	if a.recordErr != "" {
		ui.Text(c, a.recordErr).FontSize(theme.Caption).TextColor(tok.Danger).MaxWidth(260)
	}
	box := ui.Box(c.Key("sc:recorder")).Focusable().Label(m.ShortcutRecord(label)).MinWidth(120).Height(24).Padding(0, 10).
		Radius(theme.RadiusSmall).Background(tok.Accent.Alpha(0.12)).Border(1, tok.Accent).Center()
	box.Focus()
	box.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputKeyDown {
			return false
		}
		a.recordKey(m, ev)
		return true
	})
	box.Children(func() {
		ui.Text(c, m.ShortcutPressKeys()).FontSize(theme.Footnote).FontWeight(500).TextColor(tok.Accent).SingleLine()
	})
	if (IconButton{Icon: icons.Close, Label: m.Cancel(), Size: Small}).Clicked(c, "sc:cancel") {
		a.recording, a.recordErr = "", ""
	}
}

// recordKey takes a key pressed while a shortcut is recorded.
func (a *App) recordKey(m locales.Messages, ev ui.InputEvent) {
	key := ev.Key
	if key == ui.KeyUnknown {
		key = ev.PhysicalKey
	}
	if key == ui.KeyEscape && ev.Mods == 0 {
		a.recording, a.recordErr = "", ""
		return
	}
	k := keymap.Combo{Mods: ev.Mods, Key: key}
	if p := a.shortcutProblem(m, a.recording, k); p != "" {
		a.recordErr = p
		return
	}
	if err := a.setShortcut(a.recording, k); err != nil {
		a.recordErr = m.ErrGeneric(err.Error())
		return
	}
	a.recording, a.recordErr = "", ""
}

// keyCap is one key of a shortcut, drawn as a key.
func keyCap(c *ui.Context, tok theme.Tokens, s string) {
	ui.Box(c).MinWidth(24).Height(22).Padding(0, 7).Radius(theme.RadiusSmall).Background(tok.Inset).Border(1, tok.Border).
		Center().Children(func() {
		ui.Text(c, s).FontSize(theme.Footnote).FontWeight(500).SingleLine()
	})
}

func (a *App) aboutPage(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	v := a.p.Version
	if v == "" {
		v = "dev"
	}
	// The app itself: its tile, name, version and what it is.
	a.contentCard(c, tok).FillWidth().Padding(theme.Space(5)).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Image(c, a.logo).Size(60, 60).Shrink(0).Radius(theme.RadiusSheet).Label(m.AppName())
			ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, m.AppName()).FontSize(theme.Title2).FontWeight(700)
				ui.Text(c, m.CurrentVersion(v)).FontSize(theme.Footnote).TextColor(tok.TextMuted)
				ui.Text(c, m.AboutTagline()).FontSize(theme.Body).TextColor(tok.TextMuted)
			})
			if pill(c, m.CheckForUpdates(), true, false, false) {
				a.upd.Check()
			}
		})
	})
	a.settingsSection(c, tok, icons.Info, m.AboutInfo(), func(g *settingsGroup) {
		value := func(s string) func() {
			return func() { ui.Text(c, s).FontSize(theme.Body).TextColor(tok.TextMuted).SingleLine() }
		}
		g.row(m.AboutVersion(), "", value(v))
		g.row(m.AboutPlatform(), "", value(runtime.GOOS+" / "+runtime.GOARCH))
		dir := a.p.DataDir
		note := dir
		if dir == "" {
			note = m.AboutDataInMemory()
		}
		g.row(m.AboutDataFolder(), note, func() {
			if dir != "" && a.p.OpenPath != nil && iconButton(c, "datadir", icons.OpenFile, m.OpenDataFolder()) {
				if err := a.p.OpenPath(dir); err != nil {
					c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
				}
			}
		})
		if !a.kc.PasswordProtected() {
			g.row(m.AboutSecrets(), "", value(m.SecretsKeychain()))
			return
		}
		g.row(m.AboutSecrets(), m.SecretsMasterPassword(), func() {
			if pill(c, m.UseSystemKeychainEllipsis(), false, false, false) {
				a.withSecrets(func() { a.openMasterPassword(masterPwSystem) })
			}
			if pill(c, m.ChangePasswordEllipsis(), false, false, false) {
				a.withSecrets(func() { a.openMasterPassword(masterPwChange) })
			}
		})
	})
	a.settingsSection(c, tok, icons.OpenWith, m.AboutLinks(), func(g *settingsGroup) {
		link := func(key, url string) func() {
			return func() { ui.Link(c.Key(key), url[len("https://"):], url).FontSize(theme.Body) }
		}
		g.row(m.SourceCode(), "", link("repo", repoURL))
		g.row(m.License(), "", link("license", repoURL+"/blob/main/LICENSE"))
		g.row(m.ReportIssue(), "", link("issues", repoURL+"/issues"))
	})
}

// updatesSection is how SSHYard looks for updates: on its own or not, how
// often and through which proxy, with the update found.
func (a *App) updatesSection(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.settingsSection(c, tok, icons.Download, m.Updates(), func(g *settingsGroup) {
		g.row(m.CheckAutomatically(), "", func() {
			a.updateCheck = s.CheckUpdates
			if formSwitch(c.Key("autoupd"), &a.updateCheck).Label(m.CheckAutomatically()).Changed() {
				s.CheckUpdates = a.updateCheck
				*changed = true
			}
		})
		// The updater reads the interval at launch.
		g.row(m.UpdateInterval(), m.UpdateIntervalNote(), func() {
			a.updEvery = max(slices.Index(settings.UpdateIntervals, s.UpdateIntervalHours), 0)
			labels := make([]string, len(settings.UpdateIntervals))
			for i, h := range settings.UpdateIntervals {
				labels[i] = m.Hours(strconv.Itoa(h))
			}
			if formSegmented(c.Key("updevery"), &a.updEvery, false, labels...).Label(m.UpdateInterval()).Changed() {
				s.UpdateIntervalHours = settings.UpdateIntervals[a.updEvery]
				*changed = true
			}
		})
		// Checks and downloads go the way chosen, from the next request.
		g.row(m.FieldProxy(), m.ProxySystemNote(), func() {
			sel := a.proxySelect(c.Key("updproxy"), m, &a.updProxy, proxyPick{system: true, back: func(id string) {
				if id == "" {
					return
				}
				ns := a.settings
				ns.UpdateProxy = id
				if err := a.ApplySettings(ns); err != nil {
					a.toastError(m.ErrGeneric(err.Error()))
				}
			}})
			if sel.Changed() && a.updProxy != s.UpdateProxy {
				s.UpdateProxy = a.updProxy
				*changed = true
			}
		})
		switch nv := a.upd.Available(); {
		case !a.upd.Enabled():
			g.row(m.UpdatesDisabled(), "", func() {})
		case nv != "":
			g.row(m.UpdateAvailable(nv), "", func() {
				if pill(c, m.ViewUpdate(), true, false, false) {
					a.upd.Check()
				}
			})
		}
	})
}
