//go:build !windows

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalShell: a local session runs the user's shell on a pty of the
// terminal's size, and ends, Exited, with it.
func TestLocalShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s := New(Config{}, Options{Local: true}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	if s.Client() != nil {
		t.Fatal("a local session has an SSH client")
	}
	if _, tagged := s.Tag(); tagged {
		t.Fatal("a local shell is tagged")
	}
	s.Write([]byte("echo out-$((40+2)); stty size\n"))
	waitFor(t, "the shell's output", func() bool { return strings.Contains(s.Term.ScreenText(), "out-42") })
	waitFor(t, "the pty's size", func() bool { return strings.Contains(s.Term.ScreenText(), "24 80") })
	s.Resize(100, 30)
	s.Write([]byte("stty size\n"))
	waitFor(t, "the new size", func() bool { return strings.Contains(s.Term.ScreenText(), "30 100") })
	s.Write([]byte("exit\n"))
	waitState(t, s, Exited)
}

// TestLocalClose: closing a local session ends its shell.
func TestLocalClose(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s := New(Config{}, Options{Local: true}, 80, 24)
	s.Start()
	waitState(t, s, Connected)
	s.Close()
	<-s.Done()
}

// TestLocalShellStartsInDir: a local shell runs in the folder asked for,
// not the user's home.
func TestLocalShellStartsInDir(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "cwd")
	sh := Shell{Path: "/bin/sh", Args: []string{"-c", `pwd -P > "$SSHYARD_TEST_CWD"`}, Env: []string{"SSHYARD_TEST_CWD=" + out}}
	p, err := startLocal(sh, dir, 80, 24, "xterm")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := dir
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		want = real
	}
	if got := strings.TrimSpace(string(b)); got != want {
		t.Errorf("shell started in %q, want %q", got, want)
	}
}
