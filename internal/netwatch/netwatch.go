// Package netwatch notices when the network under this computer's
// connections may have changed: an interface or an address came or went
// (another Wi-Fi network, a VPN turned on or off, a cable plugged in), or
// the computer woke from sleep. A connection on the old path then often
// hangs until a long TCP timeout; told of the change, the app can check
// its connections at once.
//
// It polls, which works the same on every system: the addresses of the
// interfaces that are up, every few seconds, and the wall clock, which
// jumps by much more than the poll interval across a sleep. A burst of
// changes (an interface going down, then up with a new address) is told
// once it has settled.
package netwatch

import (
	"context"
	"net"
	"slices"
	"strings"
	"time"
)

// Defaults of a Watcher's zero fields.
const (
	// DefaultEvery is how often the network is read.
	DefaultEvery = 3 * time.Second
	// DefaultSettle is how long the network must stay the same after a
	// change before the change is told.
	DefaultSettle = 2 * time.Second
	// DefaultMaxWait is the longest a burst of changes delays the news.
	DefaultMaxWait = 15 * time.Second
	// DefaultGap is how far the wall clock may move between two reads
	// before the computer is taken to have slept.
	DefaultGap = 20 * time.Second
)

// Detector tells, from reads of the network's state taken one after the
// other, when it changed: the state differs from the last one read, or
// the wall clock jumped between two reads by more than Gap. A change is
// told once nothing else changed for Settle, or MaxWait after the first
// change of a burst, whichever comes first. Its zero value is ready, with
// the defaults; it is not safe for concurrent use.
type Detector struct {
	Settle, MaxWait, Gap time.Duration

	read    bool // at is set
	known   bool // state is set
	state   string
	at      time.Time
	pending bool
	first   time.Time // the first change of the burst pending
	latest  time.Time // and its latest
}

// Observe takes a read at now: state is the network's, unless ok is false
// (it could not be read; the clock still counts). It reports whether a
// change is to be told now.
func (d *Detector) Observe(now time.Time, state string, ok bool) bool {
	settle, maxWait, gap := or(d.Settle, DefaultSettle), or(d.MaxWait, DefaultMaxWait), or(d.Gap, DefaultGap)
	// The wall clock: the monotonic one stops while the computer sleeps.
	now = now.Round(0)
	changed := false
	if d.read {
		if step := now.Sub(d.at); step > gap || step < -gap {
			changed = true
		}
	}
	d.read, d.at = true, now
	if ok {
		if d.known && state != d.state {
			changed = true
		}
		d.known, d.state = true, state
	}
	if changed {
		if !d.pending {
			d.pending, d.first = true, now
		}
		d.latest = now
	}
	if d.pending && (now.Sub(d.latest) >= settle || now.Sub(d.first) >= maxWait) {
		d.pending = false
		return true
	}
	return false
}

// Pending reports a change seen and not told yet.
func (d *Detector) Pending() bool { return d.pending }

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Watcher reads the network every Every and calls changed as its
// Detector says.
type Watcher struct {
	// Every is how often the network is read; 0 is DefaultEvery.
	Every time.Duration
	// State reads the network's state; nil is Addresses.
	State func() (string, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Detector decides when a change is told.
	Detector Detector
}

// Watch calls changed, on its own goroutine, each time the network
// changes, until ctx ends. It blocks.
func Watch(ctx context.Context, changed func()) {
	var w Watcher
	w.Run(ctx, changed)
}

// Run is Watch with w's settings.
func (w *Watcher) Run(ctx context.Context, changed func()) {
	every := or(w.Every, DefaultEvery)
	state, now := w.State, w.Now
	if state == nil {
		state = Addresses
	}
	if now == nil {
		now = time.Now
	}
	observe := func() {
		s, err := state()
		if w.Detector.Observe(now(), s, err == nil) {
			changed()
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	observe()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			observe()
		}
	}
}

// Addresses is the state of this computer's network: the addresses of
// its interfaces that are up and running, by interface, in order. The
// loopback and link-local addresses, which come and go with interfaces
// no connection to a server uses (and on some systems flap all the
// time), are left out.
func Addresses() (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var lines []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagRunning == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if usable(a) {
				lines = append(lines, ifc.Name+" "+a.String())
			}
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n"), nil
}

// usable is an address a connection to a server may go out from.
func usable(a net.Addr) bool {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	default:
		return false
	}
	return ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}
