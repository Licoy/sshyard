// Package zmodem carries files over a terminal's own byte stream with
// ZMODEM, as rz and sz on the other end expect: Find spots a transfer the
// other end started in what it prints, Receive takes the files sz sends,
// Send gives rz the files it waits for. It draws nothing; the caller
// asks the user and shows how it goes.
package zmodem

import (
	"bytes"
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/xfer"
)

// Offer is a transfer the other end started.
type Offer struct {
	// Upload says the other end runs rz and waits for files, which we
	// send: it sent ZRINIT. Otherwise it runs sz and sends us files: it
	// sent ZRQINIT.
	Upload bool
}

// Find reports where in p a transfer starts: the first complete hex
// ZRQINIT or ZRINIT header in it whose CRC holds, -1 and nil for none.
func Find(p []byte) (at int, o *Offer) {
	sig := []byte{zpad, zpad, zdle, zhex}
	for off := 0; ; {
		i := bytes.Index(p[off:], sig)
		if i < 0 {
			return -1, nil
		}
		i += off
		if h, ok := parseHex(p[i+len(sig):]); ok && (h.typ == zrqinit || h.typ == zrinit) {
			return i, &Offer{Upload: h.typ == zrinit}
		}
		off = i + 1
	}
}

// parseHex reads the 14 hex digits of a hex header at the start of p.
func parseHex(p []byte) (header, bool) {
	if len(p) < 14 {
		return header{}, false
	}
	var raw [7]byte
	for i := range raw {
		hi, ok1 := unhex(p[2*i])
		lo, ok2 := unhex(p[2*i+1])
		if !ok1 || !ok2 {
			return header{}, false
		}
		raw[i] = hi<<4 | lo
	}
	if crc16(0, raw[:5]) != uint16(raw[5])<<8|uint16(raw[6]) {
		return header{}, false
	}
	return header{typ: raw[0], b: [4]byte{raw[1], raw[2], raw[3], raw[4]}}, true
}

// Options are how a transfer goes.
type Options struct {
	// EscapeControl escapes every control character both ways, for a
	// line that would not carry them as they are (Telnet, serial).
	EscapeControl bool
	// Progress is told how the files go; nil for nothing.
	Progress xfer.Progress
	// Timeout is how long to wait for the other end before asking again;
	// 0 is 10 seconds.
	Timeout time.Duration
}

// maxRetries is how many times in a row a wait times out, or a frame
// comes bad, before the transfer gives up.
const maxRetries = 10

// cancelSeq cancels the other end: the CANs stop it, the backspaces rub
// them out where a shell took them instead.
var cancelSeq = append(bytes.Repeat([]byte{zdle}, 10), bytes.Repeat([]byte{bs}, 10)...)

var errCanceled = errors.New("zmodem: canceled")

// session is one transfer.
type session struct {
	ctx     context.Context
	c       xfer.Conn
	r       *reader
	enc     encoder
	timeout time.Duration
	opts    Options
}

func newSession(ctx context.Context, c xfer.Conn, o Options) *session {
	s := &session{ctx: ctx, c: c, r: &reader{c: c, done: ctx.Done()}, timeout: o.Timeout, opts: o}
	if s.timeout <= 0 {
		s.timeout = 10 * time.Second
	}
	s.enc.ctl = o.EscapeControl
	s.r.ctl = o.EscapeControl
	return s
}

// flush sends what was encoded.
func (s *session) flush() error {
	if len(s.enc.out) == 0 {
		return nil
	}
	_, err := s.c.Write(s.enc.out)
	s.enc.out = s.enc.out[:0]
	return err
}

func (s *session) sendHex(h header) error {
	if err := s.flush(); err != nil {
		return err
	}
	_, err := s.c.Write(hexHeader(h))
	return err
}

func (s *session) progress(f xfer.File, done int64) {
	if s.opts.Progress != nil {
		s.opts.Progress(f, done)
	}
}

// end finishes a transfer that ended with err: one that did not end
// well, nor because the other end cancelled it, cancels the other end
// and drains what it still sends. It returns what the shell printed
// after the transfer.
func (s *session) end(err error) ([]byte, error) {
	if errors.Is(err, errCanceled) || s.ctx.Err() != nil {
		err = context.Cause(s.ctx)
		if err == nil {
			err = context.Canceled
		}
	}
	if err == nil {
		return s.r.rest(), nil
	}
	if errors.Is(err, xfer.ErrRefused) {
		return s.drain(), err
	}
	s.c.Write(cancelSeq)
	return s.drain(), err
}

// Quiet and its cap are how long draining waits for the other end to
// stop sending after it was cancelled.
const (
	quiet    = 500 * time.Millisecond
	drainFor = 3 * time.Second
)

// drain reads what the other end still sends until it goes quiet, and
// returns what looks like the shell printing after it: the text after
// the last ZMODEM byte, when there is little of it and it is text.
func (s *session) drain() []byte {
	got := s.r.rest()
	s.r.buf, s.r.r = s.r.buf[:0], 0
	stop := time.Now().Add(drainFor)
	buf := make([]byte, 32<<10)
	for time.Now().Before(stop) {
		s.c.SetReadDeadline(time.Now().Add(quiet))
		n, err := s.c.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}
	// What ends the last frame: its CRC, a hex header's CR LF and XON.
	if i := bytes.LastIndexFunc(got, func(r rune) bool {
		return r == zdle || r == xon || r == xoff || r == 0 || r == utf8.RuneError
	}); i >= 0 {
		_, n := utf8.DecodeRune(got[i:])
		got = got[i+n:]
	}
	if len(got) > 4096 {
		return nil
	}
	return got
}

// Refuse declines an offer the user did not take: it cancels the other
// end and returns what the shell printed after.
func Refuse(ctx context.Context, c xfer.Conn) (rest []byte, err error) {
	s := newSession(ctx, c, Options{})
	if _, err := c.Write(cancelSeq); err != nil {
		return nil, err
	}
	return s.drain(), nil
}
