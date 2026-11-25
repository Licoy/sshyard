package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vault"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh/knownhosts"
)

// backupApp is a test app whose backups derive cheaply and save to dir.
func backupApp(t *testing.T) (*App, *ui.Tester, string) {
	t.Helper()
	a, tt := newTestApp(t)
	a.kdf = vault.Params{Time: 1, Memory: 64, Threads: 1}
	dir := t.TempDir()
	a.p.SaveFile = func(title, path string) (string, error) { return filepath.Join(dir, filepath.Base(path)), nil }
	return a, tt, dir
}

// fillStore gives a a credential with a password, a key credential with
// its key and passphrase, a proxy with a password, a host using them, a
// trusted host key and an AI provider with its key.
func fillStore(t *testing.T, a *App) (assets.Credential, assets.Credential, assets.Proxy) {
	t.Helper()
	pw, err := a.store.AddCredential(assets.Credential{ID: assets.NewID(), Name: "deploy", Kind: assets.CredPassword})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := a.store.AddCredential(assets.Credential{ID: assets.NewID(), Name: "laptop key", Kind: assets.CredKey, Encrypted: true})
	px, _ := a.store.AddProxy(assets.Proxy{ID: assets.NewID(), Name: "office", Kind: assets.ProxySOCKS5, Address: "proxy.lan", User: "me"})
	a.kc.Set(keychain.Cred(pw.ID, keychain.Password), []byte("hunter2"))
	a.kc.Set(keychain.Cred(key.ID, keychain.PrivateKey), bytes.Repeat([]byte("K"), 5000)) // chunked
	a.kc.Set(keychain.Cred(key.ID, keychain.Passphrase), []byte("open sesame"))
	a.kc.Set(keychain.ProxyPassword(px.ID), []byte("proxy-pw"))
	if _, err := a.store.Add(assets.Host{Name: "web", Address: "10.0.0.5", User: "deploy", Auth: assets.AuthCredential, CredentialID: pw.ID, ProxyID: px.ID}); err != nil {
		t.Fatal(err)
	}
	_, pub := newKey(t)
	a.known.Add("10.0.0.5:22", pub)
	s := a.settings
	s.TerminalFontSize, s.SidebarWidth = 17, 300
	s.AIProviders = []settings.AIProvider{{ID: "prov1", Name: "Work", BaseURL: "https://llm.example.com/v1", Model: "m"}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.kc.Set(keychain.AIKey("prov1"), []byte("sk-test"))
	return pw, key, px
}

// export runs the export sheet with password, as typed in both fields.
func export(t *testing.T, a *App, tt *ui.Tester, dir, password string, secrets bool) string {
	t.Helper()
	a.openExport()
	a.backup.password, a.backup.confirm, a.backup.secrets = password, password, secrets
	f := a.backup
	if !a.ExportBackup(en) {
		t.Fatalf("export refused: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "export", func() bool { return a.backup == nil })
	matches, _ := filepath.Glob(filepath.Join(dir, "*"+backupExt))
	if len(matches) != 1 {
		t.Fatalf("saved %v", matches)
	}
	return matches[0]
}

func TestBackupRoundTrip(t *testing.T) {
	src, stt, sdir := backupApp(t)
	pw, key, px := fillStore(t, src)
	path := export(t, src, stt, sdir, "correct horse", true)
	b, _ := os.ReadFile(path)
	for _, secret := range []string{"hunter2", "open sesame", "proxy-pw", "sk-test", "deploy", "10.0.0.5"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("%q readable in the backup file", secret)
		}
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no Unix modes
		t.Fatalf("backup mode %v", st.Mode())
	}

	dst, tt, _ := backupApp(t)
	dst.openRestore(en, path, b, nil)
	if dst.backup == nil || !dst.backup.restore {
		t.Fatal("restore sheet did not open")
	}
	tt.Frame()
	if !tt.HasText("Restore from Backup") || !tt.HasText(filepath.Base(path)) {
		t.Fatalf("restore sheet texts %q", tt.Texts())
	}
	dst.backup.password = "correct horse"
	if !dst.RestoreBackup(en) {
		t.Fatalf("restore refused: %q", dst.backup.err)
	}
	waitForIn(t, tt, slowWait, "restore", func() bool { return dst.backup == nil })

	if dst.store.Len() != 1 || len(dst.store.Credentials()) != 2 || len(dst.store.Proxies()) != 1 {
		t.Fatalf("restored %+v", dst.store.Snapshot())
	}
	for k, want := range map[string]string{
		keychain.Cred(pw.ID, keychain.Password):    "hunter2",
		keychain.Cred(key.ID, keychain.PrivateKey): strings.Repeat("K", 5000),
		keychain.Cred(key.ID, keychain.Passphrase): "open sesame",
		keychain.ProxyPassword(px.ID):              "proxy-pw",
		keychain.AIKey("prov1"):                    "sk-test",
	} {
		if got, err := dst.kc.Get(k); err != nil || string(got) != want {
			t.Errorf("%s = %.20q, %v", k, got, err)
		}
	}
	kh, _ := dst.known.Contents()
	if !strings.Contains(string(kh), knownhosts.Normalize("10.0.0.5:22")) {
		t.Fatalf("known_hosts not restored:\n%s", kh)
	}
	s := dst.settings
	if s.TerminalFontSize != 17 || len(s.AIProviders) != 1 || s.AIProviders[0].ID != "prov1" {
		t.Fatalf("preferences not restored: %+v", s)
	}
	if s.SidebarWidth == 300 {
		t.Fatal("this device's sidebar width was replaced")
	}
	tt.Frame()
	if !tt.HasText(en.BackupRestored(1, 2)) {
		t.Fatalf("no success toast; texts %q", tt.Texts())
	}
}

// TestBackupWithoutSecrets: the switch off leaves every secret out, and
// restoring keeps the secrets this device has for the same entries.
func TestBackupWithoutSecrets(t *testing.T) {
	a, tt, dir := backupApp(t)
	pw, _, _ := fillStore(t, a)
	path := export(t, a, tt, dir, "correct horse", false)
	b, _ := os.ReadFile(path)
	plain, err := vault.Open(b, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := unpackBackup(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Secrets) != 0 || len(p.Assets.Credentials) != 2 {
		t.Fatalf("payload %+v", p)
	}
	a.kc.Set(keychain.Cred(pw.ID, keychain.Password), []byte("newer"))
	if err := a.restoreBackup(en, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.kc.Get(keychain.Cred(pw.ID, keychain.Password)); string(got) != "newer" {
		t.Fatalf("local secret became %q", got)
	}
}

func TestBackupPasswordChecks(t *testing.T) {
	a, tt, dir := backupApp(t)
	a.openSettings(prefBackup)
	tt.Frame()
	mustClick(t, tt, en.ExportEllipsis())
	tt.Frame()
	if a.backup == nil || !a.backup.secrets {
		t.Fatal("export sheet did not open with secrets on")
	}
	// The first field takes typing at once.
	for range 3 {
		tt.Frame()
	}
	tt.Type("short")
	tt.Frame()
	if a.backup.password != "short" {
		t.Fatalf("typed into the password field, it holds %q", a.backup.password)
	}
	if a.ExportBackup(en) || a.backup.err != en.BackupPasswordShort("8") {
		t.Fatalf("short password: %q", a.backup.err)
	}
	a.backup.password, a.backup.confirm = "long enough", "long enougj"
	if a.ExportBackup(en) || a.backup.err != en.BackupPasswordMismatch() {
		t.Fatalf("mismatch: %q", a.backup.err)
	}
	if ms, _ := filepath.Glob(filepath.Join(dir, "*")); len(ms) != 0 {
		t.Fatalf("saved %v", ms)
	}
}

func TestRestoreWrongPassword(t *testing.T) {
	a, stt, sdir := backupApp(t)
	fillStore(t, a)
	path := export(t, a, stt, sdir, "correct horse", true)
	b, _ := os.ReadFile(path)

	dst, tt, _ := backupApp(t)
	dst.openRestore(en, path, b, nil)
	dst.backup.password = "wrong horse"
	dst.RestoreBackup(en)
	waitForIn(t, tt, slowWait, "wrong password", func() bool { return dst.backup != nil && !dst.backup.busy })
	if dst.backup.err != en.BackupWrongPassword() || dst.store.Len() != 0 {
		t.Fatalf("err %q, %d hosts", dst.backup.err, dst.store.Len())
	}
}

func TestRestoreNotABackup(t *testing.T) {
	a, tt, _ := backupApp(t)
	a.openRestore(en, "/tmp/hosts.json", []byte(`{"hosts":[]}`), nil)
	tt.Frame()
	if a.backup != nil || !tt.HasText(en.BackupOpenFailed("hosts.json", en.BackupNotBackup())) {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// TestRestoreTakesOnlyItsSecrets: a backup can write only the keychain
// entries of its own entries, under IDs that are safe to use.
func TestRestoreTakesOnlyItsSecrets(t *testing.T) {
	a, _, _ := backupApp(t)
	victim, _ := a.store.AddCredential(assets.Credential{ID: assets.NewID(), Name: "mine", Kind: assets.CredPassword})
	a.kc.Set(keychain.Cred(victim.ID, keychain.Password), []byte("mine"))
	own := assets.Credential{ID: assets.NewID(), Name: "theirs", Kind: assets.CredPassword}
	p := backupPayload{Settings: a.settings, Assets: assets.Snapshot{Credentials: []assets.Credential{own}}, Secrets: map[string][]byte{
		keychain.Cred(own.ID, keychain.Password):    []byte("theirs"),
		keychain.Cred(victim.ID, keychain.Password): []byte("overwritten"),
		"sshyard/probe": []byte("x"),
	}}
	if err := a.restoreBackup(en, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.kc.Get(keychain.Cred(victim.ID, keychain.Password)); string(got) != "mine" {
		t.Fatalf("another entry's secret became %q", got)
	}
	if ok, _ := a.kc.Exists("sshyard/probe"); ok {
		t.Fatal("wrote a key of no entry")
	}
	bad := backupPayload{Settings: a.settings, Assets: assets.Snapshot{Credentials: []assets.Credential{{ID: victim.ID + "/x", Name: "bad", Kind: assets.CredPassword}}},
		Secrets: map[string][]byte{keychain.Cred(victim.ID+"/x", keychain.Password): []byte("x")}}
	if err := a.restoreBackup(en, bad); backupErrText(en, err) != en.BackupDamaged() {
		t.Fatalf("bad ID: %v", err)
	}
	if ok, _ := a.kc.Exists(keychain.Cred(victim.ID+"/x", keychain.Password)); ok {
		t.Fatal("wrote the secret of a bad ID")
	}
}

// TestRestoreBrokenEntriesPutsSecretsBack: entries the store refuses
// leave the keychain as it was.
func TestRestoreBrokenEntriesPutsSecretsBack(t *testing.T) {
	a, _, _ := backupApp(t)
	cr := assets.Credential{ID: assets.NewID(), Name: "c", Kind: assets.CredPassword}
	p := backupPayload{Settings: a.settings, Assets: assets.Snapshot{
		Credentials: []assets.Credential{cr},
		Hosts:       []assets.Host{{ID: assets.NewID(), Address: "h", User: "u", Auth: assets.AuthCredential, CredentialID: "missing"}},
	}, Secrets: map[string][]byte{keychain.Cred(cr.ID, keychain.Password): []byte("pw")}}
	if err := a.restoreBackup(en, p); backupErrText(en, err) != en.BackupDamaged() {
		t.Fatalf("err %v", err)
	}
	if ok, _ := a.kc.Exists(keychain.Cred(cr.ID, keychain.Password)); ok || len(a.store.Credentials()) != 0 {
		t.Fatal("a failed restore left its secret or entries")
	}
}

func TestMergeSettings(t *testing.T) {
	cur := settings.Default()
	cur.RememberLayout = false
	cur.TrayClick, cur.TrayMaterial, cur.TrayHeight = settings.TrayClickWindow, settings.MaterialOpaque, 700
	cur.SidebarWidth, cur.UIFont = 260, "Local Font"
	cur.FilesDual, cur.FilesSplit, cur.EditorTreeWidth = true, 30, 300
	cur.AIProviders = []settings.AIProvider{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	cur.AIConsentHosts = []string{"h1"}
	in := settings.Default()
	in.SidebarWidth, in.UIFont, in.KeepAliveSeconds = 400, "Other Font", 99
	in.FilesSplit, in.EditorTreeWidth, in.EditorTreeHidden = 70, 500, true
	in.AIProviders = []settings.AIProvider{{ID: "b", Name: "B2"}, {ID: "c", Name: "C"}}
	in.AIConsentHosts = []string{"h1", "h2"}
	cur.BackupTargets = []settings.BackupTarget{{ID: "here", Name: "NAS", Kind: settings.BackupWebDAV}}
	in.BackupTargets = []settings.BackupTarget{{ID: "there", Name: "Other", Kind: settings.BackupS3}}
	cur.SessionLog.Dir, in.SessionLog.Dir = "/here/logs", "/there/logs"
	out := mergeSettings(cur, in)
	if out.TrayClick != cur.TrayClick || out.TrayMaterial != cur.TrayMaterial || out.TrayHeight != cur.TrayHeight {
		t.Fatalf("device tray preferences replaced: click=%q material=%q height=%d", out.TrayClick, out.TrayMaterial, out.TrayHeight)
	}
	if out.SessionLog.Dir != "/here/logs" {
		t.Fatalf("session log folder %q", out.SessionLog.Dir)
	}
	if out.RememberLayout || out.SidebarWidth != 260 || out.UIFont != "Local Font" || out.KeepAliveSeconds != 99 ||
		!out.FilesDual || out.FilesSplit != 30 || out.EditorTreeWidth != 300 || out.EditorTreeHidden {
		t.Fatalf("merged %+v", out)
	}
	if len(out.BackupTargets) != 1 || out.BackupTargets[0].ID != "here" {
		t.Fatalf("backup locations %+v", out.BackupTargets)
	}
	if len(out.AIProviders) != 3 || out.AIProviders[1].Name != "B2" || out.AIProviders[2].ID != "c" {
		t.Fatalf("providers %+v", out.AIProviders)
	}
	if len(out.AIConsentHosts) != 2 {
		t.Fatalf("consent %v", out.AIConsentHosts)
	}
}

func TestRestoreKeepsLocalDownloadDir(t *testing.T) {
	local, foreign := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		name     string
		localDir string
		incoming string
	}{
		{name: "configured", localDir: local, incoming: foreign},
		{name: "default", incoming: foreign},
		{name: "legacy", localDir: local},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			s := a.settings
			s.DownloadDir = tc.localDir
			if err := a.ApplySettings(s); err != nil {
				t.Fatal(err)
			}
			incoming := settings.Default()
			incoming.DownloadDir, incoming.KeepAliveSeconds = tc.incoming, 77
			encoded, err := json.Marshal(backupPayload{Settings: incoming})
			if err != nil {
				t.Fatal(err)
			}
			p, err := unpackBackup(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.restoreBackup(en, p); err != nil {
				t.Fatal(err)
			}
			saved, err := settings.Load(filepath.Join(a.p.DataDir, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			if saved.DownloadDir != tc.localDir || saved.KeepAliveSeconds != 77 {
				t.Fatalf("saved directory %q, want %q; keep-alive %d, want 77", saved.DownloadDir, tc.localDir, saved.KeepAliveSeconds)
			}
		})
	}
}

// TestRestoreKeepsLocalACP: a restore never adds or changes a program this
// computer starts. The ACP agents, a legacy single agent and the default
// agent of an older backup are ignored; other preferences still restore,
// and an export leaves them out.
func TestRestoreKeepsLocalACP(t *testing.T) {
	local := []settings.ACPConfig{{ID: "claude", Agent: "claude", Command: "/usr/local/bin/claude-acp", Args: []string{"--acp"}}}
	theirs := settings.Default()
	theirs.KeepAliveSeconds = 77
	theirs.AIACPAgents = []settings.ACPConfig{
		{ID: "claude", Agent: "claude", Command: "/tmp/evil", Args: []string{"--other"}},
		{ID: "extra", Command: "/tmp/other"},
	}
	theirs.AIDefaultAgent = "acp:extra"
	legacy := settings.Default()
	legacy.AIACP = &settings.ACPConfig{ID: "old", Command: "/tmp/legacy"}
	legacy.AIDefaultAgent = "acp"
	for _, tc := range []struct {
		name  string
		local []settings.ACPConfig
		def   string
		in    settings.Settings
	}{
		{"configured", local, "acp:claude", theirs},
		{"none", nil, "builtin", theirs},
		{"legacy", nil, "builtin", legacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := backupApp(t)
			s := a.settings
			s.AIACPAgents, s.AIDefaultAgent = slices.Clone(tc.local), tc.def
			if err := a.ApplySettings(s); err != nil {
				t.Fatal(err)
			}
			// An older backup carries them in its settings.
			raw, err := json.Marshal(backupPayload{Settings: tc.in})
			if err != nil {
				t.Fatal(err)
			}
			p, err := unpackBackup(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.restoreBackup(en, p); err != nil {
				t.Fatal(err)
			}
			got := a.settings
			if !slices.EqualFunc(got.AIACPAgents, tc.local, func(x, y settings.ACPConfig) bool { return x.SameLaunch(y) }) || got.AIACP != nil {
				t.Fatalf("ACP agents became %+v (legacy %+v), want %+v", got.AIACPAgents, got.AIACP, tc.local)
			}
			if got.AIDefaultAgent != tc.def {
				t.Fatalf("default agent became %q, want %q", got.AIDefaultAgent, tc.def)
			}
			if tc.in.KeepAliveSeconds == 77 && got.KeepAliveSeconds != 77 {
				t.Fatalf("other preferences not restored: keep-alive %d", got.KeepAliveSeconds)
			}
			out, err := a.collectBackup(false)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Settings.AIACPAgents) != 0 || out.Settings.AIACP != nil || out.Settings.AIDefaultAgent != "" {
				t.Fatalf("export carries ACP launch settings: %+v %+v %q", out.Settings.AIACPAgents, out.Settings.AIACP, out.Settings.AIDefaultAgent)
			}
		})
	}
}

// installBackupStore uses real ciphertext with an independent simulated device keychain.
func installBackupStore(t *testing.T, a *App) (keychain.OpenOptions, *keychain.Memory) {
	t.Helper()
	if err := a.kc.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	system := keychain.NewMemory()
	opts := keychain.OpenOptions{Path: filepath.Join(dir, "secrets.enc"), LockPath: filepath.Join(dir, "lock"), Scope: dir, System: system}
	var err error
	a.kc, err = keychain.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return opts, system
}

func TestBackupIndependentDeviceMasters(t *testing.T) {
	src, stt, dir := backupApp(t)
	_, srcSystem := installBackupStore(t, src)
	pw, _, _ := fillStore(t, src)
	if err := src.kc.Set(keychain.MCPToken, []byte("source-device-token")); err != nil {
		t.Fatal(err)
	}
	path := export(t, src, stt, dir, "correct horse", true)
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Open(sealed, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := unpackBackup(plain)
	if err != nil {
		t.Fatal(err)
	}
	// The archive is compressed: look for the token in what it holds.
	held, _ := json.Marshal(payload)
	if _, ok := payload.Secrets[keychain.MasterKey]; ok {
		t.Fatal("backup contains device master")
	}
	if _, ok := payload.Secrets[keychain.MCPToken]; ok || bytes.Contains(held, []byte("source-device-token")) {
		t.Fatal("backup contains device MCP token")
	}
	dst, _, _ := backupApp(t)
	opts, dstSystem := installBackupStore(t, dst)
	srcMaster, err := srcSystem.Get(keychain.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(srcMaster)
	dstMaster, err := dstSystem.Get(keychain.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(dstMaster)
	if bytes.Equal(srcMaster, dstMaster) || len(srcSystem.Keys()) != 1 || len(dstSystem.Keys()) != 1 {
		t.Fatal("devices do not have independent master-only system keychains")
	}
	// Even injected vault entries cannot replace device-local secrets.
	payload.Secrets[keychain.MasterKey] = srcMaster
	payload.Secrets[keychain.MCPToken] = []byte("foreign-token")
	if err := dst.kc.Set(keychain.MCPToken, []byte("destination-device-token")); err != nil {
		t.Fatal(err)
	}
	if err := dst.restoreBackup(en, payload); err != nil {
		t.Fatal(err)
	}
	if err := dst.kc.Close(); err != nil {
		t.Fatal(err)
	}
	dst.kc, err = keychain.Open(opts)
	if err != nil {
		t.Fatal("destination cannot reopen restored ciphertext:", err)
	}
	if value, err := dst.kc.Get(keychain.Cred(pw.ID, keychain.Password)); err != nil || string(value) != "hunter2" {
		t.Fatal("restored credential did not survive encrypted-store reopen")
	}
	if value, err := dst.kc.Get(keychain.MCPToken); err != nil || string(value) != "destination-device-token" {
		t.Fatal("restore replaced the device MCP token")
	}
	if ok, err := dst.kc.Exists(keychain.MasterKey); ok || err != nil {
		t.Fatal("restore imported the source master into the secret store")
	}
	ciphertext, err := os.ReadFile(opts.Path)
	if err != nil || bytes.Contains(ciphertext, []byte("hunter2")) {
		t.Fatal("restored credential is not encrypted on disk")
	}
}

func TestRestoreBatchFailureKeepsPreviousSecrets(t *testing.T) {
	a, _, backend := newFlakyApp(t)
	first := addCred(t, a, "first", "old-first")
	second := addCred(t, a, "second", "old-second")
	firstKey := keychain.Cred(first.ID, keychain.Password)
	secondKey := keychain.Cred(second.ID, keychain.Password)
	failed := false
	backend.failOn(func(key string) bool {
		if key == secondKey && !failed {
			failed = true
			return true
		}
		return false
	}, nil)
	payload := backupPayload{Assets: a.store.Snapshot(), Settings: a.settings, Secrets: map[string][]byte{
		firstKey: []byte("new-first"), secondKey: []byte("new-second"),
	}}
	if err := a.restoreBackup(en, payload); err == nil || !failed {
		t.Fatal("restore bypassed injected batch write failure")
	}
	for key, want := range map[string]string{firstKey: "old-first", secondKey: "old-second"} {
		if got, err := a.kc.Get(key); err != nil || string(got) != want {
			t.Fatal("failed restore changed an existing secret")
		}
	}
}

func TestRestoreMetadataFailureRollsBackEncryptedBatch(t *testing.T) {
	a, _, _ := backupApp(t)
	opts, _ := installBackupStore(t, a)
	path := filepath.Join(t.TempDir(), "hosts.json")
	var err error
	a.store, err = assets.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cr := addCred(t, a, "before", "old-secret")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := backupPayload{Assets: a.store.Snapshot(), Settings: a.settings, Secrets: map[string][]byte{
		keychain.Cred(cr.ID, keychain.Password): []byte("replacement-secret"),
	}}
	payload.Assets.Credentials[0].Name = "after"
	if err := a.restoreBackup(en, payload); err == nil {
		t.Fatal("restore ignored metadata write failure")
	}
	if got, ok := a.store.Credential(cr.ID); !ok || got.Name != "before" {
		t.Fatal("failed restore changed in-memory metadata")
	}
	if err := a.kc.Close(); err != nil {
		t.Fatal(err)
	}
	a.kc, err = keychain.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := a.kc.Get(keychain.Cred(cr.ID, keychain.Password)); err != nil || string(value) != "old-secret" {
		t.Fatal("metadata failure did not persist secret rollback")
	}
}
