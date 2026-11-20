// Package applock is the lock in front of SSHYard's windows: what opens
// it (a password, a time-based one-time code of an authenticator app, or
// both), when it locks by itself, and how long wrong attempts make the
// next one wait.
//
// It keeps the host list and the sessions from whoever sits down at an
// unlocked computer; it does not encrypt the metadata on disk, which
// anyone reading the data folder reads anyway. Its file, applock.json,
// stays on this computer: no backup takes it, and deleting it removes the
// lock. The password itself is never stored, only a vault-sealed check
// that opens with it; the one-time code's secret is a secret of the
// encrypted local store (keychain.AppLockTOTP).
package applock

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vault"
)

// File is the name of the lock's file in the data folder.
const File = "applock.json"

// Method is what opens the lock.
type Method string

const (
	// Off is no lock.
	Off      Method = ""
	Password Method = "password"
	TOTP     Method = "totp"
	// Both asks for the password and a one-time code.
	Both Method = "both"
)

// Methods are the methods to choose from, in the order they are offered.
var Methods = []Method{Password, TOTP, Both}

// NeedsPassword reports whether m asks for the password.
func (m Method) NeedsPassword() bool { return m == Password || m == Both }

// NeedsCode reports whether m asks for a one-time code.
func (m Method) NeedsCode() bool { return m == TOTP || m == Both }

// IdleChoices are the minutes of inactivity the lock may wait before it
// locks, 0 for never.
var IdleChoices = []int{1, 5, 15, 30, 60, 0}

// Config is the lock as applock.json keeps it.
type Config struct {
	Method Method `json:"method,omitempty"`
	// Verifier is a vault sealed with the password, for a method asking
	// for it.
	Verifier []byte `json:"verifier,omitempty"`
	// IdleMinutes of no input in SSHYard lock it; 0 never does.
	IdleMinutes int `json:"idleMinutes"`
	// LockOnSleep locks as the screen locks or the computer sleeps.
	LockOnSleep bool `json:"lockOnSleep"`
	// LastStep is the time step of the last one-time code taken, which
	// no code may reuse.
	LastStep int64 `json:"lastStep,omitempty"`
	// Failures is how many attempts in a row failed, RetryAt when the
	// next may be made: kept so that quitting does not start them over.
	Failures int       `json:"failures,omitempty"`
	RetryAt  time.Time `json:"retryAt,omitzero"`
}

// Default is the lock before one is set up.
func Default() Config { return Config{IdleMinutes: 15, LockOnSleep: true} }

// Enabled reports whether the lock is on.
func (c Config) Enabled() bool { return c.Method != Off }

// Load reads the lock at path; a missing file is Default. A file that
// cannot be read is an error, which the caller keeps locked.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if !c.valid() {
		return Config{}, errors.New("applock: invalid configuration")
	}
	return c, nil
}

func (c Config) valid() bool {
	switch c.Method {
	case Off, Password, TOTP, Both:
	default:
		return false
	}
	return !c.Method.NeedsPassword() || vault.Check(c.Verifier) == nil
}

// Save writes the lock to path atomically.
func Save(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return settings.WriteFileAtomic(path, b, 0o600)
}

// verified is what a verifier holds.
const verified = "sshyard-applock"

// NewVerifier seals the check a password opens, deriving with p.
func NewVerifier(password []byte, p vault.Params) ([]byte, error) {
	return vault.Seal([]byte(verified), password, p)
}

// CheckPassword reports whether password opens verifier. An error is a
// verifier that cannot be opened at all.
func CheckPassword(verifier, password []byte) (bool, error) {
	b, err := vault.Open(verifier, password)
	switch {
	case errors.Is(err, vault.ErrPassword):
		return false, nil
	case err != nil:
		return false, err
	}
	return string(b) == verified, nil
}

// The wait after failed attempts: the first freeTries cost nothing, each
// one after doubles the wait from firstWait, up to maxWait.
const (
	freeTries = 5
	firstWait = 30 * time.Second
	maxWait   = 15 * time.Minute
)

// Fail counts a failed attempt at now.
func (c *Config) Fail(now time.Time) {
	c.Failures++
	if n := c.Failures - freeTries; n > 0 {
		d := maxWait
		if n <= 10 {
			d = min(firstWait<<(n-1), maxWait)
		}
		c.RetryAt = now.Add(d)
	}
}

// Succeed forgets the failed attempts.
func (c *Config) Succeed() { c.Failures, c.RetryAt = 0, time.Time{} }

// Wait is how long until the next attempt may be made at now: never more
// than maxWait, so that a clock set back does not lock the user out.
func (c Config) Wait(now time.Time) time.Duration {
	if !now.Before(c.RetryAt) {
		return 0
	}
	return min(c.RetryAt.Sub(now), maxWait)
}
