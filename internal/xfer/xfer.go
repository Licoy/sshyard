// Package xfer is what the transfers carried over a terminal's own byte
// stream (ZMODEM, trzsz) share: the stream as they see it, the files
// they carry, where those come from and where they go.
package xfer

import (
	"errors"
	"io"
	"io/fs"
	"time"
)

// Conn is the shell's byte stream while a transfer has it: Read gives
// what the shell prints, from where the transfer was found on, Write
// sends to the shell as it is, never as typed (no newline translation,
// no local echo), and nothing typed reaches the shell meanwhile.
type Conn interface {
	io.Reader
	io.Writer
	// SetReadDeadline makes a Read waiting past t return an error that
	// matches os.ErrDeadlineExceeded; the zero time waits for ever. What
	// is already waiting is read even past t, so a deadline in the past
	// polls. A Read after the session lost the shell returns io.EOF.
	SetReadDeadline(t time.Time) error
}

// File is a file or folder a transfer carries.
type File struct {
	// Name is its path in the transfer, slash-separated: a base name, or
	// a path below a folder sent whole. It comes from the other end as
	// it is: whoever makes it a local name must check it.
	Name string
	// Size is its size in bytes, -1 when the other end did not say.
	Size int64
	// ModTime is when it last changed, zero when not said.
	ModTime time.Time
	// Mode holds its permission bits, 0 when not said.
	Mode fs.FileMode
	// Dir marks a folder: it has no content.
	Dir bool
}

// ErrSkip, from Sink.Create, makes the transfer skip the file and go on
// with the next.
var ErrSkip = errors.New("xfer: skip this file")

// ErrRefused is a transfer the other end refused or cancelled.
var ErrRefused = errors.New("xfer: cancelled by the other end")

// Writer takes a file's content as it is received.
type Writer interface {
	io.Writer
	// Commit keeps the file, once all of it came and checked out.
	Commit() error
	// Abort drops what was written; Commit or Abort is called once.
	Abort()
}

// Sink is where received files go.
type Sink interface {
	// Create starts receiving f, saved as local: its slash-separated
	// path, below where the files go, which may differ from f.Name for a
	// name already taken. For a folder it makes it and returns a nil
	// Writer. ErrSkip skips f; any other error ends the transfer.
	Create(f File) (local string, w Writer, err error)
}

// Source is what is sent.
type Source interface {
	// Files lists what is sent, in order, a folder before what is in it.
	Files() []File
	// Open opens the content of Files()[i], a file, which the transfer
	// closes.
	Open(i int) (io.ReadCloser, error)
}

// Progress is told how a transfer goes: done is 0 when f starts, and
// grows as its bytes go through. It may be nil, and must not wait.
type Progress func(f File, done int64)
