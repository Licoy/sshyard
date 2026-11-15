package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/awake"
	"github.com/Licoy/sshyard/internal/sftpsvc"
)

// awakeHold is a system hold that says whether it is taken.
type awakeHold struct {
	mu   sync.Mutex
	held bool
}

func (h *awakeHold) Hold() error    { h.mu.Lock(); h.held = true; h.mu.Unlock(); return nil }
func (h *awakeHold) Release() error { h.mu.Lock(); h.held = false; h.mu.Unlock(); return nil }
func (h *awakeHold) is() bool       { h.mu.Lock(); defer h.mu.Unlock(); return h.held }

func waitHeld(t *testing.T, h *awakeHold, want bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.is() != want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: held %v, want %v", what, h.is(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTransfersKeepAwake: a files pane's queue holds the computer awake
// while a transfer runs or waits its turn, not while all are paused or
// over.
func TestTransfersKeepAwake(t *testing.T) {
	h := &awakeHold{}
	a := &App{awake: awake.New(h)}
	defer a.awake.Close()
	fs := a.newFiles()
	ts := fs.transfers
	// No connection comes: each transfer runs, waiting for it.
	ts.Client = func(ctx context.Context) (*sftpsvc.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ts.Logf = func(string, ...any) {}
	ts.SetConcurrency(1)
	dir := t.TempDir()
	first := ts.Add(sftpsvc.Download, "/srv/a.bin", filepath.Join(dir, "a.bin"), "a.bin")
	second := ts.Add(sftpsvc.Download, "/srv/b.bin", filepath.Join(dir, "b.bin"), "b.bin")
	waitHeld(t, h, true, "two transfers")

	ts.Pause(first)
	time.Sleep(20 * time.Millisecond)
	if !h.is() {
		t.Fatal("let go while the second transfer runs")
	}
	ts.Pause(second)
	waitHeld(t, h, false, "both paused")
	ts.Resume(second)
	waitHeld(t, h, true, "one resumed")
	fs.close()
	ts.Wait()
	waitHeld(t, h, false, "the pane closed")

	// Turned off in the settings, nothing is held.
	a.awake.SetEnabled(false)
	third := ts.Add(sftpsvc.Download, "/srv/c.bin", filepath.Join(dir, "c.bin"), "c.bin")
	time.Sleep(20 * time.Millisecond)
	if h.is() {
		t.Fatal("held with the setting off")
	}
	a.awake.SetEnabled(true)
	waitHeld(t, h, true, "the setting on again")
	ts.Cancel(third)
	ts.Wait()
	waitHeld(t, h, false, "canceled")
}

// TestTerminalTransferKeepsAwake: a terminal's ZMODEM or trzsz transfer
// holds it while it runs, not while it only asks.
func TestTerminalTransferKeepsAwake(t *testing.T) {
	h := &awakeHold{}
	k := awake.New(h)
	defer k.Close()
	x := &xferPane{seen: map[string]bool{}, keep: k}
	x.begin(context.Background())
	time.Sleep(20 * time.Millisecond)
	if h.is() {
		t.Fatal("held while the transfer only asks")
	}
	x.going(&xferRun{proto: protoZmodem, start: time.Now()})
	waitHeld(t, h, true, "the transfer running")
	x.end()
	waitHeld(t, h, false, "the transfer over")
}
