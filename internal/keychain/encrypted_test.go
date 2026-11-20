package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type failSystem struct {
	*Memory
	setErr        error
	readErr       error
	reads         int
	failReadAt    int
	corruptVerify bool
}

func (s *failSystem) Set(key string, v []byte) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.Memory.Set(key, v)
}
func (s *failSystem) Get(key string) ([]byte, error) {
	s.reads++
	if s.failReadAt == s.reads {
		return nil, s.readErr
	}
	if s.corruptVerify && s.reads == 2 {
		return bytes.Repeat([]byte{7}, 32), nil
	}
	return s.Memory.Get(key)
}

func TestFirstOpenFailuresAndRetry(t *testing.T) {
	for _, stage := range []string{"read", "set", "verify", "mismatch", "file"} {
		t.Run(stage, func(t *testing.T) {
			o := options(t)
			s := &failSystem{Memory: NewMemory()}
			o.System = s
			denied := &SystemError{Code: -128, Err: errors.New("authorization denied")}
			wantOp := "master-read"
			switch stage {
			case "read":
				s.failReadAt = 1
				s.readErr = denied
			case "set":
				s.setErr = denied
				wantOp = "master-write"
			case "verify":
				s.failReadAt = 2
				s.readErr = denied
				wantOp = "master-verify"
			case "mismatch":
				s.corruptVerify = true
				wantOp = "master-verify"
			case "file":
				if err := os.WriteFile(filepath.Dir(o.Path), []byte("blocked directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				wantOp = "file-read"
				if runtime.GOOS == "windows" {
					// Windows reads a path under a file as not found, so
					// the store is new and making its folder fails.
					wantOp = "file-write"
				}
			}
			got, err := Open(o)
			var oe *OpError
			if got != nil || !errors.As(err, &oe) || oe.Op != wantOp {
				t.Fatalf("failed stage: %v", err)
			}
			if stage != "mismatch" && stage != "file" && !errors.Is(err, denied) {
				t.Fatal("lost cause", err)
			}
			masterBefore, _ := s.Memory.Get(MasterKey)
			s.failReadAt = 0
			s.setErr = nil
			s.corruptVerify = false
			if stage == "file" {
				if err := os.Remove(filepath.Dir(o.Path)); err != nil {
					t.Fatal(err)
				}
			}
			k := openTest(t, o)
			mustGetMissing(t, k, "no-secret")
			masterAfter, _ := s.Memory.Get(MasterKey)
			if len(masterBefore) > 0 && !bytes.Equal(masterBefore, masterAfter) {
				t.Fatal("retry replaced existing master")
			}
		})
	}
}
func mustGetMissing(t *testing.T, k *Keychain, key string) {
	t.Helper()
	if _, err := k.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestInitialFileCommitFailureKeepsMaster(t *testing.T) {
	o := options(t)
	// A nonexistent child of an existing nondirectory passes file ENOENT on
	// Windows differently, so use a valid target that cannot be renamed over.
	if err := os.MkdirAll(o.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	// This fails file-read, without ever generating a master.
	if k, err := Open(o); err == nil || k != nil {
		t.Fatal("directory accepted")
	}
	if err := os.Remove(o.Path); err != nil {
		t.Fatal(err)
	}
	s := &failSystem{Memory: NewMemory()}
	o.System = s
	// After the master is written, replace the target parent with a file.
	o.System = &onSetSystem{Backend: s, after: func() {
		if err := os.Remove(filepath.Dir(o.Path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Dir(o.Path), []byte("block"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	if k, err := Open(o); k != nil || err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	masterBefore, _ := s.Memory.Get(MasterKey)
	if len(masterBefore) != 32 {
		t.Fatal("master was removed after file failure")
	}
	if err := os.Remove(filepath.Dir(o.Path)); err != nil {
		t.Fatal(err)
	}
	o.System = s
	openTest(t, o)
	after, _ := s.Memory.Get(MasterKey)
	if !bytes.Equal(masterBefore, after) {
		t.Fatal("master changed")
	}
}

type onSetSystem struct {
	Backend
	after func()
}

func (s *onSetSystem) Set(key string, v []byte) error {
	if err := s.Backend.Set(key, v); err != nil {
		return err
	}
	s.after()
	return nil
}

func TestWriteFailurePreservesOldValuesAndBatch(t *testing.T) {
	o := options(t)
	k := openTest(t, o)
	if err := k.Apply([]Change{{Key: "one", Value: []byte("old1")}, {Key: "two", Value: []byte("old2")}}); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(o.Path)
	// Force atomic replacement to fail, after a temporary ciphertext is synced.
	if err := os.Remove(o.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(o.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := k.Apply([]Change{{Key: "one", Value: []byte("new1")}, {Key: "two", Delete: true}}); err == nil {
		t.Fatal("write failure masked")
	}
	mustGet(t, k, "one", []byte("old1"))
	mustGet(t, k, "two", []byte("old2"))
	entries, err := os.ReadDir(filepath.Dir(o.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("temporary files leaked")
	}
	if err := os.Remove(o.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := k.Apply([]Change{{Key: "one", Value: []byte("new1")}, {Key: "two", Delete: true}}); err != nil {
		t.Fatal(err)
	}
	k.Close()
	k = openTest(t, o)
	mustGet(t, k, "one", []byte("new1"))
	mustGetMissing(t, k, "two")
	if runtime.GOOS != "windows" {
		stat, _ := os.Stat(o.Path)
		if stat.Mode().Perm() != 0o600 {
			t.Fatal("ciphertext permissions", stat.Mode())
		}
	}
}

func TestLifetimeLockAndConcurrentWrites(t *testing.T) {
	o := options(t)
	k := openTest(t, o)
	other := o
	other.Path = filepath.Join(t.TempDir(), "different.enc")
	if second, err := Open(other); second != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("shared master raced: %v", err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := k.Set(fmt.Sprintf("key%d", i), []byte{byte(i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i := range 20 {
		mustGet(t, k, fmt.Sprintf("key%d", i), []byte{byte(i)})
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockChild$")
	cmd.Env = append(os.Environ(), "SSHYARD_TEST_LOCK="+o.LockPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock: %v %s", err, out)
	}
	k.Close()
	lock, err := acquireLock(o.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if _, err := os.Stat(o.LockPath); err != nil {
		t.Fatal("lock file removed", err)
	}
}
func TestLockChild(t *testing.T) {
	path := os.Getenv("SSHYARD_TEST_LOCK")
	if path == "" {
		t.Skip("subprocess helper")
	}
	lock, err := acquireLock(path)
	if lock != nil {
		lock.Close()
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("child acquired parent's lock: %v", err)
	}
}

// An embedded Memory must not promote a batch implementation that skips hooks.
type flakyBackend struct {
	*Memory
	failKey string
	fail    error
}

func (b *flakyBackend) Set(key string, value []byte) error {
	if key == b.failKey {
		return b.fail
	}
	return b.Memory.Set(key, value)
}
func TestInjectedBatchFailureRollsBack(t *testing.T) {
	b := &flakyBackend{Memory: NewMemory(), failKey: "two", fail: errors.New("injected")}
	b.Memory.Set("one", []byte("old"))
	k := New(b)
	err := k.Apply([]Change{{Key: "one", Value: []byte("new")}, {Key: "two", Value: []byte("new")}})
	if !errors.Is(err, b.fail) {
		t.Fatal("failure bypassed", err)
	}
	mustGet(t, k, "one", []byte("old"))
	mustGetMissing(t, k, "two")
}

func TestClosedStoreCannotBypassFacade(t *testing.T) {
	o := options(t)
	k := openTest(t, o)
	backend := k.Backend()
	if err := k.Set("credential", []byte("value")); err != nil {
		t.Fatal(err)
	}
	k.Close()
	if _, err := backend.Get("credential"); !errors.Is(err, ErrLocked) {
		t.Fatal("backend bypassed close", err)
	}
	if err := k.Apply([]Change{{Key: "credential", Delete: true}}); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
}

// TestWipe: an unlocked store wipes to an empty one, still unlocked; a
// locked one loses its file without the master key, and opens empty
// with the same master key after.
func TestWipe(t *testing.T) {
	o := options(t)
	k, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Apply([]Change{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}}); err != nil {
		t.Fatal(err)
	}
	if err := k.Wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("a"); !errors.Is(err, ErrNotFound) || !k.Unlocked() {
		t.Fatalf("after wipe: %v, unlocked %v", err, k.Unlocked())
	}
	if err := k.Set("c", []byte("3")); err != nil {
		t.Fatal(err)
	}
	k.Close()
	master, _ := o.System.Get(MasterKey)

	k, err = OpenLocked(o)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.Wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(o.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file left: %v", err)
	}
	if err := k.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("secret kept: %v", err)
	}
	if now, _ := o.System.Get(MasterKey); !bytes.Equal(now, master) {
		t.Fatal("master key replaced")
	}
}
