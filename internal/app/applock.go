package app

import (
	"errors"
	"image"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/Licoy/sshyard/internal/applock"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"rsc.io/qr"
)

// The app lock stands in front of every window (applock): while it is
// locked the windows show only the lock screen, and the shortcuts, the
// tray's actions and the notifications naming hosts wait, while sessions,
// forwards, transfers and backups go on. It locks as the app starts, after
// the minutes without input it is set to, as the screen locks or the
// computer sleeps, and when asked. The secret of its one-time codes is in
// the encrypted store, which must be unlocked to check a code; so is what
// opens it (keychain.AppLock), which wins over applock.json once the
// store is unlocked, as anyone reading the data folder can delete or
// replace that file. A lock that can no longer be opened is reset, which
// deletes every secret before the lock.

// appLock is the lock and the lock screen's fields, which live only while
// it shows.
type appLock struct {
	cfg applock.Config
	// loaded is set once applock.json was read; broken when it could not
	// be, which keeps the app locked until the store's record restores it
	// or the lock is reset
	loaded, broken bool
	locked         bool
	// input is when a window last had input
	input          time.Time
	password, code string
	// busy is set while the password is checked, off the UI thread
	busy bool
	err  string
	// gen is a new one for each attempt that failed, whose fields start
	// empty
	gen  int
	hold focusHold
	// form is the sheet setting the lock up, changing or removing it
	form     *lockForm
	formOpen bool
	formHold focusHold
	// resetOpen asks whether to reset the lock
	resetOpen bool
	stopSleep func()
	// now is time.Now, replaced in tests
	now func() time.Time
}

func (a *App) lockClock() time.Time {
	if a.lock.now != nil {
		return a.lock.now()
	}
	return time.Now()
}

func (a *App) lockPath() string { return filepath.Join(a.p.DataDir, applock.File) }

// loadAppLock reads the lock once the data folder is known: locked from
// the start when one is set up, or when its file cannot be read.
func (a *App) loadAppLock() {
	l := &a.lock
	if l.loaded {
		return
	}
	l.loaded = true
	l.input = a.lockClock()
	l.cfg = applock.Default()
	if a.p.DataDir == "" {
		return
	}
	cfg, err := applock.Load(a.lockPath())
	if err != nil {
		log.Printf("sshyard: app lock unreadable: %v", err)
		l.broken, l.locked = true, true
		return
	}
	l.cfg, l.locked = cfg, cfg.Enabled()
}

// appLocked reports whether the lock screen stands in front of the app.
func (a *App) appLocked() bool { return a.lock.locked }

// LockApp locks the app now, when a lock is set up.
func (a *App) LockApp() {
	l := &a.lock
	if !l.cfg.Enabled() || l.locked {
		return
	}
	l.locked = true
	l.password, l.code, l.err, l.hold = "", "", "", focusHold{}
	l.gen++
	a.closeLockForm()
	// Only an unlock of the store may show over the lock screen.
	if a.masterPw != nil && !a.masterPw.unlocks() {
		a.closeMasterPassword()
	}
	a.invalidate()
}

// noteInput is told of each input in a window.
func (a *App) noteInput(ui.InputEvent) { a.lock.input = a.lockClock() }

// checkIdle locks the app once it had no input for the minutes set, and
// is how long until then otherwise: 0 when it never locks.
func (a *App) checkIdle() time.Duration {
	l := &a.lock
	if l.locked || !l.cfg.Enabled() || l.cfg.IdleMinutes <= 0 {
		return 0
	}
	left := time.Duration(l.cfg.IdleMinutes)*time.Minute - a.lockClock().Sub(l.input)
	if left <= 0 {
		a.LockApp()
		return 0
	}
	return left
}

// lockGate runs at the start of a window's frame: true when the lock
// screen shows in place of the window.
func (a *App) lockGate(c *ui.Context) bool {
	if left := a.checkIdle(); left > 0 {
		c.After(left)
	}
	return a.lock.locked
}

// watchSleep locks the app as the screen locks or the computer sleeps,
// while the lock is set to.
func (a *App) watchSleep() {
	if a.p.WatchSleep == nil || a.lock.stopSleep != nil {
		return
	}
	a.lock.stopSleep = a.p.WatchSleep(func() {
		a.post(func() {
			if a.lock.cfg.LockOnSleep {
				a.LockApp()
			}
		})
	})
}

// saveLock keeps cfg as the lock.
func (a *App) saveLock(cfg applock.Config) error {
	if err := applock.Save(a.lockPath(), cfg); err != nil {
		return err
	}
	a.lock.cfg = cfg
	return nil
}

// countFailure counts a failed attempt in the lock, kept so that quitting
// does not start the count over; a failure to save it keeps it here.
func (a *App) countFailure(cfg applock.Config) {
	cfg.Fail(a.lockClock())
	if err := a.saveLock(cfg); err != nil {
		log.Printf("sshyard: app lock attempts not saved: %v", err)
		a.lock.cfg = cfg
	}
}

// waitText says how long until the next attempt, "" when it may be made.
func (a *App) waitText(m locales.Messages) string {
	w := a.lock.cfg.Wait(a.lockClock())
	if w <= 0 {
		return ""
	}
	return m.AppLockWait(strconv.Itoa(int((w + time.Second - 1) / time.Second)))
}

// totpSecret reads the one-time codes' secret from the unlocked store.
func (a *App) totpSecret(m locales.Messages) ([]byte, string) {
	b, err := a.kc.Get(keychain.AppLockTOTP)
	switch {
	case errors.Is(err, keychain.ErrNotFound):
		return nil, m.AppLockSecretMissing()
	case err != nil:
		return nil, secretStoreErrorText(m, err)
	}
	return b, ""
}

// SubmitAppUnlock checks what the lock screen holds, the password off the
// UI thread, the code on it once the store is unlocked.
func (a *App) SubmitAppUnlock(m locales.Messages) bool {
	l := &a.lock
	if !l.locked || l.busy || l.broken {
		return false
	}
	cfg := l.cfg
	if l.err = a.waitText(m); l.err != "" {
		return false
	}
	if cfg.Method.NeedsPassword() && l.password == "" || cfg.Method.NeedsCode() && len(l.code) != applock.Digits {
		return false
	}
	if cfg.Method.NeedsCode() && !a.unlocked() {
		a.withSecrets(func() {
			if a.lock.locked {
				a.SubmitAppUnlock(m)
			}
		})
		return false
	}
	pw, code, gen := []byte(l.password), l.code, l.gen
	l.busy = true
	go func() {
		ok, err := true, error(nil)
		if cfg.Method.NeedsPassword() {
			ok, err = applock.CheckPassword(cfg.Verifier, pw)
		}
		clear(pw)
		a.post(func() { a.appUnlockDone(m, ok, err, code, gen) })
	}()
	return true
}

// appUnlockDone takes the password's check, made against the lock as it
// was at gen: one the store's record replaced since opens nothing.
func (a *App) appUnlockDone(m locales.Messages, ok bool, err error, code string, gen int) {
	l := &a.lock
	l.busy = false
	if !l.locked || l.gen != gen {
		return
	}
	if err != nil {
		l.err = m.ErrGeneric(err.Error())
		return
	}
	cfg := l.cfg
	if cfg.Method.NeedsCode() {
		secret, msg := a.totpSecret(m)
		if msg != "" {
			l.err = msg
			return
		}
		step, good := applock.Verify(secret, code, a.lockClock(), cfg.LastStep)
		clear(secret)
		if good {
			cfg.LastStep = step
		}
		ok = ok && good
	}
	if !ok {
		a.countFailure(cfg)
		l.password, l.code, l.err = "", "", m.AppLockWrong()
		l.gen++
		return
	}
	cfg.Succeed()
	if err := a.saveLock(cfg); err != nil {
		log.Printf("sshyard: app lock not saved: %v", err)
		l.cfg = cfg
	}
	l.locked = false
	l.input = a.lockClock()
	l.password, l.code, l.err = "", "", ""
	a.invalidate()
}

// What the lock sheet does.
type lockFormMode int

const (
	lockEnable  lockFormMode = iota // set the lock up
	lockChange                      // change how it opens
	lockDisable                     // remove it
)

// lockForm is the sheet setting the lock up, changing or removing it. The
// lock as it is must be opened again to change or remove it.
type lockForm struct {
	mode lockFormMode
	// method is the index in applock.Methods of the new method
	method               int
	curPassword, curCode string
	password, confirm    string
	// secret is a new authenticator's, shown as qr and confirmed by code
	secret []byte
	qr     *ui.Bitmap
	code   string
	busy   bool
	err    string
}

func (a *App) openLockForm(mode lockFormMode) {
	f := &lockForm{mode: mode}
	if mode == lockChange {
		f.method = max(0, slices.Index(applock.Methods, a.lock.cfg.Method))
	}
	a.lock.form, a.lock.formOpen = f, true
}

func (a *App) closeLockForm() {
	if f := a.lock.form; f != nil {
		clear(f.secret)
	}
	a.lock.form, a.lock.formOpen = nil, false
}

// next is the method the sheet sets, Off to remove the lock.
func (f *lockForm) next() applock.Method {
	if f.mode == lockDisable {
		return applock.Off
	}
	return applock.Methods[f.method]
}

// freshSecret reports whether the sheet binds a new authenticator: one
// already bound stays while the method keeps asking for its codes.
func (a *App) freshSecret(f *lockForm) bool {
	return f.next().NeedsCode() && !(f.mode == lockChange && a.lock.cfg.Method.NeedsCode())
}

// ensureSecret makes the new authenticator's secret and its QR code, once
// the sheet binds one.
func (a *App) ensureSecret(f *lockForm) {
	if !a.freshSecret(f) || f.secret != nil {
		return
	}
	f.secret = applock.NewSecret()
	account, _ := os.Hostname()
	if account == "" {
		account = "SSHYard"
	}
	if code, err := qr.Encode(applock.URI(f.secret, "SSHYard", account), qr.M); err == nil {
		f.qr = ui.NewBitmap(qrImage(code)).Pixelated()
	}
}

// qrImage is code's modules, one pixel each, in a quiet zone four wide:
// dark on light whatever the theme, as the cameras read them.
func qrImage(code *qr.Code) image.Image {
	const quiet = 4
	n := code.Size + 2*quiet
	img := image.NewGray(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			v := color.Gray{Y: 0xff}
			if code.Black(x-quiet, y-quiet) {
				v.Y = 0
			}
			img.SetGray(x, y, v)
		}
	}
	return img
}

// SubmitLockForm checks the sheet, then checks the lock as it is and
// derives the new password's verifier off the UI thread.
func (a *App) SubmitLockForm(m locales.Messages) bool {
	f := a.lock.form
	if f == nil || f.busy {
		return false
	}
	f.err = ""
	cur, next := a.lock.cfg, f.next()
	proving := f.mode != lockEnable
	if proving {
		if f.err = a.waitText(m); f.err != "" {
			return false
		}
		if cur.Method.NeedsPassword() && f.curPassword == "" || cur.Method.NeedsCode() && len(f.curCode) != applock.Digits {
			return false
		}
	}
	if next.NeedsPassword() {
		switch {
		case len([]rune(f.password)) < minBackupPassword:
			f.err = m.BackupPasswordShort(strconv.Itoa(minBackupPassword))
			return false
		case f.password != f.confirm:
			f.err = m.BackupPasswordMismatch()
			return false
		}
	}
	fresh := a.freshSecret(f)
	var freshStep int64
	if fresh {
		a.ensureSecret(f)
		var ok bool
		if freshStep, ok = applock.Verify(f.secret, f.code, a.lockClock(), 0); !ok {
			f.err = m.AppLockCodeWrong()
			return false
		}
	}
	// The store keeps what opens the lock, and the codes' secret.
	if !a.unlocked() {
		a.withSecrets(func() {
			if a.lock.form == f {
				a.SubmitLockForm(m)
			}
		})
		return false
	}
	curPw, newPw, kdf := []byte(f.curPassword), []byte(f.password), a.kdf
	checkPw, newVerifier := proving && cur.Method.NeedsPassword(), next.NeedsPassword()
	f.busy = true
	go func() {
		ok, err := true, error(nil)
		var verifier []byte
		if checkPw {
			ok, err = applock.CheckPassword(cur.Verifier, curPw)
		}
		if ok && err == nil && newVerifier {
			verifier, err = applock.NewVerifier(newPw, kdf)
		}
		clear(curPw)
		clear(newPw)
		a.post(func() { a.lockFormDone(m, f, ok, err, verifier, fresh, freshStep) })
	}()
	return true
}

func (a *App) lockFormDone(m locales.Messages, f *lockForm, ok bool, err error, verifier []byte, fresh bool, freshStep int64) {
	f.busy = false
	if a.lock.form != f {
		return
	}
	if err != nil {
		f.err = m.ErrGeneric(err.Error())
		return
	}
	cur := a.lock.cfg
	if f.mode != lockEnable && cur.Method.NeedsCode() {
		secret, msg := a.totpSecret(m)
		if msg != "" {
			f.err = msg
			return
		}
		step, good := applock.Verify(secret, f.curCode, a.lockClock(), cur.LastStep)
		clear(secret)
		if good {
			cur.LastStep = step
		}
		ok = ok && good
	}
	if !ok {
		a.countFailure(cur)
		f.curPassword, f.curCode, f.err = "", "", m.AppLockWrong()
		return
	}
	next := cur
	next.Succeed()
	next.Method, next.Verifier = f.next(), verifier
	switch {
	case !next.Method.NeedsCode():
		next.LastStep = 0
	case fresh:
		next.LastStep = freshStep
	}
	var secret []byte
	if fresh {
		secret = f.secret
	}
	if f.err = a.commitLock(m, cur.Method, next, secret); f.err == "" {
		a.lock.input = a.lockClock()
		a.closeLockForm()
	}
}

// commitLock keeps next as the lock, with secret as its codes' secret
// when a new authenticator is bound: the store's record of it first,
// which a failure to save the lock puts back. The secret of a method that
// no longer asks for codes is forgotten after. It says why it could not.
func (a *App) commitLock(m locales.Messages, was applock.Method, next applock.Config, secret []byte) string {
	tx := &secretTx{kc: a.kc}
	changes := []keychain.Change{{Key: keychain.AppLock, Delete: true}}
	if next.Enabled() {
		changes[0] = keychain.Change{Key: keychain.AppLock, Value: next.Record()}
	}
	if secret != nil {
		changes = append(changes, keychain.Change{Key: keychain.AppLockTOTP, Value: secret})
	}
	if err := tx.setBatch(changes); err != nil {
		return m.ErrKeychainSave(secretStoreErrorText(m, err))
	}
	if err := a.saveLock(next); err != nil {
		msg := m.ErrGeneric(err.Error())
		if rerr := tx.rollback(); rerr != nil {
			msg = m.ErrKeychainRestore(msg, secretStoreErrorText(m, rerr))
		}
		return msg
	}
	if was.NeedsCode() && !next.Method.NeedsCode() {
		if err := a.kc.Delete(keychain.AppLockTOTP); err != nil && !errors.Is(err, keychain.ErrNotFound) {
			a.toastError(m.ErrGeneric(secretStoreErrorText(m, err)))
		}
	}
	return ""
}

// adoptLockRecord runs as the store unlocks: the lock opens as the
// store's record says, whatever applock.json says, which anyone reading
// the data folder may have deleted or replaced. A lock that opened
// another way locks the app again, and it reports so.
func (a *App) adoptLockRecord() bool {
	l := &a.lock
	if !l.loaded || a.kc == nil {
		return false
	}
	rec, err := a.kc.Get(keychain.AppLock)
	if errors.Is(err, keychain.ErrNotFound) {
		return false
	}
	if err != nil {
		log.Printf("sshyard: app lock record unreadable: %v", err)
		return false
	}
	defer clear(rec)
	cfg := l.cfg
	if l.broken {
		cfg = applock.Default()
	}
	changed, err := cfg.Adopt(rec)
	if err != nil {
		log.Printf("sshyard: app lock record invalid: %v", err)
		return false
	}
	if !changed && !l.broken {
		return false
	}
	l.broken = false
	if err := a.saveLock(cfg); err != nil {
		log.Printf("sshyard: app lock not saved: %v", err)
		l.cfg = cfg
	}
	a.LockApp()
	l.password, l.code, l.hold = "", "", focusHold{}
	l.gen++
	l.err = locales.In(i18n.Locale()).AppLockRestored()
	return true
}

// ResetAppLock is the way out of a lock that can no longer be opened:
// every saved secret is deleted, then the lock, so that removing it never
// hands over what it stood in front of. Hosts and settings stay. It says
// why it could not.
func (a *App) ResetAppLock(m locales.Messages) string {
	if err := a.kc.Wipe(); err != nil {
		return m.ErrGeneric(secretStoreErrorText(m, err))
	}
	a.unlock.stored = a.unlocked()
	if err := os.Remove(a.lockPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return m.ErrGeneric(err.Error())
	}
	l := &a.lock
	l.cfg, l.broken, l.locked = applock.Default(), false, false
	l.password, l.code, l.err, l.hold = "", "", "", focusHold{}
	l.gen++
	l.input = a.lockClock()
	l.resetOpen = false
	a.closeLockForm()
	a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.AppLockResetDone()} })
	a.invalidate()
	return ""
}

// setLockIdle and setLockOnSleep change when the lock locks by itself.
func (a *App) setLockIdle(minutes int) error {
	cfg := a.lock.cfg
	cfg.IdleMinutes = minutes
	a.lock.input = a.lockClock()
	return a.saveLock(cfg)
}

func (a *App) setLockOnSleep(on bool) error {
	cfg := a.lock.cfg
	cfg.LockOnSleep = on
	return a.saveLock(cfg)
}
