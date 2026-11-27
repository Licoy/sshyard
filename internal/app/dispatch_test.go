package app

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/egoist/mygo/ui"
)

// The test goroutine is the UI thread; no View frames are rendered.
type dispatchPump struct{ work chan func() }

func newDispatchPump() *dispatchPump   { return &dispatchPump{work: make(chan func(), 16)} }
func (p *dispatchPump) post(fn func()) { p.work <- fn }
func (p *dispatchPump) next(t *testing.T) {
	t.Helper()
	select {
	case fn := <-p.work:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("UI callback was not scheduled")
	}
}

func TestPostDispatchesWithoutFrames(t *testing.T) {
	p := newDispatchPump()
	a := &App{winState: testWin(), p: Platform{Post: p.post}}
	var got []int
	a.post(func() {
		got = append(got, 1)
		a.post(func() { got = append(got, 3) })
	})
	a.post(func() { got = append(got, 2) })
	if len(got) != 0 || len(p.work) != 1 {
		t.Fatalf("callbacks ran inline or wakeups were not batched: %v, %d", got, len(p.work))
	}
	p.next(t)
	p.next(t)
	if !slices.Equal(got, []int{1, 2, 3}) || len(p.work) != 0 {
		t.Fatalf("FIFO/exactly once: %v, pending %d", got, len(p.work))
	}
	a.post(func() { got = append(got, 4) })
	p.next(t)
	if !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Fatalf("subsequent batch: %v", got)
	}
}

func TestPostCloseDropsPendingAndLateCallbacks(t *testing.T) {
	p := newDispatchPump()
	dir := t.TempDir()
	a, err := New(Platform{Post: p.post, DataDir: dir, Secrets: keychain.NewMemory(), HomeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	a.post(func() { ran = true })
	a.Close()
	p.next(t)
	a.post(func() { ran = true })
	if ran || len(p.work) != 0 {
		t.Fatalf("closed app ran work or scheduled more: %v, %d", ran, len(p.work))
	}
}

func TestSilentUnlockDispatchesWithoutFrames(t *testing.T) {
	for _, closeBeforeAccept := range []bool{false, true} {
		t.Run(map[bool]string{false: "hidden", true: "closed"}[closeBeforeAccept], func(t *testing.T) {
			dir := t.TempDir()
			b := &silentBackend{authorizationBackend: authorizationBackend{Memory: keychain.NewMemory()}}
			b.allowed.Store(true)
			kc, err := keychain.Open(keychain.OpenOptions{Path: filepath.Join(dir, "secrets.enc"), LockPath: filepath.Join(dir, "secrets.lock"), Scope: "sshyard", System: b.Memory})
			if err != nil {
				t.Fatal(err)
			}
			if err := kc.Set("secret", []byte("value")); err != nil {
				t.Fatal(err)
			}
			if err := kc.Close(); err != nil {
				t.Fatal(err)
			}
			p := newDispatchPump()
			a, err := New(Platform{Post: p.post, DataDir: dir, Secrets: b, HomeDir: dir, CacheDir: filepath.Join(dir, "cache")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Close)
			a.settings.MCPEnabled, a.settings.MCPPort = true, 0
			// Start's silent probe must complete even while the window is hidden.
			a.probeUnlock()
			var completion func()
			select {
			case completion = <-p.work:
			case <-time.After(5 * time.Second):
				t.Fatal("silent unlock completion was not scheduled")
			}
			if closeBeforeAccept {
				a.Close()
			}
			completion()
			if closeBeforeAccept {
				if a.mcp.srv != nil || a.unlocked() || a.unlock.servicesStarted {
					t.Fatal("late unlock revived closed services")
				}
			} else if !a.unlocked() || a.mcp.srv == nil || !a.unlock.servicesStarted {
				t.Fatalf("hidden startup: unlocked=%v MCP=%v err=%v", a.unlocked(), a.mcp.srv != nil, a.mcp.err)
			}
			if b.calls.Load() != 0 || b.silent.Load() != 1 {
				t.Fatalf("startup authorization=%d silent=%d", b.calls.Load(), b.silent.Load())
			}
		})
	}
}

func TestPostClipboardClockBetweenFrames(t *testing.T) {
	p := newDispatchPump()
	dir := t.TempDir()
	a, err := New(Platform{Post: p.post, DataDir: dir, Secrets: keychain.NewMemory(), HomeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	tt := ui.NewTester(a.View, 1200, 760)
	tt.Frame()
	before := time.Now()
	var now time.Time
	a.post(func() { now = a.clip.clock() })
	p.next(t)
	if now.Before(before) || now.After(time.Now()) {
		t.Fatalf("clipboard callback used a stale frame clock: %v", now)
	}
}
