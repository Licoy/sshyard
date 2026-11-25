package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vault"
)

func TestDefaultBackupPassword(t *testing.T) {
	srv, fs := davServer(t)
	a, tt, dir := backupApp(t)
	fillStore(t, a)
	a.openSettings(prefBackup)
	tt.Frame()
	if !tt.HasText(en.DefaultBackupPassword()) || !tt.HasText(en.DefaultBackupPasswordNote()) {
		t.Fatalf("texts %q", tt.Texts())
	}
	// Without one, an export asks for its password, with nothing to choose.
	a.openExport()
	tt.Frame()
	if a.backup.source != pwCustom || tt.HasText(en.BackupPasswordSource()) || !tt.HasText(en.ConfirmPassword()) {
		t.Fatalf("export sheet; texts %q", tt.Texts())
	}
	a.closeBackup()
	tt.Frame()

	mustClick(t, tt, en.SetPasswordEllipsis())
	tt.Frame()
	f := a.defaultPw
	if f == nil {
		t.Fatalf("sheet did not open; texts %q", tt.Texts())
	}
	// The first field takes typing at once.
	for range 3 {
		tt.Frame()
	}
	tt.Type("correct")
	tt.Frame()
	if f.password != "correct" {
		t.Fatalf("typed into the password field, it holds %q", f.password)
	}
	f.password, f.confirm = "short", "short"
	if a.SaveDefaultPassword(en) || f.err != en.BackupPasswordShort("8") {
		t.Fatalf("short password: %q", f.err)
	}
	f.password, f.confirm = "correct horse", "correct hors"
	if a.SaveDefaultPassword(en) || f.err != en.BackupPasswordMismatch() {
		t.Fatalf("mismatch: %q", f.err)
	}
	f.confirm = "correct horse"
	if !a.SaveDefaultPassword(en) {
		t.Fatalf("save refused: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "derive", func() bool { return a.defaultPw == nil })
	if ok, _ := a.kc.Exists(keychain.DefaultBackupKey); !ok || !a.settings.DefaultBackupPassword {
		t.Fatal("the default backup password was not kept")
	}
	if dataHas(t, a, "correct horse") {
		t.Fatal("the password is readable in the data directory")
	}
	tt.Frame()
	if !tt.HasText(en.DefaultBackupPasswordSetNote()) || !tt.HasText(en.ChangePasswordEllipsis()) {
		t.Fatalf("texts %q", tt.Texts())
	}

	// An export seals with it without asking, and opens with it.
	a.openExport()
	tt.Frame()
	if a.backup.source != pwDefault || !tt.HasText(en.BackupPasswordSource()) || tt.HasText(en.ConfirmPassword()) {
		t.Fatalf("export sheet; texts %q", tt.Texts())
	}
	if !a.ExportBackup(en) {
		t.Fatalf("export refused: %q", a.backup.err)
	}
	waitForIn(t, tt, slowWait, "export", func() bool { return a.backup == nil })
	files, _ := filepath.Glob(filepath.Join(dir, "*"+backupExt))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	sealed, _ := os.ReadFile(files[0])
	plain, err := vault.Open(sealed, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := unpackBackup(plain)
	if err != nil || len(p.Secrets) == 0 || p.Settings.DefaultBackupPassword {
		t.Fatalf("export holds %+v, %v", p, err)
	}

	// So does a new location, which keeps no key of its own.
	a.openTargetForm(nil)
	tf := a.target
	if tf.source != pwDefault {
		t.Fatalf("new location source %d", tf.source)
	}
	tf.kind = slices.Index(targetKinds, settings.BackupWebDAV)
	tf.name, tf.url, tf.dir, tf.user, tf.secret = "NAS", srv.URL, "b", "me", "dav-secret-1"
	if !a.SaveTargetForm(en) || a.target != nil {
		t.Fatalf("save refused: %q", tf.err)
	}
	tg := a.settings.BackupTargets[0]
	if ok, _ := a.kc.Exists(keychain.BackupKey(tg.ID)); !tg.DefaultPassword || ok {
		t.Fatalf("saved %+v, own key %v", tg, ok)
	}
	st := a.cloudNow().of(tg.ID)
	a.BackupNow(tg.ID)
	waitForIn(t, tt, slowWait, "upload", func() bool { return !st.busy })
	if st.err != nil {
		t.Fatal(st.err)
	}
	if _, err := vault.Open(davRead(t, fs, "/b/"+st.Object), []byte("correct horse")); err != nil {
		t.Fatal(err)
	}

	// It stays while a location uses it.
	tt.Frame()
	mustClick(t, tt, en.Remove())
	tt.Frame()
	if !a.settings.DefaultBackupPassword || !tt.HasText(en.ErrDefaultBackupPasswordInUse()) {
		t.Fatalf("removed in use; texts %q", tt.Texts())
	}

	// Switched to a password of its own, the location needs one.
	a.openTargetForm(&tg)
	tf = a.target
	tf.source = pwCustom
	if a.SaveTargetForm(en) || tf.err != en.RequiredField(en.BackupPassword()) {
		t.Fatalf("own password missing: %q", tf.err)
	}
	tf.password, tf.confirm = "battery staple", "battery staple"
	if !a.SaveTargetForm(en) {
		t.Fatalf("save refused: %q", tf.err)
	}
	waitForIn(t, tt, slowWait, "derive", func() bool { return a.target == nil })
	tg, _ = a.settings.BackupTarget(tg.ID)
	if ok, _ := a.kc.Exists(keychain.BackupKey(tg.ID)); tg.DefaultPassword || !ok {
		t.Fatalf("saved %+v, own key %v", tg, ok)
	}

	// Then it goes, and a new location has nothing to choose.
	if err := a.RemoveDefaultPassword(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.kc.Exists(keychain.DefaultBackupKey); ok || a.settings.DefaultBackupPassword {
		t.Fatal("the default backup password stayed")
	}
	a.openTargetForm(nil)
	tt.Frame()
	if a.target.source != pwCustom || tt.HasText(en.BackupPasswordSource()) {
		t.Fatalf("new location; texts %q", tt.Texts())
	}
	a.closeTargetForm()

	// A location still sealing with it says it is missing.
	old := addTarget(t, a, settings.BackupTarget{Name: "Old", Kind: settings.BackupWebDAV, URL: srv.URL, Dir: "b", User: "me", DefaultPassword: true}, "dav-secret-1")
	a.BackupNow(old.ID)
	if err := a.cloudNow().of(old.ID).err; !errors.Is(err, errDefaultBackupKey) || backupErrText(en, err) != en.ErrDefaultBackupKeyMissing() {
		t.Fatalf("err %v", err)
	}
	a.openTargetForm(&old)
	if a.SaveTargetForm(en) || a.target.err != en.ErrDefaultBackupKeyMissing() {
		t.Fatalf("saved without the default password: %q", a.target.err)
	}
}

// TestDefaultBackupPasswordStaysHere: a backup neither holds nor replaces
// whether this device has a default backup password.
func TestDefaultBackupPasswordStaysHere(t *testing.T) {
	cur := settings.Default()
	cur.DefaultBackupPassword = true
	if !mergeSettings(cur, settings.Default()).DefaultBackupPassword {
		t.Fatal("a restore dropped the default backup password")
	}
	cur.DefaultBackupPassword = false
	in := settings.Default()
	in.DefaultBackupPassword = true
	if mergeSettings(cur, in).DefaultBackupPassword {
		t.Fatal("a restore set a default backup password")
	}
}
