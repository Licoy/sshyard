// Package scpfs gives SFTP to a server that has none. A server without
// the sftp subsystem (a router, a NAS, a small system running dropbear)
// still runs commands: an SFTP server in this process (pkg/sftp's
// RequestServer) answers the client, with POSIX shell commands on exec
// channels for listings and metadata, and scp's protocol for the
// contents of files (cat where the server has no scp).
package scpfs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// The exit statuses the scripts end with, for the errors they stand for.
const (
	exitNotExist   = 90
	exitPermission = 91
	exitExist      = 92
	exitFailure    = 93
)

// ErrNoShell reports a server that runs no commands either.
var ErrNoShell = errors.New("scp: the server runs no commands")

// remote is a server, as probed once per connection.
type remote struct {
	conn *ssh.Client
	// home is where the login starts.
	home string
	// gnu: stat takes -c formats (GNU, busybox); else BSD's -f.
	gnu bool
	// ls: the server has no stat (OpenWrt's busybox): ls and date stand
	// in.
	ls bool
	// scp: the server has scp to move contents with; else cat.
	scp bool
	// xargs: the server has xargs, to stat a folder's entries at once.
	xargs bool
}

type probe struct {
	once sync.Once
	r    *remote
	err  error
}

var remotes = struct {
	sync.Mutex
	m map[*ssh.Client]*probe
}{m: map[*ssh.Client]*probe{}}

// noSCP, for tests, acts as if the server had no scp.
var noSCP bool

// noStat, for tests, acts as if the server had no stat.
var noStat bool

// remoteOf probes conn's server once; a failed probe is tried again.
func remoteOf(conn *ssh.Client) (*remote, error) {
	remotes.Lock()
	p := remotes.m[conn]
	if p == nil {
		p = &probe{}
		remotes.m[conn] = p
		go func() {
			conn.Wait()
			remotes.Lock()
			delete(remotes.m, conn)
			remotes.Unlock()
		}()
	}
	remotes.Unlock()
	p.once.Do(func() { p.r, p.err = probeRemote(conn) })
	if p.err != nil {
		remotes.Lock()
		if remotes.m[conn] == p {
			delete(remotes.m, conn)
		}
		remotes.Unlock()
	}
	return p.r, p.err
}

func probeRemote(conn *ssh.Client) (*remote, error) {
	r := &remote{conn: conn}
	out, err := r.output(`pwd
if stat -c %s / >/dev/null 2>&1; then echo gnu; elif stat -f %z / >/dev/null 2>&1; then echo bsd; else echo ls; fi
if command -v scp >/dev/null 2>&1; then echo scp; else echo cat; fi
if command -v xargs >/dev/null 2>&1; then echo xargs; else echo loop; fi`)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoShell, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 4 || !path.IsAbs(lines[len(lines)-4]) {
		return nil, fmt.Errorf("%w: %q", ErrNoShell, out)
	}
	n := len(lines)
	r.home = lines[n-4]
	r.gnu = lines[n-3] == "gnu" && !noStat
	r.ls = lines[n-3] == "ls" || noStat
	r.scp = lines[n-2] == "scp" && !noSCP
	r.xargs = lines[n-1] == "xargs"
	return r, nil
}

// NewClient is an SFTP client of conn's server served by commands; closing
// it ends the server in this process, not the connection.
func NewClient(conn *ssh.Client) (*sftp.Client, error) {
	r, err := remoteOf(conn)
	if err != nil {
		return nil, err
	}
	cr, sw := io.Pipe() // the server writes, the client reads
	sr, cw := io.Pipe() // the client writes, the server reads
	h := &handlers{r: r}
	srv := sftp.NewRequestServer(pipeConn{sr, sw}, sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h}, sftp.WithStartDirectory(r.home))
	go func() {
		srv.Serve()
		srv.Close()
		cr.Close()
	}()
	c, err := sftp.NewClientPipe(cr, cw)
	if err != nil {
		cw.Close()
		return nil, err
	}
	return c, nil
}

// pipeConn is the server's side of the two pipes.
type pipeConn struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeConn) Close() error {
	p.r.Close()
	return p.w.Close()
}

// quote is s as one word of a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// run runs script on the server with stdin, its output to stdout.
func (r *remote) run(script string, stdin io.Reader, stdout io.Writer) error {
	s, err := r.conn.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	var stderr limited
	s.Stdin, s.Stdout, s.Stderr = stdin, stdout, &stderr
	return scriptError(s.Run(script), stderr.String())
}

func (r *remote) output(script string) ([]byte, error) {
	var out bytes.Buffer
	err := r.run(script, nil, &out)
	return out.Bytes(), err
}

// limited keeps the first 4 KiB written to it: a command's errors.
type limited struct{ bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := 4096 - l.Len(); room > 0 {
		l.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// scriptError is the error a script's exit and its errors stand for.
func scriptError(err error, stderr string) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(stderr)
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitStatus() {
		case exitNotExist:
			return fs.ErrNotExist
		case exitPermission:
			return permissionError(msg)
		case exitExist:
			return fs.ErrExist
		}
	}
	if msg == "" {
		return err
	}
	return messageError(msg)
}

// messageError is the error a command's message tells of.
func messageError(msg string) error {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "no such file"), strings.Contains(l, "nonexistent"):
		// A PathError: pkg/sftp's server sees through no other wrapping.
		return &fs.PathError{Op: "scp", Path: msg, Err: fs.ErrNotExist}
	case strings.Contains(l, "permission denied"), strings.Contains(l, "not permitted"), strings.Contains(l, "read-only file system"):
		return permissionError(msg)
	case strings.Contains(l, "file exists"):
		return fmt.Errorf("%w: %s", fs.ErrExist, msg)
	}
	return errors.New(msg)
}

func permissionError(msg string) error {
	if msg == "" {
		return sftp.ErrSSHFxPermissionDenied
	}
	return fmt.Errorf("%w: %s", sftp.ErrSSHFxPermissionDenied, msg)
}

// statCmd is the command that prints "mode size mtime uid gid name" of
// its arguments, mode in hex: lstat's, or stat's when follow.
func (r *remote) statCmd(follow bool) string {
	l := ""
	if follow {
		l = "-L "
	}
	if r.gnu {
		return "stat " + l + "-c '%f %s %Y %u %g %n'"
	}
	return "stat " + l + "-f '%Xp %z %m %u %g %N'"
}

// lsStat is the script that prints the line statCmd would of the entry
// name (a shell word), from ls and date, on a server without stat: a
// device's size is 0, and so is an mtime date cannot tell.
func lsStat(name string, follow bool) string {
	l := "-lnd"
	if follow {
		l = "-lndL"
	}
	return "ls " + l + " -- " + name + ` 2>/dev/null | { read m n u g s x || exit 0; case $s in ''|*[!0-9]*) s=0;; esac
t=$(date -r ` + name + ` +%s 2>/dev/null); case $t in ''|*[!0-9]*) t=0;; esac
printf '%s %s %s %s %s %s\n' "$m" "$s" "$t" "$u" "$g" ` + name + `; }`
}

// fileInfo is an entry as stat printed it.
type fileInfo struct {
	name     string
	size     int64
	mode     fs.FileMode
	mtime    time.Time
	uid, gid uint32
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() fs.FileMode  { return f.mode }
func (f fileInfo) ModTime() time.Time { return f.mtime }
func (f fileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fileInfo) Sys() any           { return nil }
func (f fileInfo) Uid() uint32        { return f.uid }
func (f fileInfo) Gid() uint32        { return f.gid }

var _ sftp.FileInfoUidGid = fileInfo{}

// parseStat reads a line of statCmd.
func parseStat(line string) (fileInfo, bool) {
	f := strings.SplitN(line, " ", 6)
	if len(f) != 6 {
		return fileInfo{}, false
	}
	mode, err1 := strconv.ParseUint(f[0], 16, 32)
	if err1 != nil {
		if m, ok := lsMode(f[0]); ok {
			mode, err1 = uint64(m), nil
		}
	}
	size, err2 := strconv.ParseInt(f[1], 10, 64)
	mtime, err3 := strconv.ParseInt(f[2], 10, 64)
	uid, err4 := strconv.ParseUint(f[3], 10, 32)
	gid, err5 := strconv.ParseUint(f[4], 10, 32)
	if err := errors.Join(err1, err2, err3, err4, err5); err != nil {
		return fileInfo{}, false
	}
	return fileInfo{name: f[5], size: size, mode: unixMode(uint32(mode)), mtime: time.Unix(mtime, 0), uid: uint32(uid), gid: uint32(gid)}, true
}

// lsMode is st_mode of a mode as ls prints it (drwxr-sr-t), with or
// without the mark of an ACL after it.
func lsMode(s string) (uint32, bool) {
	if len(s) < 10 {
		return 0, false
	}
	types := map[byte]uint32{'-': 0o100000, 'd': 0o040000, 'l': 0o120000, 'b': 0o060000, 'c': 0o020000, 'p': 0o010000, 's': 0o140000}
	m, ok := types[s[0]]
	if !ok {
		return 0, false
	}
	for i, special := range []uint32{0o4000, 0o2000, 0o1000} {
		r, w, x := s[1+3*i], s[2+3*i], s[3+3*i]
		shift := uint(3 * (2 - i))
		switch r {
		case 'r':
			m |= 4 << shift
		case '-':
		default:
			return 0, false
		}
		switch w {
		case 'w':
			m |= 2 << shift
		case '-':
		default:
			return 0, false
		}
		switch x {
		case 'x':
			m |= 1 << shift
		case 's', 't':
			m |= 1<<shift | special
		case 'S', 'T':
			m |= special
		case '-':
		default:
			return 0, false
		}
	}
	return m, true
}

// unixMode is st_mode as a FileMode.
func unixMode(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	switch m & 0o170000 {
	case 0o040000:
		mode |= fs.ModeDir
	case 0o120000:
		mode |= fs.ModeSymlink
	case 0o060000:
		mode |= fs.ModeDevice
	case 0o020000:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case 0o010000:
		mode |= fs.ModeNamedPipe
	case 0o140000:
		mode |= fs.ModeSocket
	}
	if m&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if m&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if m&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

// parseListing reads statCmd's lines of a folder's entries. A name with a
// line break in it goes on over the next lines.
func parseListing(out []byte) []os.FileInfo {
	var infos []fileInfo
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if fi, ok := parseStat(line); ok {
			infos = append(infos, fi)
		} else if n := len(infos); n > 0 {
			infos[n-1].name += "\n" + line
		}
	}
	list := make([]os.FileInfo, 0, len(infos))
	for _, fi := range infos {
		if fi.name != "." && fi.name != ".." && fi.name != "" {
			list = append(list, fi)
		}
	}
	return list
}

// list lists dir's entries, links as links.
func (r *remote) list(dir string) ([]os.FileInfo, error) {
	q := quote(dir)
	names := `for f in .[!.]* ..?* *; do if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\0' "$f"; fi; done`
	each := names + " | xargs -0 -r " + r.statCmd(false) + " -- 2>/dev/null"
	if r.ls {
		each = `for f in .[!.]* ..?* *; do if [ -e "$f" ] || [ -L "$f" ]; then ` + lsStat(`"$f"`, false) + `; fi; done`
	} else if !r.xargs {
		each = `for f in .[!.]* ..?* *; do if [ -e "$f" ] || [ -L "$f" ]; then ` + r.statCmd(false) + ` -- "$f" 2>/dev/null; fi; done`
	}
	out, err := r.output("cd -- " + q + " 2>/dev/null || { [ -e " + q + " ] || exit 90; [ -d " + q + " ] || exit 93; exit 91; }\n" + each + "\nexit 0")
	if err != nil {
		return nil, err
	}
	return parseListing(out), nil
}

// stat is p's entry: lstat's, or stat's when follow.
func (r *remote) stat(p string, follow bool) (fileInfo, error) {
	q := quote(p)
	exists := "[ -e " + q + " ] || [ -L " + q + " ]"
	if follow {
		exists = "[ -e " + q + " ]"
	}
	cmd := r.statCmd(follow) + " -- " + q
	if r.ls {
		cmd = lsStat(q, follow)
	}
	out, err := r.output(exists + " || exit 90\n" + cmd)
	if err != nil {
		return fileInfo{}, err
	}
	fi, ok := parseStat(strings.SplitN(string(out), "\n", 2)[0])
	if !ok {
		return fileInfo{}, fmt.Errorf("scp: stat %s: %q", p, out)
	}
	fi.name = path.Base(p)
	return fi, nil
}

// handlers answer the SFTP client.
type handlers struct{ r *remote }

// listerAt lists infos.
type listerAt []os.FileInfo

func (l listerAt) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}

func (h *handlers) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	switch req.Method {
	case "List":
		l, err := h.r.list(req.Filepath)
		return listerAt(l), err
	case "Stat":
		fi, err := h.r.stat(req.Filepath, true)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

func (h *handlers) Lstat(req *sftp.Request) (sftp.ListerAt, error) {
	fi, err := h.r.stat(req.Filepath, false)
	if err != nil {
		return nil, err
	}
	return listerAt{fi}, nil
}

// RealPath is p from home, with the links of a folder resolved.
func (h *handlers) RealPath(p string) (string, error) {
	if !path.IsAbs(p) {
		p = path.Join(h.r.home, p)
	}
	p = path.Clean(p)
	if p == h.r.home {
		return p, nil
	}
	out, err := h.r.output("cd -- " + quote(p) + " 2>/dev/null && pwd -P")
	if err != nil {
		return p, nil
	}
	if r := strings.TrimSuffix(string(out), "\n"); path.IsAbs(r) {
		return r, nil
	}
	return p, nil
}

func (h *handlers) Readlink(p string) (string, error) {
	q := quote(p)
	out, err := h.r.output("[ -L " + q + " ] || exit 90\nreadlink -- " + q)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

func (h *handlers) Filecmd(req *sftp.Request) error {
	q := quote(req.Filepath)
	exists := "{ [ -e " + q + " ] || [ -L " + q + " ]; } || exit 90\n"
	switch req.Method {
	case "Setstat":
		return h.setstat(req)
	case "Rename":
		t := quote(req.Target)
		return h.r.run(exists+"if [ -e "+t+" ] || [ -L "+t+" ]; then exit 92; fi\nmv -- "+q+" "+t, nil, io.Discard)
	case "PosixRename":
		return h.PosixRename(req)
	case "Rmdir":
		return h.r.run(exists+"rmdir -- "+q, nil, io.Discard)
	case "Mkdir":
		return h.r.run("if [ -e "+q+" ] || [ -L "+q+" ]; then exit 92; fi\nmkdir -- "+q, nil, io.Discard)
	case "Remove":
		return h.r.run(exists+"if [ -d "+q+" ] && [ ! -L "+q+" ]; then exit 93; fi\nrm -f -- "+q, nil, io.Discard)
	case "Symlink":
		// Filepath is what the link points to, Target the link.
		return h.r.run("ln -s -- "+q+" "+quote(req.Target), nil, io.Discard)
	case "Link":
		return h.r.run(exists+"ln -- "+q+" "+quote(req.Target), nil, io.Discard)
	}
	return sftp.ErrSSHFxOpUnsupported
}

func (h *handlers) PosixRename(req *sftp.Request) error {
	q := quote(req.Filepath)
	return h.r.run("{ [ -e "+q+" ] || [ -L "+q+" ]; } || exit 90\nmv -f -- "+q+" "+quote(req.Target), nil, io.Discard)
}

// touchTime is t as touch -t takes it, in UTC.
func touchTime(t uint32) string { return time.Unix(int64(t), 0).UTC().Format("200601021504.05") }

func (h *handlers) setstat(req *sftp.Request) error {
	q := quote(req.Filepath)
	fl, at := req.AttrFlags(), req.Attributes()
	var cmds []string
	if fl.Size {
		if at.Size == 0 {
			cmds = append(cmds, ": > "+q)
		} else {
			cmds = append(cmds, "truncate -s "+strconv.FormatUint(at.Size, 10)+" -- "+q)
		}
	}
	if fl.UidGid {
		cmds = append(cmds, fmt.Sprintf("chown -- %d:%d %s", at.UID, at.GID, q))
	}
	if fl.Permissions {
		cmds = append(cmds, fmt.Sprintf("chmod -- %o %s", at.Mode&0o7777, q))
	}
	if fl.Acmodtime {
		cmds = append(cmds, "TZ=UTC0 touch -a -t "+touchTime(at.Atime)+" -- "+q, "TZ=UTC0 touch -m -t "+touchTime(at.Mtime)+" -- "+q)
	}
	if len(cmds) == 0 {
		return nil
	}
	return h.r.run("{ [ -e "+q+" ] || [ -L "+q+" ]; } || exit 90\n"+strings.Join(cmds, " &&\n"), nil, io.Discard)
}

func (h *handlers) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	fi, err := h.r.stat(req.Filepath, true)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("scp: %s is a folder", req.Filepath)
	}
	return &reader{r: h.r, path: req.Filepath}, nil
}

func (h *handlers) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	return h.open(req)
}

func (h *handlers) OpenFile(req *sftp.Request) (sftp.WriterAtReaderAt, error) {
	return h.open(req)
}

// open makes, empties or checks the file a write opens, as its flags ask,
// and learns whether what it holds must be kept.
func (h *handlers) open(req *sftp.Request) (*writer, error) {
	fl := req.Pflags()
	q := quote(req.Filepath)
	var b strings.Builder
	b.WriteString("if [ -d " + q + " ]; then exit 93; fi\n")
	if fl.Excl {
		b.WriteString("if [ -e " + q + " ] || [ -L " + q + " ]; then exit 92; fi\n")
	}
	switch {
	case fl.Trunc:
		b.WriteString(": > " + q + " || exit $?\n")
	case fl.Creat:
		b.WriteString("[ -e " + q + " ] || : > " + q + " || exit $?\n")
	default:
		b.WriteString("[ -e " + q + " ] || exit 90\n")
	}
	b.WriteString("wc -c < " + q)
	out, err := h.r.output(b.String())
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("scp: size of %s: %q", req.Filepath, out)
	}
	return &writer{r: h.r, path: req.Filepath, base: size > 0}, nil
}

// reader reads a file through one stream from its start: scp's source
// side (scp -f), or cat. A read behind the stream starts it again; one
// ahead skips what is between.
type reader struct {
	r    *remote
	path string

	mu   sync.Mutex
	s    *ssh.Session
	in   io.WriteCloser
	out  *bufio.Reader
	errs *limited
	pos  int64
	// size is what scp said the file holds; -1 reading with cat.
	size int64
	// waited is set once cat's end was waited for, with why it failed.
	waited  bool
	waitErr error
}

func (rd *reader) ReadAt(p []byte, off int64) (int, error) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if rd.s == nil || off < rd.pos {
		if err := rd.start(); err != nil {
			return 0, err
		}
	}
	if off > rd.pos {
		n, err := io.CopyN(io.Discard, rd.data(), off-rd.pos)
		rd.pos += n
		if err != nil {
			return 0, rd.end(err)
		}
	}
	n, err := io.ReadFull(rd.data(), p)
	rd.pos += int64(n)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	if err != nil {
		err = rd.end(err)
	}
	return n, err
}

// data is what is left of the file in the stream.
func (rd *reader) data() io.Reader {
	if rd.size < 0 {
		return rd.out
	}
	return io.LimitReader(rd.out, rd.size-rd.pos)
}

// end is err, or at the end of a cat, why cat stopped if it failed.
func (rd *reader) end(err error) error {
	if err != io.EOF || rd.size >= 0 {
		return err
	}
	if !rd.waited {
		rd.waited, rd.waitErr = true, scriptError(rd.s.Wait(), rd.errs.String())
	}
	if rd.waitErr != nil {
		return rd.waitErr
	}
	return io.EOF
}

func (rd *reader) start() error {
	rd.stop()
	s, err := rd.r.conn.NewSession()
	if err != nil {
		return err
	}
	in, err1 := s.StdinPipe()
	out, err2 := s.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		s.Close()
		return err
	}
	errs := &limited{}
	s.Stderr = errs
	cmd := "cat -- " + quote(rd.path)
	if rd.r.scp {
		cmd = "scp -f -- " + quote(rd.path)
	}
	if err := s.Start(cmd); err != nil {
		s.Close()
		return err
	}
	rd.s, rd.in, rd.out, rd.errs, rd.pos, rd.size = s, in, bufio.NewReaderSize(out, 64<<10), errs, 0, -1
	rd.waited, rd.waitErr = false, nil
	if !rd.r.scp {
		in.Close()
		return nil
	}
	// Source: we ask, it names the file ("C0644 <size> <name>"), we
	// ask again and the contents follow.
	if _, err := in.Write([]byte{0}); err != nil {
		rd.stop()
		return err
	}
	line, err := readLine(rd.out)
	if err == nil {
		_, err = in.Write([]byte{0})
	}
	var mode uint32
	if err == nil {
		if _, serr := fmt.Sscanf(line, "C%o %d ", &mode, &rd.size); serr != nil || rd.size < 0 {
			err = fmt.Errorf("scp: unexpected %q", line)
		}
	}
	if err != nil {
		rd.stop()
		return err
	}
	return nil
}

// readLine reads a line of scp, a failure as an error.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("scp: %w", err)
	}
	line = strings.TrimSuffix(line, "\n")
	if line != "" && (line[0] == 1 || line[0] == 2) {
		return "", messageError(strings.TrimSpace(line[1:]))
	}
	return line, nil
}

// ack reads scp's answer: a zero byte, or a failure and its message.
func ack(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("scp: %w", err)
	}
	if b == 0 {
		return nil
	}
	msg, _ := r.ReadString('\n')
	return messageError(strings.TrimSpace(msg))
}

func (rd *reader) stop() {
	if rd.s != nil {
		rd.in.Close()
		rd.s.Close()
		rd.s = nil
	}
}

func (rd *reader) Close() error {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.stop()
	return nil
}

// writer keeps what is written in a temporary file on this computer and
// sends it whole when closed: scp's sink side (scp -t) takes a file of
// known size, and so it keeps the file's mode and owner. Without scp,
// cat writes it.
type writer struct {
	r    *remote
	path string

	mu sync.Mutex
	f  *os.File
	// base: the file holds data a write that is not whole must keep;
	// it is read into f first.
	base  bool
	dirty bool
}

// file is the temporary file, with the remote file's data when it is kept.
func (w *writer) file() (*os.File, error) {
	if w.f != nil {
		return w.f, nil
	}
	f, err := os.CreateTemp("", "sshyard-scp-*")
	if err != nil {
		return nil, err
	}
	if w.base {
		rd := &reader{r: w.r, path: w.path}
		_, err = io.Copy(f, io.NewSectionReader(rd, 0, 1<<62))
		rd.Close()
		if err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, err
		}
	}
	w.f = f
	return f, nil
}

func (w *writer) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := w.file()
	if err != nil {
		return 0, err
	}
	w.dirty = true
	return f.WriteAt(p, off)
}

func (w *writer) ReadAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := w.file()
	if err != nil {
		return 0, err
	}
	return f.ReadAt(p, off)
}

func (w *writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	defer func() {
		w.f.Close()
		os.Remove(w.f.Name())
		w.f = nil
	}()
	if !w.dirty {
		return nil
	}
	w.dirty = false
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if !w.r.scp {
		return w.r.run("cat > "+quote(w.path), w.f, io.Discard)
	}
	fi, err := w.f.Stat()
	if err != nil {
		return err
	}
	return w.send(fi.Size())
}

// send sends the temporary file with scp's sink (scp -t). The name it
// gives is not used: the target is the file itself, not a folder.
func (w *writer) send(size int64) error {
	s, err := w.r.conn.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	in, err1 := s.StdinPipe()
	out, err2 := s.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	errs := &limited{}
	s.Stderr = errs
	if err := s.Start("scp -t -- " + quote(w.path)); err != nil {
		return err
	}
	br := bufio.NewReader(out)
	err = ack(br)
	if err == nil {
		_, err = fmt.Fprintf(in, "C0644 %d upload\n", size)
	}
	if err == nil {
		err = ack(br)
	}
	if err == nil {
		_, err = io.Copy(in, w.f)
	}
	if err == nil {
		_, err = in.Write([]byte{0})
	}
	if err == nil {
		err = ack(br)
	}
	in.Close()
	if werr := scriptError(s.Wait(), errs.String()); err == nil {
		err = werr
	}
	return err
}
