package app

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

// A search starts once typing rests, or at once on Enter; the same query
// searches again only under another folder, on another client or on
// Enter.
func TestFileSearchDue(t *testing.T) {
	now := time.Unix(100, 0)
	c1, c2 := &sftpsvc.Client{}, &sftpsvc.Client{}
	sr := &fileSearch{typedAt: now}
	if ok, _ := sr.due(now, "", "/srv", c1, true); ok {
		t.Error("nothing typed searches")
	}
	if ok, wait := sr.due(now.Add(100*time.Millisecond), "log", "/srv", c1, false); ok || wait != searchSettle-100*time.Millisecond {
		t.Errorf("while typing: %v, wait %v", ok, wait)
	}
	if ok, _ := sr.due(now, "log", "/srv", c1, true); !ok {
		t.Error("Enter does not search")
	}
	if ok, _ := sr.due(now.Add(searchSettle), "log", "/srv", c1, false); !ok {
		t.Error("typing rested, no search")
	}
	sr.ran, sr.root, sr.client = "log", "/srv", c1
	if ok, wait := sr.due(now.Add(time.Hour), "log", "/srv", c1, false); ok || wait != 0 {
		t.Errorf("the same search again: %v, wait %v", ok, wait)
	}
	for _, tc := range []struct {
		root   string
		client *sftpsvc.Client
		submit bool
	}{{"/", c1, false}, {"/srv", c2, false}, {"/srv", c1, true}} {
		if ok, _ := sr.due(now, "log", tc.root, tc.client, tc.submit); !ok {
			t.Errorf("%+v: no search", tc)
		}
	}
}

func TestSearchRootAndPlace(t *testing.T) {
	if got := searchRoot(true, "/home/me", false); got != "/home/me" {
		t.Errorf("here: %q", got)
	}
	if got := searchRoot(true, "/C:/Users", true); got != "/" {
		t.Errorf("whole server: %q", got)
	}
	if got := searchRoot(false, "", true); got != "" {
		t.Errorf("no folder yet: %q", got)
	}
	for _, tc := range []struct {
		root, dir string
		whole     bool
		want      string
	}{
		{"/home/me", "/home/me", false, "./"},
		{"/home/me", "/home/me/logs/old", false, "./logs/old"},
		{"/", "/etc", false, "./etc"},
		{"/home/me", "/home/mew", false, "/home/mew"},
		{"/", "/var/log", true, "/var/log"},
	} {
		if got := searchPlace(true, tc.root, tc.dir, tc.whole); got != tc.want {
			t.Errorf("searchPlace(%q, %q, %v) = %q, want %q", tc.root, tc.dir, tc.whole, got, tc.want)
		}
	}
	if runtime.GOOS != "windows" {
		if got := searchPlace(false, "/Users/me", "/Users/me/a", false); got != "./a" {
			t.Errorf("local: %q", got)
		}
	}
}

// A remote search runs find over the server's shell where it has one,
// and walks the folders over SFTP where it has none, finding the same;
// a file found opens its folder with it chosen, closing the search.
func TestFilesSearchRemote(t *testing.T) {
	skipWindowsSFTP(t)
	for _, shell := range []bool{true, false} {
		t.Run(map[bool]string{true: "find", false: "walk"}[shell], func(t *testing.T) {
			var mu sync.Mutex
			var ran []string
			a, tt, fc, root := filesPaneOn(t, func(srv *sshtest.Server) {
				if !shell {
					return
				}
				run := sshtest.ShellExec(srv.SFTPRoot)
				srv.Exec = func(ch ssh.Channel, cmd string) int {
					mu.Lock()
					ran = append(ran, cmd)
					mu.Unlock()
					return run(ch, cmd)
				}
			})
			write(t, filepath.Join(root, "logs", "app.LOG"), "a")
			write(t, filepath.Join(root, "logs", "deep", "b.log"), "b")
			write(t, filepath.Join(root, "other.txt"), "c")
			s := &fc.fs.remote
			a.goTo(fc, true, root)
			waitFor(t, tt, "listing", func() bool { return s.dir == root && len(s.entries) == 2 })

			sr := &fileSearch{sel: -1}
			a.startSearch(fc, true, sr, "*.log", root, s.client)
			waitFor(t, tt, "search", func() bool { return !sr.running })
			var got []string
			for _, mt := range sr.results {
				rel, _ := filepath.Rel(root, filepath.Join(mt.Dir, mt.Entry.Name))
				got = append(got, filepath.ToSlash(rel))
			}
			slices.Sort(got)
			if want := []string{"logs/app.LOG", "logs/deep/b.log"}; !slices.Equal(got, want) || sr.err != "" || sr.capped {
				t.Fatalf("found %v (err %q, capped %v), want %v", got, sr.err, sr.capped, want)
			}
			mu.Lock()
			usedFind := slices.ContainsFunc(ran, func(c string) bool { return strings.Contains(c, "-iname") })
			mu.Unlock()
			if usedFind != shell {
				t.Errorf("ran find: %v, want %v (%q)", usedFind, shell, ran)
			}

			i := slices.IndexFunc(sr.results, func(mt sftpsvc.Match) bool { return mt.Entry.Name == "app.LOG" })
			s.search = sr
			a.openMatch(fc, true, sr.results[i])
			if s.search != nil {
				t.Error("the search stays open")
			}
			waitFor(t, tt, "the file's folder", func() bool { return s.dir == filepath.Join(root, "logs") && s.chosen.Has("app.LOG") })
		})
	}
}

// Closing the pane, or the search, stops it.
func TestFilesSearchStops(t *testing.T) {
	fs := newFilesState(nil)
	stopped := false
	fs.local.toggleSearch()
	sr := fs.local.search
	sr.running, sr.cancel = true, func() { stopped = true }
	gen := sr.gen
	fs.close()
	if !stopped || sr.running || sr.gen == gen || fs.local.search != nil {
		t.Errorf("after close: canceled %v, running %v, gen %d (was %d), search %v", stopped, sr.running, sr.gen, gen, fs.local.search)
	}
}
