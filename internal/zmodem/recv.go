package zmodem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
)

var errCommand = errors.New("zmodem: the other end asked to run a command, which is never done")

// Receive takes the files the other end (sz) sends, into sink. It
// returns what the shell printed after the transfer: the caller shows it.
func Receive(ctx context.Context, c xfer.Conn, sink xfer.Sink, o Options) (rest []byte, err error) {
	s := newSession(ctx, c, o)
	var cur *incoming
	err = s.receive(sink, &cur)
	if cur != nil {
		cur.w.Abort()
	}
	return s.end(err)
}

// incoming is the file being received.
type incoming struct {
	f     xfer.File
	w     xfer.Writer
	off   int64
	shown int64
}

// recoverable is an error a retry gets past.
func recoverable(err error) bool {
	return errors.Is(err, errTimeout) || errors.Is(err, errBadHeader) || errors.Is(err, errBadCRC) ||
		errors.Is(err, errGarbage) || errors.Is(err, errLong)
}

func (s *session) zrinit() error {
	f := byte(canfdx | canovio | canfc32)
	if s.enc.ctl {
		f |= escctl
	}
	return s.sendHex(flagHeader(zrinit, f))
}

func (s *session) receive(sink xfer.Sink, cur **incoming) error {
	errs := 0
	// retry asks again for what is wanted: the data from where the file
	// got to, or a file.
	retry := func() error {
		if errs++; errs > maxRetries {
			return errRetries
		}
		if *cur != nil {
			return s.sendHex(posHeader(zrpos, (*cur).off))
		}
		return s.zrinit()
	}
	for {
		if s.ctx.Err() != nil {
			return errCanceled
		}
		h, err := s.r.header(s.timeout)
		if err != nil {
			if !recoverable(err) {
				return err
			}
			if err := retry(); err != nil {
				return err
			}
			continue
		}
		switch h.typ {
		case zrqinit:
			err = s.zrinit()
		case zsinit:
			if _, _, err := s.r.subpacket(s.timeout, 1024, h.crc32); err != nil {
				if !recoverable(err) {
					return err
				}
				if errs++; errs > maxRetries {
					return errRetries
				}
				err = s.sendHex(posHeader(znak, 0))
				break
			}
			if h.f0()&tescctl != 0 {
				s.enc.ctl, s.r.ctl = true, true
			}
			err = s.sendHex(posHeader(zack, 1))
		case zfile:
			data, _, perr := s.r.subpacket(s.timeout, 1024, h.crc32)
			if perr != nil {
				if !recoverable(perr) {
					return perr
				}
				if errs++; errs > maxRetries {
					return errRetries
				}
				err = s.sendHex(posHeader(znak, 0))
				break
			}
			f := parseInfo(data)
			if c := *cur; c != nil {
				// The same ZFILE again: the sender did not hear the ZRPOS.
				if c.f.Name == f.Name && c.off == 0 {
					err = s.sendHex(posHeader(zrpos, 0))
					break
				}
				c.w.Abort()
				*cur = nil
			}
			_, w, cerr := sink.Create(f)
			if errors.Is(cerr, xfer.ErrSkip) || cerr == nil && w == nil {
				err = s.sendHex(posHeader(zskip, 0))
				break
			}
			if cerr != nil {
				return cerr
			}
			*cur = &incoming{f: f, w: w}
			errs = 0
			s.progress(f, 0)
			err = s.sendHex(posHeader(zrpos, 0))
		case zdata:
			c := *cur
			if c == nil {
				continue // the rest of a file skipped
			}
			if h.pos() != c.off {
				err = retry()
				break
			}
			got, derr := s.data(c, h.crc32)
			if got {
				errs = 0
			}
			if derr != nil {
				if !recoverable(derr) {
					return derr
				}
				err = retry()
			}
		case zeof:
			c := *cur
			if c == nil {
				// The ZRINIT after the last file did not get there.
				err = s.zrinit()
				break
			}
			if h.pos() != c.off {
				continue // a ZEOF sent before data we asked for again
			}
			*cur = nil
			if err := c.w.Commit(); err != nil {
				return err
			}
			s.progress(c.f, c.off)
			errs = 0
			err = s.zrinit()
		case zfin:
			if err := s.sendHex(posHeader(zfin, 0)); err != nil {
				return err
			}
			s.overAndOut()
			return nil
		case zfreecnt:
			err = s.sendHex(posHeader(zack, 1<<32-1))
		case zchallenge:
			err = s.sendHex(header{typ: zack, b: h.b})
		case zcommand:
			return errCommand
		case zcan, zabort, zferr:
			return xfer.ErrRefused
		default:
			continue
		}
		if err != nil {
			return err
		}
	}
}

// maxData is the longest data subpacket taken: ZMODEM's are 1 KiB, its
// 8K variant's 8 KiB.
const maxData = 8 << 10

// data takes the data subpackets of a ZDATA frame for c. got says some
// of it was written.
func (s *session) data(c *incoming, c32 bool) (got bool, err error) {
	for {
		data, end, err := s.r.subpacket(s.timeout, maxData, c32)
		if err != nil {
			return got, err
		}
		if len(data) > 0 {
			if _, err := c.w.Write(data); err != nil {
				return got, fmt.Errorf("zmodem: writing %s: %w", c.f.Name, err)
			}
			c.off += int64(len(data))
			got = true
			if c.off-c.shown >= 32<<10 {
				s.progress(c.f, c.off)
				c.shown = c.off
			}
		}
		switch end {
		case zcrcw:
			return got, s.sendHex(posHeader(zack, c.off))
		case zcrcq:
			if err := s.sendHex(posHeader(zack, c.off)); err != nil {
				return got, err
			}
		case zcrce:
			return got, nil
		}
		if s.ctx.Err() != nil {
			return got, errCanceled
		}
	}
}

// overAndOut takes the "OO" the sender ends with, when it comes soon.
func (s *session) overAndOut() {
	deadline := time.Now().Add(time.Second)
	for range 2 {
		if s.r.r == len(s.r.buf) && s.r.fill(deadline) != nil {
			return
		}
		if s.r.buf[s.r.r] != 'O' {
			return
		}
		s.r.r++
	}
}

// parseInfo reads a ZFILE subpacket: the name, then the size, the time
// it changed and its mode, both in octal, and more not needed.
func parseInfo(data []byte) xfer.File {
	name, info, _ := bytes.Cut(data, []byte{0})
	if i := bytes.IndexByte(info, 0); i >= 0 {
		info = info[:i]
	}
	f := xfer.File{Name: string(name), Size: -1}
	fields := strings.Fields(string(info))
	if len(fields) > 0 {
		if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil && n >= 0 {
			f.Size = n
		}
	}
	if len(fields) > 1 {
		if t, err := strconv.ParseInt(fields[1], 8, 64); err == nil && t > 0 {
			f.ModTime = time.Unix(t, 0)
		}
	}
	if len(fields) > 2 {
		if m, err := strconv.ParseUint(fields[2], 8, 32); err == nil {
			f.Mode = fs.FileMode(m) & fs.ModePerm
		}
	}
	return f
}
