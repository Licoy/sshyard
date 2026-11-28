package app

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
)

// TestHostKeyAndPassword: a key host may keep a password too, sent after
// the key to a server that wants both; it is kept unless replaced, goes
// with the key in a backup, and unticking drops it.
func TestHostKeyAndPassword(t *testing.T) {
	const pw = "extra-login-pass"
	srv, _ := keyServer(t)
	srv.Password, srv.KeyThenPassword = pw, true
	priv, pub := newKey(t)
	authorize(t, srv, pub)
	a, tt := newTestApp(t)
	host, port := splitAddr(t, srv.Addr)
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())

	edit := func(h *assets.Host) *hostForm {
		a.openEditor(h)
		f := a.form
		if h == nil {
			f.name, f.address, f.port, f.user, f.auth = "both", host, strconv.Itoa(port), "demo", en.CredKindKey()
			f.secret.setKey(openSSHKey(t, priv, ""), "")
		}
		return f
	}

	// Ticked with no password, a new host is not saved.
	f := edit(nil)
	f.keyPassword = true
	if a.SaveForm(en) || f.err != en.CredPasswordRequired() {
		t.Fatalf("saved without the password: %q", f.err)
	}
	f.secret.password = pw
	if !a.SaveForm(en) {
		t.Fatalf("not saved: %q", f.err)
	}
	h := a.Store().List()[0]
	if h.Auth != assets.AuthKey {
		t.Fatalf("auth %q", h.Auth)
	}
	cr, err := a.hostCredentials(h, nil)
	if err != nil || len(cr.PrivateKey) == 0 || cr.Password != pw {
		t.Fatalf("credentials %+v, %v", cr.Password, err)
	}
	if keys := backupSecretKeys(assets.Snapshot{Hosts: []assets.Host{h}}, nil); !slices.Contains(keys, keychain.Host(h.ID, keychain.Password)) {
		t.Errorf("a backup leaves the password out: %q", keys)
	}
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, a, tt, id)

	// Edited again, the blank field keeps the saved password.
	f = edit(&h)
	if !f.keyPassword || !f.keyPasswordSaved {
		t.Fatal("the editor does not show the saved password")
	}
	if !a.SaveForm(en) {
		t.Fatalf("not saved: %q", f.err)
	}
	if got, err := a.kc.Get(keychain.Host(h.ID, keychain.Password)); err != nil || string(got) != pw {
		t.Fatalf("password %q, %v", got, err)
	}

	// A clone gets a copy of it.
	a.openClone(h)
	a.form.name = "both copy"
	if !a.SaveForm(en) {
		t.Fatalf("clone not saved: %q", a.form.err)
	}
	for _, c := range a.Store().List() {
		if c.ID == h.ID {
			continue
		}
		if got, err := a.kc.Get(keychain.Host(c.ID, keychain.Password)); err != nil || string(got) != pw {
			t.Fatalf("clone password %q, %v", got, err)
		}
	}

	// Unticked, it is deleted and the key goes alone.
	f = edit(&h)
	f.keyPassword = false
	if !a.SaveForm(en) {
		t.Fatalf("not saved: %q", f.err)
	}
	if _, err := a.kc.Get(keychain.Host(h.ID, keychain.Password)); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("password kept: %v", err)
	}
	if cr, err := a.hostCredentials(h, nil); err != nil || cr.Password != "" {
		t.Fatalf("credentials %q, %v", cr.Password, err)
	}
	noSecrets(t, a.p.DataDir, pw)
}
