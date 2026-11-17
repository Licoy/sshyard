package app

import (
	"bufio"
	"github.com/Licoy/sshyard/internal/session"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

// terminalBeside opens a terminal beside the Files pane fc, in its tab.
func terminalBeside(t *testing.T, a *App, tt interface{ Frame() }, fc *conn) *conn {
	t.Helper()
	a.Connect(fc.host.ID, openAt{pane: fc.id, edge: layout.Right})
	tt.Frame()
	tc := a.focused()
	if tc == nil || tc.kind != layout.Terminal || a.ws.ActiveTab().Count(layout.Terminal) != 1 {
		t.Fatalf("no terminal beside the files pane: %v", a.ws.Items())
	}
	return tc
}

func TestFilesFollowTerminal(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	write(t, filepath.Join(root, "var", "log", "syslog"), "x")
	write(t, filepath.Join(root, "etc", "hosts"), "x")
	// A shell reports an SFTP path. On Windows that is /C:/..., not the
	// local path the files were created with.
	logs := sftpsvc.RemoteJoin(fc.fs.remote.dir, "var", "log")
	etc := sftpsvc.RemoteJoin(fc.fs.remote.dir, "etc")
	h, _ := a.store.Get(fc.host.ID)
	h.CwdFollow = true // as the host editor makes it
	if _, err := a.store.Update(h); err != nil {
		t.Fatal(err)
	}
	tc := terminalBeside(t, a, tt, fc)
	waitFor(t, tt, "terminal up", func() bool { return !tc.up.IsZero() })

	// The shell reports nothing, and the server cannot tell (it runs no
	// commands): after a while, a line saying so, and the snippet a click
	// away.
	const noReport = "This shell doesn’t report its folder, so Files can’t follow it."
	if tt.HasText(noReport) {
		t.Fatal("notice at once")
	}
	waitFor(t, tt, "probe failed", func() bool {
		tt.Advance(probeEvery)
		return tc.probe == probeFailed
	})
	tt.Move(600, 700) // over nothing that has a tooltip
	tt.Advance(noReportAfter + time.Second)
	tt.Frame()
	if !tt.HasText(noReport) || tt.HasText(cwdSnippet) {
		t.Fatalf("no notice, or the snippet in the pane: %q", tt.Texts())
	}
	a.ws.Activate(fc.id)
	tt.Frame()
	mustClick(t, tt, "How to follow this shell")
	tt.Frame()
	if !tt.HasText(cwdSnippet) {
		t.Fatalf("no snippet in %q", tt.Texts())
	}
	mustClick(t, tt, "Copy")
	tt.Frame()
	if tt.Clipboard() != cwdSnippet {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	mustClick(t, tt, "Close")
	tt.Frame()
	if fc.fs.snippetOpen {
		t.Error("snippet sheet not closed")
	}

	// cd /var/log: the Files pane goes there.
	tc.s.Term.WriteString("\x1b]7;file://box" + logs + "\x07")
	tt.Frame()
	waitFor(t, tt, "followed", func() bool { return fc.fs.remote.dir == logs && tt.HasText("syslog") })
	if tt.HasText(noReport) {
		t.Error("notice after a report")
	}

	// Stopped in the header: it stays.
	mustClick(t, tt, "Follow terminal")
	tc.s.Term.WriteString("\x1b]1337;CurrentDir=" + etc + "\x07")
	tt.Frame()
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if fc.fs.remote.dir != logs {
		t.Fatalf("followed while stopped: %q", fc.fs.remote.dir)
	}
	// Following again catches up.
	mustClick(t, tt, "Follow terminal")
	waitFor(t, tt, "caught up", func() bool { return fc.fs.remote.dir == etc })

	// The host switched off: no button, no following.
	h, _ = a.store.Get(fc.host.ID)
	h.CwdFollow = false
	if _, err := a.store.Update(h); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("Follow terminal") {
		t.Error("follow button with the host switch off")
	}
	tc.s.Term.WriteString("\x1b]7;file://box" + logs + "\x07")
	tt.Frame()
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if fc.fs.remote.dir != etc {
		t.Errorf("followed with the host switch off: %q", fc.fs.remote.dir)
	}
}

// TestFilesFollowProbeUntagged follows a shell the server did not tag (its
// AcceptEnv names no such variable): the server finds it as the
// connection's one terminal, and a read that finds nothing at first, as
// of a shell not yet running, is tried again.
func TestFilesFollowProbeUntagged(t *testing.T) {
	var dir atomic.Value
	dir.Store("")
	var asks atomic.Int32
	a, tt, fc, root := filesPaneOn(t, func(srv *sshtest.Server) {
		srv.RefuseEnv = true
		srv.Exec = func(ch ssh.Channel, cmd string) int {
			if !strings.HasPrefix(cmd, "sh -c ") || strings.Contains(cmd, session.TagEnv) || !strings.Contains(cmd, "/fd/0") {
				return 127
			}
			if asks.Add(1) == 1 {
				return 1
			}
			io.WriteString(ch, dir.Load().(string)+"\n")
			return 0
		}
	})
	write(t, filepath.Join(root, "srv", "app.log"), "x")
	srvDir := sftpsvc.RemoteJoin(fc.fs.remote.dir, "srv")
	dir.Store(srvDir)
	h, _ := a.store.Get(fc.host.ID)
	h.CwdFollow = true
	if _, err := a.store.Update(h); err != nil {
		t.Fatal(err)
	}
	tc := terminalBeside(t, a, tt, fc)
	waitFor(t, tt, "terminal up", func() bool { return !tc.up.IsZero() })
	if _, ok := tc.s.Tag(); ok {
		t.Fatal("tagged by a server that refused it")
	}
	waitFor(t, tt, "followed", func() bool {
		tt.Advance(probeEvery)
		return fc.fs.remote.dir == srvDir && tt.HasText("app.log")
	})
	if tc.probe == probeFailed {
		t.Error("gave up after one miss")
	}
}

// TestFilesFollowProbe follows a shell that does not report its folder:
// the server finds it by its tag, and says where it is.
func TestFilesFollowProbe(t *testing.T) {
	var dir atomic.Value
	dir.Store("")
	a, tt, fc, root := filesPaneOn(t, func(srv *sshtest.Server) {
		srv.Exec = func(ch ssh.Channel, cmd string) int {
			if !strings.HasPrefix(cmd, "sh -c ") || !strings.Contains(cmd, session.TagEnv) {
				return 127
			}
			io.WriteString(ch, dir.Load().(string)+"\n")
			return 0
		}
	})
	write(t, filepath.Join(root, "srv", "app.log"), "x")
	write(t, filepath.Join(root, "srv", "sub", "deep.txt"), "x")
	srvDir := sftpsvc.RemoteJoin(fc.fs.remote.dir, "srv")
	subDir := sftpsvc.RemoteJoin(srvDir, "sub")
	dir.Store(srvDir)
	h, _ := a.store.Get(fc.host.ID)
	h.CwdFollow = true
	if _, err := a.store.Update(h); err != nil {
		t.Fatal(err)
	}
	tc := terminalBeside(t, a, tt, fc)
	waitFor(t, tt, "terminal up", func() bool { return !tc.up.IsZero() })
	if _, ok := tc.s.Tag(); !ok {
		t.Fatal("shell not tagged")
	}
	waitFor(t, tt, "followed", func() bool { return fc.fs.remote.dir == srvDir && tt.HasText("app.log") })
	// cd sub: read again a moment later.
	dir.Store(subDir)
	waitFor(t, tt, "followed again", func() bool {
		tt.Advance(probeEvery)
		return fc.fs.remote.dir == subDir && tt.HasText("deep.txt")
	})
	tt.Advance(noReportAfter + time.Second)
	tt.Frame()
	if tt.HasText("This shell doesn’t report its folder, so Files can’t follow it.") || tc.probe == probeFailed {
		t.Errorf("notice while the server tells: %q", tt.Texts())
	}
}

func TestFilesFollowOnlyTheTerminalsHost(t *testing.T) {
	var asked atomic.Int32
	a, tt, fc, root := filesPaneOn(t, func(srv *sshtest.Server) {
		srv.Exec = func(ch ssh.Channel, cmd string) int {
			if cmd != "hostname" {
				return 127
			}
			asked.Add(1)
			io.WriteString(ch, "web1.example.com\n")
			return 0
		}
	})
	dirs := map[string]string{}
	for _, d := range []string{"a", "b", "c", "d", "e", "f"} {
		write(t, filepath.Join(root, d, "in-"+d), "x")
		dirs[d] = sftpsvc.RemoteJoin(fc.fs.remote.dir, d)
	}
	h, _ := a.store.Get(fc.host.ID)
	h.CwdFollow = true
	if _, err := a.store.Update(h); err != nil {
		t.Fatal(err)
	}
	tc := terminalBeside(t, a, tt, fc)
	waitFor(t, tt, "terminal up", func() bool { return !tc.up.IsZero() })
	report := func(host, dir string) {
		tc.s.Term.WriteString("\x1b]7;file://" + host + dir + "\x07")
		tt.Frame()
	}
	stays := func(what, dir string) {
		t.Helper()
		for range 5 {
			time.Sleep(20 * time.Millisecond)
			tt.Frame()
		}
		if fc.fs.remote.dir != dir {
			t.Fatalf("%s: followed to %q", what, fc.fs.remote.dir)
		}
	}

	// The server's own name, short or full: followed (once asked).
	report("WEB1", dirs["a"])
	waitFor(t, tt, "own short name", func() bool { return fc.fs.remote.dir == dirs["a"] })
	report("web1.example.com", dirs["b"])
	waitFor(t, tt, "own full name", func() bool { return fc.fs.remote.dir == dirs["b"] })
	// No host, localhost and the address in the settings: followed.
	report("", dirs["c"])
	waitFor(t, tt, "no host", func() bool { return fc.fs.remote.dir == dirs["c"] })
	report("localhost", dirs["d"])
	waitFor(t, tt, "localhost", func() bool { return fc.fs.remote.dir == dirs["d"] })
	report("127.0.0.1", dirs["e"])
	waitFor(t, tt, "address", func() bool { return fc.fs.remote.dir == dirs["e"] })

	// ssh db2 inside the terminal: its reports are not this server's.
	report("db2", dirs["f"])
	stays("another host", dirs["e"])
	report("web1.other.net", dirs["f"])
	stays("another domain", dirs["e"])
	// Back out of it: followed again.
	report("web1", dirs["a"])
	waitFor(t, tt, "back", func() bool { return fc.fs.remote.dir == dirs["a"] })
	if n := asked.Load(); n != 1 {
		t.Errorf("asked the hostname %d times, want once per connection", n)
	}
}

func TestSameHostName(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"web1", "web1", true},
		{"web1", "WEB1", true},
		{"web1", "web1.example.com", true},
		{"web1.example.com", "web1", true},
		{"web1.example.com", "web1.other.net", false},
		{"web1", "web2", false},
		{"10", "10.0.0.1", false},
		{"10.0.0.1", "10.0.0.2", false},
		{"", "", false},
	} {
		if got := sameHostName(c.a, c.b); got != c.same {
			t.Errorf("sameHostName(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestFollowDir(t *testing.T) {
	for in, want := range map[string]string{
		"/srv/app":          "/srv/app",
		"/srv/app/":         "/srv/app",
		"/srv/a/../b/./c//": "/srv/b/c",
		"/../..":            "/",
		"/":                 "/",
		"":                  "",
		"srv/app":           "",
		"~/app":             "",
		"../etc":            "",
		"/a\x00b":           "",
	} {
		if got := followDir(in); got != want {
			t.Errorf("followDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSlowProbe: the hint to report the folder is for a server that
// tells, slowly, once a read other than the first (which starts the
// probe shell) was timed.
func TestSlowProbe(t *testing.T) {
	for _, c := range []struct {
		probe probeState
		ok    int
		rtt   time.Duration
		slow  bool
	}{
		{probeIdle, 2, probeSlow, true},
		{probeRunning, 3, time.Second, true},
		{probeIdle, 1, 0, false},
		{probeIdle, 2, probeSlow - time.Millisecond, false},
		{probeFailed, 2, time.Second, false},
	} {
		tc := &conn{probe: c.probe, probeOK: c.ok, probeRTT: c.rtt}
		if got := slowProbe(tc); got != c.slow {
			t.Errorf("slowProbe(%v, %d, %v) = %v", c.probe, c.ok, c.rtt, got)
		}
	}
}

// probeShellMark is the mark of a read typed into a probe shell.
var probeShellMark = regexp.MustCompile(`' ([0-9a-f]{16}) "\$\?" "\$d" ([0-9a-f]{16})\n$`)

// TestFilesFollowProbeAfterEnter follows a shell that does not report its
// folder through one probe shell kept open, read again soon after an
// Enter, not only every probeEvery.
func TestFilesFollowProbeAfterEnter(t *testing.T) {
	var dir atomic.Value
	dir.Store("")
	var shells, reads, oneShots atomic.Int32
	a, tt, fc, root := filesPaneOn(t, func(srv *sshtest.Server) {
		srv.Exec = func(ch ssh.Channel, cmd string) int {
			switch {
			case cmd == "exec sh":
				shells.Add(1)
				r := bufio.NewReader(ch)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return 0
					}
					m := probeShellMark.FindStringSubmatch(line)
					if m == nil || !strings.Contains(line, session.TagEnv) {
						return 2
					}
					reads.Add(1)
					io.WriteString(ch, m[1]+" 0 "+dir.Load().(string)+" "+m[1]+"\n")
				}
			case strings.HasPrefix(cmd, "sh -c "):
				oneShots.Add(1)
			}
			return 127
		}
	})
	write(t, filepath.Join(root, "srv", "app.log"), "x")
	write(t, filepath.Join(root, "srv", "sub", "deep.txt"), "x")
	srvDir := sftpsvc.RemoteJoin(fc.fs.remote.dir, "srv")
	subDir := sftpsvc.RemoteJoin(srvDir, "sub")
	dir.Store(srvDir)
	h, _ := a.store.Get(fc.host.ID)
	h.CwdFollow = true
	if _, err := a.store.Update(h); err != nil {
		t.Fatal(err)
	}
	tc := terminalBeside(t, a, tt, fc)
	waitFor(t, tt, "terminal up", func() bool { return !tc.up.IsZero() })
	waitFor(t, tt, "followed", func() bool { return fc.fs.remote.dir == srvDir && tc.probe == probeIdle })

	// cd sub, Enter: read again in a moment, well before probeEvery.
	dir.Store(subDir)
	before := reads.Load()
	if err := tc.s.Send([]byte("cd sub\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "the Enter seen", func() bool { return tc.enterSeen == tc.s.Enters() && tc.enterSeen != tc.probeEnters })
	tt.Advance(probeAfterEnter)
	waitFor(t, tt, "followed after the Enter", func() bool { return fc.fs.remote.dir == subDir && tt.HasText("deep.txt") })
	if n := reads.Load() - before; n != 1 {
		t.Errorf("%d reads after one Enter", n)
	}
	if n, m := shells.Load(), oneShots.Load(); n != 1 || m != 0 {
		t.Errorf("%d probe shells and %d one-shot reads; want 1 and 0", n, m)
	}
	// Nothing typed: no read before probeEvery.
	before = reads.Load()
	tt.Advance(probeAfterEnter)
	for range 5 {
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
	if reads.Load() != before {
		t.Error("read again with no Enter")
	}
}
