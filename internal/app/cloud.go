package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/backup"
	"github.com/Licoy/sshyard/internal/backup/folder"
	"github.com/Licoy/sshyard/internal/backup/gist"
	"github.com/Licoy/sshyard/internal/backup/s3"
	"github.com/Licoy/sshyard/internal/backup/webdav"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vault"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// Cloud backup: the places this device backs up to (settings.BackupTarget),
// a folder on a WebDAV server, an S3 bucket, a GitHub gist or a folder of
// this computer that a sync client such as iCloud Drive carries to the
// user's other devices. A backup goes
// there as a backup file does to disk, sealed here first with the key
// derived from the place's backup password, kept in the encrypted local
// store, so that neither a backup by hand nor the automatic ones, when
// turned on, ask for it. Each backup is a new file named for when and by
// which device it was made, so devices never overwrite each other; after
// one, this device's oldest past the place's Keep are deleted.

// cloudFile is what this device knows of its places, in DataDir.
const cloudFile = "backups.json"

// cloudState is what this device knows of its places beyond their
// settings: the name its backups carry and how each place's went. Nothing
// in it is secret.
type cloudState struct {
	Device  string                  `json:"device"`
	Targets map[string]*targetState `json:"targets,omitempty"`
}

// targetState is how one place's backups went.
type targetState struct {
	// Last is when the last backup was put there, Object its name.
	Last   time.Time `json:"last,omitzero"`
	Object string    `json:"object,omitempty"`
	// Checked is when an automatic backup last found nothing changed.
	Checked time.Time `json:"checked,omitzero"`
	// Digest is an HMAC, under the place's key, of what the last backup
	// held, so that an unchanged automatic one is skipped.
	Digest string `json:"digest,omitempty"`
	// busy is set while a backup goes there, err is why the last failed
	// and retry when a failed automatic one is tried again; none is kept.
	busy  bool
	err   error
	retry time.Time
}

// The automatic backups are checked cloudEvery once the store is
// unlocked, the first time after cloudFirst; a failed one is tried again
// after cloudRetry.
const (
	cloudFirst = time.Minute
	cloudEvery = 10 * time.Minute
	cloudRetry = 30 * time.Minute
)

var (
	// errBackupKey reports a place whose key is not in the store.
	errBackupKey = errors.New("app: backup key missing")
	// errBackupProxy reports a place whose proxy was deleted: it is not
	// reached directly instead.
	errBackupProxy = errors.New("app: backup location proxy missing")
)

// cloudNow is what this device knows of its places, read once.
func (a *App) cloudNow() *cloudState {
	if a.cloud != nil {
		return a.cloud
	}
	s := &cloudState{}
	if a.p.DataDir != "" {
		if b, err := os.ReadFile(filepath.Join(a.p.DataDir, cloudFile)); err == nil {
			json.Unmarshal(b, s)
		}
	}
	if s.Targets == nil {
		s.Targets = map[string]*targetState{}
	}
	a.cloud = s
	if !backup.ValidDevice(s.Device) {
		s.Device = strings.ToLower(rand.Text()[:8])
		a.saveCloud()
	}
	return s
}

// of is how the place id's backups went.
func (s *cloudState) of(id string) *targetState {
	st := s.Targets[id]
	if st == nil {
		st = &targetState{}
		s.Targets[id] = st
	}
	return st
}

// deviceID is this device's name in its backups' file names.
func (a *App) deviceID() string { return a.cloudNow().Device }

// saveCloud keeps what this device knows of its places, forgetting places
// since deleted. A failure is logged: it costs at most an early backup.
func (a *App) saveCloud() {
	if a.p.DataDir == "" || a.cloud == nil {
		return
	}
	for id := range a.cloud.Targets {
		if _, ok := a.settings.BackupTarget(id); !ok {
			delete(a.cloud.Targets, id)
		}
	}
	b, err := json.Marshal(a.cloud)
	if err == nil {
		err = settings.WriteFileAtomic(filepath.Join(a.p.DataDir, cloudFile), b, 0o600)
	}
	if err != nil {
		log.Printf("sshyard: saving %s: %v", cloudFile, err)
	}
}

// remote is the place t, reached with secret through its proxy. bound is
// given, off the UI thread, the gist a Gist place finds or makes.
func (a *App) remote(t settings.BackupTarget, secret string, bound func(id string)) (backup.Remote, error) {
	if t.Kind == settings.BackupFolder {
		return &folder.Folder{Dir: t.Dir}, nil
	}
	dial := a.p.Dial
	if t.ProxyID != "" {
		px, ok := a.store.Proxy(t.ProxyID)
		if !ok {
			return nil, errBackupProxy
		}
		dial = a.proxyDialer(px).DialContext
	}
	hc := backup.Client(dial)
	switch t.Kind {
	case settings.BackupS3:
		return &s3.Bucket{Endpoint: t.URL, Region: t.Region, Bucket: t.Bucket, Prefix: t.Dir, PathStyle: t.PathStyle, AccessKey: t.User, SecretKey: secret, HTTP: hc}, nil
	case settings.BackupGist:
		return &gist.Gist{Token: secret, ID: t.GistID, Bound: bound, API: a.gistAPI, HTTP: hc}, nil
	}
	return &webdav.Folder{URL: t.URL, Dir: t.Dir, User: t.User, Password: secret, HTTP: hc}, nil
}

// targetRemote is t, reached with its secret from the store. A gist it
// finds or makes is kept in its settings.
func (a *App) targetRemote(t settings.BackupTarget) (backup.Remote, error) {
	secret, err := a.kc.Get(keychain.BackupSecret(t.ID))
	if err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return nil, err
	}
	defer clear(secret)
	return a.remote(t, string(secret), func(gid string) { a.post(func() { a.bindGist(t.ID, gid) }) })
}

// bindGist keeps gid as the gist of the place id, unless it has one.
func (a *App) bindGist(id, gid string) {
	s := a.settings
	s.BackupTargets = slices.Clone(s.BackupTargets)
	for i, t := range s.BackupTargets {
		if t.ID == id && t.GistID == "" {
			s.BackupTargets[i].GistID = gid
			if err := a.ApplySettings(s); err != nil {
				a.toastError(err.Error())
			}
			return
		}
	}
}

// targetKey is the key t's backups are sealed with: its own, or the
// default backup password's.
func (a *App) targetKey(t settings.BackupTarget) (vault.Key, error) {
	if t.DefaultPassword {
		return a.defaultBackupKey()
	}
	return a.readBackupKey(keychain.BackupKey(t.ID), errBackupKey)
}

// putBackup puts sealed in r as name, then deletes this device's backups
// there past keep. A clean-up that fails leaves older backups behind; it
// does not fail the backup.
func putBackup(ctx context.Context, r backup.Remote, name string, sealed []byte, device string, keep int) error {
	if err := r.Put(ctx, name, sealed); err != nil {
		return err
	}
	if keep == 0 {
		return nil
	}
	objs, err := r.List(ctx)
	if err != nil {
		return nil
	}
	for _, o := range backup.Prune(objs, device, keep) {
		if r.Delete(ctx, o.Name) != nil {
			break
		}
	}
	return nil
}

// backupDone records how the backup name to the place id went; digest is
// the HMAC of what it held.
func (a *App) backupDone(id, name, digest string, err error) {
	st := a.cloudNow().of(id)
	st.busy = false
	if err != nil {
		st.err, st.retry = err, time.Now().Add(cloudRetry)
		return
	}
	st.err, st.retry = nil, time.Time{}
	st.Last, st.Object, st.Digest = time.Now(), name, digest
	a.saveCloud()
}

// cloudToast tells how a backup to the place name went.
func (a *App) cloudToast(name string, err error) {
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
		if err != nil {
			return ui.Toast{Title: m.BackupUploadFailed(name, backupErrText(m, err)), Type: "error"}
		}
		return ui.Toast{Title: m.BackupUploaded(name), Type: "success"}
	})
}

// BackupNow backs up to the place id with its key.
func (a *App) BackupNow(id string) {
	t, ok := a.settings.BackupTarget(id)
	if !ok || a.cloudNow().of(id).busy {
		return
	}
	if !a.unlocked() {
		a.withSecrets(func() { a.BackupNow(id) })
		return
	}
	a.backupWithKey(t, false)
}

// backupWithKey backs up to t with its key, off the UI thread once
// gathered. An automatic one (auto) is skipped when nothing changed since
// the last, and tells only through the settings.
func (a *App) backupWithKey(t settings.BackupTarget, auto bool) {
	st := a.cloudNow().of(t.ID)
	fail := func(err error) {
		a.backupDone(t.ID, "", "", err)
		if !auto {
			a.cloudToast(t.Name, err)
		}
	}
	key, err := a.targetKey(t)
	if err != nil {
		fail(err)
		return
	}
	p, err := a.collectBackup(!t.OmitSecrets)
	var plain []byte
	if err == nil {
		plain, err = json.Marshal(p)
	}
	if err != nil {
		key.Clear()
		fail(err)
		return
	}
	mac := hmac.New(sha256.New, key.KEK)
	mac.Write(plain)
	digest := hex.EncodeToString(mac.Sum(nil))
	if auto && digest == st.Digest {
		key.Clear()
		st.Checked = time.Now()
		a.saveCloud()
		return
	}
	plain, err = a.packBackup(p)
	var r backup.Remote
	if err == nil {
		r, err = a.targetRemote(t)
	}
	if err != nil {
		key.Clear()
		fail(err)
		return
	}
	device := a.deviceID()
	name, ctx := backup.Name(time.Now(), device), a.cloudCtx
	st.busy, st.err = true, nil
	go func() {
		sealed, err := key.Seal(plain)
		key.Clear()
		if err == nil {
			err = putBackup(ctx, r, name, sealed, device, t.Keep)
		}
		a.post(func() {
			a.backupDone(t.ID, name, digest, err)
			if !auto {
				a.cloudToast(t.Name, err)
			}
		})
	}()
}

// startAutoBackup checks the automatic backups until the app closes.
func (a *App) startAutoBackup() {
	ctx := a.cloudCtx
	go func() {
		tm := time.NewTimer(cloudFirst)
		defer tm.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tm.C:
				a.post(func() { a.autoBackup(time.Now()) })
				tm.Reset(cloudEvery)
			}
		}
	}()
}

// autoBackup starts the automatic backups due at now. They wait while the
// store is locked.
func (a *App) autoBackup(now time.Time) {
	if !a.unlocked() {
		return
	}
	for _, t := range a.settings.BackupTargets {
		if t.AutoHours == 0 {
			continue
		}
		st := a.cloudNow().of(t.ID)
		last := st.Last
		if st.Checked.After(last) {
			last = st.Checked
		}
		if st.busy || now.Before(st.retry) || now.Before(last.Add(time.Duration(t.AutoHours)*time.Hour)) {
			continue
		}
		a.backupWithKey(t, true)
	}
}

// DeleteBackupTarget forgets the place id with its secret and key; the
// backups in it stay.
func (a *App) DeleteBackupTarget(id string) error {
	if _, ok := a.settings.BackupTarget(id); !ok {
		return nil
	}
	if err := a.kc.Apply([]keychain.Change{{Key: keychain.BackupSecret(id), Delete: true}, {Key: keychain.BackupKey(id), Delete: true}}); err != nil {
		return &keychainDeleteError{err}
	}
	s := a.settings
	s.BackupTargets = slices.DeleteFunc(slices.Clone(s.BackupTargets), func(t settings.BackupTarget) bool { return t.ID == id })
	if err := a.ApplySettings(s); err != nil {
		return err
	}
	a.saveCloud()
	return nil
}

// targetKinds are the kinds of place, in the editor's order.
var targetKinds = []string{settings.BackupWebDAV, settings.BackupS3, settings.BackupGist, settings.BackupFolder}

// knownPlaces are the sync clients' folders on this computer, looked for
// once or again when fresh.
func (a *App) knownPlaces(fresh bool) []folder.Place {
	if fresh || !a.placesOK {
		a.places, a.placesOK = folder.Known(runtime.GOOS, a.localHome(), os.Getenv), true
	}
	return a.places
}

// targetOption is a choice of the editor's type select: a kind of place
// and, for a folder, the sync client's folder it starts in.
type targetOption struct {
	label, kind string
	place       folder.Place
}

// targetOptions are the type select's choices: the sync clients' folders
// found, any other folder, then the kinds reached over the network.
func targetOptions(m locales.Messages, places []folder.Place) []targetOption {
	var out []targetOption
	seen := map[string]bool{}
	for _, p := range places {
		label := p.Name
		if seen[label] {
			label += " (" + filepath.Base(p.Path) + ")"
		}
		seen[p.Name] = true
		out = append(out, targetOption{label, settings.BackupFolder, p})
	}
	other := m.BackupKindFolder()
	if len(places) > 0 {
		other = m.BackupKindOtherFolder()
	}
	return append(out,
		targetOption{label: other, kind: settings.BackupFolder},
		targetOption{label: "WebDAV", kind: settings.BackupWebDAV},
		targetOption{label: "S3", kind: settings.BackupS3},
		targetOption{label: "GitHub Gist", kind: settings.BackupGist})
}

// optionOf is the option that shows f's kind and, for a folder, the sync
// client's folder its path is in: the one chosen while it still does.
func optionOf(opts []targetOption, f *targetForm) targetOption {
	kind := targetKinds[f.kind]
	if i := slices.IndexFunc(opts, func(o targetOption) bool { return o.label == f.choice }); i >= 0 && opts[i].kind == kind {
		return opts[i]
	}
	var places []folder.Place
	for _, o := range opts {
		if o.place.Path != "" {
			places = append(places, o.place)
		}
	}
	p, inPlace := folder.PlaceOf(places, f.path)
	for _, o := range opts {
		if o.kind == kind && (kind != settings.BackupFolder || o.place.Path == p.Path || !inPlace && o.place.Path == "") {
			return o
		}
	}
	return opts[0]
}

// chooseOption makes o f's kind; a sync client's folder starts the path
// in it, unless the path already is.
func chooseOption(f *targetForm, o targetOption) {
	f.choice, f.kind = o.label, slices.Index(targetKinds, o.kind)
	if o.place.Path != "" {
		if _, ok := folder.PlaceOf([]folder.Place{o.place}, f.path); !ok {
			f.path = filepath.Join(o.place.Path, "SSHYard")
		}
	}
}

// defaultFolder is where a new folder place keeps its backups: an SSHYard
// folder in the first sync client's folder found, iCloud Drive first.
func defaultFolder(places []folder.Place) string {
	if len(places) == 0 {
		return ""
	}
	return filepath.Join(places[0].Path, "SSHYard")
}

// targetForm is the backup location editor's fields. The place's secret
// and own backup password live here only while the sheet is open: saving
// moves the secret, and the key derived from the password, into the
// encrypted local store.
type targetForm struct {
	isNew bool
	// orig is the place edited; a new one has its ID already
	orig settings.BackupTarget
	name string
	// kind is the index in targetKinds, choice the type select's option
	kind                                   int
	choice                                 string
	url, dir, user, bucket, region, gistID string
	// path is a folder place's full path, apart from dir so that another
	// kind's folder is not taken for it
	path      string
	pathStyle bool
	secret    string
	// proxy is the proxy select's ID, "" none
	proxy string
	keep  float64
	// source says whether the place seals with the default backup
	// password (pwDefault) or its own (pwCustom), password, typed twice;
	// secrets puts the local store's secrets in its backups
	source            int
	password, confirm string
	secrets           bool
	// auto turns automatic backups on, every
	// (settings.BackupAutoHours[every]) apart
	auto  bool
	every int
	// busy is set while the password is derived, testing while the
	// connection is tested; tested says it was reached
	busy, testing bool
	tested        string
	err           string
}

func (a *App) openTargetForm(t *settings.BackupTarget) {
	f := &targetForm{isNew: t == nil, orig: settings.BackupTarget{ID: assets.NewID(), Kind: settings.BackupFolder}, keep: settings.BackupKeepDefault, secrets: true, every: 1, source: pwCustom}
	if a.settings.DefaultBackupPassword {
		f.source = pwDefault
	}
	if t != nil {
		f.orig = *t
		f.name, f.url, f.dir, f.user, f.bucket, f.region, f.gistID, f.pathStyle = t.Name, t.URL, t.Dir, t.User, t.Bucket, t.Region, t.GistID, t.PathStyle
		f.keep, f.secrets, f.auto = float64(t.Keep), !t.OmitSecrets, t.AutoHours > 0
		f.source = pwCustom
		if t.DefaultPassword {
			f.source = pwDefault
		}
		f.proxy = t.ProxyID
	}
	f.kind = max(slices.Index(targetKinds, f.orig.Kind), 0)
	if t != nil && t.Kind == settings.BackupFolder {
		f.path, f.dir = t.Dir, ""
		a.knownPlaces(true)
	} else {
		f.path = defaultFolder(a.knownPlaces(true))
	}
	if i := slices.Index(settings.BackupAutoHours, f.orig.AutoHours); i >= 0 {
		f.every = i
	}
	a.target, a.targetEditing = f, true
}

func (a *App) closeTargetForm() {
	a.target, a.targetEditing = nil, false
}

// chooses reports whether the editor offers the default backup password:
// while one is set, or the place seals with it.
func (f *targetForm) chooses(a *App) bool {
	return a.settings.DefaultBackupPassword || f.orig.DefaultPassword
}

// ownKey reports whether the place edited has a key of its own that stays
// when no password is typed.
func (f *targetForm) ownKey() bool {
	return !f.isNew && !f.orig.DefaultPassword
}

// keepsSecret reports whether the place edited has a secret that stays
// its own when none is typed: a kind's secret means nothing to another.
func (f *targetForm) keepsSecret() bool {
	return !f.isNew && f.orig.Kind == targetKinds[f.kind]
}

// targetDraft is the place the form describes, or why it can not be one.
func (a *App) targetDraft(m locales.Messages, f *targetForm) (settings.BackupTarget, string) {
	t := f.orig
	t.Name, t.Kind = strings.TrimSpace(f.name), targetKinds[f.kind]
	t.URL, t.Dir, t.User = strings.TrimSpace(f.url), strings.TrimSpace(f.dir), strings.TrimSpace(f.user)
	t.Bucket, t.Region, t.GistID, t.PathStyle = strings.TrimSpace(f.bucket), strings.TrimSpace(f.region), strings.TrimSpace(f.gistID), f.pathStyle
	t.Keep = min(max(int(f.keep), 0), settings.BackupKeepMax)
	t.DefaultPassword, t.OmitSecrets, t.AutoHours = f.chooses(a) && f.source == pwDefault, !f.secrets, 0
	if f.auto {
		t.AutoHours = settings.BackupAutoHours[f.every]
	}
	t.ProxyID = f.proxy
	// What only another kind uses goes.
	switch t.Kind {
	case settings.BackupWebDAV:
		t.Bucket, t.Region, t.GistID, t.PathStyle = "", "", "", false
	case settings.BackupS3:
		t.GistID = ""
	case settings.BackupGist:
		t.URL, t.Dir, t.User, t.Bucket, t.Region, t.PathStyle = "", "", "", "", "", false
	case settings.BackupFolder:
		t.URL, t.User, t.Bucket, t.Region, t.GistID, t.PathStyle, t.ProxyID = "", "", "", "", "", false, ""
		t.Dir = strings.TrimSpace(f.path)
		if filepath.IsAbs(t.Dir) {
			t.Dir = filepath.Clean(t.Dir)
		}
	}
	needSecret := f.secret == "" && !f.keepsSecret()
	switch {
	case t.Name == "":
		return t, m.RequiredField(m.FieldName())
	case t.Kind == settings.BackupWebDAV && !targetURLOK(t.URL):
		return t, m.ErrTargetURL()
	case t.Kind == settings.BackupS3 && t.URL != "" && !targetURLOK(t.URL):
		return t, m.ErrTargetURL()
	case t.Kind == settings.BackupS3 && t.Bucket == "":
		return t, m.RequiredField(m.S3Bucket())
	case t.Kind == settings.BackupS3 && t.User == "":
		return t, m.RequiredField(m.S3AccessKey())
	case t.Kind == settings.BackupS3 && needSecret:
		return t, m.RequiredField(m.S3SecretKey())
	case t.Kind == settings.BackupGist && needSecret:
		return t, m.RequiredField(m.GistToken())
	case t.Kind == settings.BackupFolder && t.Dir == "":
		return t, m.RequiredField(m.SyncFolderPath())
	case t.Kind == settings.BackupFolder && !filepath.IsAbs(t.Dir):
		return t, m.ErrTargetFolder()
	}
	return t, ""
}

// targetURLOK reports whether raw can be a WebDAV server or an S3
// endpoint: both are http or https with a host and nothing else.
func targetURLOK(raw string) bool {
	_, err := webdav.CheckURL(raw)
	return err == nil
}

// plainHTTP reports whether raw is a URL sent unencrypted.
func plainHTTP(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Scheme, "http")
}

// SaveTargetForm checks the sheet, derives the place's key off the UI
// thread when a password was typed, then saves the place. A place with a
// password of its own needs it when new or without a key, and otherwise
// keeps its key; one with the default backup password needs that set.
func (a *App) SaveTargetForm(m locales.Messages) bool {
	f := a.target
	if f == nil || f.busy {
		return false
	}
	f.err, f.tested = "", ""
	t, msg := a.targetDraft(m, f)
	if msg != "" {
		f.err = msg
		return false
	}
	if t.DefaultPassword && !a.settings.DefaultBackupPassword {
		f.err = m.ErrDefaultBackupKeyMissing()
		return false
	}
	newKey := !t.DefaultPassword && (!f.ownKey() || f.password != "")
	if newKey {
		switch {
		case f.password == "":
			f.err = m.RequiredField(m.BackupPassword())
			return false
		case len([]rune(f.password)) < minBackupPassword:
			f.err = m.BackupPasswordShort(strconv.Itoa(minBackupPassword))
			return false
		case f.password != f.confirm:
			f.err = m.BackupPasswordMismatch()
			return false
		}
	}
	if !a.unlocked() {
		draft := a.target
		a.withSecrets(func() {
			if a.target == draft {
				a.SaveTargetForm(m)
			}
		})
		return false
	}
	if t.DefaultPassword {
		return a.commitTarget(m, f, t, nil)
	}
	if !newKey {
		switch ok, err := a.kc.Exists(keychain.BackupKey(t.ID)); {
		case err != nil:
			f.err = m.ErrGeneric(secretStoreErrorText(m, err))
			return false
		case !ok:
			f.err = m.RequiredField(m.BackupPassword())
			return false
		}
		return a.commitTarget(m, f, t, nil)
	}
	pw, cost := []byte(f.password), a.kdf
	f.busy = true
	go func() {
		k, err := vault.Derive(pw, cost)
		clear(pw)
		a.post(func() {
			defer k.Clear()
			f.busy = false
			switch {
			case a.target != f:
			case err != nil:
				f.err = m.ErrGeneric(err.Error())
			default:
				a.commitTarget(m, f, t, &k)
			}
		})
	}()
	return true
}

// commitTarget saves t: its secret and the new key k, when there is one,
// in one write to the store, then the settings, which failing puts the
// store back. A place sealing with the default backup password loses its
// own key.
func (a *App) commitTarget(m locales.Messages, f *targetForm, t settings.BackupTarget, k *vault.Key) bool {
	var changes []keychain.Change
	switch {
	case f.secret != "":
		changes = append(changes, keychain.Change{Key: keychain.BackupSecret(t.ID), Value: []byte(f.secret)})
	case !f.keepsSecret():
		changes = append(changes, keychain.Change{Key: keychain.BackupSecret(t.ID), Delete: true})
	}
	switch {
	case k != nil:
		b, err := json.Marshal(k)
		if err != nil {
			f.err = m.ErrGeneric(err.Error())
			return false
		}
		defer clear(b)
		changes = append(changes, keychain.Change{Key: keychain.BackupKey(t.ID), Value: b})
	case t.DefaultPassword && f.ownKey():
		changes = append(changes, keychain.Change{Key: keychain.BackupKey(t.ID), Delete: true})
	}
	tx := &secretTx{kc: a.kc}
	fail := func(msg string) bool {
		if rerr := tx.rollback(); rerr != nil {
			msg = m.ErrKeychainRestore(msg, secretStoreErrorText(m, rerr))
		}
		f.err = msg
		return false
	}
	if err := tx.setBatch(changes); err != nil {
		return fail(m.ErrKeychainSave(secretStoreErrorText(m, err)))
	}
	s := a.settings
	s.BackupTargets = slices.Clone(s.BackupTargets)
	if i := slices.IndexFunc(s.BackupTargets, func(o settings.BackupTarget) bool { return o.ID == t.ID }); i >= 0 {
		s.BackupTargets[i] = t
	} else {
		s.BackupTargets = append(s.BackupTargets, t)
	}
	if err := a.ApplySettings(s); err != nil {
		return fail(m.ErrGeneric(err.Error()))
	}
	// Another place starts over: its last backup is not there.
	if !f.isNew && !samePlace(f.orig, t) {
		delete(a.cloudNow().Targets, t.ID)
		a.saveCloud()
	}
	a.closeTargetForm()
	return true
}

// samePlace reports whether a and b keep their backups in the same place.
func samePlace(a, b settings.BackupTarget) bool {
	return a.Kind == b.Kind && a.URL == b.URL && a.Dir == b.Dir && a.User == b.User && a.Bucket == b.Bucket &&
		a.Region == b.Region && a.PathStyle == b.PathStyle && (a.GistID == "" || a.GistID == b.GistID)
}

// TestTarget checks, off the UI thread, that the place the sheet
// describes can be reached with its secret, typed or saved.
func (a *App) TestTarget(m locales.Messages) bool {
	f := a.target
	if f == nil || f.testing {
		return false
	}
	f.err, f.tested = "", ""
	t, msg := a.targetDraft(m, f)
	if msg != "" {
		f.err = msg
		return false
	}
	secret := f.secret
	if secret == "" && f.keepsSecret() && t.Kind != settings.BackupFolder {
		if !a.unlocked() {
			draft := a.target
			a.withSecrets(func() {
				if a.target == draft {
					a.TestTarget(m)
				}
			})
			return false
		}
		b, err := a.kc.Get(keychain.BackupSecret(t.ID))
		if err != nil && !errors.Is(err, keychain.ErrNotFound) {
			f.err = m.ErrGeneric(secretStoreErrorText(m, err))
			return false
		}
		secret = string(b)
		clear(b)
	}
	// A gist found is shown, and kept once the place is saved.
	var found string
	r, err := a.remote(t, secret, func(id string) { found = id })
	if err != nil {
		f.err = backupErrText(m, err)
		return false
	}
	f.testing = true
	ctx := a.cloudCtx
	go func() {
		err := r.Check(ctx)
		a.post(func() {
			f.testing = false
			switch {
			case err != nil:
				f.err = backupErrText(m, err)
			case found != "":
				f.tested = m.ConnectionGistFound()
				if strings.TrimSpace(f.gistID) == "" {
					f.gistID = found
				}
			default:
				f.tested = m.ConnectionOK()
			}
		})
	}()
	return true
}

// remoteList is the sheet of the backups in a place, to restore one.
type remoteList struct {
	target  settings.BackupTarget
	r       backup.Remote
	loading bool
	objs    []backup.Object
	err     error
	// fetching is the backup being downloaded, "" for none
	fetching string
}

// OpenRemoteList shows the backups in the place id, listed off the UI
// thread.
func (a *App) OpenRemoteList(id string) {
	t, ok := a.settings.BackupTarget(id)
	if !ok {
		return
	}
	if !a.unlocked() {
		a.withSecrets(func() { a.OpenRemoteList(id) })
		return
	}
	r, err := a.targetRemote(t)
	l := &remoteList{target: t, r: r, loading: err == nil, err: err}
	a.remotes, a.listingRemote = l, true
	if err != nil {
		return
	}
	ctx := a.cloudCtx
	go func() {
		objs, err := r.List(ctx)
		backup.SortNewest(objs)
		a.post(func() { l.loading, l.objs, l.err = false, objs, err })
	}()
}

// fetchRemote downloads the backup name off the UI thread, then asks for
// its password in the restore sheet.
func (a *App) fetchRemote(m locales.Messages, l *remoteList, name string) {
	if l.fetching != "" {
		return
	}
	l.fetching, l.err = name, nil
	r, ctx := l.r, a.cloudCtx
	go func() {
		data, err := r.Get(ctx, name)
		a.post(func() {
			if a.remotes != l {
				return
			}
			l.fetching = ""
			if err != nil {
				l.err = err
				return
			}
			a.remotes, a.listingRemote = nil, false
			a.openRestore(m, name, data, nil)
		})
	}()
}

// targetIcon is the icon of a kind of place.
func targetIcon(kind string) *icons.Icon {
	switch kind {
	case settings.BackupS3:
		return icons.BackupS3
	case settings.BackupGist:
		return icons.BackupGist
	case settings.BackupFolder:
		return icons.BackupFolder
	}
	return icons.BackupWebDAV
}

// targetKindText names a kind of place; the names are not translated,
// but for a folder's.
func targetKindText(m locales.Messages, kind string) string {
	switch kind {
	case settings.BackupFolder:
		return m.BackupKindFolder()
	case settings.BackupS3:
		return "S3"
	case settings.BackupGist:
		return "GitHub Gist"
	}
	return "WebDAV"
}

// autoText is how often automatic backups run every hours.
func autoText(m locales.Messages, hours int) string {
	switch hours {
	case 6:
		return m.BackupEvery6Hours()
	case 168:
		return m.BackupEveryWeek()
	}
	return m.BackupEveryDay()
}

// targetNote is a place's row note: its kind, where it is, how its last
// backup went and how often it backs up by itself.
func (a *App) targetNote(m locales.Messages, t settings.BackupTarget) string {
	parts := []string{targetKindText(m, t.Kind)}
	switch t.Kind {
	case settings.BackupFolder:
		if p, ok := folder.PlaceOf(a.knownPlaces(false), t.Dir); ok {
			parts[0] = m.BackupKindFolderVia(p.Name)
		}
		parts = append(parts, a.pv(filepath.Base(t.Dir)))
	case settings.BackupWebDAV:
		if u, err := url.Parse(t.URL); err == nil && u.Host != "" {
			parts = append(parts, a.pv(u.Host))
		}
	case settings.BackupS3:
		parts = append(parts, a.pv(t.Bucket))
	}
	var st targetState
	if p := a.cloudNow().Targets[t.ID]; p != nil {
		st = *p
	}
	switch {
	case st.busy:
		parts = append(parts, m.TargetBackingUp())
	case st.err != nil:
		parts = append(parts, m.TargetBackupFailed(backupErrText(m, st.err)))
	case st.Last.IsZero():
		parts = append(parts, m.TargetNeverBackedUp())
	default:
		parts = append(parts, m.TargetLastBackup(st.Last.Local().Format("2006-01-02 15:04")))
	}
	if t.AutoHours > 0 {
		parts = append(parts, m.TargetAuto(autoText(m, t.AutoHours)))
	}
	return strings.Join(parts, " · ")
}

// cloudSection is the backup page's places to back up to.
func (a *App) cloudSection(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	a.settingsSection(c, tok, icons.Cloud, m.CloudBackupSection(), func(g *settingsGroup) {
		ts := a.settings.BackupTargets
		if len(ts) == 0 {
			g.row(m.NoBackupTargets(), m.NoBackupTargetsNote(), func() {})
		}
		for _, t := range ts {
			busy := false
			if st := a.cloudNow().Targets[t.ID]; st != nil {
				busy = st.busy
			}
			g.row(t.Name, a.targetNote(m, t), func() {
				if (IconButton{Icon: icons.CloudUpload, Label: m.BackupNow(), Disabled: busy}).Clicked(c, "bt:up:"+t.ID) {
					a.BackupNow(t.ID)
				}
				if iconButton(c, "bt:down:"+t.ID, icons.CloudDownload, m.RestoreFromTarget()) {
					a.OpenRemoteList(t.ID)
				}
				if iconButton(c, "bt:edit:"+t.ID, icons.Edit, m.Edit()) {
					a.openTargetForm(&t)
				}
				if (IconButton{Icon: icons.Delete, Label: m.DeleteBackupTarget(), Danger: true, Disabled: busy}).Clicked(c, "bt:del:"+t.ID) {
					a.runSecretAction(func() error { return a.DeleteBackupTarget(t.ID) }, func(err error) { a.toastError(a.inUseText(m, err)) })
				}
			}, func() { softTile(c, targetIcon(t.Kind), tok.Tint(t.ID), 28) })
		}
		g.row("", "", func() {
			if pill(c, m.AddBackupTarget(), false, false, false) {
				a.openTargetForm(nil)
			}
		})
	})
	ui.Text(c, m.CloudBackupNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted).Padding(0, 4)
}

// switchRow is a switch with its label and a muted note before it, as in
// the backup sheets.
func switchRow(c *ui.Context, tok theme.Tokens, key, label, note string, on *bool, disabled bool) {
	ui.Row(c).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(theme.Body).FontWeight(500)
			if note != "" {
				ui.Text(c, note).FontSize(theme.Caption).TextColor(tok.TextMuted)
			}
		})
		formDisabled(formSwitch(c.Key(key), on).Label(label), disabled)
	})
}

// notice is a rounded banner tinted col with icon ic, for what to know
// before saving.
func notice(c *ui.Context, key string, ic *icons.Icon, col ui.Color, text string) {
	ui.Row(c.Key(key)).Padding(8, 10).Gap(8).Radius(theme.RadiusControl).Background(col.Alpha(0.12)).
		Border(1, col.Alpha(0.35)).FillWidth().Children(func() {
		ui.Icon(c, ic.SVG).Size(14, 14).Shrink(0).TextColor(col)
		ui.Text(c, text).FontSize(theme.Footnote).Shrink(1)
	})
}

// targetView is the backup location editor.
func (a *App) targetView(c *ui.Context, m locales.Messages) {
	f := a.target
	if f == nil {
		return
	}
	tok := currentTokens(c)
	kind := targetKinds[f.kind]
	sheet(stickyModal(c, &a.targetEditing, func() {
		ui.Column(c).Gap(18).Width(480).Shrink(1).MinHeight(0).Children(func() {
			title := m.EditBackupTarget()
			if f.isNew {
				title = m.NewBackupTarget()
			}
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, targetIcon(kind), tok.Tint(f.orig.ID), 36)
				ui.Column(c).Gap(1).Shrink(1).Children(func() {
					sheetTitle(c, title)
					if f.name != "" {
						ui.Text(c, f.name).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
					}
				})
			})
			sheetScroll(ui.Scroll(c.Key("bt:scroll")).Shrink(1).MinHeight(0)).Children(func() {
				ui.Column(c).Gap(14).FillWidth().Children(func() {
					a.targetFields(c, m, tok, f, kind)
				})
			})
			if f.tested != "" {
				ui.Text(c, f.tested).FontSize(theme.Footnote).TextColor(tok.Success)
			}
			if f.err != "" {
				ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				if !f.isNew && pill(c, m.Delete(), false, true, f.busy) {
					id := f.orig.ID
					a.runSecretAction(func() error {
						if a.target != f {
							return nil
						}
						if err := a.DeleteBackupTarget(id); err != nil {
							return err
						}
						a.targetEditing = false
						return nil
					}, func(err error) { f.err = a.inUseText(m, err) })
				}
				test := m.TestConnection()
				if f.testing {
					test = m.TestingConnection()
				}
				if pill(c, test, false, false, f.testing) {
					a.TestTarget(m)
				}
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.targetEditing = false
				}
				if pill(c, m.Save(), true, false, f.busy) {
					a.SaveTargetForm(m)
				}
			})
		})
	}), tok)
	if !a.targetEditing && a.target == f && !a.proxyKeeps(f) {
		a.closeTargetForm()
	}
}

// targetFields are the editor's fields for kind, and what to know of them.
func (a *App) targetFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *targetForm, kind string) {
	unchanged := ""
	if f.keepsSecret() {
		unchanged = m.PasswordUnchanged()
	}
	ui.Form(c, func() {
		ui.Field(c, m.FieldName(), func() {
			in := formTextInput(c.Key("bt:name"), &f.name).Placeholder("NAS").AutoFocus()
			a.targetHold.apply(c, f, in)
		})
		ui.Field(c, m.FieldType(), func() {
			opts := targetOptions(m, a.knownPlaces(false))
			cur := optionOf(opts, f)
			f.choice = cur.label
			labels := make([]string, len(opts))
			for i, o := range opts {
				labels[i] = o.label
			}
			if formSelect(c.Key("bt:kind"), &f.choice, labels).Label(m.FieldType()).Changed() {
				if i := slices.Index(labels, f.choice); i >= 0 {
					chooseOption(f, opts[i])
				}
			}
		})
		switch kind {
		case settings.BackupWebDAV:
			ui.Field(c, m.FieldServerURL(), func() {
				formTextInput(c.Key("bt:url"), &f.url).Placeholder("https://dav.example.com/remote.php/dav/files/me/")
			})
			ui.Field(c, m.WebdavFolder(), func() { formTextInput(c.Key("bt:dir"), &f.dir).Placeholder("SSHYard") })
			ui.Field(c, m.FieldUser(), func() { formTextInput(c.Key("bt:user"), &f.user) })
			ui.Field(c, m.FieldPassword(), func() { formTextInput(c.Key("bt:secret"), &f.secret).Password().Placeholder(unchanged) })
		case settings.BackupS3:
			ui.Field(c, m.S3Endpoint(), func() { formTextInput(c.Key("bt:url"), &f.url).Placeholder(m.S3EndpointPlaceholder()) })
			ui.Field(c, m.S3Region(), func() { formTextInput(c.Key("bt:region"), &f.region).Placeholder("us-east-1") })
			ui.Field(c, m.S3Bucket(), func() { formTextInput(c.Key("bt:bucket"), &f.bucket) })
			ui.Field(c, m.S3Prefix(), func() { formTextInput(c.Key("bt:dir"), &f.dir).Placeholder("sshyard/") })
			ui.Field(c, m.S3AccessKey(), func() { formTextInput(c.Key("bt:user"), &f.user) })
			ui.Field(c, m.S3SecretKey(), func() { formTextInput(c.Key("bt:secret"), &f.secret).Password().Placeholder(unchanged) })
		case settings.BackupGist:
			ui.Field(c, m.GistToken(), func() { formTextInput(c.Key("bt:secret"), &f.secret).Password().Placeholder(unchanged) })
			ui.Field(c, m.GistID(), func() { formTextInput(c.Key("bt:gist"), &f.gistID).Placeholder(m.GistIDPlaceholder()) })
		case settings.BackupFolder:
			ui.Field(c, m.SyncFolderPath(), func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
					formTextInput(c.Key("bt:path"), &f.path).Placeholder(m.SyncFolderPlaceholder()).Label(m.SyncFolderPath()).Grow(1).Shrink(1)
					if (IconButton{Icon: icons.OpenFile, Label: m.ChooseSyncFolder(), Disabled: a.p.PickFolder == nil}).Clicked(c, "bt:pick") {
						a.pickTargetFolder(m, f)
					}
				})
			})
		}
		if kind != settings.BackupFolder {
			ui.Field(c, m.FieldProxy(), func() {
				a.proxySelect(c.Key("bt:proxy"), m, &f.proxy, proxyPick{owner: f,
					leave: func() { a.targetEditing = false },
					back: func(id string) {
						a.target, a.targetEditing = f, true
						if id != "" {
							f.proxy = id
						}
					}})
			})
		}
		ui.Field(c, m.BackupKeep(), func() {
			ui.NumberInput(c.Key("bt:keep"), &f.keep, 0, settings.BackupKeepMax, 1).Label(m.BackupKeep())
		})
	})
	ui.Text(c, m.BackupKeepNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
	switch kind {
	case settings.BackupS3:
		switchRow(c, tok, "bt:pathstyle", m.S3PathStyle(), m.S3PathStyleNote(), &f.pathStyle, false)
	case settings.BackupGist:
		ui.Text(c, m.GistTokenNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
		notice(c, "bt:gistnote", icons.Info, tok.Accent, m.GistNotice())
	case settings.BackupFolder:
		a.folderNotes(c, m, tok, f)
	}
	if (kind == settings.BackupWebDAV || kind == settings.BackupS3) && plainHTTP(f.url) {
		notice(c, "bt:http", icons.Warning, tok.Warning, m.WarnPlainHTTP())
	}
	ui.Box(c).Height(1).FillWidth().Background(tok.Line)
	choose := f.chooses(a)
	typed := !choose || f.source == pwCustom
	ph := ""
	if f.ownKey() {
		ph = m.PasswordUnchanged()
	}
	ui.Form(c, func() {
		if choose {
			passwordSource(c, m, "bt:source", &f.source)
		}
		if !typed {
			return
		}
		ui.Field(c, m.BackupPassword(), func() {
			formTextInput(c.Key("bt:pw"), &f.password).Password().Placeholder(ph).Disabled(f.busy)
		})
		ui.Field(c, m.ConfirmPassword(), func() {
			formTextInput(c.Key("bt:confirm"), &f.confirm).Password().Placeholder(ph).Disabled(f.busy)
		})
	})
	switch {
	case typed:
		ui.Text(c, m.BackupPasswordNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
	case !a.settings.DefaultBackupPassword:
		notice(c, "bt:nodefault", icons.Warning, tok.Warning, m.ErrDefaultBackupKeyMissing())
	default:
		ui.Text(c, m.DefaultBackupPasswordUsed()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
	}
	switchRow(c, tok, "bt:secrets", m.BackupIncludeSecrets(), m.BackupIncludeSecretsNote(), &f.secrets, f.busy)
	ui.Box(c).Height(1).FillWidth().Background(tok.Line)
	switchRow(c, tok, "bt:auto", m.AutoBackup(), m.AutoBackupNote(), &f.auto, f.busy)
	if f.auto {
		ui.Form(c, func() {
			ui.Field(c, m.AutoBackupInterval(), func() {
				formSegmented(c.Key("bt:every"), &f.every, false, m.BackupEvery6Hours(), m.BackupEveryDay(), m.BackupEveryWeek()).Label(m.AutoBackupInterval())
			})
		})
	}
}

// remoteListView is the sheet of a place's backups.
func (a *App) remoteListView(c *ui.Context, m locales.Messages) {
	l := a.remotes
	if l == nil {
		return
	}
	tok := currentTokens(c)
	sheet(stickyModal(c, &a.listingRemote, func() {
		ui.Column(c).Gap(18).Width(480).Shrink(1).MinHeight(0).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, targetIcon(l.target.Kind), tok.Tint(l.target.ID), 36)
				ui.Column(c).Gap(1).Shrink(1).Children(func() {
					sheetTitle(c, m.RestoreFromTarget())
					ui.Text(c, l.target.Name).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
				})
			})
			switch {
			case l.loading:
				ui.Text(c, m.RemoteBackupsLoading()).FontSize(theme.Body).TextColor(tok.TextMuted)
			case len(l.objs) == 0 && l.err == nil:
				ui.Text(c, m.RemoteBackupsEmpty()).FontSize(theme.Body).TextColor(tok.TextMuted)
			case len(l.objs) > 0:
				device := a.deviceID()
				sheetScroll(ui.Scroll(c.Key("rl:scroll")).Shrink(1).MinHeight(0).MaxHeight(360)).Children(func() {
					card(c, tok).FillWidth().Children(func() {
						g := &settingsGroup{c: c, tok: tok}
						for _, o := range l.objs {
							date := o.Time.Local().Format("2006-01-02 15:04")
							label := m.RemoteBackupOtherDevice(date, o.Device)
							if o.Device == device {
								label = m.RemoteBackupThisDevice(date)
							}
							note := ""
							if o.Size > 0 {
								note = humanSize(o.Size)
							}
							g.row(label, note, func() {
								if l.fetching == o.Name {
									ui.Text(c, m.RemoteBackupDownloading()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
									return
								}
								if (IconButton{Icon: icons.CloudDownload, Label: m.RestoreThisBackup(), Disabled: l.fetching != "", Size: Small}).Clicked(c, "rl:get:"+o.Name) {
									a.fetchRemote(m, l, o.Name)
								}
							})
						}
					})
				})
			}
			if l.err != nil {
				ui.Text(c, backupErrText(m, l.err)).FontSize(theme.Footnote).TextColor(tok.Danger)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Close(), false, false, false) {
					a.listingRemote = false
				}
			})
		})
	}), tok)
	if !a.listingRemote && a.remotes == l {
		a.remotes = nil
	}
}

// pickTargetFolder has the folder place's path chosen in the system's
// dialog.
func (a *App) pickTargetFolder(m locales.Messages, f *targetForm) {
	start := f.path
	if !filepath.IsAbs(start) {
		start = a.localHome()
	}
	title := m.ChooseSyncFolder()
	go func() {
		dir, err := a.p.PickFolder(title, start)
		a.post(func() {
			switch {
			case a.target != f:
			case err != nil:
				f.err = m.ErrGeneric(err.Error())
			case dir != "":
				f.path, f.err, f.tested = dir, "", ""
			}
		})
	}()
}

// folderNotes are what to know of a folder place.
func (a *App) folderNotes(c *ui.Context, m locales.Messages, tok theme.Tokens, f *targetForm) {
	places := a.knownPlaces(false)
	ui.Text(c, m.SyncFolderNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
	if runtime.GOOS == "darwin" && !slices.ContainsFunc(places, func(p folder.Place) bool { return p.Name == folder.ICloudDrive }) {
		notice(c, "bt:icloud", icons.Info, tok.Accent, m.ICloudDriveOff())
	}
}
