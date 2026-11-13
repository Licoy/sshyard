package app

import (
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keys"
	"github.com/Licoy/sshyard/internal/migration"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

type migrationAction uint8

const (
	migrationSkip migrationAction = iota
	migrationOverwrite
	migrationKeepBoth
)

type migrationRow struct {
	entry             migration.Entry
	name, group, pass string
	selected          bool
	saveSecret        bool
	action            migrationAction
	// A text import edits every field and chooses the login: port is the
	// port typed, auth and cred the login (set only for such rows), and
	// password the password; a key is entry.PrivateKey or entry.KeyPath,
	// opened with pass. problem is why the last save refused the row.
	port, password string
	auth           assets.AuthMethod
	cred           string
	problem        string
}
type migrationForm struct {
	source                       migration.Source
	rows                         []*migrationRow
	issues                       []migration.Issue
	baseline                     []assets.Host
	search, group, err           string
	saveSecrets, reading, saving bool
	// costly is set once the user agreed to derive the batch's PuTTY keys
	// whose passphrase derivation costs more than usual (off the UI
	// thread, as the batch is saved)
	costly bool
	// composing shows the text area of a text import, text its content;
	// batch edits the selected rows, table keeps the place of its rows,
	// done is what the saved import did
	composing bool
	text      string
	batch     migrationBatch
	table     ui.ListState
	done      *migrationOutcome
	// paths are the files read, encoding the legacy code page they were
	// decoded in (empty when none was); choosing another reads them again
	paths    []string
	encoding migration.Encoding
}

// migrationOutcome counts what a text import did with its rows.
type migrationOutcome struct{ added, updated, skipped int }

// editable reports a text or CSV import, whose rows edit every field.
func (f *migrationForm) editable() bool {
	return f.source == migration.Text || f.source == migration.CSV
}

func migrationTarget(address string, port int, user string) string {
	address = strings.Trim(strings.TrimSpace(address), "[]")
	if ip, err := netip.ParseAddr(address); err == nil {
		address = ip.Unmap().String()
	}
	if port == 0 {
		port = 22
	}
	return strings.ToLower(address) + "\x00" + strconv.Itoa(port) + "\x00" + user
}
func (a *App) newMigration(source migration.Source, result migration.Result) *migrationForm {
	f := &migrationForm{source: source, issues: result.Issues, baseline: a.store.List(), encoding: result.Encoding}
	// A text import's rows choose their login, which saves its secret.
	f.saveSecrets = f.editable()
	saved := f.saved()
	for _, e := range result.Entries {
		if e.Port == 0 {
			e.Port = 22
		}
		if e.User == "" {
			e.User = a.localUser()
		}
		r := &migrationRow{entry: e, name: e.Name, group: e.Group, selected: true, saveSecret: true}
		if f.editable() {
			a.editableRow(r, saved)
		} else {
			// An invalid row starts unselected so it does not hold back the others.
			r.selected = !slices.ContainsFunc(e.Issues, func(i migration.Issue) bool { return i.Code == "invalid-entry" })
		}
		f.rows = append(f.rows, r)
	}
	return f
}

// editableRow readies a text import's row: its fields are checked as they
// are edited, and it logs in with its password, else as the saved host
// it would overwrite does, else by asking.
func (a *App) editableRow(r *migrationRow, saved map[string]assets.Host) {
	e := &r.entry
	// A copy: deleting in place would rewrite the entry's issues in result.
	e.Issues = slices.DeleteFunc(slices.Clone(e.Issues), func(i migration.Issue) bool { return i.Code == "invalid-entry" })
	if e.Port > 0 {
		r.port = strconv.Itoa(e.Port)
	}
	r.password = string(e.Password)
	clear(e.Password)
	e.Password = nil
	r.auth = assets.AuthAsk
	if old, ok := saved[migrationTarget(e.Address, e.Port, e.User)]; ok {
		r.auth, r.cred = old.Auth, old.CredentialID
	}
	if r.password != "" {
		r.auth, r.cred = assets.AuthPassword, ""
	}
}

// saved maps the target of each saved host to it.
func (f *migrationForm) saved() map[string]assets.Host {
	out := map[string]assets.Host{}
	for _, h := range f.baseline {
		out[migrationTarget(h.Address, h.Port, h.User)] = h
	}
	return out
}

// overwrites is the saved host the row replaces, nil for a new one.
func (f *migrationForm) overwrites(saved map[string]assets.Host, r *migrationRow) *assets.Host {
	if r.action != migrationOverwrite {
		return nil
	}
	if h, ok := saved[migrationTarget(r.entry.Address, r.entry.Port, r.entry.User)]; ok {
		return &h
	}
	return nil
}
func (f *migrationForm) visible(r *migrationRow) bool {
	return strings.Contains(strings.ToLower(r.name+" "+r.entry.Address+" "+r.entry.User+" "+r.group), strings.ToLower(f.search))
}
func (f *migrationForm) selectVisible(selected bool) {
	for _, r := range f.rows {
		if f.visible(r) {
			r.selected = selected
		}
	}
}

// migrationChoices derives conflicts from the current selection, including
// internal duplicates whose earlier row may have been deselected.
func (f *migrationForm) choices() (map[*migrationRow]bool, map[string]*migrationRow) {
	conflicts := map[*migrationRow]bool{}
	targets := map[string]string{}
	final := map[string]*migrationRow{}
	for _, h := range f.baseline {
		targets[migrationTarget(h.Address, h.Port, h.User)] = h.ID
	}
	for i, r := range f.rows {
		target := migrationTarget(r.entry.Address, r.entry.Port, r.entry.User)
		id, exists := targets[target]
		conflicts[r] = exists
		if !r.selected || exists && r.action == migrationSkip {
			continue
		}
		if !exists || r.action == migrationKeepBoth {
			id = "row:" + strconv.Itoa(i)
		}
		targets[target] = id
		final[id] = r
	}
	return conflicts, final
}
func (f *migrationForm) count() int { _, rows := f.choices(); return len(rows) }
func (f *migrationForm) needsSecrets() bool {
	if !f.saveSecrets {
		return false
	}
	_, rows := f.choices()
	saved := f.saved()
	for _, r := range rows {
		e := r.entry
		if r.auth != "" {
			// A secret given is written; another login deletes those of the
			// host it overwrites, and a blank one of its own login keeps them.
			old := f.overwrites(saved, r)
			given := r.auth == assets.AuthPassword && r.password != "" || r.auth == assets.AuthKey && (len(e.PrivateKey) > 0 || e.KeyPath != "")
			if given || old != nil && old.Auth != r.auth && (old.Auth == assets.AuthPassword || old.Auth == assets.AuthKey) {
				return true
			}
			continue
		}
		if r.saveSecret && (len(e.Password) > 0 || len(e.PrivateKey) > 0 || e.KeyPath != "") {
			return true
		}
	}
	return false
}

// forget drops the secrets the form holds.
func (f *migrationForm) forget() {
	for _, r := range f.rows {
		clear(r.entry.Password)
		clear(r.entry.PrivateKey)
		clear(r.entry.Passphrase)
		r.pass, r.password = "", ""
	}
	f.text = ""
	f.batch.password, f.batch.pass = "", ""
}
func (a *App) closeMigration() {
	if f := a.migration; f != nil && !f.saving {
		f.forget()
		a.migration = nil
		a.migrationOpen = false
	}
}

// openTextMigration shows the text area of a text import.
func (a *App) openTextMigration() {
	a.migration, a.migrationOpen = &migrationForm{source: migration.Text, composing: true}, true
}

// readTextMigration reads the text area's host list into the preview.
func (a *App) readTextMigration(m locales.Messages) {
	f := a.migration
	if f == nil || !f.composing {
		return
	}
	result, err := migration.ParseText(f.text)
	if err != nil {
		f.err = migrationReadError(m, err)
		return
	}
	f.text = ""
	a.migration = a.newMigration(migration.Text, result)
}
func (a *App) readMigration(m locales.Messages, source migration.Source, directory bool) {
	f := &migrationForm{source: source, reading: true}
	a.migration, f.reading, a.migrationOpen = f, true, true
	go func() {
		var paths []string
		var err error
		if directory {
			var path string
			path, err = a.p.PickFolder(m.MigrationChooseFolder(), "")
			if path != "" {
				paths = []string{path}
			}
		} else {
			title := m.MigrationChooseFiles()
			switch source {
			case migration.Text:
				title = m.MigrationTextChooseFile()
			case migration.CSV:
				title = m.MigrationCSVChooseFile()
			}
			paths, err = a.p.PickFiles(title)
		}
		var result migration.Result
		if err == nil && len(paths) > 0 {
			result, err = migration.Read(source, paths, migration.Options{Prefer: migration.LanguageEncoding(i18n.Locale())})
		}
		a.post(func() {
			if a.migration != f {
				clearMigrationResult(result)
				return
			}
			f.reading = false
			if err != nil {
				clearMigrationResult(result)
				f.err = migrationReadError(m, err)
				return
			}
			if len(paths) == 0 {
				a.closeMigration()
				return
			}
			a.migration = a.readMigrationResult(m, source, paths, result)
		})
	}()
}

// rereadMigration reads the preview's files again in another legacy code
// page; on failure the preview stays as it was, with the error.
func (a *App) rereadMigration(m locales.Messages, encoding migration.Encoding) {
	f := a.migration
	if f == nil || f.reading || f.saving || f.done != nil || len(f.paths) == 0 || encoding == f.encoding {
		return
	}
	source, paths := f.source, f.paths
	f.reading, f.err = true, ""
	go func() {
		result, err := migration.Read(source, paths, migration.Options{Encoding: encoding})
		a.post(func() {
			if a.migration != f {
				clearMigrationResult(result)
				return
			}
			f.reading = false
			if err != nil {
				clearMigrationResult(result)
				f.err = migrationReadError(m, err)
				return
			}
			f.forget()
			a.migration = a.readMigrationResult(m, source, paths, result)
		})
	}()
}

// readMigrationResult is the preview of what paths read to.
func (a *App) readMigrationResult(m locales.Messages, source migration.Source, paths []string, result migration.Result) *migrationForm {
	next := a.newMigration(source, result)
	next.paths = paths
	if len(next.rows) == 0 {
		next.err = m.MigrationEmpty()
	}
	return next
}
func (a *App) startMigrationSave(m locales.Messages) {
	f := a.migration
	if f == nil || f.saving || f.reading || f.composing || f.done != nil || f.count() == 0 {
		return
	}
	if f.editable() && a.checkMigration(m, f).blocking > 0 {
		return
	}
	if f.needsSecrets() && !a.unlocked() {
		a.withSecrets(func() {
			if a.migration == f {
				a.startMigrationSave(m)
			}
		})
		return
	}
	f.saving = true
	f.err = ""
	for _, r := range f.rows {
		r.problem = ""
	}
	outcome := f.outcome()
	// Freeze editor input while the batch runs off the UI thread.
	go func() {
		count, err := a.saveMigration(m, f)
		a.post(func() {
			if a.migration != f {
				return
			}
			f.saving = false
			var rowErr *migrationRowError
			var ce *keys.CostError
			switch {
			case errors.As(err, &ce):
				// Nothing was saved or derived: asked, the batch runs again
				// with its costly keys allowed.
				a.askCost(&costAsk{memory: ce.Memory, passes: ce.Passes, run: func() {
					a.closeCost()
					if a.migration == f {
						f.costly = true
						a.startMigrationSave(m)
					}
				}})
				return
			case errors.As(err, &rowErr) && f.editable():
				rowErr.row.problem = rowErr.text
				return
			case err != nil:
				f.err = err.Error()
				return
			case f.editable():
				f.forget()
				f.done = &outcome
				return
			}
			a.closeMigration()
			a.toast(func(m locales.Messages) ui.Toast {
				return ui.Toast{Title: m.MigrationDone(strconv.Itoa(count)), Type: "success"}
			})
		})
	}()
}

// outcome counts the rows the selection adds, the saved hosts it
// overwrites and the rows left out.
func (f *migrationForm) outcome() migrationOutcome {
	_, final := f.choices()
	var o migrationOutcome
	for id := range final {
		if strings.HasPrefix(id, "row:") {
			o.added++
		} else {
			o.updated++
		}
	}
	o.skipped = len(f.rows) - len(final)
	return o
}
func (a *App) migrationUnchanged(f *migrationForm) bool {
	return reflect.DeepEqual(f.baseline, a.store.List())
}

func clearMigrationResult(result migration.Result) {
	for _, e := range result.Entries {
		clear(e.Password)
		clear(e.PrivateKey)
		clear(e.Passphrase)
	}
}

func migrationReadError(m locales.Messages, err error) string {
	var fe *migration.FormatError
	switch {
	case errors.Is(err, migration.ErrNoEntries):
		return m.MigrationEmpty()
	case errors.Is(err, migration.ErrTooLarge):
		return m.MigrationTooLarge()
	case errors.Is(err, migration.ErrNoSessions):
		return m.MigrationNoSessions()
	case errors.As(err, &fe) && fe.Line > 0:
		return m.MigrationFormatErrorAt(strconv.Itoa(fe.Line), fe.File)
	case errors.Is(err, migration.ErrFormat):
		return m.MigrationFormatError()
	case errors.Is(err, migration.ErrEncrypted):
		return m.MigrationEncryptedSource()
	default:
		return m.MigrationReadError()
	}
}
