package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// TestUserScript runs userBody on a /proc of its own: the user is the
// one of the terminal's foreground, the su shell, not the login shell;
// with no foreground (no terminal, or its leader gone) it is the shell's.
func TestUserScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	if err := exec.Command("sh", "-c", `printf 'a\0b\0' | grep -qzx b`).Run(); err != nil {
		t.Skip("this grep has no -z")
	}
	root := t.TempDir()
	tag := "0123abcd"
	proc := func(pid, ppid, comm, env, tpgid, uid string) {
		d := filepath.Join(root, pid)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "environ"), []byte(env), 0o644)
		os.WriteFile(filepath.Join(d, "stat"), []byte(pid+" ("+comm+") S "+ppid+" "+pid+" "+pid+" 34816 "+tpgid+" 4194560"), 0o644)
		os.WriteFile(filepath.Join(d, "status"), []byte("Name:\t"+comm+"\nUid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\nGid:\t0\n"), 0o644)
	}
	tagged := "HOME=/home/demo\x00" + TagEnv + "=" + tag + "\x00"
	run := func() (TermUser, error) {
		t.Helper()
		out, err := exec.Command("sh", "-c", strings.Replace(userBody(tag), "p=/proc", "p="+root, 1)).Output()
		if err != nil {
			return TermUser{}, err
		}
		return parseUser(string(out))
	}
	proc("100", "1", "sshd: demo@pts/0", "HOME=/\x00", "-1", "4242")
	proc("101", "100", "bash", tagged, "103", "4242")
	proc("102", "101", "su", tagged, "103", "0")
	// su - clears the environment: the root shell holds no tag.
	proc("103", "102", "bash", "HOME=/root\x00", "103", "0")
	u, err := run()
	if err != nil || u.LoginUID != 4242 || u.UID != 0 || u.Name != "root" || !u.Switched() || !u.Root() {
		t.Fatalf("after su: %+v %v", u, err)
	}
	os.RemoveAll(filepath.Join(root, "103"))
	os.RemoveAll(filepath.Join(root, "102"))
	proc("101", "100", "bash", tagged, "101", "4242")
	if u, err := run(); err != nil || u.UID != 4242 || u.Switched() || u.Root() {
		t.Fatalf("back in the login shell: %+v %v", u, err)
	}
	proc("101", "100", "bash", tagged, "999", "4242")
	if u, err := run(); err != nil || u.UID != 4242 || u.Switched() {
		t.Fatalf("a foreground gone: %+v %v", u, err)
	}
	if _, err := exec.Command("sh", "-c", strings.Replace(userBody("nope"), "p=/proc", "p="+root, 1)).Output(); err == nil {
		t.Fatal("an unknown tag found a user")
	}
}

func TestParseUser(t *testing.T) {
	u, err := parseUser("1000 0 alice root\n")
	if err != nil || u != (TermUser{LoginUID: 1000, Login: "alice", UID: 0, Name: "root"}) {
		t.Fatalf("%+v %v", u, err)
	}
	if u, err := parseUser("1000 1001 - -"); err != nil || u.Login != "" || u.Name != "" || u.UID != 1001 {
		t.Fatalf("no names: %+v %v", u, err)
	}
	for _, out := range []string{"", "1000 0 alice", "x 0 a b", "1000 -1 a b"} {
		if _, err := parseUser(out); !errors.Is(err, ErrNoUser) {
			t.Errorf("parseUser(%q) = %v", out, err)
		}
	}
}

// TestProbeUser reads the user through the probe shell the directory is
// read through; a shell not found is ErrNoUser.
func TestProbeUser(t *testing.T) {
	var gone atomic.Bool
	s, shells, oneShots, _ := probeServer(t, func(line, mark string) string {
		if !strings.Contains(line, "Uid:") {
			return mark + " 0 /srv " + mark + "\n"
		}
		if gone.Load() {
			return mark + " 1  " + mark + "\n"
		}
		return mark + " 0 1000 0 demo root " + mark + "\n"
	})
	u, err := s.ProbeUser(context.Background())
	if err != nil || !u.Root() || u.Login != "demo" {
		t.Fatalf("%+v %v", u, err)
	}
	if d, err := s.ProbeCwd(context.Background()); err != nil || d != "/srv" {
		t.Fatalf("cwd: %q %v", d, err)
	}
	gone.Store(true)
	if _, err := s.ProbeUser(context.Background()); !errors.Is(err, ErrNoUser) {
		t.Fatalf("no shell: %v", err)
	}
	if n, m := shells.Load(), oneShots.Load(); n != 1 || m != 0 {
		t.Fatalf("%d probe shells, %d one-shot reads; want 1 and 0", n, m)
	}
}
