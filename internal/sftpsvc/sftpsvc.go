// Package sftpsvc is SSHYard's file transfer service over SFTP
// (github.com/pkg/sftp): it lists remote and local directories and
// uploads or downloads a single file at a time, reporting progress.
package sftpsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Entry is a file or directory of a listing.
type Entry struct {
	Name    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
	// UID and GID are the owner and the group, when IDs says they are
	// known (not on Windows).
	UID, GID uint32
	IDs      bool
}

func sortEntries(es []Entry) {
	slices.SortFunc(es, func(a, b Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

func entry(fi fs.FileInfo) Entry {
	e := Entry{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}
	if st, ok := fi.Sys().(*sftp.FileStat); ok {
		e.UID, e.GID, e.IDs = st.UID, st.GID, true
	} else {
		e.UID, e.GID, e.IDs = localIDs(fi)
	}
	return e
}

// Client is an SFTP session on an SSH connection.
type Client struct {
	c *sftp.Client
	// conn is the connection, for a second SFTP session (dirPager)
	conn       *ssh.Client
	operation  *ssh.Session
	operations *operationPool
	// scp is set when the server has no SFTP and its shell and scp
	// stand in (scpfs).
	scp bool
	// protocol is how the Client's channels reach the files.
	protocol Protocol
	// sudo is set when each channel runs the SFTP server through sudo.
	sudo *sudoAuth
	// closed ends when the Client closes, and with it the channels of
	// the transfers it opened (Transfers).
	closed     context.Context
	markClosed context.CancelFunc
	// reused is set on a channel taken from the pool (operationPool),
	// watched once the pool watches it for its end.
	reused, watched bool
}

// opened readies a Client whose channel was just opened.
func opened(c *Client) *Client {
	c.closed, c.markClosed = context.WithCancel(context.Background())
	return c
}

// SCP reports a server without SFTP, whose files move with scp.
func (c *Client) SCP() bool { return c.scp }

// Protocol is how a Client reaches the server's files.
type Protocol int

const (
	// Auto speaks SFTP, and scp (scpfs) where the server has none.
	Auto Protocol = iota
	// SFTPOnly speaks SFTP; a server without it is an error (ErrNoSFTP).
	SFTPOnly
	// SCPOnly moves files with the server's shell and scp, whether or
	// not it has SFTP.
	SCPOnly
)

// ErrNoSFTP is a server without SFTP, to a client that speaks only SFTP.
var ErrNoSFTP = errors.New("sftp: the server has no SFTP")

// Protocol is how c was asked to reach the files.
func (c *Client) Protocol() Protocol { return c.protocol }

// New opens SFTP on an SSH connection; closing the Client does not close
// the connection.
func New(conn *ssh.Client) (*Client, error) {
	return NewContext(context.Background(), conn)
}

// NewContext negotiates only a dedicated channel and shares a pending-channel
// quota with every SFTP client on the same pooled SSH connection.
func NewContext(ctx context.Context, conn *ssh.Client) (*Client, error) {
	return NewProtocol(ctx, conn, Auto)
}

// NewProtocol is NewContext reaching the files as p says.
func NewProtocol(ctx context.Context, conn *ssh.Client, p Protocol) (*Client, error) {
	c := &Client{conn: conn, operations: operationSlots(conn), protocol: p}
	sub, err := c.newOperation(ctx)
	if err != nil {
		return nil, fmt.Errorf("sftp: %w", err)
	}
	return sub, nil
}

// Close ends the SFTP session.
func (c *Client) Close() error {
	if c.markClosed != nil {
		c.markClosed()
	}
	if c.operation != nil {
		c.operation.Close()
	}
	return c.c.Close()
}

// Home is the directory the server starts in.
func (c *Client) Home() (string, error) { return c.c.Getwd() }

// List lists a remote directory, directories first.
func (c *Client) List(dir string) ([]Entry, error) {
	es, _, err := c.ListUnfinished(dir)
	return es, err
}

// ListContext interrupts a stalled listing on its own SFTP channel.
func (c *Client) ListContext(ctx context.Context, dir string) ([]Entry, error) {
	var es []Entry
	err := c.withOperation(ctx, true, func(sub *Client) (err error) {
		es, err = sub.List(dir)
		return err
	})
	if err != nil {
		return nil, err
	}
	return es, nil
}

// peekLinks is how many links a Peek follows at most, peekWorkers how
// many at once.
const (
	peekLinks   = 512
	peekWorkers = 16
)

// Peek lists a remote directory as List does, directories first, but
// only reads it: what an unfinished save left there stays, and of a
// folder with many links the first peekLinks are followed, the others
// listed as files. Closing the Client ends a Peek stalled on a silent
// server.
func (c *Client) Peek(ctx context.Context, dir string) ([]Entry, error) {
	fis, err := c.readDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(fis))
	var links []int
	for _, fi := range fis {
		if n := fi.Name(); n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00") {
			continue
		}
		if fi.Mode()&fs.ModeSymlink != 0 && len(links) < peekLinks {
			links = append(links, len(out))
		}
		out = append(out, entry(fi))
	}
	c.followLinks(ctx, dir, out, links)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortEntries(out)
	return out, nil
}

// followLinks marks the entries of dir at links whose targets are
// directories, so that linked directories open: peekWorkers Stats at
// once, each a round trip. Ending ctx stops handing out links.
func (c *Client) followLinks(ctx context.Context, dir string, out []Entry, links []int) {
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(peekWorkers, len(links)) {
		wg.Go(func() {
			for i := range next {
				if t, err := c.c.Stat(path.Join(dir, out[i].Name)); err == nil {
					out[i].IsDir = t.IsDir()
				}
			}
		})
	}
	for _, i := range links {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
}

// ListUnfinished lists a remote directory, directories first, and the
// files there whose last save did not finish (to Restore or Discard).
func (c *Client) ListUnfinished(dir string) ([]Entry, []Unfinished, error) {
	fis, err := c.c.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	// What cut saves left here: old copies removed, unfinished saves
	// reported.
	changed, unfinished := c.cleanLeftovers(dir, fis)
	if changed {
		if fis, err = c.c.ReadDir(dir); err != nil {
			return nil, nil, err
		}
	}
	out := make([]Entry, 0, len(fis))
	var links []int
	for _, fi := range fis {
		if n := fi.Name(); n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00") {
			continue // no file has such a name: a server's trick (or bug)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			links = append(links, len(out))
		}
		out = append(out, entry(fi))
	}
	// Every link is followed, not only Peek's first peekLinks.
	c.followLinks(context.Background(), dir, out, links)
	sortEntries(out)
	return out, unfinished, nil
}

// ListLocal lists a local directory, directories first.
func ListLocal(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		e := entry(fi)
		if fi.Mode()&fs.ModeSymlink != 0 {
			if t, err := os.Stat(filepath.Join(dir, de.Name())); err == nil {
				e.IsDir = t.IsDir()
			}
		}
		out = append(out, e)
	}
	sortEntries(out)
	return out, nil
}

// ErrBadName is a new folder's name that is empty or not a single name.
var ErrBadName = errors.New("sftp: a folder name must be one name, without slashes")

func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return ErrBadName
	}
	return nil
}

// Mkdir makes a folder named name in the remote directory dir.
func (c *Client) Mkdir(dir, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	p := path.Join(dir, name)
	return p, c.c.Mkdir(p)
}

// MkdirLocal makes a folder named name in the local directory dir.
func MkdirLocal(dir, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.Mkdir(p, 0o755)
}

// RemoteParent is the parent of a remote directory.
func RemoteParent(dir string) string { return path.Dir(path.Clean(dir)) }

// RemoteJoin joins remote path elements.
func RemoteJoin(elem ...string) string { return path.Join(elem...) }

// Progress reports bytes done of total.
type Progress func(done, total int64)

type progressWriter struct {
	ctx   context.Context
	w     io.Writer
	done  int64
	total int64
	fn    Progress
	last  time.Time
	step  func(done int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.w.Write(b)
	p.count(n)
	return n, err
}

func (p *progressWriter) count(n int) {
	p.done += int64(n)
	if p.step != nil {
		p.step(p.done)
	}
	if p.fn != nil && (time.Since(p.last) > 50*time.Millisecond || p.done == p.total) {
		p.last = time.Now()
		p.fn(p.done, p.total)
	}
}

// progressReader counts what it reads as progressWriter counts what it
// writes (its w unused): an SFTP file's ReadFrom reads the source as it
// sends the writes.
type progressReader struct {
	r io.Reader
	p *progressWriter
}

func (pr progressReader) Read(b []byte) (int, error) {
	if err := pr.p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := pr.r.Read(b)
	pr.p.count(n)
	return n, err
}

// Upload copies the local file to remoteDir, under its own name, and
// returns the remote path. A failed upload leaves no partial file.
func (c *Client) Upload(ctx context.Context, local, remoteDir string, progress Progress) (string, error) {
	src, err := os.Open(local)
	if err != nil {
		return "", err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("sftp: %s is a directory; only single files transfer", local)
	}
	remote := path.Join(remoteDir, filepath.Base(local))
	dst, err := c.c.OpenFile(remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", err
	}
	pw := &progressWriter{ctx: ctx, w: dst, total: fi.Size(), fn: progress}
	if progress != nil {
		progress(0, fi.Size())
	}
	_, err = io.Copy(pw, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		c.c.Remove(remote)
		return "", err
	}
	c.c.Chmod(remote, fi.Mode().Perm())
	if progress != nil {
		progress(fi.Size(), fi.Size())
	}
	return remote, nil
}

// Download copies the remote file to localDir, under its own name, and
// returns the local path. It writes a temporary file and renames it, so
// a failed download leaves nothing behind.
func (c *Client) Download(ctx context.Context, remote, localDir string, progress Progress) (string, error) {
	// The name is the server's: it must be safe as a local name.
	name := path.Base(remote)
	if err := SafeName(name); err != nil {
		return "", fmt.Errorf("sftp: %w", err)
	}
	src, err := c.c.Open(remote)
	if err != nil {
		return "", err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("sftp: %s is a directory; only single files transfer", remote)
	}
	local := filepath.Join(localDir, name)
	tmp, err := os.CreateTemp(localDir, "."+name+".part-*")
	if err != nil {
		return "", err
	}
	pw := &progressWriter{ctx: ctx, w: tmp, total: fi.Size(), fn: progress}
	if progress != nil {
		progress(0, fi.Size())
	}
	_, err = io.Copy(pw, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		os.Chmod(tmp.Name(), LocalMode(fi.Mode()))
		err = os.Rename(tmp.Name(), local)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if progress != nil {
		progress(fi.Size(), fi.Size())
	}
	return local, nil
}
