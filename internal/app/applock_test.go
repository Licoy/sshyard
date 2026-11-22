package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/applock"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/vault"
	"github.com/egoist/mygo/ui"
)

// lockTestApp is a test app whose lock derives cheaply and whose clock
// the test moves.
func lockTestApp(t *testing.T) (*App, *ui.Tester, *time.Time) {
	t.Helper()
	a, tt := newTestApp(t)
	a.kdf = vault.Params{Time: 1, Memory: 64, Threads: 1}
	now := time.Unix(1_700_000_000, 0)
	a.lock.now = func() time.Time { return now }
	a.lock.input = now
	return a, tt, &now
}

// setUpLock turns the lock on with method through the sheet: password
// for a method asking for one, and the code of the new authenticator,
// whose secret it returns, for one asking for codes.
func setUpLock(t *testing.T, a *App, tt *ui.Tester, method applock.Method, password string, now time.Time) []byte {
	t.Helper()
	a.openLockForm(lockEnable)
	f := a.lock.form
	f.method = slices.Index(applock.Methods, method)
	f.password, f.confirm = password, password
	a.ensureSecret(f)
	if method.NeedsCode() {
		f.code = applock.Code(f.secret, applock.Step(now))
	}
	secret := bytes.Clone(f.secret)
	if !a.SubmitLockForm(en) {
		t.Fatalf("lock not set up: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "the lock set up", func() bool { return a.lock.form == nil })
	if a.lock.cfg.Method != method {
		t.Fatalf("method %q, want %q", a.lock.cfg.Method, method)
	}
	return secret
}

// unlockWith tries the lock screen with password and code, and waits for
// the answer.
func unlockWith(t *testing.T, a *App, tt *ui.Tester, password, code string) {
	t.Helper()
	a.lock.password, a.lock.code = password, code
	if !a.SubmitAppUnlock(en) {
		t.Fatalf("unlock not tried: %q", a.lock.err)
	}
	waitForIn(t, tt, slowWait, "the unlock checked", func() bool { return !a.lock.busy })
}

// TestAppLockPassword: the password's verifier is kept, never the
// password; a wrong one keeps the app locked and is counted on disk, the
// right one opens it and forgets the count.
func TestAppLockPassword(t *testing.T) {
	a, tt, _ := lockTestApp(t)
	if a.appLocked() {
		t.Fatal("locked with no lock set up")
	}
	a.openLockForm(lockEnable)
	f := a.lock.form
	f.password, f.confirm = "short", "short"
	if a.SubmitLockForm(en) || f.err == "" {
		t.Fatal("short password accepted")
	}
	f.password, f.confirm = "correct horse", "battery staple"
	if a.SubmitLockForm(en) || f.err == "" {
		t.Fatal("mismatched password accepted")
	}
	a.closeLockForm()
	setUpLock(t, a, tt, applock.Password, "correct horse", time.Time{})

	b, err := os.ReadFile(filepath.Join(a.p.DataDir, applock.File))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("correct horse")) {
		t.Fatal("password written to applock.json")
	}
	if a.appLocked() {
		t.Fatal("setting the lock up locked the app")
	}

	a.LockApp()
	if !a.appLocked() {
		t.Fatal("not locked")
	}
	unlockWith(t, a, tt, "wrong horse", "")
	if !a.appLocked() || a.lock.err == "" {
		t.Fatal("wrong password opened the lock")
	}
	saved, err := applock.Load(filepath.Join(a.p.DataDir, applock.File))
	if err != nil || saved.Failures != 1 {
		t.Fatalf("failure not saved: %+v %v", saved, err)
	}
	unlockWith(t, a, tt, "correct horse", "")
	if a.appLocked() {
		t.Fatalf("right password refused: %q", a.lock.err)
	}
	if saved, _ := applock.Load(filepath.Join(a.p.DataDir, applock.File)); saved.Failures != 0 {
		t.Fatalf("failures kept after unlock: %d", saved.Failures)
	}
}

// TestAppLockThrottle: past the free attempts, no attempt is made until
// the wait passed, and the password is not even checked.
func TestAppLockThrottle(t *testing.T) {
	a, tt, now := lockTestApp(t)
	setUpLock(t, a, tt, applock.Password, "correct horse", *now)
	a.LockApp()
	for range 6 {
		unlockWith(t, a, tt, "wrong horse", "")
	}
	a.lock.password = "correct horse"
	if a.SubmitAppUnlock(en) || a.lock.err == "" {
		t.Fatal("attempt made while it must wait")
	}
	*now = now.Add(time.Minute)
	unlockWith(t, a, tt, "correct horse", "")
	if a.appLocked() {
		t.Fatalf("not unlocked after the wait: %q", a.lock.err)
	}
}

// TestAppLockTOTP: the new authenticator's secret goes to the encrypted
// store, never to applock.json; a code opens the lock once, and the next
// code opens it again.
func TestAppLockTOTP(t *testing.T) {
	a, tt, now := lockTestApp(t)
	a.openLockForm(lockEnable)
	f := a.lock.form
	f.method = slices.Index(applock.Methods, applock.TOTP)
	a.ensureSecret(f)
	f.code = applock.Code(f.secret, applock.Step(*now)+5)
	if a.SubmitLockForm(en) || f.err == "" {
		t.Fatal("a code far off bound the authenticator")
	}
	a.closeLockForm()
	if a.kc.Has(keychain.AppLockTOTP) {
		t.Fatal("a cancelled setup kept its secret")
	}
	secret := setUpLock(t, a, tt, applock.TOTP, "", *now)
	stored, err := a.kc.Get(keychain.AppLockTOTP)
	if err != nil || !bytes.Equal(stored, secret) {
		t.Fatalf("secret not in the store: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(a.p.DataDir, applock.File))
	if bytes.Contains(b, secret) || bytes.Contains(b, []byte(applock.EncodeSecret(secret))) {
		t.Fatal("secret written to applock.json")
	}

	a.LockApp()
	// The code that bound the authenticator was taken already.
	unlockWith(t, a, tt, "", applock.Code(secret, applock.Step(*now)))
	if !a.appLocked() {
		t.Fatal("a code was taken twice")
	}
	*now = now.Add(30 * time.Second)
	unlockWith(t, a, tt, "", applock.Code(secret, applock.Step(*now)))
	if a.appLocked() {
		t.Fatalf("the next code refused: %q", a.lock.err)
	}
}

// TestAppLockBoth: both the password and the code must be right.
func TestAppLockBoth(t *testing.T) {
	a, tt, now := lockTestApp(t)
	secret := setUpLock(t, a, tt, applock.Both, "correct horse", *now)
	a.LockApp()
	*now = now.Add(30 * time.Second)
	code := applock.Code(secret, applock.Step(*now))
	unlockWith(t, a, tt, "wrong horse", code)
	if !a.appLocked() {
		t.Fatal("opened with a wrong password")
	}
	*now = now.Add(30 * time.Second)
	unlockWith(t, a, tt, "correct horse", applock.Code(secret, applock.Step(*now)+5))
	if !a.appLocked() {
		t.Fatal("opened with a wrong code")
	}
	*now = now.Add(30 * time.Second)
	unlockWith(t, a, tt, "correct horse", applock.Code(secret, applock.Step(*now)))
	if a.appLocked() {
		t.Fatalf("right password and code refused: %q", a.lock.err)
	}
}

// TestAppLockChangeAndDisable: changing or removing the lock asks for it
// as it is; removing a method with codes forgets their secret, and a
// change keeping codes keeps the authenticator.
func TestAppLockChangeAndDisable(t *testing.T) {
	a, tt, now := lockTestApp(t)
	secret := setUpLock(t, a, tt, applock.TOTP, "", *now)

	a.openLockForm(lockChange)
	f := a.lock.form
	f.method = slices.Index(applock.Methods, applock.Both)
	f.password, f.confirm = "correct horse", "correct horse"
	*now = now.Add(30 * time.Second)
	f.curCode = applock.Code(secret, applock.Step(*now)-5)
	if !a.SubmitLockForm(en) {
		t.Fatalf("change not tried: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "the change checked", func() bool { return !f.busy })
	if a.lock.cfg.Method != applock.TOTP || f.err == "" {
		t.Fatal("changed with a stale code")
	}
	f.curCode = applock.Code(secret, applock.Step(*now))
	f.password, f.confirm = "correct horse", "correct horse"
	if !a.SubmitLockForm(en) {
		t.Fatalf("change not tried: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "the lock changed", func() bool { return a.lock.form == nil })
	if a.lock.cfg.Method != applock.Both {
		t.Fatalf("method %q", a.lock.cfg.Method)
	}
	if rec, _ := a.kc.Get(keychain.AppLock); !bytes.Equal(rec, a.lock.cfg.Record()) {
		t.Fatal("the store's record is not the changed lock")
	}
	if stored, _ := a.kc.Get(keychain.AppLockTOTP); !bytes.Equal(stored, secret) {
		t.Fatal("the change replaced the authenticator")
	}

	a.openLockForm(lockDisable)
	f = a.lock.form
	*now = now.Add(30 * time.Second)
	f.curPassword, f.curCode = "correct horse", applock.Code(secret, applock.Step(*now))
	if !a.SubmitLockForm(en) {
		t.Fatalf("removal not tried: %q", f.err)
	}
	waitForIn(t, tt, slowWait, "the lock removed", func() bool { return a.lock.form == nil })
	if a.lock.cfg.Enabled() || a.kc.Has(keychain.AppLockTOTP) || a.kc.Has(keychain.AppLock) {
		t.Fatalf("lock left: %+v, secret %v, record %v", a.lock.cfg, a.kc.Has(keychain.AppLockTOTP), a.kc.Has(keychain.AppLock))
	}
	if saved, _ := applock.Load(filepath.Join(a.p.DataDir, applock.File)); saved.Enabled() || saved.Verifier != nil {
		t.Fatalf("saved lock: %+v", saved)
	}
}

// TestAppLockRollback: a lock that cannot be saved puts the store back.
func TestAppLockRollback(t *testing.T) {
	a, _, _ := lockTestApp(t)
	if err := os.Mkdir(filepath.Join(a.p.DataDir, applock.File), 0o700); err != nil {
		t.Fatal(err)
	}
	a.openLockForm(lockEnable)
	f := a.lock.form
	f.method = slices.Index(applock.Methods, applock.TOTP)
	a.ensureSecret(f)
	next := a.lock.cfg
	next.Method = applock.TOTP
	if msg := a.commitLock(en, applock.Off, next, f.secret); msg == "" {
		t.Fatal("saved over a directory")
	}
	if a.kc.Has(keychain.AppLockTOTP) || a.kc.Has(keychain.AppLock) || a.lock.cfg.Enabled() {
		t.Fatal("secret, record or lock kept after the save failed")
	}
}

// TestAppLockAtStart: a lock set up, or one that cannot be read, locks
// the app as it starts.
func TestAppLockAtStart(t *testing.T) {
	dir := t.TempDir()
	v, err := applock.NewVerifier([]byte("correct horse"), vault.Params{Time: 1, Memory: 64, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := applock.Save(filepath.Join(dir, applock.File), applock.Config{Method: applock.Password, Verifier: v}); err != nil {
		t.Fatal(err)
	}
	a, _ := newTestAppIn(t, dir)
	if !a.appLocked() || a.lock.broken {
		t.Fatal("not locked at start")
	}

	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, applock.File), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ = newTestAppIn(t, dir)
	if !a.appLocked() || !a.lock.broken {
		t.Fatal("a damaged lock left the app open")
	}
	a.lock.password = "anything"
	if a.SubmitAppUnlock(en) {
		t.Fatal("a damaged lock tried an unlock")
	}
}

// TestAppLockIdleAndSleep: no input for the minutes set locks the app,
// input puts it off; the screen lock locks it while that is on.
func TestAppLockIdleAndSleep(t *testing.T) {
	a, tt, now := lockTestApp(t)
	setUpLock(t, a, tt, applock.Password, "correct horse", *now)
	if err := a.setLockIdle(5); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(4 * time.Minute)
	if left := a.checkIdle(); left != time.Minute || a.appLocked() {
		t.Fatalf("locked early, %v left", left)
	}
	a.noteInput(ui.InputEvent{})
	*now = now.Add(4 * time.Minute)
	if a.checkIdle(); a.appLocked() {
		t.Fatal("input did not put the lock off")
	}
	*now = now.Add(time.Minute)
	if a.checkIdle(); !a.appLocked() {
		t.Fatal("not locked after the minutes set")
	}

	a.lock.locked = false
	if err := a.setLockIdle(0); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	if a.checkIdle(); a.appLocked() {
		t.Fatal("locked with idle locking off")
	}

	var sleep func()
	a.p.WatchSleep = func(fn func()) func() { sleep = fn; return func() {} }
	a.watchSleep()
	if err := a.setLockOnSleep(false); err != nil {
		t.Fatal(err)
	}
	sleep()
	tt.Frame()
	if a.appLocked() {
		t.Fatal("locked on sleep while that is off")
	}
	if err := a.setLockOnSleep(true); err != nil {
		t.Fatal(err)
	}
	sleep()
	waitFor(t, tt, "locked on sleep", a.appLocked)
}

// TestAppLockGatesActions: while locked, the tray and the shortcuts run
// nothing.
func TestAppLockGatesActions(t *testing.T) {
	a, tt, now := lockTestApp(t)
	setUpLock(t, a, tt, applock.Password, "correct horse", *now)
	a.runAction(actLock)
	if !a.appLocked() {
		t.Fatal("the lock action did not lock")
	}
	tabs := len(a.ws.Tabs)
	a.RunTrayAction(TrayLocalTerminal)
	a.RunTrayAction(TrayNewHost)
	if len(a.ws.Tabs) != tabs || a.form != nil {
		t.Fatal("a tray action ran while locked")
	}
	h, err := a.store.Add(assets.Host{Name: "locked", Address: "127.0.0.1", User: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ConnectFromTray(h.ID); err != nil || len(a.conns) != 0 {
		t.Fatalf("connected from the tray while locked: %v", err)
	}
	for _, act := range []TrayAction{TrayQuickConnect, TrayLocalTerminal, TrayNewHost, TraySettings} {
		if !trayDisabled(a, act) {
			t.Errorf("%s enabled while locked", act)
		}
	}
	if trayDisabled(a, TrayOpenMain) {
		t.Error("the main window cannot open while locked")
	}
}

// TestAppLockNotInBackup: the codes' secret never goes into a backup.
func TestAppLockNotInBackup(t *testing.T) {
	a, tt, now := lockTestApp(t)
	setUpLock(t, a, tt, applock.TOTP, "", *now)
	p, err := a.collectBackup(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{keychain.AppLockTOTP, keychain.AppLock} {
		if _, ok := p.Secrets[key]; ok {
			t.Fatalf("the app lock's %s is in the backup", key)
		}
	}
}

// reopenLockApp quits a and starts SSHYard again on its data and its
// system keychain, as after a restart.
func reopenLockApp(t *testing.T, a *App) (*App, *ui.Tester) {
	t.Helper()
	dir, sys := a.p.DataDir, a.p.Secrets
	a.Close()
	b, err := New(Platform{DataDir: dir, Secrets: sys, HomeDir: dir, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	b.kdf = a.kdf
	b.lock.now = a.lock.now
	b.Start()
	b.withSecrets(nil)
	tt := ui.NewTester(b.View, 1200, 760)
	tt.SetLocale("en")
	tt.Frame()
	waitFor(t, tt, "credential store unlocked", b.unlocked)
	return b, tt
}

// TestAppLockRecordWins: applock.json deleted, replaced with a lock whose
// password someone else knows, or damaged, is put back from the store's
// record as the store unlocks, and the app locks again.
func TestAppLockRecordWins(t *testing.T) {
	a, tt, now := lockTestApp(t)
	setUpLock(t, a, tt, applock.Password, "correct horse", *now)
	path := filepath.Join(a.p.DataDir, applock.File)
	mine := a.lock.cfg.Verifier
	forged, err := applock.NewVerifier([]byte("intruder pass"), a.kdf)
	if err != nil {
		t.Fatal(err)
	}
	for name, tamper := range map[string]func() error{
		"deleted":  func() error { return os.Remove(path) },
		"replaced": func() error { return applock.Save(path, applock.Config{Method: applock.Password, Verifier: forged}) },
		"damaged":  func() error { return os.WriteFile(path, []byte("{"), 0o600) },
	} {
		if err := tamper(); err != nil {
			t.Fatal(err)
		}
		a, tt = reopenLockApp(t, a)
		if !a.appLocked() || a.lock.broken || !bytes.Equal(a.lock.cfg.Verifier, mine) || a.lock.err == "" {
			t.Fatalf("%s: not restored: locked %v, broken %v, %+v", name, a.appLocked(), a.lock.broken, a.lock.cfg)
		}
		if saved, err := applock.Load(path); err != nil || !bytes.Equal(saved.Verifier, mine) {
			t.Fatalf("%s: file not restored: %v", name, err)
		}
		unlockWith(t, a, tt, "intruder pass", "")
		if !a.appLocked() {
			t.Fatalf("%s: the forged password opened the lock", name)
		}
		unlockWith(t, a, tt, "correct horse", "")
		if a.appLocked() {
			t.Fatalf("%s: own password refused: %q", name, a.lock.err)
		}
	}

	// A check under way against a forged lock opens nothing once the
	// record took its place.
	a.lock.cfg.Verifier = forged
	a.lock.locked = true
	a.lock.password = "intruder pass"
	if !a.SubmitAppUnlock(en) {
		t.Fatal("unlock not tried")
	}
	if !a.adoptLockRecord() {
		t.Fatal("the forged lock was not replaced")
	}
	waitForIn(t, tt, slowWait, "the unlock checked", func() bool { return !a.lock.busy })
	if !a.appLocked() {
		t.Fatal("a check against the forged lock opened the app")
	}
}

// TestAppLockReset: a reset deletes every saved secret, then the lock,
// keeps the lock screen and restarts the app, which then deletes the rest
// of the data: no host is left to see.
func TestAppLockReset(t *testing.T) {
	a, tt, now := lockTestApp(t)
	relaunched := 0
	a.p.Relaunch = func() { relaunched++ }
	h, err := a.store.Add(assets.Host{Name: "gone", Address: "127.0.0.1", User: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.kc.Set(keychain.Host(h.ID, keychain.Password), []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	setUpLock(t, a, tt, applock.Both, "correct horse", *now)
	a.LockApp()
	dir := a.p.DataDir
	for _, name := range []string{"updater.json", "window-state.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "ai-history", "h1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("hosts"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte("gone.example ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := a.ResetAppLock(en); msg != "" {
		t.Fatal(msg)
	}
	if !a.appLocked() || !a.lock.resetting || a.lock.cfg.Enabled() {
		t.Fatal("the lock screen went before the restart")
	}
	if relaunched != 1 {
		t.Fatalf("relaunched %d times, want 1", relaunched)
	}
	for _, key := range []string{keychain.Host(h.ID, keychain.Password), keychain.AppLock, keychain.AppLockTOTP} {
		if a.kc.Has(key) {
			t.Fatalf("%s kept after the reset", key)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, applock.File)); !os.IsNotExist(err) {
		t.Fatalf("applock.json kept: %v", err)
	}
	a, _ = reopenLockApp(t, a)
	if a.appLocked() {
		t.Fatal("locked again after a restart")
	}
	if len(a.store.List()) != 0 {
		t.Fatal("a host was kept after the reset")
	}
	for _, name := range []string{resetFile, ".tmp-123", "ai-history"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s kept after the reset: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "known_hosts")); bytes.Contains(b, []byte("gone.example")) {
		t.Fatal("known_hosts kept after the reset")
	}
	for _, name := range []string{"updater.json", "window-state.json", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s deleted by the reset: %v", name, err)
		}
	}
}

// TestFinishResetOnlyWhenAsked: a data folder no reset asked to empty is
// left as it is.
func TestFinishResetOnlyWhenAsked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if done, err := finishReset(dir); done || err != nil {
		t.Fatalf("finishReset = %v, %v without a request", done, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("hosts.json deleted: %v", err)
	}
}

// TestAppLockResetClosesDirtyEditors: an editor with unsaved changes asks
// before its window closes, but not while a reset restarts the app.
func TestAppLockResetClosesDirtyEditors(t *testing.T) {
	a, _, _ := lockTestApp(t)
	cn := &conn{ed: &edWin{}}
	cn.ed.dirty.Store(true)
	if a.editorClosing(cn) {
		t.Fatal("a dirty editor closed without asking")
	}
	a.lock.resetting = true
	if !a.editorClosing(cn) {
		t.Fatal("a dirty editor held up the restart of a reset")
	}
}
