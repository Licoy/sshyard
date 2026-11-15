package app

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/shellhist"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// The command history panel: the commands the shell kept on the focused
// session's host (or this computer, for a local shell), read from its
// history files, the newest first under the day they ran, each once.
// A command runs or is inserted in the focused terminal, is copied, or
// becomes a snippet. The panel reads the files again every historyEvery
// while it shows; nothing it reads is kept, but in memory for the
// terminals to complete from (sawHistory).

// historyEvery is how often the panel reads the files again; historyTimeout
// bounds one read; historyPage is how many commands the list shows before
// the button showing more.
const (
	historyEvery   = 30 * time.Second
	historyTimeout = 20 * time.Second
	historyPage    = 200
)

// historyState is what the panel read, of which host through which
// session, and how it shows it.
type historyState struct {
	host                string
	conn, connects, gen int
	running             bool
	at                  time.Time
	hist                shellhist.History
	read                bool
	err                 error
	// query and shell filter the list, "" shell for all; selected is the
	// command whose whole text shows
	query, shell, selected string
	limit                  int
	// tipOpen shows the sheet of the tip on bash's history
	tipOpen bool
	// inline shows each command's actions as buttons, the rows being wide
	// enough for them (rowActionsFit); else a More menu holds them.
	inline bool
}

// readLocalHistory reads this computer's history for the local shell sh;
// a variable so that tests can stand in.
var readLocalHistory = func(sh session.Shell) shellhist.History {
	home, _ := os.UserHomeDir()
	return shellhist.ReadLocal(shellhist.LocalCandidates(runtime.GOOS, home, os.Getenv), shellhist.LoginShell(sh.Path))
}

// historySource is the session through which the panel reads the history
// of cn's host: cn itself for a local shell, else a session on the server;
// with a note in place of the list when there is none.
func (a *App) historySource(m locales.Messages, cn *conn) (*conn, string) {
	switch {
	case cn == nil:
		return nil, m.HistoryNoSession()
	case isLocal(cn.host.ID):
		if strings.HasPrefix(a.shell(cn.host.ID).ID, "wsl:") {
			return nil, m.HistoryWSL()
		}
		return cn, ""
	case !cn.host.SSH():
		return nil, m.HistoryStream()
	}
	tc := a.sshConn(cn)
	switch {
	case tc == nil:
		return nil, m.HistoryNoTerminal()
	case tc.s.Status().State != session.Connected:
		return nil, m.MonitorNotConnected()
	}
	return tc, ""
}

// readHistory reads the history through src when it is time, or at once
// for now. Another host starts afresh; another session or a reconnect of
// the same host reads again at once, keeping what shows meanwhile.
func (a *App) readHistory(c *ui.Context, src *conn, now bool) {
	d := a.hist
	connects := 0
	if src.s != nil {
		connects = src.s.Status().Connects
	}
	if d.host != src.host.ID {
		d.gen++
		d.host, d.running = src.host.ID, false
		d.hist, d.read, d.err, d.selected, d.shell, d.limit = shellhist.History{}, false, nil, "", "", historyPage
		d.at = time.Time{}
	}
	if d.conn != src.id || d.connects != connects {
		d.conn, d.connects, d.at = src.id, connects, time.Time{}
	}
	if d.running {
		return
	}
	if wait := historyEvery - c.Now().Sub(d.at); !now && !d.at.IsZero() && wait > 0 {
		c.After(wait)
		return
	}
	local := isLocal(src.host.ID)
	var client *ssh.Client
	var sh session.Shell
	if local {
		sh = a.shell(src.host.ID)
	} else if client = src.s.Client(); client == nil {
		return
	}
	d.running, d.at = true, c.Now()
	gen := d.gen
	go func() {
		var h shellhist.History
		var err error
		if local {
			h = readLocalHistory(sh)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
			h, err = session.ReadHistory(ctx, client)
			cancel()
		}
		a.post(func() { a.historyRead(gen, h, err) })
	}()
}

// historyRead takes what the read of generation gen found, on the UI
// thread; one of a host no longer shown is dropped.
func (a *App) historyRead(gen int, h shellhist.History, err error) {
	d := a.hist
	if d.gen != gen {
		return
	}
	d.running, d.err = false, err
	if err != nil {
		return
	}
	d.hist, d.read = h, true
	a.sawHistory(d.host, h, d.at)
	if d.shell != "" && !h.Has(d.shell) {
		d.shell = ""
	}
}

// historyFilter is the entries of shell ("" any) holding every word of
// query, without regard to case.
func historyFilter(es []shellhist.Entry, shell, query string) []shellhist.Entry {
	words := strings.Fields(strings.ToLower(query))
	if shell == "" && len(words) == 0 {
		return es
	}
	var out []shellhist.Entry
	for _, e := range es {
		if shell != "" && e.Shell != shell {
			continue
		}
		cmd := strings.ToLower(e.Cmd)
		ok := true
		for _, w := range words {
			if !strings.Contains(cmd, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}

// historyDay heads the commands that ran on at's day, now being today.
func historyDay(m locales.Messages, at, now time.Time) string {
	if at.IsZero() {
		return m.HistoryUntimed()
	}
	at, now = at.Local(), now.Local()
	y, mo, d := at.Date()
	day := time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
	ny, nm, nd := now.Date()
	today := time.Date(ny, nm, nd, 0, 0, 0, 0, time.Local)
	switch {
	case day.Equal(today):
		return m.HistoryToday()
	case day.Equal(today.AddDate(0, 0, -1)):
		return m.HistoryYesterday()
	}
	return at.Format("2006-01-02")
}

// historySnippetName is the name a snippet made of cmd starts with: its
// first line, its spaces folded, cut to fit.
func historySnippetName(cmd string) string {
	name := strings.Join(strings.Fields(strings.SplitN(cmd, "\n", 2)[0]), " ")
	if r := []rune(name); len(r) > 40 {
		name = string(r[:39]) + "…"
	}
	return name
}

// runHistory writes cmd to the focused terminal, with Enter for enter.
func (a *App) runHistory(m locales.Messages, cmd string, enter bool) {
	if err := a.sendInput(func() (string, error) { return cmd, nil }, enter); err != nil {
		a.toastError(snippetErrorText(m, err))
	}
}

// copyHistory puts cmd on the clipboard.
func (a *App) copyHistory(cmd string) {
	a.svc.WriteClipboard(cmd)
	a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.Copied()} })
}

// historySnippet opens the sheet of a new snippet holding cmd.
func (a *App) historySnippet(m locales.Messages, cmd string) {
	a.openSnippet(m, assets.Snippet{Name: historySnippetName(cmd)}, false)
	if f := a.snippets.form; f != nil {
		f.content = cmd
	}
}

// historyMenu is what can be done with cmd; disabled when there is no
// terminal to write it to.
func (a *App) historyMenu(m locales.Messages, cmd string, disabled bool) []menuEntry {
	return []menuEntry{
		{Icon: icons.Send, Label: m.HistoryRun(), Disabled: disabled, Do: func() { a.runHistory(m, cmd, true) }},
		{Icon: icons.Paste, Label: m.HistoryInsert(), Disabled: disabled, Do: func() { a.runHistory(m, cmd, false) }},
		menuSep,
		{Icon: icons.Copy, Label: m.Copy(), Do: func() { a.copyHistory(cmd) }},
		{Icon: icons.API, Label: m.HistoryAddSnippet(), Do: func() { a.historySnippet(m, cmd) }},
	}
}

// history is the history pane's state, made on first use.
func (a *App) history() *historyState {
	if a.hist == nil {
		a.hist = &historyState{limit: historyPage}
	}
	return a.hist
}

// historyHead is what the history pane's header holds after its name,
// how many commands there are, and its actions: the read going on, the
// tip on bash and the button reading them again.
func (a *App) historyHead(c *ui.Context, m locales.Messages, tok theme.Tokens) (lead func(), acts []barAction) {
	d := a.history()
	src, _ := a.historySource(m, a.focused())
	lead = func() {
		if src != nil && d.read && len(d.hist.Entries) > 0 {
			ui.Text(c, strconv.Itoa(len(d.hist.Entries))).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(0)
		}
	}
	if src != nil && d.running {
		acts = append(acts, barAction{Key: "hist:reading", Show: func() ui.Element { return ui.Spinner(c) }})
	}
	if src != nil && d.read && historyTipShows(d.hist) {
		acts = append(acts, barAction{Key: "hist:tip", Button: IconButton{Icon: icons.Warning, Label: m.HistoryBashTipTitle(), Prominent: true, Size: Tiny},
			Do: func() { d.tipOpen = true }})
	}
	acts = append(acts, barAction{Key: "hist:refresh", Button: IconButton{Icon: icons.Refresh, Label: m.Refresh(), Disabled: src == nil || d.running, Size: Tiny},
		Do: func() {
			d.err = nil
			a.readHistory(c, src, true)
		}})
	return lead, acts
}

// historyPanel is the command history pane under its header: the search
// and the shells over the list, and at the foot the terminal commands go
// to.
func (a *App) historyPanel(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	a.history()
	src, note := a.historySource(m, a.focused())
	if src != nil {
		a.readHistory(c, src, false)
	}
	ui.Column(c.Key("history")).Fill().Children(func() {
		if src == nil {
			ui.Column(c.Key("hist:note")).Grow(1).Shrink(1).MinHeight(0).FillWidth().Padding(0, theme.Space(4)).Children(func() {
				panelNote(c, tok, note)
			})
		} else {
			a.historyBody(c, m, tok)
		}
		a.snippetFoot(c, m, tok)
	})
}

// historyBody is the search, the tip on bash, the shells' tabs and the
// commands matching.
func (a *App) historyBody(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	d := a.hist
	h := d.hist
	empty := d.read && len(h.Entries) == 0
	ui.Column(c).FillWidth().Shrink(0).Padding(0, theme.Space(3), theme.Space(2), theme.Space(3)).Gap(theme.Space(2)).Children(func() {
		if !empty {
			searchField(c, m, tok, "hist:search", &d.query, m.HistorySearch())
		}
		if d.err != nil {
			ui.Row(c.Key("hist:err")).FillWidth().Padding(theme.Space(2), theme.Space(2.5)).Gap(theme.Space(2)).Radius(theme.RadiusControl).
				Background(tok.Warning.Alpha(0.12)).Border(1, tok.Warning.Alpha(0.3)).Children(func() {
				ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(tok.Warning)
				ui.Text(c, m.HistoryFailed(a.pv(d.err.Error()))).FontSize(theme.Footnote).Grow(1).Shrink(1)
			})
		}
	})
	if len(h.Shells) > 1 {
		hits := historyFilter(h.Entries, "", d.query)
		counts := map[string]int{}
		for _, e := range hits {
			counts[e.Shell]++
		}
		ui.ScrollHorizontal(c.Key("hist:shells")).FillWidth().Shrink(0).Role(ui.RoleTabList).Label(m.HistoryFilterShell()).Children(func() {
			ui.Row(c).Gap(theme.Space(1.5)).Padding(0, theme.Space(3), theme.Space(2), theme.Space(3)).Children(func() {
				a.snippetTab(c, tok, "hist:all", m.HistoryAll(), len(hits), d.shell == "", func() { d.shell = "" })
				for _, sh := range h.Shells {
					a.snippetTab(c, tok, "hist:"+sh, sh, counts[sh], d.shell == sh, func() { d.shell = sh })
				}
			})
		})
	}
	ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	switch {
	case !d.read:
		ui.Column(c.Key("hist:wait")).Grow(1).Shrink(1).MinHeight(0).FillWidth().Padding(theme.Space(3), theme.Space(4)).Children(func() {
			if d.err == nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					panelNote(c, tok, m.HistoryReading())
				})
			}
		})
	case empty:
		ui.Column(c.Key("hist:intro")).Grow(1).Shrink(1).MinHeight(0).Clip().FillWidth().Padding(theme.Space(4), theme.Space(8)).Gap(theme.Space(3)).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			softTile(c, icons.History, tok.Accent, 44)
			ui.Column(c).FillWidth().Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.HistoryNoneTitle()).FontSize(theme.Title3).FontWeight(600).TextAlign(ui.Center)
				ui.Text(c, m.HistoryNoneHint()).FontSize(theme.Footnote).TextColor(tok.TextMuted).TextAlign(ui.Center)
			})
		})
	default:
		a.historyList(c, m, tok, historyFilter(h.Entries, d.shell, d.query))
	}
	a.historyTip(c, m, tok)
}

// historyTipShows reports whether the tip on bash's history applies: the
// user's shell is bash (or unknown, with bash's history alone), whose
// history keeps no times.
func historyTipShows(h shellhist.History) bool {
	bash := h.Login == shellhist.Bash || h.Login == "" && len(h.Shells) == 1
	return bash && h.Has(shellhist.Bash) && !h.Timed(shellhist.Bash)
}

// historyTipLines are what the tip on bash suggests for ~/.bashrc.
const historyTipLines = "export HISTTIMEFORMAT='%F %T '\nPROMPT_COMMAND=\"history -a${PROMPT_COMMAND:+; $PROMPT_COMMAND}\""

// historyTip is the sheet saying why bash's history shows neither times
// nor the commands of the open shells, and what makes it.
func (a *App) historyTip(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	d := a.hist
	if !d.tipOpen {
		return
	}
	sheet(ui.Modal(c, &d.tipOpen, func() {
		ui.Column(c).Width(440).Gap(theme.Space(3)).Children(func() {
			ui.Row(c).Gap(theme.Space(2.5)).AlignItems(ui.Center).Children(func() {
				softTile(c, icons.Warning, tok.Warning, 28)
				sheetTitle(c, m.HistoryBashTipTitle())
			})
			ui.Text(c, m.HistoryBashTip()).FontSize(theme.Body).TextColor(tok.TextMuted)
			ui.Scroll(c.Key("hist:tip:code")).FillWidth().Radius(theme.RadiusSmall).Background(a.contentSurfaceFill(tok.Inset)).Border(1, tok.Line).Children(func() {
				ui.Column(c).FillWidth().Padding(theme.Space(2.5), theme.Space(3)).Children(func() {
					ui.Text(c, historyTipLines).Font(a.termFamily()).FontSize(theme.Footnote)
				})
			})
			ui.Row(c).Gap(theme.Space(2)).Children(func() {
				ui.Spacer(c)
				if pill(c, m.HistoryTipCopy(), false, false, false) {
					a.copyHistory(historyTipLines)
				}
				if pill(c, m.Close(), true, false, false) {
					d.tipOpen = false
				}
			})
		})
	}), tok)
}

// historyList is the commands hits, under the day they ran, as many as the
// list shows so far.
func (a *App) historyList(c *ui.Context, m locales.Messages, tok theme.Tokens, hits []shellhist.Entry) {
	d := a.hist
	disabled := a.snippetTarget() == nil || a.snippetTarget().tv.snippetSending
	gutterScroll(c, ui.Scroll(c.Key("hist:list")).Grow(1).Shrink(1).MinHeight(0).FillWidth()).Children(func() {
		ui.Column(c).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(0.5)).Children(func() {
			now, last := c.Now(), "\x00"
			for i, e := range hits {
				if i >= d.limit {
					ui.Row(c.Key("hist:more")).FillWidth().Padding(theme.Space(2)).Justify(ui.Center).Children(func() {
						if pill(c, m.HistoryShowMore(len(hits)-d.limit), false, false, false) {
							d.limit += historyPage
						}
					})
					break
				}
				if day := historyDay(m, e.At, now); day != last {
					a.historySection(c, tok, day, last == "\x00")
					last = day
				}
				a.historyRow(c, m, tok, i, e, disabled)
			}
			if len(hits) == 0 {
				ui.Column(c).FillWidth().Padding(theme.Space(8), theme.Space(4)).AlignItems(ui.Center).Children(func() {
					panelNote(c, tok, m.HistoryNoMatch())
				})
			}
		})
	})
}

// historySection heads the commands of a day in the list.
func (a *App) historySection(c *ui.Context, tok theme.Tokens, day string, first bool) {
	top := theme.Space(3)
	if first {
		top = theme.Space(1)
	}
	ui.Row(c.Key("hist:sec:"+day)).FillWidth().Padding(top, theme.Space(2), theme.Space(1), theme.Space(2)).Gap(theme.Space(1.5)).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icons.History.SVG).Size(12, 12).Shrink(0).TextColor(tok.TextMuted)
		ui.Text(c, day).FontSize(theme.Caption).FontWeight(600).TextColor(tok.TextMuted).Shrink(1).SingleLine()
	})
}

// historyMeta is the line under a command: when it ran, its shell, how
// many times it ran and how many lines it has.
func historyMeta(m locales.Messages, e shellhist.Entry) string {
	var parts []string
	if !e.At.IsZero() {
		parts = append(parts, e.At.Local().Format("15:04"))
	}
	parts = append(parts, e.Shell)
	if e.Count > 1 {
		parts = append(parts, m.HistoryRuns(e.Count))
	}
	if n := strings.Count(e.Cmd, "\n") + 1; n > 1 {
		parts = append(parts, m.HistoryLines(n))
	}
	return strings.Join(parts, " · ")
}

// historyRow is a command: its first line and what is known of it, with
// the button running it and, as wide as the panel allows, an IconButton
// for each of its other actions or a More menu of them, which a right
// click lists too. Chosen, it opens on its whole text.
func (a *App) historyRow(c *ui.Context, m locales.Messages, tok theme.Tokens, i int, e shellhist.Entry, disabled bool) {
	d := a.hist
	key := strconv.Itoa(i)
	sel := d.selected == e.Cmd
	fam := a.termFamily()
	more := "hist:more:" + key
	box := ui.Column(c.Key("hist:row:" + key)).FillWidth().Radius(theme.RadiusControl)
	if fit := rowActionsFit(box.Bounds().W, theme.Space(1)+theme.Space(4), 4); fit != d.inline {
		d.inline = fit
		c.Invalidate()
	}
	switch {
	case sel:
		box.Background(tok.Accent.Alpha(accentWash(tok)))
	case box.Hovered() || a.menu.key == more:
		box.Background(tok.Hover)
	}
	box.Children(func() {
		ui.Row(c).FillWidth().Padding(0, theme.Space(1), 0, 0).Gap(theme.Space(0.5)).AlignItems(ui.Center).Children(func() {
			row := ui.ButtonBase(c.Key("hist:select:"+key)).Label(m.HistoryShowWhole()).Selected(sel).Grow(1).Shrink(1).MinWidth(0).
				Padding(theme.Space(1.5), theme.Space(2)).Gap(theme.Space(0.5)).AlignItems(ui.Start).Justify(ui.Start).Cursor(ui.CursorPointer)
			row.Background(tok.Accent.Alpha(0)).TextColor(tok.Text)
			row.Children(func() {
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(theme.Space(0.5)).Children(func() {
					if !sel {
						ui.Text(c, a.pv(strings.SplitN(e.Cmd, "\n", 2)[0])).Font(fam).FontSize(theme.Footnote).SingleLine()
					}
					ui.Text(c, historyMeta(m, e)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
				})
			})
			if row.Clicked() {
				if sel {
					d.selected = ""
				} else {
					d.selected = e.Cmd
				}
			}
			if (IconButton{Icon: icons.Send, Label: m.HistoryRun(), Disabled: disabled, Size: Small}).Clicked(c, "hist:run:"+key) {
				a.runHistory(m, e.Cmd, true)
			}
			if !d.inline {
				a.menuButton(c, more, IconButton{Icon: icons.More, Label: m.MoreActions(), Size: Small}, func() []menuEntry { return a.historyMenu(m, e.Cmd, disabled) })
				return
			}
			if (IconButton{Icon: icons.Paste, Label: m.HistoryInsert(), Disabled: disabled, Size: Small}).Clicked(c, "hist:insert:"+key) {
				a.runHistory(m, e.Cmd, false)
			}
			if (IconButton{Icon: icons.Copy, Label: m.Copy(), Size: Small}).Clicked(c, "hist:copy:"+key) {
				a.copyHistory(e.Cmd)
			}
			if (IconButton{Icon: icons.API, Label: m.HistoryAddSnippet(), Size: Small}).Clicked(c, "hist:snippet:"+key) {
				a.historySnippet(m, e.Cmd)
			}
		})
		if !sel {
			return
		}
		ui.Column(c.Key("hist:detail")).FillWidth().Padding(0, theme.Space(2), theme.Space(2), theme.Space(2)).Children(func() {
			ui.Scroll(c.Key("hist:body")).FillWidth().MaxHeight(theme.Space(40)).Radius(theme.RadiusSmall).Background(a.contentSurfaceFill(tok.Inset)).Border(1, tok.Line).Children(func() {
				ui.Column(c).FillWidth().Padding(theme.Space(2), theme.Space(2.5)).Children(func() {
					ui.Text(c, a.pv(e.Cmd)).Font(fam).FontSize(theme.Footnote)
				})
			})
		})
	})
	a.contextMenu(box, func() []menuEntry { return a.historyMenu(m, e.Cmd, disabled) })
}
