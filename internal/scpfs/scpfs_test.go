package scpfs

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/pkg/sftp"
)

func TestParseListing(t *testing.T) {
	out := "41ed 64 1700000000 501 20 adir\n" +
		"81a4 3 1700000001 0 0 two\nlines\n" +
		"a1ff 10 1700000002 501 20 link\n" +
		"41ed 64 1700000000 501 20 .\n"
	l := parseListing([]byte(out))
	if len(l) != 3 {
		t.Fatalf("%d entries: %+v", len(l), l)
	}
	if !l[0].IsDir() || l[0].Mode().Perm() != 0o755 || l[0].ModTime().Unix() != 1700000000 {
		t.Errorf("folder %+v", l[0])
	}
	if l[1].Name() != "two\nlines" || l[1].Size() != 3 || !l[1].Mode().IsRegular() || l[1].Mode().Perm() != 0o644 {
		t.Errorf("file %+v", l[1])
	}
	if l[2].Mode()&fs.ModeSymlink == 0 || l[2].(fileInfo).Uid() != 501 || l[2].(fileInfo).Gid() != 20 {
		t.Errorf("link %+v", l[2])
	}
}

func TestUnixMode(t *testing.T) {
	for _, tc := range []struct {
		m    uint32
		want fs.FileMode
	}{
		{0o100644, 0o644},
		{0o040755, fs.ModeDir | 0o755},
		{0o041777, fs.ModeDir | fs.ModeSticky | 0o777},
		{0o104755, fs.ModeSetuid | 0o755},
		{0o120777, fs.ModeSymlink | 0o777},
		{0o020620, fs.ModeDevice | fs.ModeCharDevice | 0o620},
		{0o010644, fs.ModeNamedPipe | 0o644},
	} {
		if got := unixMode(tc.m); got != tc.want {
			t.Errorf("%o: %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestLsMode(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want uint32
		ok   bool
	}{
		{"-rw-r--r--", 0o100644, true},
		{"drwxr-xr-x+", 0o040755, true},
		{"drwxrwxrwt", 0o041777, true},
		{"-rwsr-Sr-T", 0o107744, true},
		{"lrwxrwxrwx", 0o120777, true},
		{"crw-rw-rw-", 0o020666, true},
		{"prw-------", 0o010600, true},
		{"81a4", 0, false},
		{"-rw-r--r", 0, false},
		{"?rw-r--r--", 0, false},
	} {
		got, ok := lsMode(tc.s)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %o %v, want %o %v", tc.s, got, ok, tc.want, tc.ok)
		}
	}
	l := parseListing([]byte("drwxr-xr-x 64 1700000000 501 20 adir\n-rw-r--r-- 3 0 0 0 two\nlines\n"))
	if len(l) != 2 || !l[0].IsDir() || l[0].Mode().Perm() != 0o755 || l[0].ModTime().Unix() != 1700000000 ||
		l[1].Name() != "two\nlines" || l[1].Size() != 3 {
		t.Fatalf("%+v", l)
	}
}

func TestQuote(t *testing.T) {
	if got := quote("it's a b"); got != `'it'\''s a b'` {
		t.Errorf("quote %s", got)
	}
}

// TestCatWithoutSCP moves contents with cat on a server without scp.
func TestCatWithoutSCP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test server runs commands with /bin/sh")
	}
	noSCP = true
	t.Cleanup(func() { noSCP = false })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := dialShell(t, root)
	if wd, err := c.Getwd(); err != nil || wd != root {
		t.Fatalf("home %q %v", wd, err)
	}

	data := bytes.Repeat([]byte("0123456789"), 50000)
	f, err := c.Create(filepath.Join(root, "f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "f.bin")); !bytes.Equal(got, data) {
		t.Fatalf("wrote %d bytes, want %d", len(got), len(data))
	}

	// A write in the middle keeps what is around it.
	f, err = c.OpenFile(filepath.Join(root, "f.bin"), os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("xyz"), 10); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{}, data...)
	copy(want[10:], "xyz")
	if got, _ := os.ReadFile(filepath.Join(root, "f.bin")); !bytes.Equal(got, want) {
		t.Fatal("a write in the middle lost the rest")
	}

	r, err := c.Open(filepath.Join(root, "f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes %v", len(got), err)
	}
	if _, err := c.Open(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Errorf("opening a missing file: %v", err)
	}
}

// TestLsWithoutStat lists and stats with ls and date on a server without
// stat, as OpenWrt's busybox.
func TestLsWithoutStat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test server runs commands with /bin/sh")
	}
	noStat = true
	t.Cleanup(func() { noStat = false })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a b.txt"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filepath.Join(root, "a b.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a b.txt", filepath.Join(root, ".link")); err != nil {
		t.Fatal(err)
	}
	c := dialShell(t, root)

	es, err := c.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]os.FileInfo{}
	for _, e := range es {
		got[e.Name()] = e
	}
	if len(got) != 3 {
		t.Fatalf("listed %v", es)
	}
	if f := got["a b.txt"]; f == nil || !f.Mode().IsRegular() || f.Mode().Perm() != 0o640 || f.Size() != 5 || !f.ModTime().Equal(mtime) {
		t.Errorf("file %+v", f)
	}
	if d := got["sub"]; d == nil || !d.IsDir() || d.Mode().Perm() != 0o750 {
		t.Errorf("folder %+v", d)
	}
	if l := got[".link"]; l == nil || l.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link %+v", l)
	}
	if fi, err := c.Stat(filepath.Join(root, ".link")); err != nil || !fi.Mode().IsRegular() || fi.Size() != 5 {
		t.Errorf("stat through the link %+v %v", fi, err)
	}
	if _, err := c.Lstat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Errorf("a missing file: %v", err)
	}
	r, err := c.Open(filepath.Join(root, "a b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("read %q %v", data, err)
	}
}

// dialShell is a client of a server without SFTP whose commands run in
// root.
func dialShell(t *testing.T, root string) *sftp.Client {
	t.Helper()
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	srv.NoSFTP = true
	srv.Exec = sshtest.ShellExec(root)
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
	c, err := NewClient(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
