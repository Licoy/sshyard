package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
)

// marker is a transfer in what the shell prints: it starts at "<<X>>"
// and ends after "<<END>>".
type marker struct {
	mu      sync.Mutex
	got     bytes.Buffer
	running chan struct{}
	ended   chan error
	reply   string
}

func newMarker(reply string) *marker {
	return &marker{running: make(chan struct{}, 4), ended: make(chan error, 4), reply: reply}
}

func (m *marker) find(p []byte) (int, func(context.Context, xfer.Conn) []byte) {
	i := bytes.Index(p, []byte("<<X>>"))
	if i < 0 {
		return -1, nil
	}
	return i, m.run
}

func (m *marker) run(ctx context.Context, c xfer.Conn) []byte {
	m.running <- struct{}{}
	c.Write([]byte(m.reply))
	buf := make([]byte, 64)
	var all []byte
	for {
		n, err := c.Read(buf)
		all = append(all, buf[:n]...)
		if i := bytes.Index(all, []byte("<<END>>")); i >= 0 {
			m.mu.Lock()
			m.got.Write(all[:i+7])
			m.mu.Unlock()
			m.ended <- nil
			return all[i+7:]
		}
		if err != nil {
			m.ended <- err
			return nil
		}
	}
}

func (m *marker) carried() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.got.String()
}

// TestTakeover: from where a transfer starts in the output, the
// transfer reads it and the terminal shows none of it, nor the session
// log; it writes to the shell as it is, typed input is refused, and what
// came after its end shows.
func TestTakeover(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(f), NewLine: "\r\n", LocalEcho: true}, 80, 24)
	defer s.Close()
	var logged bytes.Buffer
	var logMu sync.Mutex
	s.SetOutputTap(func(p []byte) { logMu.Lock(); logged.Write(p); logMu.Unlock() })
	m := newMarker("a\rb")
	s.SetTakeover(m.find)
	s.Start()
	waitState(t, s, Connected)

	f.say("$ sz file\r\nbefore<<X>>\x00\x18binary")
	<-m.running
	waitText(t, s, "before")
	waitTyped(t, f, "a\rb")
	if !s.TakenOver() {
		t.Fatal("not taken over")
	}
	if err := s.Send([]byte("typed")); !errors.Is(err, ErrInputBusy) {
		t.Fatalf("typing while taken over: %v", err)
	}
	f.say("more<<END>>\r\n$ ")
	if err := <-m.ended; err != nil {
		t.Fatal(err)
	}
	waitText(t, s, "$ ")
	f.say("after")
	waitText(t, s, "after")
	if got := m.carried(); got != "<<X>>\x00\x18binarymore<<END>>" {
		t.Fatalf("carried %q", got)
	}
	screen := s.Term.ScreenText()
	if strings.Contains(screen, "binary") || strings.Contains(screen, "<<") {
		t.Fatalf("the terminal showed the transfer: %q", screen)
	}
	logMu.Lock()
	log := logged.String()
	logMu.Unlock()
	if strings.Contains(log, "binary") || !strings.Contains(log, "before") || !strings.Contains(log, "after") {
		t.Fatalf("logged %q", log)
	}
	if s.TakenOver() {
		t.Fatal("still taken over")
	}
	// Typing works again, as typed.
	s.Write([]byte("x\r"))
	waitTyped(t, f, "a\rbx\r\n")
}

// TestTakeoverSplit: a transfer whose start came over two writes is
// found.
func TestTakeoverSplit(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(f)}, 80, 24)
	defer s.Close()
	m := newMarker("")
	s.SetTakeover(m.find)
	s.Start()
	waitState(t, s, Connected)
	f.say("text<<")
	waitText(t, s, "text<<")
	f.say("X>>data<<END>>")
	<-m.running
	if err := <-m.ended; err != nil {
		t.Fatal(err)
	}
	if got := m.carried(); got != "<<X>>data<<END>>" {
		t.Fatalf("carried %q", got)
	}
}

// TestTakeoverEndsWithSession: a transfer reading when the session
// closes sees the stream end.
func TestTakeoverEndsWithSession(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(f)}, 80, 24)
	m := newMarker("")
	s.SetTakeover(m.find)
	s.Start()
	waitState(t, s, Connected)
	f.say("<<X>>")
	<-m.running
	s.Close()
	select {
	case err := <-m.ended:
		if err == nil {
			t.Fatal("ended well")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the transfer did not end")
	}
}

// TestNoTakeover: without SetTakeover, or after it is cleared, output
// shows as it is.
func TestNoTakeover(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(f)}, 80, 24)
	defer s.Close()
	m := newMarker("")
	s.SetTakeover(m.find)
	s.SetTakeover(nil)
	s.Start()
	waitState(t, s, Connected)
	f.say("<<X>>shown")
	waitText(t, s, "<<X>>shown")
	if s.TakenOver() {
		t.Fatal("taken over")
	}
}
