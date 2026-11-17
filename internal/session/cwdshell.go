package session

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// cwdLineMax is the longest line the probe shell may print: a directory
// (PATH_MAX is 4096 on Linux) and its marks. A longer one ends it, so a
// server cannot make it hold more.
const cwdLineMax = 8 << 10

// errCwdShellGone is the probe shell ending, or printing a line too long.
var errCwdShellGone = errors.New("session: the probe shell ended")

// cwdShell is a sh kept running on a connection, its input open, that
// cwdBody is typed into for each read of a shell's directory: one round
// trip, rather than the four of opening a channel for it (ShellCwd). It
// is opened with no environment of its own, so it never holds a
// terminal's tag (TagEnv), and its input is a pipe, not a terminal.
type cwdShell struct {
	client *ssh.Client
	sess   *ssh.Session
	in     io.Writer
	// lines are what sh prints, a line at a time, closed when it ends.
	lines chan string
	done  chan struct{}
	once  sync.Once
	// used is set once it answered: one that never did is not started
	// again on the connection. idle closes it once unused a while. Both
	// are under Session.cwdMu.
	used bool
	idle *time.Timer
}

// cwdShellIdle is how long a probe shell stays open unused.
var cwdShellIdle = 30 * time.Second

// startCwdShell starts a probe shell on client.
func startCwdShell(client *ssh.Client) (*cwdShell, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	in, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start("exec sh"); err != nil {
		sess.Close()
		return nil, err
	}
	sh := &cwdShell{client: client, sess: sess, in: in, lines: make(chan string), done: make(chan struct{})}
	go sh.read(out)
	return sh, nil
}

// read passes what sh prints on to lines, until it ends, prints a line
// longer than cwdLineMax or is closed.
func (sh *cwdShell) read(out io.Reader) {
	defer close(sh.lines)
	r := bufio.NewReaderSize(out, cwdLineMax)
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			sh.close()
			return
		}
		select {
		case sh.lines <- string(line):
		case <-sh.done:
			return
		}
	}
}

// close ends the probe shell.
func (sh *cwdShell) close() {
	sh.once.Do(func() {
		close(sh.done)
		sh.sess.Close()
	})
}

// cwd reads the directory of the shell tagged tag ("" untagged), as
// ShellCwd. Its answer is one line between two marks new for each read,
// so what the login printed, or what an earlier read left, is skipped.
func (sh *cwdShell) cwd(ctx context.Context, tag string) (string, error) {
	mark := newTag()
	if _, err := io.WriteString(sh.in, cwdRequest(tag, mark)); err != nil {
		return "", err
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-sh.done:
			return "", errCwdShellGone
		case line, ok := <-sh.lines:
			if !ok {
				return "", errCwdShellGone
			}
			if d, ok, err := cwdAnswer(line, mark); ok {
				sh.used = true
				return d, err
			}
		}
	}
}

// cwdRequest is the line typed into a probe shell to read the directory
// of the shell tagged tag: cwdBody in a subshell, its input not the probe
// shell's, then its status and what it printed between two marks.
func cwdRequest(tag, mark string) string {
	return `d=$( { ` + cwdBody(tag) + `; } </dev/null 2>/dev/null); ` +
		`printf '%s %s %s %s\n' ` + mark + ` "$?" "$d" ` + mark + "\n"
}

// cwdAnswer reads line as the answer to the request marked mark: ok is
// false for a line that is not, err ErrNoCwd for no directory, or one
// over several lines.
func cwdAnswer(line, mark string) (d string, ok bool, err error) {
	rest, ok := strings.CutPrefix(line, mark+" ")
	if !ok {
		return "", false, nil
	}
	rest, whole := strings.CutSuffix(strings.TrimSuffix(rest, "\n"), " "+mark)
	code, d, _ := strings.Cut(rest, " ")
	if !whole || code != "0" {
		return "", true, ErrNoCwd
	}
	d, err = cwdPath(d)
	return d, true, err
}

// ProbeCwd is ShellCwd for the session's shell on its connection, through
// a probe shell kept open there between reads. Should that not start, or
// break, it falls back to ShellCwd, and the next read starts another;
// one that never answered is not started again on the connection. One
// read runs at a time.
func (s *Session) ProbeCwd(ctx context.Context) (string, error) {
	s.cwdMu.Lock()
	defer s.cwdMu.Unlock()
	s.mu.Lock()
	client, tag, sh := s.client, s.tag, s.cwdSh
	if s.status.State != Connected {
		client = nil
	}
	if !s.tagged || s.shell == nil {
		tag = ""
	}
	if sh != nil && sh.client != client {
		// Of a connection gone: serve closes it too.
		old := sh
		defer old.close()
		s.cwdSh, sh = nil, nil
	}
	start := client != nil && sh == nil && s.noCwdSh != client
	s.mu.Unlock()
	if client == nil {
		return "", errors.New("session: not connected")
	}
	if start {
		var err error
		if sh, err = startCwdShell(client); err != nil {
			sh = nil
			s.mu.Lock()
			s.noCwdSh = client
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			gone := s.closed || s.client != client
			if !gone {
				s.cwdSh = sh
			}
			s.mu.Unlock()
			if gone {
				sh.close()
				return "", errors.New("session: not connected")
			}
		}
	}
	if sh != nil {
		d, err := sh.cwd(ctx, tag)
		if err == nil || errors.Is(err, ErrNoCwd) {
			// Unused for cwdShellIdle (the pane stopped following, or the
			// shell reports now), it goes.
			if sh.idle == nil {
				sh.idle = time.AfterFunc(cwdShellIdle, func() { s.dropCwdShell(sh) })
			} else {
				sh.idle.Reset(cwdShellIdle)
			}
			return d, err
		}
		sh.close()
		s.mu.Lock()
		if s.cwdSh == sh {
			s.cwdSh = nil
		}
		if !sh.used && ctx.Err() == nil {
			s.noCwdSh = client
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return ShellCwd(ctx, client, tag)
}

// dropCwdShell closes the probe shell sh, which the next ProbeCwd starts
// again.
func (s *Session) dropCwdShell(sh *cwdShell) {
	s.mu.Lock()
	if s.cwdSh == sh {
		s.cwdSh = nil
	}
	s.mu.Unlock()
	sh.close()
}
