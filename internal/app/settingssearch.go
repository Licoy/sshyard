package app

import (
	"slices"
	"time"

	"github.com/Licoy/sshyard/internal/highlight"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/migration"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The settings' search finds the pages and the settings on them by what
// they are called, in the sidebar's field: the matches show by page in
// place of the page, and choosing one opens its page at it. The command
// palette finds the same settings.

// prefSearchState is the settings' search: what is typed, the match
// chosen, and the row an opened page scrolls to and lights.
type prefSearchState struct {
	query string
	sel   int
	// reveal scrolls to the match chosen, after the keyboard moved it
	reveal bool
	// focus gives the field the keyboard in the next frame
	focus bool
	// row is the label of the row or section the page scrolls to when it
	// is next drawn, flash the one lit until flashTill
	row       string
	flash     string
	flashTill time.Time
}

// prefFlashFor is how long a row found stays lit.
const prefFlashFor = 1500 * time.Millisecond

// settingEntry is a setting the search finds, on its page: the label of
// its row (or the title of its section) and the note under it, if fixed.
type settingEntry struct {
	page  prefPage
	label func(locales.Messages) string
	note  func(locales.Messages) string
}

// settingEntries are the settings on the pages, by page in the sidebar's
// order. A new row or section of a page is added here
// (TestSettingsIndexCoversPages tells of one missing).
var settingEntries = []settingEntry{
	{prefGeneral, locales.Messages.Language, nil},
	{prefGeneral, locales.Messages.RememberLayout, locales.Messages.RememberLayoutNote},
	{prefGeneral, locales.Messages.ToolPanes, locales.Messages.ToolPanesNote},
	{prefGeneral, locales.Messages.RenewalNotify, locales.Messages.RenewalNotifyNote},
	{prefGeneral, locales.Messages.WindowTraySection, nil},
	{prefGeneral, locales.Messages.CloseBehavior, locales.Messages.CloseBehaviorNote},
	{prefGeneral, locales.Messages.TrayIconStyle, locales.Messages.TrayIconStyleNote},
	{prefGeneral, locales.Messages.TrayClick, locales.Messages.TrayClickNote},
	{prefGeneral, locales.Messages.TrayMaterial, locales.Messages.TrayMaterialNote},
	{prefGeneral, locales.Messages.Updates, nil},
	{prefGeneral, locales.Messages.CheckAutomatically, nil},
	{prefGeneral, locales.Messages.UpdateInterval, locales.Messages.UpdateIntervalNote},
	{prefGeneral, locales.Messages.FieldProxy, locales.Messages.ProxySystemNote},
	{prefAppearance, locales.Messages.Theme, nil},
	{prefAppearance, locales.Messages.UiZoom, locales.Messages.UiZoomDesc},
	{prefAppearance, locales.Messages.BackgroundSection, locales.Messages.BackgroundNote},
	{prefAppearance, locales.Messages.BackgroundFit, nil},
	{prefAppearance, locales.Messages.MaterialSection, nil},
	{prefAppearance, locales.Messages.MaterialChrome, nil},
	{prefAppearance, locales.Messages.MaterialContent, locales.Messages.MaterialContentNote},
	{prefAppearance, locales.Messages.FontsSection, nil},
	{prefAppearance, locales.Messages.InterfaceFont, nil},
	{prefTerminal, locales.Messages.ColorScheme, nil},
	{prefTerminal, locales.Messages.SchemeImport, locales.Messages.SchemeImportNote},
	{prefTerminal, locales.Messages.FontsSection, nil},
	{prefTerminal, locales.Messages.CursorShape, locales.Messages.CursorShapeNote},
	{prefTerminal, locales.Messages.TerminalCursorBlink, locales.Messages.TerminalCursorBlinkNote},
	{prefTerminal, locales.Messages.TerminalLineNumbers, locales.Messages.TerminalLineNumbersNote},
	{prefTerminal, locales.Messages.TerminalTimestamps, locales.Messages.TerminalTimestampsNote},
	{prefTerminal, locales.Messages.TerminalFont, nil},
	{prefTerminal, locales.Messages.TerminalFallbackFont, locales.Messages.TerminalFallbackFontNote},
	{prefTerminal, locales.Messages.MonospacedOnly, locales.Messages.MonospacedOnlyNote},
	{prefTerminal, locales.Messages.FontSize, locales.Messages.FontSizeNote},
	{prefTerminal, locales.Messages.TerminalLineHeight, locales.Messages.TerminalLineHeightNote},
	{prefTerminal, locales.Messages.TerminalCharWidth, locales.Messages.TerminalCharWidthNote},
	{prefTerminal, locales.Messages.HighlightSection, nil},
	{prefTerminal, locales.Messages.HighlightOn, locales.Messages.HighlightOnNote},
	{prefTerminal, locales.Messages.HighlightOverride, locales.Messages.HighlightOverrideNote},
	{prefTerminal, locales.Messages.HighlightFullScreen, locales.Messages.HighlightFullScreenNote},
	{prefTerminal, locales.Messages.HighlightPresets, nil},
	{prefTerminal, locales.Messages.HighlightRules, nil},
	{prefTerminal, locales.Messages.LocalTerminal, nil},
	{prefTerminal, locales.Messages.OpenLocalOnStart, locales.Messages.OpenLocalOnStartNote},
	{prefTerminal, locales.Messages.DefaultShell, locales.Messages.DefaultShellNote},
	{prefTerminal, locales.Messages.ConnectionSection, nil},
	{prefTerminal, locales.Messages.KeepAlive, nil},
	{prefTerminal, locales.Messages.AutoReconnect, nil},
	{prefTerminal, locales.Messages.TermType, locales.Messages.TermTypeNote},
	{prefTerminal, locales.Messages.ClipboardSection, nil},
	{prefTerminal, locales.Messages.RightClick, locales.Messages.RightClickNote},
	{prefTerminal, locales.Messages.CopyOnSelect, locales.Messages.CopyOnSelectNote},
	{prefTerminal, locales.Messages.Osc52Title, locales.Messages.Osc52Note},
	{prefTerminal, locales.Messages.Osc52Limit, locales.Messages.Osc52LimitHint},
	{prefTerminal, locales.Messages.Osc52Notify, locales.Messages.Osc52NotifyNote},
	{prefMonitoring, locales.Messages.MetricsBar, locales.Messages.MetricsBarNote},
	{prefMonitoring, locales.Messages.MetricsInterval, nil},
	{prefMonitoring, locales.Messages.MetricsMount, locales.Messages.MetricsMountNote},
	{prefFiles, locales.Messages.TransferConcurrency, locales.Messages.TransferConcurrencyHint},
	{prefFiles, locales.Messages.OpenConfirmMB, locales.Messages.OpenConfirmHint},
	{prefFiles, locales.Messages.FilesEdge, locales.Messages.FilesEdgeNote},
	{prefFiles, locales.Messages.FilesZoom, locales.Messages.FilesZoomNote},
	{prefAI, locales.Messages.AiDefaultAgent, nil},
	{prefAI, locales.Messages.AiACP, nil},
	{prefAI, locales.Messages.AiContextLines, nil},
	{prefAI, locales.Messages.AiMaxToolCalls, locales.Messages.AiMaxToolCallsNote},
	{prefAI, locales.Messages.AiRequestTimeout, locales.Messages.AiRequestTimeoutNote},
	{prefAI, locales.Messages.AiAuditDays, nil},
	{prefAI, locales.Messages.AiRedact, locales.Messages.AiRedactNote},
	{prefAI, locales.Messages.AiConsentSection, nil},
	{prefAI, locales.Messages.AiPermissionSection, nil},
	{prefAI, locales.Messages.AiDefaultMode, nil},
	{prefAI, locales.Messages.AiReviewProvider, locales.Messages.AiReviewProviderNote},
	{prefAI, locales.Messages.AiReviewStrictness, locales.Messages.AiReviewStrictnessNote},
	{prefAI, locales.Messages.AiReviewWith, nil},
	{prefAI, locales.Messages.AiReviewKind, nil},
	{prefAI, locales.Messages.AiReviewModelName, locales.Messages.AiReviewModelNote},
	{prefAI, locales.Messages.AiProvidersSection, nil},
	{prefAgents, locales.Messages.ApiSection, nil},
	{prefAgents, locales.Messages.McpEnable, locales.Messages.McpEnableNote},
	{prefAgents, locales.Messages.FieldPort, nil},
	{prefAgents, locales.Messages.ApiAddress, nil},
	{prefAgents, locales.Messages.McpToken, locales.Messages.McpTokenNote},
	{prefAgents, locales.Messages.McpSection, nil},
	{prefAgents, locales.Messages.McpAddress, nil},
	{prefAgents, locales.Messages.McpSessions, nil},
	{prefAgents, locales.Messages.SkillSection, nil},
	{prefAgents, locales.Messages.SkillAbout, locales.Messages.SkillAboutNote},
	{prefCredentials, locales.Messages.LocalKeysSection, nil},
	{prefCredentials, locales.Messages.LocalKeysTry, locales.Messages.LocalKeysTryNote},
	{prefCredentials, locales.Messages.LocalKeysMCP, locales.Messages.LocalKeysMCPNote},
	{prefMigration, locales.Messages.MigrationGeneral, nil},
	{prefMigration, func(locales.Messages) string { return "OpenSSH" }, nil},
	{prefMigration, locales.Messages.MigrationText, nil},
	{prefMigration, locales.Messages.MigrationCSV, nil},
	{prefMigration, locales.Messages.MigrationCommon, nil},
	{prefMigration, locales.Messages.MigrationCrossPlatform, nil},
	{prefMigration, locales.Messages.MigrationOther, nil},
	{prefBackup, locales.Messages.ExportBackup, locales.Messages.ExportBackupNote},
	{prefBackup, locales.Messages.RestoreBackup, locales.Messages.RestoreBackupNote},
	{prefBackup, locales.Messages.DefaultBackupPassword, locales.Messages.DefaultBackupPasswordNote},
	{prefBackup, locales.Messages.CloudBackupSection, nil},
	{prefAbout, locales.Messages.AboutInfo, nil},
	{prefAbout, locales.Messages.AboutVersion, nil},
	{prefAbout, locales.Messages.AboutPlatform, nil},
	{prefAbout, locales.Messages.AboutDataFolder, nil},
	{prefAbout, locales.Messages.AboutSecrets, nil},
	{prefAbout, locales.Messages.AboutLinks, nil},
	{prefAbout, locales.Messages.SourceCode, nil},
	{prefAbout, locales.Messages.License, nil},
	{prefAbout, locales.Messages.ReportIssue, nil},
}

// settingsIndex is everything the search finds on the pages: the
// settings above, with the highlighting presets, the sources to migrate
// from and the actions with a shortcut, as their pages list them.
func settingsIndex() []settingEntry {
	out := slices.Clone(settingEntries)
	for _, p := range highlight.Presets {
		id := p.ID
		out = append(out, settingEntry{prefTerminal, func(m locales.Messages) string { return presetName(m, id) }, nil})
	}
	for _, src := range migration.Sources() {
		out = append(out, settingEntry{prefMigration, func(locales.Messages) string { return string(src) }, nil})
	}
	for _, ac := range actions {
		out = append(out, settingEntry{prefShortcuts, ac.label, nil})
	}
	return out
}

// settingHit is a page or a setting the search found; a page itself has
// no label.
type settingHit struct {
	page  prefPage
	label string
	note  string
}

// settingHits are the hits on one page, the page first if it matched,
// then its settings, the best first.
type settingHits struct {
	item prefItem
	hits []settingHit
}

// settingsMatches are the pages and settings matching q in m, by page,
// the page with the best match first; none for an empty q. A setting
// matches by its label and note in m, and by its label in English.
func settingsMatches(m locales.Messages, q string) []settingHits {
	words := splitWords(q)
	if len(words) == 0 {
		return nil
	}
	en := locales.In("en")
	type scored struct {
		hit   settingHit
		score int
	}
	byPage := map[prefPage][]scored{}
	enGroups := prefGroups(en)
	for _, g := range prefGroups(m) {
		for _, it := range g.items {
			enIt := prefItemOf(enGroups, it.page)
			if s := paletteScore(paletteItem{title: it.title, sub: it.desc, also: enIt.title + " " + enIt.desc}, words); s > 0 {
				// the page goes before its settings, whatever their score
				byPage[it.page] = append(byPage[it.page], scored{settingHit{page: it.page}, s + 1000})
			}
		}
	}
	seen := map[settingHit]bool{}
	for _, e := range settingsIndex() {
		h := settingHit{page: e.page, label: e.label(m)}
		if seen[h] {
			continue
		}
		seen[h] = true
		if e.note != nil {
			h.note = e.note(m)
		}
		if s := paletteScore(paletteItem{title: h.label, sub: h.note, also: e.label(en)}, words); s > 0 {
			byPage[e.page] = append(byPage[e.page], scored{h, s})
		}
	}
	var out []settingHits
	best := map[prefPage]int{}
	for _, g := range prefGroups(m) {
		for _, it := range g.items {
			list := byPage[it.page]
			if len(list) == 0 {
				continue
			}
			slices.SortStableFunc(list, func(x, y scored) int { return y.score - x.score })
			hs := settingHits{item: it}
			for _, sc := range list {
				hs.hits = append(hs.hits, sc.hit)
				best[it.page] = max(best[it.page], sc.score%1000)
			}
			out = append(out, hs)
		}
	}
	slices.SortStableFunc(out, func(x, y settingHits) int { return best[y.item.page] - best[x.item.page] })
	return out
}

// prefItemOf is page p's item in groups.
func prefItemOf(groups []prefGroup, p prefPage) prefItem {
	for _, g := range groups {
		for _, it := range g.items {
			if it.page == p {
				return it
			}
		}
	}
	return prefItem{}
}

// openSetting shows the settings on page p, scrolled to the row or the
// section labelled label (none for the top of the page), lit a moment.
func (a *App) openSetting(p prefPage, label string) {
	a.openSettings(p)
	a.prefSearch.query, a.prefSearch.sel = "", 0
	a.prefSearch.row, a.prefSearch.flash, a.prefSearch.flashTill = label, label, time.Time{}
}

// revealRow scrolls to el, the row or section labelled label, when the
// search opened its page at it, and lights lit (el, or a section's
// title) a moment.
func (a *App) revealRow(c *ui.Context, tok theme.Tokens, label string, el, lit ui.Element) {
	if settingsRowDrawn != nil {
		settingsRowDrawn(label)
	}
	ps := &a.prefSearch
	if label == "" || label != ps.flash {
		return
	}
	now := c.Now()
	if label == ps.row {
		el.ScrollIntoView()
		ps.row, ps.flashTill = "", now.Add(prefFlashFor)
	}
	if now.Before(ps.flashTill) {
		lit.Background(tok.Accent.Alpha(0.12)).Radius(theme.RadiusCard)
		c.After(ps.flashTill.Sub(now))
		return
	}
	ps.flash = ""
}

// settingsRowDrawn hears of each row and section drawn on a settings
// page, by its label; tests set it.
var settingsRowDrawn func(label string)

// settingsSearchField is the search at the top of the settings' sidebar:
// Up and Down choose a match, Enter opens it, Escape empties the field.
func (a *App) settingsSearchField(c *ui.Context, m locales.Messages, tok theme.Tokens, hits []settingHits) {
	ps := &a.prefSearch
	ui.Box(c).FillWidth().Shrink(0).Padding(theme.Space(3), theme.Space(2.5), theme.Space(1), theme.Space(2.5)).Children(func() {
		in := searchField(c, m, tok, "pref:search", &ps.query, m.SearchSettings())
		if ps.focus {
			in.Focus()
			ps.focus = false
		}
		n := 0
		for _, h := range hits {
			n += len(h.hits)
		}
		in.OnTextKey(func(k ui.TextKey) bool {
			if k.Mods != 0 || n == 0 {
				return false
			}
			switch k.Key {
			case ui.KeyDown:
				ps.sel++
			case ui.KeyUp:
				ps.sel--
			default:
				return false
			}
			ps.reveal = true
			return true
		})
		if in.Changed() {
			ps.sel = 0
		}
		ps.sel = max(0, min(ps.sel, n-1))
		if in.Submitted() && n > 0 {
			h := flatHits(hits)[ps.sel]
			a.openSetting(h.page, h.label)
		}
	})
}

// flatHits are the hits of all pages, in the order they show.
func flatHits(hits []settingHits) []settingHit {
	var out []settingHit
	for _, h := range hits {
		out = append(out, h.hits...)
	}
	return out
}

// settingsResults shows the matches in place of a page: a card for each
// page, with a row for each match; a click opens the page at it.
func (a *App) settingsResults(c *ui.Context, m locales.Messages, tok theme.Tokens, hits []settingHits) {
	ps := &a.prefSearch
	ui.Scroll(c.Key("prefsearch")).Fill().Children(func() {
		ui.Column(c).Padding(theme.Space(7), theme.Space(8), theme.Space(10), theme.Space(8)).Gap(theme.Space(6)).MaxWidth(720).FillWidth().Children(func() {
			if len(hits) == 0 {
				ui.Text(c, m.SettingsNoResults()).FontSize(theme.Body).TextColor(tok.TextMuted).Padding(0, theme.Space(1))
				return
			}
			i := 0
			for _, h := range hits {
				ui.Column(c).Gap(8).FillWidth().Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 4).Children(func() {
						glyph(c, h.item.icon, tok.Accent, 20)
						ui.Text(c, h.item.title).FontSize(theme.Body).FontWeight(600)
					})
					a.contentCard(c, tok).FillWidth().Children(func() {
						for j, hit := range h.hits {
							if j > 0 {
								ui.Box(c).FillWidth().Padding(0, 0, 0, 14).Children(func() { ui.Box(c).Height(1).FillWidth().Background(tok.Line) })
							}
							if a.settingResult(c, tok, h.item, hit, i == ps.sel) {
								a.openSetting(hit.page, hit.label)
							}
							i++
						}
					})
				})
			}
		})
	})
	ps.reveal = false
}

// settingResult is one match's row: its label and note, or a page's
// title and description; it reports a click.
func (a *App) settingResult(c *ui.Context, tok theme.Tokens, it prefItem, hit settingHit, chosen bool) bool {
	label, note := hit.label, hit.note
	if label == "" {
		label, note = it.title, it.desc
	}
	row := ui.ButtonBase(c.Key("prefhit:"+string(hit.page)+":"+hit.label)).Label(label).FillWidth().MinHeight(48).
		Padding(8, 14).Gap(16).Radius(theme.RadiusCard).AlignItems(ui.Center).Justify(ui.Start).Cursor(ui.CursorPointer)
	switch {
	case chosen:
		row.Background(tok.Press)
		if a.prefSearch.reveal {
			row.ScrollIntoView()
		}
	case row.Hovered():
		row.Background(tok.Hover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(theme.Body).FontWeight(500)
			if note != "" {
				ui.Text(c, note).FontSize(theme.Caption).TextColor(tok.TextMuted)
			}
		})
		ui.Icon(c, icons.Submenu.SVG).Size(14, 14).Shrink(0).TextColor(tok.TextMuted)
	})
	return row.Clicked()
}
