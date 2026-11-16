package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
)

func savePasswordHost(t *testing.T, a *App, address string) assets.Host {
	t.Helper()
	a.openEditor(nil)
	a.form.name, a.form.address, a.form.user = "host", address, "demo"
	a.form.auth, a.form.secret.password = en.CredKindPassword(), "pw"
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	return a.store.List()[0]
}

func TestHostOwnedPasswordConnectionAndLifecycle(t *testing.T) {
	srv, _ := keyServer(t)
	a, tt := newTestApp(t)
	h := savePasswordHost(t, a, "127.0.0.1")
	host, port := splitAddr(t, srv.Addr)
	h.Address, h.Port = host, port
	h, _ = a.store.Update(h)
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	if len(a.store.Credentials()) != 0 {
		t.Fatal("created reusable credential")
	}
	if got, err := a.mcpCredentials(h); err != nil || got.Password != "pw" {
		t.Fatalf("MCP credentials %+v %v", got, err)
	}
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, a, tt, id)
	a.openEditor(&h)
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	if value, err := a.kc.Get(keychain.Host(h.ID, keychain.Password)); err != nil || string(value) != "pw" {
		t.Fatalf("blank edit lost secret: %q %v", value, err)
	}
	oldKey := a.hopKey(h)
	a.openEditor(&h)
	a.form.secret.password = "replacement"
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	if oldKey == a.hopKey(h) {
		t.Fatal("pool identity unchanged")
	}
	h, _ = a.store.Get(h.ID)
	a.openEditor(&h)
	a.form.auth = en.AuthAgent()
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	if a.kc.Has(keychain.Host(h.ID, keychain.Password)) {
		t.Fatal("obsolete password retained")
	}
	noSecrets(t, a.p.DataDir, "replacement")
}

func TestHostOwnedKeyConnectionAndPrompt(t *testing.T) {
	srv, _ := keyServer(t)
	priv, pub := newKey(t)
	authorize(t, srv, pub)
	a, tt := newTestApp(t)
	host, port := splitAddr(t, srv.Addr)
	a.openEditor(nil)
	f := a.form
	f.name, f.address, f.user, f.port = "host", host, "demo", "22"
	f.port = strings.TrimPrefix(srv.Addr, host+":")
	f.auth = en.CredKindKey()
	f.secret.setKey(openSSHKey(t, priv, "secret-pass"), "")
	f.secret.passphrase = "secret-pass"
	if !a.SaveForm(en) {
		t.Fatal(f.err)
	}
	h := a.store.List()[0]
	if h.Port != port {
		t.Fatal(h.Port)
	}
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, a, tt, id)
	a.openEditor(&h)
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	a.openEditor(&h)
	a.form.secret.askPass = true
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	h, _ = a.store.Get(h.ID)
	if a.kc.Has(keychain.Host(h.ID, keychain.Passphrase)) {
		t.Fatal("saved run-only passphrase")
	}
	if err := a.mcpLogin(h); !errors.Is(err, errMCPNeedsInput) {
		t.Fatal(err)
	}
	if key, pass := a.promptKey(h, session.KindPassphrase); key != keychain.Host(h.ID, keychain.Passphrase) || !pass {
		t.Fatal(key, pass)
	}
	if err := a.DeleteHost(h.ID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range keychain.Kinds {
		if a.kc.Has(keychain.Host(h.ID, kind)) {
			t.Fatal("deleted host secret retained")
		}
	}
}

func TestHostOwnedAuthMouseFields(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	a.openEditor(nil)
	tt.Frame()
	mustClick(t, tt, a.askLabel(m))
	mustClick(t, tt, m.CredKindPassword())
	if a.form.auth != m.CredKindPassword() {
		t.Fatalf("auth %q, want password", a.form.auth)
	}
	mustClick(t, tt, m.CredKindPassword())
	mustClick(t, tt, m.CredKindKey())
	if a.form.auth != m.CredKindKey() {
		t.Fatalf("auth %q, want key", a.form.auth)
	}
	for _, choice := range a.quickChoices(m) {
		if choice.method == assets.AuthPassword || choice.method == assets.AuthKey {
			t.Fatal("host choices leaked to quick connect")
		}
	}
}

func TestHostOwnedSecretsRollback(t *testing.T) {
	a, _, kc := newFlakyApp(t)
	h := savePasswordHost(t, a, "127.0.0.1")
	kc.failOn(suffix("/password"), nil)
	a.openEditor(&h)
	a.form.secret.password = "new"
	if a.SaveForm(en) {
		t.Fatal("saved with failed secret write")
	}
	kc.failOn(nil, nil)
	if value, _ := a.kc.Get(keychain.Host(h.ID, keychain.Password)); string(value) != "pw" {
		t.Fatal("failed save lost password")
	}
	// Force metadata persistence to fail without weakening the secret backend.
	path := filepath.Join(a.p.DataDir, "hosts.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	a.openEditor(&h)
	a.form.secret.password = "metadata-failed"
	if a.SaveForm(en) {
		t.Fatal("saved with failed metadata write")
	}
	if value, _ := a.kc.Get(keychain.Host(h.ID, keychain.Password)); string(value) != "pw" {
		t.Fatal("metadata failure lost password")
	}
	if err := a.DeleteHost(h.ID); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	if value, _ := a.kc.Get(keychain.Host(h.ID, keychain.Password)); string(value) != "pw" {
		t.Fatal("failed delete lost password")
	}
	os.Remove(path)
	os.WriteFile(path, data, 0600)
}

func TestHostOwnedSecretsBackup(t *testing.T) {
	a, _ := newTestApp(t)
	h := savePasswordHost(t, a, "127.0.0.1")
	p, err := a.collectBackup(true)
	if err != nil {
		t.Fatal(err)
	}
	key := keychain.Host(h.ID, keychain.Password)
	if string(p.Secrets[key]) != "pw" {
		t.Fatal("host secret absent from backup")
	}
	b, _ := newTestApp(t)
	if err := b.restoreBackup(en, p); err != nil {
		t.Fatal(err)
	}
	if value, err := b.kc.Get(key); err != nil || string(value) != "pw" {
		t.Fatal("host secret not restored", err)
	}
	b.typed[key] = "stale"
	delete(p.Secrets, key)
	if err := b.restoreBackup(en, p); err != nil {
		t.Fatal(err)
	}
	if b.kc.Has(key) || b.typed[key] != "" {
		t.Fatal("omitted backup borrowed old secret")
	}
	p.Assets.Hosts[0].ID = "../malicious"
	if err := b.restoreBackup(en, p); !errors.Is(err, errBadBackup) {
		t.Fatal(err)
	}
}

func TestHostOwnedSecretValidationAndReadFailures(t *testing.T) {
	a, _, kc := newFlakyApp(t)
	a.openEditor(nil)
	f := a.form
	f.address, f.user, f.auth = "example.com", "demo", en.CredKindPassword()
	if a.SaveForm(en) || !strings.Contains(f.err, en.CredPasswordRequired()) {
		t.Fatal("missing password accepted", f.err)
	}
	f.auth = en.CredKindKey()
	if a.SaveForm(en) || !strings.Contains(f.err, en.CredKeyRequired()) {
		t.Fatal("missing key accepted", f.err)
	}
	f.secret.setKey([]byte("not a private key"), "")
	if a.SaveForm(en) || !strings.Contains(f.err, en.ErrNotAKey()) {
		t.Fatal("invalid key accepted", f.err)
	}
	priv, _ := newKey(t)
	f.secret.setKey(openSSHKey(t, priv, "right"), "")
	f.secret.passphrase = "wrong"
	if a.SaveForm(en) || !strings.Contains(f.err, en.ErrKeyPassphraseWrong()) {
		t.Fatal("wrong passphrase accepted", f.err)
	}
	h := savePasswordHost(t, a, "example.com")
	kc.mu.Lock()
	kc.failGet = suffix("/password")
	kc.mu.Unlock()
	a.openEditor(&h)
	if a.SaveForm(en) {
		t.Fatal("secret read failure ignored")
	}
	kc.mu.Lock()
	kc.failGet = nil
	kc.mu.Unlock()
	if value, _ := a.kc.Get(keychain.Host(h.ID, keychain.Password)); string(value) != "pw" {
		t.Fatal("failed read changed password")
	}
}

func TestHostOwnedSwitchToReusableKeyCleansSecrets(t *testing.T) {
	a, _ := newTestApp(t)
	h := savePasswordHost(t, a, "example.com")
	cr, err := a.store.SaveCredential(assets.Credential{Name: "key", Kind: assets.CredKey, User: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SwitchToKey(h.ID, cr.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	if a.kc.Has(keychain.Host(h.ID, keychain.Password)) {
		t.Fatal("switch retained host password")
	}
	got, _ := a.store.Get(h.ID)
	if got.Auth != assets.AuthCredential || got.CredentialID != cr.ID {
		t.Fatal(got)
	}
}

func TestHostOwnedMissingPasswordPrompts(t *testing.T) {
	srv, _ := keyServer(t)
	a, tt := newTestApp(t)
	h := addSrvHost(t, a, srv, "missing", assets.AuthPassword, "")
	if _, err := a.Connect(h.ID, openAt{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "missing saved password prompt", func() bool { return a.prompting })
	if a.prompt.key != keychain.Host(h.ID, keychain.Password) || a.prompt.passphrase {
		t.Fatal(a.prompt)
	}
	a.prompt.value = "pw"
	a.SubmitPrompt()
	for _, c := range a.conns {
		waitFor(t, tt, "password retry connected", func() bool { return c.s.Status().State == session.Connected })
	}
	if a.kc.Has(keychain.Host(h.ID, keychain.Password)) {
		t.Fatal("one-run password saved")
	}
}

func TestHostOwnedLegacyPEMBlankEdit(t *testing.T) {
	a, _ := newTestApp(t)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy encrypted PEM remains supported for imported SSH keys.
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(priv), []byte("legacy-pass"), x509.PEMCipherAES256) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	a.openEditor(nil)
	f := a.form
	f.address, f.user, f.auth = "example.com", "demo", en.CredKindKey()
	f.secret.setKey(pem.EncodeToMemory(block), "")
	f.secret.passphrase, f.secret.askPass = "legacy-pass", true
	if !a.SaveForm(en) {
		t.Fatal(f.err)
	}
	h := a.store.List()[0]
	a.openEditor(&h)
	a.form.name = "renamed"
	if !a.SaveForm(en) {
		t.Fatal("blank existing PEM edit failed", a.form.err)
	}
	if a.kc.Has(keychain.Host(h.ID, keychain.Passphrase)) {
		t.Fatal("stored run-only passphrase")
	}
	a.openEditor(&h)
	a.form.secret.passphrase = "wrong"
	if a.SaveForm(en) || !strings.Contains(a.form.err, en.ErrKeyPassphraseWrong()) {
		t.Fatal("new wrong passphrase accepted")
	}
}

func TestHostCloneCopiesSecrets(t *testing.T) {
	a, _ := newTestApp(t)
	src := savePasswordHost(t, a, "10.0.0.1")
	a.openClone(src)
	a.form.address = "10.0.0.2"
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	var clone assets.Host
	for _, h := range a.store.List() {
		if h.ID != src.ID {
			clone = h
		}
	}
	if clone.ID == "" || clone.Address != "10.0.0.2" || clone.User != src.User || clone.Auth != assets.AuthPassword {
		t.Fatalf("clone %+v", clone)
	}
	if got, _ := a.store.Get(src.ID); got.Address != "10.0.0.1" {
		t.Fatalf("source changed: %+v", got)
	}
	if value, err := a.kc.Get(keychain.Host(clone.ID, keychain.Password)); err != nil || string(value) != "pw" {
		t.Fatalf("clone password %q %v", value, err)
	}
	if err := a.DeleteHost(src.ID); err != nil {
		t.Fatal(err)
	}
	if value, err := a.kc.Get(keychain.Host(clone.ID, keychain.Password)); err != nil || string(value) != "pw" {
		t.Fatalf("clone lost password with its source: %q %v", value, err)
	}

	priv, _ := newKey(t)
	a.openEditor(nil)
	f := a.form
	f.name, f.address, f.user = "keyed", "10.0.0.3", "demo"
	f.auth = en.CredKindKey()
	f.secret.setKey(openSSHKey(t, priv, "secret-pass"), "")
	f.secret.passphrase = "secret-pass"
	if !a.SaveForm(en) {
		t.Fatal(f.err)
	}
	var keyed assets.Host
	for _, h := range a.store.List() {
		if h.Name == "keyed" {
			keyed = h
		}
	}
	a.openClone(keyed)
	a.form.name = "keyed-2"
	if !a.SaveForm(en) {
		t.Fatal(a.form.err)
	}
	for _, h := range a.store.List() {
		if h.Name != "keyed-2" {
			continue
		}
		for _, kind := range []keychain.Kind{keychain.PrivateKey, keychain.Passphrase} {
			want, _ := a.kc.Get(keychain.Host(keyed.ID, kind))
			if got, err := a.kc.Get(keychain.Host(h.ID, kind)); err != nil || string(got) != string(want) {
				t.Fatalf("clone %s %v", kind, err)
			}
		}
		return
	}
	t.Fatal("key clone not saved")
}
