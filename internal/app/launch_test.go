package app

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/Licoy/sshyard/internal/applock"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vault"
)

const launchTestPassword = "correct horse"

func saveLaunchTestLock(t *testing.T, data string) {
	t.Helper()
	v, err := applock.NewVerifier([]byte(launchTestPassword), vault.Params{Time: 1, Memory: 64, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := applock.Save(filepath.Join(data, applock.File), applock.Config{Method: applock.Password, Verifier: v}); err != nil {
		t.Fatal(err)
	}
}

func newLaunchTestApp(t *testing.T, p Platform) (*App, *dispatchPump) {
	t.Helper()
	withShells(t, session.Shell{ID: "sh", Name: "sh", Path: "/bin/sh"})
	pump := newDispatchPump()
	p.Post, p.Secrets = pump.post, keychain.NewMemory()
	p.HomeDir, p.CacheDir = p.DataDir, filepath.Join(p.DataDir, "cache")
	a, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, pump
}

func launchTabDirs(t *testing.T, a *App) []string {
	t.Helper()
	if len(a.conns) != len(a.ws.Tabs) {
		t.Fatalf("%d panes for %d tabs", len(a.conns), len(a.ws.Tabs))
	}
	var dirs []string
	for _, tab := range a.ws.Tabs {
		c := a.conns[tab.Root.Item.ID]
		if !tab.Root.Leaf() || c == nil || !isLocal(c.host.ID) || c.s == nil {
			t.Fatal("expected one local terminal in each tab")
		}
		dirs = append(dirs, c.s.Dir())
	}
	return dirs
}

func requireLaunchTabDirs(t *testing.T, a *App, want ...string) {
	t.Helper()
	if got := launchTabDirs(t, a); !slices.Equal(got, want) {
		t.Fatalf("local terminal directories = %q, want %q", got, want)
	}
}

func unlockLaunchTestApp(t *testing.T, a *App, pump *dispatchPump, password string) {
	t.Helper()
	a.lock.password = password
	if !a.SubmitAppUnlock(en) {
		t.Fatalf("unlock was not submitted: %q", a.lock.err)
	}
	waitForLaunchTestUnlock(t, a, pump)
}

func waitForLaunchTestUnlock(t *testing.T, a *App, pump *dispatchPump) {
	t.Helper()
	for a.lock.busy {
		pump.next(t)
	}
}

func TestEarlyLaunchDirsSuppressDefaultLocalTerminal(t *testing.T) {
	data := t.TempDir()
	s := settings.Default()
	s.OpenLocalOnStart = true
	if err := settings.Save(filepath.Join(data, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	a, pump := newLaunchTestApp(t, Platform{DataDir: data})
	first, second := t.TempDir(), t.TempDir()
	a.SetLaunchDir(first)
	a.SetLaunchDir(second)
	a.Start()
	pump.next(t)
	requireLaunchTabDirs(t, a, first, second)
}

func TestLaunchDirsWaitForAppUnlock(t *testing.T) {
	data := t.TempDir()
	saveLaunchTestLock(t, data)
	s := settings.Default()
	s.OpenLocalOnStart = true
	if err := settings.Save(filepath.Join(data, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	a, pump := newLaunchTestApp(t, Platform{DataDir: data})
	first, second, third, fourth := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	a.SetLaunchDir(first)
	pump.next(t)
	requireLaunchTabDirs(t, a)

	a.Start()
	a.SetLaunchDir(second)
	pump.next(t)
	if !a.appLocked() {
		t.Fatal("saved lock did not lock at startup")
	}
	requireLaunchTabDirs(t, a)

	unlockLaunchTestApp(t, a, pump, "wrong horse")
	if !a.appLocked() || a.lock.err == "" {
		t.Fatal("wrong password did not fail the unlock")
	}
	requireLaunchTabDirs(t, a)
	unlockLaunchTestApp(t, a, pump, launchTestPassword)
	if a.appLocked() {
		t.Fatalf("unlock failed: %q", a.lock.err)
	}
	requireLaunchTabDirs(t, a, first, second)

	a.LockApp()
	a.SetLaunchDir(third)
	a.SetLaunchDir(fourth)
	pump.next(t)
	requireLaunchTabDirs(t, a, first, second)
	a.appUnlockDone(en, true, nil, "", a.lock.gen-1)
	if !a.appLocked() {
		t.Fatal("outdated unlock opened the app")
	}
	requireLaunchTabDirs(t, a, first, second)
	unlockLaunchTestApp(t, a, pump, launchTestPassword)
	requireLaunchTabDirs(t, a, first, second, third, fourth)
}

func TestLaunchDirsStayQueuedDuringAppLockReset(t *testing.T) {
	data := t.TempDir()
	saveLaunchTestLock(t, data)
	a, pump := newLaunchTestApp(t, Platform{DataDir: data})
	a.SetLaunchDir(t.TempDir())
	a.Start()
	pump.next(t)
	a.lock.password = launchTestPassword
	if !a.SubmitAppUnlock(en) {
		t.Fatal("unlock was not submitted")
	}
	if msg := a.ResetAppLock(en); msg != "" {
		t.Fatal(msg)
	}
	waitForLaunchTestUnlock(t, a, pump)
	if !a.appLocked() || a.SubmitAppUnlock(en) {
		t.Fatal("reset allowed an unlock before the restart")
	}
	a.SetLaunchDir(t.TempDir())
	pump.next(t)
	requireLaunchTabDirs(t, a)
}

func TestLaunchDirsPreserveLayoutAcrossConcurrentStartup(t *testing.T) {
	data := t.TempDir()
	s := settings.Default()
	s.OpenLocalOnStart, s.RememberLayout = true, true
	if err := settings.Save(filepath.Join(data, "settings.json"), s); err != nil {
		t.Fatal(err)
	}
	const savedLayout = `{"version":1,"tabs":[{"title":"Restored","focus":1,"root":{"id":1,"kind":"terminal","host":"local:sh"}}]}`
	if err := os.WriteFile(filepath.Join(data, "layout.json"), []byte(savedLayout), 0o600); err != nil {
		t.Fatal(err)
	}

	var callers sync.WaitGroup
	starting := make(chan struct{})
	a, pump := newLaunchTestApp(t, Platform{DataDir: data, WatchSleep: func(func()) func() {
		// startIndependent has started but has yet to restore the layout.
		close(starting)
		callers.Wait()
		return func() {}
	}})
	first, last := t.TempDir(), t.TempDir()
	a.SetLaunchDir(first)
	pump.next(t)
	requireLaunchTabDirs(t, a)

	const concurrent = 8
	dirs := make([]string, 0, concurrent)
	for range concurrent {
		dir := t.TempDir()
		dirs = append(dirs, dir)
		callers.Go(func() {
			<-starting
			a.SetLaunchDir(dir)
		})
	}
	a.Start()
	requireLaunchTabDirs(t, a, "")
	a.SetLaunchDir(last)
	pump.next(t)
	got := launchTabDirs(t, a)
	if len(got) != concurrent+3 || got[0] != "" || got[1] != first || got[len(got)-1] != last || a.ws.Tabs[0].Title != "Restored" {
		t.Fatalf("restoration and sequential launches: directories %q, %d tabs", got, len(a.ws.Tabs))
	}
	// Unordered concurrent calls must each open exactly one terminal.
	expected := slices.Sorted(slices.Values(dirs))
	actual := slices.Sorted(slices.Values(got[2 : len(got)-1]))
	if !slices.Equal(actual, expected) {
		t.Fatalf("concurrent directories = %q, want %q", actual, expected)
	}
}
