package app

import (
	"path/filepath"
	"testing"

	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/sshtest"
)

// secondFilesPane opens a files pane on another test server, in the app
// of the first, returning the pane and the folder its server serves.
func secondFilesPane(t *testing.T, a *App) (*conn, string) {
	t.Helper()
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv.SFTPRoot = root
	h := addHost(t, a, srv, "other", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	id, err := a.OpenFiles(h.ID, openAt{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a.conns[id], root
}

// Copy on one server's files and Paste on another's copies them server
// to server, in the destination's queue, asking about what is there;
// a folder pasted into itself on the same server is refused.
func TestFilesRelayCopyPaste(t *testing.T) {
	skipWindowsSFTP(t)
	a, tt, src, srcRoot := filesPane(t)
	dst, dstRoot := secondFilesPane(t, a)
	waitFor(t, tt, "second files pane connected", func() bool {
		a.ensureSFTP(dst)
		return dst.fs.remote.client != nil
	})
	write(t, filepath.Join(srcRoot, "proj", "a.txt"), "new a")
	write(t, filepath.Join(srcRoot, "proj", "sub", "b.txt"), "new b")
	write(t, filepath.Join(dstRoot, "proj", "a.txt"), "old a")

	if a.canPaste(dst, true) {
		t.Fatal("paste with nothing copied")
	}
	a.copyEntries(src, true, []string{srcRoot + "/proj"})
	if !a.canPaste(dst, true) || !a.canPaste(src, false) {
		t.Fatal("a server's entries do not paste on a server or here")
	}
	a.paste(dst, true)
	waitFor(t, tt, "conflict", func() bool {
		k := dst.fs.conflict
		return k != nil && k.at < len(k.items) && k.items[k.at].Rel == "proj/a.txt"
	})
	// Keep both, as the dialog's button answers.
	k := dst.fs.conflict
	k.choice[k.at], k.last = resRename, resRename
	k.at++
	a.resolveConflicts(dst, k)
	waitFor(t, tt, "copied", func() bool {
		return read(filepath.Join(dstRoot, "proj", "a (2).txt")) == "new a" && read(filepath.Join(dstRoot, "proj", "sub", "b.txt")) == "new b"
	})
	dst.fs.transfers.Wait()
	if read(filepath.Join(dstRoot, "proj", "a.txt")) != "old a" {
		t.Error("the file kept was replaced")
	}
	trs := dst.fs.transfers.List()
	if len(trs) != 2 || len(src.fs.transfers.List()) != 0 {
		t.Fatalf("queued %+v, source queue %+v", trs, src.fs.transfers.List())
	}
	for _, tr := range trs {
		if tr.Dir != sftpsvc.Relay || tr.State != sftpsvc.Done || tr.From != "box" {
			t.Errorf("%+v", tr)
		}
	}

	// The same server: into itself is refused, nothing planned.
	a.copyEntries(dst, true, []string{dstRoot + "/proj"})
	a.Relay(dst, dst, []string{dstRoot + "/proj"}, dstRoot+"/proj/sub")
	if dst.fs.planning != nil || dst.fs.remote.err != sftpsvc.ErrIntoItself.Error() {
		t.Fatalf("planning %v, err %q", dst.fs.planning, dst.fs.remote.err)
	}

	// This computer's entries paste only onto a server (an upload).
	write(t, filepath.Join(src.fs.local.dir, "here.txt"), "here")
	a.copyEntries(src, false, []string{filepath.Join(src.fs.local.dir, "here.txt")})
	if a.canPaste(src, false) || !a.canPaste(dst, true) {
		t.Fatal("local entries paste rules")
	}
	a.paste(dst, true)
	waitFor(t, tt, "uploaded", func() bool { return read(filepath.Join(dst.fs.remote.dir, "here.txt")) == "here" })
	dst.fs.transfers.Wait()
}
