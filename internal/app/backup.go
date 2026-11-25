package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/backup"
	"github.com/Licoy/sshyard/internal/backup/folder"
	"github.com/Licoy/sshyard/internal/backup/gist"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vault"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// A backup is everything SSHYard keeps (the hosts and what they use, the
// preferences, the trusted host keys and, unless left out, their secrets
// from the encrypted local store) in one compressed archive (package
// backup) sealed with a password (package vault), to keep, here or in the
// cloud, or to move to another device. Restoring merges it in.

// backupPayload is what a backup file holds once opened. A backup from
// before archives is this, as JSON.
type backupPayload struct {
	Assets     assets.Snapshot   `json:"assets"`
	Settings   settings.Settings `json:"settings"`
	KnownHosts string            `json:"knownHosts,omitempty"`
	// Secrets are the entries above, by key; never the master key or MCP token.
	Secrets map[string][]byte `json:"secrets,omitempty"`
}

// The files of a backup's archive.
const (
	backupAssets   = "assets.json"
	backupSettings = "settings.json"
	backupKnown    = "known_hosts"
	backupSecrets  = "secrets.json"
)

// packBackup is the archive of p, made on this device.
func (a *App) packBackup(p backupPayload) ([]byte, error) {
	p.Settings.Background = settings.DefaultBackground()
	files := map[string][]byte{}
	var err error
	if files[backupAssets], err = json.Marshal(p.Assets); err != nil {
		return nil, err
	}
	if files[backupSettings], err = json.Marshal(p.Settings); err != nil {
		return nil, err
	}
	if p.KnownHosts != "" {
		files[backupKnown] = []byte(p.KnownHosts)
	}
	if p.Secrets != nil {
		if files[backupSecrets], err = json.Marshal(p.Secrets); err != nil {
			return nil, err
		}
	}
	m := backup.Manifest{App: a.p.Version, Created: time.Now().UTC().Truncate(time.Second), Device: a.deviceID(), Secrets: p.Secrets != nil}
	return backup.Pack(m, files)
}

// unpackBackup is what an opened backup holds: its archive, or the JSON of
// a backup from before archives.
func unpackBackup(plain []byte) (backupPayload, error) {
	p := backupPayload{Settings: settings.Default()}
	if !backup.IsArchive(plain) {
		if json.Unmarshal(plain, &p) != nil {
			return p, vault.ErrCorrupt
		}
		return p, nil
	}
	_, files, err := backup.Unpack(plain)
	if err != nil {
		return p, err
	}
	b, ok := files[backupAssets]
	if !ok || json.Unmarshal(b, &p.Assets) != nil {
		return p, vault.ErrCorrupt
	}
	if b, ok := files[backupSettings]; ok && json.Unmarshal(b, &p.Settings) != nil {
		return p, vault.ErrCorrupt
	}
	p.KnownHosts = string(files[backupKnown])
	if b, ok := files[backupSecrets]; ok && json.Unmarshal(b, &p.Secrets) != nil {
		return p, vault.ErrCorrupt
	}
	return p, nil
}

// backupExt is the extension of a backup file.
const backupExt = backup.Ext

// minBackupPassword is the shortest backup password taken, in characters.
const minBackupPassword = 8

// maxBackupFile bounds what a restore reads.
const maxBackupFile = backup.MaxSize

// backupForm is the backup sheet: exporting, with the default backup
// password or a new one typed twice, or restoring the file data with its
// password. The password lives here only while the sheet is open.
type backupForm struct {
	restore bool
	// source is where an export's password comes from, pwDefault or
	// pwCustom
	source            int
	password, confirm string
	// secrets puts the local store's secrets in an export; on by default
	secrets bool
	// name, data and created are the backup file being restored
	name    string
	data    []byte
	created time.Time
	// busy is set while the password is derived and the file sealed or
	// opened, off the UI thread; cancel stops a restore's opening before
	// its next derivation, when the sheet closes
	busy   bool
	cancel context.CancelFunc
	err    string
}

// backupSecretKeys are the allowed secret keys of the entries of snap and of
// the AI providers.
func backupSecretKeys(snap assets.Snapshot, provs []settings.AIProvider) []string {
	var out []string
	for _, h := range snap.Hosts {
		if h.Auth == assets.AuthPassword {
			out = append(out, keychain.Host(h.ID, keychain.Password))
		}
		if h.Auth == assets.AuthKey {
			out = append(out, keychain.Host(h.ID, keychain.PrivateKey), keychain.Host(h.ID, keychain.Passphrase))
		}
		if h.Startup {
			out = append(out, keychain.HostStartup(h.ID))
		}
	}
	for _, c := range snap.Credentials {
		for _, k := range keychain.Kinds {
			out = append(out, keychain.Cred(c.ID, k))
		}
	}
	for _, p := range snap.Proxies {
		out = append(out, keychain.ProxyPassword(p.ID))
	}
	for _, f := range snap.Forwards {
		out = append(out, keychain.ForwardPassword(f.ID))
	}
	for _, n := range snap.Snippets {
		out = append(out, keychain.SnippetContent(n.ID))
	}
	for _, p := range provs {
		out = append(out, keychain.AIKey(p.ID))
	}
	return out
}

// collectBackup gathers what a backup holds. With secrets it reads them
// from the encrypted local store, and fails when it cannot be read: a
// backup missing secrets it seems to have is worse than none.
func (a *App) collectBackup(secrets bool) (backupPayload, error) {
	p := backupPayload{Assets: a.store.Snapshot(), Settings: a.settings}
	p.Settings.BackupTargets, p.Settings.DefaultBackupPassword = nil, false
	p.Settings.Background = settings.DefaultBackground()
	// What starts a program on this computer is not taken by a restore,
	// so it is not put in a backup either.
	p.Settings.AIACPAgents, p.Settings.AIACP, p.Settings.AIDefaultAgent = nil, nil, ""
	if b, err := a.known.Contents(); err == nil {
		p.KnownHosts = string(b)
	}
	if !secrets {
		return p, nil
	}
	p.Secrets = map[string][]byte{}
	for _, key := range backupSecretKeys(p.Assets, p.Settings.AIProviders) {
		v, err := a.kc.Get(key)
		switch {
		case errors.Is(err, keychain.ErrNotFound):
		case err != nil:
			return backupPayload{}, err
		default:
			p.Secrets[key] = v
		}
	}
	return p, nil
}

// errBadBackup reports a backup whose entries can not be taken.
var errBadBackup = errors.New("app: invalid backup entries")

// restoreBackup merges p into this device: one batch of secrets is encrypted
// under this device's master key, then entries are saved, with compensating
// rollback on failure (not crash atomicity across files); host keys and preferences
// follow, whose
// failures are told but undo nothing, except that AI keys dropped for a
// moved endpoint are put back when the preferences that move it fail.
// Only the secrets of the backup's own entries are taken.
func (a *App) restoreBackup(m locales.Messages, p backupPayload) error {
	snap := p.Assets
	for _, id := range backupIDs(snap, p.Settings.AIProviders) {
		if !assets.ValidID(id) {
			return errBadBackup
		}
	}
	for _, n := range snap.Snippets {
		if v, ok := p.Secrets[keychain.SnippetContent(n.ID)]; ok {
			if err := assets.ValidateSnippetContent(string(v)); err != nil {
				return errBadBackup
			}
		}
	}
	for _, h := range snap.Hosts {
		if v, ok := p.Secrets[keychain.HostStartup(h.ID)]; ok && h.Startup {
			if err := assets.ValidateStartup(string(v)); err != nil {
				return errBadBackup
			}
		}
	}
	tx := &secretTx{kc: a.kc}
	var changes []keychain.Change
	for _, key := range backupSecretKeys(snap, p.Settings.AIProviders) {
		if v := p.Secrets[key]; len(v) > 0 {
			changes = append(changes, keychain.Change{Key: key, Value: v})
		}
	}
	// Host secrets are replaced, including omitted secrets and obsolete auth kinds.
	for _, h := range snap.Hosts {
		previous, exists := a.store.Get(h.ID)
		if !exists || (previous.Auth != assets.AuthPassword && previous.Auth != assets.AuthKey) {
			continue
		}
		allowed := backupSecretKeys(assets.Snapshot{Hosts: []assets.Host{h}}, nil)
		for _, kind := range keychain.Kinds {
			key := keychain.Host(h.ID, kind)
			if !slices.Contains(allowed, key) || len(p.Secrets[key]) == 0 {
				changes = append(changes, keychain.Change{Key: key, Delete: true})
			}
		}
	}
	// A host's startup commands are replaced too: a host restored without
	// them types none of those it had here.
	for _, h := range snap.Hosts {
		key := keychain.HostStartup(h.ID)
		if previous, exists := a.store.Get(h.ID); exists && previous.Startup && (!h.Startup || len(p.Secrets[key]) == 0) {
			changes = append(changes, keychain.Change{Key: key, Delete: true})
		}
	}
	// Restoring metadata without its body clears any existing same-ID body;
	// the entry remains unavailable until the user supplies its command.
	for _, n := range snap.Snippets {
		key := keychain.SnippetContent(n.ID)
		_, exists := a.store.Snippet(n.ID)
		if _, ok := p.Secrets[key]; !ok && exists {
			changes = append(changes, keychain.Change{Key: key, Delete: true})
		}
	}
	// An AI provider moved to another endpoint without a key of its own
	// loses the key it has here, so that key never reaches the new one.
	var dropped []keychain.Change
	defer func() {
		for _, c := range dropped {
			clear(c.Value)
		}
	}()
	for _, key := range a.movedAIKeys(p.Settings.AIProviders, p.Secrets) {
		old, err := a.kc.Get(key)
		if errors.Is(err, keychain.ErrNotFound) {
			continue
		}
		if err != nil {
			return errors.New(m.ErrKeychainSave(secretStoreErrorText(m, err)))
		}
		dropped = append(dropped, keychain.Change{Key: key, Value: old})
		changes = append(changes, keychain.Change{Key: key, Delete: true})
	}
	if err := tx.setBatch(changes); err != nil {
		msg := m.ErrKeychainSave(secretStoreErrorText(m, err))
		if rerr := tx.rollback(); rerr != nil {
			msg = m.ErrKeychainRestore(msg, secretStoreErrorText(m, rerr))
		}
		return errors.New(msg)
	}
	if err := a.store.Restore(snap); err != nil {
		var fe *assets.FieldError
		if errors.As(err, &fe) || errors.Is(err, assets.ErrJumpLoop) {
			err = errBadBackup
		}
		if rerr := tx.rollback(); rerr != nil {
			return errors.New(m.ErrKeychainRestore(backupErrText(m, err), secretStoreErrorText(m, rerr)))
		}
		return err
	}
	for _, h := range snap.Hosts {
		a.forgetHostSecrets(h.ID)
	}
	if p.KnownHosts != "" {
		if _, err := a.known.Merge([]byte(p.KnownHosts)); err != nil {
			a.toastError(m.ErrGeneric(err.Error()))
		}
	}
	if err := a.ApplySettings(mergeSettings(a.settings, p.Settings)); err != nil {
		// The providers keep the endpoints their keys belong to.
		if len(dropped) > 0 {
			err = errors.Join(err, a.kc.Apply(dropped))
		}
		a.toastError(m.ErrGeneric(err.Error()))
	}
	return nil
}

// movedAIKeys are the keys of this device's AI providers that in moves to
// another endpoint without bringing a key of its own.
func (a *App) movedAIKeys(in []settings.AIProvider, secrets map[string][]byte) []string {
	var out []string
	for _, p := range in {
		key := keychain.AIKey(p.ID)
		if len(secrets[key]) > 0 || slices.Contains(out, key) {
			continue
		}
		if cur := a.providerByID(p.ID); cur != nil && !sameAIEndpoint(cur.BaseURL, p.BaseURL) {
			out = append(out, key)
		}
	}
	return out
}

// sameAIEndpoint reports whether requests to the base URLs a and b reach
// the same service: the same scheme, host and port, whatever the path. A
// URL the AI client would not dial differs from any other, unless both are
// written alike.
func sameAIEndpoint(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	ea, oka := aiEndpoint(a)
	eb, okb := aiEndpoint(b)
	return oka && okb && ea == eb
}

// aiEndpoint is the scheme, host and port the AI client sends requests to
// for a base URL, false when it would not dial it.
func aiEndpoint(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return "", false
	}
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	switch {
	case scheme != "http" && scheme != "https":
		return "", false
	case port == "" && scheme == "http":
		port = "80"
	case port == "":
		port = "443"
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), true
}

// backupIDs are the IDs the backup's secrets are stored under.
func backupIDs(snap assets.Snapshot, provs []settings.AIProvider) []string {
	var out []string
	for _, h := range snap.Hosts {
		out = append(out, h.ID)
	}
	for _, c := range snap.Credentials {
		out = append(out, c.ID)
	}
	for _, p := range snap.Proxies {
		out = append(out, p.ID)
	}
	for _, f := range snap.Forwards {
		out = append(out, f.ID)
	}
	for _, n := range snap.Snippets {
		out = append(out, n.ID)
	}
	for _, p := range provs {
		out = append(out, p.ID)
	}
	return out
}

// mergeSettings are the preferences of a backup, in, over those of this
// device, cur: what belongs to this device stays (the window's layout and
// materials, the layout of its files panes and editor windows, the fonts
// installed here, the MCP endpoint, the places it backs up to and its
// default backup password, its download and session log folders, the
// external ACP agents it starts and its default agent), AI providers are
// merged by ID and the hosts that confirmed AI use are joined.
func mergeSettings(cur, in settings.Settings) settings.Settings {
	out := in
	out.Background = cur.Background
	out.RememberLayout = cur.RememberLayout
	out.SidebarWidth, out.SidebarHidden = cur.SidebarWidth, cur.SidebarHidden
	out.ChromeMaterial, out.ContentMaterial = cur.ChromeMaterial, cur.ContentMaterial
	out.TrayClick, out.TrayMaterial, out.TrayHeight = cur.TrayClick, cur.TrayMaterial, cur.TrayHeight
	out.UIFont, out.TerminalFont = cur.UIFont, cur.TerminalFont
	out.FilesDual, out.FilesSplit = cur.FilesDual, cur.FilesSplit
	out.DownloadDir = cur.DownloadDir
	out.EditorTreeWidth, out.EditorTreeHidden = cur.EditorTreeWidth, cur.EditorTreeHidden
	// A backup never turns the MCP endpoint on, nor lets it use the keys
	// of this computer: that is asked here.
	out.MCPEnabled, out.MCPPort, out.LocalKeysMCP = cur.MCPEnabled, cur.MCPPort, cur.LocalKeysMCP
	// Nor does it choose where this device backs up to, nor where it
	// keeps its session logs.
	out.BackupTargets, out.DefaultBackupPassword = slices.Clone(cur.BackupTargets), cur.DefaultBackupPassword
	out.SessionLog = cur.SessionLog
	// Nor does it add or change a program this computer starts: an ACP
	// agent's executable and arguments, and the default agent that may
	// name one, stay as they are here. A legacy single agent in the
	// backup is dropped too, or Normalize would take it in.
	out.AIACPAgents, out.AIACP, out.AIDefaultAgent = slices.Clone(cur.AIACPAgents), cur.AIACP, cur.AIDefaultAgent
	out.AIProviders = slices.Clone(cur.AIProviders)
	for _, p := range in.AIProviders {
		if i := slices.IndexFunc(out.AIProviders, func(o settings.AIProvider) bool { return o.ID == p.ID }); i >= 0 {
			out.AIProviders[i] = p
		} else {
			out.AIProviders = append(out.AIProviders, p)
		}
	}
	out.AIConsentHosts = slices.Clone(cur.AIConsentHosts)
	for _, id := range in.AIConsentHosts {
		if !slices.Contains(out.AIConsentHosts, id) {
			out.AIConsentHosts = append(out.AIConsentHosts, id)
		}
	}
	return out
}

// backupErrText says why a backup could not be made, put in a place,
// fetched from one, opened or restored.
func backupErrText(m locales.Messages, err error) string {
	switch {
	case errors.Is(err, vault.ErrFormat):
		return m.BackupNotBackup()
	case errors.Is(err, vault.ErrVersion), errors.Is(err, backup.ErrVersion):
		return m.BackupNewer()
	case errors.Is(err, vault.ErrPassword):
		return m.BackupWrongPassword()
	case errors.Is(err, vault.ErrTooCostly):
		return m.BackupTooCostly()
	case errors.Is(err, vault.ErrCorrupt), errors.Is(err, errBadBackup), errors.Is(err, backup.ErrArchive):
		return m.BackupDamaged()
	case errors.Is(err, backup.ErrTooLarge):
		return m.ErrCloudTooLarge()
	case errors.Is(err, backup.ErrAuth), errors.Is(err, gist.ErrToken):
		return m.ErrCloudAuth()
	case errors.Is(err, folder.ErrPath):
		return m.ErrTargetFolder()
	case errors.Is(err, folder.ErrDenied):
		return m.ErrFolderDenied()
	case errors.Is(err, backup.ErrNotFound):
		return m.ErrCloudNotFound()
	case errors.Is(err, errBackupKey):
		return m.ErrBackupKeyMissing()
	case errors.Is(err, errDefaultBackupKey):
		return m.ErrDefaultBackupKeyMissing()
	case errors.Is(err, errBackupProxy):
		return m.ErrBackupProxyMissing()
	}
	return m.ErrGeneric(secretStoreErrorText(m, err))
}

// toastError shows msg as an error toast at the next frame.
func (a *App) toastError(msg string) {
	a.toasts = append(a.toasts, func(locales.Messages) ui.Toast { return ui.Toast{Title: msg, Type: "error"} })
}

func (a *App) openExport() {
	f := &backupForm{secrets: true, source: pwCustom}
	if a.settings.DefaultBackupPassword {
		f.source = pwDefault
	}
	a.backup, a.backingUp = f, true
}

func (a *App) closeBackup() {
	if f := a.backup; f != nil && f.cancel != nil {
		f.cancel()
	}
	a.backup, a.backingUp = nil, false
}

// ExportBackup checks the sheet's password and gathers the backup, then,
// off the UI thread, asks where to save it and seals it there.
func (a *App) ExportBackup(m locales.Messages) bool {
	f := a.backup
	if f == nil || f.restore || f.busy || a.p.SaveFile == nil {
		return false
	}
	f.err = ""
	withDefault := f.source == pwDefault && a.settings.DefaultBackupPassword
	switch {
	case withDefault:
	case len([]rune(f.password)) < minBackupPassword:
		f.err = m.BackupPasswordShort(strconv.Itoa(minBackupPassword))
		return false
	case f.password != f.confirm:
		f.err = m.BackupPasswordMismatch()
		return false
	}
	// The default backup password's key is in the store too.
	if (f.secrets || withDefault) && !a.unlocked() {
		draft := a.backup
		a.withSecrets(func() {
			if a.backup == draft {
				a.ExportBackup(m)
			}
		})
		return false
	}
	p, err := a.collectBackup(f.secrets)
	var data []byte
	if err == nil {
		data, err = a.packBackup(p)
	}
	if err != nil {
		f.err = m.BackupExportFailed(secretStoreErrorText(m, err))
		return false
	}
	var key vault.Key
	if withDefault {
		if key, err = a.defaultBackupKey(); err != nil {
			f.err = backupErrText(m, err)
			return false
		}
	}
	pw, cost, title := []byte(f.password), a.kdf, m.ExportBackup()
	def := filepath.Join(a.homeDir(), "SSHYard-"+time.Now().Format("2006-01-02")+backupExt)
	f.busy = true
	go func() {
		defer key.Clear()
		path, err := a.p.SaveFile(title, def)
		if path == "" && err == nil {
			a.post(func() { f.busy = false })
			return
		}
		if err == nil {
			var sealed []byte
			if withDefault {
				sealed, err = key.Seal(data)
			} else {
				sealed, err = vault.Seal(data, pw, cost)
			}
			if err == nil {
				err = settings.WriteFileAtomic(path, sealed, 0o600)
			}
		}
		a.post(func() { a.exportDone(m, f, path, err) })
	}()
	return true
}

func (a *App) exportDone(m locales.Messages, f *backupForm, path string, err error) {
	f.busy = false
	if err != nil {
		if a.backup == f {
			f.err = m.BackupExportFailed(err.Error())
		} else {
			a.toastError(m.BackupExportFailed(err.Error()))
		}
		return
	}
	if a.backup == f {
		a.closeBackup()
	}
	name := filepath.Base(path)
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.BackupSaved(name), Type: "success"} })
}

// pickBackup reads a backup file through the system's open dialog, off
// the UI thread.
func (a *App) pickBackup(m locales.Messages) {
	if a.p.PickFile == nil {
		return
	}
	title := m.RestoreBackup()
	go func() {
		path, err := a.p.PickFile(title)
		if path == "" && err == nil {
			return
		}
		var data []byte
		if err == nil {
			data, err = readBackupFile(path)
		}
		a.post(func() { a.openRestore(m, path, data, err) })
	}()
}

func readBackupFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBackupFile+1))
	if err == nil && len(b) > maxBackupFile {
		err = vault.ErrFormat
	}
	return b, err
}

// openRestore opens the restore sheet on the backup file read from path,
// or says why it can not be restored.
func (a *App) openRestore(m locales.Messages, path string, data []byte, err error) {
	if err == nil {
		err = vault.Check(data)
	}
	if err != nil {
		a.toastError(m.BackupOpenFailed(filepath.Base(path), backupErrText(m, err)))
		return
	}
	created, _ := vault.Created(data)
	a.backup, a.backingUp = &backupForm{restore: true, name: filepath.Base(path), data: data, created: created}, true
}

// RestoreBackup opens the sheet's backup with its password, off the UI
// thread, then merges it in. A backup whose password derivation costs
// more than usual is opened only once the user agrees.
func (a *App) RestoreBackup(m locales.Messages) bool {
	return a.openBackup(m, false)
}

// openBackup is RestoreBackup, with costly once the user agreed to the
// cost. Closing the sheet cancels it: no derivation starts after that,
// though one under way runs to its end, as Argon2 can not be interrupted.
func (a *App) openBackup(m locales.Messages, costly bool) bool {
	f := a.backup
	if f == nil || !f.restore || f.busy {
		return false
	}
	if f.password == "" {
		f.err = m.RequiredField(m.BackupPassword())
		return false
	}
	f.err, f.busy = "", true
	data, pw := f.data, []byte(f.password)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go func() {
		defer cancel()
		p := backupPayload{Settings: settings.Default()}
		plain, err := openVault(ctx, data, pw, costly)
		if err == nil {
			p, err = unpackBackup(plain)
		}
		a.post(func() { a.restoreDone(m, f, p, err) })
	}()
	return true
}

func (a *App) backupNeedsSecrets(p backupPayload) bool {
	for _, key := range backupSecretKeys(p.Assets, p.Settings.AIProviders) {
		if len(p.Secrets[key]) > 0 {
			return true
		}
	}
	for _, h := range p.Assets.Hosts {
		if previous, ok := a.store.Get(h.ID); ok && (previous.Auth == assets.AuthPassword || previous.Auth == assets.AuthKey) {
			return true
		}
	}
	for _, n := range p.Assets.Snippets {
		if _, ok := a.store.Snippet(n.ID); ok {
			return true
		}
	}
	// Dropping the key of a provider that moves needs the store open.
	return len(a.movedAIKeys(p.Settings.AIProviders, p.Secrets)) > 0
}

func (a *App) restoreDone(m locales.Messages, f *backupForm, p backupPayload, err error) {
	// A sheet closed while the backup opened restores nothing.
	if a.backup != f {
		return
	}
	f.busy = false
	var ce *vault.CostError
	if errors.As(err, &ce) {
		a.askCost(&costAsk{backup: true, memory: ce.Cost.Memory, passes: ce.Cost.Passes, run: func() {
			a.closeCost()
			if a.backup == f {
				a.openBackup(m, true)
			}
		}})
		return
	}
	if err == nil && !a.unlocked() && a.backupNeedsSecrets(p) {
		a.withSecrets(func() { a.restoreDone(m, f, p, nil) })
		return
	}
	if err == nil {
		err = a.restoreBackup(m, p)
	}
	if err != nil {
		f.err = backupErrText(m, err)
		return
	}
	a.closeBackup()
	hosts, creds := len(p.Assets.Hosts), len(p.Assets.Credentials)
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
		return ui.Toast{Title: m.BackupRestored(hosts, creds), Type: "success"}
	})
}

// backupSection is the settings' backup page.
func (a *App) backupSection(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	a.defaultPasswordSection(c, m, tok)
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		g.row(m.ExportBackup(), m.ExportBackupNote(), func() {
			if pill(c, m.ExportEllipsis(), false, false, a.p.SaveFile == nil) {
				a.openExport()
			}
		})
		g.row(m.RestoreBackup(), m.RestoreBackupNote(), func() {
			if pill(c, m.KeyChooseFile(), false, false, a.p.PickFile == nil) {
				a.pickBackup(m)
			}
		})
	})
	a.cloudSection(c, m, tok)
}

// backupView is the export or restore sheet.
func (a *App) backupView(c *ui.Context, m locales.Messages) {
	f := a.backup
	if f == nil {
		return
	}
	tok := currentTokens(c)
	sheet(stickyModal(c, &a.backingUp, func() {
		ui.Column(c).Gap(18).Width(460).Children(func() {
			title, sub := m.ExportBackup(), ""
			if f.restore {
				title, sub = m.RestoreBackup(), f.name
			}
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, icons.Backup, tok.TintAt(6), 36)
				ui.Column(c).Gap(1).Shrink(1).Children(func() {
					sheetTitle(c, title)
					if sub != "" {
						ui.Text(c, sub).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
					}
				})
			})
			choose := !f.restore && a.settings.DefaultBackupPassword
			typed := !choose || f.source == pwCustom
			ui.Form(c, func() {
				if choose {
					passwordSource(c, m, "bk:source", &f.source)
				}
				if !typed {
					return
				}
				ui.Field(c, m.BackupPassword(), func() {
					in := formTextInput(c.Key("bk:pw"), &f.password).Password().AutoFocus().Disabled(f.busy)
					a.backupHold.apply(c, f, in)
					if in.Submitted() && f.restore {
						a.RestoreBackup(m)
					}
				})
				if !f.restore {
					ui.Field(c, m.ConfirmPassword(), func() {
						if formTextInput(c.Key("bk:confirm"), &f.confirm).Password().Disabled(f.busy).Submitted() {
							a.ExportBackup(m)
						}
					})
				}
			})
			if !typed {
				ui.Text(c, m.DefaultBackupPasswordUsed()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			}
			if f.restore {
				if !f.created.IsZero() {
					ui.Text(c, m.BackupMadeOn(f.created.Local().Format("2006-01-02 15:04"))).FontSize(theme.Footnote).TextColor(tok.TextMuted)
				}
				ui.Text(c, m.BackupRestoreNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			} else {
				ui.Row(c).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
					ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
						ui.Text(c, m.BackupIncludeSecrets()).FontSize(theme.Body).FontWeight(500)
						ui.Text(c, m.BackupIncludeSecretsNote()).FontSize(theme.Caption).TextColor(tok.TextMuted)
					})
					formDisabled(formSwitch(c.Key("bk:secrets"), &f.secrets).Label(m.BackupIncludeSecrets()), f.busy)
				})
				ui.Text(c, m.BackupExportWarning()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			}
			if f.err != "" {
				ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.backingUp = false
				}
				switch {
				case f.restore && f.busy:
					pill(c, m.BackupRestoring(), true, false, true)
				case f.restore:
					if pill(c, m.Restore(), true, false, false) {
						a.RestoreBackup(m)
					}
				case f.busy:
					pill(c, m.BackupExporting(), true, false, true)
				default:
					if pill(c, m.ExportEllipsis(), true, false, false) {
						a.ExportBackup(m)
					}
				}
			})
		})
	}), tok)
	if !a.backingUp && a.backup == f {
		a.closeBackup()
	}
}
