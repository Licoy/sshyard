package app

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/session"
	"golang.org/x/crypto/ssh"
)

func TestReturnDirCmd(t *testing.T) {
	for _, tc := range []struct {
		dir, start, shell, want string
	}{
		{"/srv/app", "/home/demo", "/bin/bash", "cd '/srv/app'"},
		{"/srv/it's here", "/home/demo", "/usr/bin/zsh", `cd '/srv/it'\''s here'`},
		{"/srv/$HOME `x` ;rm", "/home/demo", "/bin/sh", "cd '/srv/$HOME `x` ;rm'"},
		{"/srv/a/../b/", "/home/demo", "-bash", "cd '/srv/b'"},
		{"/srv/app", "/home/demo", "/usr/local/bin/fish", "cd '/srv/app'"},
		// fish reads a backslash in single quotes.
		{`/srv/a\'b`, "/home/demo", "/usr/bin/fish", ""},
		{`/srv/a\b`, "/home/demo", "/bin/bash", `cd '/srv/a\b'`},
		// Already there.
		{"/home/demo", "/home/demo", "/bin/bash", ""},
		{"/home/demo/", "/home/demo", "/bin/bash", ""},
		// Not a folder that can be typed safely.
		{"relative/dir", "/home/demo", "/bin/bash", ""},
		{"", "/home/demo", "/bin/bash", ""},
		{"/srv/a\nrm -rf ~", "/home/demo", "/bin/bash", ""},
		{"/srv/a\rb", "/home/demo", "/bin/bash", ""},
		{"/srv/a\x1b[2Jb", "/home/demo", "/bin/bash", ""},
		{"/srv/a\u0085b", "/home/demo", "/bin/bash", ""},
		{"/srv/\xff", "/home/demo", "/bin/bash", ""},
		// Shells whose quoting is not known, or none told.
		{"/srv/app", "/home/demo", "/bin/tcsh", ""},
		{"/srv/app", "/home/demo", "/usr/bin/pwsh", ""},
		{"/srv/app", "/home/demo", "/usr/bin/nu", ""},
		{"/srv/app", "/home/demo", `"$SHELL"`, ""},
		{"/srv/app", "/home/demo", "", ""},
		{"/srv/文件夹 1", "/home/demo", "/bin/dash", "cd '/srv/文件夹 1'"},
	} {
		if got := returnDirCmd(tc.dir, tc.start, tc.shell); got != tc.want {
			t.Errorf("returnDirCmd(%q, %q, %q) = %q, want %q", tc.dir, tc.start, tc.shell, got, tc.want)
		}
	}
}

func TestStartDirOf(t *testing.T) {
	for _, tc := range []struct{ dir, home, want string }{
		{"", "/home/demo", "/home/demo"},
		{"~", "/home/demo/", "/home/demo"},
		{"~/work/api", "/home/demo", "/home/demo/work/api"},
		{"/srv/app/", "/home/demo", "/srv/app"},
		{"work", "/home/demo", "/home/demo/work"},
	} {
		if got := startDirOf(tc.dir, tc.home); got != tc.want {
			t.Errorf("startDirOf(%q, %q) = %q, want %q", tc.dir, tc.home, got, tc.want)
		}
	}
}

func TestReturnDirWanted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		on       bool
		connects int
		back     string
		tmux     string
		ran      bool
		want     bool
	}{
		{"a reconnect", true, 2, "/srv", "", false, true},
		{"setting off", false, 2, "/srv", "", false, false},
		{"first connect", true, 1, "/srv", "", false, false},
		{"no folder reported", true, 3, "", "", false, false},
		{"tmux attaches", true, 2, "/srv", "tmux attach-session -t '$1'", false, false},
		{"opened for a command", true, 2, "/srv", "", true, false},
	} {
		if got := returnDirWanted(tc.on, tc.connects, tc.back, tc.tmux, tc.ran); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBackDir: a starting shell goes back to the folder the shells before
// it last reported, not to one its own reported already.
func TestBackDir(t *testing.T) {
	c := &conn{start: &shellStart{}}
	back := func(n int) string {
		c.start.mu.Lock()
		defer c.start.mu.Unlock()
		return c.start.backDir(n)
	}
	if d := back(2); d != "" {
		t.Fatalf("back to %q with none reported", d)
	}
	c.noteCwd("/srv/a", 1)
	c.noteCwd("/srv/b", 1)
	if d := back(1); d != "" {
		t.Fatalf("the first shell goes back to its own %q", d)
	}
	if d := back(2); d != "/srv/b" {
		t.Fatalf("back to %q, want /srv/b", d)
	}
	// The new shell reported its home before it was asked: still the
	// old one's.
	c.noteCwd("/home/demo", 2)
	if d := back(2); d != "/srv/b" {
		t.Fatalf("back to %q, want /srv/b", d)
	}
	if d := back(3); d != "/home/demo" {
		t.Fatalf("back to %q, want /home/demo", d)
	}
	// A terminal without a shell start (local, Telnet, serial) keeps none.
	(&conn{}).noteCwd("/x", 1)
}

func TestRetryNowAfterNetworkChange(t *testing.T) {
	ssh := assets.Host{ID: "h1"}
	for _, tc := range []struct {
		name string
		h    assets.Host
		st   session.State
		want bool
	}{
		{"waiting SSH", ssh, session.Reconnecting, true},
		{"connected SSH", ssh, session.Connected, false},
		{"failed SSH", ssh, session.Failed, false},
		{"disconnected SSH", ssh, session.Disconnected, false},
		{"Telnet", assets.Host{ID: "t", Protocol: assets.ProtoTelnet}, session.Reconnecting, false},
		{"serial", assets.Host{ID: "s", Protocol: assets.ProtoSerial}, session.Reconnecting, false},
		{"local", assets.Host{ID: localID + "zsh"}, session.Reconnecting, false},
	} {
		if got := retryNow(tc.h, tc.st); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// returnExec answers the question of the user's shell and home.
type returnExec struct {
	mu          sync.Mutex
	shell, home string
	asked       int
}

func (x *returnExec) run(ch ssh.Channel, cmd string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	if cmd != returnDirScript {
		return 127
	}
	x.asked++
	io.WriteString(ch, x.shell+"\n"+x.home+"\n")
	return 0
}

func (x *returnExec) set(shell, home string) {
	x.mu.Lock()
	x.shell, x.home = shell, home
	x.mu.Unlock()
}

// TestReturnToFolderAfterReconnect: a terminal whose connection came back
// is taken back to the folder its shell reported, quoted, once the new
// shell is at its prompt; not for a shell whose quoting is not known,
// with the setting off, nor when it starts there anyway.
func TestReturnToFolderAfterReconnect(t *testing.T) {
	x := &returnExec{shell: "/bin/bash", home: "/home/demo"}
	srv := startMetricSrv(t, x.run)
	a, tt := newTestApp(t)
	if !a.Settings().ReturnToFolder {
		t.Fatal("returning to the folder must default on")
	}
	cn := connected(t, a, tt, srv)
	cn.s.Term.WriteString("\x1b]7;file:///srv/it's%20here\x07")
	waitFor(t, tt, "the folder reported", func() bool { return cn.cwd == "/srv/it's here" })
	const typed = `you said: cd '/srv/it'\''s here'`
	time.Sleep(dirQuietForTest)
	tt.Frame()
	if strings.Contains(cn.s.Term.ScreenText(), "you said: cd") {
		t.Fatal("a cd typed at the first connect")
	}

	reconnect := func(n int) {
		t.Helper()
		srv.Drop()
		waitFor(t, tt, "reconnected", func() bool {
			st := cn.s.Status()
			return st.State == session.Connected && st.Connects == n
		})
	}
	reconnect(2)
	waitFor(t, tt, "the cd back", func() bool { return strings.Contains(cn.s.Term.ScreenText(), typed) })

	cds := func() int { return strings.Count(cn.s.Term.ScreenText(), "you said: cd") }
	noCd := func(why string) {
		t.Helper()
		time.Sleep(dirQuietForTest)
		tt.Frame()
		if n := cds(); n != 1 {
			t.Fatalf("%s: %d cds typed:\n%s", why, n, cn.s.Term.ScreenText())
		}
	}
	x.set("/bin/tcsh", "/home/demo")
	reconnect(3)
	noCd("a csh")

	x.set("/bin/zsh", "/srv/it's here")
	reconnect(4)
	noCd("starting there")

	x.set("/bin/zsh", "/home/demo")
	s := a.Settings()
	s.ReturnToFolder = false
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	reconnect(5)
	noCd("the setting off")
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.asked != 3 {
		t.Errorf("the server was asked %d times, want once a reconnect while on", x.asked)
	}
}
