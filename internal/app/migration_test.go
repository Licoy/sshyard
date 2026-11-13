package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/migration"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func migrationEntry(name, address string) migration.Entry {
	return migration.Entry{ID: name, Name: name, Address: address, Port: 22, User: "root"}
}
func migrationDraft(a *App, entries ...migration.Entry) *migrationForm {
	return a.newMigration(migration.Tabby, migration.Result{Entries: entries})
}
func TestMigrationSourcesAndPreview(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.store.Add(assets.Host{Name: "existing backup", Address: "backup.example.com", User: "root"}); err != nil {
		t.Fatal(err)
	}
	m := locales.In("en")
	var management []prefPage
	for _, g := range prefGroups(m) {
		if g.title == m.SettingsGroupManage() {
			for _, item := range g.items {
				management = append(management, item.page)
			}
		}
	}
	if !reflect.DeepEqual(management, []prefPage{prefCredentials, prefProxies, prefKnownHosts, prefMigration, prefBackup}) {
		t.Fatalf("manage order %v", management)
	}
	f := migrationDraft(a, migrationEntry("db", "db.example.com"), migrationEntry("backup", "backup.example.com"))
	a.migration, a.migrationOpen = f, true
	preview := ui.NewTester(func(c *ui.Context) { a.migrationView(c, locales.In(c.Locale())) }, 1000, 900)
	preview.Frame()
	if f.saveSecrets {
		t.Fatal("credentials opted in by default")
	}
	f.search = "db.example.com"
	preview.Frame()
	if preview.HasText("root@backup.example.com:22") {
		t.Fatal("search did not filter")
	}
	a.closeMigration()
	if a.migration != nil || a.migrationOpen {
		t.Fatal("cancel retained draft")
	}
}
func currentTestTokens(dark bool) theme.Tokens {
	if dark {
		return theme.DarkTokens()
	}
	return theme.LightTokens()
}
func TestMigrationSelectionAndConflictTargets(t *testing.T) {
	a, _ := newTestApp(t)
	first := migrationEntry("one", "2001:db8::1")
	second := migrationEntry("two", "[2001:0db8::1]")
	f := migrationDraft(a, first, second)
	conflicts, _ := f.choices()
	if !conflicts[f.rows[1]] || f.count() != 1 {
		t.Fatal("IPv6 duplicate not skipped")
	}
	f.search = "one"
	f.selectVisible(false)
	if f.rows[0].selected || !f.rows[1].selected {
		t.Fatal("search selection affected hidden row")
	}
	f.search = ""
	f.rows[0].selected = true
	f.rows[1].action = migrationKeepBoth
	n, err := a.saveMigration(en, f)
	if err != nil || n != 2 || a.store.Len() != 2 {
		t.Fatalf("keep both: count %d, err %v", n, err)
	}
	if migrationTarget("EXAMPLE.com", 22, "Root") == migrationTarget("example.com", 22, "root") {
		t.Fatal("user case was folded")
	}
}
func TestMigrationOverwritePreservesCredentialsAndRouting(t *testing.T) {
	for _, auth := range []assets.AuthMethod{assets.AuthPassword, assets.AuthCredential} {
		t.Run(string(auth), func(t *testing.T) {
			a, _ := newTestApp(t)
			jump, err := a.store.Add(assets.Host{Name: "jump", Address: "jump", User: "root"})
			if err != nil {
				t.Fatal(err)
			}
			h := assets.Host{Name: "old", Address: "host", User: "root", Auth: auth, JumpChain: []string{jump.ID}}
			if auth == assets.AuthCredential {
				cr, err := a.store.AddCredential(assets.Credential{ID: assets.NewID(), Name: "shared", Kind: assets.CredPassword})
				if err != nil {
					t.Fatal(err)
				}
				h.CredentialID = cr.ID
			}
			h, err = a.store.Add(h)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.kc.Set(keychain.Host(h.ID, keychain.Password), []byte("kept")); err != nil {
				t.Fatal(err)
			}
			f := migrationDraft(a, migrationEntry("new", "host"))
			f.rows[0].action = migrationOverwrite
			f.saveSecrets = true
			if f.needsSecrets() {
				t.Fatal("empty source requests unlock")
			}
			n, err := a.saveMigration(en, f)
			if err != nil || n != 1 {
				t.Fatalf("save %d %v", n, err)
			}
			got, _ := a.store.Get(h.ID)
			if got.Name != "new" || got.Auth != auth || got.CredentialID != h.CredentialID || !reflect.DeepEqual(got.JumpChain, h.JumpChain) {
				t.Fatalf("overwrite lost fields: %+v", got)
			}
			secret, err := a.kc.Get(keychain.Host(h.ID, keychain.Password))
			if err != nil || string(secret) != "kept" {
				t.Fatal("overwrite deleted original secret")
			}
		})
	}
}
func TestMigrationConsentPersistenceAndCache(t *testing.T) {
	a, _ := newTestApp(t)
	opts, _ := installBackupStore(t, a)
	path := filepath.Join(t.TempDir(), "hosts.json")
	var err error
	a.store, err = assets.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := migrationEntry("imported", "host")
	e.Password = []byte("migration-secret-test")
	f := migrationDraft(a, e)
	f.rows[0].entry.KeyPath = "/nonexistent/key"
	if f.needsSecrets() {
		t.Fatal("no consent requests credentials")
	}
	if n, err := a.saveMigration(en, f); err != nil || n != 1 {
		t.Fatalf("metadata import %d %v", n, err)
	}
	h := a.store.List()[0]
	if h.Auth != assets.AuthAsk {
		t.Fatal("unapproved credentials saved")
	}
	f = migrationDraft(a, e)
	f.saveSecrets = true
	f.rows[0].action = migrationOverwrite
	a.typed[keychain.Host(h.ID, keychain.Password)] = "stale"
	if n, err := a.saveMigration(en, f); err != nil || n != 1 {
		t.Fatalf("credential import %d %v", n, err)
	}
	if a.typed[keychain.Host(h.ID, keychain.Password)] != "" || a.typedGen[keychain.Host(h.ID, keychain.Password)] == 0 {
		t.Fatal("typed credential cache not invalidated")
	}
	metadata, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := os.ReadFile(opts.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadata, e.Password) || bytes.Contains(cipher, e.Password) {
		t.Fatal("secret exposed on disk")
	}
	reopened, err := assets.Open(path)
	if err != nil || reopened.Len() != 1 || reopened.List()[0].Auth != assets.AuthPassword {
		t.Fatal("metadata reopen failed")
	}
	if err := a.kc.Close(); err != nil {
		t.Fatal(err)
	}
	a.kc, err = keychain.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	password, err := a.kc.Get(keychain.Host(h.ID, keychain.Password))
	if err != nil || !bytes.Equal(password, e.Password) {
		t.Fatal("encrypted credential reopen failed")
	}
}
func TestMigrationInvalidRowStartsUnselected(t *testing.T) {
	a, _ := newTestApp(t)
	bad := migrationEntry("bad", "bad host")
	bad.Issues = []migration.Issue{{Code: "invalid-entry", Name: "bad", Field: "address"}}
	f := migrationDraft(a, bad, migrationEntry("good", "good"))
	if f.rows[0].selected || !f.rows[1].selected {
		t.Fatal("invalid row selected by default")
	}
	f.rows[0].selected = true
	want := en.MigrationRowError("bad", migrationIssue(en, bad.Issues[0]))
	if n, err := a.saveMigration(en, f); n != 0 || err == nil || err.Error() != want || a.store.Len() != 0 {
		t.Fatalf("selected invalid row: %d %v", n, err)
	}
	f.rows[0].selected = false
	if n, err := a.saveMigration(en, f); err != nil || n != 1 || a.store.Len() != 1 {
		t.Fatalf("valid rows: %d %v", n, err)
	}
}
func TestMigrationStaleAndDependencies(t *testing.T) {
	a, _ := newTestApp(t)
	e := migrationEntry("host", "host")
	e.Jumps = []migration.Hop{{Ref: "missing"}}
	f := migrationDraft(a, e)
	if n, err := a.saveMigration(en, f); err == nil || n != 0 || a.store.Len() != 0 {
		t.Fatal("unresolved jump saved")
	}
	jump := migrationEntry("jump", "jump")
	e.Jumps = []migration.Hop{{Ref: "jump"}}
	f = migrationDraft(a, e, jump)
	f.rows[1].selected = false
	if _, err := a.saveMigration(en, f); err == nil {
		t.Fatal("deselected jump dependency saved")
	}
	f.rows[1].selected = true
	if n, err := a.saveMigration(en, f); err != nil || n != 2 {
		t.Fatalf("resolved jump %d %v", n, err)
	}
	f = migrationDraft(a, migrationEntry("other", "other"))
	if _, err := a.store.Add(assets.Host{Name: "concurrent", Address: "new", User: "root"}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.saveMigration(en, f); n != 0 || err == nil || err.Error() != en.MigrationStale() {
		t.Fatal("stale preview saved")
	}
}
