package app

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// userAfterEnter is how soon after an Enter typed into an assistant's
// linked terminal it is read again who runs it: su and sudo -i may ask
// for a password first, whose Enter reads it again.
const userAfterEnter = 600 * time.Millisecond

// userWait bounds one read of who runs a terminal.
const userWait = 5 * time.Second

// termUser is who runs an SSH host assistant's linked terminal, as last
// read from its server: of the session of and its connect conn, ok once
// read. Off the UI thread only root is read, by the gate.
type termUser struct {
	u    session.TermUser
	ok   bool
	of   *session.Session
	conn int
	// enters is the terminal's Enters when last read, due when it is to
	// be read again (zero for not), busy while a read runs, miss the
	// reads in a row that found no shell: after probeTries of them, as
	// on a server without /proc, none is set and it is not read again on
	// this connect.
	enters uint64
	due    time.Time
	busy   bool
	miss   int
	// seen is the user whose banner was dismissed.
	seen string
	root atomic.Bool
	none atomic.Bool
}

// missed counts a read that found no shell.
func (st *termUser) missed() {
	st.miss++
	st.none.Store(st.miss >= probeTries)
}

// userName names u's foreground user for the model and the banner.
func userName(u session.TermUser) string {
	if u.Name != "" {
		return u.Name
	}
	return "uid " + strconv.Itoa(u.UID)
}

// loginName names the user cn's host logs in as.
func loginName(cn *conn) string {
	switch {
	case cn.ai.user.ok && cn.ai.user.u.Login != "":
		return cn.ai.user.u.Login
	case cn.host.User != "":
		return cn.host.User
	case cn.ai.user.ok:
		return "uid " + strconv.Itoa(cn.ai.user.u.LoginUID)
	}
	return ""
}

// switchedTo is who cn's linked terminal was switched to with su or
// sudo -i, "" while it runs as the login user or is not known.
func switchedTo(cn *conn) string {
	if !cn.ai.user.ok || !cn.ai.user.u.Switched() {
		return ""
	}
	return userName(cn.ai.user.u)
}

// userTerminal is the session of the terminal the built-in assistant of
// SSH host pane cn is linked to, connected, else nil.
func (a *App) userTerminal(cn *conn) *session.Session {
	if cn.ai == nil || cn.ai.agent != "builtin" || !hasSSH(cn.host) {
		return nil
	}
	t := a.conns[cn.ai.link]
	if t == nil || t.kind != layout.Terminal || t.s == nil || t.s.Status().State != session.Connected {
		return nil
	}
	return t.s
}

// watchTermUser reads who runs the linked terminal of AI pane cn: once it
// is linked, and userAfterEnter after each Enter typed into it. It returns
// how soon it wants a frame again, 0 for never.
func (a *App) watchTermUser(cn *conn, now time.Time) time.Duration {
	s := a.userTerminal(cn)
	if s == nil {
		return 0
	}
	st := &cn.ai.user
	connects := s.Status().Connects
	if st.of != s || st.conn != connects {
		st.u, st.ok, st.of, st.conn, st.miss = session.TermUser{}, false, s, connects, 0
		st.enters, st.due = s.Enters(), now
		st.root.Store(false)
		st.none.Store(false)
	}
	if st.busy || st.none.Load() {
		return 0
	}
	if n := s.Enters(); n != st.enters {
		st.enters, st.due = n, now.Add(userAfterEnter)
	}
	switch {
	case st.due.IsZero():
		return 0
	case now.Before(st.due):
		return st.due.Sub(now)
	}
	st.due, st.busy = time.Time{}, true
	state := cn.ai
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), userWait)
		defer cancel()
		u, err := s.ProbeUser(ctx)
		a.post(func() { a.noteTermUser(state, s, connects, u, err) })
	}()
	return 0
}

// noteTermUser keeps what a read of who runs terminal s found, for the
// pane state is, unless the pane has since been linked elsewhere.
func (a *App) noteTermUser(state *aiState, s *session.Session, connects int, u session.TermUser, err error) {
	st := &state.user
	if st.of != s || st.conn != connects {
		return
	}
	st.busy = false
	switch {
	case err == nil:
		st.u, st.ok, st.miss = u, true, 0
		st.root.Store(u.Root())
	case errors.Is(err, session.ErrNoUser):
		st.missed()
	}
}

// freshTermUser is the terminal to read who runs it again before a
// question to pane cn is sent: one whose last Enter was not read yet.
func (a *App) freshTermUser(cn *conn) *session.Session {
	s := a.userTerminal(cn)
	st := &cn.ai.user
	if s == nil || st.of != s || st.none.Load() || (s.Enters() == st.enters && st.due.IsZero() && st.ok) {
		return nil
	}
	return s
}

// readTermUser reads who runs s off the UI thread, for a turn about to
// start, and keeps it as watchTermUser would; switched is who s was
// switched to, as switchedTo, or fallback when it cannot be read.
func (a *App) readTermUser(ctx context.Context, state *aiState, s *session.Session, fallback string) (switched string) {
	if state.user.none.Load() {
		return fallback
	}
	ctx, cancel := context.WithTimeout(ctx, userWait)
	defer cancel()
	u, err := s.ProbeUser(ctx)
	if errors.Is(err, session.ErrNoUser) {
		a.post(func() {
			if st := &state.user; st.of == s {
				st.missed()
			}
		})
	}
	if err != nil {
		return fallback
	}
	state.user.root.Store(u.Root())
	enters := s.Enters()
	a.post(func() {
		st := &state.user
		if st.of != s {
			return
		}
		st.u, st.ok, st.miss = u, true, 0
		if st.enters == enters {
			st.due = time.Time{}
		}
	})
	if !u.Switched() {
		return ""
	}
	return userName(u)
}

// termUserBanner says that the linked terminal of pane cn was switched to
// another user while the assistant still runs as the login user, with a
// button to type into the terminal instead.
func (a *App) termUserBanner(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	user := switchedTo(cn)
	if user == "" || cn.ai.inTerm || cn.ai.user.seen == user || a.userTerminal(cn) == nil {
		return
	}
	id := cn.id
	aiBannerWith(c.Key("userbar"), tok.Warning, icons.Warning, m.AiTermUserBar(user, loginName(cn)), func() {
		if (IconButton{Icon: icons.Terminal, Label: m.AiRunInTerminal(), Size: Tiny, Disabled: cn.ai.running}).Clicked(c, "interm") {
			a.SetAITerminal(id, true)
		}
	}, func() { cn.ai.user.seen = user })
}

// SetAITerminal has the assistant of SSH host pane id type into its
// linked terminal (on), as whoever runs it, or run on channels of its own
// as the login user; the conversation goes on with the new tools from
// the next question.
func (a *App) SetAITerminal(id int, on bool) {
	c := a.conns[id]
	if c == nil || c.ai == nil || c.ai.running || !hasSSH(c.host) {
		return
	}
	c.ai.inTerm = on
}
