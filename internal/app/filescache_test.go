package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/sshtest"
)

// remoteNames are the names the remote side lists.
func remoteNames(s *remoteSide) []string {
	var out []string
	for _, e := range s.listed {
		out = append(out, e.Name)
	}
	return out
}

// TestFilesRemoteCache: a folder gone to again shows as it last listed at
// once, then as it lists now; one gone since goes back where it was, with
// the error.
func TestFilesRemoteCache(t *testing.T) {
	skipWindowsSFTP(t)
	a, tt, fc, root := filesPane(t)
	write(t, filepath.Join(root, "a", "old.txt"), "x")
	write(t, filepath.Join(root, "b", "b.txt"), "x")
	s := &fc.fs.remote
	dirA := sftpsvc.RemoteJoin(s.dir, "a")
	dirB := sftpsvc.RemoteJoin(s.dir, "b")
	a.listRemote(s, dirA)
	waitFor(t, tt, "a", func() bool { return s.dir == dirA })
	a.listRemote(s, dirB)
	waitFor(t, tt, "b", func() bool { return s.dir == dirB })

	write(t, filepath.Join(root, "a", "new.txt"), "x")
	a.listRemote(s, dirA)
	if s.dir != dirA || !slices.Equal(remoteNames(s), []string{"old.txt"}) {
		t.Fatalf("not shown at once as it was: %q %q", s.dir, remoteNames(s))
	}
	waitFor(t, tt, "listed anew", func() bool { return slices.Contains(remoteNames(s), "new.txt") })
	if s.err != "" {
		t.Fatalf("error %q", s.err)
	}

	// b removed since: shown as it was, then back in a with the error.
	if err := os.RemoveAll(filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	a.listRemote(s, dirB)
	if s.dir != dirB {
		t.Fatalf("b not shown at once: %q", s.dir)
	}
	waitFor(t, tt, "the error", func() bool { return s.err != "" })
	if s.dir != dirA || !slices.Contains(remoteNames(s), "new.txt") {
		t.Fatalf("not back in a: %q %q", s.dir, remoteNames(s))
	}
	if _, ok := s.cached(dirB); ok {
		t.Error("a folder that is gone still remembered")
	}
}

// TestFilesRemoteListLatest: of two listings asked for, the last one
// shows, even when the first comes back after it.
func TestFilesRemoteListLatest(t *testing.T) {
	skipWindowsSFTP(t)
	a, tt, fc, root := filesPane(t)
	write(t, filepath.Join(root, "slow", "s.txt"), "x")
	write(t, filepath.Join(root, "fast", "f.txt"), "x")
	s := &fc.fs.remote
	slow := sftpsvc.RemoteJoin(s.dir, "slow")
	fast := sftpsvc.RemoteJoin(s.dir, "fast")
	release, slowDone := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { listUnfinished = (*sftpsvc.Client).ListUnfinished })
	listUnfinished = func(c *sftpsvc.Client, dir string) ([]sftpsvc.Entry, []sftpsvc.Unfinished, error) {
		if dir == slow {
			defer close(slowDone)
			<-release
		}
		return c.ListUnfinished(dir)
	}
	a.listRemote(s, slow)
	a.listRemote(s, fast)
	waitFor(t, tt, "fast", func() bool { return s.dir == fast })
	close(release)
	<-slowDone
	for range 5 {
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
	if s.dir != fast {
		t.Fatalf("an older listing showed over the last: %q", s.dir)
	}
	if _, ok := s.cached(slow); ok {
		t.Error("an older listing remembered")
	}
}

// TestFilesRemoteCacheInvalidated: what was remembered goes with the
// connection, and listing the folder shown again (as after a change in
// it) forgets the folders under it.
func TestFilesRemoteCacheInvalidated(t *testing.T) {
	skipWindowsSFTP(t)
	var srv *sshtest.Server
	a, tt, fc, root := filesPaneOn(t, func(s *sshtest.Server) { srv = s })
	write(t, filepath.Join(root, "a", "sub", "x.txt"), "x")
	s := &fc.fs.remote
	top := s.dir
	dirA := sftpsvc.RemoteJoin(top, "a")
	sub := sftpsvc.RemoteJoin(dirA, "sub")
	for _, d := range []string{dirA, sub, dirA} {
		a.listRemote(s, d)
		waitFor(t, tt, d, func() bool { return s.dir == d })
	}
	if _, ok := s.cached(sub); !ok {
		t.Fatal("sub not remembered")
	}
	if err := a.Mkdir(fc, true, "made"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.cached(sub); ok {
		t.Error("a folder under one changed still remembered")
	}
	waitFor(t, tt, "made", func() bool { return slices.Contains(remoteNames(s), "made") })

	// Reconnected: only the folder listed on the new connection.
	if _, ok := s.cached(top); !ok {
		t.Fatal("the first folder not remembered")
	}
	old := s.client
	srv.Drop()
	waitFor(t, tt, "reconnected", func() bool {
		return fc.s.Status().State == session.Connected && s.client != nil && s.client != old
	})
	if len(s.cache) != 1 || s.cache[0].dir != s.dir {
		t.Errorf("remembered across connections: %d folders", len(s.cache))
	}
}

func TestRemoteSideRemember(t *testing.T) {
	var s remoteSide
	for i := range dirCacheMax + 2 {
		s.remember(dirListing{dir: "/d/" + string(rune('a'+i))})
	}
	if len(s.cache) != dirCacheMax {
		t.Fatalf("%d remembered", len(s.cache))
	}
	if _, ok := s.cached("/d/a"); ok {
		t.Error("the oldest kept")
	}
	s.remember(dirListing{dir: "/d/c"})
	s.remember(dirListing{dir: "/d/" + string(rune('a'+dirCacheMax+2))})
	if _, ok := s.cached("/d/c"); !ok {
		t.Error("one used again forgotten")
	}
	s.remember(dirListing{dir: "/"})
	s.forget("/d", true)
	if len(s.cache) != 1 || s.cache[0].dir != "/" {
		t.Errorf("forget under /d left %d", len(s.cache))
	}
	for _, c := range []struct {
		p, dir string
		in     bool
	}{
		{"/a/b", "/a", true},
		{"/ab", "/a", false},
		{"/a", "/a", false},
		{"/a", "/", true},
		{"/", "/", false},
		{"/C:/x", "/C:", true},
	} {
		if got := remoteUnder(c.p, c.dir); got != c.in {
			t.Errorf("remoteUnder(%q, %q) = %v", c.p, c.dir, got)
		}
	}
}
