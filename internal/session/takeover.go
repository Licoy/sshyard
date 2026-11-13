package session

import (
	"context"
	"io"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
)

// Takeover finds, in what the shell prints, a transfer that takes the
// shell's byte stream over (ZMODEM, trzsz): at is where in p it starts,
// and run carries it, -1 and nil for none. run reads the shell's output
// from at on, writes to the shell as it is, and returns the output it
// read past the end of the transfer, which the terminal then shows. Its
// ctx ends when the session loses the shell. Find runs on the session's
// goroutine and must not wait.
type Takeover func(p []byte) (at int, run func(ctx context.Context, c xfer.Conn) (rest []byte))

// SetTakeover makes fn look for transfers in what the shell prints from
// now on; nil stops looking.
func (s *Session) SetTakeover(fn Takeover) {
	if fn == nil {
		s.takeover.Store(nil)
		return
	}
	s.takeover.Store(&fn)
}

// TakenOver reports whether a transfer has the shell's stream now: the
// terminal shows nothing of it, and typed input is refused.
func (s *Session) TakenOver() bool {
	return s.taken.Load() != nil
}

// takenOver is a transfer that has the stream.
type takenOver struct {
	q      *xfer.Queue
	done   chan struct{}
	cancel context.CancelFunc
}

// queueMax is how much output waits for a transfer before the shell's
// output is held up.
const queueMax = 4 << 20

// tailLen is how much of what was printed last is kept, to find a
// transfer whose start came split over two writes.
const tailLen = 64

// find looks for a transfer in p, or starting in the end of what was
// printed before it. It returns what in p is printed before it, and what
// it reads first.
func (s *Session) find(fn Takeover, p []byte) (before, from []byte, run func(context.Context, xfer.Conn) []byte) {
	if at, run := fn(p); run != nil {
		return p[:at], p[at:], run
	}
	if len(s.tail) == 0 {
		return nil, nil, nil
	}
	joined := append(append([]byte(nil), s.tail...), p[:min(len(p), 2*tailLen)]...)
	at, run := fn(joined)
	if run == nil {
		return nil, nil, nil
	}
	if off := at - len(s.tail); off >= 0 {
		return p[:off], p[off:], run
	}
	// It started in what was printed already, which the terminal showed.
	return nil, append(append([]byte(nil), s.tail[at:]...), p...), run
}

// keepTail keeps the end of what was printed.
func (s *Session) keepTail(p []byte) {
	if len(p) >= tailLen {
		s.tail = append(s.tail[:0], p[len(p)-tailLen:]...)
		return
	}
	if over := len(s.tail) + len(p) - tailLen; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
	}
	s.tail = append(s.tail, p...)
}

// startTakeover hands the stream, from what from holds on, to run. It
// reports false while there is no shell to take over. Called holding
// outMu.
func (s *Session) startTakeover(from []byte, run func(context.Context, xfer.Conn) []byte) bool {
	s.mu.Lock()
	in := s.stdin
	s.mu.Unlock()
	if in == nil {
		return false
	}
	q := xfer.NewQueue(queueMax)
	q.Add(from)
	ctx, cancel := context.WithCancel(context.Background())
	t := &takenOver{q: q, done: make(chan struct{}), cancel: cancel}
	s.taken.Store(t)
	s.tail = s.tail[:0]
	go s.carry(ctx, t, in, run)
	return true
}

// carry runs a transfer, then gives the terminal back what it left.
func (s *Session) carry(ctx context.Context, t *takenOver, in *serializedInput, run func(context.Context, xfer.Conn) []byte) {
	// Typed input is refused, and replies wait, while the transfer writes.
	in.mu.Lock()
	rest := run(ctx, &takeConn{q: t.q, w: in.raw()})
	in.mu.Unlock()
	t.cancel()
	left := t.q.Close()
	s.outMu.Lock()
	s.taken.Store(nil)
	close(t.done)
	if p := append(rest, left...); len(p) > 0 {
		(&termWriter{s: s}).show(p)
	}
	s.outMu.Unlock()
	s.changed()
}

// endTakeover ends the transfer that has the stream, when the shell is
// gone. It never waits for the output, which may be stuck.
func (s *Session) endTakeover() {
	if t := s.taken.Load(); t != nil {
		t.cancel()
		t.q.Close()
	}
}

// takeConn is the stream as a transfer has it.
type takeConn struct {
	q *xfer.Queue
	w io.Writer
}

func (c *takeConn) Read(p []byte) (int, error)        { return c.q.Read(p) }
func (c *takeConn) Write(p []byte) (int, error)       { return c.w.Write(p) }
func (c *takeConn) SetReadDeadline(t time.Time) error { return c.q.SetReadDeadline(t) }
