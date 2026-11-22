package sftpsvc

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Licoy/sshyard/internal/scpfs"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

// connectNoSFTP connects to a server that refuses SFTP and runs commands
// with this computer's shell, in a folder of its own.
func connectNoSFTP(t *testing.T) (*Client, string) {
	t.Helper()
	return connectWithout(t, func(srv *sshtest.Server) { srv.NoSFTP = true })
}

// connectWithout connects to a server set up by without to have no SFTP
// and runs commands with this computer's shell, in a folder of its own.
func connectWithout(t *testing.T, without func(*sshtest.Server)) (*Client, string) {
	t.Helper()
	conn, root := dialWithout(t, without)
	c, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, root
}

// dialWithout is the connection of connectWithout, before SFTP.
func dialWithout(t *testing.T, without func(*sshtest.Server)) (*ssh.Client, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test server runs commands with /bin/sh")
	}
	if _, err := exec.LookPath("scp"); err != nil {
		t.Skip("no scp on this computer")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	srv.Exec = sshtest.ShellExec(root)
	without(srv)
	t.Cleanup(srv.Close)
	kh, _ := session.OpenKnownHosts(filepath.Join(t.TempDir(), "kh"))
	kh.Add(srv.Addr, srv.HostKey.PublicKey())
	conn, err := session.Dial(context.Background(), session.Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(),
		Credentials: func(context.Context) (session.Credentials, error) {
			return session.Credentials{Password: "secret"}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, root
}

func TestSCPFallback(t *testing.T) {
	c, root := connectNoSFTP(t)
	if !c.SCP() {
		t.Fatal("a server without SFTP is not served by scp")
	}
	os.Mkdir(filepath.Join(root, "adir"), 0o755)
	os.WriteFile(filepath.Join(root, "b c.txt"), []byte("bee"), 0o640)
	os.WriteFile(filepath.Join(root, ".hidden"), nil, 0o600)
	os.Symlink(filepath.Join(root, "adir"), filepath.Join(root, "link"))

	home, err := c.Home()
	if err != nil || home != root {
		t.Fatalf("home %q %v, want %q", home, err, root)
	}
	es, err := c.List(home)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, e := range es {
		byName[e.Name] = e
	}
	if len(es) != 4 || !byName["adir"].IsDir || byName["b c.txt"].Size != 3 || byName["b c.txt"].Mode.Perm() != 0o640 ||
		byName["link"].Mode&fs.ModeSymlink == 0 || !byName[".hidden"].Mode.IsRegular() || !byName["adir"].IDs {
		t.Fatalf("listing %+v", es)
	}
	if _, err := c.List(RemoteJoin(home, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("listing a missing folder: %v", err)
	}

	local := t.TempDir()
	data := make([]byte, 1<<20+123)
	rand.Read(data)
	src := filepath.Join(local, "blob.bin")
	os.WriteFile(src, data, 0o640)
	remote, err := c.Upload(context.Background(), src, RemoteJoin(home, "adir"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "adir", "blob.bin")); !bytes.Equal(got, data) {
		t.Fatal("uploaded bytes differ")
	}
	if fi, err := os.Stat(filepath.Join(root, "adir", "blob.bin")); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("uploaded mode %v %v", fi.Mode(), err)
	}
	out := t.TempDir()
	got, err := c.Download(context.Background(), remote, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); !bytes.Equal(b, data) {
		t.Fatal("downloaded bytes differ")
	}
	if _, err := c.Download(context.Background(), RemoteJoin(home, "adir"), out, nil); err == nil {
		t.Error("downloaded a folder")
	}

	// Save over a file: the editor's path.
	p := RemoteJoin(home, "b c.txt")
	b, v, err := c.ReadFile(p)
	if err != nil || string(b) != "bee" {
		t.Fatalf("read %q %v", b, err)
	}
	if _, err := c.WriteFile(p, []byte("hive\n"), v); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b c.txt")); string(b) != "hive\n" {
		t.Errorf("saved %q", b)
	}

	if _, err := c.Mkdir(home, "new dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Mkdir(home, "new dir"); err == nil {
		t.Error("made a folder twice")
	}
	if np, err := c.Rename(RemoteJoin(home, "new dir"), "it's renamed"); err != nil || np != RemoteJoin(home, "it's renamed") {
		t.Fatalf("rename %q %v", np, err)
	}
	if err := c.Chmod(RemoteJoin(home, "b c.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, err := c.Stat(RemoteJoin(home, "b c.txt")); err != nil || e.Mode.Perm() != 0o600 || e.Size != 5 {
		t.Errorf("stat %+v %v", e, err)
	}
	if err := c.Remove(RemoteJoin(home, "b c.txt")); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(RemoteJoin(home, "it's renamed")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "it's renamed")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("folder left: %v", err)
	}
	if c.Exists(RemoteJoin(home, "b c.txt")) {
		t.Error("removed file still there")
	}
}

// A server that takes the sftp subsystem but has no sftp-server to run
// (dropbear, or OpenSSH without the package) is served by scp too.
func TestSCPFallbackMissingServer(t *testing.T) {
	c, root := connectWithout(t, func(srv *sshtest.Server) { srv.SFTPExit = 127 })
	if !c.SCP() {
		t.Fatal("a server whose sftp-server is missing is not served by scp")
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("ay"), 0o644)
	es, err := c.List(root)
	if err != nil || len(es) != 1 || es[0].Name != "a.txt" || es[0].Size != 2 {
		t.Fatalf("listing %+v %v", es, err)
	}
	// The connection remembers it: the next client goes to scp at once.
	again, err := New(c.conn)
	if err != nil || !again.SCP() {
		t.Fatalf("second client scp %v %v", again != nil && again.SCP(), err)
	}
	again.Close()
}

// Without commands either, the failed SFTP handshake is the error.
func TestMissingServerWithoutShell(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	srv.SFTPExit = 127
	t.Cleanup(srv.Close)
	kh, _ := session.OpenKnownHosts(filepath.Join(t.TempDir(), "kh"))
	kh.Add(srv.Addr, srv.HostKey.PublicKey())
	conn, err := session.Dial(context.Background(), session.Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(),
		Credentials: func(context.Context) (session.Credentials, error) {
			return session.Credentials{Password: "secret"}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c, err := New(conn)
	if err == nil {
		c.Close()
		t.Fatal("a server with neither SFTP nor commands gave a client")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Errorf("error %v, want the handshake's", err)
	}
	if !errors.Is(err, scpfs.ErrNoShell) {
		t.Errorf("error %v, want why scp could not stand in", err)
	}
	if withoutSFTP(conn) {
		t.Error("the connection is marked without SFTP though scp could not stand in")
	}
}

// SCPOnly moves files with scp on a server that has SFTP too.
func TestSCPOnly(t *testing.T) {
	conn, root := dialWithout(t, func(*sshtest.Server) {})
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("aye"), 0o644)
	c, err := NewProtocol(context.Background(), conn, SCPOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.SCP() || c.Protocol() != SCPOnly {
		t.Fatalf("scp %v protocol %v", c.SCP(), c.Protocol())
	}
	es, err := c.ListContext(context.Background(), root)
	if err != nil || len(es) != 1 || es[0].Name != "a.txt" {
		t.Fatalf("listed %+v %v", es, err)
	}
	if withoutSFTP(conn) {
		t.Error("a server with SFTP is marked without it")
	}
	auto, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer auto.Close()
	if auto.SCP() {
		t.Error("a client of the same connection left SFTP for scp")
	}
}

// SFTPOnly does not fall back to scp, whether the server refuses SFTP
// or ends it unanswered.
func TestSFTPOnly(t *testing.T) {
	for name, without := range map[string]func(*sshtest.Server){
		"refused": func(srv *sshtest.Server) { srv.NoSFTP = true },
		"missing": func(srv *sshtest.Server) { srv.SFTPExit = 127 },
	} {
		t.Run(name, func(t *testing.T) {
			conn, _ := dialWithout(t, without)
			c, err := NewProtocol(context.Background(), conn, SFTPOnly)
			if err == nil {
				c.Close()
				t.Fatal("a server without SFTP gave an SFTP-only client")
			}
			if name == "refused" && !errors.Is(err, ErrNoSFTP) {
				t.Errorf("error %v, want ErrNoSFTP", err)
			}
			if withoutSFTP(conn) {
				t.Error("the connection is marked without SFTP")
			}
		})
	}
}
