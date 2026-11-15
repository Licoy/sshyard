// Package awake keeps the computer from going to sleep on its own while
// something needs it up, files transferring: the system's "no idle
// sleep" hold is taken while at least one owner is busy, and let go once
// none is. The display may still turn off, and the user can still put the
// computer to sleep. Each system has its own hold (System); where there
// is none, or it fails, nothing is held and nothing is said but a log
// line.
package awake

import (
	"log"
	"runtime"
	"sync"
	"sync/atomic"
)

// Backend is a system's hold. Hold and Release are called in turn, never
// at once, from one goroutine locked to its OS thread (a Windows hold
// belongs to its thread).
type Backend interface {
	// Hold keeps the system from sleeping while idle.
	Hold() error
	// Release lets it sleep again.
	Release() error
}

// Keeper takes the hold while any of its owners is busy. It is safe for
// concurrent use. A nil Keeper, or one without a backend, holds nothing.
type Keeper struct {
	b    Backend
	logf func(format string, args ...any)

	mu     sync.Mutex
	owners map[any]bool // the busy ones
	off    bool
	closed bool

	kick chan struct{}
	done chan struct{}
	// held is whether the backend holds, as last applied
	held   atomic.Bool
	logged atomic.Bool
}

// New returns a keeper on b; nil holds nothing.
func New(b Backend) *Keeper {
	k := &Keeper{b: b, logf: log.Printf, owners: map[any]bool{}, kick: make(chan struct{}, 1), done: make(chan struct{})}
	if b == nil {
		close(k.done)
		return k
	}
	go k.run()
	return k
}

// Set says whether owner is busy. The hold is taken while one is, and
// let go when the last one is not; saying the same again changes nothing.
func (k *Keeper) Set(owner any, busy bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	if k.closed || k.owners[owner] == busy {
		k.mu.Unlock()
		return
	}
	if busy {
		k.owners[owner] = true
	} else {
		delete(k.owners, owner)
	}
	k.mu.Unlock()
	k.notify()
}

// Busy is how many owners are busy.
func (k *Keeper) Busy() int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.owners)
}

// SetEnabled turns the keeper on or off (settings.KeepAwake): off, the
// hold is let go however busy the owners are, and taken again when it is
// turned back on while one still is.
func (k *Keeper) SetEnabled(on bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	changed := k.off == on
	k.off = !on
	k.mu.Unlock()
	if changed {
		k.notify()
	}
}

// Held reports whether the system's hold is taken now.
func (k *Keeper) Held() bool { return k != nil && k.held.Load() }

// Close lets the hold go for good, as the app quits, and waits for it.
func (k *Keeper) Close() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.closed = true
	k.owners = map[any]bool{}
	k.mu.Unlock()
	k.notify()
	<-k.done
}

// want is whether the hold is to be taken. Under k.mu.
func (k *Keeper) want() bool { return len(k.owners) > 0 && !k.off && !k.closed }

func (k *Keeper) notify() {
	select {
	case k.kick <- struct{}{}:
	default:
	}
}

// run applies what is wanted to the backend, from one thread, until the
// keeper closes.
func (k *Keeper) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(k.done)
	applied := false
	for range k.kick {
		k.mu.Lock()
		want, closed := k.want(), k.closed
		k.mu.Unlock()
		if want != applied {
			var err error
			if want {
				err = k.b.Hold()
			} else {
				err = k.b.Release()
			}
			// A hold that failed is not tried again before the owners
			// are all done: a system without one would be asked at
			// every change of a transfer.
			applied = want
			k.held.Store(want && err == nil)
			if err != nil && !k.logged.Swap(true) {
				k.logf("sshyard: keeping the computer awake: %v", err)
			}
		}
		if closed {
			return
		}
	}
}
