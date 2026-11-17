package app

import (
	"cmp"
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/cmdline"
	"github.com/Licoy/sshyard/internal/fileicons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/shellhist"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// What the terminals complete from (see termSuggest), by host: the lines
// run at its prompts since the app started, and the commands its shell's
// history files hold, read as a terminal on it first completes and again
// at most every suggestEvery once more ran; and the folders of the paths
// typed, read on the pane's connection as its list asks, kept for
// suggestDirFor. All of it in memory only, none of it written anywhere.

const (
	suggestEvery   = 2 * time.Minute
	suggestDirFor  = 10 * time.Second
	suggestTimeout = 10 * time.Second
	suggestRan     = 500  // the lines run kept for a host
	suggestFiles   = 5000 // the commands of its history files kept
	suggestDirs    = 64   // the folders kept
	pathListHeight = 240  // the path list's height at most
)

// suggestState is what the terminals complete from, by host ID; gen
// changes as it does, and used counts the frames a host was completed on.
type suggestState struct {
	hosts map[string]*suggestHost
	gen   int
	used  int
}

// suggestHost is what a host's terminals complete from.
type suggestHost struct {
	id string
	// ran are the lines run, files the commands of the history files,
	// the newest first
	ran, files []string
	// reading is set while the files are read, at when they last were;
	// stale once a line ran since
	reading bool
	at      time.Time
	stale   bool
	// used is when a terminal on it last completed (suggestState.used)
	used int
	// home is the folder ~ is, homeAsked set once it was asked for;
	// noSFTP a server whose folders cannot be read
	home      string
	homeAsked bool
	noSFTP    bool
	dirs      map[string]*suggestDir
}

// suggestDir is a folder read for the path list; at is zero until the
// first read ends.
type suggestDir struct {
	es      []cmdline.Entry
	reading bool
	at      time.Time
}

// errNoSFTP is a server without SFTP, whose folders are not listed.
var errNoSFTP = errors.New("no sftp")

func (a *App) suggests() *suggestState {
	if a.sug == nil {
		a.sug = &suggestState{hosts: map[string]*suggestHost{}}
	}
	return a.sug
}

func (st *suggestState) host(id string) *suggestHost {
	h := st.hosts[id]
	if h == nil {
		h = &suggestHost{id: id, dirs: map[string]*suggestDir{}}
		st.hosts[id] = h
	}
	return h
}

// historyCmds are the commands of h that typing can complete to, the
// newest first, each once.
func historyCmds(h shellhist.History) []string {
	out := make([]string, 0, min(len(h.Entries), suggestFiles))
	seen := map[string]bool{}
	for _, e := range h.Entries {
		cmd := strings.TrimRight(e.Cmd, " ")
		if cmd == "" || seen[cmd] || !cmdline.Typeable(cmd) {
			continue
		}
		seen[cmd] = true
		if out = append(out, cmd); len(out) == suggestFiles {
			break
		}
	}
	return out
}

// sawHistory takes the history the panel read of host, so that its
// terminals need not read it again.
func (a *App) sawHistory(host string, h shellhist.History, at time.Time) {
	if a.settings.TerminalCompletion == settings.CompletionOff {
		return
	}
	st := a.suggests()
	sh := st.host(host)
	sh.files, sh.at, sh.stale = historyCmds(h), at, false
	st.gen++
}

// canSuggest reports whether the terminal pane cn completes: a shell's,
// SSH or local, connected, typed into alone.
func (a *App) canSuggest(cn *conn) bool {
	return cn.kind == layout.Terminal && cn.s != nil && cn.tv != nil && !isStream(cn.host) &&
		cn.s.Status().State == session.Connected && a.fanoutFrom(cn) == nil
}

// canListPaths reports whether the folders of cn's paths can be listed:
// on this computer, but for Windows' shells; on a server, with SFTP; not
// in a container.
func (a *App) canListPaths(cn *conn) bool {
	switch {
	case cn.container:
		return false
	case isLocal(cn.host.ID):
		return runtime.GOOS != "windows"
	}
	return !a.suggests().host(cn.host.ID).noSFTP
}

// suggestCwd is the folder cn's shell is in on its host, "" when it is not
// known: as followCwd took it, unless the shell last reported a folder it
// did not take, of another host the shell went on to.
func (a *App) suggestCwd(cn *conn) string {
	if d := followDir(cn.s.Term.Cwd()); d != "" && d != cn.cwd {
		return ""
	}
	return cn.cwd
}

// askPathList opens the path list of the focused terminal, as its
// shortcut does.
func (a *App) askPathList() {
	if cn := a.focused(); a.page == pageWorkspace && cn != nil && cn.kind == layout.Terminal && a.settings.TerminalCompletion != settings.CompletionOff {
		a.listAsk, a.focusReq = cn.id, cn.id
	}
}

// suggestFrame readies the completion of the terminal pane cn for this
// frame, and shows its path list.
func (a *App) suggestFrame(c *ui.Context, cn *conn, focused bool, tb ui.Rect, m locales.Messages) {
	tv := cn.tv
	s := &tv.suggest
	// Asked for from the palette, the pane takes the focus back first.
	ask := a.listAsk == cn.id && focused
	if ask || a.listAsk == cn.id && a.focused() != cn {
		a.listAsk = 0
	}
	s.mode = a.settings.TerminalCompletion
	s.on = s.mode != settings.CompletionOff && a.canSuggest(cn)
	if !s.on || !focused || a.page != pageWorkspace {
		s.src = nil
		s.clear()
		return
	}
	st := a.suggests()
	if h := st.host(cn.host.ID); h.used != st.used || st.used == 0 {
		st.used++
		h.used = st.used
	}
	s.src = connSuggest{a: a, cn: cn}
	s.lists, s.gen = a.canListPaths(cn), st.gen
	t := cn.s.Term
	if ask {
		s.capture(t)
	}
	s.refresh(t)
	s.listPaths(ask)
	if s.list.shows() {
		a.pathListView(c, cn, tb, m)
	}
}

// readSuggestHistory reads the history files of cn's host, when it is
// time.
func (a *App) readSuggestHistory(cn *conn, now time.Time) {
	st := a.suggests()
	h := st.host(cn.host.ID)
	if h.reading || !h.at.IsZero() && (!h.stale || now.Sub(h.at) < suggestEvery) {
		return
	}
	local := isLocal(cn.host.ID)
	var client *ssh.Client
	var sh session.Shell
	if local {
		if sh = a.shell(cn.host.ID); strings.HasPrefix(sh.ID, "wsl:") {
			return
		}
	} else if cn.s == nil {
		return
	} else if client = cn.s.Client(); client == nil {
		return
	}
	h.reading, h.at, h.stale = true, now, false
	go func() {
		var hist shellhist.History
		var err error
		if local {
			hist = readLocalHistory(sh)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
			hist, err = session.ReadHistory(ctx, client)
			cancel()
		}
		cmds := historyCmds(hist)
		a.post(func() {
			h.reading = false
			if err == nil {
				h.files = cmds
				st.gen++
			}
		})
	}()
}

// connSuggest is what the terminal pane cn completes from.
type connSuggest struct {
	a  *App
	cn *conn
}

// suggest takes the commands of cn's host first, its lines run before
// its history files', then those of the other hosts, the last completed
// on first. The history files are read once something is typed, so a
// terminal no one types in never runs a command for them.
func (s connSuggest) suggest(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	s.a.readSuggestHistory(s.cn, time.Now())
	st := s.a.suggests()
	own := st.host(s.cn.host.ID)
	others := make([]*suggestHost, 0, len(st.hosts))
	for _, h := range st.hosts {
		if h != own {
			others = append(others, h)
		}
	}
	slices.SortFunc(others, func(x, y *suggestHost) int {
		return cmp.Or(cmp.Compare(y.used, x.used), strings.Compare(x.id, y.id))
	})
	srcs := [][]string{own.ran, own.files}
	for _, h := range others {
		srcs = append(srcs, h.ran, h.files)
	}
	return cmdline.Suggest(input, srcs...)
}

// ran keeps the line run, but one starting with a blank, which shells
// leave out of their history; the folders read may have changed.
func (s connSuggest) ran(line string) {
	line = strings.TrimRight(line, " ")
	if line == "" || line[0] == ' ' || !cmdline.Typeable(line) {
		return
	}
	st := s.a.suggests()
	h := st.host(s.cn.host.ID)
	if i := slices.Index(h.ran, line); i >= 0 {
		h.ran = slices.Delete(h.ran, i, i+1)
	}
	h.ran = slices.Insert(h.ran, 0, line)
	if len(h.ran) > suggestRan {
		h.ran = h.ran[:suggestRan]
	}
	h.stale = true
	clear(h.dirs)
	st.gen++
}

// entries reads the folder of word from the folder the shell is in, or
// its host's home.
func (s connSuggest) entries(word string) ([]cmdline.Entry, string, listState) {
	a, cn := s.a, s.cn
	h := a.suggests().host(cn.host.ID)
	local := isLocal(cn.host.ID)
	if local && h.home == "" {
		h.home, _ = os.UserHomeDir()
	}
	cwd := a.suggestCwd(cn)
	dir, base, ok := cmdline.Dir(word, cwd, h.home)
	if !ok {
		switch {
		case local:
		case strings.HasPrefix(word, "~/") && h.home == "":
			if !h.homeAsked && a.readSuggestHome(cn, h) {
				return nil, "", listReading
			}
		case cwd == "" && !strings.HasPrefix(word, "/") && !strings.HasPrefix(word, "~"):
			if cn.s.Term.Cwd() == "" && a.probeCwd(cn, time.Now()) > 0 {
				return nil, "", listReading
			}
		}
		return nil, "", listNone
	}
	d := h.dirs[dir]
	if d == nil || !d.reading && time.Since(d.at) > suggestDirFor {
		if !a.readSuggestDir(cn, h, dir) {
			return nil, "", listNone
		}
		d = h.dirs[dir]
	}
	if d.at.IsZero() {
		return nil, base, listReading
	}
	return d.es, base, listRead
}

// readSuggestHome asks cn's server for the home folder; it reports
// whether it is asking.
func (a *App) readSuggestHome(cn *conn, h *suggestHost) bool {
	client := cn.s.Client()
	if client == nil {
		return false
	}
	h.homeAsked = true
	go func() {
		home, err := withSFTP(client, func(ctx context.Context, cl *sftpsvc.Client) (string, error) { return cl.Home() })
		a.post(func() {
			if errors.Is(err, errNoSFTP) {
				h.noSFTP = true
			}
			if err == nil && strings.HasPrefix(home, "/") {
				h.home = home
			}
			a.suggests().gen++
		})
	}()
	return true
}

// readSuggestDir reads the folder dir of cn's host, on its connection or
// on this computer; it reports whether it is reading it.
func (a *App) readSuggestDir(cn *conn, h *suggestHost, dir string) bool {
	var read func() ([]sftpsvc.Entry, error)
	if isLocal(cn.host.ID) {
		read = func() ([]sftpsvc.Entry, error) { return sftpsvc.ListLocal(dir) }
	} else {
		client := cn.s.Client()
		if client == nil || h.noSFTP {
			return false
		}
		read = func() ([]sftpsvc.Entry, error) {
			return withSFTP(client, func(ctx context.Context, cl *sftpsvc.Client) ([]sftpsvc.Entry, error) { return cl.Peek(ctx, dir) })
		}
	}
	d := h.dirs[dir]
	if d == nil {
		if len(h.dirs) >= suggestDirs {
			clear(h.dirs)
		}
		d = &suggestDir{}
		h.dirs[dir] = d
	}
	d.reading = true
	go func() {
		fes, err := read()
		es := make([]cmdline.Entry, 0, len(fes))
		for _, e := range fes {
			es = append(es, cmdline.Entry{Name: e.Name, Dir: e.IsDir})
		}
		a.post(func() {
			d.reading = false
			if errors.Is(err, errNoSFTP) {
				h.noSFTP = true
			}
			if h.dirs[dir] != d {
				return // forgotten meanwhile: a line ran
			}
			d.es, d.at = es, time.Now()
			a.suggests().gen++
		})
	}()
	return true
}

// withSFTP runs do on an SFTP session of its own on client, closed after
// or after suggestTimeout, whichever is first.
func withSFTP[T any](client *ssh.Client, do func(context.Context, *sftpsvc.Client) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
	defer cancel()
	cl, err := sftpsvc.NewContext(ctx, client)
	if err != nil {
		return zero, err
	}
	defer cl.Close()
	if cl.SCP() {
		return zero, errNoSFTP
	}
	stop := context.AfterFunc(ctx, func() { cl.Close() })
	defer stop()
	return do(ctx, cl)
}

// pathListView is the path list of the terminal pane cn, whose box is tb,
// below its cursor (above when there is no room below): the entries
// matching what is typed, each with its file icon, the one chosen lit,
// or why there are none. A click types one; the terminal keeps the
// keyboard.
func (a *App) pathListView(c *ui.Context, cn *conn, tb ui.Rect, m locales.Messages) {
	tv := cn.tv
	l := &tv.suggest.list
	tok := currentTokens(c)
	cr := tv.caret()
	key := "pathlist:" + strconv.Itoa(cn.id)
	reveal := l.reveal
	l.reveal = false
	ui.Overlay(c, func() {
		at := ui.Box(c.Key(key+":at")).Absolute().Left(tb.X+cr.X).Top(tb.Y+cr.Y).Size(cr.W, cr.H).PassThrough()
		panel := ui.Column(c.Key(key)).AttachTo(at, ui.AnchorBottomStart, ui.AnchorTopStart).
			MinWidth(200).MaxWidth(360).Padding(4).Radius(theme.RadiusCard).
			Background(tok.Inset).Border(1, tok.GlassEdge).KeepFocus().Role(ui.RoleList).Label(m.CompletionList())
		tok.Lift.On(panel)
		panel.Children(func() {
			if len(l.shown) == 0 {
				note := m.CompletionNoMatches()
				switch l.state {
				case listReading:
					note = m.Loading()
				case listNone:
					note = m.CompletionNoFolder()
				}
				ui.Text(c, note).FontSize(theme.Footnote).TextColor(tok.TextMuted).Padding(6, 8).SingleLine()
				return
			}
			ui.Scroll(c.Key("scroll")).MaxHeight(pathListHeight).FillWidth().Children(func() {
				for i, e := range l.shown {
					row := ui.Row(c.Key("pe:"+e.Name)).FillWidth().AlignItems(ui.Center).Gap(theme.Space(2)).
						Padding(4, 8).Radius(theme.RadiusSmall).Cursor(ui.CursorPointer).Role(ui.RoleListItem).Selected(i == l.sel)
					switch {
					case i == l.sel:
						row.Background(tok.Accent.Alpha(0.12))
						if reveal {
							row.ScrollIntoView()
						}
					case row.Hovered():
						row.Background(tok.Hover)
					}
					if row.Clicked() {
						tv.acceptPath(i)
						a.focusReq = cn.id
					}
					row.Children(func() {
						fileImage(c, fileicons.Lookup(e.Name, e.Dir), 16)
						name := shownName(e.Name)
						if e.Dir {
							name += "/"
						}
						ui.Text(c, name).FontSize(theme.Body).Grow(1).Shrink(1).MinWidth(0).SingleLine()
					})
				}
			})
		})
	})
}
