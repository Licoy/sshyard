package sftpsvc

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// SearchLimit is the most entries a search finds before it stops.
const SearchLimit = 1000

// Match is an entry a search found, in its folder Dir.
type Match struct {
	Dir   string
	Entry Entry
}

// SearchMatcher says whether a name matches the pattern typed: a part of
// it, whatever the case, or, when the pattern has * or ?, the whole name
// as a glob where * is any run of characters and ? any one. Brackets
// and backslashes are themselves.
func SearchMatcher(pattern string) func(name string) bool {
	p := strings.ToLower(pattern)
	if !strings.ContainsAny(p, "*?") {
		return func(name string) bool { return strings.Contains(strings.ToLower(name), p) }
	}
	g := globEscaper.Replace(p)
	return func(name string) bool {
		ok, _ := path.Match(g, strings.ToLower(name))
		return ok
	}
}

// globEscaper makes the characters of a glob other than * and ? stand
// for themselves, in path.Match and in find's -iname alike.
var globEscaper = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)

// findName is the -iname pattern of what was typed, matching as
// SearchMatcher does.
func findName(pattern string) string {
	g := globEscaper.Replace(pattern)
	if !strings.ContainsAny(pattern, "*?") {
		g = "*" + g + "*"
	}
	return g
}

// searchPruned are the folders of a whole disk a search does not go
// into: the kernel's, the devices', the runtime's, and the system's
// second view of the disk on macOS.
var searchPruned = []string{"/proc", "/sys", "/dev", "/run", "/System/Volumes"}

// prunedUnder are the searchPruned folders inside root, a POSIX path.
func prunedUnder(root string) []string {
	var out []string
	for _, p := range searchPruned {
		if root == "/" || strings.HasPrefix(p, root+"/") {
			out = append(out, p)
		}
	}
	return out
}

// findMarker is the line the find command writes first, which says a
// POSIX shell runs it.
const findMarker = "sshyard-find"

// findBefore is how many lines may come before findMarker.
const findBefore = 100

// findMissing is the status the find command ends with when the server
// has no find that takes -iname.
const findMissing = 126

// FindCommand is the shell command that lists, one path a line, the
// entries under the remote folder root (an absolute path) whose names
// match pattern as SearchMatcher does, without going into the folders
// prunedUnder root or following links. It writes findMarker first, and
// ends with findMissing when the server has no find that takes -iname.
// Every path and pattern is quoted; the script runs in sh, whatever the
// login shell is.
func FindCommand(root, pattern string) string {
	q := ShellQuote(root)
	var b strings.Builder
	b.WriteString("echo " + findMarker + "; ")
	b.WriteString("find " + q + " -prune -iname x >/dev/null 2>&1 || exit " + strconv.Itoa(findMissing) + "; ")
	b.WriteString("exec find " + q)
	if ps := prunedUnder(root); len(ps) > 0 {
		b.WriteString(` \(`)
		for i, p := range ps {
			if i > 0 {
				b.WriteString(" -o")
			}
			b.WriteString(" -path " + ShellQuote(p))
		}
		b.WriteString(` \) -prune -o`)
	}
	b.WriteString(" -iname " + ShellQuote(findName(pattern)) + " -print 2>/dev/null")
	return "sh -c " + ShellQuote(b.String())
}

// errNoFind says the server ran no find for a search (no POSIX shell,
// no find, or no exec channel): the search walks the folders over SFTP.
var errNoFind = errors.New("sftp: no find on the server")

// runLines runs cmd on the server, giving each line it writes to line
// until line returns false, and is the status it ended with (-1 when
// not known, as when line stopped it).
type runLines func(ctx context.Context, cmd string, line func(string) bool) (status int, err error)

// sshLines runs commands on exec channels of conn.
func sshLines(conn *ssh.Client) runLines {
	return func(ctx context.Context, cmd string, line func(string) bool) (int, error) {
		s, err := conn.NewSession()
		if err != nil {
			return -1, err
		}
		defer s.Close()
		out, err := s.StdoutPipe()
		if err != nil {
			return -1, err
		}
		stop := context.AfterFunc(ctx, func() {
			s.Signal(ssh.SIGTERM)
			s.Close()
		})
		defer stop()
		if err := s.Start(cmd); err != nil {
			return -1, err
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			if !line(sc.Text()) {
				s.Signal(ssh.SIGTERM)
				return -1, nil
			}
		}
		if err := sc.Err(); err != nil {
			return -1, err
		}
		err = s.Wait()
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return exit.ExitStatus(), nil
		}
		if err != nil {
			return -1, err
		}
		return 0, nil
	}
}

// searchStatters is how many of the paths find lists are described at
// once.
const searchStatters = 8

// findSearch searches under the remote folder root with FindCommand,
// run by run, describing each path it lists with stat: each found once
// described, at most limit, in no set order. capped says it stopped at
// limit. errNoFind says the server ran no find, before any was found.
func findSearch(ctx context.Context, run runLines, stat func(context.Context, string) (Entry, error), root, pattern string, limit int, found func(Match)) (capped bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	prefix := root + "/"
	if root == "/" {
		prefix = "/"
	}
	paths := make(chan string, 64)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range searchStatters {
		wg.Go(func() {
			for p := range paths {
				e, err := stat(ctx, p)
				if err != nil || ctx.Err() != nil {
					continue // gone, or a name with a line break in it
				}
				e.Name = path.Base(p)
				mu.Lock()
				found(Match{Dir: path.Dir(p), Entry: e})
				mu.Unlock()
			}
		})
	}
	marked, before, n := false, 0, 0
	status, err := run(ctx, FindCommand(root, pattern), func(l string) bool {
		if !marked {
			// what a login script writes may come first
			marked, before = l == findMarker, before+1
			return marked || before < findBefore
		}
		if l == root || !strings.HasPrefix(l, prefix) {
			return true
		}
		if n >= limit {
			capped = true
			return false
		}
		n++
		select {
		case paths <- l:
			return true
		case <-ctx.Done():
			return false
		}
	})
	close(paths)
	wg.Wait()
	switch {
	case capped:
		return true, nil
	case ctx.Err() != nil:
		return false, ctx.Err()
	case !marked || status == findMissing:
		return false, errNoFind
	}
	// find ends with 1 when some folder could not be read, as most on a
	// whole disk cannot: what it listed is what there is to find.
	if err != nil && n == 0 {
		return false, err
	}
	return false, nil
}

// walkFS is what walkSearch walks: a server's folders or this computer's.
type walkFS interface {
	// readDir lists the folder dir, links as links.
	readDir(ctx context.Context, dir string) ([]fs.FileInfo, error)
	join(dir, name string) string
	// isDir says whether the link p leads to a folder, when it can tell.
	isDir(ctx context.Context, p string) (dir, ok bool)
}

// walkWorkers is how many folders a walk lists at once.
const walkWorkers = 8

// walkSearch searches under root by listing its folders, walkWorkers at
// once, shallower ones first: each entry whose name matches is found, at
// most limit, then the walk stops (capped). It goes into no link and no
// folder prunedUnder root, and leaves out what it cannot read.
func walkSearch(ctx context.Context, w walkFS, root string, match func(string) bool, limit int, found func(Match)) (capped bool, err error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pruned := map[string]bool{}
	for _, p := range prunedUnder(root) {
		pruned[p] = true
	}
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	queue, busy, n := []string{root}, 0, 0
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	})
	defer stop()
	var wg sync.WaitGroup
	for range walkWorkers {
		wg.Go(func() {
			for {
				mu.Lock()
				for len(queue) == 0 && busy > 0 && ctx.Err() == nil {
					cond.Wait()
				}
				if len(queue) == 0 || ctx.Err() != nil {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				dir := queue[0]
				queue = queue[1:]
				busy++
				mu.Unlock()
				var subs []string
				var hits []Match
				fis, err := w.readDir(ctx, dir)
				if err != nil {
					fis = nil // what cannot be read is left out
				}
				for _, fi := range fis {
					name := fi.Name()
					if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
						continue
					}
					p := w.join(dir, name)
					link := fi.Mode()&fs.ModeSymlink != 0
					if match(name) {
						e := entry(fi)
						if link {
							if d, ok := w.isDir(ctx, p); ok {
								e.IsDir = d
							}
						}
						hits = append(hits, Match{Dir: dir, Entry: e})
					}
					if fi.IsDir() && !link && !pruned[p] {
						subs = append(subs, p)
					}
				}
				mu.Lock()
				busy--
				for _, h := range hits {
					if n >= limit {
						capped = true
						cancel()
						break
					}
					n++
					found(h)
				}
				if ctx.Err() == nil {
					queue = append(queue, subs...)
				}
				mu.Unlock()
				cond.Broadcast()
			}
		})
	}
	wg.Wait()
	if capped {
		return true, nil
	}
	return false, parent.Err()
}

// remoteWalk walks a server's folders over an SFTP client.
type remoteWalk struct{ c *Client }

func (w remoteWalk) readDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	return w.c.readDir(ctx, dir)
}

func (remoteWalk) join(dir, name string) string { return path.Join(dir, name) }

func (w remoteWalk) isDir(ctx context.Context, p string) (bool, bool) {
	fi, err := withCtx(ctx, func() (fs.FileInfo, error) { return w.c.c.Stat(p) })
	if err != nil {
		return false, false
	}
	return fi.IsDir(), true
}

// localWalk walks this computer's folders.
type localWalk struct{}

func (localWalk) readDir(_ context.Context, dir string) ([]fs.FileInfo, error) {
	des, err := os.ReadDir(dir)
	out := make([]fs.FileInfo, 0, len(des))
	for _, d := range des {
		if fi, err := d.Info(); err == nil {
			out = append(out, fi)
		}
	}
	return out, err
}

func (localWalk) join(dir, name string) string { return filepath.Join(dir, name) }

func (localWalk) isDir(_ context.Context, p string) (bool, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return false, false
	}
	return fi.IsDir(), true
}

// windowsPath says p is a path of a Windows server, as its SFTP shows
// them: a drive first, "/C:/Users".
var windowsPath = regexp.MustCompile(`^/[A-Za-z]:(/|$)`).MatchString

// Search finds the entries under the remote folder root whose names
// match pattern (SearchMatcher), at most limit (SearchLimit when 0),
// giving each to found as it is found, from another goroutine, one at a
// time. capped says it stopped at limit. It runs find on the server
// where it can, which lists a whole disk quickly, and walks the folders
// over SFTP where it cannot: on a server without a POSIX shell or find,
// on Windows, and with sudo (Host.SFTPSudo), whose find would not see
// what its SFTP does. The kernel's, devices' and runtime's folders of a
// whole disk are left out, links are not followed into, and what cannot
// be read is passed over.
func (c *Client) Search(ctx context.Context, root, pattern string, limit int, found func(Match)) (capped bool, err error) {
	if limit <= 0 {
		limit = SearchLimit
	}
	root = path.Clean(root)
	if c.conn != nil && c.sudo == nil && path.IsAbs(root) && !windowsPath(root) {
		if home, _ := c.Home(); !windowsPath(home) {
			stat := func(ctx context.Context, p string) (Entry, error) {
				fi, err := withCtx(ctx, func() (fs.FileInfo, error) { return c.c.Stat(p) })
				if err != nil {
					// a broken link
					fi, err = withCtx(ctx, func() (fs.FileInfo, error) { return c.c.Lstat(p) })
				}
				if err != nil {
					return Entry{}, err
				}
				return entry(fi), nil
			}
			capped, err = findSearch(ctx, sshLines(c.conn), stat, root, pattern, limit, found)
			if !errors.Is(err, errNoFind) {
				return capped, err
			}
		}
	}
	sub, err := c.operationClient(ctx)
	if err != nil {
		return false, err
	}
	defer sub.Close()
	stop := context.AfterFunc(ctx, func() { sub.Close() })
	defer stop()
	return walkSearch(ctx, remoteWalk{sub}, root, SearchMatcher(pattern), limit, found)
}

// SearchLocal finds the entries under the local folder root as Search
// does, by walking its folders.
func SearchLocal(ctx context.Context, root, pattern string, limit int, found func(Match)) (capped bool, err error) {
	if limit <= 0 {
		limit = SearchLimit
	}
	return walkSearch(ctx, localWalk{}, filepath.Clean(root), SearchMatcher(pattern), limit, found)
}
