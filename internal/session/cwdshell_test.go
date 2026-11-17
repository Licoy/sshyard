package session

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestCwdAnswer(t *testing.T) {
	const mark = "00ff00ff00ff00ff"
	for _, c := range []struct {
		line string
		d    string
		ok   bool
		err  error
	}{
		{mark + " 0 /srv/app " + mark + "\n", "/srv/app", true, nil},
		{mark + " 0 /srv/a/../my app " + mark + "\n", "/srv/my app", true, nil},
		{mark + " 1  " + mark + "\n", "", true, ErrNoCwd},
		{mark + " 0 srv " + mark + "\n", "", true, ErrNoCwd},
		// A directory over two lines: its first.
		{mark + " 0 /srv/a\n", "", true, ErrNoCwd},
		{"welcome to the server\n", "", false, nil},
		{"1111111111111111 0 /srv 1111111111111111\n", "", false, nil},
		{"/srv/b " + mark + "\n", "", false, nil},
	} {
		d, ok, err := cwdAnswer(c.line, mark)
		if d != c.d || ok != c.ok || !errors.Is(err, c.err) {
			t.Errorf("cwdAnswer(%q) = %q %v %v", c.line, d, ok, err)
		}
	}
}

// TestCwdRequest types requests into a sh of its own, as into a probe
// shell, on a /proc of its own: each is answered on a line of its own
// marks, the sh still there for the next.
func TestCwdRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	if err := exec.Command("sh", "-c", `printf 'a\0b\0' | grep -qzx b`).Run(); err != nil {
		t.Skip("this grep has no -z")
	}
	root := t.TempDir()
	proc := func(pid, ppid, env, cwd string) {
		d := filepath.Join(root, pid)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "environ"), []byte(env), 0o644)
		os.WriteFile(filepath.Join(d, "stat"), []byte(pid+" (bash) S "+ppid+" 1 1 0"), 0o644)
		os.Symlink(cwd, filepath.Join(d, "cwd"))
	}
	proc("100", "1", "HOME=/root\x00", "/")
	proc("101", "100", TagEnv+"=0123abcd\x00", "/srv/app")
	sh := exec.Command("sh")
	in, _ := sh.StdinPipe()
	out, _ := sh.StdoutPipe()
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); sh.Wait() }()
	r := bufio.NewReader(out)
	ask := func(tag, mark string) (string, error) {
		t.Helper()
		io.WriteString(in, strings.Replace(cwdRequest(tag, mark), "p=/proc", "p="+root, 1))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("sh ended: %v", err)
			}
			if d, ok, err := cwdAnswer(line, mark); ok {
				return d, err
			}
		}
	}
	if d, err := ask("0123abcd", "aaaaaaaaaaaaaaaa"); err != nil || d != "/srv/app" {
		t.Fatalf("%q %v", d, err)
	}
	if d, err := ask("nope", "bbbbbbbbbbbbbbbb"); !errors.Is(err, ErrNoCwd) {
		t.Fatalf("an unknown tag: %q %v", d, err)
	}
	if d, err := ask("0123abcd", "cccccccccccccccc"); err != nil || d != "/srv/app" {
		t.Fatalf("again: %q %v", d, err)
	}
}

// TestEnters counts the input that holds an Enter, typed or written
// whole, not the terminal's replies.
func TestEnters(t *testing.T) {
	var out strings.Builder
	s := &Session{status: Status{State: Connected}}
	s.stdin = &serializedInput{WriteCloser: nopWriteCloser{&out}, enters: &s.enters}
	s.Send([]byte("ls"))
	waitFor(t, "ls written", func() bool { s.stdin.mu.Lock(); defer s.stdin.mu.Unlock(); return out.Len() == 2 })
	if n := s.Enters(); n != 0 {
		t.Fatalf("%d Enters without one", n)
	}
	s.Send([]byte("\r"))
	waitFor(t, "Enter written", func() bool { s.stdin.mu.Lock(); defer s.stdin.mu.Unlock(); return out.Len() == 3 })
	s.Write([]byte("cd /srv\r"))
	s.replyInput([]byte("\x1b[0n\r"))
	if n := s.Enters(); n != 2 {
		t.Fatalf("%d Enters, want 2", n)
	}
}

// requestMark is the mark of a request typed into a probe shell.
var requestMark = regexp.MustCompile(`' ([0-9a-f]{16}) "\$\?" "\$d" ([0-9a-f]{16})$`)

// probeServer is a server whose "exec sh" is a probe shell that answers
// with answer(tag of the request), counting the shells opened, and whose
// one-shot script (ShellCwd) answers /oneshot.
func probeServer(t *testing.T, answer func(line string, mark string) string) (*Session, *atomic.Int32, *atomic.Int32, chan struct{}) {
	t.Helper()
	srv := server(t)
	var shells, oneShots atomic.Int32
	ended := make(chan struct{}, 8)
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		switch {
		case cmd == "exec sh":
			shells.Add(1)
			defer func() { ended <- struct{}{} }()
			io.WriteString(ch, "motd from .bashrc\n")
			r := bufio.NewReader(ch)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return 0
				}
				m := requestMark.FindStringSubmatch(strings.TrimSuffix(line, "\n"))
				if m == nil {
					return 2
				}
				reply := answer(line, m[1])
				if reply == "" {
					continue
				}
				if _, err := io.WriteString(ch, reply); err != nil {
					return 0
				}
			}
		case strings.HasPrefix(cmd, "sh -c "):
			oneShots.Add(1)
			io.WriteString(ch, "/oneshot\n")
			return 0
		}
		return 127
	}
	s := New(Config{Addr: srv.Addr, User: "demo", HostKey: trusted(t, srv).Callback(), Credentials: password("secret")}, Options{}, 80, 24)
	t.Cleanup(s.Close)
	s.Start()
	waitState(t, s, Connected)
	return s, &shells, &oneShots, ended
}

// TestProbeCwd reads the shell's directory through one probe shell kept
// open: what the login printed is skipped, the tag asked for is the
// shell's, and the probe shell ends with the session.
func TestProbeCwd(t *testing.T) {
	s, shells, oneShots, ended := probeServer(t, func(line, mark string) string {
		if strings.Contains(line, TagEnv+"=") {
			return "stray\n" + mark + " 0 /srv/app/../www " + mark + "\n"
		}
		return mark + " 1  " + mark + "\n"
	})
	for range 3 {
		d, err := s.ProbeCwd(context.Background())
		if err != nil || d != "/srv/www" {
			t.Fatalf("%q %v", d, err)
		}
	}
	if n, m := shells.Load(), oneShots.Load(); n != 1 || m != 0 {
		t.Fatalf("%d probe shells, %d one-shot reads; want 1 and 0", n, m)
	}
	s.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe shell outlived the session")
	}
}

// TestProbeCwdIdle: a probe shell unused a while goes; the next read
// starts another.
func TestProbeCwdIdle(t *testing.T) {
	old := cwdShellIdle
	cwdShellIdle = 50 * time.Millisecond
	t.Cleanup(func() { cwdShellIdle = old })
	s, shells, _, ended := probeServer(t, func(line, mark string) string {
		return mark + " 0 /srv " + mark + "\n"
	})
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/srv" {
		t.Fatalf("%q %v", d, err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle probe shell stayed")
	}
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/srv" || shells.Load() != 2 {
		t.Fatalf("%q %v, %d probe shells", d, err, shells.Load())
	}
}

// TestProbeCwdNoShell: a server where the probe shell does not run is
// read one-shot, without trying the probe shell again.
func TestProbeCwdNoShell(t *testing.T) {
	srv := server(t)
	var shells atomic.Int32
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		if cmd == "exec sh" {
			shells.Add(1)
			return 127
		}
		io.WriteString(ch, "/oneshot\n")
		return 0
	}
	s := New(Config{Addr: srv.Addr, User: "demo", HostKey: trusted(t, srv).Callback(), Credentials: password("secret")}, Options{}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	for range 3 {
		if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/oneshot" {
			t.Fatalf("%q %v", d, err)
		}
	}
	if n := shells.Load(); n != 1 {
		t.Fatalf("started the probe shell %d times", n)
	}
}

// TestProbeCwdBroken: a probe shell that does not answer in time is
// closed and the next read starts another; one printing a line longer
// than cwdLineMax is let go of, the read done one-shot.
func TestProbeCwdBroken(t *testing.T) {
	var mode atomic.Int32 // 0 answers, 1 is silent, 2 floods
	s, shells, oneShots, ended := probeServer(t, func(line, mark string) string {
		switch mode.Load() {
		case 1:
			return ""
		case 2:
			return strings.Repeat("x", cwdLineMax+1)
		}
		return mark + " 0 /srv " + mark + "\n"
	})
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/srv" {
		t.Fatalf("%q %v", d, err)
	}
	mode.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := s.ProbeCwd(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a silent probe shell: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the silent probe shell was not closed")
	}
	mode.Store(0)
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/srv" || shells.Load() != 2 {
		t.Fatalf("after a timeout: %q %v, %d probe shells", d, err, shells.Load())
	}
	mode.Store(2)
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/oneshot" || oneShots.Load() != 1 {
		t.Fatalf("a flood: %q %v, %d one-shot reads", d, err, oneShots.Load())
	}
	mode.Store(0)
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/srv" || shells.Load() != 3 {
		t.Fatalf("after a flood: %q %v, %d probe shells", d, err, shells.Load())
	}
}
