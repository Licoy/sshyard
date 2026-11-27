package app

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/termlog"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The session logs pane lists the terminals: it starts and stops each
// one's log, a file of what it printed from then on, and saves what a
// terminal holds already; then the logs written last. A log starts on its
// own only for a host set to log its terminals (Host.SessionLog), as one
// connects.

var errLogNoTerminal = errors.New("log: not a terminal pane")

// recentMax is how many logs the pane lists as written last.
const recentMax = 12

// logsState is the session logs pane's: the logs written last, as last
// listed from dir.
type logsState struct {
	recent  []recentLog
	dir     string
	listing bool
}

// recentLog is a log file of the folder.
type recentLog struct {
	Path, Host string
	Size       int64
	Mod        time.Time
}

// recentLogs are the n logs of dir written last, the last first: those of
// its hosts' folders.
func recentLogs(dir string, n int) []recentLog {
	var out []recentLog
	hosts, _ := os.ReadDir(dir)
	for _, h := range hosts {
		if !h.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(dir, h.Name()))
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".log" {
				continue
			}
			if info, err := f.Info(); err == nil {
				out = append(out, recentLog{Path: filepath.Join(dir, h.Name(), f.Name()), Host: h.Name(), Size: info.Size(), Mod: info.ModTime()})
			}
		}
	}
	slices.SortFunc(out, func(a, b recentLog) int { return b.Mod.Compare(a.Mod) })
	return out[:min(n, len(out))]
}

// listLogs lists the logs written last again, in the background.
func (a *App) listLogs() {
	if a.logs.listing {
		return
	}
	a.logs.listing = true
	dir := a.logDir()
	go func() {
		recent := recentLogs(dir, recentMax)
		a.post(func() {
			a.logs.recent, a.logs.dir, a.logs.listing = recent, dir, false
		})
	}()
}

// logDir is the folder the session logs go to, a folder of each host's
// in it.
func (a *App) logDir() string {
	if d := a.settings.SessionLog.Dir; d != "" {
		return d
	}
	return filepath.Join(a.localHome(), "SSHYard", "logs")
}

// startLog starts the log of terminal pane c, from what it prints next.
func (a *App) startLog(c *conn) error {
	if c == nil || c.kind != layout.Terminal || c.s == nil {
		return errLogNoTerminal
	}
	if c.log != nil {
		return nil
	}
	h := a.liveHost(c.host)
	st := a.settings.SessionLog
	r, err := termlog.Start(a.logDir(), termlog.Header{Name: h.Label(), Target: h.Target()}, termlog.Options{Raw: st.Raw, Stamps: st.Stamps}, time.Now(), a.invalidate)
	if err != nil {
		return err
	}
	c.log, c.logConns = r, c.s.Status().Connects
	if st.Raw {
		c.s.SetOutputTap(r.Raw)
	} else {
		c.s.Term.SetLineHook(r.Line)
	}
	return nil
}

// watchLogs marks in each log where its session connected again, and
// starts the log of a terminal of a host set to log them as it connects,
// once.
func (a *App) watchLogs(m locales.Messages) {
	for _, c := range a.conns {
		if c.kind != layout.Terminal || c.s == nil {
			continue
		}
		st := c.s.Status()
		if c.log != nil && st.Connects != c.logConns {
			if c.logConns > 0 {
				c.log.Mark(m.LogReconnected(), time.Now())
			}
			c.logConns = st.Connects
		}
		if c.log == nil && !c.autoLog && st.State == session.Connected && a.liveHost(c.host).SessionLog {
			c.autoLog = true
			if err := a.startLog(c); err != nil {
				a.logToast(err)
			}
		}
	}
}

// setHostLog sets whether the terminals of host id are logged as they
// connect.
func (a *App) setHostLog(id string, on bool) {
	h, ok := a.store.Get(id)
	if !ok {
		return
	}
	h.SessionLog = on
	if _, err := a.store.Update(h); err != nil {
		a.logToast(err)
	}
}

// setLogFormat sets how the logs to come are written.
func (a *App) setLogFormat(raw, stamps bool) error {
	st := a.settings
	st.SessionLog.Raw, st.SessionLog.Stamps = raw, stamps
	return a.ApplySettings(st)
}

// stopLog ends the log of c with the line its terminal is on; the file is
// closed in the background, a failure not reported yet told then.
func (a *App) stopLog(c *conn) {
	r := c.log
	if r == nil {
		return
	}
	c.log = nil
	c.s.Term.SetLineHook(nil)
	c.s.SetOutputTap(nil)
	tail, failed := c.s.Term.CursorLine(), r.Err() != nil
	go func() {
		err := r.Close(tail, time.Now())
		a.post(func() {
			if err != nil && !failed {
				a.logToast(err)
			}
			a.listLogs()
		})
	}()
}

// toggleLog starts the log of c, or stops it.
func (a *App) toggleLog(c *conn) {
	if c.log != nil {
		a.stopLog(c)
		return
	}
	if err := a.startLog(c); err != nil {
		a.logToast(err)
	}
}

func (a *App) logToast(err error) {
	a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.LogFailed(err.Error()), Type: "error"} })
}

// logCount is how many terminals are being logged, and whether the log of
// any of them failed.
func (a *App) logCount() (n int, failed bool) {
	for _, c := range a.conns {
		if c.log != nil {
			n++
			failed = failed || c.log.Err() != nil
		}
	}
	return n, failed
}

// bufferText is all a terminal holds, its scrollback and its screen, as
// text.
func bufferText(t *vt.Terminal) string {
	text := t.Text(vt.Pos{}, vt.Pos{Line: math.MaxInt32, Col: math.MaxInt32})
	return strings.TrimRight(text, " \n") + "\n"
}

// saveBuffer saves what terminal c holds to a file asked for.
func (a *App) saveBuffer(c *conn, m locales.Messages) {
	if a.p.SaveFile == nil || c.s == nil {
		return
	}
	text := bufferText(c.s.Term)
	dir, title := a.logDir(), m.LogSaveOutput()
	name := termlog.SafeName(a.liveHost(c.host).Label()) + "_" + time.Now().Format("2006-01-02_150405") + ".txt"
	go func() {
		if os.MkdirAll(dir, 0o700) != nil {
			dir = a.localHome()
		}
		path, err := a.p.SaveFile(title, filepath.Join(dir, name))
		if err == nil && path != "" {
			err = os.WriteFile(path, []byte(text), 0o600)
		}
		a.post(func() {
			switch {
			case err != nil:
				a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.LogSaveFailed(err.Error()), Type: "error"} })
			case path != "":
				a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.LogSaved(path)} })
			}
		})
	}()
}

// openLogDir shows the logs folder, made when it is not there yet.
func (a *App) openLogDir() {
	if a.p.OpenPath == nil {
		return
	}
	dir := a.logDir()
	go func() {
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			err = a.p.OpenPath(dir)
		}
		if err != nil {
			a.post(func() { a.logToast(err) })
		}
	}()
}

// chooseLogDir asks for the folder of the logs to come.
func (a *App) chooseLogDir(m locales.Messages) {
	if a.p.PickFolder == nil {
		return
	}
	title, start := m.LogChooseFolder(), a.logDir()
	go func() {
		dir, err := a.p.PickFolder(title, start)
		a.post(func() {
			if err != nil {
				a.logToast(err)
				return
			}
			if dir != "" {
				a.setLogDir(dir)
			}
		})
	}()
}

// setLogDir makes dir the folder of the logs to come; those being kept
// stay where they are.
func (a *App) setLogDir(dir string) error {
	st := a.settings
	st.SessionLog.Dir = dir
	err := a.ApplySettings(st)
	a.listLogs()
	return err
}

// logPanel is what the session logs pane shows: the folder and how logs
// are written, then the terminals, each with its log, and the logs
// written last.
func (a *App) logPanel(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	panes := a.terminalPanes(a.allTabs())
	n, _ := a.logCount()
	st := a.settings.SessionLog
	ui.Column(c.Key("logs")).Fill().Children(func() {
		ui.Row(c.Key("log:dir")).FillWidth().Shrink(0).Padding(theme.Space(2), theme.Space(2), theme.Space(1), theme.Space(4)).Gap(theme.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Folder.SVG).Size(14, 14).Shrink(0).TextColor(tok.TextMuted)
			ui.Text(c, a.logDir()).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine().Grow(1).Shrink(1).MinWidth(0).Tooltip(a.logDir())
			if (IconButton{Icon: icons.OpenFile, Label: m.LogOpenFolder(), Disabled: a.p.OpenPath == nil, Size: Small}).Clicked(c, "log:open") {
				a.openLogDir()
			}
			if (IconButton{Icon: icons.Edit, Label: m.LogChooseFolder(), Disabled: a.p.PickFolder == nil, Size: Small}).Clicked(c, "log:choose") {
				a.chooseLogDir(m)
			}
		})
		// How the logs to come are written.
		ui.Row(c.Key("log:format")).FillWidth().Shrink(0).Padding(theme.Space(1), theme.Space(3), theme.Space(2), theme.Space(4)).Gap(theme.Space(3)).AlignItems(ui.Center).Children(func() {
			format := 0
			if st.Raw {
				format = 1
			}
			formSegmented(c.Key("log:fmt"), &format, false, m.LogFormatText(), m.LogFormatRaw()).Label(m.LogFormat()).Tooltip(m.LogFormatHint()).Shrink(0)
			stamps := st.Stamps
			formDisabled(ui.Checkbox(c.Key("log:stamps"), &stamps, m.LogStamps()), format == 1).Shrink(1)
			if raw := format == 1; raw != st.Raw || stamps != st.Stamps {
				if err := a.setLogFormat(raw, stamps); err != nil {
					a.logToast(err)
				}
			}
		})
		ui.Row(c.Key("log:all")).FillWidth().Shrink(0).Padding(0, theme.Space(2), theme.Space(2), theme.Space(4)).Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LogRecordingCount(n)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Grow(1).Shrink(1)
			if (IconButton{Icon: icons.Record, Label: m.LogStartAll(), Disabled: n == len(panes), Size: Small}).Clicked(c, "log:startall") {
				for _, cn := range panes {
					if err := a.startLog(cn); err != nil {
						a.logToast(err)
						break
					}
				}
			}
			if (IconButton{Icon: icons.Stop, Label: m.LogStopAll(), Disabled: n == 0, Size: Small}).Clicked(c, "log:stopall") {
				for _, cn := range a.conns {
					a.stopLog(cn)
				}
			}
		})
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
		gutterScroll(c, ui.Scroll(c.Key("log:list")).Grow(1).Shrink(1).MinHeight(0).FillWidth()).Children(func() {
			ui.Column(c).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(0.5)).Children(func() {
				if len(panes) == 0 {
					ui.Column(c).FillWidth().Padding(theme.Space(6), theme.Space(4)).AlignItems(ui.Center).Children(func() {
						panelNote(c, tok, m.LogNoTerminal())
					})
				}
				for _, cn := range panes {
					a.logRow(c, m, tok, cn)
				}
				a.recentLogsList(c, m, tok)
			})
		})
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
		ui.Column(c).FillWidth().Shrink(0).Padding(theme.Space(3)).Children(func() {
			panelNote(c, tok, m.LogNote())
		})
	})
}

// logRow is a terminal of the logs pane: its log, kept or not, how long
// and big it is so far, and whether its host's terminals are logged as
// they connect.
func (a *App) logRow(c *ui.Context, m locales.Messages, tok theme.Tokens, cn *conn) {
	r := cn.log
	ui.Column(c.Key("log:row:"+strconv.Itoa(cn.id))).FillWidth().Padding(theme.Space(1), theme.Space(2)).Gap(theme.Space(0.5)).Radius(theme.RadiusControl).Children(func() {
		ui.Row(c).FillWidth().MinHeight(24).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
			dot(c, stateColor(tok, cn.s.Status().State))
			ui.Text(c, a.paneName(cn)).FontSize(theme.Footnote).SingleLine().Grow(1).Shrink(1).MinWidth(0)
			if r != nil && r.Err() == nil {
				d := time.Since(r.Started())
				tickDur(c, d)
				ui.Text(c, fmtDur(m, d)+" · "+humanSize(r.Size())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(0)
			}
			label := m.LogStart()
			if r != nil {
				label = m.LogStop()
			}
			if (IconButton{Icon: icons.Record, On: icons.RecordOn, Label: label, Active: r != nil, Toggle: true, Size: Small}).Clicked(c, "rec") {
				a.toggleLog(cn)
			}
			if (IconButton{Icon: icons.Save, Label: m.LogSaveOutput(), Disabled: a.p.SaveFile == nil, Size: Small}).Clicked(c, "save") {
				a.saveBuffer(cn, m)
			}
			if (IconButton{Icon: icons.FileText, Label: m.LogOpenFile(), Disabled: r == nil || a.p.OpenPath == nil, Size: Small}).Clicked(c, "open") && r != nil {
				a.openLogFile(r.Path())
			}
		})
		if h, ok := a.store.Get(cn.host.ID); ok {
			auto := h.SessionLog
			if formCheckbox(c.Key("auto"), &auto, m.LogAuto()).MarginStart(theme.Space(3.5)).Changed() {
				a.setHostLog(h.ID, auto)
			}
		}
		if r != nil && r.Err() != nil {
			ui.Text(c, m.LogFailed(r.Err().Error())).FontSize(theme.Caption).TextColor(tok.Danger).FillWidth()
		}
	})
}

// openLogFile opens the log at path with its app.
func (a *App) openLogFile(path string) {
	if a.p.OpenPath == nil {
		return
	}
	go func() {
		if err := a.p.OpenPath(path); err != nil {
			a.post(func() { a.logToast(err) })
		}
	}()
}

// recentLogsList is the logs of the folder written last, each opened as
// it is clicked.
func (a *App) recentLogsList(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	if a.logs.dir != a.logDir() {
		a.listLogs()
	}
	ui.Row(c.Key("log:recent")).FillWidth().Padding(theme.Space(3), theme.Space(2), theme.Space(1), theme.Space(2)).Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.LogRecent()).FontSize(theme.Caption).FontWeight(600).TextColor(tok.TextMuted).SingleLine().Grow(1)
		if (IconButton{Icon: icons.Refresh, Label: m.Refresh(), Disabled: a.logs.listing, Size: Small}).Clicked(c, "log:relist") {
			a.listLogs()
		}
	})
	if len(a.logs.recent) == 0 {
		ui.Text(c, m.LogRecentNone()).FontSize(theme.Caption).TextColor(tok.TextMuted).Padding(0, theme.Space(2))
		return
	}
	for _, l := range a.logs.recent {
		row := ui.Row(c.Key("log:file:"+l.Path)).FillWidth().MinHeight(28).Padding(theme.Space(1), theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).
			Radius(theme.RadiusControl).Cursor(ui.CursorPointer).Label(m.LogOpenFile() + " " + filepath.Base(l.Path)).Tooltip(l.Path)
		if row.Hovered() {
			row.Background(tok.Hover)
		}
		row.Children(func() {
			ui.Icon(c, icons.FileText.SVG).Size(14, 14).Shrink(0).TextColor(tok.TextMuted)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, a.pv(l.Host)).FontSize(theme.Footnote).SingleLine()
				ui.Text(c, l.Mod.Format("2006-01-02 15:04")+" · "+humanSize(l.Size)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			})
		})
		if row.Clicked() {
			a.openLogFile(l.Path)
		}
	}
}
