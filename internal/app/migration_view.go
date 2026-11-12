package app

import (
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/migration"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func (a *App) migrationSection(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ui.Text(c, m.MigrationReadOnly()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
	a.settingsSection(c, tok, icons.Import, m.MigrationGeneral(), func(g *settingsGroup) {
		g.row("OpenSSH", m.MigrationFormat("~/.ssh/config"), func() {
			if (IconButton{Icon: icons.Import, Label: m.ImportSSHConfig()}).Clicked(c, "migration:openssh") {
				a.OpenImport(m, "")
			}
			if a.p.PickFile != nil && (IconButton{Icon: icons.OpenFile, Label: m.ImportChooseFile()}).Clicked(c, "migration:openssh:file") {
				a.pickImportFile(m)
			}
		})
		g.row(m.MigrationText(), m.MigrationFormat("host=… port=… user=… password=… title=…"), func() {
			if (IconButton{Icon: icons.Edit, Label: m.MigrationTextInput()}).Clicked(c, "migration:text") {
				a.openTextMigration()
			}
			if a.p.PickFiles != nil && (IconButton{Icon: icons.OpenFile, Label: m.MigrationTextChooseFile()}).Clicked(c, "migration:text:file") {
				a.readMigration(m, migration.Text, false)
			}
		})
		g.row(m.MigrationCSV(), m.MigrationFormat("host,port,user,password,title"), func() {
			if a.p.PickFiles != nil && (IconButton{Icon: icons.OpenFile, Label: m.MigrationCSVChooseFile()}).Clicked(c, "migration:csv:file") {
				a.readMigration(m, migration.CSV, false)
			}
		})
	})
	sources := migration.Sources()
	groups := []struct {
		title      string
		start, end int
	}{{m.MigrationCommon(), 0, 3}, {m.MigrationCrossPlatform(), 3, 7}, {m.MigrationOther(), 7, 10}}
	formats := []string{".xsh", ".mxtsessions / .ini", ".reg / ~/.putty/sessions/", "user.sessions / .json", ".json", ".yaml / .yml", ".json / .csv", ".remmina", ".xml / .csv", ".ini"}
	for _, group := range groups {
		a.settingsSection(c, tok, icons.Import, group.title, func(g *settingsGroup) {
			for i := group.start; i < group.end; i++ {
				source := sources[i]
				g.row(string(source), m.MigrationFormat(formats[i]), func() {
					if a.p.PickFiles != nil && (IconButton{Icon: icons.OpenFile, Label: m.MigrationChooseSourceFiles(string(source))}).Clicked(c, "migration:files:"+string(source)) {
						a.readMigration(m, source, false)
					}
					if (source == migration.Xshell || source == migration.PuTTY || source == migration.Remmina) && a.p.PickFolder != nil && (IconButton{Icon: icons.Folder, Label: m.MigrationChooseSourceFolder(string(source))}).Clicked(c, "migration:folder:"+string(source)) {
						a.readMigration(m, source, true)
					}
				})
			}
		})
	}
}
func (a *App) migrationView(c *ui.Context, m locales.Messages) {
	f := a.migration
	if f == nil {
		return
	}
	tok := currentTokens(c)
	width, height := c.Size()
	source := string(f.source)
	switch f.source {
	case migration.Text:
		source = m.MigrationText()
	case migration.CSV:
		source = m.MigrationCSV()
	}
	previewing := !f.reading && !f.saving && !f.composing && f.done == nil
	var k migrationCheck
	if f.editable() && previewing {
		k = a.checkMigration(m, f)
	}
	sheet(stickyModal(c, &a.migrationOpen, func() {
		// The rows scroll between the controls and the buttons when the
		// window is too short for them. A text import's table takes most of
		// the window.
		col := ui.Column(c).Width(min(float32(720), max(float32(220), width-theme.Space(24))))
		if f.editable() && previewing {
			col = ui.Column(c).Width(min(float32(1320), max(float32(220), width-theme.Space(24)))).Height(max(float32(200), height-theme.Space(24)))
		}
		col.Gap(theme.Space(3)).Shrink(1).MinHeight(0).Children(func() {
			switch {
			case f.composing:
				sheetTitle(c, m.MigrationText())
			case f.done != nil:
				sheetTitle(c, m.MigrationResultTitle())
			default:
				sheetTitle(c, m.MigrationPreview(source))
			}
			switch {
			case f.reading:
				ui.Text(c, m.MigrationReading()).FontSize(theme.Body)
			case f.saving:
				ui.Text(c, m.MigrationSaving()).FontSize(theme.Body)
			case f.composing:
				a.migrationComposer(c, m, tok, f, max(float32(100), height-theme.Space(80)))
			case f.done != nil:
				migrationOutcomeView(c, m, tok, *f.done)
			default:
				a.migrationPreview(c, m, tok, f, k, max(float32(100), height-theme.Space(105)))
			}
			if n := len(k.problems); n > 0 {
				ui.Text(c, m.MigrationFixRows(strconv.Itoa(n))).TextColor(tok.Danger).FontSize(theme.Footnote)
			}
			if f.err != "" {
				ui.Text(c, f.err).TextColor(tok.Danger).FontSize(theme.Footnote)
			}
			ui.Row(c).Gap(theme.Space(2)).FillWidth().Children(func() {
				ui.Spacer(c)
				switch {
				case f.done != nil:
					if pill(c, m.Close(), true, false, false) {
						a.closeMigration()
					}
				case f.composing:
					if pill(c, m.Cancel(), false, false, false) {
						a.closeMigration()
					}
					if pill(c, m.MigrationTextPreview(), true, false, strings.TrimSpace(f.text) == "") {
						a.readTextMigration(m)
					}
				default:
					if pill(c, m.Cancel(), false, false, f.saving) {
						a.closeMigration()
					}
					if pill(c, m.ImportButton(strconv.Itoa(f.count())), true, false, f.reading || f.saving || f.count() == 0 || k.blocking > 0) {
						a.startMigrationSave(m)
					}
				}
			})
		})
	}), tok)
	if !a.migrationOpen {
		if f.saving {
			a.migrationOpen = true
		} else {
			a.closeMigration()
		}
	}
}
func (a *App) migrationPreview(c *ui.Context, m locales.Messages, tok theme.Tokens, f *migrationForm, k migrationCheck, height float32) {
	if !f.editable() {
		ui.Text(c, m.MigrationReadOnly()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
	}
	if f.encoding != "" {
		ui.Row(c).Gap(theme.Space(2)).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, m.MigrationEncodingNote(string(f.encoding))).FontSize(theme.Footnote).TextColor(tok.TextMuted).Grow(1).Shrink(1)
			choice := string(f.encoding)
			var options []string
			for _, e := range migration.Encodings() {
				options = append(options, string(e))
			}
			if formSelect(c.Key("migration:encoding"), &choice, options).Label(m.MigrationEncoding()).Changed() {
				a.rereadMigration(m, migration.Encoding(choice))
			}
		})
	}
	ui.Row(c).Gap(theme.Space(2)).AlignItems(ui.Center).FillWidth().Children(func() {
		in := formTextInput(c.Key("migration:search"), &f.search).Label(m.SearchHosts()).Placeholder(m.SearchHosts()).AutoFocus().Grow(1).Shrink(1).MinWidth(0)
		a.migrationHold.apply(c, f, in)
		if pill(c, m.MigrationSelectAll(), false, false, false) {
			f.selectVisible(true)
		}
		if pill(c, m.MigrationSelectNone(), false, false, false) {
			f.selectVisible(false)
		}
	})
	if f.editable() {
		choices, groups := a.hostAuthChoices(m), a.migrationGroups(f)
		a.migrationBatchView(c, m, tok, f, choices, groups)
		for _, issue := range f.issues {
			warnLine(c, tok, migrationIssue(m, issue))
		}
		a.migrationTable(c, m, tok, f, k, choices, groups)
		return
	}
	ui.Row(c).Gap(theme.Space(2)).FillWidth().Children(func() {
		formTextInput(c.Key("migration:group"), &f.group).Label(m.FieldGroup()).Placeholder(m.FieldGroup()).Grow(1).Shrink(1)
		if pill(c, m.MigrationApplyGroup(), false, false, false) {
			for _, r := range f.rows {
				if r.selected {
					r.group = f.group
				}
			}
		}
	})
	formCheckbox(c.Key("migration:secrets"), &f.saveSecrets, m.MigrationSaveSecrets())
	ui.Text(c, m.MigrationSecretsNote()).FontSize(theme.Caption).TextColor(tok.TextMuted)
	sheetScroll(ui.Scroll(c.Key("migration:rows")).MaxHeight(height).Shrink(1).MinHeight(0)).Children(func() {
		ui.Column(c).Gap(theme.Space(2)).FillWidth().Children(func() {
			for _, issue := range f.issues {
				warnLine(c, tok, migrationIssue(m, issue))
			}
			for i, r := range f.rows {
				if f.visible(r) {
					a.migrationRowView(c, m, tok, f, r, i)
				}
			}
		})
	})
}

// migrationConflictView offers the choices for a row whose target a saved
// host (or an earlier row) has.
func migrationConflictView(c *ui.Context, m locales.Messages, tok theme.Tokens, key string, r *migrationRow) {
	ui.Text(c, m.MigrationConflict()).FontSize(theme.Caption).TextColor(tok.Warning)
	labels := importActions(m)
	action := labels[int(r.action)]
	formSelect(c.Key(key+":action"), &action, labels).Label(m.ImportConflictAction()).FillWidth()
	for i, label := range labels {
		if action == label {
			r.action = migrationAction(i)
		}
	}
}
func (a *App) migrationRowView(c *ui.Context, m locales.Messages, tok theme.Tokens, f *migrationForm, r *migrationRow, i int) {
	key := "migration:row:" + strconv.Itoa(i)
	ui.Column(c.Key(key)).Padding(theme.Space(2)).Gap(theme.Space(2)).FillWidth().Radius(theme.RadiusControl).Border(1, tok.Line).Children(func() {
		ui.Row(c).Gap(theme.Space(2)).FillWidth().Children(func() {
			formCheckbox(c.Key(key+":pick"), &r.selected, "").Label(m.ImportPick(r.name))
			formTextInput(c.Key(key+":name"), &r.name).Label(m.FieldName()).Grow(1).Shrink(1)
		})
		ui.Text(c, r.entry.User+"@"+r.entry.Address+":"+strconv.Itoa(r.entry.Port)).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine().Shrink(1)
		formTextInput(c.Key(key+":group"), &r.group).Label(m.FieldGroup()).Placeholder(m.FieldGroup()).FillWidth()
		conflicts, _ := f.choices()
		if conflicts[r] {
			migrationConflictView(c, m, tok, key, r)
		}
		for _, issue := range r.entry.Issues {
			warnLine(c, tok, migrationIssue(m, issue))
		}
		if len(r.entry.Jumps) > 0 {
			ui.Text(c, m.MigrationJumpCount(strconv.Itoa(len(r.entry.Jumps)))).FontSize(theme.Footnote).TextColor(tok.TextMuted)
		}
		if f.saveSecrets && (len(r.entry.Password) > 0 || len(r.entry.PrivateKey) > 0 || r.entry.KeyPath != "") {
			formCheckbox(c.Key(key+":secret"), &r.saveSecret, m.MigrationSaveRowSecrets())
		}
		if f.saveSecrets && (len(r.entry.PrivateKey) > 0 || r.entry.KeyPath != "") {
			ui.Text(c, r.entry.KeyPath).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
			if a.p.PickFile != nil && (IconButton{Icon: icons.OpenFile, Label: m.MigrationRelocateKey()}).Clicked(c, key+":key") {
				a.pickMigrationKey(m, f, func(path string) {
					r.entry.KeyPath = path
					r.entry.PrivateKey = nil
				})
			}
			formTextInput(c.Key(key+":pass"), &r.pass).Password().Label(m.FieldPassphrase()).Placeholder(m.FieldPassphrase()).FillWidth()
		}
	})
}
