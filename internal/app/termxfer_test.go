package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/xfer"
	"github.com/Licoy/sshyard/locales"
)

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func receive(t *testing.T, s *localSink, f xfer.File, data string) string {
	t.Helper()
	rel, w, err := s.Create(f)
	if err != nil {
		t.Fatalf("create %q: %v", f.Name, err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	return rel
}

// TestLocalSinkNoOverwrite: a received file never replaces one there,
// nor one received before it with the same name.
func TestLocalSinkNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "mine")
	s := newLocalSink(dir, false)
	if rel := receive(t, s, xfer.File{Name: "a.txt"}, "one"); rel != "a (1).txt" {
		t.Fatalf("saved as %q", rel)
	}
	if rel := receive(t, s, xfer.File{Name: "a.txt"}, "two"); rel != "a (2).txt" {
		t.Fatalf("saved as %q", rel)
	}
	if rel := receive(t, s, xfer.File{Name: "noext"}, "x"); rel != "noext" {
		t.Fatalf("saved as %q", rel)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "mine" {
		t.Fatalf("the file there became %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "a (2).txt")); got != "two" {
		t.Fatalf("got %q", got)
	}
	if s.count() != 3 {
		t.Fatalf("count %d", s.count())
	}
}

// TestLocalSinkPartial: a file is written as .part until it is all
// there, and an aborted one leaves nothing.
func TestLocalSinkPartial(t *testing.T) {
	dir := t.TempDir()
	s := newLocalSink(dir, false)
	rel, w, err := s.Create(xfer.File{Name: "big.bin"})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("half"))
	if _, err := os.Stat(filepath.Join(dir, rel)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the file shows before it all came")
	}
	if _, err := os.Stat(filepath.Join(dir, rel+".part")); err != nil {
		t.Fatal(err)
	}
	w.Abort()
	left, _ := os.ReadDir(dir)
	if len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	if s.count() != 0 {
		t.Fatalf("count %d", s.count())
	}
}

// TestLocalSinkNames: ZMODEM's names lose the server's folders and an
// unsafe one is skipped; trzsz paths stay below the folder, and an
// unsafe one ends the transfer.
func TestLocalSinkNames(t *testing.T) {
	dir := t.TempDir()
	s := newLocalSink(dir, false)
	if rel := receive(t, s, xfer.File{Name: "/etc/passwd"}, "x"); rel != "passwd" {
		t.Fatalf("saved as %q", rel)
	}
	for _, name := range []string{"..", "a/..", "CON", "bad\x01name", ""} {
		if _, _, err := s.Create(xfer.File{Name: name}); !errors.Is(err, xfer.ErrSkip) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	s = newLocalSink(dir, true)
	for _, name := range []string{"../out", "a/../../out", "a//b", "a/CON"} {
		if _, _, err := s.Create(xfer.File{Name: name}); err == nil || errors.Is(err, xfer.ErrSkip) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "out")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside the folder")
	}
}

// TestLocalSinkFolders: a folder sent whole whose name is taken is saved
// under a new name, with what is in it.
func TestLocalSinkFolders(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "d", "old"), "mine")
	s := newLocalSink(dir, true)
	rel, w, err := s.Create(xfer.File{Name: "d", Dir: true})
	if err != nil || w != nil || rel != "d (1)" {
		t.Fatalf("%q %v %v", rel, w, err)
	}
	if rel := receive(t, s, xfer.File{Name: "d/sub/x.txt"}, "x"); rel != "d (1)/sub/x.txt" {
		t.Fatalf("saved as %q", rel)
	}
	if got := readFile(t, filepath.Join(dir, "d (1)", "sub", "x.txt")); got != "x" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "d", "sub")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote into the folder there")
	}
}

// TestLocalSinkMode: a received file is never made a program.
func TestLocalSinkMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits")
	}
	dir := t.TempDir()
	s := newLocalSink(dir, false)
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	rel := receive(t, s, xfer.File{Name: "run.sh", Mode: 0o4777, ModTime: at}, "x")
	fi, err := os.Stat(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 || fi.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if !fi.ModTime().Equal(at) {
		t.Fatalf("modified %v", fi.ModTime())
	}
}

// TestLocalSource: files go as they are; a folder only where folders
// go, with what is in it below its name, links left out.
func TestLocalSource(t *testing.T) {
	dir := t.TempDir()
	top := filepath.Join(dir, "top")
	writeFile(t, filepath.Join(top, "a"), "aa")
	writeFile(t, filepath.Join(top, "sub", "b"), "b")
	file := filepath.Join(dir, "f.txt")
	writeFile(t, file, "fff")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(file, filepath.Join(top, "link")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newLocalSource([]string{file, top}, false); !errors.Is(err, errXferFolder) {
		t.Fatalf("a folder where files go: %v", err)
	}
	s, err := newLocalSource([]string{file, top}, true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range s.Files() {
		names = append(names, f.Name)
	}
	want := []string{"f.txt", "top", "top/a", "top/sub", "top/sub/b"}
	if !slices.Equal(names, want) {
		t.Fatalf("listed %q", names)
	}
	if s.count() != 3 || s.Files()[0].Size != 3 || !s.Files()[1].Dir {
		t.Fatalf("count %d, %+v", s.count(), s.Files())
	}
	r, err := s.Open(2)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 8)
	n, _ := r.Read(b)
	r.Close()
	if string(b[:n]) != "aa" {
		t.Fatalf("read %q", b[:n])
	}
}

// TestDroppedFiles: files dropped on a terminal go with the transfer
// over the protocol chosen for them, once, and only for a while.
func TestDroppedFiles(t *testing.T) {
	now := time.Now()
	x := &xferPane{}
	x.drop([]string{"/a"}, protoTrzsz, now.Add(time.Minute))
	if got := x.takeDropped(protoZmodem, now); got != nil {
		t.Fatalf("taken by the other protocol: %v", got)
	}
	x.drop([]string{"/a"}, protoTrzsz, now.Add(time.Minute))
	if got := x.takeDropped(protoTrzsz, now.Add(2*time.Minute)); got != nil {
		t.Fatalf("taken too late: %v", got)
	}
	x.drop([]string{"/a"}, protoTrzsz, now.Add(time.Minute))
	if got := x.takeDropped(protoTrzsz, now); !slices.Equal(got, []string{"/a"}) {
		t.Fatalf("got %v", got)
	}
	if got := x.takeDropped(protoTrzsz, now); got != nil {
		t.Fatalf("taken twice: %v", got)
	}
	// An upload with files dropped for it asks nothing.
	a := &App{}
	x.drop([]string{"/b"}, protoTrzsz, time.Now().Add(time.Minute))
	r := a.askXfer(context.Background(), x, 1, protoTrzsz, xferUpload)
	if !slices.Equal(r.paths, []string{"/b"}) {
		t.Fatalf("answer %+v", r)
	}
}

// TestHostTransfersOff: a host saved with transfers off keeps them off,
// and its terminals then take none; a new host takes them.
func TestHostTransfersOff(t *testing.T) {
	a, _ := newTestApp(t)
	m := locales.In("en")
	h, err := a.Store().Add(assets.Host{Name: "box", Address: "example.com", Port: 22, User: "demo", Auth: assets.AuthAsk})
	if err != nil {
		t.Fatal(err)
	}
	sz := []byte("**\x18B00000000000000\r\x8a\x11")
	x := &xferPane{seen: map[string]bool{}}
	if at, _, _ := a.findTransfer(x, h.ID, sz); at < 0 {
		t.Fatal("not taken by default")
	}
	a.openEditor(&h)
	if !a.form.transfers {
		t.Fatal("the editor shows transfers off")
	}
	a.form.transfers = false
	if !a.SaveForm(m) {
		t.Fatal(a.form.err)
	}
	if got, _ := a.Store().Get(h.ID); !got.TransfersOff {
		t.Fatal("not stored")
	}
	if at, _, _ := a.findTransfer(x, h.ID, sz); at >= 0 {
		t.Fatal("taken on a host that turned them off")
	}
	h, _ = a.Store().Get(h.ID)
	a.openEditor(&h)
	if a.form.transfers {
		t.Fatal("the editor shows transfers on")
	}
	a.openEditor(nil)
	if !a.form.transfers {
		t.Fatal("a new host has transfers off")
	}
}

// TestFindTransfer: the shell's transfers are taken while the setting
// and the host let them, the first in the output first, and a trzsz one
// once.
func TestFindTransfer(t *testing.T) {
	st, err := assets.Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	off, err := st.Add(assets.Host{Name: "off", Address: "example.com", Port: 22, User: "u", TransfersOff: true})
	if err != nil {
		t.Fatal(err)
	}
	on, err := st.Add(assets.Host{Name: "on", Address: "example.com", Port: 22, User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: st}
	sz := []byte("$ sz f\r\n**\x18B00000000000000\r\x8a\x11")
	tsz := []byte("$ tsz f\r\n::TRZSZ:TRANSFER:S:1.1.6:1700000000100\r\n")
	x := &xferPane{seen: map[string]bool{}}
	if at, zo, to := a.findTransfer(x, on.ID, sz); at >= 0 || zo != nil || to != nil {
		t.Fatal("taken with the setting off")
	}
	a.xferOn.Store(true)
	if at, zo, _ := a.findTransfer(x, off.ID, sz); at >= 0 || zo != nil {
		t.Fatal("taken on a host that turned them off")
	}
	if at, zo, _ := a.findTransfer(x, on.ID, sz); at != 8 || zo == nil || zo.Upload {
		t.Fatalf("sz: %d %v", at, zo)
	}
	both := append(append([]byte(nil), tsz...), sz...)
	at, zo, to := a.findTransfer(x, on.ID, both)
	if zo != nil || to == nil || at != len(tsz) {
		t.Fatalf("tsz first: %d %v %v", at, zo, to)
	}
	// tmux shows the magic again as it redraws.
	if _, _, to := a.findTransfer(x, on.ID, tsz); to != nil {
		t.Fatal("the same trzsz transfer taken twice")
	}
	// A local terminal has no saved host.
	if at, zo, _ := a.findTransfer(x, "local", sz); at < 0 || zo == nil {
		t.Fatal("not taken on a local terminal")
	}
}
