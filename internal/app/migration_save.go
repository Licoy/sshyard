package app

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/keys"
	"github.com/Licoy/sshyard/internal/migration"
	"github.com/Licoy/sshyard/locales"
)

// migrationSnapshot resolves the complete batch before writing any secret.
func (a *App) migrationSnapshot(m locales.Messages, f *migrationForm) (assets.Snapshot, map[*migrationRow]string, error) {
	var snap assets.Snapshot
	ids := map[*migrationRow]string{}
	targets := map[string]assets.Host{}
	for _, h := range f.baseline {
		targets[migrationTarget(h.Address, h.Port, h.User)] = h
	}
	for _, r := range f.rows {
		if !r.selected {
			continue
		}
		e := r.entry
		target := migrationTarget(e.Address, e.Port, e.User)
		old, exists := targets[target]
		if exists && r.action == migrationSkip {
			if exists {
				ids[r] = old.ID
			}
			continue
		}
		for _, issue := range r.entry.Issues {
			if issue.Code == "missing-reference" || issue.Code == "duplicate-id" || issue.Code == "invalid-entry" {
				return snap, nil, errors.New(m.MigrationDependency())
			}
		}
		h := assets.Host{ID: assets.NewID(), Auth: assets.AuthAsk, CwdFollow: true, Created: time.Now()}
		if exists && r.action == migrationOverwrite {
			h = old
		}
		h.Name, h.Address, h.Port, h.User, h.Group = r.name, e.Address, e.Port, e.User, r.group
		h.Updated = time.Now()
		if len(e.Jumps) > 0 {
			h.JumpChain = nil
		}
		h.Normalize()
		if err := h.Validate(); err != nil {
			return snap, nil, errors.New(fieldError(m, err))
		}
		ids[r] = h.ID
		// Internal duplicates have the same conflict choices as stored hosts.
		replaced := false
		for i := range snap.Hosts {
			if snap.Hosts[i].ID == h.ID {
				snap.Hosts[i] = h
				replaced = true
			}
		}
		if !replaced {
			snap.Hosts = append(snap.Hosts, h)
		}
		targets[target] = h
	}
	for id, r := range migrationFinalRows(f, ids) {
		if len(r.entry.Jumps) == 0 {
			continue
		}
		for i := range snap.Hosts {
			if snap.Hosts[i].ID == id {
				snap.Hosts[i].JumpChain = nil
				for _, hop := range r.entry.Jumps {
					resolved, err := migrationResolveHop(hop, f, ids, targets)
					if err != nil {
						return snap, nil, errors.New(m.MigrationDependency())
					}
					snap.Hosts[i].JumpChain = append(snap.Hosts[i].JumpChain, resolved)
				}
			}
		}
	}
	all := map[string]assets.Host{}
	for _, h := range f.baseline {
		all[h.ID] = h
	}
	for _, h := range snap.Hosts {
		all[h.ID] = h
	}
	for _, h := range snap.Hosts {
		if !migrationValidChain(h.ID, all, map[string]bool{}) {
			return snap, nil, errors.New(m.MigrationDependency())
		}
	}
	return snap, ids, nil
}
func migrationFinalRows(f *migrationForm, ids map[*migrationRow]string) map[string]*migrationRow {
	rows := map[string]*migrationRow{}
	conflicts, _ := f.choices()
	for _, r := range f.rows {
		if id, ok := ids[r]; ok && r.selected && !(conflicts[r] && r.action == migrationSkip) {
			rows[id] = r
		}
	}
	return rows
}
func migrationResolveHop(hop migration.Hop, f *migrationForm, ids map[*migrationRow]string, targets map[string]assets.Host) (string, error) {
	if hop.Ref != "" {
		match := ""
		for _, r := range f.rows {
			if r.entry.ID == hop.Ref {
				id, ok := ids[r]
				if !ok || match != "" {
					return "", errors.New("unresolved reference")
				}
				match = id
			}
		}
		if match != "" {
			return match, nil
		}
		return "", errors.New("unresolved reference")
	}
	if h, ok := targets[migrationTarget(hop.Address, hop.Port, hop.User)]; ok {
		return h.ID, nil
	}
	return "", errors.New("unresolved hop")
}
func migrationValidChain(id string, hosts map[string]assets.Host, seen map[string]bool) bool {
	if seen[id] {
		return false
	}
	h, ok := hosts[id]
	if !ok {
		return false
	}
	seen[id] = true
	for _, jump := range h.JumpChain {
		if !migrationValidChain(jump, hosts, seen) {
			return false
		}
	}
	return true
}

// migrationRowError is a row's credential that cannot be saved.
type migrationRowError struct {
	row  *migrationRow
	text string
}

func (e *migrationRowError) Error() string { return e.text }

// migrationAuth is how h logs in as r says, and the secrets it saves. A
// PuTTY key whose passphrase derivation costs more than usual is a
// *keys.CostError unless costly, the user having agreed.
func migrationAuth(m locales.Messages, h assets.Host, r *migrationRow, costly bool) (assets.Host, []keychain.Change, error) {
	e := r.entry
	var changes []keychain.Change
	rowError := func(err error) error {
		text := m.MigrationKeyError()
		if r.auth != "" {
			text = keyError(m, err)
		}
		return &migrationRowError{r, text}
	}
	switch r.auth {
	case "":
	case assets.AuthPassword:
		// A blank password keeps that of the password host overwritten.
		e.PrivateKey, e.KeyPath, e.Password = nil, "", []byte(r.password)
	case assets.AuthKey:
		e.Password = nil
	default:
		if h.Auth == assets.AuthPassword || h.Auth == assets.AuthKey {
			changes = hostSecretDeletes(h.ID)
		}
		h.Auth, h.CredentialID, h.AskPassphrase = r.auth, r.cred, false
		h.Normalize()
		return h, changes, nil
	}
	data := e.PrivateKey
	var err error
	if e.KeyPath != "" && len(data) == 0 {
		data, err = readKeyFile(e.KeyPath)
	}
	if err != nil {
		return h, nil, rowError(err)
	}
	switch {
	case len(data) > 0:
		pass := e.Passphrase
		if r.pass != "" {
			pass = []byte(r.pass)
		}
		load := importPlainKey
		if costly {
			load = importCostlyKey
		}
		k, err := load(data, pass)
		if errors.Is(err, keys.ErrPassphraseNeeded) && len(k.PEM) > 0 {
			err = nil
		}
		if errors.Is(err, keys.ErrCostly) {
			return h, nil, err // the batch asks, and runs again
		}
		if err != nil {
			return h, nil, rowError(err)
		}
		if h.Certificate != "" {
			cert, err := keys.ParseCert([]byte(h.Certificate))
			if err != nil || k.Fingerprint() == "" || !cert.Matches(k.Fingerprint()) {
				h.Certificate = ""
			}
		}
		h.Auth, h.CredentialID, h.AskPassphrase = assets.AuthKey, "", k.Encrypted && len(pass) == 0
		changes = hostSecretDeletes(h.ID)
		changes = append(changes, keychain.Change{Key: keychain.Host(h.ID, keychain.PrivateKey), Value: k.PEM})
		if k.Encrypted && len(pass) > 0 {
			changes = append(changes, keychain.Change{Key: keychain.Host(h.ID, keychain.Passphrase), Value: pass})
		}
	case len(e.Password) > 0:
		h.Auth, h.CredentialID, h.AskPassphrase = assets.AuthPassword, "", false
		changes = hostSecretDeletes(h.ID)
		changes = append(changes, keychain.Change{Key: keychain.Host(h.ID, keychain.Password), Value: e.Password})
	}
	return h, changes, nil
}
func (a *App) saveMigration(m locales.Messages, f *migrationForm) (int, error) {
	if !a.migrationUnchanged(f) {
		return 0, errors.New(m.MigrationStale())
	}
	snap, ids, err := a.migrationSnapshot(m, f)
	if err != nil {
		return 0, err
	}
	var changes []keychain.Change
	if f.saveSecrets {
		for id, r := range migrationFinalRows(f, ids) {
			if !r.saveSecret {
				continue
			}
			for i, h := range snap.Hosts {
				if h.ID == id {
					updated, batch, err := migrationAuth(m, h, r, f.costly)
					if err != nil {
						return 0, err
					}
					snap.Hosts[i] = updated
					changes = append(changes, batch...)
				}
			}
		}
	}
	// Keep one final value per secret key, including internal overwrite choices.
	final := make([]keychain.Change, 0, len(changes))
	indices := map[string]int{}
	for _, change := range changes {
		if i, ok := indices[change.Key]; ok {
			final[i] = change
		} else {
			indices[change.Key] = len(final)
			final = append(final, change)
		}
	}
	tx := &secretTx{kc: a.kc}
	fail := func(err error) (int, error) {
		msg := m.MigrationSaveError()
		if errors.Is(err, assets.ErrSnapshotChanged) {
			msg = m.MigrationStale()
		}
		if errors.Is(err, keychain.ErrLocked) {
			msg = secretStoreErrorText(m, err)
		}
		if rollback := tx.rollback(); rollback != nil {
			msg = m.ErrKeychainRestore(msg, secretStoreErrorText(m, rollback))
		}
		// Error details are mapped by operation; source contents are never shown.
		return 0, errors.New(msg)
	}
	if err := tx.setBatch(final); err != nil {
		return fail(err)
	}
	if err := a.store.RestoreIfHostsUnchanged(snap, f.baseline); err != nil {
		return fail(err)
	}
	a.mu.Lock()
	for _, change := range final {
		delete(a.typed, change.Key)
		a.typedGen[change.Key]++
	}
	a.mu.Unlock()
	return len(snap.Hosts), nil
}

func migrationIssue(m locales.Messages, issue migration.Issue) string {
	var text string
	switch issue.Code {
	case "unsupported-protocol":
		text = m.MigrationUnsupportedProtocol()
	case "unsupported-field":
		text = m.MigrationUnsupportedField()
	case "encrypted-secret":
		text = m.MigrationEncryptedSecret()
	case "invalid-entry":
		text = m.MigrationInvalidEntry()
	case "damaged-entry":
		text = m.MigrationDamagedEntry()
	case "default-user":
		text = m.MigrationDefaultUser()
	case "missing-reference":
		text = m.MigrationDependency()
	case "duplicate-id":
		text = m.MigrationDuplicateID()
	case "empty-source":
		text = m.MigrationEmpty()
	case "unclosed-quote":
		text = m.MigrationUnclosedQuote()
	default:
		text = m.MigrationUnsupportedField()
	}
	// Only bounded labels are displayed, never raw parser errors or secret values.
	label := issue.Field
	if label == "" {
		label = issue.Name
	}
	label = strings.Map(func(r rune) rune {
		if r < ' ' {
			return -1
		}
		return r
	}, label)
	if len([]rune(label)) > 100 {
		label = string([]rune(label)[:100]) + "…"
	}
	if label != "" {
		text += " · " + label
	}
	if issue.Line > 0 {
		text += " · " + m.MigrationLine(strconv.Itoa(issue.Line))
	}
	return text
}
