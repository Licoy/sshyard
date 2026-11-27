package app

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The backend fails at the real authorization boundary, before any secret save.
type linuxAuthorizationBackend struct {
	*keychain.Memory
	fail  atomic.Bool
	calls atomic.Int32
}

func (b *linuxAuthorizationBackend) Get(k string) ([]byte, error) {
	b.calls.Add(1)
	if b.fail.Load() {
		return nil, &keychain.LinuxServiceError{Kind: keychain.LinuxServiceSessionBus, Err: errors.New("private-token /private/keyring")}
	}
	return b.Memory.Get(k)
}

// TestLinuxUnlockModalReasons: each Linux keyring failure says why it
// failed without the raw diagnostic, and Cancel dismisses it.
func TestLinuxUnlockModalReasons(t *testing.T) {
	m := locales.In("en")
	a := &App{winState: testWin()}
	tt := ui.NewTester(func(c *ui.Context) { a.unlockModal(c, locales.In(c.Locale()), currentTokens(c)) }, 700, 500)
	tt.SetLocale("en")
	for _, tc := range linuxSecretReasons(m) {
		a.unlock.err = &keychain.OpError{Op: "master-read", Err: &keychain.LinuxServiceError{Kind: tc.kind, Err: errors.New("private-token /private/keyring")}}
		a.unlock.modal = true
		tt.Frame()
		if want := m.SecretsFailureDetail(m.SecretsStageReadMaster(), tc.reason); !tt.HasText(want) {
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
		if strings.Contains(strings.Join(tt.Texts(), " "), "private") {
			t.Fatal("raw Linux diagnostic visible")
		}
		mustClick(t, tt, m.Cancel())
		tt.Frame()
		if a.unlock.modal {
			t.Fatal("cancel did not dismiss modal")
		}
	}
}

func TestLinuxUnlockCancelRetryPreservesDraft(t *testing.T) {
	b := &linuxAuthorizationBackend{Memory: keychain.NewMemory()}
	b.fail.Store(true)
	a, tt := lockedTestApp(t, b)
	a.openEditor(nil)
	a.form.name, a.form.address, a.form.user = "draft", "draft.example", "demo"
	a.form.auth, a.form.secret.password = en.CredKindPassword(), "transient"
	// Drive the handler because TextInput owns its own frame state.
	if a.SaveForm(en) {
		t.Fatal("saved while system keyring unavailable")
	}
	waitFor(t, tt, "Linux authorization failure", func() bool { return a.unlock.modal })
	modal := ui.NewTester(func(c *ui.Context) { a.unlockModal(c, en, currentTokens(c)) }, 700, 500)
	modal.Frame()
	mustClick(t, modal, en.Cancel())
	modal.Frame()
	if a.form == nil || a.form.name != "draft" || a.form.secret.password != "transient" || a.unlock.after != nil || a.store.Len() != 0 {
		t.Fatal("cancel discarded draft or saved credentials")
	}
	calls := b.calls.Load()
	tt.Frame()
	if b.calls.Load() != calls {
		t.Fatal("cancel retried automatically")
	}
	// Start another explicit save, then use the modal's Retry to resume it.
	if a.SaveForm(en) {
		t.Fatal("saved before retry")
	}
	waitFor(t, tt, "second Linux authorization failure", func() bool { return a.unlock.modal })
	b.fail.Store(false)
	modal.Frame()
	mustClick(t, modal, en.SecretsRetry())
	waitFor(t, tt, "save after retry", func() bool { return a.store.Len() == 1 })
	if !a.unlocked() || a.unlock.modal {
		t.Fatal("retry did not unlock and save")
	}
	h := a.store.List()[0]
	if value, err := a.kc.Get(keychain.Host(h.ID, keychain.Password)); err != nil || string(value) != "transient" {
		t.Fatalf("draft secret: %q %v", value, err)
	}
}
