package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/backup"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
)

// TestCloudFolderTarget backs up to a sync folder and restores from it on
// another device: the place has no secret and no proxy, and starts in the
// sync client's folder found in the home folder.
func TestCloudFolderTarget(t *testing.T) {
	home := t.TempDir()
	dropbox := filepath.Join(home, "Dropbox")
	if err := os.Mkdir(dropbox, 0o700); err != nil {
		t.Fatal(err)
	}
	src, stt, _ := backupApp(t)
	src.p.HomeDir = home
	pw, _, _ := fillStore(t, src)

	src.openTargetForm(nil)
	f := src.target
	want := filepath.Join(dropbox, "SSHYard")
	if f.path != want {
		t.Fatalf("new folder place starts at %q, want %q", f.path, want)
	}
	// The type select starts on the sync client's folder found, and
	// another option changes the kind, or the folder.
	opts := targetOptions(en, src.knownPlaces(false))
	labels := []string{}
	for _, o := range opts {
		labels = append(labels, o.label)
	}
	if !slices.Equal(labels, []string{"Dropbox", en.BackupKindOtherFolder(), "WebDAV", "S3", "GitHub Gist"}) {
		t.Fatalf("options %q", labels)
	}
	if o := optionOf(opts, f); o.label != "Dropbox" || targetKinds[f.kind] != settings.BackupFolder {
		t.Fatalf("new place shows %q, kind %s", o.label, targetKinds[f.kind])
	}
	chooseOption(f, opts[2])
	if targetKinds[f.kind] != settings.BackupWebDAV || f.path != want {
		t.Fatalf("WebDAV chosen: kind %s, path %q", targetKinds[f.kind], f.path)
	}
	chooseOption(f, opts[1])
	if o := optionOf(opts, f); o.label != en.BackupKindOtherFolder() || f.path != want {
		t.Fatalf("other folder chosen: shows %q, path %q", o.label, f.path)
	}
	f.path, f.choice = filepath.Join(home, "elsewhere"), ""
	chooseOption(f, opts[0])
	if f.path != want {
		t.Fatalf("Dropbox chosen: path %q", f.path)
	}
	f.name, f.proxy = "Dropbox", "gone"
	f.password, f.confirm = "correct horse", "correct horse"
	f.path = "SSHYard"
	if src.SaveTargetForm(en) || f.err != en.ErrTargetFolder() {
		t.Fatalf("relative folder: %q", f.err)
	}
	f.path = " "
	if src.SaveTargetForm(en) || f.err != en.RequiredField(en.SyncFolderPath()) {
		t.Fatalf("no folder: %q", f.err)
	}
	f.path = filepath.Join(home, "missing", "SSHYard")
	src.TestTarget(en)
	waitFor(t, stt, "test", func() bool { return !f.testing })
	if f.err != en.ErrCloudNotFound() {
		t.Fatalf("folder without its parent: %q", f.err)
	}
	f.path = want + string(filepath.Separator)
	src.TestTarget(en)
	waitFor(t, stt, "test", func() bool { return !f.testing })
	if f.tested != en.ConnectionOK() || f.err != "" {
		t.Fatalf("test: %q, %q", f.tested, f.err)
	}
	if !src.SaveTargetForm(en) {
		t.Fatalf("save refused: %q", f.err)
	}
	waitForIn(t, stt, slowWait, "derive", func() bool { return src.target == nil })
	ts := src.settings.BackupTargets
	if len(ts) != 1 || ts[0].Kind != settings.BackupFolder || ts[0].Dir != want || ts[0].ProxyID != "" || ts[0].URL != "" {
		t.Fatalf("saved %+v", ts)
	}
	tg := ts[0]
	src.openTargetForm(&tg)
	if o := optionOf(opts, src.target); o.label != "Dropbox" {
		t.Fatalf("editing the place shows %q", o.label)
	}
	src.closeTargetForm()
	if note := src.targetNote(en, tg); !strings.HasPrefix(note, en.BackupKindFolderVia("Dropbox")+" · SSHYard · ") {
		t.Fatalf("row note %q", note)
	}
	if ok, _ := src.kc.Exists(keychain.BackupSecret(tg.ID)); ok {
		t.Fatal("a folder place has a secret")
	}

	src.BackupNow(tg.ID)
	st := src.cloudNow().of(tg.ID)
	waitForIn(t, stt, slowWait, "write", func() bool { return !st.Last.IsZero() || st.err != nil })
	if st.err != nil {
		t.Fatal(st.err)
	}
	des, _ := os.ReadDir(want)
	if len(des) != 1 || des[0].Name() != st.Object {
		t.Fatalf("folder holds %v, state %+v", des, st)
	}
	if _, device, ok := backup.Parse(st.Object); !ok || device != src.deviceID() {
		t.Fatalf("named %q", st.Object)
	}
	sealed, _ := os.ReadFile(filepath.Join(want, st.Object))
	for _, secret := range []string{"hunter2", "proxy-pw", "sk-test", "10.0.0.5"} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Fatalf("%q readable in the backup", secret)
		}
	}

	// Another device, whose sync client brought the folder over, restores.
	dst, tt, _ := backupApp(t)
	tg2 := addTarget(t, dst, settings.BackupTarget{Name: "Dropbox", Kind: settings.BackupFolder, Dir: want}, "")
	dst.OpenRemoteList(tg2.ID)
	l := dst.remotes
	waitFor(t, tt, "list", func() bool { return !l.loading })
	if l.err != nil || len(l.objs) != 1 || l.objs[0].Name != st.Object {
		t.Fatalf("listed %v, %v", l.objs, l.err)
	}
	dst.fetchRemote(en, l, st.Object)
	waitFor(t, tt, "read", func() bool { return dst.backup != nil })
	dst.backup.password = "correct horse"
	if !dst.RestoreBackup(en) {
		t.Fatalf("restore refused: %q", dst.backup.err)
	}
	waitForIn(t, tt, slowWait, "restore", func() bool { return dst.backup == nil })
	if got, err := dst.kc.Get(keychain.Cred(pw.ID, keychain.Password)); err != nil || string(got) != "hunter2" {
		t.Fatalf("restored secret %q, %v", got, err)
	}
}
