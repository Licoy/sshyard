package netwatch

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a fake wall clock read every step.
type clock struct{ t time.Time }

func (c *clock) step(d time.Duration) time.Time {
	c.t = c.t.Add(d)
	return c.t
}

func TestDetectorTellsASettledChange(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	d := Detector{Settle: 2 * time.Second, MaxWait: 15 * time.Second, Gap: 20 * time.Second}
	if d.Observe(c.t, "wifi 10.0.0.2/24", true) {
		t.Fatal("the first read is no change")
	}
	for i := 0; i < 3; i++ {
		if d.Observe(c.step(3*time.Second), "wifi 10.0.0.2/24", true) {
			t.Fatalf("read %d: nothing changed", i)
		}
	}
	// Another network: told once it stayed so for Settle.
	if d.Observe(c.step(3*time.Second), "wifi 192.168.1.7/24", true) {
		t.Fatal("told before the change settled")
	}
	if !d.Pending() {
		t.Fatal("the change is not pending")
	}
	if !d.Observe(c.step(3*time.Second), "wifi 192.168.1.7/24", true) {
		t.Fatal("the settled change was not told")
	}
	if d.Observe(c.step(3*time.Second), "wifi 192.168.1.7/24", true) {
		t.Fatal("told twice")
	}
}

func TestDetectorDebouncesABurst(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	d := Detector{Settle: 2 * time.Second, MaxWait: 15 * time.Second, Gap: 20 * time.Second}
	d.Observe(c.t, "a", true)
	// The interface goes down, comes up without an address, then with a
	// new one: one change, told after the last.
	told := 0
	for _, s := range []string{"", "b0", "b"} {
		if d.Observe(c.step(time.Second), s, true) {
			told++
		}
	}
	if told != 0 {
		t.Fatalf("told %d times during the burst", told)
	}
	if !d.Observe(c.step(3*time.Second), "b", true) {
		t.Fatal("the burst was not told once settled")
	}
	// A network that never settles is told by MaxWait all the same.
	start := c.t
	n := 0
	for i := 0; c.t.Sub(start) < 20*time.Second; i++ {
		if d.Observe(c.step(time.Second), string(rune('c'+i%2)), true) {
			n++
			if c.t.Sub(start) < 15*time.Second {
				t.Fatalf("told after %v, before MaxWait", c.t.Sub(start))
			}
		}
	}
	if n != 1 {
		t.Fatalf("a flapping network was told %d times in 20s, want 1", n)
	}
}

func TestDetectorTellsAWakeFromSleep(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	d := Detector{Settle: 2 * time.Second, MaxWait: 15 * time.Second, Gap: 20 * time.Second}
	d.Observe(c.t, "a", true)
	// A late read, well under the gap, is no sleep.
	if d.Observe(c.step(10*time.Second), "a", true) || d.Pending() {
		t.Fatal("a late read counted as a sleep")
	}
	// The wall clock jumped: the computer slept, the network is the same.
	if d.Observe(c.step(40*time.Minute), "a", true) {
		t.Fatal("told before the network settled after the wake")
	}
	if !d.Pending() {
		t.Fatal("the wake is not pending")
	}
	if !d.Observe(c.step(3*time.Second), "a", true) {
		t.Fatal("the wake was not told")
	}
}

func TestDetectorUnreadableNetwork(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	d := Detector{Settle: 2 * time.Second}
	// Unread at first: the first state read is no change.
	d.Observe(c.t, "", false)
	if d.Observe(c.step(3*time.Second), "a", true) || d.Pending() {
		t.Fatal("the first state read counted as a change")
	}
	// A failed read keeps the state it had.
	d.Observe(c.step(3*time.Second), "", false)
	if d.Observe(c.step(3*time.Second), "a", true) || d.Pending() {
		t.Fatal("a failed read counted as a change")
	}
}

func TestWatcherRuns(t *testing.T) {
	var mu sync.Mutex
	state := "a"
	var reads atomic.Int32
	w := Watcher{Every: 5 * time.Millisecond, Detector: Detector{Settle: time.Millisecond}, State: func() (string, error) {
		reads.Add(1)
		mu.Lock()
		defer mu.Unlock()
		if state == "" {
			return "", errors.New("unreadable")
		}
		return state, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		w.Run(ctx, func() { changes <- struct{}{} })
		close(done)
	}()
	for reads.Load() < 3 {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-changes:
		t.Fatal("told a change with none")
	default:
	}
	mu.Lock()
	state = "b"
	mu.Unlock()
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("the change was not told")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end with its context")
	}
	if len(changes) != 0 {
		t.Fatalf("told %d more changes", len(changes))
	}
}

func TestUsableAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"192.168.1.7/24":       true,
		"10.8.0.2/32":          true,
		"2001:db8::7/64":       true,
		"127.0.0.1/8":          false,
		"::1/128":              false,
		"fe80::1c2:3ff:fe4/64": false,
		"169.254.10.2/16":      false,
	} {
		ip, n, err := net.ParseCIDR(addr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		if got := usable(n); got != want {
			t.Errorf("usable(%s) = %v, want %v", addr, got, want)
		}
	}
	if _, err := Addresses(); err != nil {
		t.Fatalf("Addresses: %v", err)
	}
}
