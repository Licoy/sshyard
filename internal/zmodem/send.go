package zmodem

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"path"

	"github.com/Licoy/sshyard/internal/xfer"
)

var errNoSeek = errors.New("zmodem: the receiver asked for part of a file again, which cannot be read again")

// Send gives the other end (rz) src's files; folders are left out, as
// ZMODEM carries none. It returns what the shell printed after.
func Send(ctx context.Context, c xfer.Conn, src xfer.Source, o Options) (rest []byte, err error) {
	s := newSession(ctx, c, o)
	return s.end(s.send(src))
}

// peer is what the receiver's ZRINIT says it can take.
type peer struct {
	flags byte
	// buflen, when not 0, is how much it takes before it must answer.
	buflen int
}

func (s *session) send(src xfer.Source) error {
	p, err := s.awaitInit()
	if err != nil {
		return err
	}
	c32 := p.flags&canfc32 != 0
	if p.flags&escctl != 0 {
		s.enc.ctl = true
	}
	for i, f := range src.Files() {
		if f.Dir {
			continue
		}
		rc, err := src.Open(i)
		if err != nil {
			return err
		}
		err = s.sendFile(p, c32, f, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return s.finish()
}

// awaitInit reads the receiver's ZRINIT. While the user chose files it
// may have sent several: the last that came counts.
func (s *session) awaitInit() (peer, error) {
	errs := 0
	for {
		if s.ctx.Err() != nil {
			return peer{}, errCanceled
		}
		h, err := s.r.header(s.timeout)
		if err != nil {
			if !recoverable(err) {
				return peer{}, err
			}
			if errs++; errs > maxRetries {
				return peer{}, errRetries
			}
			if err := s.sendHex(posHeader(zrqinit, 0)); err != nil {
				return peer{}, err
			}
			continue
		}
		switch h.typ {
		case zrinit:
			for s.pending() {
				h2, err := s.r.header(s.timeout)
				if err != nil || h2.typ != zrinit {
					break
				}
				h = h2
			}
			return peer{flags: h.f0(), buflen: int(h.b[0]) | int(h.b[1])<<8}, nil
		case zchallenge:
			if err := s.sendHex(header{typ: zack, b: h.b}); err != nil {
				return peer{}, err
			}
		case zcan, zabort, zferr, zfin:
			return peer{}, xfer.ErrRefused
		}
	}
}

// pending says the start of a header, or a cancel, can be read without
// waiting, dropping what else is there.
func (s *session) pending() bool {
	for s.r.ready() {
		if c := s.r.buf[s.r.r]; c == zpad || c == zdle {
			return true
		}
		s.r.r++
	}
	return false
}

// info is f's ZFILE subpacket.
func info(f xfer.File) []byte {
	mode := f.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	var mtime int64
	if !f.ModTime.IsZero() {
		mtime = f.ModTime.Unix()
	}
	b := []byte(path.Base(f.Name))
	b = append(b, 0)
	b = fmt.Appendf(b, "%d %o %o 0", max(f.Size, 0), mtime, 0o100000|uint32(mode))
	return append(b, 0)
}

func (s *session) sendFile(p peer, c32 bool, f xfer.File, rc io.Reader) error {
	errs := 0
	for {
		s.enc.binHeader(header{typ: zfile, b: [4]byte{0, 0, 0, zcbin}, crc32: c32})
		s.enc.subpacket(info(f), zcrcw, c32)
		if err := s.flush(); err != nil {
			return err
		}
	wait:
		for {
			if s.ctx.Err() != nil {
				return errCanceled
			}
			h, err := s.r.header(s.timeout)
			if err != nil {
				if !recoverable(err) {
					return err
				}
				if errs++; errs > maxRetries {
					return errRetries
				}
				break wait
			}
			switch h.typ {
			case zrinit:
				// A ZRINIT sent before ours came: ask again once nothing
				// else is on its way.
				if s.pending() {
					continue
				}
				if errs++; errs > maxRetries {
					return errRetries
				}
				break wait
			case znak:
				if errs++; errs > maxRetries {
					return errRetries
				}
				break wait
			case zrpos:
				return s.sendData(p, c32, f, rc, h.pos())
			case zskip:
				return nil
			case zcrc:
				crc, err := fileCRC(rc, h.pos())
				if err != nil {
					return err
				}
				if err := s.sendHex(posHeader(zcrc, int64(crc))); err != nil {
					return err
				}
			case zcan, zabort, zferr, zfin:
				return xfer.ErrRefused
			}
		}
	}
}

// fileCRC is the CRC-32 of the first n bytes of what rc reads, all of
// them for 0, which the receiver asks for to resume a file.
func fileCRC(rc io.Reader, n int64) (uint32, error) {
	sk, ok := rc.(io.Seeker)
	if !ok {
		return 0, errNoSeek
	}
	if _, err := sk.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	h := crc32.NewIEEE()
	var r io.Reader = rc
	if n > 0 {
		r = io.LimitReader(rc, n)
	}
	if _, err := io.Copy(h, r); err != nil {
		return 0, err
	}
	_, err := sk.Seek(0, io.SeekStart)
	return h.Sum32(), err
}

// window is how far the sender streams ahead of what the receiver
// acknowledged.
const window = 256 << 10

// tx is a file being sent.
type tx struct {
	s   *session
	p   peer
	c32 bool
	f   xfer.File
	rc  io.Reader
	// at is where rc is, acked how much the receiver said it has.
	at, acked, shown int64
	errs             int
	// redo, when not -1, is where the receiver asked to send from again.
	redo int64
	// eof says all of it went and the ZEOF waits for an answer, done
	// that the receiver has it all, skip that it wants no more of it.
	eof, done, skip bool
}

func (t *tx) seek(to int64) error {
	if to == t.at {
		return nil
	}
	sk, ok := t.rc.(io.Seeker)
	if !ok {
		return errNoSeek
	}
	if _, err := sk.Seek(to, io.SeekStart); err != nil {
		return err
	}
	t.at = to
	return nil
}

// back takes what the receiver said while the file goes.
func (t *tx) back(h header) error {
	switch h.typ {
	case zack:
		t.acked = max(t.acked, h.pos())
	case zrpos:
		if t.errs++; t.errs > maxRetries {
			return errRetries
		}
		t.redo = h.pos()
	case zrinit:
		t.done = t.eof
	case zskip:
		t.skip = true
	case zcan, zabort, zferr:
		return xfer.ErrRefused
	}
	return nil
}

// wait reads what the receiver says next. Hearing nothing sends again
// from what it acknowledged, or the ZEOF again.
func (t *tx) wait() error {
	h, err := t.s.r.header(t.s.timeout)
	if err == nil {
		return t.back(h)
	}
	if !recoverable(err) {
		return err
	}
	if t.errs++; t.errs > maxRetries {
		return errRetries
	}
	if !t.eof {
		t.redo = t.acked
	}
	return nil
}

func (t *tx) stopped() bool { return t.redo >= 0 || t.skip || t.done }

// frame sends a ZDATA frame from t.at, until the end of the file (eof),
// a ZCRCW, or the receiver asking for something else.
func (t *tx) frame(buf []byte) (eof bool, err error) {
	s := t.s
	s.enc.binHeader(header{typ: zdata, b: posHeader(zdata, t.at).b, crc32: t.c32})
	overlap := t.p.flags&canovio != 0
	since, count := 0, 0
	for {
		if s.ctx.Err() != nil {
			return false, errCanceled
		}
		n, rerr := io.ReadFull(t.rc, buf)
		last := rerr == io.EOF || rerr == io.ErrUnexpectedEOF
		if rerr != nil && !last {
			return false, rerr
		}
		t.at += int64(n)
		since += n
		count++
		var end byte
		switch {
		case last:
			end = zcrce
		case !overlap, t.p.buflen > 0 && since+len(buf) > t.p.buflen:
			end = zcrcw
		case count%16 == 0:
			end = zcrcq
		default:
			end = zcrcg
		}
		s.enc.subpacket(buf[:n], end, t.c32)
		if end != zcrcg || len(s.enc.out) >= 32<<10 {
			if err := s.flush(); err != nil {
				return false, err
			}
		}
		if t.at-t.shown >= 32<<10 {
			s.progress(t.f, t.at)
			t.shown = t.at
		}
		switch end {
		case zcrce:
			return true, nil
		case zcrcw:
			for t.acked < t.at && !t.stopped() {
				if err := t.wait(); err != nil {
					return false, err
				}
			}
			return false, nil
		}
		for s.pending() {
			h, err := s.r.header(s.timeout)
			if err == nil {
				err = t.back(h)
			}
			if err != nil && !recoverable(err) {
				return false, err
			}
		}
		for t.at-t.acked > window && !t.stopped() {
			if err := t.wait(); err != nil {
				return false, err
			}
		}
		if t.stopped() {
			return false, nil
		}
	}
}

// sendData sends f from pos until the receiver has all of it.
func (s *session) sendData(p peer, c32 bool, f xfer.File, rc io.Reader, pos int64) error {
	t := &tx{s: s, p: p, c32: c32, f: f, rc: rc, redo: -1}
	if err := t.seek(pos); err != nil {
		return err
	}
	t.acked, t.shown = t.at, t.at
	s.progress(f, t.at)
	buf := make([]byte, 1024)
	for {
		eof, err := t.frame(buf)
		if err != nil {
			return err
		}
		if eof {
			t.eof = true
			for !t.stopped() {
				s.enc.binHeader(header{typ: zeof, b: posHeader(zeof, t.at).b, crc32: c32})
				if err := s.flush(); err != nil {
					return err
				}
				for errs := t.errs; t.errs == errs && !t.stopped(); {
					if err := t.wait(); err != nil {
						return err
					}
				}
			}
			t.eof = false
		}
		if t.skip || t.done {
			s.progress(f, t.at)
			return nil
		}
		if t.redo >= 0 {
			if err := t.seek(t.redo); err != nil {
				return err
			}
			t.acked, t.redo = t.at, -1
		}
	}
}

// finish ends the session once the files went: ZFIN until the receiver
// answers with its own, then the "OO" it waits for.
func (s *session) finish() error {
	errs := 0
	for {
		if s.ctx.Err() != nil {
			return errCanceled
		}
		if err := s.sendHex(posHeader(zfin, 0)); err != nil {
			return err
		}
		for {
			h, err := s.r.header(s.timeout)
			if err != nil {
				if !recoverable(err) {
					return err
				}
				if errs++; errs > maxRetries {
					return errRetries
				}
				break
			}
			switch h.typ {
			case zfin:
				_, err := s.c.Write([]byte("OO"))
				return err
			case zcan, zabort, zferr:
				return xfer.ErrRefused
			}
		}
	}
}
