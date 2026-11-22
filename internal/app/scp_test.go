package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/sshtest"
)

// TestFilesSCPBadge: a server without SFTP shows its files through its
// shell and scp, with a badge in the pane header saying so.
func TestFilesSCPBadge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test server runs commands with /bin/sh")
	}
	_, tt, fc, _ := filesPaneOn(t, func(srv *sshtest.Server) {
		srv.NoSFTP = true
		srv.Exec = sshtest.ShellExec(srv.SFTPRoot)
		os.WriteFile(filepath.Join(srv.SFTPRoot, "hello.txt"), []byte("hi"), 0o644)
	})
	waitFor(t, tt, "the listing", func() bool { return tt.HasText("hello.txt") })
	if !fc.fs.remote.client.SCP() || !tt.HasText("SCP") {
		t.Fatalf("no SCP badge: %q", tt.Texts())
	}
	if !tt.HasText(en.ScpFallbackNote()) {
		t.Errorf("the badge does not say why: %q", tt.Texts())
	}
}

// TestFilesProtocolChoice: a host set to scp reaches its files with scp
// though its server has SFTP, and set back to automatic, with SFTP.
func TestFilesProtocolChoice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test server runs commands with /bin/sh")
	}
	a, tt, fc, _ := filesPaneOn(t, func(srv *sshtest.Server) {
		srv.Exec = sshtest.ShellExec(srv.SFTPRoot)
		os.WriteFile(filepath.Join(srv.SFTPRoot, "hello.txt"), []byte("hi"), 0o644)
	})
	waitFor(t, tt, "the listing", func() bool { return tt.HasText("hello.txt") })
	if fc.fs.remote.client.SCP() {
		t.Fatal("a server with SFTP is reached with scp")
	}
	set := func(p assets.FileProtocol) {
		t.Helper()
		h := a.liveHost(fc.host)
		h.FileProtocol = p
		if _, err := a.Store().Update(h); err != nil {
			t.Fatal(err)
		}
	}
	set(assets.FileSCP)
	waitFor(t, tt, "files with scp", func() bool {
		c := fc.fs.remote.client
		return c != nil && c.SCP() && tt.HasText("hello.txt")
	})
	set(assets.FileAuto)
	waitFor(t, tt, "files with SFTP again", func() bool {
		c := fc.fs.remote.client
		return c != nil && !c.SCP() && tt.HasText("hello.txt")
	})
}
