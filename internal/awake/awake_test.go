package awake

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fake is a backend that counts what it was asked.
type fake struct {
	mu             sync.Mutex
	held           bool
	holds, release int
	fail           error
	// overlap is set when two calls ran at once.
	overlap bool
	busy    bool
}

func (f *fake) call(hold bool) error {
	f.mu.Lock()
	if f.busy {
		f.overlap = true
	}
	f.busy = true
	f.mu.Unlock()
	time.Sleep(time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy = false
	if hold {
		f.holds++
		if f.fail != nil {
			return f.fail
		}
		f.held = true
	} else {
		f.release++
		f.held = false
	}
	return nil
}

func (f *fake) Hold() error    { return f.call(true) }
func (f *fake) Release() error { return f.call(false) }

func (f *fake) state() (held bool, holds, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held, f.holds, f.release
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestKeeperHoldsWhileAnyOwnerIsBusy(t *testing.T) {
	f := &fake{}
	k := New(f)
	defer k.Close()
	k.Set("files 1", false)
	time.Sleep(10 * time.Millisecond)
	if held, holds, _ := f.state(); held || holds != 0 {
		t.Fatal("held with no owner busy")
	}
	k.Set("files 1", true)
	eventually(t, "the hold", func() bool { h, _, _ := f.state(); return h && k.Held() })
	// Another owner, and the same one again: still one hold.
	k.Set("files 2", true)
	k.Set("files 1", true)
	if k.Busy() != 2 {
		t.Fatalf("%d busy, want 2", k.Busy())
	}
	k.Set("files 1", false)
	time.Sleep(10 * time.Millisecond)
	if held, holds, releases := f.state(); !held || holds != 1 || releases != 0 {
		t.Fatalf("held %v after %d holds, %d releases; want held once", held, holds, releases)
	}
	k.Set("files 1", false) // not busy twice: no count below zero
	k.Set("files 2", false)
	eventually(t, "the release", func() bool { h, _, _ := f.state(); return !h && !k.Held() })
	k.Set("files 2", true)
	eventually(t, "the hold again", func() bool { h, holds, _ := f.state(); return h && holds == 2 })
}

func TestKeeperConcurrentOwners(t *testing.T) {
	f := &fake{}
	k := New(f)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				k.Set(i, j%2 == 0)
			}
			k.Set(i, false)
		}()
	}
	wg.Wait()
	if k.Busy() != 0 {
		t.Fatalf("%d busy after every owner was done", k.Busy())
	}
	eventually(t, "the release", func() bool { h, _, _ := f.state(); return !h })
	k.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overlap {
		t.Error("the backend was called twice at once")
	}
	if f.holds != f.release {
		t.Errorf("%d holds, %d releases", f.holds, f.release)
	}
}

func TestKeeperEnabledAndClose(t *testing.T) {
	f := &fake{}
	k := New(f)
	k.Set("x", true)
	eventually(t, "the hold", func() bool { h, _, _ := f.state(); return h })
	// Turned off, it lets go while the owner is busy, and holds again
	// when turned back on.
	k.SetEnabled(false)
	eventually(t, "the release", func() bool { h, _, _ := f.state(); return !h })
	k.Set("y", true)
	time.Sleep(10 * time.Millisecond)
	if h, _, _ := f.state(); h {
		t.Fatal("held while off")
	}
	k.SetEnabled(true)
	eventually(t, "the hold again", func() bool { h, _, _ := f.state(); return h })
	// Quitting lets go, and nothing holds afterwards.
	k.Close()
	if h, _, _ := f.state(); h || k.Held() {
		t.Fatal("held after Close")
	}
	k.Set("z", true)
	k.Close()
	time.Sleep(10 * time.Millisecond)
	if h, _, _ := f.state(); h {
		t.Fatal("held after Close")
	}
}

func TestKeeperFailedHoldIsQuiet(t *testing.T) {
	f := &fake{fail: errors.New("no inhibitor")}
	k := New(f)
	var logs []string
	var mu sync.Mutex
	k.logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, format)
		mu.Unlock()
	}
	defer k.Close()
	k.Set("a", true)
	eventually(t, "the attempt", func() bool { _, holds, _ := f.state(); return holds == 1 })
	// More changes while still busy do not ask again.
	k.Set("b", true)
	k.Set("b", false)
	time.Sleep(10 * time.Millisecond)
	if _, holds, _ := f.state(); holds != 1 || k.Held() {
		t.Fatalf("asked %d times, held %v", holds, k.Held())
	}
	// Once all are done, the next busy owner asks again.
	k.Set("a", false)
	eventually(t, "the release", func() bool { _, _, releases := f.state(); return releases == 1 })
	k.Set("a", true)
	eventually(t, "the second attempt", func() bool { _, holds, _ := f.state(); return holds == 2 })
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 {
		t.Errorf("logged %d times, want once", len(logs))
	}
}

func TestNilKeeper(t *testing.T) {
	for _, k := range []*Keeper{nil, New(nil)} {
		k.Set("a", true)
		k.SetEnabled(false)
		if k.Held() {
			t.Fatal("a keeper without a backend holds")
		}
		k.Close()
	}
}
