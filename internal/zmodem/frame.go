package zmodem

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
)

// The bytes that frame ZMODEM.
const (
	zpad   = '*'
	zdle   = 0x18 // also CAN: five in a row cancel
	zbin   = 'A'
	zhex   = 'B'
	zbin32 = 'C'
	xon    = 0x11
	xoff   = 0x13
	dle    = 0x10
	bs     = 0x08
)

// Frame types.
const (
	zrqinit = iota
	zrinit
	zsinit
	zack
	zfile
	zskip
	znak
	zabort
	zfin
	zrpos
	zdata
	zeof
	zferr
	zcrc
	zchallenge
	zcompl
	zcan
	zfreecnt
	zcommand
	zstderr
)

// What ends a data subpacket, after a ZDLE.
const (
	zcrce = 'h' // ends the frame, a header follows
	zcrcg = 'i' // the frame goes on, no answer wanted
	zcrcq = 'j' // the frame goes on, ZACK wanted
	zcrcw = 'k' // ends the frame, ZACK wanted
	zrub0 = 'l' // 0x7f
	zrub1 = 'm' // 0xff
)

// ZRINIT capabilities, in ZF0.
const (
	canfdx  = 0x01 // full duplex
	canovio = 0x02 // can take data while writing to disk
	canfc32 = 0x20 // takes CRC-32 frames
	escctl  = 0x40 // wants control characters escaped
)

// ZSINIT's ZF0: the sender wants control characters escaped.
const tescctl = 0x40

// zcbin is ZFILE's ZF0 for a binary file, sent as it is.
const zcbin = 1

var (
	errTimeout   = errors.New("zmodem: the other end stopped answering")
	errBadHeader = errors.New("zmodem: bad header")
	errBadCRC    = errors.New("zmodem: bad CRC")
	errGarbage   = errors.New("zmodem: no header in what the other end sent")
	errLong      = errors.New("zmodem: data subpacket too long")
	errRetries   = errors.New("zmodem: too many errors")
)

// header is a frame header: its type and four bytes, ZP0..ZP3 in order,
// which are ZF3..ZF0 too.
type header struct {
	typ byte
	b   [4]byte
	// crc32 says it came, or goes, as ZBIN32: the data subpackets after
	// it carry a CRC-32.
	crc32 bool
}

func posHeader(typ byte, pos int64) header {
	h := header{typ: typ}
	binary.LittleEndian.PutUint32(h.b[:], uint32(pos))
	return h
}

func flagHeader(typ, f0 byte) header {
	return header{typ: typ, b: [4]byte{0, 0, 0, f0}}
}

func (h header) pos() int64 { return int64(binary.LittleEndian.Uint32(h.b[:])) }
func (h header) f0() byte   { return h.b[3] }

// crc16 is ZMODEM's CRC-16 (XMODEM: polynomial 0x1021, from 0).
func crc16(crc uint16, p []byte) uint16 {
	for _, b := range p {
		crc = crcTable[byte(crc>>8)^b] ^ crc<<8
	}
	return crc
}

var crcTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

const hexDigits = "0123456789abcdef"

// hexHeader is h as a hex header, which goes as it is, never escaped.
func hexHeader(h header) []byte {
	raw := [5]byte{h.typ, h.b[0], h.b[1], h.b[2], h.b[3]}
	crc := crc16(0, raw[:])
	out := make([]byte, 0, 22)
	out = append(out, zpad, zpad, zdle, zhex)
	for _, b := range append(raw[:], byte(crc>>8), byte(crc)) {
		out = append(out, hexDigits[b>>4], hexDigits[b&15])
	}
	out = append(out, '\r', '\n'|0x80)
	// A receiver taking a ZACK or ZFIN reads nothing after it.
	if h.typ != zack && h.typ != zfin {
		out = append(out, xon)
	}
	return out
}

// encoder escapes what is sent in binary headers and data subpackets.
type encoder struct {
	out []byte
	// ctl escapes every control character.
	ctl bool
	// last is the byte last sent: a CR after '@' is escaped, lest
	// "@\r" be taken for a Telnet escape.
	last byte
}

func (e *encoder) byte(c byte) {
	esc := false
	switch c {
	case zdle, dle, xon, xoff, dle | 0x80, xon | 0x80, xoff | 0x80:
		esc = true
	case '\r', '\r' | 0x80:
		esc = e.ctl || e.last&0x7f == '@'
	default:
		esc = e.ctl && c&0x60 == 0
	}
	if esc {
		e.out = append(e.out, zdle, c^0x40)
	} else {
		e.out = append(e.out, c)
	}
	e.last = c
}

func (e *encoder) bytes(p []byte) {
	for _, c := range p {
		e.byte(c)
	}
}

// binHeader adds h as a binary header: ZBIN32 when h.crc32.
func (e *encoder) binHeader(h header) {
	raw := [5]byte{h.typ, h.b[0], h.b[1], h.b[2], h.b[3]}
	if h.crc32 {
		e.out = append(e.out, zpad, zdle, zbin32)
		e.bytes(raw[:])
		var c [4]byte
		binary.LittleEndian.PutUint32(c[:], crc32.ChecksumIEEE(raw[:]))
		e.bytes(c[:])
		return
	}
	e.out = append(e.out, zpad, zdle, zbin)
	e.bytes(raw[:])
	crc := crc16(0, raw[:])
	e.byte(byte(crc >> 8))
	e.byte(byte(crc))
}

// subpacket adds data as a data subpacket ending with end.
func (e *encoder) subpacket(data []byte, end byte, c32 bool) {
	e.bytes(data)
	e.out = append(e.out, zdle, end)
	if c32 {
		crc := crc32.Update(crc32.ChecksumIEEE(data), crc32.IEEETable, []byte{end})
		var c [4]byte
		binary.LittleEndian.PutUint32(c[:], crc)
		e.bytes(c[:])
	} else {
		crc := crc16(crc16(0, data), []byte{end})
		e.byte(byte(crc >> 8))
		e.byte(byte(crc))
	}
	if end == zcrcw {
		e.out = append(e.out, xon)
	}
}

// reader reads frames from the other end, buffered: what it read past
// the end of a transfer is what the shell printed after it.
type reader struct {
	c   xfer.Conn
	buf []byte
	r   int
	// ctl drops raw control characters, which only come escaped.
	ctl bool
	// done, when closed, ends a wait early.
	done <-chan struct{}
	data []byte
}

// slice is how long one read waits before looking at done again.
const slice = 200 * time.Millisecond

// fill reads more, waiting until deadline.
func (r *reader) fill(deadline time.Time) error {
	if r.r == len(r.buf) {
		r.buf, r.r = r.buf[:0], 0
	} else if r.r > 0 && len(r.buf) == cap(r.buf) {
		n := copy(r.buf, r.buf[r.r:])
		r.buf, r.r = r.buf[:n], 0
	}
	if cap(r.buf)-len(r.buf) < 4096 {
		nb := make([]byte, len(r.buf)-r.r, 2*cap(r.buf)+32<<10)
		copy(nb, r.buf[r.r:])
		r.buf, r.r = nb, 0
	}
	for {
		select {
		case <-r.done:
			return errCanceled
		default:
		}
		dl := deadline
		if until := time.Now().Add(slice); dl.IsZero() || until.Before(dl) {
			dl = until
		}
		r.c.SetReadDeadline(dl)
		n, err := r.c.Read(r.buf[len(r.buf):cap(r.buf)])
		r.buf = r.buf[:len(r.buf)+n]
		if n > 0 {
			return nil
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return errTimeout
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

// raw reads a byte as it came.
func (r *reader) raw(deadline time.Time) (byte, error) {
	if r.r == len(r.buf) {
		if err := r.fill(deadline); err != nil {
			return 0, err
		}
	}
	c := r.buf[r.r]
	r.r++
	return c, nil
}

// ready says a byte can be read without waiting.
func (r *reader) ready() bool {
	if r.r < len(r.buf) {
		return true
	}
	return r.fill(time.Now()) == nil && r.r < len(r.buf)
}

// rest is what was read and not taken.
func (r *reader) rest() []byte {
	if r.r == len(r.buf) {
		return nil
	}
	return append([]byte(nil), r.buf[r.r:]...)
}

// frameEnd marks, in what zdl returns, the end of a data subpacket.
const frameEnd = 0x100

// zdl reads a byte, unescaped: ZDLE x is x^0x40, a data subpacket's end
// comes as frameEnd|end, raw XON and XOFF are dropped.
func (r *reader) zdl(deadline time.Time) (int, error) {
	for {
		c, err := r.raw(deadline)
		if err != nil {
			return 0, err
		}
		switch {
		case c == zdle:
		case c&0x7f == xon || c&0x7f == xoff:
			continue
		case r.ctl && c&0x60 == 0:
			continue
		default:
			return int(c), nil
		}
		cans := 1
		for {
			c, err = r.raw(deadline)
			if err != nil {
				return 0, err
			}
			switch {
			case c == zdle:
				if cans++; cans >= 5 {
					return 0, xfer.ErrRefused
				}
				continue
			case c&0x7f == xon || c&0x7f == xoff:
				continue
			case c >= zcrce && c <= zcrcw:
				return frameEnd | int(c), nil
			case c == zrub0:
				return 0x7f, nil
			case c == zrub1:
				return 0xff, nil
			case c&0x60 == 0x40:
				return int(c ^ 0x40), nil
			}
			return 0, errBadHeader
		}
	}
}

// maxGarbage is how much a header is looked for in before giving up.
const maxGarbage = 1 << 20

// header reads the next header, skipping what comes before one.
func (r *reader) header(timeout time.Duration) (header, error) {
	deadline := time.Now().Add(timeout)
	garbage, cans := 0, 0
	for {
		c, err := r.raw(deadline)
		if err != nil {
			return header{}, err
		}
		switch c {
		case zdle:
			if cans++; cans >= 5 {
				return header{}, xfer.ErrRefused
			}
			continue
		case zpad:
		default:
			cans = 0
			if garbage++; garbage > maxGarbage {
				return header{}, errGarbage
			}
			continue
		}
		cans = 0
		for c == zpad {
			if c, err = r.raw(deadline); err != nil {
				return header{}, err
			}
		}
		if c != zdle {
			r.r-- // what follows the pads may start a header
			continue
		}
		f, err := r.raw(deadline)
		if err != nil {
			return header{}, err
		}
		var h header
		switch f {
		case zhex:
			h, err = r.hexHeader(deadline)
		case zbin:
			h, err = r.binHeader(deadline, false)
		case zbin32:
			h, err = r.binHeader(deadline, true)
		case zdle:
			cans = 2
			continue
		default:
			continue
		}
		return h, err
	}
}

func (r *reader) hexHeader(deadline time.Time) (header, error) {
	var raw [7]byte
	for i := range raw {
		var v byte
		for range 2 {
			c, err := r.raw(deadline)
			if err != nil {
				return header{}, err
			}
			d, ok := unhex(c &^ 0x80)
			if !ok {
				r.r--
				return header{}, errBadHeader
			}
			v = v<<4 | d
		}
		raw[i] = v
	}
	if crc16(0, raw[:5]) != uint16(raw[5])<<8|uint16(raw[6]) {
		return header{}, errBadCRC
	}
	// CR LF and the XON after: taken while they are there, so that what
	// is left after a last header is only what the shell printed.
	for range 3 {
		if !r.ready() {
			break
		}
		c := r.buf[r.r]
		if c&0x7f != '\r' && c&0x7f != '\n' && c&0x7f != xon {
			break
		}
		r.r++
	}
	return header{typ: raw[0], b: [4]byte{raw[1], raw[2], raw[3], raw[4]}}, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (r *reader) binHeader(deadline time.Time, c32 bool) (header, error) {
	n := 7
	if c32 {
		n = 9
	}
	var raw [9]byte
	for i := range n {
		c, err := r.zdl(deadline)
		if err != nil {
			return header{}, err
		}
		if c&frameEnd != 0 {
			return header{}, errBadHeader
		}
		raw[i] = byte(c)
	}
	if c32 {
		if crc32.ChecksumIEEE(raw[:5]) != binary.LittleEndian.Uint32(raw[5:9]) {
			return header{}, errBadCRC
		}
	} else if crc16(0, raw[:5]) != uint16(raw[5])<<8|uint16(raw[6]) {
		return header{}, errBadCRC
	}
	return header{typ: raw[0], b: [4]byte{raw[1], raw[2], raw[3], raw[4]}, crc32: c32}, nil
}

// subpacket reads a data subpacket of at most max bytes, with a CRC-32
// when c32. The data is good until the next call.
func (r *reader) subpacket(timeout time.Duration, max int, c32 bool) ([]byte, byte, error) {
	deadline := time.Now().Add(timeout)
	data := r.data[:0]
	var end byte
	for {
		c, err := r.zdl(deadline)
		if err != nil {
			return nil, 0, err
		}
		if c&frameEnd != 0 {
			end = byte(c)
			break
		}
		if len(data) >= max {
			return nil, 0, errLong
		}
		data = append(data, byte(c))
	}
	r.data = data
	n := 2
	if c32 {
		n = 4
	}
	var crc [4]byte
	for i := range n {
		c, err := r.zdl(deadline)
		if err != nil {
			return nil, 0, err
		}
		if c&frameEnd != 0 {
			return nil, 0, errBadCRC
		}
		crc[i] = byte(c)
	}
	if c32 {
		want := crc32.Update(crc32.ChecksumIEEE(data), crc32.IEEETable, []byte{end})
		if want != binary.LittleEndian.Uint32(crc[:]) {
			return nil, 0, errBadCRC
		}
	} else if crc16(crc16(0, data), []byte{end}) != uint16(crc[0])<<8|uint16(crc[1]) {
		return nil, 0, errBadCRC
	}
	return data, end, nil
}
