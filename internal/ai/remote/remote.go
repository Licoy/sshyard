// Package remote runs an AI command on a host's existing SSH connection.
// It implements policy.Exec. A2 keeps a Runner beside the gate: Authorize,
// then Run, then Done. Runner never dials and never takes a pool reference.
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/session"
	"golang.org/x/crypto/ssh"
	"mvdan.cc/sh/v3/syntax"
)

const (
	// MaxConcurrent is how many AI commands one host runs at once.
	// A third Run waits. The wait is only a delay: the caller sees
	// it as Run taking longer, or as ctx.Err() if the context ends first.
	MaxConcurrent = 2
	// DefaultCommandTimeout is the limit for one Run when Options is zero.
	// Any other CommandTimeout is clamped to MinCommandTimeout..MaxCommandTimeout.
	// These are not settings: the settings page does not name them.
	DefaultCommandTimeout = 60 * time.Second
	MinCommandTimeout     = 5 * time.Second
	MaxCommandTimeout     = 30 * time.Minute
	// DefaultTurnTimeout is one agent turn, from BeginTurn or the first Run.
	// A positive Options.TurnTimeout is used as given and is not clamped.
	DefaultTurnTimeout = 10 * time.Minute
	// KillGrace is how long a cancel waits after SIGTERM before SIGKILL.
	KillGrace = 5 * time.Second
)

// ErrStopUnconfirmed means the remote command's cleanup could not be verified.
var ErrStopUnconfirmed = errors.New("remote: command stop could not be confirmed")

// CountingWriter counts bytes written through it. Wrap the stdout and
// stderr passed to Run and add Bytes for audit Done. The process-group
// marker is removed before it reaches the writers Run was given.
type CountingWriter struct {
	W io.Writer
	n atomic.Int64
}

// Write writes to W (a nil W discards) and counts the bytes accepted.
func (c *CountingWriter) Write(p []byte) (int, error) {
	n := len(p)
	var err error
	if c.W != nil {
		n, err = c.W.Write(p)
	}
	c.n.Add(int64(n))
	return n, err
}

// Bytes is how many bytes Write has accepted.
func (c *CountingWriter) Bytes() int64 { return c.n.Load() }

// Options sets the limits for a Runner. Zero uses the defaults.
type Options struct {
	// CommandTimeout limits one Run. Zero means 60s. Any other value
	// is clamped to 5s–30min.
	CommandTimeout time.Duration
	// TurnTimeout limits one turn. Zero means 10 minutes. A positive
	// value is used as given so a test can be short.
	TurnTimeout time.Duration
}

// Runner runs commands for one host. Two Runners built on the same
// client share that host's concurrency cap. The turn clock is per Runner.
type Runner struct {
	client      *ssh.Client
	cmdTimeout  time.Duration
	turnTimeout time.Duration

	mu      sync.Mutex
	turnEnd time.Time
}

var _ policy.Exec = (*Runner)(nil)

// New runs commands on client. client is the host's pooled connection.
func New(client *ssh.Client, opt Options) *Runner {
	turn := opt.TurnTimeout
	if turn <= 0 {
		turn = DefaultTurnTimeout
	}
	return &Runner{client: client, cmdTimeout: commandTimeout(opt.CommandTimeout), turnTimeout: turn}
}

// BeginTurn starts a new turn. Run stops a command when the turn ends.
// The first Run starts a turn when BeginTurn was not called.
func (r *Runner) BeginTurn() {
	r.mu.Lock()
	r.turnEnd = time.Now().Add(r.turnTimeout)
	r.mu.Unlock()
}

func (r *Runner) ensureTurn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turnEnd.IsZero() {
		r.turnEnd = time.Now().Add(r.turnTimeout)
	}
}

func (r *Runner) deadline(ctx context.Context, timeout time.Duration) time.Time {
	d := time.Now().Add(timeout)
	r.mu.Lock()
	end := r.turnEnd
	r.mu.Unlock()
	if !end.IsZero() && end.Before(d) {
		d = end
	}
	if cd, ok := ctx.Deadline(); ok && cd.Before(d) {
		d = cd
	}
	return d
}

func commandTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultCommandTimeout
	}
	if d < MinCommandTimeout {
		return MinCommandTimeout
	}
	if d > MaxCommandTimeout {
		return MaxCommandTimeout
	}
	return d
}

// shortcut: one slot record per *ssh.Client for the process lifetime.
// Drop entries when hosts are opened and closed often enough to matter.
var (
	gatesMu sync.Mutex
	gates   = map[*ssh.Client]*gate{}
)

type gate struct {
	slots chan struct{}
}

func gateFor(c *ssh.Client) *gate {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	g := gates[c]
	if g == nil {
		g = &gate{slots: make(chan struct{}, MaxConcurrent)}
		gates[c] = g
	}
	return g
}

func acquire(ctx context.Context, c *ssh.Client) (func(), error) {
	g := gateFor(c)
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Run executes cmd with sh -c in its own process group. dir is the
// remote working directory (empty keeps the server's). A non-zero
// remote status is the exit code with a nil error. A cancelled context
// returns -1 with the cancellation cause. A command or turn timeout uses
// context.DeadlineExceeded. Cancel and timeout send SSH SIGTERM, then
// stop the process group, escalating to SIGKILL after KillGrace. If the
// stop cannot be verified, the error also matches ErrStopUnconfirmed.
func (r *Runner) Run(ctx context.Context, cmd, dir string, stdout, stderr io.Writer) (int, error) {
	return r.RunWithTimeout(ctx, cmd, dir, stdout, stderr, r.cmdTimeout)
}

// RunWithTimeout preserves this runner's turn clock and shared host quota.
func (r *Runner) RunWithTimeout(ctx context.Context, cmd, dir string, stdout, stderr io.Writer, timeout time.Duration) (int, error) {
	timeout = commandTimeout(timeout)
	if r.client == nil {
		return -1, errors.New("remote: no client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	r.ensureTurn()
	deadline := r.deadline(ctx, timeout)
	if !time.Now().Before(deadline) {
		return -1, context.DeadlineExceeded
	}
	waitCtx, cancelWait := context.WithDeadline(ctx, deadline)
	defer cancelWait()
	release, err := acquire(waitCtx, r.client)
	if err != nil {
		return -1, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	deadline = r.deadline(ctx, timeout)
	if !time.Now().Before(deadline) {
		return -1, context.DeadlineExceeded
	}
	script, err := wrap(cmd, dir)
	if err != nil {
		return -1, err
	}
	idC := make(chan mark, 1)
	errOut := &markerWriter{rest: stderr, id: idC}
	// cmdCtx is not the caller's context: cancelling that closes the
	// channel and leaves a non-PTY process running. Stop the command first.
	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	defer cancelCmd()
	var sess atomic.Pointer[ssh.Session]
	errc := make(chan error, 1)
	go func() {
		errc <- session.ExecStarted(cmdCtx, r.client, script, stdout, errOut, func(s *ssh.Session) {
			sess.Store(s)
		})
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	var id mark
	for {
		select {
		case err := <-errc:
			return mapExit(ctx.Err(), err)
		case id = <-idC:
			idC = nil
		case <-ctx.Done():
			return r.halt(ctx.Err(), &sess, id, idC, errc, cancelCmd)
		case <-timer.C:
			return r.halt(timeoutErr(ctx), &sess, id, idC, errc, cancelCmd)
		}
	}
}

func timeoutErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

// halt signals the command, kills what the marker named, and waits until
// ExecStarted returns. The channel stays open until that kill finishes.
func (r *Runner) halt(reason error, sess *atomic.Pointer[ssh.Session], id mark, idC <-chan mark, errc <-chan error, cancelCmd context.CancelFunc) (int, error) {
	if idC != nil {
		select {
		case id = <-idC:
		case err := <-errc:
			return mapExit(reason, err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	// A natural exit can be ready at the same time as cancellation.
	select {
	case err := <-errc:
		return mapExit(reason, err)
	default:
	}
	stopErr := r.stop(sess.Load(), id)
	cancelCmd()
	select {
	case <-errc:
	case <-time.After(KillGrace):
		if stopErr == nil {
			stopErr = errors.New("remote: command did not exit")
		}
	}
	if stopErr != nil {
		return -1, errors.Join(reason, ErrStopUnconfirmed, stopErr)
	}
	return -1, reason
}

func (r *Runner) stop(sess *ssh.Session, id mark) error {
	if sess != nil {
		_ = sess.Signal(ssh.SIGTERM)
	}
	if id.id <= 1 {
		return errors.New("remote: no remote process")
	}
	kctx, cancel := context.WithTimeout(context.Background(), KillGrace+4*time.Second)
	defer cancel()
	var buf bytes.Buffer
	err := session.Exec(kctx, r.client, killScript(id), &buf, io.Discard)
	what := "process"
	if id.group {
		what = "process group"
	}
	if strings.Contains(buf.String(), "STILL_ALIVE") {
		return fmt.Errorf("remote: %s %d still running", what, id.id)
	}
	if err != nil {
		return fmt.Errorf("remote: stop %s %d: %w", what, id.id, err)
	}
	return nil
}

// mark is the process the wrapper reported on stderr.
// group is true when id is a process-group id (setsid or perl setpgrp).
type mark struct {
	id    int
	group bool
}

// pidTreeAwk reads "pid ppid" lines and prints root plus descendants.
// A BSD header ("PID PPID") and blank lines are not pids.
const pidTreeAwk = `{if ($1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/) c[$2]=c[$2]" "$1} END{s=r; n=split(s,q," "); for(i=1;i<=n;i++){m=split(c[q[i]],a," "); for(j=1;j<=m;j++) if(a[j] ~ /^[0-9]+$/){n++; q[n]=a[j]; s=s" "a[j]}} print s}`

func killScript(id mark) string {
	steps := int(KillGrace / (100 * time.Millisecond))
	// One line, then sh -c, so a POSIX login shell runs it. $'' quoting
	// would break dash, which has no ANSI-C quotes.
	var body string
	if id.group {
		body = fmt.Sprintf(`kill -TERM -%d 2>/dev/null || true; i=0; while kill -0 -%d 2>/dev/null; do if [ "$i" -ge %d ]; then break; fi; i=$((i+1)); sleep 0.1; done; if kill -0 -%d 2>/dev/null; then kill -KILL -%d 2>/dev/null || true; sleep 0.2; fi; if kill -0 -%d 2>/dev/null; then echo STILL_ALIVE; exit 1; fi; exit 0`, id.id, id.id, steps, id.id, id.id, id.id)
	} else {
		// Snapshot the tree first: after the parent dies, children reparent
		// and a later scan would miss them. Do not signal the group. The
		// fallback shell may be the session leader.
		// One -o: procps appends repeated -o flags, BSD ps keeps the last.
		// pid=,ppid= drops the header on both; awk also skips a header line.
		// shortcut: needs ps and awk. Without them only the reported pid is
		// killed; children can survive.
		body = fmt.Sprintf(`ids=%d; found=$(ps -A -o pid=,ppid= | awk -v r=%d '%s'); if [ -n "$found" ]; then ids=$found; fi; for p in $ids; do kill -TERM "$p" 2>/dev/null || true; done; i=0; alive=0; while :; do alive=0; for p in $ids; do if kill -0 "$p" 2>/dev/null; then alive=1; fi; done; if [ "$alive" -eq 0 ]; then break; fi; if [ "$i" -ge %d ]; then break; fi; i=$((i+1)); sleep 0.1; done; if [ "$alive" -eq 1 ]; then for p in $ids; do kill -KILL "$p" 2>/dev/null || true; done; sleep 0.2; fi; for p in $ids; do if kill -0 "$p" 2>/dev/null; then echo STILL_ALIVE; exit 1; fi; done; exit 0`, id.id, id.id, pidTreeAwk, steps)
	}
	q, err := syntax.Quote(body, syntax.LangBash)
	if err != nil {
		return body
	}
	return "sh -c " + q
}

func mapExit(reason, err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil
	}
	if reason != nil {
		return -1, errors.Join(reason, ErrStopUnconfirmed, err)
	}
	return -1, err
}

// wrap runs cmd so it can be killed without signalling the SSH session.
// setsid -w is first: --wait matters when the caller is already a session
// leader (OpenSSH), because setsid then forks. perl setpgrp is the same
// kind of group on hosts without setsid (macOS). The last branch shares
// the session group and reports a pid.
func wrap(cmd, dir string) (string, error) {
	group, err := commandScript("SSHYARD_PGID", cmd, dir)
	if err != nil {
		return "", err
	}
	pid, err := commandScript("SSHYARD_PID", cmd, dir)
	if err != nil {
		return "", err
	}
	return "if command -v setsid >/dev/null 2>&1 && setsid -w true >/dev/null 2>&1; then exec setsid -w sh -c " + group +
		"; elif command -v perl >/dev/null 2>&1; then exec perl -e 'setpgrp(0,0); exec @ARGV or die' sh -c " + group +
		"; else exec sh -c " + pid + "; fi", nil
}

func commandScript(marker, cmd, dir string) (string, error) {
	qcmd, err := syntax.Quote(cmd, syntax.LangBash)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "printf '%s %%s\\n' $$ >&2; ", marker)
	if dir != "" {
		qdir, err := syntax.Quote(dir, syntax.LangBash)
		if err != nil {
			return "", err
		}
		b.WriteString("cd -- ")
		b.WriteString(qdir)
		b.WriteString(" && ")
	}
	b.WriteString("exec sh -c ")
	b.WriteString(qcmd)
	return syntax.Quote(b.String(), syntax.LangBash)
}

type markerWriter struct {
	rest io.Writer
	id   chan mark
	buf  []byte
	done bool
}

func (m *markerWriter) Write(p []byte) (int, error) {
	if m.done {
		return m.writeRest(p)
	}
	m.buf = append(m.buf, p...)
	i := bytes.IndexByte(m.buf, '\n')
	if i < 0 {
		if len(m.buf) > 128 {
			m.done = true
			b := m.buf
			m.buf = nil
			return m.writeRest(b)
		}
		return len(p), nil
	}
	line := string(m.buf[:i])
	rest := append([]byte(nil), m.buf[i+1:]...)
	m.buf = nil
	m.done = true
	if id, ok := parseMarker(line); ok {
		select {
		case m.id <- id:
		default:
		}
	} else if line != "" {
		rest = append(append([]byte(line), '\n'), rest...)
	}
	if len(rest) > 0 {
		if _, err := m.writeRest(rest); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func (m *markerWriter) writeRest(p []byte) (int, error) {
	if m.rest == nil || len(p) == 0 {
		return len(p), nil
	}
	return m.rest.Write(p)
}

func parseMarker(line string) (mark, bool) {
	line = strings.TrimRight(line, "\r")
	group := false
	var rest string
	switch {
	case strings.HasPrefix(line, "SSHYARD_PGID "):
		group = true
		rest = line[len("SSHYARD_PGID "):]
	case strings.HasPrefix(line, "SSHYARD_PID "):
		rest = line[len("SSHYARD_PID "):]
	default:
		return mark{}, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || n <= 1 {
		return mark{}, false
	}
	return mark{id: n, group: group}, true
}
