package trzsz

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
)

// version is the trzsz version this client says it is.
const version = "1.1.6"

var (
	errLineLong = errors.New("trzsz: line too long")
	errTimeout  = errors.New("trzsz: the server stopped answering")
	errStopped  = errors.New("Stopped")
)

// maxLine is the longest line taken: a chunk of the largest buffer a
// server may use, in base64.
const maxLine = 48 << 20

// config is what the server's #CFG says, of what this client uses.
type config struct {
	Directory  bool  `json:"directory"`
	Overwrite  bool  `json:"overwrite"`
	Timeout    int   `json:"timeout"`
	MaxBufSize int64 `json:"bufsize"`
	TmuxJunk   bool  `json:"tmux_output_junk"`
}

// sourceFile is a file's name as a folder transfer gives it.
type sourceFile struct {
	PathID  int      `json:"path_id"`
	RelPath []string `json:"path_name"`
	IsDir   bool     `json:"is_dir"`
	Archive bool     `json:"archive"`
	Size    int64    `json:"size"`
	Perm    *uint32  `json:"perm"`
}

type transfer struct {
	ctx  context.Context
	c    xfer.Conn
	o    *Offer
	opt  Options
	r    lineReader
	nl   string
	cfg  config
	junk bool
}

func newTransfer(ctx context.Context, c xfer.Conn, o *Offer, opt Options) *transfer {
	t := &transfer{ctx: ctx, c: c, o: o, opt: opt, nl: "\n"}
	t.r = lineReader{c: c, done: ctx.Done(), windows: o.Windows}
	if o.Windows {
		t.nl = "!\n"
	}
	t.cfg.Timeout = 20
	t.cfg.MaxBufSize = 10 << 20
	return t
}

// remoteError is a failure the server reported.
type remoteError struct{ msg string }

func (e *remoteError) Error() string { return "trzsz: " + e.msg }
func (e *remoteError) Unwrap() error { return xfer.ErrRefused }

// end finishes a transfer that ended with err. A failure of this end is
// told to the server, which then ends; the server's own needs nothing.
func (t *transfer) end(err error) ([]byte, error) {
	if err == nil {
		return t.r.rest(), nil
	}
	if t.ctx.Err() != nil {
		err = context.Cause(t.ctx)
		if err == nil {
			err = context.Canceled
		}
	}
	var re *remoteError
	if errors.As(err, &re) {
		return t.r.rest(), err
	}
	// What the server still sends, a chunk on its way, goes first.
	t.r.drain(500 * time.Millisecond)
	msg := err.Error()
	if t.ctx.Err() != nil {
		msg = errStopped.Error()
	}
	t.sendString("fail", msg)
	return nil, err
}

func (t *transfer) timeout() time.Time {
	if t.cfg.Timeout <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(t.cfg.Timeout) * time.Second)
}

func (t *transfer) sendLine(typ, payload string) error {
	_, err := t.c.Write([]byte("#" + typ + ":" + payload + t.nl))
	return err
}

func encode(p []byte) string {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	z.Write(p)
	z.Close()
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// decode undoes encode, taking at most max bytes.
func decode(s string, max int64) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("trzsz: %w", err)
	}
	z, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("trzsz: %w", err)
	}
	defer z.Close()
	out, err := io.ReadAll(io.LimitReader(z, max+1))
	if err != nil {
		return nil, fmt.Errorf("trzsz: %w", err)
	}
	if int64(len(out)) > max {
		return nil, errLineLong
	}
	return out, nil
}

func (t *transfer) sendString(typ, s string) error { return t.sendLine(typ, encode([]byte(s))) }
func (t *transfer) sendInteger(typ string, n int64) error {
	return t.sendLine(typ, strconv.FormatInt(n, 10))
}

// recv reads the next line, of type typ: another type is an error, the
// server's failure a remoteError.
func (t *transfer) recv(typ string, junk bool) (string, error) {
	line, err := t.r.line(t.timeout(), junk || t.junk)
	if err != nil {
		return "", err
	}
	if junk || t.junk || t.o.Windows {
		if i := bytes.LastIndex(line, []byte("#"+typ+":")); i >= 0 {
			line = line[i:]
		} else if i := bytes.LastIndexByte(line, '#'); i > 0 {
			line = line[i:]
		}
		line = stripTmuxStatus(line)
	}
	i := bytes.IndexByte(line, ':')
	if len(line) == 0 || line[0] != '#' || i < 1 {
		return "", fmt.Errorf("trzsz: unexpected %q", clip(line))
	}
	got, payload := string(line[1:i]), string(line[i+1:])
	if got == typ {
		return payload, nil
	}
	switch got {
	case "fail", "FAIL", "EXIT":
		msg, err := decode(payload, 1<<20)
		if err != nil {
			return "", &remoteError{payload}
		}
		return "", &remoteError{string(msg)}
	}
	return "", fmt.Errorf("trzsz: got %s where %s was due", clip([]byte(got)), typ)
}

func clip(p []byte) []byte {
	if len(p) > 64 {
		return p[:64]
	}
	return p
}

func (t *transfer) recvString(typ string, junk bool) (string, error) {
	s, err := t.recv(typ, junk)
	if err != nil {
		return "", err
	}
	b, err := decode(s, 1<<20)
	return string(b), err
}

func (t *transfer) recvInteger(typ string) (int64, error) {
	s, err := t.recv(typ, false)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("trzsz: %s %q", typ, clip([]byte(s)))
	}
	return n, nil
}

// check reads the SUCC the server answers with, which must be want.
func (t *transfer) check(want int64) error {
	n, err := t.recvInteger("SUCC")
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("trzsz: the server took %d, not %d", n, want)
	}
	return nil
}

func (t *transfer) recvBinary(typ string, max int64) ([]byte, error) {
	s, err := t.recv(typ, false)
	if err != nil {
		return nil, err
	}
	return decode(s, max)
}

// sendAction says whether the user took the transfer, and what this
// client can do: no binary mode, so the server sends base64; folders.
func (t *transfer) sendAction(confirm bool) error {
	act, _ := json.Marshal(map[string]any{
		"lang":        "go",
		"version":     version,
		"confirm":     confirm,
		"newline":     t.nl,
		"binary":      false,
		"support_dir": true,
	})
	return t.sendString("ACT", string(act))
}

func (t *transfer) recvConfig() error {
	s, err := t.recvString("CFG", true)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(s), &t.cfg); err != nil {
		return fmt.Errorf("trzsz: config: %w", err)
	}
	if t.cfg.MaxBufSize <= 0 || t.cfg.MaxBufSize > maxLine/2 {
		t.cfg.MaxBufSize = 10 << 20
	}
	t.junk = t.cfg.TmuxJunk
	return nil
}

func (t *transfer) progress(f xfer.File, done int64) {
	if t.opt.Progress != nil {
		t.opt.Progress(f, done)
	}
}

func (t *transfer) stopped() error {
	if t.ctx.Err() != nil {
		return errStopped
	}
	return nil
}

func (t *transfer) receive(sink xfer.Sink) error {
	if t.o.Mode != Download {
		return errors.New("trzsz: not a download")
	}
	if err := t.sendAction(true); err != nil {
		return err
	}
	if err := t.recvConfig(); err != nil {
		return err
	}
	num, err := t.recvInteger("NUM")
	if err != nil {
		return err
	}
	if err := t.sendInteger("SUCC", num); err != nil {
		return err
	}
	var tops []string
	for range num {
		if err := t.stopped(); err != nil {
			return err
		}
		name, err := t.recvString("NAME", false)
		if err != nil {
			return err
		}
		f := xfer.File{Name: name, Size: -1}
		if t.cfg.Directory {
			var sf sourceFile
			if err := json.Unmarshal([]byte(name), &sf); err != nil || len(sf.RelPath) == 0 {
				return fmt.Errorf("trzsz: bad name %q", clip([]byte(name)))
			}
			f = xfer.File{Name: strings.Join(sf.RelPath, "/"), Dir: sf.IsDir, Size: sf.Size}
			if sf.Perm != nil {
				f.Mode = os.FileMode(*sf.Perm).Perm()
			}
		}
		local, w, err := sink.Create(f)
		if errors.Is(err, xfer.ErrSkip) {
			err = errors.New("trzsz: a file cannot be skipped")
		}
		if err != nil {
			return err
		}
		// The server is told the local name of what it sent at the top.
		top, _, _ := strings.Cut(local, "/")
		if !contains(tops, top) {
			tops = append(tops, top)
		}
		if err := t.sendString("SUCC", top); err != nil {
			if w != nil {
				w.Abort()
			}
			return err
		}
		if w == nil {
			continue
		}
		if err := t.receiveData(f, w); err != nil {
			w.Abort()
			return err
		}
	}
	msg := savedText(len(tops), t.opt.Dir)
	if t.opt.Saved != nil {
		msg = t.opt.Saved(len(tops), t.opt.Dir)
	}
	return t.sendString("EXIT", msg)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func savedText(n int, dir string) string {
	s := "Saved " + strconv.Itoa(n) + " file/directory"
	if n > 1 {
		s = "Saved " + strconv.Itoa(n) + " files/directories"
	}
	if dir != "" {
		s += " to " + dir
	}
	return s
}

func (t *transfer) receiveData(f xfer.File, w xfer.Writer) error {
	size, err := t.recvInteger("SIZE")
	if err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("trzsz: size %d", size)
	}
	if err := t.sendInteger("SUCC", size); err != nil {
		return err
	}
	f.Size = size
	t.progress(f, 0)
	h := md5.New()
	var step int64
	for step < size {
		if err := t.stopped(); err != nil {
			return err
		}
		data, err := t.recvBinary("DATA", min(size-step, t.cfg.MaxBufSize))
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return errors.New("trzsz: empty chunk")
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		h.Write(data)
		step += int64(len(data))
		t.progress(f, step)
		if err := t.sendInteger("SUCC", int64(len(data))); err != nil {
			return err
		}
	}
	want, err := t.recvBinary("MD5", 64)
	if err != nil {
		return err
	}
	sum := h.Sum(nil)
	if !bytes.Equal(sum, want) {
		return errors.New("Check MD5 failed")
	}
	if err := t.sendLine("SUCC", encode(sum)); err != nil {
		return err
	}
	return w.Commit()
}

func (t *transfer) send(src xfer.Source) error {
	if t.o.Mode == Download {
		return errors.New("trzsz: not an upload")
	}
	files := src.Files()
	if err := t.sendAction(true); err != nil {
		return err
	}
	if err := t.recvConfig(); err != nil {
		return err
	}
	ids := map[string]int{}
	for _, f := range files {
		top, _, _ := strings.Cut(f.Name, "/")
		if !t.cfg.Directory && (f.Dir || top != f.Name) {
			return errors.New("trzsz: the server takes no folders: run trz -d")
		}
		if _, ok := ids[top]; ok && t.cfg.Overwrite && top == f.Name {
			return fmt.Errorf("trzsz: %s twice", top)
		}
		if _, ok := ids[top]; !ok {
			ids[top] = len(ids)
		}
	}
	if err := t.sendInteger("NUM", int64(len(files))); err != nil {
		return err
	}
	if err := t.check(int64(len(files))); err != nil {
		return err
	}
	for i, f := range files {
		if err := t.stopped(); err != nil {
			return err
		}
		name := path.Base(f.Name)
		if t.cfg.Directory {
			top, _, _ := strings.Cut(f.Name, "/")
			sf := sourceFile{PathID: ids[top], RelPath: strings.Split(f.Name, "/"), IsDir: f.Dir, Size: max(f.Size, 0)}
			if p := uint32(f.Mode.Perm()); p != 0 {
				sf.Perm = &p
			}
			b, _ := json.Marshal(sf)
			name = string(b)
		}
		if err := t.sendString("NAME", name); err != nil {
			return err
		}
		if _, err := t.recvString("SUCC", false); err != nil {
			return err
		}
		if f.Dir {
			continue
		}
		rc, err := src.Open(i)
		if err != nil {
			return err
		}
		err = t.sendData(f, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return t.sendString("EXIT", savedText(len(ids), ""))
}

func (t *transfer) sendData(f xfer.File, r io.Reader) error {
	size := max(f.Size, 0)
	if err := t.sendInteger("SIZE", size); err != nil {
		return err
	}
	if err := t.check(size); err != nil {
		return err
	}
	t.progress(f, 0)
	h := md5.New()
	chunk := int64(1024)
	buf := make([]byte, chunk)
	var step int64
	for step < size {
		if err := t.stopped(); err != nil {
			return err
		}
		begin := time.Now()
		n, err := io.ReadFull(r, buf[:min(chunk, size-step)])
		if err != nil {
			return fmt.Errorf("trzsz: reading %s: %w", f.Name, err)
		}
		data := buf[:n]
		if err := t.sendLine("DATA", encode(data)); err != nil {
			return err
		}
		h.Write(data)
		if err := t.check(int64(n)); err != nil {
			return err
		}
		step += int64(n)
		t.progress(f, step)
		// Chunks grow while they go fast, as the reference client's do.
		switch took := time.Since(begin); {
		case int64(n) == chunk && took < 500*time.Millisecond && chunk < t.cfg.MaxBufSize:
			chunk = min(chunk*2, t.cfg.MaxBufSize)
			buf = make([]byte, chunk)
		case took >= 2*time.Second && chunk > 1024:
			chunk = 1024
		}
	}
	sum := h.Sum(nil)
	if err := t.sendLine("MD5", encode(sum)); err != nil {
		return err
	}
	got, err := t.recvBinary("SUCC", 64)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, sum) {
		return errors.New("Check MD5 failed")
	}
	return nil
}

// stripTmuxStatus drops the status line tmux draws into what it shows.
func stripTmuxStatus(p []byte) []byte {
	for {
		begin := bytes.Index(p, []byte("\x1bP="))
		if begin < 0 {
			return p
		}
		at := begin + 3
		mid := bytes.Index(p[at:], []byte("\x1bP="))
		if mid < 0 {
			return p[:begin]
		}
		at += mid + 3
		end := bytes.Index(p[at:], []byte("\x1b\\"))
		if end < 0 {
			return p[:begin]
		}
		at += end + 2
		p = append(append([]byte(nil), p[:begin]...), p[at:]...)
	}
}

// lineReader reads the server's lines, buffered.
type lineReader struct {
	c       xfer.Conn
	done    <-chan struct{}
	windows bool
	buf     []byte
	r       int
	held    []byte
}

const slice = 200 * time.Millisecond

func (l *lineReader) fill(deadline time.Time) error {
	if l.r == len(l.buf) {
		l.buf, l.r = l.buf[:0], 0
	}
	if cap(l.buf)-len(l.buf) < 4096 {
		nb := make([]byte, len(l.buf)-l.r, 2*(len(l.buf)-l.r)+64<<10)
		copy(nb, l.buf[l.r:])
		l.buf, l.r = nb, 0
	}
	for {
		select {
		case <-l.done:
			return errStopped
		default:
		}
		dl := deadline
		if until := time.Now().Add(slice); dl.IsZero() || until.Before(dl) {
			dl = until
		}
		l.c.SetReadDeadline(dl)
		n, err := l.c.Read(l.buf[len(l.buf):cap(l.buf)])
		l.buf = l.buf[:len(l.buf)+n]
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

// line reads a line, without its newline. junk drops lines that end in
// CR, which are the terminal's, not the server's.
func (l *lineReader) line(deadline time.Time, junk bool) ([]byte, error) {
	if l.windows {
		return l.windowsLine(deadline)
	}
	out := l.held[:0]
	for {
		if l.r == len(l.buf) {
			if err := l.fill(deadline); err != nil {
				return nil, err
			}
		}
		p := l.buf[l.r:]
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			out = append(out, p...)
			l.r = len(l.buf)
		} else {
			out = append(out, p[:i]...)
			l.r += i + 1
		}
		if len(out) > maxLine {
			return nil, errLineLong
		}
		if i < 0 {
			continue
		}
		if junk && len(out) > 0 && out[len(out)-1] == '\r' {
			out = out[:0]
			continue
		}
		l.held = out
		return out, nil
	}
}

func isVT100End(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("#:+/=", c) >= 0
}

// windowsLine reads a line from a Windows server, which ends its lines
// with "!" as its console redraws them: what is no part of the protocol
// is dropped, escape sequences, and the characters a cursor move shows
// twice.
func (l *lineReader) windowsLine(deadline time.Time) ([]byte, error) {
	out := l.held[:0]
	last := byte(0x1b)
	skip, newline, dup, home, preHome := false, false, false, false, false
	for {
		if l.r == len(l.buf) {
			if err := l.fill(deadline); err != nil {
				return nil, err
			}
		}
		p := l.buf[l.r:]
		i := bytes.IndexByte(p, '!')
		if i >= 0 {
			l.r += i + 1
			if l.r < len(l.buf) && l.buf[l.r] == '\n' {
				l.r++
			}
			p = p[:i]
		} else {
			l.r = len(l.buf)
		}
		for _, c := range p {
			if c == '\n' {
				newline = true
			}
			switch {
			case skip:
				if isVT100End(c) {
					skip = false
					if c == 'H' && last >= '0' && last <= '9' {
						dup = true
					}
				}
				if last == '[' && c == 'H' {
					home = true
				}
				last = c
			case c == 0x1b:
				skip, last = true, c
			case isLetter(c):
				if dup {
					dup = false
					if newline && len(out) > 0 && (c == out[len(out)-1] || preHome) {
						out[len(out)-1] = c
						continue
					}
				}
				out = append(out, c)
				preHome, home, newline = home, false, false
			}
		}
		if len(out) > maxLine {
			return nil, errLineLong
		}
		if i >= 0 && len(out) > 0 && !skip {
			l.held = out
			return out, nil
		}
	}
}

// rest is what was read and not taken.
func (l *lineReader) rest() []byte {
	if l.r == len(l.buf) {
		return nil
	}
	return append([]byte(nil), l.buf[l.r:]...)
}

// drain drops what comes until it is quiet for d.
func (l *lineReader) drain(d time.Duration) {
	l.buf, l.r = l.buf[:0], 0
	buf := make([]byte, 32<<10)
	stop := time.Now().Add(10 * time.Second)
	for time.Now().Before(stop) {
		l.c.SetReadDeadline(time.Now().Add(d))
		if _, err := l.c.Read(buf); err != nil {
			return
		}
	}
}
