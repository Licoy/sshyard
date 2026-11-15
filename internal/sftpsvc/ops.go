package sftpsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

// Item is a file or folder a transfer plan copies, with where it goes.
type Item struct {
	Src, Dst string
	// Rel is the path below the folder the user chose, with slashes.
	Rel   string
	Size  int64
	IsDir bool
	// Exists says a file is already at Dst (folders merge).
	Exists bool
}

// PlanUpload lists what uploading the local paths into remoteDir copies:
// folders recursively, folders before what is in them. A folder may end
// in a separator (Finder drops folders as "file:///…/name/"): it is
// still the folder itself that goes into remoteDir.
func (c *Client) PlanUpload(ctx context.Context, srcs []string, remoteDir string) ([]Item, error) {
	var out []Item
	for _, src := range srcs {
		src = filepath.Clean(src)
		base := filepath.Dir(src)
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, p)
			rel = filepath.ToSlash(rel)
			it := Item{Src: p, Dst: path.Join(remoteDir, rel), Rel: rel, IsDir: d.IsDir()}
			if d.Type()&fs.ModeSymlink != 0 {
				fi, err := os.Stat(p)
				if err != nil {
					return nil // a broken link is skipped
				}
				if fi.IsDir() {
					return nil // linked folders are not followed
				}
			}
			if !it.IsDir {
				fi, err := os.Stat(p)
				if err != nil {
					return err
				}
				it.Size = fi.Size()
				_, err = withCtx(ctx, func() (os.FileInfo, error) { return c.c.Lstat(it.Dst) })
				if cerr := ctx.Err(); cerr != nil {
					return cerr
				}
				it.Exists = err == nil
			}
			out = append(out, it)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Limits on what a download plan walks, so that a server answering with
// endless folders (a loop, or a "../" entry) cannot hang it.
var (
	// PlanMaxDepth is how many folders deep a plan goes.
	PlanMaxDepth = 64
	// PlanMaxEntries is how many files and folders a plan holds.
	PlanMaxEntries = 200_000
)

// Errors a download plan stops with.
var (
	ErrPlanTooDeep  = errors.New("folders nested too deep")
	ErrPlanTooLarge = errors.New("too many files and folders")
	ErrPlanLoop     = errors.New("a folder contains itself")
)

// Skip is something a download plan leaves out, and why.
type Skip struct {
	// Src is the remote path, Rel the path below the folder chosen.
	Src, Rel string
	Reason   SkipReason
}

// SkipReason is why a download plan leaves something out.
type SkipReason int

const (
	// SkipName is a name the server allows that is not safe as a local
	// file name (SafeName): "aux.c", "10:30.log", "x.".
	SkipName SkipReason = iota
	// SkipLinkedFolder is a link to a folder inside a folder being
	// downloaded: links are followed only for what was chosen.
	SkipLinkedFolder
	// SkipBrokenLink is a link whose target cannot be read.
	SkipBrokenLink
)

// PlanDownload lists what downloading the remote paths into localDir
// copies: folders recursively, folders before what is in them. What it
// cannot copy safely it leaves out and returns as skips, and the rest
// still downloads: a name that does not pass SafeName, a link to a
// folder inside a folder, a link to nothing. A path chosen that is
// itself a link to a folder is followed (the server resolves it; the
// loop check and the limits below apply as to any folder). Names no file
// can have ("", ".", "..", or with "/" or NUL: a server's trick) are
// dropped, as List hides them. Where each item goes stays in localDir.
//
// It stops with an error at PlanMaxDepth, at PlanMaxEntries (items,
// skips and listed entries not yet planned, all counted), and at a
// folder met again (by its real path). Folders are listed a reply at a
// time on a second SFTP session (dirPager), so a folder of a million
// names stops at the limit, holding no more than it; only when the
// server refuses that session, or it breaks (a protocol error, from the
// folder it broke in on), is each folder read whole first.
func (c *Client) PlanDownload(ctx context.Context, srcs []string, localDir string) ([]Item, []Skip, error) {
	return c.planRemote(ctx, srcs, planTarget{
		name: SafeName,
		dst:  func(rel string) (string, error) { return LocalPath(localDir, rel) },
		exists: func(p string) (bool, error) {
			_, err := os.Lstat(p)
			return err == nil, nil
		},
	})
}

// PlanRelay lists what copying the paths of this server into remoteDir
// on the server of to copies, as PlanDownload does for a local folder:
// the same walk, links, limits and skips, but names are checked as names
// on a server (RemoteName: a single name, without "/", "\" or NUL), as
// any other is allowed there. Where each item goes stays in remoteDir.
// to may be c itself (another folder of the same server).
func (c *Client) PlanRelay(ctx context.Context, srcs []string, to *Client, remoteDir string) ([]Item, []Skip, error) {
	return c.planRemote(ctx, srcs, planTarget{
		name: RemoteName,
		dst:  func(rel string) (string, error) { return RemotePath(remoteDir, rel) },
		exists: func(p string) (bool, error) {
			_, err := withCtx(ctx, func() (os.FileInfo, error) { return to.c.Lstat(p) })
			if cerr := ctx.Err(); cerr != nil {
				return false, cerr
			}
			return err == nil, nil
		},
	})
}

// planTarget is where a plan of a server's paths copies them to: name
// checks each name the server sent, dst makes a path below the folder
// chosen, and exists says something is already at one.
type planTarget struct {
	name   func(string) error
	dst    func(rel string) (string, error)
	exists func(p string) (bool, error)
}

// planRemote is PlanDownload and PlanRelay, to the target to.
func (c *Client) planRemote(ctx context.Context, srcs []string, to planTarget) ([]Item, []Skip, error) {
	var (
		out     []Item
		skips   []Skip
		pending int // entries listed and not yet planned
	)
	seen := map[string]bool{}
	pager, err := c.openPager(ctx)
	if err == nil {
		defer func() {
			if pager != nil {
				pager.Close()
			}
		}()
	} else {
		pager = nil
	}
	tooLarge := func(p string, more int) error {
		if len(out)+len(skips)+pending+more > PlanMaxEntries {
			return fmt.Errorf("sftp: %s: %w (over %d)", p, ErrPlanTooLarge, PlanMaxEntries)
		}
		return nil
	}
	var walk func(p, rel string, fi fs.FileInfo, depth int) error
	walk = func(p, rel string, fi fs.FileInfo, depth int) error {
		pending--
		if err := ctx.Err(); err != nil {
			return err
		}
		// The names come from the server: each must be safe where it
		// goes, and where it goes must stay in the folder chosen.
		if to.name(path.Base(rel)) != nil {
			skips = append(skips, Skip{Src: p, Rel: rel, Reason: SkipName})
			return nil
		}
		dst, err := to.dst(rel)
		if err != nil {
			return fmt.Errorf("sftp: %w", err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			t, err := withCtx(ctx, func() (os.FileInfo, error) { return c.c.Stat(p) })
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case err != nil:
				skips = append(skips, Skip{Src: p, Rel: rel, Reason: SkipBrokenLink})
				return nil
			case t.IsDir() && depth > 0:
				skips = append(skips, Skip{Src: p, Rel: rel, Reason: SkipLinkedFolder})
				return nil
			}
			fi = t
		}
		it := Item{Src: p, Dst: dst, Rel: rel, IsDir: fi.IsDir()}
		if !it.IsDir {
			it.Size = fi.Size()
			if it.Exists, err = to.exists(dst); err != nil {
				return err
			}
			out = append(out, it)
			return nil
		}
		out = append(out, it)
		if depth >= PlanMaxDepth {
			return fmt.Errorf("sftp: %s: %w (over %d)", p, ErrPlanTooDeep, PlanMaxDepth)
		}
		key := p
		if r, err := withCtx(ctx, func() (string, error) { return c.c.RealPath(p) }); err == nil {
			key = r
		}
		if seen[key] {
			return fmt.Errorf("sftp: %s: %w (%s)", p, ErrPlanLoop, key)
		}
		seen[key] = true
		var ents []fs.FileInfo
		add := func(batch []fs.FileInfo) error {
			for _, e := range batch {
				if n := e.Name(); n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00") {
					continue // no file has such a name
				}
				ents = append(ents, e)
			}
			return tooLarge(p, len(ents))
		}
		if pager != nil {
			err = pager.each(p, add)
			if errors.Is(err, errPagerBroken) && ctx.Err() == nil {
				// Partway through, the pager broke: this folder, and
				// the rest, are read whole, as with no pager.
				pager.Close()
				pager, ents = nil, nil
			}
		}
		if pager == nil {
			var fis []os.FileInfo
			if fis, err = c.readDir(ctx, p); err == nil {
				err = add(fis)
			}
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return err
		}
		pending += len(ents)
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for i, e := range ents {
			ents[i] = nil // planned: not held while the rest is
			// Not path.Join: the name is checked as it came.
			if err := walk(strings.TrimSuffix(p, "/")+"/"+e.Name(), rel+"/"+e.Name(), e, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, src := range srcs {
		// Before asking the server. A name with "\" does not survive a
		// server that treats it as a separator, so the stat would miss
		// the file instead of leaving it out.
		base := path.Base(src)
		if to.name(base) != nil {
			skips = append(skips, Skip{Src: src, Rel: base, Reason: SkipName})
			continue
		}
		fi, err := withCtx(ctx, func() (os.FileInfo, error) { return c.c.Lstat(src) })
		if err != nil {
			return nil, nil, err
		}
		if err := tooLarge(src, 1); err != nil {
			return nil, nil, err
		}
		pending++
		if err := walk(src, path.Base(src), fi, 0); err != nil {
			return nil, nil, err
		}
	}
	return out, skips, nil
}

// UniqueName returns p, or "name (2).ext" and so on, the first that
// taken does not report as taken.
func UniqueName(p string, remote bool, taken func(string) bool) string {
	if !taken(p) {
		return p
	}
	dir, base := filepath.Split(p)
	if remote {
		dir, base = path.Split(p)
	}
	ext := nameExt(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; ; i++ {
		q := dir + stem + " (" + strconv.Itoa(i) + ")" + ext
		if !taken(q) {
			return q
		}
	}
}

// nameExt is the extension of the file name base that a numbered copy
// keeps after its number: ".gz", or ".tar.gz" (with ".tar" before any
// last extension); none for ".bashrc".
func nameExt(base string) string {
	ext := path.Ext(base)
	if ext == base {
		return ""
	}
	stem := strings.TrimSuffix(base, ext)
	if inner := path.Ext(stem); inner != stem && strings.EqualFold(inner, ".tar") {
		ext = inner + ext
	}
	return ext
}

// Exists says whether something is at the remote path.
func (c *Client) Exists(p string) bool {
	_, err := c.c.Lstat(p)
	return err == nil
}

// LocalExists says whether something is at the local path.
func LocalExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// MkdirAll makes the remote folder and its parents.
func (c *Client) MkdirAll(p string) error {
	if err := c.checkTransferParent(context.Background(), p); err != nil {
		return err
	}
	return c.c.MkdirAll(p)
}

// Stat describes the remote path, following links.
func (c *Client) Stat(p string) (Entry, error) {
	fi, err := c.c.Stat(p)
	if err != nil {
		return Entry{}, err
	}
	return entry(fi), nil
}

// Rename renames the remote path within its folder to name.
func (c *Client) Rename(p, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	to := path.Join(path.Dir(p), name)
	if c.Exists(to) {
		return "", fs.ErrExist
	}
	return to, c.c.Rename(p, to)
}

// RenameLocal renames the local path within its folder to name.
func RenameLocal(p, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	to := filepath.Join(filepath.Dir(p), name)
	if LocalExists(to) {
		return "", fs.ErrExist
	}
	return to, os.Rename(p, to)
}

// Remove deletes the remote path, a folder with what is in it.
func (c *Client) Remove(p string) error {
	fi, err := c.c.Lstat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return c.c.RemoveAll(p)
	}
	return c.c.Remove(p)
}

// RemoveLocal deletes the local path, a folder with what is in it.
func RemoveLocal(p string) error {
	if _, err := os.Lstat(p); err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// Chmod sets the remote path's permission bits.
func (c *Client) Chmod(p string, mode fs.FileMode) error { return c.c.Chmod(p, mode.Perm()) }

// CreateFile makes an empty file named name in the remote folder dir.
func (c *Client) CreateFile(dir, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	p := path.Join(dir, name)
	f, err := c.c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", err
	}
	return p, f.Close()
}

// CreateLocalFile makes an empty file named name in the local folder dir.
func CreateLocalFile(dir, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	return p, f.Close()
}

// ParseMode reads permission bits written in octal, as "644" or "0755".
func ParseMode(s string) (fs.FileMode, error) {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 || s == "" {
		return 0, fmt.Errorf("sftp: %q is not an octal mode such as 644", s)
	}
	return fs.FileMode(n), nil
}

// Version is what a remote file was when read: an edit is saved over it
// only if it is still that.
type Version struct {
	Size    int64
	ModTime time.Time
	// Sum is the SHA-256 of the content read or written, for a file of
	// at most SumMax bytes (zero for a larger one, and from Version): an
	// edit that keeps the size and the time (within a second, on
	// servers that keep only seconds) still changes it.
	Sum [sha256.Size]byte
}

// SumMax is the largest file whose Version has a Sum: saving one reads
// it again to compare.
const SumMax = 1 << 20

// withSum is v with the Sum of data, its content, if small enough.
func (v Version) withSum(data []byte) Version {
	if len(data) <= SumMax && int64(len(data)) == v.Size {
		v.Sum = sha256.Sum256(data)
	}
	return v
}

// ErrChanged is a remote file changed since it was read.
var ErrChanged = errors.New("sftp: the file changed on the server since it was opened")

// DefaultReadLimit bounds editor-independent and AI reads.
const DefaultReadLimit int64 = 10 << 20

var ErrReadLimit = errors.New("sftp: file exceeds read limit")

// ReadFile reads at most DefaultReadLimit bytes.
func (c *Client) ReadFile(p string) ([]byte, Version, error) {
	return c.ReadFileContext(context.Background(), p, DefaultReadLimit)
}

// ReadFileContext uses a dedicated SFTP session, so cancellation interrupts
// stalled server requests without closing other files or the SSH connection.
func (c *Client) ReadFileContext(ctx context.Context, p string, limit int64) ([]byte, Version, error) {
	return c.readFileContext(ctx, p, limit, false)
}

// ReadFileNoLinksContext repeats the path check on the operation session
// immediately before opening, rather than trusting an earlier policy check.
func (c *Client) ReadFileNoLinksContext(ctx context.Context, p string, limit int64) ([]byte, Version, error) {
	return c.readFileContext(ctx, p, limit, true)
}

func (c *Client) readFileContext(ctx context.Context, p string, limit int64, noLinks bool) ([]byte, Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, Version{}, err
	}
	if limit <= 0 || limit > 1<<30 {
		return nil, Version{}, ErrReadLimit
	}
	sub, err := c.operationClient(ctx)
	if err != nil {
		return nil, Version{}, err
	}
	defer sub.Close()
	stop := context.AfterFunc(ctx, func() { sub.Close() })
	defer stop()
	if noLinks {
		p, err = sub.resolvePath(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			return nil, Version{}, err
		}
	}
	data, ver, err := sub.readBounded(p, limit)
	if ctx.Err() != nil {
		return nil, Version{}, ctx.Err()
	}
	return data, ver, err
}

func (c *Client) readBounded(p string, limit int64) ([]byte, Version, error) {
	f, err := c.c.Open(p)
	if err != nil {
		return nil, Version{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, Version{}, err
	}
	if !fi.Mode().IsRegular() {
		return nil, Version{}, errors.New("sftp: not a regular file")
	}
	if fi.Size() < 0 || fi.Size() > limit {
		return nil, Version{}, ErrReadLimit
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, Version{}, err
	}
	if int64(len(data)) > limit {
		return nil, Version{}, ErrReadLimit
	}
	after, err := f.Stat()
	if err != nil {
		return nil, Version{}, err
	}
	if int64(len(data)) != fi.Size() || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		return nil, Version{}, ErrChanged
	}
	return data, Version{Size: fi.Size(), ModTime: fi.ModTime()}.withSum(data), nil
}

// Version is the remote file's version now.
func (c *Client) Version(p string) (Version, error) {
	fi, err := c.c.Stat(p)
	if err != nil {
		return Version{}, err
	}
	return Version{Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// WriteFile replaces the remote file with data, unless it is no longer
// the version was (ErrChanged); a zero was writes regardless. It returns
// the new version. With a Sum in was, the file is read again and
// compared, as its size and time may not show an edit.
//
// The file is replaced atomically: data goes to a hidden sibling
// (".sshyard-…-name", mode 0600 while written), which gets the file's
// mode (and its owner and group, when they differ and the server lets
// it) and is renamed over it with posix-rename@openssh.com; a reader sees
// the old file or the new one, and a failed or cut save leaves the old
// one. Without that extension, the file is moved aside, the copy put in
// its place, and the old one removed (or put back if that fails). A link
// is written through (its target replaced). The file is written in place
// as before, truncated, only when a copy cannot replace it as it is: the
// folder is not writable, or its owner or group cannot be kept. A file
// the user may not write is not replaced either (the permission error).
// A replaced file is a new inode: hard links to the old one keep it.
func (c *Client) WriteFile(p string, data []byte, was Version) (Version, error) {
	return c.writeFile(p, data, was, false)
}

// WriteFileNoLinks does not adopt a link that appeared after authorization.
func (c *Client) WriteFileNoLinks(p string, data []byte, was Version) (Version, error) {
	return c.writeFile(p, data, was, true)
}

// WriteFileNoLinksContext keeps validation and atomic replacement on a dedicated
// cancellable session. Cancellation cannot undo a rename already completed.
func (c *Client) WriteFileNoLinksContext(ctx context.Context, p string, data []byte, was Version) (Version, error) {
	sub, err := c.operationClient(ctx)
	if err != nil {
		return Version{}, err
	}
	defer sub.Close()
	stop := context.AfterFunc(ctx, func() { sub.Close() })
	defer stop()
	ver, err := sub.writeFileContext(ctx, p, data, was, true)
	if ctx.Err() != nil {
		return Version{}, ctx.Err()
	}
	return ver, err
}

func (c *Client) writeFile(p string, data []byte, was Version, noLinks bool) (Version, error) {
	return c.writeFileContext(context.Background(), p, data, was, noLinks)
}

func (c *Client) writeFileContext(ctx context.Context, p string, data []byte, was Version, noLinks bool) (Version, error) {
	if noLinks {
		if _, err := c.resolvePath(ctx, p); err != nil {
			return Version{}, err
		}
	}
	// An old file a cut save left aside comes back first.
	c.cleanBeforeSave(p)
	if !was.ModTime.IsZero() {
		now, err := c.Version(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Version{}, err
		}
		if err != nil || now.Size != was.Size || !now.ModTime.Equal(was.ModTime) {
			return Version{}, ErrChanged
		}
		if was.Sum != ([sha256.Size]byte{}) {
			limit := max(DefaultReadLimit, was.Size)
			if limit > 1<<30 {
				return Version{}, ErrReadLimit
			}
			if noLinks {
				if _, err := c.resolvePath(ctx, p); err != nil {
					return Version{}, err
				}
			}
			_, cur, err := c.readBounded(p, limit)
			if err != nil {
				return Version{}, err
			}
			if cur.Sum != was.Sum {
				return Version{}, ErrChanged
			}
		}
	}
	target, fi, err := c.linkTarget(p)
	if err != nil {
		return Version{}, err
	}
	if target != p {
		if noLinks {
			return Version{}, ErrUnsafePath
		}
		c.cleanBeforeSave(target)
		if target, fi, err = c.linkTarget(p); err != nil {
			return Version{}, err
		}
	}
	if fi != nil {
		if !fi.Mode().IsRegular() {
			return Version{}, fmt.Errorf("%s is not a regular file", p)
		}
		// Replacing needs only the folder writable: is the file?
		f, err := c.c.OpenFile(target, os.O_WRONLY)
		if err != nil {
			return Version{}, err
		}
		f.Close()
	}
	if noLinks {
		if _, err := c.resolvePath(ctx, p); err != nil {
			return Version{}, err
		}
	}
	if err := c.replace(target, data, fi); errors.Is(err, errInPlace) {
		if noLinks {
			return Version{}, err
		}
		if err := c.writeInPlace(target, data); err != nil {
			return Version{}, err
		}
	} else if err != nil {
		return Version{}, err
	}
	if afterRename != nil {
		afterRename()
	}
	v, err := c.Version(p)
	if err != nil {
		return Version{}, err
	}
	// The stat is a round trip after the rename. Another writer can land
	// there. A different size leaves Sum zero, so the next save would
	// take their file as the one just written and overwrite it.
	if v.Size != int64(len(data)) {
		return Version{}, ErrChanged
	}
	return v.withSum(data), nil
}

// linkTarget follows p's links (not RealPath: some servers do not
// resolve links there) to the file a write of p writes, and returns it
// with its info, nil when it does not exist (a dangling link: its target
// is created).
func (c *Client) linkTarget(p string) (string, fs.FileInfo, error) {
	for range 40 {
		fi, err := c.c.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return p, nil, nil
		} else if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return p, fi, err
		}
		to, err := c.c.ReadLink(p)
		if err != nil {
			return "", nil, err
		}
		if !path.IsAbs(to) {
			to = path.Join(path.Dir(p), to)
		}
		p = to
	}
	return "", nil, fmt.Errorf("%s: too many links", p)
}

// errInPlace: a copy cannot replace the file as it is.
var errInPlace = errors.New("write in place")

// posixRename says whether the server replaces files atomically with
// posix-rename@openssh.com.
func (c *Client) posixRename() bool {
	_, ok := c.c.HasExtension("posix-rename@openssh.com")
	return ok && !noPosixRename
}

// noPosixRename, for tests, acts as if the server lacked
// posix-rename@openssh.com.
var noPosixRename bool

// beforeReplace, when set (by tests), runs before the copy tmp replaces
// p; an error stops the save there.
var beforeReplace func(tmp, p string) error

// afterAside, for tests, runs once a save without posix-rename moved the
// old file to its backup; an error cuts the save there.
var afterAside func(aside, p string) error

// replace writes data to a sibling of p and renames it over p, giving it
// old's mode, owner and group (old nil: p does not exist).
func (c *Client) replace(p string, data []byte, old fs.FileInfo) error {
	tmp := tempName(p)
	f, err := c.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if errors.Is(err, fs.ErrPermission) {
		return errInPlace
	} else if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			c.c.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if old != nil {
		if err := c.keepOwner(f, old); err != nil {
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if _, ok := c.c.HasExtension("fsync@openssh.com"); ok {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	mode := fs.FileMode(0o644)
	if old != nil {
		mode = old.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if beforeReplace != nil {
		if err := beforeReplace(tmp, p); err != nil {
			return err
		}
	}
	if err := c.renameOver(tmp, p, old != nil, data); err != nil {
		return err
	}
	done = true
	return nil
}

// keepOwner gives f old's owner and group if they differ, or returns
// errInPlace when the server refuses.
func (c *Client) keepOwner(f *sftp.File, old fs.FileInfo) error {
	want, ok := old.Sys().(*sftp.FileStat)
	if !ok {
		return nil
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if got, ok := fi.Sys().(*sftp.FileStat); ok && got.UID == want.UID && got.GID == want.GID {
		return nil
	}
	if err := f.Chown(int(want.UID), int(want.GID)); err != nil {
		return errInPlace
	}
	return nil
}

// renameOver renames tmp to p, replacing p if exists; data is what tmp
// holds.
func (c *Client) renameOver(tmp, p string, exists bool, data []byte) error {
	if c.posixRename() {
		return c.c.PosixRename(tmp, p)
	}
	if !exists {
		return c.c.Rename(tmp, p)
	}
	// Plain SFTP rename does not replace: move p aside to a backup first.
	// Cut before the copy is in place, the backup is the old file: the
	// next save of the file puts it back (cleanBeforeSave); listing its
	// folder offers to (cleanLeftovers).
	aside := backupName(p)
	// What the copy is, to tell it from a backup put back over it.
	copied, err := c.c.Lstat(tmp)
	if err != nil {
		return err
	}
	if err := c.c.Rename(p, aside); err != nil {
		return err
	}
	// Dated now, so that a listing (which removes backups whose file is
	// there an hour on) tells it from one an hour old. Best effort.
	now := time.Now()
	c.c.Chtimes(aside, now, now)
	if afterAside != nil {
		if err := afterAside(aside, p); err != nil {
			return err
		}
	}
	if err := c.c.Rename(tmp, p); err != nil {
		if rerr := c.c.Rename(aside, p); rerr != nil {
			if _, serr := c.c.Lstat(p); serr == nil {
				return err // put back meanwhile
			}
			return fmt.Errorf("%w (the old file is %s until the folder is next listed)", err, path.Base(aside))
		}
		return err
	}
	if afterSwap != nil {
		afterSwap(aside, p)
	}
	if err := c.c.Remove(aside); errors.Is(err, fs.ErrNotExist) {
		// Another window's cleanup took the backup: if it put it back
		// over the copy (some servers' rename replaces), say so.
		if fi, err := c.c.Lstat(p); err != nil || fi.Size() != copied.Size() || !fi.ModTime().Equal(copied.ModTime()) || fi.Mode() != copied.Mode() {
			return ErrSaveUndone
		}
		// Times have a second's precision: an old version of the same
		// size saved the same second looks the same. Small files are
		// compared whole.
		if copied.Size() <= undoneCompareMax {
			if now, _, err := c.ReadFile(p); err != nil || sha256.Sum256(now) != sha256.Sum256(data) {
				return ErrSaveUndone
			}
		}
	}
	return nil
}

// ErrSaveUndone: while a save without posix-rename was replacing a
// file, another window (cleaning up a cut save) put its old version back
// over the new one. It is an ErrChanged: the file on the server is not
// what was saved, and the user chooses, as for any change there.
var ErrSaveUndone = fmt.Errorf("%w: another window put its old version back while it was saved", ErrChanged)

// undoneCompareMax is the largest saved file whose content is compared
// to tell whether its save was undone.
const undoneCompareMax = 1 << 20

// afterSwap, for tests, runs once a save without posix-rename put its
// copy in place, before it removes the backup.
var afterSwap func(aside, p string)

// afterRename, for tests, runs once the new bytes are in place and before
// the version is read back, where another writer can still land.
var afterRename func()

// writeInPlace writes data over p, truncating it.
func (c *Client) writeInPlace(p string, data []byte) error {
	f, err := c.c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, bytes.NewReader(data))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// IsText says whether data looks like text to edit: UTF-8 without NULs
// in its first 8 KB.
func IsText(data []byte) bool {
	head := data[:min(len(data), 8<<10)]
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	if len(head) < len(data) {
		// A character cut at the end of the 8 KB is fine.
		for i := 0; i < utf8.UTFMax-1 && len(head) > 0 && !utf8.RuneStart(head[len(head)-1]); i++ {
			head = head[:len(head)-1]
		}
		if len(head) > 0 && head[len(head)-1] >= utf8.RuneSelf {
			head = head[:len(head)-1]
		}
	}
	return utf8.Valid(head)
}
