package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/backup"
	"github.com/Licoy/sshyard/internal/backup/gist"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vault"
	xwebdav "golang.org/x/net/webdav"
)

// davServer is a WebDAV server in memory that takes user "me", password
// "dav-secret-1".
func davServer(t *testing.T) (*httptest.Server, xwebdav.FileSystem) {
	t.Helper()
	fs := xwebdav.NewMemFS()
	h := &xwebdav.Handler{FileSystem: fs, LockSystem: xwebdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "dav-secret-1" {
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, fs
}

// davFiles are the names in dir of fs, sorted.
func davFiles(t *testing.T, fs xwebdav.FileSystem, dir string) []string {
	t.Helper()
	f, err := fs.OpenFile(context.Background(), dir, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	infos, _ := f.Readdir(-1)
	var out []string
	for _, fi := range infos {
		out = append(out, fi.Name())
	}
	slices.Sort(out)
	return out
}

func davRead(t *testing.T, fs xwebdav.FileSystem, name string) []byte {
	t.Helper()
	f, err := fs.OpenFile(context.Background(), name, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return b
}

func davWrite(t *testing.T, fs xwebdav.FileSystem, name string, b []byte) {
	t.Helper()
	ctx := context.Background()
	fs.Mkdir(ctx, path.Dir(name), 0o700)
	f, err := fs.OpenFile(ctx, name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(b)
	f.Close()
}

// addTarget saves tg with its secret and the key of the backup password
// "correct horse", as the editor would.
func addTarget(t *testing.T, a *App, tg settings.BackupTarget, secret string) settings.BackupTarget {
	t.Helper()
	if tg.ID == "" {
		tg.ID = assets.NewID()
	}
	k, err := vault.Derive([]byte("correct horse"), a.kdf)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Clear()
	kb, _ := json.Marshal(k)
	if err := a.kc.Set(keychain.BackupSecret(tg.ID), []byte(secret)); err != nil {
		t.Fatal(err)
	}
	if err := a.kc.Set(keychain.BackupKey(tg.ID), kb); err != nil {
		t.Fatal(err)
	}
	s := a.settings
	s.BackupTargets = append(slices.Clone(s.BackupTargets), tg)
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	return tg
}

// dataHas reports whether a file of a's data directory holds s.
func dataHas(t *testing.T, a *App, s string) bool {
	t.Helper()
	for _, name := range []string{"settings.json", cloudFile, "hosts.json"} {
		if b, err := os.ReadFile(filepath.Join(a.p.DataDir, name)); err == nil && bytes.Contains(b, []byte(s)) {
			return true
		}
	}
	return false
}

func TestCloudTargetForm(t *testing.T) {
	srv, _ := davServer(t)
	a, tt, _ := backupApp(t)
	a.openSettings(prefBackup)
	tt.Frame()
	if !tt.HasText(en.CloudBackupSection()) || !tt.HasText(en.NoBackupTargets()) {
		t.Fatalf("texts %q", tt.Texts())
	}
	mustClick(t, tt, en.AddBackupTarget())
	tt.Frame()
	f := a.target
	if f == nil || !f.isNew || !tt.HasText(en.NewBackupTarget()) {
		t.Fatalf("editor did not open; texts %q", tt.Texts())
	}
	// The first field takes typing at once.
	for range 3 {
		tt.Frame()
	}
	tt.Type("NAS")
	tt.Frame()
	if f.name != "NAS" {
		t.Fatalf("typed into the name field, it holds %q", f.name)
	}

	if targetKinds[f.kind] != settings.BackupFolder {
		t.Fatalf("a new place is a %s, not a sync folder", targetKinds[f.kind])
	}
	f.kind = slices.Index(targetKinds, settings.BackupWebDAV)
	f.url = "ftp://dav.example.com/"
	if a.SaveTargetForm(en) || f.err != en.ErrTargetURL() {
		t.Fatalf("bad URL: %q", f.err)
	}
	f.url, f.dir, f.user, f.secret = srv.URL+"/", "SSHYard", "me", "wrong"
	tt.Frame()
	if !tt.HasText(en.WarnPlainHTTP()) {
		t.Fatalf("no plain HTTP warning; texts %q", tt.Texts())
	}
	if !a.TestTarget(en) {
		t.Fatalf("test refused: %q", f.err)
	}
	waitFor(t, tt, "test", func() bool { return !f.testing })
	if f.err != en.ErrCloudAuth() {
		t.Fatalf("wrong password: %q", f.err)
	}
	f.secret = "dav-secret-1"
	a.TestTarget(en)
	waitFor(t, tt, "test", func() bool { return !f.testing })
	if f.tested != en.ConnectionOK() || f.err != "" {
		t.Fatalf("test: %q, %q", f.tested, f.err)
	}
	// The backup password, shared by backups by hand and automatic ones,
	// is asked for a new place, typed twice.
	if !tt.HasText(en.BackupPassword()) || tt.HasText(en.AutoBackupInterval()) {
		t.Fatalf("texts %q", tt.Texts())
	}
	if a.SaveTargetForm(en) || f.err != en.RequiredField(en.BackupPassword()) {
		t.Fatalf("no password: %q", f.err)
	}
	f.password, f.confirm = "short", "short"
	if a.SaveTargetForm(en) || f.err != en.BackupPasswordShort("8") {
		t.Fatalf("short password: %q", f.err)
	}
	f.password, f.confirm = "correct horse", "correct hors"
	if a.SaveTargetForm(en) || f.err != en.BackupPasswordMismatch() {
		t.Fatalf("mismatch: %q", f.err)
	}
	f.confirm = "correct horse"
	if !a.SaveTargetForm(en) {
		t.Fatalf("save refused: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "derive", func() bool { return a.target == nil })
	ts := a.settings.BackupTargets
	if len(ts) != 1 || ts[0].Kind != settings.BackupWebDAV || ts[0].URL != srv.URL+"/" || ts[0].Keep != settings.BackupKeepDefault || ts[0].AutoHours != 0 || ts[0].OmitSecrets {
		t.Fatalf("saved %+v", ts)
	}
	id := ts[0].ID
	if got, err := a.kc.Get(keychain.BackupSecret(id)); err != nil || string(got) != "dav-secret-1" {
		t.Fatalf("secret %q, %v", got, err)
	}
	k, err := a.targetKey(ts[0])
	if err != nil {
		t.Fatal(err)
	}
	k.Clear()
	if dataHas(t, a, "dav-secret-1") || dataHas(t, a, "correct horse") {
		t.Fatal("a password is readable in the data directory")
	}
	tt.Frame()
	if !tt.HasText("NAS") || !tt.HasText(en.BackupNow()) || !tt.HasText(en.RestoreFromTarget()) {
		t.Fatalf("no row for the place; texts %q", tt.Texts())
	}

	// Editing keeps the secret and the key unless one is typed, with
	// automatic backups turned on too.
	key, _ := a.kc.Get(keychain.BackupKey(id))
	a.openTargetForm(&ts[0])
	f = a.target
	f.auto, f.secrets = true, false
	if !a.SaveTargetForm(en) || a.target != nil {
		t.Fatalf("save refused: %q", f.err)
	}
	tg, _ := a.settings.BackupTarget(id)
	if tg.AutoHours != 24 || !tg.OmitSecrets {
		t.Fatalf("edited %+v", tg)
	}
	if got, _ := a.kc.Get(keychain.BackupSecret(id)); string(got) != "dav-secret-1" {
		t.Fatalf("secret became %q", got)
	}
	if got, _ := a.kc.Get(keychain.BackupKey(id)); !bytes.Equal(got, key) {
		t.Fatal("the key changed with no password typed")
	}

	// Another kind's secret is not kept.
	a.openTargetForm(&tg)
	f = a.target
	f.kind = slices.Index(targetKinds, settings.BackupGist)
	if a.SaveTargetForm(en) || f.err != en.RequiredField(en.GistToken()) {
		t.Fatalf("gist without a token: %q", f.err)
	}
	a.closeTargetForm()

	// Turning automatic backups off keeps the key, for backups by hand.
	a.openTargetForm(&tg)
	a.target.auto = false
	if !a.SaveTargetForm(en) {
		t.Fatalf("save refused: %q", a.target.err)
	}
	if ok, _ := a.kc.Exists(keychain.BackupKey(id)); !ok {
		t.Fatal("the key went with automatic backups off")
	}

	// A place without its key asks for the password again.
	a.kc.Delete(keychain.BackupKey(id))
	tg, _ = a.settings.BackupTarget(id)
	a.openTargetForm(&tg)
	f = a.target
	if a.SaveTargetForm(en) || f.err != en.RequiredField(en.BackupPassword()) {
		t.Fatalf("no key, no password: %q", f.err)
	}
	f.password, f.confirm = "battery staple", "battery staple"
	if !a.SaveTargetForm(en) {
		t.Fatalf("save refused: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "derive", func() bool { return a.target == nil })

	// Deleting the place deletes its secret and key.
	if err := a.DeleteBackupTarget(id); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{keychain.BackupSecret(id), keychain.BackupKey(id)} {
		if ok, _ := a.kc.Exists(k); ok {
			t.Fatalf("%s stayed", k)
		}
	}
	if len(a.settings.BackupTargets) != 0 {
		t.Fatal("the place stayed")
	}
}

func TestCloudBackupAndRestore(t *testing.T) {
	srv, fs := davServer(t)
	src, stt, _ := backupApp(t)
	pw, _, _ := fillStore(t, src)
	tg := addTarget(t, src, settings.BackupTarget{Name: "NAS", Kind: settings.BackupWebDAV, URL: srv.URL, Dir: "SSHYard", User: "me", Keep: 5}, "dav-secret-1")
	src.openSettings(prefBackup)
	stt.Frame()
	if !stt.HasText(en.TargetNeverBackedUp()) {
		t.Fatalf("texts %q", stt.Texts())
	}
	// Backing up by hand seals with the place's key, without asking.
	mustClick(t, stt, en.BackupNow())
	st := src.cloudNow().of(tg.ID)
	waitForIn(t, stt, slowWait, "upload", func() bool { return !st.Last.IsZero() || st.err != nil })
	if src.backup != nil || st.err != nil {
		t.Fatalf("backup sheet %+v, err %v", src.backup, st.err)
	}
	stt.Frame()
	if !stt.HasText(en.BackupUploaded("NAS")) {
		t.Fatalf("no success toast; texts %q", stt.Texts())
	}
	files := davFiles(t, fs, "/SSHYard")
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	if _, device, ok := backup.Parse(files[0]); !ok || device != src.deviceID() {
		t.Fatalf("named %q", files[0])
	}
	sealed := davRead(t, fs, "/SSHYard/"+files[0])
	for _, secret := range []string{"hunter2", "proxy-pw", "sk-test", "10.0.0.5"} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Fatalf("%q readable in the uploaded backup", secret)
		}
	}
	if st.Object != files[0] {
		t.Fatalf("state %+v", st)
	}
	stt.Frame()
	if stt.HasText(en.TargetNeverBackedUp()) {
		t.Fatal("the row did not show the backup")
	}

	// Another device restores it from the same place.
	dst, tt, _ := backupApp(t)
	tg2 := addTarget(t, dst, settings.BackupTarget{Name: "NAS", Kind: settings.BackupWebDAV, URL: srv.URL, Dir: "SSHYard", User: "me"}, "dav-secret-1")
	dst.OpenRemoteList(tg2.ID)
	l := dst.remotes
	waitFor(t, tt, "list", func() bool { return !l.loading })
	if l.err != nil || len(l.objs) != 1 {
		t.Fatalf("listed %v, %v", l.objs, l.err)
	}
	tt.Frame()
	date := l.objs[0].Time.Local().Format("2006-01-02 15:04")
	if !tt.HasText(en.RemoteBackupOtherDevice(date, src.deviceID())) {
		t.Fatalf("texts %q", tt.Texts())
	}
	mustClick(t, tt, en.RestoreThisBackup())
	waitFor(t, tt, "download", func() bool { return dst.backup != nil })
	if !dst.backup.restore || dst.backup.name != files[0] || dst.remotes != nil {
		t.Fatalf("restore sheet %+v", dst.backup)
	}
	dst.backup.password = "correct horse"
	if !dst.RestoreBackup(en) {
		t.Fatalf("restore refused: %q", dst.backup.err)
	}
	waitForIn(t, tt, slowWait, "restore", func() bool { return dst.backup == nil })
	if got, err := dst.kc.Get(keychain.Cred(pw.ID, keychain.Password)); err != nil || string(got) != "hunter2" {
		t.Fatalf("restored secret %q, %v", got, err)
	}
	// The places stay this device's own.
	if ts := dst.settings.BackupTargets; len(ts) != 1 || ts[0].ID != tg2.ID {
		t.Fatalf("places after restore %+v", ts)
	}
}

func TestCloudAutoBackup(t *testing.T) {
	srv, fs := davServer(t)
	a, tt, _ := backupApp(t)
	fillStore(t, a)
	tg := addTarget(t, a, settings.BackupTarget{Name: "NAS", Kind: settings.BackupWebDAV, URL: srv.URL, Dir: "b", User: "me", Keep: 2}, "dav-secret-1")
	// Without automatic backups, nothing runs.
	a.autoBackup(time.Now())
	if a.cloudNow().of(tg.ID).busy {
		t.Fatal("a place without automatic backups backed up")
	}
	s := a.settings
	s.BackupTargets = slices.Clone(s.BackupTargets)
	s.BackupTargets[0].AutoHours = 24
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	tg = s.BackupTargets[0]

	// Two older backups of this device and one of another.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []string{backup.Name(old, a.deviceID()), backup.Name(old.Add(time.Hour), a.deviceID()), backup.Name(old, "zz99zz99")} {
		davWrite(t, fs, "/b/"+n, []byte("{}"))
	}
	st := a.cloudNow().of(tg.ID)
	run := func(at time.Time) {
		t.Helper()
		a.autoBackup(at)
		waitForIn(t, tt, slowWait, "automatic backup", func() bool { return !st.busy })
		if st.err != nil {
			t.Fatal(st.err)
		}
	}
	run(time.Now())
	first := st.Object
	want := []string{backup.Name(old, "zz99zz99"), backup.Name(old.Add(time.Hour), a.deviceID()), first}
	slices.Sort(want)
	if got := davFiles(t, fs, "/b"); !slices.Equal(got, want) {
		t.Fatalf("after pruning to 2: %v, want %v", got, want)
	}
	// Sealed with the key, it opens with the password.
	plain, err := vault.Open(davRead(t, fs, "/b/"+first), []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := unpackBackup(plain)
	if err != nil || len(p.Assets.Credentials) != 2 || len(p.Secrets) == 0 || len(p.Settings.BackupTargets) != 0 {
		t.Fatalf("automatic backup holds %+v, %v", p, err)
	}

	// Not due yet; then due but unchanged.
	run(time.Now().Add(time.Hour))
	run(time.Now().Add(25 * time.Hour))
	if st.Object != first || st.Checked.IsZero() {
		t.Fatalf("an unchanged backup ran: %+v", st)
	}
	// Changed, it runs, under a new name.
	if _, err := a.store.Add(assets.Host{Name: "db", Address: "10.0.0.6", User: "root", Auth: assets.AuthAgent}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	run(time.Now().Add(50 * time.Hour))
	if st.Object == first {
		t.Fatal("a changed backup did not run")
	}
	if got := davFiles(t, fs, "/b"); len(got) != 3 || !slices.Contains(got, st.Object) {
		t.Fatalf("after the second: %v", got)
	}
	// What is known survives a restart, without secrets.
	b, _ := os.ReadFile(filepath.Join(a.p.DataDir, cloudFile))
	var saved cloudState
	if json.Unmarshal(b, &saved) != nil || saved.Device != a.deviceID() || saved.Targets[tg.ID] == nil || saved.Targets[tg.ID].Object != st.Object {
		t.Fatalf("%s: %s", cloudFile, b)
	}
	if dataHas(t, a, "correct horse") || dataHas(t, a, "dav-secret-1") {
		t.Fatal("a secret is readable in the data directory")
	}

	// A failure is shown on the place's row and tried again later.
	a.kc.Set(keychain.BackupSecret(tg.ID), []byte("wrong"))
	if _, err := a.store.Add(assets.Host{Name: "cache", Address: "10.0.0.7", User: "root", Auth: assets.AuthAgent}); err != nil {
		t.Fatal(err)
	}
	a.autoBackup(time.Now().Add(100 * time.Hour))
	waitForIn(t, tt, slowWait, "failed backup", func() bool { return !st.busy })
	if st.err == nil || st.retry.IsZero() {
		t.Fatalf("state %+v", st)
	}
	a.openSettings(prefBackup)
	tt.Frame()
	if !tt.HasText(en.TargetBackupFailed(en.ErrCloudAuth())) {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// gistAPI is a GitHub API in memory with no gists to begin with.
type gistAPI struct {
	mu      sync.Mutex
	created []map[string]any
}

func (g *gistAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer ghp_test" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/gists":
		w.Write([]byte("[]"))
	case r.Method == http.MethodPost && r.URL.Path == "/gists":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		g.created = append(g.created, body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"g1","files":{}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestCloudGistKeepsItsGist: the gist the first backup makes is kept in
// the place's settings.
func TestCloudGistKeepsItsGist(t *testing.T) {
	api := &gistAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	a, tt, _ := backupApp(t)
	a.gistAPI = srv.URL
	tg := addTarget(t, a, settings.BackupTarget{Name: "Gist", Kind: settings.BackupGist}, "ghp_test")
	a.BackupNow(tg.ID)
	st := a.cloudNow().of(tg.ID)
	waitForIn(t, tt, slowWait, "upload", func() bool { return !st.busy })
	if st.err != nil {
		t.Fatal(st.err)
	}
	waitFor(t, tt, "gist kept", func() bool { got, _ := a.settings.BackupTarget(tg.ID); return got.GistID == "g1" })
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.created) != 1 || api.created[0]["description"] != gist.Description || api.created[0]["public"] != false {
		t.Fatalf("created %v", api.created)
	}
}

// TestCloudBackupNowKey: a backup by hand leaves the secrets out when the
// place says so, and a place without its key says so.
func TestCloudBackupNowKey(t *testing.T) {
	srv, fs := davServer(t)
	a, tt, _ := backupApp(t)
	fillStore(t, a)
	tg := addTarget(t, a, settings.BackupTarget{Name: "NAS", Kind: settings.BackupWebDAV, URL: srv.URL, Dir: "b", User: "me", OmitSecrets: true}, "dav-secret-1")
	st := a.cloudNow().of(tg.ID)
	a.BackupNow(tg.ID)
	waitForIn(t, tt, slowWait, "upload", func() bool { return !st.busy })
	if st.err != nil || a.backup != nil {
		t.Fatalf("err %v, backup sheet %+v", st.err, a.backup)
	}
	plain, err := vault.Open(davRead(t, fs, "/b/"+st.Object), []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := unpackBackup(plain)
	if err != nil || len(p.Assets.Credentials) != 2 || len(p.Secrets) != 0 {
		t.Fatalf("backup holds %+v, %v", p, err)
	}

	a.kc.Delete(keychain.BackupKey(tg.ID))
	a.BackupNow(tg.ID)
	if !errors.Is(st.err, errBackupKey) || st.busy {
		t.Fatalf("state %+v", st)
	}
	tt.Frame()
	if !tt.HasText(en.BackupUploadFailed("NAS", en.ErrBackupKeyMissing())) {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// TestCloudProxyGone: a place whose proxy was deleted is not reached
// directly instead.
func TestCloudProxyGone(t *testing.T) {
	a, _, _ := backupApp(t)
	if _, err := a.remote(settings.BackupTarget{ID: "x", Kind: settings.BackupWebDAV, URL: "https://dav.example.com", ProxyID: "gone"}, "", nil); backupErrText(en, err) != en.ErrBackupProxyMissing() {
		t.Fatalf("err %v", err)
	}
}

// TestBackupLeavesTargetsOut: a backup holds none of this device's places.
func TestBackupLeavesTargetsOut(t *testing.T) {
	a, _, _ := backupApp(t)
	addTarget(t, a, settings.BackupTarget{Name: "NAS", Kind: settings.BackupWebDAV, URL: "https://dav.example.com"}, "dav-secret-1")
	p, err := a.collectBackup(true)
	if err != nil {
		t.Fatal(err)
	}
	held, _ := json.Marshal(p)
	if len(p.Settings.BackupTargets) != 0 || bytes.Contains(held, []byte("dav-secret-1")) || bytes.Contains(held, []byte("dav.example.com")) {
		t.Fatalf("backup holds the places: %s", held)
	}
	if len(a.settings.BackupTargets) != 1 {
		t.Fatal("collecting changed the settings")
	}
}

// TestRestoreBackupBeforeArchives: a backup file from before archives,
// JSON sealed, still restores.
func TestRestoreBackupBeforeArchives(t *testing.T) {
	a, tt, _ := backupApp(t)
	cr := assets.Credential{ID: assets.NewID(), Name: "old", Kind: assets.CredPassword}
	p := backupPayload{Settings: settings.Default(), Assets: assets.Snapshot{Credentials: []assets.Credential{cr}},
		Secrets: map[string][]byte{keychain.Cred(cr.ID, keychain.Password): []byte("v1-secret")}}
	plain, _ := json.Marshal(p)
	sealed, err := vault.Seal(plain, []byte("correct horse"), a.kdf)
	if err != nil {
		t.Fatal(err)
	}
	a.openRestore(en, "/tmp/SSHYard-2026-01-01"+backupExt, sealed, nil)
	a.backup.password = "correct horse"
	if !a.RestoreBackup(en) {
		t.Fatalf("restore refused: %q", a.backup.err)
	}
	waitForIn(t, tt, slowWait, "restore", func() bool { return a.backup == nil })
	if got, err := a.kc.Get(keychain.Cred(cr.ID, keychain.Password)); err != nil || string(got) != "v1-secret" {
		t.Fatalf("restored %q, %v", got, err)
	}
}
