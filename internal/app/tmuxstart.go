package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// What an SSH terminal's shell is given each time it starts
// (session.Options.Start): the command the terminal was opened for, once
// (the shell in a container); else the tmux session the terminal is in,
// attached again after a reconnect; else its host's (Host.Tmux), created
// when there is none. Which session a terminal is in is read again every
// tmuxWatchEvery, and by the tmux panel, so that the user's own attach,
// switch, detach and exit are followed.

// tmuxWatchEvery is how often a terminal in a tmux session, or on a host
// attaching one, is read; tmuxGrace how long after a connect or an attach
// a read may not show the terminal's client yet.
const (
	tmuxWatchEvery = 30 * time.Second
	tmuxGrace      = 10 * time.Second
)

// tmuxMark is the tmux session a terminal is in: attached again by its ID
// while it is the same session (created when it was, if that is known),
// else by its name. at is when it was set or attached.
type tmuxMark struct {
	id, name string
	created  time.Time
	at       time.Time
}

func markOf(s session.TmuxSession, now time.Time) *tmuxMark {
	return &tmuxMark{id: s.ID, name: s.Name, created: s.Created, at: now}
}

// find is the session of ss the mark names.
func (k *tmuxMark) find(ss []session.TmuxSession) (session.TmuxSession, bool) {
	for _, s := range ss {
		if s.ID == k.id && (k.created.IsZero() || s.Created.Equal(k.created)) {
			return s, true
		}
	}
	for _, s := range ss {
		if k.name != "" && s.Name == k.name {
			return s, true
		}
	}
	return session.TmuxSession{}, false
}

// shellStart is what a terminal's shell is given as it starts. The
// session asks from its own goroutine, hence mu.
type shellStart struct {
	mu sync.Mutex
	// once is typed at the next start only
	once string
	// mark is the tmux session the terminal is in, nil for none
	mark *tmuxMark
	// host is the host's tmux session (Host.Tmux), "" when it has none or
	// the terminal was opened without it
	host string
	// dir is the folder the shell is taken to as it starts
	// (Host.TerminalDir), "" for its home
	dir string
	// ran is set once a command given once was typed: the folders the
	// terminal then reports are not the server's
	ran bool
	// cwd is the folder the terminal last reported, at its connect number
	// cwdConn; prevCwd the one before, of an earlier connect (noteCwd)
	cwd, prevCwd      string
	cwdConn, prevConn int
}

func (st *shellStart) setMark(k *tmuxMark) {
	st.mu.Lock()
	st.mark = k
	st.mu.Unlock()
}

// What a start could not do, for a toast.
const (
	tmuxNoteNone = iota
	tmuxNoteMissing
	tmuxNoteGone
	tmuxNoteFailed
)

// tmuxStartCmd is the command a starting shell is given, from the
// server's sessions as listed (or err), the session mark the terminal was
// in and host its host's: the mark's session when it is still there, else
// the host's. mark is what to keep, and note what went wrong.
func tmuxStartCmd(info session.TmuxInfo, err error, mark *tmuxMark, host string, now time.Time) (cmd string, keep *tmuxMark, note int) {
	switch {
	case errors.Is(err, session.ErrNoTmux):
		return "", nil, tmuxNoteMissing
	case err != nil:
		if host != "" {
			return session.TmuxNewOrAttachCommand(host), mark, tmuxNoteNone
		}
		return "", mark, tmuxNoteFailed
	}
	if mark != nil {
		if s, ok := mark.find(info.Sessions); ok {
			return session.TmuxAttachCommand(s.ID), markOf(s, now), tmuxNoteNone
		}
		if host == "" {
			return "", nil, tmuxNoteGone
		}
	}
	return session.TmuxNewOrAttachCommand(host), nil, tmuxNoteNone
}

// shellStartCmd is what c's shell is asked to run as it starts, on client.
func (a *App) shellStartCmd(c *conn) func(context.Context, *ssh.Client) string {
	st := c.start
	return func(ctx context.Context, client *ssh.Client) string {
		connects := c.s.Status().Connects
		st.mu.Lock()
		once, mark, host := st.once, st.mark, st.host
		st.once = ""
		if once != "" {
			st.ran = true
		}
		back, ran, dir := st.backDir(connects), st.ran, st.dir
		st.mu.Unlock()
		if once != "" {
			return once
		}
		cmd := ""
		if mark != nil || host != "" {
			cmd = a.tmuxStart(ctx, c, client, mark, host)
		}
		if returnDirWanted(a.returnDirOn.Load(), connects, back, cmd, ran) {
			cmd = returnDirOn(ctx, client, back, dir)
		}
		return cmd
	}
}

// tmuxStart is the tmux command c's starting shell is given on client:
// an attach to the session mark the terminal was in, else to the host's
// (created when there is none), "" for none. It says what it could not
// do.
func (a *App) tmuxStart(ctx context.Context, c *conn, client *ssh.Client, mark *tmuxMark, host string) string {
	st := c.start
	ctx, cancel := context.WithTimeout(ctx, tmuxTimeout)
	info, err := session.TmuxList(ctx, client, "")
	cancel()
	cmd, keep, note := tmuxStartCmd(info, err, mark, host, time.Now())
	st.mu.Lock()
	if st.mark == mark {
		st.mark = keep
	}
	st.mu.Unlock()
	if note != tmuxNoteNone {
		name := ""
		if mark != nil {
			name = mark.name
			if name == "" {
				name = mark.id
			}
		}
		a.post(func() { a.tmuxStartNote(c, note, name, err) })
	}
	return cmd
}

// tmuxStartNote says what a start of c's shell could not do: that its
// server has no tmux (once per host), that the session it was in is gone,
// or that it could not be read.
func (a *App) tmuxStartNote(c *conn, note int, name string, err error) {
	if note == tmuxNoteMissing {
		if a.tmuxWarned == nil {
			a.tmuxWarned = map[string]bool{}
		}
		if a.tmuxWarned[c.host.ID] {
			return
		}
		a.tmuxWarned[c.host.ID] = true
	}
	host := c.host.Label()
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
		switch note {
		case tmuxNoteMissing:
			return ui.Toast{Title: m.TmuxStartMissing(a.pv(host))}
		case tmuxNoteGone:
			return ui.Toast{Title: m.TmuxStartGone(a.pv(name))}
		}
		return ui.Toast{Title: m.TmuxStartFailed(err.Error()), Type: "error"}
	})
}

// tmuxSeen takes what a list read for c says of the session c is in: the
// one its client is attached to, or none once the server tells that it
// has no client, or once the session is gone.
func (a *App) tmuxSeen(c *conn, info session.TmuxInfo, now time.Time) {
	st := c.start
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if cl, ok := info.Mine(); ok {
		if s, ok := info.Session(cl.Session); ok {
			st.mark = markOf(s, now)
		}
		return
	}
	k := st.mark
	if k == nil || now.Sub(k.at) < tmuxGrace || now.Sub(c.tmuxUp) < tmuxGrace {
		return
	}
	if _, ok := k.find(info.Sessions); !ok || info.Tells {
		st.mark = nil
	}
}

// watchTmux reads which session the terminals in a tmux session, or on a
// host attaching one, are in, each every tmuxWatchEvery from its connect.
func (a *App) watchTmux(c *ui.Context) {
	now := c.Now()
	for _, cn := range a.conns {
		st := cn.start
		if st == nil || cn.s == nil {
			continue
		}
		if n := cn.s.Status().Connects; n != cn.tmuxConn {
			cn.tmuxConn, cn.tmuxUp, cn.tmuxAt = n, now, now
		}
		st.mu.Lock()
		on := st.mark != nil || st.host != ""
		st.mu.Unlock()
		client := cn.s.Client()
		tag, tagged := cn.s.Tag()
		if !on || cn.tmuxReading || client == nil || !tagged {
			continue
		}
		if wait := tmuxWatchEvery - now.Sub(cn.tmuxAt); wait > 0 {
			c.After(wait)
			continue
		}
		cn.tmuxReading, cn.tmuxAt = true, now
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
			info, err := session.TmuxList(ctx, client, tag)
			cancel()
			a.post(func() {
				cn.tmuxReading = false
				if err == nil && a.conns[cn.id] == cn {
					a.tmuxSeen(cn, info, time.Now())
				}
			})
		}()
	}
}

// openTerminal opens a terminal to a saved host in a new tab, prep
// preparing it before it starts.
func (a *App) openTerminal(hostID string, prep func(*conn)) (int, error) {
	c, err := a.makeConn(hostID, layout.Terminal, 0)
	if err != nil {
		return 0, err
	}
	if prep != nil {
		prep(c)
	}
	a.place(c, openAt{})
	return c.id, nil
}

// ConnectWithoutTmux opens a terminal to a saved host that does not
// attach to the host's tmux session.
func (a *App) ConnectWithoutTmux(hostID string) (int, error) {
	return a.openTerminal(hostID, func(c *conn) {
		if c.start != nil {
			c.start.host = ""
		}
	})
}
