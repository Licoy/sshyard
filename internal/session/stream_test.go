package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStream is the other end of a Telnet connection or serial port:
// say writes its output, typed is what was sent to it.
type fakeStream struct {
	r *io.PipeReader
	w *io.PipeWriter

	mu     sync.Mutex
	in     bytes.Buffer
	breaks int
}

func newFakeStream() *fakeStream {
	r, w := io.Pipe()
	return &fakeStream{r: r, w: w}
}

func (f *fakeStream) Read(p []byte) (int, error) { return f.r.Read(p) }

func (f *fakeStream) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.in.Write(p)
}

func (f *fakeStream) Close() error          { f.w.Close(); return nil }
func (f *fakeStream) Resize(int, int) error { return nil }

func (f *fakeStream) say(s string) { f.w.Write([]byte(s)) }

func (f *fakeStream) typed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.in.String()
}

// breakStream can send a break, as a serial port.
type breakStream struct{ *fakeStream }

func (b breakStream) Break(time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.breaks++
	return nil
}

// probeStream answers probes with answer.
type probeStream struct {
	*fakeStream
	answer func(ctx context.Context) (time.Duration, error)
}

func (p probeStream) Probe(ctx context.Context) (time.Duration, error) { return p.answer(ctx) }

func opener(st Stream) Opener {
	return func(context.Context, int, int, string) (Stream, error) { return st, nil }
}

func waitTyped(t *testing.T, f *fakeStream, want string) {
	t.Helper()
	waitFor(t, "typed "+want, func() bool { return f.typed() == want })
}

// TestStreamSession: a stream's output shows, Enter sends the host's
// newline, typing echoes when asked, and the other end closing it is the
// shell exiting.
func TestStreamSession(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(f), NewLine: "\r\n", LocalEcho: true}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	f.say("router> ")
	waitText(t, s, "router>")
	s.Write([]byte("show ver\r"))
	waitTyped(t, f, "show ver\r\n")
	waitText(t, s, "router> show ver")
	f.Close()
	if st := waitState(t, s, Exited); st.Err != nil {
		t.Fatalf("exited with %v", st.Err)
	}
}

// TestStreamBackspaceBS: the option makes the terminal's Backspace send
// BS, and a session without it keeps DEL.
func TestStreamBackspaceBS(t *testing.T) {
	bs := New(Config{}, Options{Open: opener(newFakeStream()), BackspaceBS: true}, 80, 24)
	defer bs.Close()
	del := New(Config{}, Options{Open: opener(newFakeStream())}, 80, 24)
	defer del.Close()
	if !bs.Term.Modes().BackspaceBS || del.Term.Modes().BackspaceBS {
		t.Fatalf("BackspaceBS %v, without it %v", bs.Term.Modes().BackspaceBS, del.Term.Modes().BackspaceBS)
	}
}

// TestStreamLost: a stream that fails is a lost connection, and one that
// does not open is the session failing with its error.
func TestStreamLost(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(f)}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	unplugged := errors.New("unplugged")
	f.w.CloseWithError(unplugged)
	st := waitState(t, s, Disconnected)
	if !errors.Is(st.Err, unplugged) || !errors.Is(st.Err, errConnectionLost) {
		t.Fatalf("lost with %v", st.Err)
	}

	busy := errors.New("busy")
	s2 := New(Config{}, Options{Open: func(context.Context, int, int, string) (Stream, error) { return nil, busy }}, 80, 24)
	defer s2.Close()
	s2.Start()
	if st := waitState(t, s2, Failed); !errors.Is(st.Err, busy) {
		t.Fatalf("failed with %v", st.Err)
	}
}

// TestStreamLogin: the login and password prompts are answered once
// each with the credentials, the password never echoed.
func TestStreamLogin(t *testing.T) {
	f := newFakeStream()
	cfg := Config{User: "admin", Credentials: password("s3cret")}
	s := New(cfg, Options{Open: opener(f), Login: true, LocalEcho: true}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	f.say("\x1b[1mlogin:\x1b[0m ")
	waitTyped(t, f, "admin\r")
	f.say("admin\r\nPassword: ")
	waitTyped(t, f, "admin\rs3cret\r")
	f.say("\r\nLogin incorrect\r\nlogin: ")
	waitText(t, s, "Login incorrect")
	time.Sleep(20 * time.Millisecond)
	if got := f.typed(); got != "admin\rs3cret\r" {
		t.Fatalf("answered again: %q", got)
	}
	if strings.Contains(s.Term.ScreenText(), "s3cret") {
		t.Fatal("the password shows")
	}
}

// TestLoginFeed: prompts match through colors and split writes, a
// password is answered once, and nothing after the login window.
func TestLoginFeed(t *testing.T) {
	now := time.Now()
	l := newLogin("pi", "pw", "\r\n", now)
	if got := l.feed([]byte("Last login: Mon Nov  2 10:00\r\n"), now); got != nil {
		t.Fatalf("answered a banner: %q", got)
	}
	if got := l.feed([]byte("raspberry log"), now); got != nil {
		t.Fatalf("answered half a prompt: %q", got)
	}
	if got := string(l.feed([]byte("in: "), now)); got != "pi\r\n" {
		t.Fatalf("user answer %q", got)
	}
	if got := string(l.feed([]byte("\x1b[33mPassword for pi:\x1b[0m "), now)); got != "pw\r\n" {
		t.Fatalf("password answer %q", got)
	}
	if got := l.feed([]byte("Password: "), now); got != nil {
		t.Fatalf("answered twice: %q", got)
	}

	late := newLogin("pi", "pw", "", now)
	if got := late.feed([]byte("login: "), now.Add(loginFor+time.Second)); got != nil {
		t.Fatalf("answered late: %q", got)
	}
	nouser := newLogin("", "pw", "", now)
	if got := nouser.feed([]byte("login: "), now); got != nil {
		t.Fatalf("answered with no user: %q", got)
	}
	if got := string(nouser.feed([]byte("Password: "), now)); got != "pw\r" {
		t.Fatalf("password answer %q", got)
	}
}

// TestStreamBreak: a session sends a break on a stream that can, and
// says so for one that cannot.
func TestStreamBreak(t *testing.T) {
	f := newFakeStream()
	s := New(Config{}, Options{Open: opener(breakStream{f})}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	if !s.CanBreak() {
		t.Fatal("cannot break")
	}
	if err := s.Break(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	breaks := f.breaks
	f.mu.Unlock()
	if breaks != 1 {
		t.Fatalf("%d breaks", breaks)
	}

	plain := New(Config{}, Options{Open: opener(newFakeStream())}, 80, 24)
	defer plain.Close()
	plain.Start()
	waitState(t, plain, Connected)
	if plain.CanBreak() || !errors.Is(plain.Break(), ErrNoBreak) {
		t.Fatal("a plain stream breaks")
	}
}

// TestStreamKeepAliveLenient: a stream that never answers a probe does
// not take them and stays up; one that stops answering is lost.
func TestStreamKeepAliveLenient(t *testing.T) {
	never := probeStream{newFakeStream(), func(ctx context.Context) (time.Duration, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	s := New(Config{}, Options{Open: opener(never), KeepAlive: 20 * time.Millisecond, KeepAliveMax: 1}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	time.Sleep(200 * time.Millisecond)
	if st := s.Status(); st.State != Connected {
		t.Fatalf("a server without probes is %v: %v", st.State, st.Err)
	}

	var mu sync.Mutex
	answers := 1
	stops := probeStream{newFakeStream(), func(ctx context.Context) (time.Duration, error) {
		mu.Lock()
		ok := answers > 0
		answers--
		mu.Unlock()
		if ok {
			return time.Millisecond, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	s2 := New(Config{}, Options{Open: opener(stops), KeepAlive: 20 * time.Millisecond, KeepAliveMax: 1}, 80, 24)
	defer s2.Close()
	s2.Start()
	if st := waitState(t, s2, Disconnected); !errors.Is(st.Err, ErrKeepAliveTimeout) {
		t.Fatalf("lost with %v", st.Err)
	}
}
