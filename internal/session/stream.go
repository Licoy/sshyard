package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"time"
)

// Stream is a terminal that is no SSH shell: the byte stream of a Telnet
// connection or a serial port (Options.Open), or a local shell's
// pseudo-terminal. Output is read from it and input written to it.
type Stream interface {
	io.ReadWriteCloser
	// Resize tells the other end the terminal's size, where it has one.
	Resize(cols, rows int) error
}

// Prober is a Stream that can measure its round trip, which keeps it
// alive: a Telnet connection's TIMING-MARK.
type Prober interface {
	Probe(ctx context.Context) (time.Duration, error)
}

// Breaker is a Stream that can send a break: a serial port, or Telnet's
// BRK.
type Breaker interface {
	Break(d time.Duration) error
}

// ErrNoBreak is Break on a session whose connection cannot send one.
var ErrNoBreak = errors.New("session: this connection cannot send a break")

// breakFor is how long a break holds the line.
const breakFor = 250 * time.Millisecond

// streamOut is the copy of a stream's output to the terminal: err is
// what ended it, nil at the end of the stream, set before done closes.
type streamOut struct {
	done chan struct{}
	err  error
}

// connectStream opens the session's stream with open, sized as the
// terminal, and copies its output to the terminal until it ends.
func (s *Session) connectStream(open func(cols, rows int) (Stream, error), lg *login) error {
	s.mu.Lock()
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	p, err := open(cols, rows)
	if err != nil {
		return err
	}
	s.Term.ResetOutputStream()
	s.Term.ResetInputModes(true)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		p.Close()
		return context.Canceled
	}
	out := &streamOut{done: make(chan struct{})}
	in := &serializedInput{WriteCloser: p}
	if !s.opts.Local {
		in.WriteCloser = &streamInput{Stream: p, newline: newline(s.opts.NewLine), echo: s.echoer()}
		in.reply = p
	}
	s.stream, s.streamOut, s.stdin = p, out, in
	s.status.State, s.status.Err, s.status.Attempt = Connected, nil, 0
	s.status.Connects++
	s.mu.Unlock()
	w := &termWriter{s: s, login: lg, in: in}
	go func() {
		_, out.err = io.Copy(w, p)
		close(out.done)
	}()
	s.changed()
	return nil
}

// connectOpen connects a session of Options.Open: it reads the
// credentials first when it logs in at the prompts.
func (s *Session) connectOpen(ctx context.Context) error {
	var lg *login
	if s.opts.Login && s.cfg.Credentials != nil {
		cr, err := s.cfg.Credentials(ctx)
		if err != nil {
			return err
		}
		user := s.cfg.User
		if cr.User != "" {
			user = cr.User
		}
		lg = newLogin(user, cr.Password, s.opts.NewLine, time.Now())
	}
	return s.connectStream(func(cols, rows int) (Stream, error) {
		return s.opts.Open(ctx, cols, rows, s.opts.Term)
	}, lg)
}

// serveStream waits for an opened stream to end: nil when the other end
// closed it, as a shell that exited, the error when it was lost.
func (s *Session) serveStream(ctx context.Context, p Stream) error {
	s.mu.Lock()
	out := s.streamOut
	s.mu.Unlock()
	kctx, stop := context.WithCancel(ctx)
	defer stop()
	lost := make(chan error, 1)
	if pr, ok := p.(Prober); ok {
		// A server that never answers a probe does not take them: it is
		// not lost for that.
		go s.keepAlive(kctx, func() error { _, err := pr.Probe(kctx); return err }, true, lost)
	}
	var err error
	select {
	case <-out.done:
		if out.err != nil {
			err = errors.Join(errConnectionLost, out.err)
		}
	case err = <-lost:
	case <-ctx.Done():
	}
	stop()
	s.endTakeover()
	s.Term.ResetInputModes(false)
	p.Close()
	<-out.done
	s.mu.Lock()
	s.stream, s.streamOut, s.stdin = nil, nil, nil
	s.mu.Unlock()
	return err
}

// Break sends a break, on a serial port or a Telnet connection.
func (s *Session) Break() error {
	s.mu.Lock()
	p := s.stream
	s.mu.Unlock()
	b, ok := p.(Breaker)
	if !ok {
		return ErrNoBreak
	}
	return b.Break(breakFor)
}

// CanBreak reports whether Break can send one now.
func (s *Session) CanBreak() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.stream.(Breaker)
	return ok && s.status.State == Connected
}

// echoer writes typed input to the terminal, for Options.LocalEcho.
func (s *Session) echoer() func([]byte) {
	if !s.opts.LocalEcho {
		return nil
	}
	return func(p []byte) {
		s.Term.Write(echoed(p))
		s.changed()
	}
}

// echoed is how typed input shows: Enter as a new line, a delete as the
// character rubbed out.
func echoed(p []byte) []byte {
	var out []byte
	for i, b := range p {
		switch {
		case b == '\r' && (i+1 == len(p) || p[i+1] != '\n'):
			out = append(out, '\r', '\n')
		case b == 0x7f || b == '\b':
			out = append(out, '\b', ' ', '\b')
		default:
			out = append(out, b)
		}
	}
	return out
}

// newline is what Enter sends for a NewLine option: nil for "\r", which
// needs no change.
func newline(nl string) []byte {
	if nl == "" || nl == "\r" {
		return nil
	}
	return []byte(nl)
}

// streamInput is what is typed into a stream: Enter as the host's
// newline, echoed to the terminal for a device that does not echo.
type streamInput struct {
	Stream
	newline []byte
	echo    func([]byte)
}

func (w *streamInput) Write(p []byte) (int, error) {
	q := p
	if w.newline != nil && bytes.IndexByte(p, '\r') >= 0 {
		q = bytes.ReplaceAll(p, []byte{'\r'}, w.newline)
	}
	if w.echo != nil {
		w.echo(q)
	}
	if _, err := w.Stream.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}

// login answers a stream's login and password prompts once, as a user
// would type them, for a while after connecting.
type login struct {
	user, password string
	newline        string
	until          time.Time
	sentUser, done bool
	tail           []byte
}

// loginFor is how long after connecting the prompts are answered.
const loginFor = time.Minute

var (
	userPrompt = regexp.MustCompile(`(?i)\b(login|user ?name|user)\b[^\r\n:]{0,20}:\s*$`)
	passPrompt = regexp.MustCompile(`(?i)\bpass(word|code|phrase)?\b[^\r\n:]{0,40}:\s*$`)
	ansiSeq    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

func newLogin(user, password, nl string, now time.Time) *login {
	if nl == "" {
		nl = "\r"
	}
	return &login{user: user, password: password, newline: nl, until: now.Add(loginFor)}
}

// feed takes output and returns what to answer, nil for nothing.
func (l *login) feed(p []byte, now time.Time) []byte {
	if l.done {
		return nil
	}
	if now.After(l.until) {
		l.done, l.tail = true, nil
		return nil
	}
	l.tail = append(l.tail, p...)
	if n := len(l.tail); n > 512 {
		l.tail = append([]byte(nil), l.tail[n-512:]...)
	}
	text := ansiSeq.ReplaceAll(l.tail, nil)
	switch {
	case passPrompt.Match(text):
		l.done, l.tail = true, nil
		if l.password == "" {
			return nil
		}
		return []byte(l.password + l.newline)
	case !l.sentUser && l.user != "" && userPrompt.Match(text):
		l.sentUser, l.tail = true, nil
		return []byte(l.user + l.newline)
	}
	return nil
}
