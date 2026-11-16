package sftpsvc

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSearchMatcher(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"conf", "nginx.CONF", true},
		{"Conf", "my-config.yml", true},
		{"conf", "nginx.cnf", false},
		{"*.log", "Error.LOG", true},
		{"*.log", "error.log.1", false},
		{"err?r.log", "error.log", true},
		{"[a]", "x[a]y", true}, // brackets are themselves
		{"[a]", "xay", false},  // not a class
		{"*[1]*", "v[1].txt", true},
		{`a\b`, `xa\by`, true},
	} {
		if got := SearchMatcher(tc.pattern)(tc.name); got != tc.want {
			t.Errorf("SearchMatcher(%q)(%q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestFindCommandPrunes(t *testing.T) {
	whole := FindCommand("/", "x")
	for _, p := range []string{"/proc", "/sys", "/dev", "/run"} {
		if !strings.Contains(whole, "-path '\\''"+p+"'\\''") {
			t.Errorf("whole-disk command does not prune %s: %s", p, whole)
		}
	}
	if here := FindCommand("/home/me", "x"); strings.Contains(here, "-prune -o") {
		t.Errorf("a folder with nothing to prune prunes: %s", here)
	}
	if got := prunedUnder("/proc"); len(got) != 0 {
		t.Errorf("prunedUnder(/proc) = %v, want none: the folder searched is not pruned", got)
	}
	if got := prunedUnder("/System"); !slices.Equal(got, []string{"/System/Volumes"}) {
		t.Errorf("prunedUnder(/System) = %v", got)
	}
}

// TestFindCommandRuns runs the command in this computer's sh, over names
// a shell would read as code, globs or options.
func TestFindCommandRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell")
	}
	root := filepath.Join(t.TempDir(), "it's $(here) [x]")
	names := []string{"it's.TXT", "$(touch pwned).txt", "-name.txt", "a[1].txt", "other.md", "sub/deep it's.txt", "sub/x"}
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(pattern string) []string {
		t.Helper()
		out, err := exec.Command("sh", "-c", FindCommand(root, pattern)).Output()
		if err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if lines[0] != findMarker {
			t.Fatalf("%q: first line %q, want the marker", pattern, lines[0])
		}
		var got []string
		for _, l := range lines[1:] {
			if rel, _ := filepath.Rel(root, l); rel != "." { // findSearch leaves the root out
				got = append(got, rel)
			}
		}
		slices.Sort(got)
		return got
	}
	if got, want := run("it's"), []string{"it's.TXT", "sub/deep it's.txt"}; !slices.Equal(got, want) {
		t.Errorf("it's: %v, want %v", got, want)
	}
	if got, want := run("[1]"), []string{"a[1].txt"}; !slices.Equal(got, want) {
		t.Errorf("[1]: %v, want %v", got, want)
	}
	if got, want := run("$(touch"), []string{"$(touch pwned).txt"}; !slices.Equal(got, want) {
		t.Errorf("$(touch: %v, want %v", got, want)
	}
	if got, want := run("*.md"), []string{"other.md"}; !slices.Equal(got, want) {
		t.Errorf("*.md: %v, want %v", got, want)
	}
	if _, err := os.Stat("pwned"); err == nil {
		os.Remove("pwned")
		t.Fatal("a name ran as a command")
	}
}

// fakeRun is a server whose find lists lines, then ends with status.
func fakeRun(lines []string, status int, err error) (runLines, *string) {
	var ran string
	return func(ctx context.Context, cmd string, line func(string) bool) (int, error) {
		ran = cmd
		for _, l := range lines {
			if ctx.Err() != nil {
				return -1, ctx.Err()
			}
			if !line(l) {
				return -1, nil
			}
		}
		return status, err
	}, &ran
}

func statAll(_ context.Context, p string) (Entry, error) {
	if strings.HasSuffix(p, "/gone") {
		return Entry{}, fs.ErrNotExist
	}
	return Entry{Name: "ignored", IsDir: strings.HasSuffix(p, "dir")}, nil
}

// collect gathers what a search finds.
type collect struct {
	mu sync.Mutex
	ms []Match
}

func (c *collect) found(m Match) {
	c.mu.Lock()
	c.ms = append(c.ms, m)
	c.mu.Unlock()
}

func (c *collect) paths() []string {
	var out []string
	for _, m := range c.ms {
		out = append(out, path.Join(m.Dir, m.Entry.Name))
	}
	slices.Sort(out)
	return out
}

func TestFindSearch(t *testing.T) {
	run, ran := fakeRun([]string{"motd line", findMarker, "/srv", "/srv/a.log", "/srv/x/bdir", "/elsewhere/c", "/srv/gone"}, 1, nil)
	var got collect
	capped, err := findSearch(context.Background(), run, statAll, "/srv", "a", 10, got.found)
	if err != nil || capped {
		t.Fatalf("findSearch = %v, %v", capped, err)
	}
	if want := []string{"/srv/a.log", "/srv/x/bdir"}; !slices.Equal(got.paths(), want) {
		t.Errorf("found %v, want %v (not the root, not outside it, not what is gone)", got.paths(), want)
	}
	if *ran != FindCommand("/srv", "a") {
		t.Errorf("ran %q", *ran)
	}
	for _, m := range got.ms {
		if m.Entry.Name == "bdir" && !m.Entry.IsDir {
			t.Error("a folder found is not a folder")
		}
	}
}

func TestFindSearchCaps(t *testing.T) {
	lines := []string{findMarker}
	for i := range 20 {
		lines = append(lines, "/r/f"+string(rune('a'+i)))
	}
	run, _ := fakeRun(lines, 0, nil)
	var got collect
	capped, err := findSearch(context.Background(), run, statAll, "/r", "f", 5, got.found)
	if err != nil || !capped || len(got.ms) != 5 {
		t.Fatalf("findSearch = %v, %v with %d found; want capped at 5", capped, err, len(got.ms))
	}
}

func TestFindSearchFallsBack(t *testing.T) {
	for name, run := range map[string]runLines{
		"no shell": func() runLines { r, _ := fakeRun([]string{"'sh' is not recognized"}, 1, nil); return r }(),
		"no find":  func() runLines { r, _ := fakeRun([]string{findMarker}, findMissing, nil); return r }(),
		"no exec": func(context.Context, string, func(string) bool) (int, error) {
			return -1, errors.New("exec refused")
		},
	} {
		if _, err := findSearch(context.Background(), run, statAll, "/", "x", 10, func(Match) {}); !errors.Is(err, errNoFind) {
			t.Errorf("%s: err %v, want errNoFind", name, err)
		}
	}
}

func TestFindSearchCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := func(ctx context.Context, _ string, line func(string) bool) (int, error) {
		line(findMarker)
		line("/r/a")
		cancel()
		<-ctx.Done()
		return -1, ctx.Err()
	}
	if _, err := findSearch(ctx, run, statAll, "/r", "a", 10, func(Match) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want canceled", err)
	}
}

// fakeTree is a server's folders, by path: each folder's entries.
type fakeTree struct {
	dirs   map[string][]fs.FileInfo
	denied map[string]bool
	links  map[string]bool // what each link leads to: a folder or not
	mu     sync.Mutex
	read   []string
	block  chan struct{} // when set, every listing waits for it
}

func (f *fakeTree) readDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	f.mu.Lock()
	f.read = append(f.read, dir)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.denied[dir] {
		return nil, fs.ErrPermission
	}
	return f.dirs[dir], nil
}

func (*fakeTree) join(dir, name string) string { return path.Join(dir, name) }

func (f *fakeTree) isDir(_ context.Context, p string) (bool, bool) {
	d, ok := f.links[p]
	return d, ok
}

func dirInfo(name string) fs.FileInfo { return pagedInfo{name: name, mode: fs.ModeDir | 0o755} }
func fileInfo(name string) fs.FileInfo {
	return pagedInfo{name: name, mode: 0o644, mtime: time.Unix(1, 0)}
}
func linkInfo(name string) fs.FileInfo { return pagedInfo{name: name, mode: fs.ModeSymlink | 0o777} }

func searchTree() *fakeTree {
	return &fakeTree{
		dirs: map[string][]fs.FileInfo{
			"/":              {dirInfo("proc"), dirInfo("home"), dirInfo("secret"), linkInfo("loop"), fileInfo("notes.txt")},
			"/proc":          {fileInfo("notes-proc")},
			"/home":          {dirInfo("me"), linkInfo("notes-dir")},
			"/home/me":       {fileInfo("Notes.md"), fileInfo("other"), dirInfo("notes")},
			"/home/me/notes": {fileInfo("a")},
			"/secret":        {fileInfo("notes.key")},
		},
		denied: map[string]bool{"/secret": true},
		links:  map[string]bool{"/home/notes-dir": true},
	}
}

func TestWalkSearch(t *testing.T) {
	f := searchTree()
	var got collect
	capped, err := walkSearch(context.Background(), f, "/", SearchMatcher("notes"), 100, got.found)
	if err != nil || capped {
		t.Fatalf("walkSearch = %v, %v", capped, err)
	}
	want := []string{"/home/me/Notes.md", "/home/me/notes", "/home/notes-dir", "/notes.txt"}
	if !slices.Equal(got.paths(), want) {
		t.Errorf("found %v, want %v", got.paths(), want)
	}
	for _, m := range got.ms {
		if m.Entry.Name == "notes-dir" && !m.Entry.IsDir {
			t.Error("a link to a folder found is not a folder")
		}
	}
	for _, d := range f.read {
		if d == "/proc" || d == "/loop" || d == "/home/notes-dir" {
			t.Errorf("walked into %s", d)
		}
	}
}

func TestWalkSearchFolder(t *testing.T) {
	var got collect
	if _, err := walkSearch(context.Background(), searchTree(), "/proc", SearchMatcher("notes"), 100, got.found); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/proc/notes-proc"}; !slices.Equal(got.paths(), want) {
		t.Errorf("found %v, want %v: the folder searched is not pruned", got.paths(), want)
	}
}

func TestWalkSearchCaps(t *testing.T) {
	var got collect
	capped, err := walkSearch(context.Background(), searchTree(), "/", SearchMatcher("*"), 3, got.found)
	if err != nil || !capped || len(got.ms) != 3 {
		t.Fatalf("walkSearch = %v, %v with %d found; want capped at 3", capped, err, len(got.ms))
	}
}

func TestWalkSearchCanceled(t *testing.T) {
	f := searchTree()
	f.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := walkSearch(ctx, f, "/", SearchMatcher("notes"), 100, func(Match) {})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled walk did not stop")
	}
}

func TestSearchLocal(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a/Report.txt", "a/b/report.md", "c/other"} {
		p := filepath.Join(root, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Mkdir(filepath.Join(root, "locked"), 0o755)
	os.WriteFile(filepath.Join(root, "locked", "report.key"), nil, 0o644)
	if runtime.GOOS != "windows" {
		os.Chmod(filepath.Join(root, "locked"), 0)
		defer os.Chmod(filepath.Join(root, "locked"), 0o755)
		os.Symlink(root, filepath.Join(root, "a", "loop"))
	}
	var got []string
	capped, err := SearchLocal(context.Background(), root, "REPORT", 0, func(m Match) {
		rel, _ := filepath.Rel(root, filepath.Join(m.Dir, m.Entry.Name))
		got = append(got, filepath.ToSlash(rel))
	})
	if err != nil || capped {
		t.Fatalf("SearchLocal = %v, %v", capped, err)
	}
	slices.Sort(got)
	want := []string{"a/Report.txt", "a/b/report.md"}
	if os.Getuid() == 0 || runtime.GOOS == "windows" {
		// root reads it anyway; Windows has no mode to lock it with
		want = append(want, "locked/report.key")
	}
	if !slices.Equal(got, want) {
		t.Errorf("found %v, want %v", got, want)
	}
}

func TestWindowsPath(t *testing.T) {
	for p, want := range map[string]bool{"/C:/Users/me": true, "/d:": true, "/home/c:": false, "/": false} {
		if windowsPath(p) != want {
			t.Errorf("windowsPath(%q) = %v", p, !want)
		}
	}
}
