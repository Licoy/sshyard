package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Licoy/sshyard/internal/vt"
	"golang.org/x/crypto/ssh"
)

// State is a session's connection state.
type State int

const (
	Idle State = iota
	Connecting
	Connected
	Reconnecting // waiting to retry after the connection was lost
	Disconnected // the connection was lost and is not retried
	Exited       // the remote shell exited
	Failed       // connecting failed in a way the user must address
	Closed       // the user closed the session
)

func (s State) String() string {
	return [...]string{"idle", "connecting", "connected", "reconnecting", "disconnected", "exited", "failed", "closed"}[s]
}

// Options are a session's behavior.
type Options struct {
	// KeepAlive is how often to probe the server; 0 turns probes off.
	KeepAlive time.Duration
	// KeepAliveMax is how many probes in a row may fail before the
	// connection counts as lost; 0 is 3.
	KeepAliveMax int
	// AutoReconnect reconnects after the connection is lost.
	AutoReconnect bool
	// MaxAttempts bounds automatic reconnects in a row; 0 is 8.
	MaxAttempts int
	// Backoff is the first retry delay, doubled each attempt up to
	// MaxBackoff; 0 are 1 and 30 seconds.
	Backoff, MaxBackoff time.Duration
	// Term is the TERM to request; "" (or one TermOK refuses) is
	// DefaultTerm. The Linux console's ("linux") sends its keys too.
	Term string
	// Scrollback is the terminal's scrollback; 0 is vt.DefaultScrollback.
	Scrollback int
	// NoShell connects without a shell or PTY, for a files (SFTP) pane:
	// the connection is kept, probed and reconnected like a shell's.
	NoShell bool
	// Local runs the user's shell on this computer, in a pseudo-terminal,
	// instead of connecting anywhere: the Config is not used, and the
	// session has no Client. It ends, Exited, with the shell.
	Local bool
	// Shell is the shell a local session runs; zero is the first of
	// Shells.
	Shell Shell
	// Open, when set, opens the terminal's stream instead of an SSH
	// shell: a Telnet connection or a serial port. The session has no
	// Client; it ends, Exited, when the other end closes the stream, and
	// is reconnected when the stream is lost.
	Open func(ctx context.Context, cols, rows int, term string) (Stream, error)
	// Login answers an opened stream's login and password prompts with
	// the Config's User and Credentials, read before it is opened.
	Login bool
	// NewLine is what Enter sends on an opened stream; "" is "\r".
	NewLine string
	// LocalEcho shows what is typed on an opened stream, for a device
	// that does not echo it.
	LocalEcho bool
	// Dir is the folder an SSH shell goes to each time it starts: a cd
	// typed once the shell has gone quiet (enterShell). "~" or "~/…" is
	// under the home folder.
	Dir string
	// Start, when set, is asked each time an SSH shell starts for a
	// command to type after the cd to Dir, as an attach to a tmux
	// session; "" types none. It may take its time, and run commands on
	// client.
	Start func(ctx context.Context, client *ssh.Client) string
}

// Status is a session's state, for the UI.
type Status struct {
	State State
	Err   error
	// Attempt is the reconnect attempt, from 1; Retry when it starts.
	Attempt int
	Retry   time.Time
	// Connects counts successful connections.
	Connects int
	// Hop is the host being dialed while connecting through jump
	// hosts: a jump host's name, or the host's own at the last hop.
	Hop string
}

// Session is a shell on a host. Its terminal stays the same across
// reconnects, so scrollback survives them.
type Session struct {
	cfg  Config
	opts Options
	Term *vt.Terminal

	// OnChange, when set, is called from any goroutine whenever the
	// terminal or the status changes: the app invalidates its window.
	OnChange func()

	mu      sync.Mutex
	status  Status
	client  *ssh.Client
	release func() // lets go of client: closes it, or hands it back to the pool
	shell   *ssh.Session
	// stream is the terminal of a local or Options.Open session, its
	// output copied to the terminal by streamOut.
	stream    Stream
	streamOut *streamOut
	stdin     *serializedInput
	cols      int
	rows      int
	cancel    context.CancelFunc
	wake      chan struct{}
	closed    bool
	started   bool
	done      chan struct{}

	// probe, when set, is how often to measure keepalive round trip
	// while a metrics bar is on screen. Zero uses KeepAlive.
	probe time.Duration
	// latency is the last answered keepalive round trip.
	latency   time.Duration
	latencyOK bool
	// probeNote wakes keepAlive when SetProbe changes the interval.
	probeNote chan struct{}
	// tag marks the shell's environment (TagEnv), tagged once the server
	// took it, so that ShellCwd finds the shell among the server's
	// processes.
	tag    string
	tagged bool
	// tap, when set, is given what the shell writes to the terminal, as
	// it comes, after the terminal took it (SetOutputTap).
	tap atomic.Pointer[func([]byte)]
	// watchers are given the same bytes as tap (Watch), copied on write
	// so the session's goroutine reads them without a lock.
	watchMu  sync.Mutex
	watchers atomic.Pointer[[]*func([]byte)]
	// outAt is when the shell last wrote to the terminal, in Unix
	// nanoseconds, 0 before its first output.
	outAt atomic.Int64
	// takeover finds transfers in the output (SetTakeover).
	takeover atomic.Pointer[Takeover]
	// outMu orders the output: the terminal, or the transfer that took
	// the stream over (taken), gets it in order. tail is the end of what
	// the terminal last got. taken changes holding outMu, and is read
	// without it too: the output may be stuck in the terminal holding it.
	outMu sync.Mutex
	taken atomic.Pointer[takenOver]
	tail  []byte
}

// New returns a session of cols×rows; Start connects it.
func New(cfg Config, opts Options, cols, rows int) *Session {
	if opts.KeepAliveMax == 0 {
		opts.KeepAliveMax = 3
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 8
	}
	if opts.Backoff == 0 {
		opts.Backoff = time.Second
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if !TermOK(opts.Term) {
		opts.Term = DefaultTerm
	}
	s := &Session{cfg: cfg, opts: opts, cols: max(cols, 1), rows: max(rows, 1), wake: make(chan struct{}, 1), done: make(chan struct{}), probeNote: make(chan struct{}, 1), tag: newTag()}
	s.Term = vt.New(s.cols, s.rows)
	s.Term.SetLinuxKeys(linuxTerm(opts.Term))
	if opts.Scrollback > 0 {
		s.Term.SetScrollback(opts.Scrollback)
	}
	s.Term.Reply = s.replyInput
	return s
}

// Config returns the session's configuration.
func (s *Session) Config() Config { return s.cfg }

// Status returns the session's status.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Client returns the SSH client while connected, for SFTP.
func (s *Session) Client() *ssh.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != Connected {
		return nil
	}
	return s.client
}

// SetProbe sets how often a visible metrics bar measures keepalive round
// trip. d <= 0 keeps Options.KeepAlive (and sends nothing when that is
// 0). The same value again does not wake the probe, so a frame may call
// this every time it draws.
func (s *Session) SetProbe(d time.Duration) {
	s.mu.Lock()
	if s.probe == d {
		s.mu.Unlock()
		return
	}
	s.probe = d
	s.mu.Unlock()
	select {
	case s.probeNote <- struct{}{}:
	default:
	}
}

// probeEvery is the current keepalive period.
func (s *Session) probeEvery() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probe > 0 {
		return s.probe
	}
	return s.opts.KeepAlive
}

// Latency is the last keepalive round trip. ok is false until one probe
// has been answered.
func (s *Session) Latency() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latency, s.latencyOK
}

func (s *Session) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

func (s *Session) setStatus(fn func(st *Status)) {
	s.mu.Lock()
	fn(&s.status)
	s.mu.Unlock()
	s.changed()
}

// Start connects the session in the background.
func (s *Session) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()
	go s.run(ctx)
}

// Reconnect connects again now: after a failure, after the connection was
// lost, or while waiting for the next automatic attempt. The user does
// this after fixing what failed (trusting a host key, typing a password).
func (s *Session) Reconnect() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if !s.started {
		s.mu.Unlock()
		s.Start()
		return
	}
	st := s.status.State
	s.mu.Unlock()
	switch st {
	case Reconnecting:
		select {
		case s.wake <- struct{}{}:
		default:
		}
	case Disconnected, Failed, Exited:
		s.mu.Lock()
		s.started = false
		s.done = make(chan struct{})
		s.mu.Unlock()
		s.Start()
	}
}

// Close ends the session for good.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	cancel, shell, stream := s.cancel, s.shell, s.stream
	s.status.State = Closed
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if shell != nil {
		// Only this session's channel: the connection may serve others.
		shell.Close()
	}
	if stream != nil {
		stream.Close()
	}
	s.endTakeover()
	s.changed()
}

// Done is closed when the session's connection loop ends.
func (s *Session) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Write sends input to the shell; it is dropped while disconnected.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()
	if w == nil {
		return 0, errors.New("session: not connected")
	}
	return w.Write(p)
}

// Send writes p to the shell without waiting for it: after the input
// still being written, in order. Typed input goes this way, so a stalled
// connection never holds up the UI thread.
func (s *Session) Send(p []byte) error {
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()
	if w == nil {
		return errors.New("session: not connected")
	}
	return w.send(p)
}

// Resize changes the size of the terminal and of the remote PTY.
func (s *Session) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	s.mu.Lock()
	if cols == s.cols && rows == s.rows {
		s.mu.Unlock()
		return
	}
	s.cols, s.rows = cols, rows
	shell, stream := s.shell, s.stream
	s.mu.Unlock()
	s.Term.Resize(cols, rows)
	if shell != nil {
		shell.WindowChange(rows, cols)
	}
	if stream != nil {
		stream.Resize(cols, rows)
	}
	s.changed()
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// run connects, runs the shell and reconnects until the session ends.
func (s *Session) run(ctx context.Context) {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	defer close(done)
	attempt := 0
	for {
		state := Connecting
		if attempt > 0 {
			state = Reconnecting
		}
		s.setStatus(func(st *Status) { st.State, st.Attempt = state, attempt })
		err := s.connect(ctx)
		if s.isClosed() || ctx.Err() != nil {
			return
		}
		if err == nil {
			// Connected, and the shell ran until it ended.
			attempt = 0
			lost := s.serve(ctx)
			if s.isClosed() {
				return
			}
			if lost == nil {
				s.setStatus(func(st *Status) { st.State, st.Err = Exited, nil })
				return
			}
			err = lost
			if !s.opts.AutoReconnect {
				s.setStatus(func(st *Status) { st.State, st.Err = Disconnected, err })
				return
			}
		} else if !Classify(err).Retryable() || attempt == 0 && s.Status().Connects == 0 || !s.opts.AutoReconnect {
			// The first connection, or one the user has to fix, is not
			// retried by itself: the UI shows the error and a button.
			s.setStatus(func(st *Status) { st.State, st.Err = Failed, err })
			return
		}
		attempt++
		if attempt > s.opts.MaxAttempts {
			s.setStatus(func(st *Status) { st.State, st.Err = Disconnected, err })
			return
		}
		delay := s.opts.Backoff << (attempt - 1)
		if delay > s.opts.MaxBackoff || delay <= 0 {
			delay = s.opts.MaxBackoff
		}
		retry := time.Now().Add(delay)
		s.setStatus(func(st *Status) { st.State, st.Err, st.Attempt, st.Retry = Reconnecting, err, attempt, retry })
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// dial gets the session a connection: the pool's shared one, or its own.
func (s *Session) dial(ctx context.Context) (*ssh.Client, func(), error) {
	cfg := s.cfg
	if len(cfg.Jumps) > 0 {
		cfg.Progress = func(hop string) {
			s.setStatus(func(st *Status) { st.Hop = hop })
			if s.cfg.Progress != nil {
				s.cfg.Progress(hop)
			}
		}
	}
	defer s.setStatus(func(st *Status) { st.Hop = "" })
	if p := cfg.Pool; p != nil {
		return p.Acquire(ctx, cfg.Key, cfg)
	}
	client, err := Dial(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return client, func() { client.Close() }, nil
}

// broken reports the connection lost, to the pool that shares it.
func (s *Session) broken(client *ssh.Client) {
	if s.cfg.Pool != nil {
		s.cfg.Pool.Broken(client)
	}
}

func (s *Session) connect(ctx context.Context) error {
	if s.opts.Local {
		return s.connectLocal()
	}
	if s.opts.Open != nil {
		return s.connectOpen(ctx)
	}
	client, release, err := s.dial(ctx)
	if err != nil {
		return err
	}
	if s.opts.NoShell {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			release()
			return context.Canceled
		}
		s.client, s.release = client, release
		s.status.State, s.status.Err, s.status.Attempt = Connected, nil, 0
		s.status.Connects++
		s.mu.Unlock()
		s.changed()
		return nil
	}
	shell, err := client.NewSession()
	if err != nil {
		// A shared connection that cannot open a channel is gone.
		s.broken(client)
		release()
		return err
	}
	fail := func(err error) error {
		shell.Close()
		release()
		return err
	}
	s.mu.Lock()
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := shell.RequestPty(s.opts.Term, rows, cols, modes); err != nil {
		return fail(err)
	}
	// Servers take LC_* by default (AcceptEnv): one that does not just
	// leaves the shell untagged.
	tagged := shell.Setenv(TagEnv, s.tag) == nil
	stdin, err := shell.StdinPipe()
	if err != nil {
		return fail(err)
	}
	out := &termWriter{s: s}
	shell.Stdout = out
	shell.Stderr = out
	// A new shell starts with the terminal's input as at a reset: the
	// last program may have gone with the mouse or focus reports on.
	s.Term.ResetOutputStream()
	s.Term.ResetInputModes(true)
	s.outAt.Store(0)
	if err := shell.Shell(); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fail(context.Canceled)
	}
	s.client, s.release, s.shell, s.stdin, s.tagged = client, release, shell, &serializedInput{WriteCloser: stdin}, tagged
	s.status.State, s.status.Err, s.status.Attempt = Connected, nil, 0
	s.status.Connects++
	s.mu.Unlock()
	if s.opts.Dir != "" || s.opts.Start != nil {
		go s.enterShell(ctx, client, shell, time.Now())
	}
	s.changed()
	return nil
}

// dirQuiet is how long a new shell prints nothing before enterShell takes
// its prompt to be up; dirWait is how long it waits at most, for a shell
// that prints nothing.
const (
	dirQuiet = 250 * time.Millisecond
	dirWait  = 5 * time.Second
)

// enterShell types the cd to Options.Dir, then the command Options.Start
// gives, into shell, started at start, once it has printed and then gone
// quiet for dirQuiet, or after dirWait: typed before the prompt, the
// terminal would echo it twice. It gives up when shell is gone.
func (s *Session) enterShell(ctx context.Context, client *ssh.Client, shell *ssh.Session, start time.Time) {
	var line string
	if s.opts.Dir != "" {
		line = cdLine(s.opts.Dir)
	}
	if s.opts.Start != nil {
		if cmd := s.opts.Start(ctx, client); cmd != "" {
			// Its leading space keeps it out of the history, as the cd's.
			line += " " + cmd + "\r"
		}
	}
	if line == "" {
		return
	}
	for {
		time.Sleep(dirQuiet / 5)
		s.mu.Lock()
		in, cur := s.stdin, s.shell
		s.mu.Unlock()
		if cur != shell || in == nil {
			return
		}
		now := time.Now()
		at := s.outAt.Load()
		if (at != 0 && now.Sub(time.Unix(0, at)) >= dirQuiet) || now.Sub(start) >= dirWait {
			_ = in.send([]byte(line))
			return
		}
	}
}

// cdLine is the line changing a shell to dir, single-quoted as POSIX
// shells and fish read it, with "~" left to the shell to expand.
// Its leading space keeps it out of the history of shells that ignore
// such lines.
func cdLine(dir string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	arg := quote(dir)
	switch {
	case dir == "~":
		arg = "~"
	case strings.HasPrefix(dir, "~/"):
		arg = "~/" + quote(dir[2:])
	}
	return " cd " + arg + "\r"
}

// serve waits for the shell to end. It returns nil when the shell exited
// and the error when the connection was lost.
func (s *Session) serve(ctx context.Context) error {
	s.mu.Lock()
	client, shell, stream := s.client, s.shell, s.stream
	s.mu.Unlock()
	if p, ok := stream.(localPTY); ok && s.opts.Local {
		return s.serveLocal(ctx, p)
	}
	if stream != nil {
		return s.serveStream(ctx, stream)
	}

	lost := make(chan error, 1)
	ping := func() error {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		return err
	}
	go s.keepAlive(ctx, ping, false, lost)
	waited := make(chan error, 1)
	if shell != nil {
		go func() { waited <- shell.Wait() }()
	}
	connDone := make(chan error, 1)
	go func() { connDone <- client.Wait() }()

	var err error
	shellWaited := false
	select {
	case err = <-waited:
		shellWaited = true
		var exit *ssh.ExitError
		if err == nil || errors.As(err, &exit) {
			err = nil
		} else if err == io.EOF || errors.As(err, new(*ssh.ExitMissingError)) {
			err = errConnectionLost
		}
	case err = <-lost:
	case e := <-connDone:
		if s.isClosed() {
			break
		}
		err = errors.Join(errConnectionLost, e)
	case <-ctx.Done():
	}
	s.endTakeover()
	// What the program asked of the mouse goes with it.
	s.Term.ResetInputModes(false)
	if err != nil {
		// Lost: the other sessions on a shared connection lose it too.
		s.broken(client)
	}
	if shell != nil {
		shell.Close()
	}
	s.mu.Lock()
	release := s.release
	s.client, s.release, s.shell, s.stdin = nil, nil, nil, nil
	s.mu.Unlock()
	if release != nil {
		release()
	}
	// A lost connection is now closed, so Wait can drain its output copies
	// before a replacement shell resets the decoder. A normal cancellation
	// only closes one channel on a possibly shared connection; do not wait
	// for a remote peer to acknowledge that close.
	if err != nil && shell != nil && !shellWaited {
		select {
		case <-waited:
		case <-ctx.Done():
		}
	}
	return err
}

var errConnectionLost = errors.New("session: connection lost (EOF)")

// ErrKeepAliveTimeout reports a server that stopped answering keepalives.
var ErrKeepAliveTimeout = errors.New("session: server stopped responding (keepalive timeout)")

// keepAlive pings every probe period and reports the connection lost
// when a ping fails, or KeepAliveMax in a row go unanswered. lenient
// stops pinging instead while none was ever answered: a server that does
// not take them.
func (s *Session) keepAlive(ctx context.Context, ping func() error, lenient bool, lost chan<- error) {
	var timer *time.Timer
	var timed <-chan time.Time
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer = nil
		timed = nil
	}
	arm := func() {
		stopTimer()
		every := s.probeEvery()
		if every <= 0 {
			return
		}
		timer = time.NewTimer(every)
		timed = timer.C
	}
	arm()
	defer stopTimer()
	misses := 0
	answered := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.probeNote:
			stopTimer()
			// Hiding the bar must not send a probe of its own.
			if s.probeEvery() <= 0 {
				continue
			}
		case <-timed:
			timer = nil
			timed = nil
		}
		every := s.probeEvery()
		if every <= 0 {
			continue
		}
		start := time.Now()
		reply := make(chan error, 1)
		go func() { reply <- ping() }()
		select {
		case err := <-reply:
			if err != nil {
				lost <- errors.Join(errConnectionLost, err)
				return
			}
			misses, answered = 0, true
			s.mu.Lock()
			s.latency = time.Since(start)
			s.latencyOK = true
			s.mu.Unlock()
			s.changed()
		case <-time.After(every):
			misses++
			if lenient && !answered {
				return
			}
			if misses >= s.opts.KeepAliveMax {
				lost <- ErrKeepAliveTimeout
				return
			}
		case <-ctx.Done():
			return
		}
		arm()
	}
}

// SetOutputTap gives fn the bytes the session prints, as the terminal
// takes them, until it is set again; nil stops it. fn runs on the
// session's goroutine, must not keep p, and must not wait.
func (s *Session) SetOutputTap(fn func(p []byte)) {
	if fn == nil {
		s.tap.Store(nil)
		return
	}
	s.tap.Store(&fn)
}

// Watch gives fn the bytes the session prints, as SetOutputTap does,
// until stop is called; any number may watch at once. fn runs on the
// session's goroutine, must not keep p, and must not wait.
func (s *Session) Watch(fn func(p []byte)) (stop func()) {
	f := &fn
	s.watchMu.Lock()
	var ws []*func([]byte)
	if old := s.watchers.Load(); old != nil {
		ws = append(ws, *old...)
	}
	ws = append(ws, f)
	s.watchers.Store(&ws)
	s.watchMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.watchMu.Lock()
			defer s.watchMu.Unlock()
			var rest []*func([]byte)
			if old := s.watchers.Load(); old != nil {
				for _, g := range *old {
					if g != f {
						rest = append(rest, g)
					}
				}
			}
			s.watchers.Store(&rest)
		})
	}
}

// termWriter writes output to the terminal, answering the login prompts
// in it when it logs in.
type termWriter struct {
	s     *Session
	login *login
	in    *serializedInput
}

func (w *termWriter) Write(p []byte) (int, error) {
	s := w.s
	for {
		s.outMu.Lock()
		t := s.taken.Load()
		if t == nil {
			err := w.show(p)
			s.outMu.Unlock()
			return len(p), err
		}
		s.outMu.Unlock()
		if t.q.Add(p) {
			s.outAt.Store(time.Now().UnixNano())
			return len(p), nil
		}
		// The transfer ended: what it left goes to the terminal first.
		<-t.done
	}
}

// show gives p to the terminal, or from where a transfer starts in it,
// to the transfer. Called holding outMu.
func (w *termWriter) show(p []byte) error {
	if fn := w.s.takeover.Load(); fn != nil {
		if before, from, run := w.s.find(*fn, p); run != nil {
			if len(before) > 0 {
				w.print(before)
			}
			if w.s.startTakeover(from, run) {
				w.s.changed()
				return nil
			}
			p = from
		}
	}
	w.s.keepTail(p)
	return w.print(p)
}

func (w *termWriter) print(p []byte) error {
	_, err := w.s.Term.Write(p)
	if tap := w.s.tap.Load(); tap != nil {
		(*tap)(p)
	}
	if ws := w.s.watchers.Load(); ws != nil {
		for _, fn := range *ws {
			(*fn)(p)
		}
	}
	w.s.outAt.Store(time.Now().UnixNano())
	w.s.changed()
	if w.login != nil {
		if answer := w.login.feed(p, time.Now()); answer != nil {
			w.in.answer(answer)
		}
	}
	return err
}
