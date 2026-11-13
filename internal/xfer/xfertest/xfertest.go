// Package xfertest has what tests of transfers over a terminal use: both
// ends of a byte stream, and files kept in memory.
package xfertest

import (
	"bytes"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
)

// End is one end of a Pipe.
type End struct {
	in   *xfer.Queue
	peer *End
	mu   sync.Mutex
	// Tamper, when set, changes what this end writes on its way.
	Tamper func(p []byte) []byte
	// Wrote is all this end wrote, as it went out.
	Wrote bytes.Buffer
}

// Pipe returns both ends of a stream: what one writes, the other reads.
func Pipe() (a, b *End) {
	a = &End{in: xfer.NewQueue(0)}
	b = &End{in: xfer.NewQueue(0)}
	a.peer, b.peer = b, a
	return a, b
}

func (e *End) Read(p []byte) (int, error)        { return e.in.Read(p) }
func (e *End) SetReadDeadline(t time.Time) error { return e.in.SetReadDeadline(t) }

func (e *End) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	q := append([]byte(nil), p...)
	if e.Tamper != nil {
		q = e.Tamper(q)
	}
	e.Wrote.Write(q)
	if !e.peer.in.Add(q) {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

// Close ends what this end reads, returning what it left unread.
func (e *End) Close() []byte { return e.in.Close() }

var _ xfer.Conn = (*End)(nil)

// Sink keeps received files in memory.
type Sink struct {
	mu sync.Mutex
	// Files are those committed, by name.
	Files map[string][]byte
	// Dirs are the folders made.
	Dirs []string
	// Info is what was said of each file created.
	Info []xfer.File
	// Aborted counts the files dropped.
	Aborted int
	// Skip names files to skip; Fail fails Create for every file.
	Skip map[string]bool
	Fail error
	// Rename gives the local name of a file, when set.
	Rename func(name string) string
}

func (s *Sink) Create(f xfer.File) (string, xfer.Writer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Info = append(s.Info, f)
	if s.Fail != nil {
		return "", nil, s.Fail
	}
	if s.Skip[f.Name] {
		return "", nil, xfer.ErrSkip
	}
	local := f.Name
	if s.Rename != nil {
		local = s.Rename(local)
	}
	if f.Dir {
		s.Dirs = append(s.Dirs, local)
		return local, nil, nil
	}
	return local, &writer{s: s, name: local}, nil
}

type writer struct {
	s    *Sink
	name string
	buf  bytes.Buffer
	done bool
}

func (w *writer) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *writer) Commit() error {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.done {
		return errors.New("xfertest: committed twice")
	}
	w.done = true
	if w.s.Files == nil {
		w.s.Files = map[string][]byte{}
	}
	w.s.Files[w.name] = w.buf.Bytes()
	return nil
}

func (w *writer) Abort() {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if !w.done {
		w.done = true
		w.s.Aborted++
	}
}

// Committed returns the names of the files committed, sorted.
func (s *Sink) Committed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for n := range s.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Item is a file or folder a Source sends.
type Item struct {
	File xfer.File
	Data []byte
}

// Source sends Items in order, their content seekable.
type Source struct {
	Items []Item
	// Opened counts the files opened.
	Opened int
}

func (s *Source) Files() []xfer.File {
	var fs []xfer.File
	for _, it := range s.Items {
		f := it.File
		if !f.Dir && f.Size == 0 {
			f.Size = int64(len(it.Data))
		}
		fs = append(fs, f)
	}
	return fs
}

func (s *Source) Open(i int) (io.ReadCloser, error) {
	if i < 0 || i >= len(s.Items) || s.Items[i].File.Dir {
		return nil, errors.New("xfertest: not a file")
	}
	s.Opened++
	return readSeekCloser{bytes.NewReader(s.Items[i].Data)}, nil
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }
