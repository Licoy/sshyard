package sshtest

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
)

// FakeEntry is a file or folder a FakeFS lists, under any name at all.
type FakeEntry struct {
	Name string
	Dir  bool
	Size int64
}

// FakeFS is an sftp server (Server.SFTPHandlers) that lists what it is
// told to, names included, as a hostile or broken server may: Dirs maps
// a folder's path to its listing, Files a file's path to what reading it
// gives. Writes keep what was written, by path.
type FakeFS struct {
	mu     sync.Mutex
	Dirs   map[string][]FakeEntry
	Files  map[string][]byte
	Writes map[string][]byte
	// List, when set, lists the folders Dirs does not have.
	List func(p string) ([]FakeEntry, bool)
	// Real, when set, answers realpath requests.
	Real func(p string) string
	// Lists counts listing requests.
	Lists int
	// Listing, when set, is told of each folder whose names are about to
	// be sent, each time (after the folder was opened).
	Listing func(p string)
	// Many, when set, gives folders of n files ("f0000000" on) the others
	// do not have, made as they are listed.
	Many func(p string) (n int, ok bool)
	// Served counts the entries listings have sent.
	Served atomic.Int64

	seen []string

	stallMu sync.Mutex
	stall   chan struct{}
}

// Stop makes the server stop answering: requests from then on (and
// the rest of listings under way) wait until Resume.
func (f *FakeFS) Stop() {
	f.stallMu.Lock()
	defer f.stallMu.Unlock()
	if f.stall == nil {
		f.stall = make(chan struct{})
	}
}

// Resume answers again, the requests that waited first.
func (f *FakeFS) Resume() {
	f.stallMu.Lock()
	defer f.stallMu.Unlock()
	if f.stall != nil {
		close(f.stall)
		f.stall = nil
	}
}

// wait waits while the server is stopped.
func (f *FakeFS) wait() {
	f.stallMu.Lock()
	ch := f.stall
	f.stallMu.Unlock()
	if ch != nil {
		<-ch
	}
}

// Conn is the server's side of a session (Server.SFTPConn): while the
// server is stopped, no reply goes out, whatever the request.
func (f *FakeFS) Conn(rw io.ReadWriteCloser) io.ReadWriteCloser { return stalledConn{rw, f.wait} }

type stalledConn struct {
	io.ReadWriteCloser
	wait func()
}

func (c stalledConn) Write(b []byte) (int, error) {
	c.wait()
	return c.ReadWriteCloser.Write(b)
}

// stalling is a listing that waits while its server is stopped before
// each reply.
type stalling struct {
	l sftp.ListerAt
	f *FakeFS
	p string
}

func (s stalling) ListAt(out []os.FileInfo, off int64) (int, error) {
	if s.f.Listing != nil {
		s.f.Listing(s.p)
	}
	s.f.wait()
	return s.l.ListAt(out, off)
}

// Seen lists the requests served so far ("Method path"), in order.
func (f *FakeFS) Seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// File is what the file p holds, nil when there is none.
func (f *FakeFS) File(p string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.Files[p]...)
}

// Listed counts the listing requests served so far.
func (f *FakeFS) Listed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Lists
}

// note records a request; f.mu is held.
func (f *FakeFS) note(r *sftp.Request) {
	f.seen = append(f.seen, r.Method+" "+r.Filepath)
}

// Handlers serves fs.
func (f *FakeFS) Handlers() sftp.Handlers {
	return sftp.Handlers{FileGet: f, FilePut: f, FileCmd: f, FileList: f}
}

type fakeInfo struct {
	name string
	size int64
	dir  bool
}

func (i fakeInfo) Name() string { return i.name }
func (i fakeInfo) Size() int64  { return i.size }
func (i fakeInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i fakeInfo) ModTime() time.Time { return time.Unix(1700000000, 0) }
func (i fakeInfo) IsDir() bool        { return i.dir }
func (i fakeInfo) Sys() any           { return nil }

type lister []os.FileInfo

func (l lister) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if off+int64(n) >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}

// many lists n files, making each as it is sent.
type many struct {
	n      int64
	served *atomic.Int64
}

func (l many) ListAt(out []os.FileInfo, off int64) (int, error) {
	n := 0
	for ; n < len(out) && off+int64(n) < l.n; n++ {
		out[n] = fakeInfo{name: fmt.Sprintf("f%07d", off+int64(n)), size: 1}
	}
	l.served.Add(int64(n))
	if off+int64(n) >= l.n {
		return n, io.EOF
	}
	return n, nil
}

func (f *FakeFS) stat(p string) (os.FileInfo, bool) {
	if b, ok := f.Files[p]; ok {
		return fakeInfo{name: path.Base(p), size: int64(len(b))}, true
	}
	if _, ok := f.Dirs[p]; ok || p == "/" {
		return fakeInfo{name: path.Base(p), dir: true}, true
	}
	if f.List != nil {
		if _, ok := f.List(p); ok {
			return fakeInfo{name: path.Base(p), dir: true}, true
		}
	}
	if f.Many != nil {
		if _, ok := f.Many(p); ok {
			return fakeInfo{name: path.Base(p), dir: true}, true
		}
	}
	for _, e := range f.Dirs[path.Dir(p)] {
		if e.Name == path.Base(p) {
			return fakeInfo{name: e.Name, size: e.Size, dir: e.Dir}, true
		}
	}
	return nil, false
}

// Filelist lists, stats or reads links.
func (f *FakeFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(r)
	switch r.Method {
	case "List":
		f.Lists++
		es, ok := f.Dirs[r.Filepath]
		if !ok && f.List != nil {
			es, ok = f.List(r.Filepath)
		}
		if !ok && f.Many != nil {
			if n, ok := f.Many(r.Filepath); ok {
				return stalling{many{int64(n), &f.Served}, f, r.Filepath}, nil
			}
		}
		if !ok {
			return nil, os.ErrNotExist
		}
		var l lister
		for _, e := range es {
			l = append(l, fakeInfo{name: e.Name, size: e.Size, dir: e.Dir})
		}
		f.Served.Add(int64(len(l)))
		return stalling{l, f, r.Filepath}, nil
	case "Stat", "Lstat":
		fi, ok := f.stat(r.Filepath)
		if !ok {
			return nil, os.ErrNotExist
		}
		return lister{fi}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// RealPath resolves a path (sftp.RealPathFileLister).
func (f *FakeFS) RealPath(p string) (string, error) {
	if f.Real != nil {
		return f.Real(p), nil
	}
	if !path.IsAbs(p) {
		p = "/" + p
	}
	return path.Clean(p), nil
}

// Fileread reads a file.
func (f *FakeFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(r)
	b, ok := f.Files[r.Filepath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.NewReader(b), nil
}

type fakeWriter struct {
	f *FakeFS
	p string
}

func (w fakeWriter) WriteAt(b []byte, off int64) (int, error) {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	if _, ok := w.f.Files[w.p]; !ok {
		// Removed while open: as with an unlinked file, the writes
		// still queued land nowhere.
		return len(b), nil
	}
	cur := w.f.Writes[w.p]
	if need := int(off) + len(b); need > len(cur) {
		cur = append(cur, make([]byte, need-len(cur))...)
	}
	copy(cur[off:], b)
	w.f.Writes[w.p] = cur
	w.f.Files[w.p] = cur
	return len(b), nil
}

// Filewrite writes a file.
func (f *FakeFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(r)
	if f.Writes == nil {
		f.Writes = map[string][]byte{}
	}
	if f.Files == nil {
		f.Files = map[string][]byte{}
	}
	f.Writes[r.Filepath] = nil
	f.Files[r.Filepath] = nil
	return fakeWriter{f, r.Filepath}, nil
}

// Filecmd renames and removes files; the rest is accepted and ignored.
func (f *FakeFS) Filecmd(r *sftp.Request) error {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(r)
	switch r.Method {
	case "Rename", "PosixRename":
		b, ok := f.Files[r.Filepath]
		if !ok {
			return os.ErrNotExist
		}
		f.Files[r.Target] = b
		delete(f.Files, r.Filepath)
	case "Remove":
		delete(f.Files, r.Filepath)
	}
	return nil
}

// Names lists the files there are, sorted.
func (f *FakeFS) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
