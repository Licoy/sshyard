package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func externalTestSetup(t *testing.T) (*App, *conn, *dispatchPump) {
	t.Helper()
	skipWindowsSFTP(t)
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	sshConn, err := ssh.Dial("tcp", srv.Addr, &ssh.ClientConfig{
		User: "demo", Auth: []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: ssh.FixedHostKey(srv.HostKey.PublicKey()), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sshConn.Close() })
	client, err := sftpsvc.New(sshConn)
	if err != nil {
		t.Fatal(err)
	}
	cn := &conn{fs: newFilesState(nil)}
	cn.fs.remote.client = client
	cn.fs.transfers.Client = func(context.Context) (*sftpsvc.Client, error) { return client, nil }
	t.Cleanup(cn.fs.close)
	pump := newDispatchPump()
	a := &App{p: Platform{Post: pump.post, CacheDir: t.TempDir(), OpenPath: func(string) error { return nil }}, settings: settings.Default()}
	return a, cn, pump
}

func waitExternal(t *testing.T, pump *dispatchPump, ready func() bool) {
	t.Helper()
	for !ready() {
		pump.next(t)
	}
}

func pollExternalSave(t *testing.T, a *App, cn *conn, ed *extEdit, now time.Time) {
	t.Helper()
	poll := extPoll{ed: ed, epoch: ed.epoch, previous: ed.observed}
	sample, err := pollExternalCopy(poll)
	if err != nil {
		t.Fatal(err)
	}
	a.externalPolled(cn, poll, now, sample, nil)
}

func TestExternalEditAskSkipAndReuse(t *testing.T) {
	a, cn, pump := externalTestSetup(t)
	a.settings.ExternalUpload = settings.ExternalUploadAsk
	opened := make(chan string, 2)
	a.p.OpenPath = func(p string) error { opened <- p; return nil }
	remote := filepath.Join(t.TempDir(), "notes.txt")
	write(t, remote, "server")
	how := extHow{system: true}
	a.editExternally(cn, true, remote, 6, how)
	ed := cn.fs.externalEdit(remote)
	if ed == nil || ed.state != extOpening {
		t.Fatal("the copy is not tracked during opening")
	}
	a.editExternally(cn, true, remote, 6, how)
	waitExternal(t, pump, func() bool { return ed.state == extWatching && !ed.launching })
	if len(cn.fs.edits) != 1 || len(opened) != 1 || <-opened != ed.local || read(ed.local) != "server" {
		t.Fatal("duplicate open did not share the download and launch")
	}
	a.editExternally(cn, true, remote, editorHardReadLimit+1, how)
	waitExternal(t, pump, func() bool { return !ed.launching })
	if len(opened) != 1 || <-opened != ed.local || cn.fs.openOpen {
		t.Fatal("reopening did not reuse the private copy")
	}
	write(t, ed.local, "change in editor")
	if len(cn.fs.extUnsynced()) != 1 {
		t.Fatal("unpolled save did not prevent closing")
	}
	now := time.Now()
	pollExternalSave(t, a, cn, ed, now)
	if ed.state != extChanged || ed.ask {
		t.Fatal("unsettled save was offered for upload")
	}
	pollExternalSave(t, a, cn, ed, now.Add(extSaveDebounce))
	if ed.state != extChanged || !ed.ask || read(remote) != "server" {
		t.Fatal("confirmed save was not waiting for approval")
	}
	write(t, ed.local, "a later local save")
	a.extSkip(cn, ed)
	pollExternalSave(t, a, cn, ed, now.Add(2*extSaveDebounce))
	if ed.state != extWatching || len(cn.fs.extUnsynced()) != 0 || read(remote) != "server" {
		t.Fatal("skipped contents were uploaded or still pending")
	}
	a.extUploadNow(cn, ed)
	waitExternal(t, pump, func() bool { return ed.state != extUploading })
	if read(remote) != "a later local save" || ed.uploads != 1 {
		t.Fatal("explicit upload did not send the previously skipped save")
	}
	a.extStop(cn, ed)
	if len(cn.fs.edits) != 0 || cn.fs.extWatchCancel != nil || ed.ctx.Err() == nil {
		t.Fatal("stopping the last edit left a session or watcher behind")
	}
	if _, err := os.Stat(filepath.Dir(ed.local)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory still exists: %v", err)
	}
}

func TestExternalEditConflictsAndReload(t *testing.T) {
	a, cn, pump := externalTestSetup(t)
	remote := filepath.Join(t.TempDir(), "large.txt")
	data := func(char byte) []byte { return bytes.Repeat([]byte{char}, sftpsvc.SumMax+1) }
	writeBytes := func(p string, data []byte) {
		t.Helper()
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertBytes := func(p string, expected []byte) {
		t.Helper()
		if b, err := os.ReadFile(p); err != nil || !bytes.Equal(b, expected) {
			t.Fatalf("unexpected contents at %s: %d bytes, %v", p, len(b), err)
		}
	}
	writeBytes(remote, data('a'))
	a.editExternally(cn, true, remote, sftpsvc.SumMax+1, extHow{system: true})
	ed := cn.fs.externalEdit(remote)
	waitExternal(t, pump, func() bool { return ed.state == extWatching && !ed.launching })
	if ed.ver.Sum != sha256.Sum256(data('a')) {
		t.Fatal("open did not retain the full digest")
	}
	writeBytes(ed.local, data('b'))
	a.extUploadNow(cn, ed)
	waitExternal(t, pump, func() bool { return ed.state != extUploading })
	if ed.state != extWatching || ed.uploads != 1 || ed.lastUpload.IsZero() || ed.ver.Sum != sha256.Sum256(data('b')) {
		t.Fatalf("normal upload failed: state %v, error %v", ed.state, ed.err)
	}
	assertBytes(remote, data('b'))
	conflict := func(local, server byte) {
		t.Helper()
		writeBytes(ed.local, data(local))
		writeBytes(remote, data(server))
		if err := os.Chtimes(remote, ed.ver.ModTime, ed.ver.ModTime); err != nil {
			t.Fatal(err)
		}
		a.extUploadNow(cn, ed)
		waitExternal(t, pump, func() bool { return ed.state != extUploading })
		if ed.state != extConflict || !errors.Is(ed.err, sftpsvc.ErrChanged) {
			t.Fatalf("same-length server change passed the version check: %v", ed.err)
		}
		assertBytes(remote, data(server))
	}
	conflict('c', 'd')
	a.extOverwrite(cn, ed)
	waitExternal(t, pump, func() bool { return ed.state != extUploading })
	if ed.state != extWatching || ed.uploads != 2 {
		t.Fatalf("explicit overwrite failed: %v", ed.err)
	}
	assertBytes(remote, data('c'))
	conflict('e', 'f')
	a.extTakeServer(cn, ed)
	waitExternal(t, pump, func() bool { return ed.state != extUploading })
	if ed.state != extWatching || ed.uploads != 2 || ed.ver.Sum != sha256.Sum256(data('f')) {
		t.Fatalf("reload failed: %v", ed.err)
	}
	assertBytes(ed.local, data('f'))
	assertBytes(remote, data('f'))
	cn.fs.close()
	if ed.ctx.Err() == nil || len(cn.fs.edits) != 0 || cn.fs.extWatchCancel != nil {
		t.Fatal("pane close left an edit or watcher behind")
	}
	if _, err := os.Stat(ed.local); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pane close retained the private copy: %v", err)
	}
}

func TestExternalEditUploadRetainsNewerSave(t *testing.T) {
	a, cn, pump := externalTestSetup(t)
	remote := filepath.Join(t.TempDir(), "notes.txt")
	write(t, remote, "one")
	a.editExternally(cn, true, remote, 3, extHow{system: true})
	ed := cn.fs.externalEdit(remote)
	waitExternal(t, pump, func() bool { return ed.state == extWatching && !ed.launching })
	get := cn.fs.transfers.Client
	uploading, sent := make(chan struct{}), make(chan struct{})
	cn.fs.transfers.Client = func(ctx context.Context) (*sftpsvc.Client, error) {
		close(uploading)
		select {
		case <-sent:
			return get(ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	write(t, ed.local, "the first change")
	now := time.Now()
	pollExternalSave(t, a, cn, ed, now)
	pollExternalSave(t, a, cn, ed, now.Add(extSaveDebounce))
	select {
	case <-uploading:
	case <-time.After(5 * time.Second):
		t.Fatal("auto upload never read the first edit")
	}
	write(t, ed.local, "the second change")
	close(sent)
	waitExternal(t, pump, func() bool { return ed.state != extUploading })
	if read(remote) != "the first change" || len(cn.fs.extUnsynced()) != 1 {
		t.Fatalf("the newer copy was treated as uploaded: %q", read(remote))
	}
	cn.fs.transfers.Client = get
	pollExternalSave(t, a, cn, ed, now.Add(2*extSaveDebounce))
	pollExternalSave(t, a, cn, ed, now.Add(3*extSaveDebounce))
	waitExternal(t, pump, func() bool { return ed.state != extUploading })
	if read(remote) != "the second change" || len(cn.fs.extUnsynced()) != 0 {
		t.Fatalf("the newer save was not uploaded: %q, %v", read(remote), ed.err)
	}
}

func TestExternalEditOpenRoute(t *testing.T) {
	a, cn, pump := externalTestSetup(t)
	a.settings.FileEditor = settings.FileEditorSystem
	opened := make(chan string, 2)
	a.p.OpenPath = func(p string) error { opened <- p; return nil }
	dir := t.TempDir()
	cn.fs.remote.dir, cn.fs.local.dir = dir, dir
	text := filepath.Join(dir, "text.txt")
	write(t, text, "text")
	a.openEntry(cn, false, sftpsvc.Entry{Name: "text.txt", Size: 4})
	pump.next(t)
	select {
	case p := <-opened:
		if p != text || len(cn.fs.edits) != 0 || cn.fs.copies.dir != "" {
			t.Fatal("a local file did not open directly")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the local file did not open")
	}
	binary := filepath.Join(dir, "blob.data")
	write(t, binary, "x\x00")
	a.openEntry(cn, true, sftpsvc.Entry{Name: "blob.data", Size: 2})
	waitExternal(t, pump, func() bool { return cn.fs.withOpen })
	if len(opened) != 0 || len(cn.fs.edits) != 0 || string(cn.fs.with.data) != "x\x00" {
		t.Fatal("the unknown binary did not ask before opening")
	}
	write(t, binary, "new server data")
	a.openOutside(cn, cn.fs.with, extHow{system: true})
	ed := cn.fs.externalEdit(binary)
	waitExternal(t, pump, func() bool { return ed.state == extWatching && !ed.launching })
	if len(opened) != 1 || read(ed.local) != "x\x00" || ed.ver.Sum != sha256.Sum256([]byte("x\x00")) {
		t.Fatal("opening the binary did not reuse the bytes and version already read")
	}
	var choice string
	a.p.OpenWith = func(p, _ string) error { choice = p; return context.Canceled }
	a.editExternally(cn, true, text, 4, extHow{choose: true})
	before := len(a.toasts)
	waitExternal(t, pump, func() bool { return cn.fs.externalEdit(text) == nil })
	if _, err := os.Stat(choice); choice == "" || !errors.Is(err, os.ErrNotExist) || len(a.toasts) != before {
		t.Fatal("canceling the chooser retained a copy or produced a toast")
	}
}

func TestExternalEditRetryOnlyAfterChangedContent(t *testing.T) {
	a, cn, pump := externalTestSetup(t)
	a.settings.ExternalUpload = settings.ExternalUploadAsk
	remote := filepath.Join(t.TempDir(), "notes.txt")
	write(t, remote, "original")
	a.editExternally(cn, true, remote, 8, extHow{system: true})
	ed := cn.fs.externalEdit(remote)
	waitExternal(t, pump, func() bool { return ed.state == extWatching && !ed.launching })
	now := time.Now()
	if err := os.Chtimes(ed.local, now, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	pollExternalSave(t, a, cn, ed, now)
	if ed.state != extWatching || len(cn.fs.extUnsynced()) != 0 {
		t.Fatal("a metadata-only change marked the copy dirty")
	}
	write(t, ed.local, "changed")
	pollExternalSave(t, a, cn, ed, now)
	failure := errors.New("connection lost")
	a.externalSyncFailed(cn, ed, ed.observed, failure)
	pollExternalSave(t, a, cn, ed, now.Add(extSaveDebounce))
	if ed.state != extFailed || ed.err != failure || ed.ask {
		t.Fatal("a failed version was offered again without a new save")
	}
	write(t, ed.local, "original")
	pollExternalSave(t, a, cn, ed, now.Add(2*extSaveDebounce))
	write(t, ed.local, "changed")
	pollExternalSave(t, a, cn, ed, now.Add(3*extSaveDebounce))
	pollExternalSave(t, a, cn, ed, now.Add(4*extSaveDebounce))
	if ed.state != extChanged || !ed.ask || read(remote) != "original" {
		t.Fatal("the failure blocked a new save after the content changed")
	}
}
