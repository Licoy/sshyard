package app

import (
	"errors"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/keys"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// Host-only choices stay out of quick connect and credential rebinding.
func (a *App) hostAuthChoices(m locales.Messages) []authChoice {
	return append([]authChoice{{m.CredKindPassword(), assets.AuthPassword, ""}, {m.CredKindKey(), assets.AuthKey, ""}, {m.AuthNone(), assets.AuthNone, ""}}, a.authChoices(m)...)
}

func (a *App) hostSecretFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	s := &f.secret
	switch f.auth {
	case m.CredKindPassword():
		ui.Field(c, m.FieldPassword(), func() {
			ph := ""
			if f.orig.Auth == assets.AuthPassword {
				ph = m.PasswordUnchanged()
			}
			formTextInput(c.Key("hf:password"), &s.password).Password().Placeholder(ph)
		})
	case m.CredKindKey():
		ui.Field(c, m.FieldPrivateKey(), func() { a.keyPicker(c, m, tok, s) })
		ui.Field(c, m.FieldCertificate(), func() { a.certPicker(c, m, tok, s) })
		if s.encrypted() || s.genKey != nil {
			ui.Field(c, m.FieldPassphrase(), func() { formTextInput(c.Key("hf:pass"), &s.passphrase).Password().Placeholder(m.PasswordUnchanged()) })
			ui.Field(c, "", func() { formCheckbox(c.Key("hf:ask"), &s.askPass, m.AskPassphraseEachTime()) })
		}
	}
	if s.err != "" {
		ui.Text(c, s.err).FontSize(theme.Footnote).TextColor(tok.Danger)
	}
}

func hostFormNeedsSecrets(f *hostForm, h assets.Host) bool {
	return startupChanges(f, h) != nil || hostAuthNeedsSecrets(f, h)
}

// hostAuthNeedsSecrets reports whether saving h changes or copies the
// secrets it logs in with.
func hostAuthNeedsSecrets(f *hostForm, h assets.Host) bool {
	if f.from != "" && (h.Auth == assets.AuthPassword || h.Auth == assets.AuthKey) {
		return true // copies the secrets it keeps
	}
	if h.Auth != f.orig.Auth {
		return h.Auth == assets.AuthPassword || h.Auth == assets.AuthKey || f.orig.Auth == assets.AuthPassword || f.orig.Auth == assets.AuthKey
	}
	switch h.Auth {
	case assets.AuthPassword:
		return f.secret.password != ""
	case assets.AuthKey:
		return f.secret.loaded() || f.secret.passphrase != "" || f.secret.askPass != f.orig.AskPassphrase
	}
	return false
}

// hostSecretChanges validates new input and preserves existing, blank fields.
func (a *App) hostSecretChanges(m locales.Messages, f *hostForm, h assets.Host) (assets.Host, []keychain.Change, error) {
	if !hostAuthNeedsSecrets(f, h) && (!a.unlocked() || (h.Auth != assets.AuthPassword && h.Auth != assets.AuthKey)) {
		return h, nil, nil
	}
	s := &f.secret
	// src owns the saved secrets blank fields keep: the cloned host's
	// for a clone, which gets copies of them.
	src := h.ID
	if f.from != "" {
		src = f.from
	}
	values := map[keychain.Kind][]byte{}
	keep := map[keychain.Kind]bool{}
	fail := func(text string) (assets.Host, []keychain.Change, error) { return h, nil, errors.New(text) }
	switch h.Auth {
	case assets.AuthPassword:
		if s.password != "" {
			values[keychain.Password] = []byte(s.password)
		} else {
			exists, err := a.kc.Exists(keychain.Host(src, keychain.Password))
			if err != nil {
				return h, nil, err
			}
			if f.orig.Auth != h.Auth || !exists {
				return fail(m.CredPasswordRequired())
			}
			if src == h.ID {
				keep[keychain.Password] = true
			} else if values[keychain.Password], err = a.kc.Get(keychain.Host(src, keychain.Password)); err != nil {
				return h, nil, err
			}
		}
	case assets.AuthKey:
		var k keys.Key
		var err error
		var saved []byte // the key kept from the saved one
		pass := []byte(s.passphrase)
		switch {
		case s.genKey != nil:
			k, err = keys.Encode(s.genKey, s.comment(), pass)
		case s.keyData != nil:
			k, err = a.loadKey(s.keyData, pass)
			if a.askKeyCost(err, s.keyData, pass, func() {
				if a.form == f {
					a.SaveForm(m)
				}
			}) {
				return h, nil, errCostAsked
			}
		case f.orig.Auth == assets.AuthKey:
			var data []byte
			data, err = a.kc.Get(keychain.Host(src, keychain.PrivateKey))
			if err != nil {
				return h, nil, err
			}
			if len(pass) == 0 && !s.askPass {
				pass, err = a.kc.Get(keychain.Host(src, keychain.Passphrase))
				if errors.Is(err, keychain.ErrNotFound) {
					err = nil
				}
				if err != nil {
					return h, nil, err
				}
			}
			k, err = keys.Import(data, pass)
			saved = data
		default:
			return fail(m.CredKeyRequired())
		}
		savedUnread := saved != nil && len(pass) == 0
		if errors.Is(err, keys.ErrPassphraseNeeded) && s.askPass && (k.Public != nil || savedUnread) && !keys.IsPPK(s.keyData) {
			err = nil
		}
		if err != nil {
			return fail(keyError(m, err))
		}
		if s.cert != "" && !s.certInfo.Matches(k.Fingerprint()) {
			return fail(m.ErrCertMismatch())
		}
		switch {
		case saved == nil:
			values[keychain.PrivateKey] = k.PEM
		case src == h.ID:
			keep[keychain.PrivateKey] = true
		default:
			values[keychain.PrivateKey] = saved
		}
		h.AskPassphrase = k.Encrypted && s.askPass
		if k.Encrypted && !h.AskPassphrase {
			if len(pass) == 0 {
				return fail(m.ErrKeyPassphraseNeeded())
			}
			values[keychain.Passphrase] = pass
		}
	}
	var changes []keychain.Change
	for _, kind := range keychain.Kinds {
		if keep[kind] {
			continue
		}
		value, set := values[kind]
		changes = append(changes, keychain.Change{Key: keychain.Host(h.ID, kind), Value: value, Delete: !set})
	}
	return h, changes, nil
}

func (a *App) saveHostForm(m locales.Messages, f *hostForm, h assets.Host) (assets.Host, error) {
	if f.isNew {
		h.ID = assets.NewID()
	}
	h, changes, err := a.hostSecretChanges(m, f, h)
	if err != nil {
		return assets.Host{}, err
	}
	return a.saveHostSecrets(h, f.isNew, append(changes, startupChanges(f, h)...))
}

// saveHostSecrets compensates secret changes if metadata cannot be saved.
func (a *App) saveHostSecrets(h assets.Host, fresh bool, changes []keychain.Change) (assets.Host, error) {
	tx := &secretTx{kc: a.kc}
	err := tx.setBatch(changes)
	var saved assets.Host
	if err == nil {
		if fresh {
			saved, err = a.store.AddHost(h)
		} else {
			saved, err = a.store.Update(h)
		}
	}
	if err != nil {
		return assets.Host{}, errors.Join(err, tx.rollback())
	}
	a.mu.Lock()
	for _, change := range changes {
		delete(a.typed, change.Key)
		a.typedGen[change.Key]++
	}
	a.mu.Unlock()
	return saved, nil
}

func (a *App) deleteHostSecrets(id string) error {
	if h, ok := a.store.Get(id); !ok || (h.Auth != assets.AuthPassword && h.Auth != assets.AuthKey && !h.Startup) {
		return a.store.Delete(id)
	}
	tx := &secretTx{kc: a.kc}
	changes := hostSecretDeletes(id)
	if err := tx.setBatch(changes); err != nil {
		return &keychainDeleteError{errors.Join(err, tx.rollback())}
	}
	if err := a.store.Delete(id); err != nil {
		return errors.Join(err, tx.rollback())
	}
	a.mu.Lock()
	for _, change := range changes {
		delete(a.typed, change.Key)
		a.typedGen[change.Key]++
	}
	a.mu.Unlock()
	return nil
}

// hostCredentials is shared by interactive and saved-only MCP connections.
func (a *App) hostCredentials(h assets.Host, typed func(string) string) (session.Credentials, error) {
	var out session.Credentials
	read := func(kind keychain.Kind) ([]byte, error) {
		key := keychain.Host(h.ID, kind)
		if typed != nil {
			if value := typed(key); value != "" {
				return []byte(value), nil
			}
		}
		return a.kc.Get(key)
	}
	if h.Auth == assets.AuthPassword {
		value, err := read(keychain.Password)
		out.Password = string(value)
		if errors.Is(err, keychain.ErrNotFound) {
			err = session.ErrNoCredentials
		}
		return out, err
	}
	var err error
	out.Certificate = []byte(h.Certificate)
	out.PrivateKey, err = a.kc.Get(keychain.Host(h.ID, keychain.PrivateKey))
	if errors.Is(err, keychain.ErrNotFound) {
		err = session.ErrNoCredentials
	}
	if err != nil {
		return out, err
	}
	if h.AskPassphrase {
		if typed == nil {
			return out, errMCPNeedsInput
		}
		out.Passphrase = []byte(typed(keychain.Host(h.ID, keychain.Passphrase)))
	} else {
		out.Passphrase, err = read(keychain.Passphrase)
		if errors.Is(err, keychain.ErrNotFound) {
			err = nil
		}
	}
	if err == nil && typed == nil {
		if _, parseErr := keys.Import(out.PrivateKey, out.Passphrase); parseErr != nil {
			return out, errMCPNeedsInput
		}
	}
	return out, err
}

func hostSecretDeletes(id string) []keychain.Change {
	var changes []keychain.Change
	for _, kind := range keychain.Kinds {
		changes = append(changes, keychain.Change{Key: keychain.Host(id, kind), Delete: true})
	}
	return append(changes, keychain.Change{Key: keychain.HostStartup(id), Delete: true})
}

func (a *App) forgetHostSecrets(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, change := range hostSecretDeletes(id) {
		delete(a.typed, change.Key)
		a.typedGen[change.Key]++
	}
}
