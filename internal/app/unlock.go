package app

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/Licoy/sshyard/internal/keychain"
)

// Authorization runs off the UI thread; completed actions return through post.
type unlockState struct {
	mu              sync.Mutex
	closed          bool
	ready           atomic.Bool
	secretsReady    atomic.Bool
	busy            bool
	probing         bool
	completed       bool
	err             error
	closeErr        error
	modal           bool
	hold            focusHold
	after           func()
	servicesStarted bool
	started         bool
	stored          bool
}

type lockedSecrets struct{}

func (lockedSecrets) Get(string) ([]byte, error) { return nil, keychain.ErrLocked }
func (lockedSecrets) Set(string, []byte) error   { return keychain.ErrLocked }
func (lockedSecrets) Delete(string) error        { return keychain.ErrLocked }

func (a *App) ready() bool    { return a.unlock.ready.Load() }
func (a *App) unlocked() bool { return a.ready() && a.unlock.secretsReady.Load() }

// withSecrets is only called by explicit user actions, never while rendering.
func (a *App) withSecrets(after func()) {
	if a.unlocked() {
		if after != nil {
			after()
		}
		return
	}
	if a.unlock.busy {
		return
	}
	a.unlock.after = after
	a.retryUnlock()
}

func (a *App) prepareStorage() error {
	if err := a.resolveStorage(); err != nil {
		return err
	}
	kc, err := a.openSecrets()
	if err != nil {
		return err
	}
	if err := a.loadData(); err != nil {
		a.closeSecrets(kc)
		return &keychain.OpError{Op: "file-read", Err: err}
	}
	a.loadAppLock()
	_, statErr := os.Stat(filepath.Join(a.p.DataDir, "secrets.enc"))
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		a.closeSecrets(kc)
		return &keychain.OpError{Op: "file-read", Err: statErr}
	}
	a.unlock.stored = statErr == nil
	a.kc = kc
	a.keychainOK = true
	a.unlock.ready.Store(true)
	a.unlock.secretsReady.Store(kc.Unlocked())
	a.syncChoices()
	a.resolveFonts(false, false, false)
	a.updateCheck = a.settings.CheckUpdates
	// The updater keeps its own choice: follow the settings.
	a.upd.SetAutomatic(a.settings.CheckUpdates)
	a.routeUpdates()
	return nil
}

func (a *App) retryUnlock() {
	a.unlock.mu.Lock()
	if a.unlock.closed || a.unlock.busy || a.unlocked() {
		a.unlock.mu.Unlock()
		return
	}
	if a.ready() && a.kc.PasswordProtected() {
		a.unlock.mu.Unlock()
		a.openMasterPassword(masterPwUnlock)
		return
	}
	a.unlock.busy = true
	a.unlock.completed = false
	a.unlock.err = nil
	a.unlock.modal = false
	a.unlock.mu.Unlock()
	if !a.ready() {
		if err := a.prepareStorage(); err != nil {
			a.acceptUnlock(err)
			return
		}
		a.applyWindowSettings()
		a.startIndependent()
		a.unlock.mu.Lock()
		after := a.unlock.after
		a.unlock.after = nil
		a.unlock.busy = false
		a.unlock.mu.Unlock()
		if after != nil {
			a.withSecrets(after)
		}
		return
	}
	kc := a.kc
	go func() {
		err := kc.Unlock()
		a.unlock.mu.Lock()
		if a.unlock.closed {
			a.closeSecrets(kc)
			a.unlock.mu.Unlock()
			return
		}
		a.unlock.completed = true
		a.post(func() { a.acceptUnlock(err) })
		a.unlock.mu.Unlock()
	}()
}

// probeUnlock unlocks at startup only when the system keychain needs no
// authorization, as after the user always allowed SSHYard; otherwise the
// store stays locked for an explicit unlock, and nothing is shown.
func (a *App) probeUnlock() {
	if !a.ready() || a.unlocked() || !a.unlock.stored {
		return
	}
	kc := a.kc
	a.unlock.probing = true
	go func() {
		err := kc.TryUnlock()
		a.post(func() {
			a.unlock.probing = false
			if err == nil && !a.unlock.busy {
				a.acceptUnlock(nil)
			}
		})
	}()
}

func (a *App) openSecrets() (*keychain.Keychain, error) {
	lock := a.p.SecretLockPath
	if lock == "" {
		lock = filepath.Join(a.p.DataDir, "secrets.lock")
	}
	scope := a.p.SecretScope
	if scope == "" {
		scope = "sshyard"
	}
	return keychain.OpenLocked(keychain.OpenOptions{Path: filepath.Join(a.p.DataDir, "secrets.enc"), LockPath: lock, Scope: scope, System: a.p.Secrets, KDF: a.kdf})
}

func (a *App) acceptUnlock(err error) {
	a.unlock.mu.Lock()
	if a.unlock.closed {
		a.unlock.mu.Unlock()
		return
	}
	a.unlock.busy = false
	if err != nil {
		a.unlock.err, a.unlock.modal = err, true
		a.unlock.mu.Unlock()
		return
	}
	a.unlock.secretsReady.Store(true)
	after := a.unlock.after
	a.unlock.after = nil
	a.unlock.mu.Unlock()
	a.startRestoredConnections()
	if !a.unlock.servicesStarted {
		a.unlock.servicesStarted = true
		a.autoStartForwards()
		a.syncMCP()
		a.startAutoBackup()
	}
	if after != nil {
		after()
	}
}

// StoragePaths keeps the canonical lifetime lock separate from overridden data.
type StoragePaths struct{ DataDir, LockPath, Scope string }

func (a *App) resolveStorage() error {
	if a.p.ResolveStorage != nil {
		paths, err := a.p.ResolveStorage()
		if err != nil {
			return &keychain.OpError{Op: "file-read", Err: err}
		}
		a.p.DataDir, a.p.SecretLockPath, a.p.SecretScope = paths.DataDir, paths.LockPath, paths.Scope
	} else if a.p.StartupError != nil {
		return &keychain.OpError{Op: "file-read", Err: a.p.StartupError}
	}
	if a.p.DataDir == "" {
		return &keychain.OpError{Op: "file-read", Err: errors.New("data directory unavailable")}
	}
	return nil
}

// Cleanup errors remain observable without exposing system diagnostic strings.
// Callers hold unlock.mu, including the worker that receives a late result.
func (a *App) closeSecrets(kc *keychain.Keychain) {
	if err := kc.Close(); err != nil {
		a.unlock.closeErr = err
		code := "unknown"
		var system *keychain.SystemError
		if errors.As(err, &system) {
			code = strconv.Itoa(system.Code)
		}
		log.Printf("sshyard: credential store cleanup failed (stage=close, code=%s)", code)
	}
}

// A compact status appears only when saved data or pending services need it.
func (a *App) showSecretsStatus() bool {
	if a.unlocked() || a.unlock.probing && !a.unlock.busy {
		return false
	}
	if a.unlock.stored || a.unlock.busy || a.unlock.err != nil || a.settings.MCPEnabled {
		return true
	}
	for _, f := range a.store.Forwards() {
		if f.AutoStart {
			return true
		}
	}
	return false
}
