package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/egoist/mygo/ui"
)

// editorOn opens the file name of a files pane from its row's menu, in
// the editor or with the questions opening it asks.
func editorOn(t *testing.T, a *App, tt interface {
	RightClick(string) error
	Frame()
}, name string) {
	t.Helper()
	if err := tt.RightClick(name); err != nil {
		t.Fatal(err)
	}
	if err := chooseMenu(a, tt, "Edit"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

// A remote file opens in the editor in a window of its own, on its own
// connection; saving writes it back, asking when the server's changed.
func TestEditorWindow(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	wins := fakeWindows(a)
	file := filepath.Join(root, "notes.txt")
	write(t, file, "one\n")
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("notes.txt") })
	editorOn(t, a, tt, "notes.txt")
	w, et, ec := openedWindow(t, a, tt, wins, 1)
	if len(a.ws.Tabs) != 1 || a.focused() != fc {
		t.Fatalf("the editor took a pane: %v", a.ws.Items())
	}
	ed := shown(ec)
	if ed == nil || ed.st.Buffer.String() != "one\n" || !et.HasText("notes.txt") {
		t.Fatalf("editor shows %+v", ed)
	}
	waitFor(t, et, "editor connected", func() bool { return ec.fs.remote.client != nil })
	waitFor(t, et, "title", func() bool { title, _, _, _ := w.state(); return title == "notes.txt — box" })

	// Edit and save: the file is what the editor holds.
	ed.st.Buffer.Set("two\nlines\n")
	et.Frame()
	if !et.HasText("Edited") {
		t.Error("not marked edited")
	}
	waitFor(t, et, "window edited", func() bool { _, edited, _, _ := w.state(); return edited })
	mustClick(t, et, "Save")
	waitFor(t, et, "saved", func() bool { return read(file) == "two\nlines\n" && !ed.saving })
	et.Frame()
	if ed.st.Buffer.Modified() || et.HasText("Edited") {
		t.Error("still edited after saving")
	}

	// Changed on the server meanwhile: asks; reload takes the server's.
	write(t, file, "changed elsewhere\n")
	ed.st.Buffer.Set("mine\n")
	et.Frame()
	a.saveTab(ec, ed, false)
	waitFor(t, et, "conflict", func() bool { return et.HasText("Changed on the server") })
	mustClick(t, et, "Reload")
	waitFor(t, et, "reloaded", func() bool { return ed.st.Buffer.String() == "changed elsewhere\n" && !ed.loading })
	// or replace it.
	write(t, file, "changed again!\n")
	ed.st.Buffer.Set("mine\n")
	a.saveTab(ec, ed, false)
	waitFor(t, et, "conflict", func() bool { return et.HasText("Changed on the server") })
	mustClick(t, et, "Replace")
	waitFor(t, et, "replaced", func() bool { return read(file) == "mine\n" })

	// Opened again, the window comes to the front: no second one.
	editorOn(t, a, tt, "notes.txt")
	waitFor(t, tt, "focused", func() bool { _, _, _, n := w.state(); return n == 1 })
	if len(wins.list()) != 1 {
		t.Fatal("a second window on the same file")
	}

	// The syntax can be chosen; plain text too.
	if ed.lang != "" && ed.st.Buffer.Language() == nil {
		t.Errorf("syntax %q without a language", ed.lang)
	}

	// Closing with unsaved changes asks; Cancel keeps it, Don't Save
	// closes it and its connection.
	ed.st.Buffer.Set("unsaved\n")
	et.Frame()
	if w.userClose() {
		t.Fatal("closed with unsaved changes")
	}
	waitFor(t, et, "close question", func() bool { return et.HasText("Save changes to “notes.txt”?") })
	mustClick(t, et, "Cancel")
	if _, _, closed, _ := w.state(); closed || a.conns[ec.id] != ec {
		t.Fatal("canceling closed it")
	}
	w.userClose()
	waitFor(t, et, "close question", func() bool { return et.HasText("Don’t Save") })
	mustClick(t, et, "Don’t Save")
	waitFor(t, tt, "closed", func() bool { _, _, closed, _ := w.state(); return closed && a.conns[ec.id] == nil })
	if read(file) != "mine\n" {
		t.Errorf("Don't Save saved: %q", read(file))
	}
}

// Save in the close question saves, then closes.
func TestEditorWindowSaveOnClose(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	wins := fakeWindows(a)
	file := filepath.Join(root, "main.go")
	write(t, file, "package main\n")
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("main.go") })
	editorOn(t, a, tt, "main.go")
	w, et, ec := openedWindow(t, a, tt, wins, 1)
	ed := shown(ec)
	if ed.lang != "Go" || !et.HasText("Go") {
		t.Errorf("syntax %q", ed.lang)
	}
	ed.st.Buffer.Set("package other\n")
	et.Frame()
	w.userClose()
	waitFor(t, et, "close question", func() bool { return et.HasText("Save changes to “main.go”?") })
	// Save, the default button: the toolbar's Save shares the name.
	et.Key(0, ui.KeyEnter)
	waitFor(t, tt, "saved and closed", func() bool { _, _, closed, _ := w.state(); return closed && a.conns[ec.id] == nil })
	if read(file) != "package other\n" {
		t.Errorf("saved %q", read(file))
	}
}

// A local file opens in the editor too, and saves to disk: nothing is
// uploaded.
func TestEditorLocalFile(t *testing.T) {
	a, tt, fc, dir := localPane(t, "local.txt")
	wins := fakeWindows(a)
	file := filepath.Join(dir, "local.txt")
	waitFor(t, tt, "listing", func() bool { return tt.HasText("local.txt") })
	mustClick(t, tt, "local.txt")
	mustClick(t, tt, "local.txt")
	w, et, ec := openedWindow(t, a, tt, wins, 1)
	ed := shown(ec)
	if !ec.ed.local || ec.s != nil || ed.st.Buffer.String() != "local.txt" {
		t.Fatalf("local editor %+v", ed)
	}
	waitFor(t, et, "title", func() bool { title, _, _, _ := w.state(); return title == "local.txt — Local" })
	ed.st.Buffer.Set("edited\n")
	et.Frame()
	mustClick(t, et, "Save")
	waitFor(t, et, "saved", func() bool { return read(file) == "edited\n" && !ed.saving })
	if n := len(fc.fs.transfers.List()); n != 0 {
		t.Fatalf("%d transfers for a local edit", n)
	}
	// Changed on disk meanwhile: asks.
	write(t, file, "theirs\n")
	later := time.Now().Add(time.Minute)
	os.Chtimes(file, later, later)
	ed.st.Buffer.Set("mine\n")
	a.saveTab(ec, ed, false)
	waitFor(t, et, "conflict", func() bool { return et.HasText("Changed on disk") })
	mustClick(t, et, "Reload")
	waitFor(t, et, "reloaded", func() bool { return ed.st.Buffer.String() == "theirs\n" && !ed.loading })
	w.userClose()
	waitFor(t, tt, "closed", func() bool { return a.conns[ec.id] == nil })
}

func TestOpenConfirmations(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	wins := fakeWindows(a)
	s := a.settings
	s.OpenConfirmMB = 1
	a.ApplySettings(s)
	write(t, filepath.Join(root, "big.log"), strings.Repeat("x", 2<<20))
	write(t, filepath.Join(root, "blob.dat"), "a\x00b")
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("big.log") && tt.HasText("blob.dat") })

	editorOn(t, a, tt, "big.log")
	if !tt.HasText("Open a large file?") || !tt.HasText("2.0 MB") {
		t.Fatalf("no large file question in %q", tt.Texts())
	}
	mustClick(t, tt, "Cancel")
	tt.Frame()
	if len(wins.list()) != 0 || tt.HasText("Open a large file?") {
		t.Fatal("opened after cancel")
	}
	editorOn(t, a, tt, "big.log")
	mustClick(t, tt, "Open")
	_, _, ec := openedWindow(t, a, tt, wins, 1)
	ed := shown(ec)
	if ed.st.Buffer.Len() != 2<<20 {
		t.Fatalf("read %d", ed.st.Buffer.Len())
	}

	// Not text: asks how to open it, as text too, in a tab of the same
	// window.
	editorOn(t, a, tt, "blob.dat")
	waitFor(t, tt, "open with", func() bool { return tt.HasText("Open “blob.dat”") })
	mustClick(t, tt, "Open as Text")
	waitFor(t, tt, "second tab", func() bool { return len(ec.ed.tabs) == 2 })
	if n := len(wins.list()); n != 1 {
		t.Fatalf("%d windows for one host", n)
	}
	bd := shown(ec)
	if bd.name != "blob.dat" || bd.st.Buffer.String() != "a\x00b" {
		t.Fatalf("opened %q", bd.st.Buffer.String())
	}
}

func TestOpenLargeBinaryRequiresBothConfirmations(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	wins := fakeWindows(a)
	settings := a.settings
	settings.OpenConfirmMB = 1
	a.ApplySettings(settings)
	write(t, filepath.Join(root, "big.dat"), "\x00"+strings.Repeat("x", (2<<20)-1))
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("big.dat") })
	editorOn(t, a, tt, "big.dat")
	if !tt.HasText("Open a large file?") {
		t.Fatal("missing size confirmation")
	}
	mustClick(t, tt, "Open")
	waitFor(t, tt, "independent binary question", func() bool { return tt.HasText("Open “big.dat”") })
	if len(wins.list()) != 0 {
		t.Fatal("size confirmation also opened binary content")
	}
	mustClick(t, tt, "Cancel")
	tt.Frame()
	if len(wins.list()) != 0 || fc.fs.openOpen || fc.fs.withOpen {
		t.Fatal("binary cancel opened an editor")
	}
	editorOn(t, a, tt, "big.dat")
	if !tt.HasText("Open a large file?") {
		t.Fatal("missing second size confirmation")
	}
	mustClick(t, tt, "Open")
	waitFor(t, tt, "binary question", func() bool { return tt.HasText("Open “big.dat”") })
	mustClick(t, tt, "Open as Text")
	_, _, ec := openedWindow(t, a, tt, wins, 1)
	ed := shown(ec)
	if ed.name != "big.dat" || ed.st.Buffer.Len() != 2<<20 {
		t.Fatalf("opened %q, %d", ed.name, ed.st.Buffer.Len())
	}
}

func TestOpenConfirmationCannotBypassHardReadLimit(t *testing.T) {
	for _, threshold := range []int{10, 512} {
		t.Run(strconv.Itoa(threshold), func(t *testing.T) {
			a, tt, fc, root := filesPane(t)
			wins := fakeWindows(a)
			settings := a.settings
			settings.OpenConfirmMB = threshold
			a.ApplySettings(settings)
			file, err := os.Create(filepath.Join(root, "oversized.dat"))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(editorHardReadLimit + 1); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			a.refreshSides(fc)
			waitFor(t, tt, "listing", func() bool { return tt.HasText("oversized.dat") })
			editorOn(t, a, tt, "oversized.dat")
			if threshold < 256 {
				if !tt.HasText("Open a large file?") {
					t.Fatal("missing size confirmation")
				}
				mustClick(t, tt, "Open")
			}
			waitFor(t, tt, "hard size limit", func() bool { return tt.HasText("Couldn’t open oversized.dat: The file exceeds the read limit") })
			if len(wins.list()) != 0 || fc.fs.openOpen || fc.fs.withOpen {
				t.Fatal("oversized file passed the hard limit")
			}
		})
	}
}

// A remote file opens in another app from a tracked copy in the cache,
// which only the user can read. A file
// made for another app asks how to open it; the app can be chosen.
func TestOpenWithDefaultApp(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	var mu sync.Mutex
	var opened, chosen []string
	a.p.OpenPath = func(p string) error { mu.Lock(); opened = append(opened, p); mu.Unlock(); return nil }
	a.p.OpenWith = func(p, title string) error { mu.Lock(); chosen = append(chosen, p+"|"+title); mu.Unlock(); return nil }
	a.p.CacheDir = filepath.Join(t.TempDir(), "cache")
	a.settings.ExternalUpload = settings.ExternalUploadAsk
	count := func(s *[]string) int { mu.Lock(); defer mu.Unlock(); return len(*s) }
	file := filepath.Join(root, "conf.ini")
	write(t, file, "a=1\n")
	write(t, filepath.Join(root, "doc.pdf"), "%PDF-1.4")
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("conf.ini") && tt.HasText("doc.pdf") })
	tt.RightClick("conf.ini")
	if err := chooseMenu(a, tt, "Open With Default App"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "opened", func() bool { return count(&opened) == 1 })
	local := opened[0]
	if pane := filepath.Base(filepath.Dir(filepath.Dir(local))); !strings.HasPrefix(pane, strconv.Itoa(os.Getpid())+"-") {
		t.Errorf("pane folder %q not named for the process", pane)
	}
	if !strings.HasPrefix(local, a.p.CacheDir) || filepath.Base(local) != "conf.ini" || read(local) != "a=1\n" {
		t.Fatalf("opened %q", local)
	}
	wantMode := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		// Chmod only toggles read-only, so a writable copy is 0666.
		wantMode = 0o666
	}
	if fi, _ := os.Stat(local); fi.Mode().Perm() != wantMode {
		t.Errorf("copy mode %v", fi.Mode())
	}
	if ed := fc.fs.externalEdit(filepath.ToSlash(file)); ed == nil || ed.local != local || ed.state != extWatching {
		t.Fatalf("default app opened an untracked copy: %+v", ed)
	}
	// Saving it there waits for approval before uploading.
	write(t, local, "a=222\n")
	if len(fc.fs.extUnsynced()) != 1 {
		t.Fatal("the new save was absent from the close check")
	}
	if read(file) != "a=1\n" {
		t.Fatalf("uploaded %q", read(file))
	}

	// A document asks how to open it; its default app, or one chosen.
	mustClick(t, tt, "doc.pdf")
	mustClick(t, tt, "doc.pdf")
	waitFor(t, tt, "open with", func() bool { return tt.HasText("Open “doc.pdf”") })
	mustClick(t, tt, "Choose App…")
	waitFor(t, tt, "chosen", func() bool { return count(&chosen) == 1 })
	if p, title, _ := strings.Cut(chosen[0], "|"); filepath.Base(p) != "doc.pdf" || read(p) != "%PDF-1.4" || title != "Choose an App to Open “doc.pdf”" {
		t.Fatalf("chose for %q", chosen[0])
	}
	if count(&opened) != 1 {
		t.Fatal("the default app opened it too")
	}
	// Return the local copy to its original contents before closing.
	write(t, local, "a=1\n")
	ed := fc.fs.externalEdit(filepath.ToSlash(file))
	if ed == nil {
		t.Fatal("the session disappeared")
	}
	sample, err := readExternalCopy(local, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.externalPolled(fc, extPoll{ed: ed, epoch: ed.epoch}, time.Now(), sample, nil)

	// Closing the pane deletes the copies.
	a.CloseSession(fc.id)
	waitFor(t, tt, "copy deleted", func() bool { _, err := os.Stat(local); return os.IsNotExist(err) })
	es, _ := os.ReadDir(a.p.CacheDir)
	if len(es) != 0 {
		t.Errorf("left in the cache: %v", es)
	}
}

func TestCleanCache(t *testing.T) {
	root := t.TempDir()
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) string {
		d := filepath.Join(root, name)
		os.MkdirAll(d, 0o700)
		write(t, filepath.Join(d, "f.txt"), "copy")
		return d
	}
	gone := []string{
		mk(strconv.Itoa(dead.Process.Pid) + "-0123456789ab"),
		mk("abcdef012345"),
	}
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(gone[1], old, old)
	kept := []string{
		mk(strconv.Itoa(os.Getpid()) + "-0123456789ab"),
		mk(strconv.Itoa(os.Getppid()) + "-0123456789ab"), // alive
		mk("fedcba987654"), // recent, old style
		mk("notes"),
		mk("12-not-hex-at-all"),
	}
	cleanCache(root)
	for _, d := range gone {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s left", filepath.Base(d))
		}
	}
	for _, d := range kept {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed", filepath.Base(d))
		}
	}
}

// A copy that could not be written leaves no folder behind, and later
// copies get folders of their own. The files are not text: read for the
// editor, what was read is the copy.
func TestOpenWithDefaultAppAfterFailedCopy(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	var mu sync.Mutex
	var opened []string
	a.p.OpenPath = func(p string) error { mu.Lock(); opened = append(opened, p); mu.Unlock(); return nil }
	a.p.CacheDir = filepath.Join(t.TempDir(), "cache")
	write(t, filepath.Join(root, "a.dat"), "A\x00")
	write(t, filepath.Join(root, "b.dat"), "B\x00")
	var failed atomic.Bool
	old := writeLocalCopy
	writeLocalCopy = func(name string, data []byte, perm os.FileMode) error {
		if failed.CompareAndSwap(false, true) {
			os.WriteFile(name, data[:0], perm) // a partial copy
			return errors.New("disk full")
		}
		return old(name, data, perm)
	}
	t.Cleanup(func() { writeLocalCopy = old })
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("a.dat") && tt.HasText("b.dat") })
	ask := func(name string) {
		t.Helper()
		editorOn(t, a, tt, name)
		waitFor(t, tt, "open with "+name, func() bool { return tt.HasText("Open “" + name + "”") })
		mustClick(t, tt, "Open With Default App")
	}
	open := func(name string, n int) string {
		t.Helper()
		ask(name)
		var got string
		waitFor(t, tt, "opened "+name, func() bool {
			mu.Lock()
			defer mu.Unlock()
			if len(opened) < n {
				return false
			}
			got = opened[n-1]
			return true
		})
		return got
	}
	ask("a.dat")
	waitFor(t, tt, "failed copy", func() bool { return tt.HasText("disk full") })
	b := open("b.dat", 1)
	ac := open("a.dat", 2)
	if read(b) != "B\x00" || filepath.Base(b) != "b.dat" || read(ac) != "A\x00" || filepath.Base(ac) != "a.dat" {
		t.Fatalf("opened %q, %q", b, ac)
	}
	if filepath.Dir(b) == filepath.Dir(ac) {
		t.Fatalf("copies share a folder: %q", filepath.Dir(b))
	}
	subs, _ := os.ReadDir(filepath.Dir(filepath.Dir(b)))
	if len(subs) != 2 {
		t.Errorf("copy folders %v, want the two copies'", subs)
	}
	a.CloseSession(fc.id)
}

func TestAppFile(t *testing.T) {
	for name, want := range map[string]bool{"a.PDF": true, "x.tar.gz": true, "song.mp3": true, "main.go": false, "README": false, "notes.txt": false, ".bashrc": false} {
		if appFile(name) != want {
			t.Errorf("appFile(%q) = %v", name, !want)
		}
	}
}
