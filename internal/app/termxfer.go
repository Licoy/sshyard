package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/trzsz"
	"github.com/Licoy/sshyard/internal/xfer"
	"github.com/Licoy/sshyard/internal/zmodem"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// A terminal's shell can start a transfer over its own byte stream:
// ZMODEM (sz sends, rz receives) or trzsz (tsz, trz). Each is asked
// about first: any server can print what starts one. While one runs the
// terminal shows none of it, typing waits, and Ctrl+C stops it.

// xferMode is what a transfer the shell started asks for.
type xferMode int

const (
	// xferDownload: the server sends files, saved on this computer.
	xferDownload xferMode = iota
	// xferUpload: the server waits for files of this computer.
	xferUpload
	// xferUploadDir: as xferUpload, folders too (trz -d).
	xferUploadDir
)

const (
	protoZmodem = "ZMODEM"
	protoTrzsz  = "trzsz"
)

// xferAskWait is how long a question waits for its answer before the
// transfer is refused: sz gives up after about a minute and a half.
const xferAskWait = 60 * time.Second

// xferDropWait is how long files dropped on a terminal wait for the
// transfer the command typed for them starts.
const xferDropWait = 30 * time.Second

// errXferStopped is a transfer the user stopped.
var errXferStopped = errors.New("transfer stopped")

// xferPane is a terminal pane's transfers.
type xferPane struct {
	// seen are the trzsz transfers answered, by ID: tmux shows a
	// transfer's magic again as it redraws. Only the session's Takeover
	// touches it, one call at a time.
	seen map[string]bool

	mu sync.Mutex
	// stop stops the transfer asking or running; nil when none.
	stop context.CancelCauseFunc
	// run is how the transfer goes, once it does.
	run *xferRun
	// dropped are files dropped on the terminal, for the transfer the
	// command typed for them starts by droppedUntil, over droppedProto.
	dropped      []string
	droppedProto string
	droppedUntil time.Time
}

// begin is a transfer asking; it returns the context it runs in.
func (x *xferPane) begin(ctx context.Context) context.Context {
	ctx, stop := context.WithCancelCause(ctx)
	x.mu.Lock()
	x.stop, x.run = stop, nil
	x.mu.Unlock()
	return ctx
}

// end is the transfer over.
func (x *xferPane) end() {
	x.mu.Lock()
	if x.stop != nil {
		x.stop(nil)
	}
	x.stop, x.run = nil, nil
	x.mu.Unlock()
}

// cancel stops the transfer asking or running, if any; it reports
// whether there was one.
func (x *xferPane) cancel() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.stop == nil {
		return false
	}
	x.stop(errXferStopped)
	return true
}

// active says a transfer asks or runs.
func (x *xferPane) active() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.stop != nil
}

// running is how the transfer going goes; nil when none.
func (x *xferPane) running() *xferRun {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.run
}

func (x *xferPane) going(r *xferRun) {
	x.mu.Lock()
	x.run = r
	x.mu.Unlock()
}

// takeDropped are the files dropped for a transfer over proto, which
// they then no longer wait for; nil when none or too late.
func (x *xferPane) takeDropped(proto string, now time.Time) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	files := x.dropped
	ok := x.droppedProto == proto && now.Before(x.droppedUntil)
	x.dropped, x.droppedProto = nil, ""
	if !ok {
		return nil
	}
	return files
}

func (x *xferPane) drop(files []string, proto string, until time.Time) {
	x.mu.Lock()
	x.dropped, x.droppedProto, x.droppedUntil = files, proto, until
	x.mu.Unlock()
}

// xferRun is how a transfer goes, for its banner.
type xferRun struct {
	upload bool
	proto  string
	start  time.Time
	// total is how many files go, 0 when not known.
	total int

	mu sync.Mutex
	// n is how many files have started, name the last, done of its
	// size bytes through (size -1 when not known), and before the bytes
	// of the files before it.
	n          int
	name       string
	size, done int64
	before     int64
}

func (r *xferRun) progress(f xfer.File, done int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 || f.Name != r.name {
		r.n++
		r.before += r.done
		r.name, r.size, r.done = f.Name, f.Size, 0
	}
	r.done = done
}

// xferShown is what a banner shows of a transfer.
type xferShown struct {
	n, total   int
	name       string
	size, done int64
	speed      float64
}

func (r *xferRun) shown(now time.Time) xferShown {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := xferShown{n: r.n, total: r.total, name: r.name, size: r.size, done: r.done}
	if d := now.Sub(r.start).Seconds(); d >= 1 {
		s.speed = float64(r.before+r.done) / d
	}
	return s
}

// xferReply is the user's answer: where received files go, or what is
// sent; neither is a refusal.
type xferReply struct {
	dir   string
	paths []string
	// m is the user's language, for what the server is told to print.
	m locales.Messages
}

func (r xferReply) ok() bool { return r.dir != "" || len(r.paths) > 0 }

// xferAsk is the question a transfer waits on.
type xferAsk struct {
	open  bool
	pane  int
	host  string
	proto string
	mode  xferMode
	// reply takes the answer once; picking closes as the user goes on
	// to choose in the system's dialog, which waits as long as it takes.
	reply   chan xferReply
	picking chan struct{}
	picked  bool
}

// xferDrop asks how files dropped on a remote terminal go.
type xferDrop struct {
	open  bool
	pane  int
	files []string
	dirs  bool
}

// xferState is the questions of the terminals' transfers.
type xferState struct {
	ask  *xferAsk
	drop *xferDrop
}

// armTransfers makes c's terminal take the transfers its shell starts,
// while the setting and its host let it.
func (a *App) armTransfers(c *conn) {
	x := &xferPane{seen: map[string]bool{}}
	c.xfer = x
	id, hostID, stream := c.id, c.host.ID, isStream(c.host)
	c.s.SetTakeover(func(p []byte) (int, func(context.Context, xfer.Conn) []byte) {
		at, zo, to := a.findTransfer(x, hostID, p)
		switch {
		case zo != nil:
			return at, func(ctx context.Context, conn xfer.Conn) []byte {
				return a.runZmodem(ctx, conn, id, x, zo, stream)
			}
		case to != nil:
			return at, func(ctx context.Context, conn xfer.Conn) []byte {
				return a.runTrzsz(ctx, conn, id, x, to)
			}
		}
		return -1, nil
	})
}

// findTransfer finds in p the first transfer the shell starts, a ZMODEM
// or a trzsz one, and where; none while the setting or the host turned
// them off. A trzsz transfer is found once.
func (a *App) findTransfer(x *xferPane, hostID string, p []byte) (int, *zmodem.Offer, *trzsz.Offer) {
	if !a.xferOn.Load() || a.transfersOff(hostID) {
		return -1, nil, nil
	}
	zat, zo := zmodem.Find(p)
	tat, to := trzsz.Find(p)
	if to != nil && to.ID != "" && x.seen[to.ID] {
		to = nil
	}
	switch {
	case zo != nil && (to == nil || zat < tat):
		return zat, zo, nil
	case to != nil:
		if to.ID != "" {
			x.seen[to.ID] = true
		}
		return tat, nil, to
	}
	return -1, nil, nil
}

// transfersOff says the saved host turned transfers off. It runs off
// the UI thread: the store has its own lock.
func (a *App) transfersOff(hostID string) bool {
	if a.store == nil {
		return false
	}
	h, ok := a.store.Get(hostID)
	return ok && h.TransfersOff
}

func (a *App) runZmodem(outer context.Context, c xfer.Conn, id int, x *xferPane, o *zmodem.Offer, stream bool) []byte {
	ctx := x.begin(outer)
	defer x.end()
	mode := xferDownload
	if o.Upload {
		mode = xferUpload
	}
	r := a.askXfer(ctx, x, id, protoZmodem, mode)
	if !r.ok() {
		rest, _ := zmodem.Refuse(outer, c)
		a.xferEnded(id, mode, protoZmodem, ctx, nil, 0, "")
		return rest
	}
	// Telnet and serial lines may eat control characters.
	opts := zmodem.Options{EscapeControl: stream}
	if mode == xferUpload {
		src, err := newLocalSource(r.paths, false)
		if err != nil {
			rest, _ := zmodem.Refuse(outer, c)
			a.xferEnded(id, mode, protoZmodem, ctx, err, 0, "")
			return rest
		}
		run := &xferRun{upload: true, proto: protoZmodem, start: time.Now(), total: src.count()}
		x.going(run)
		opts.Progress = run.progress
		rest, err := zmodem.Send(ctx, c, src, opts)
		a.xferEnded(id, mode, protoZmodem, ctx, err, src.count(), "")
		return rest
	}
	sink := newLocalSink(r.dir, false)
	run := &xferRun{proto: protoZmodem, start: time.Now()}
	x.going(run)
	opts.Progress = run.progress
	rest, err := zmodem.Receive(ctx, c, sink, opts)
	a.xferEnded(id, mode, protoZmodem, ctx, err, sink.count(), r.dir)
	return rest
}

func (a *App) runTrzsz(outer context.Context, c xfer.Conn, id int, x *xferPane, o *trzsz.Offer) []byte {
	ctx := x.begin(outer)
	defer x.end()
	mode := map[trzsz.Mode]xferMode{trzsz.Download: xferDownload, trzsz.Upload: xferUpload, trzsz.UploadDir: xferUploadDir}[o.Mode]
	r := a.askXfer(ctx, x, id, protoTrzsz, mode)
	if !r.ok() {
		rest, _ := trzsz.Refuse(outer, c, o)
		a.xferEnded(id, mode, protoTrzsz, ctx, nil, 0, "")
		return rest
	}
	m := r.m
	opts := trzsz.Options{Dir: r.dir, Saved: func(n int, dir string) string { return m.TermXferSavedRemote(strconv.Itoa(n), dir) }}
	if mode != xferDownload {
		src, err := newLocalSource(r.paths, mode == xferUploadDir)
		if err != nil {
			rest, _ := trzsz.Refuse(outer, c, o)
			a.xferEnded(id, mode, protoTrzsz, ctx, err, 0, "")
			return rest
		}
		run := &xferRun{upload: true, proto: protoTrzsz, start: time.Now(), total: src.count()}
		x.going(run)
		opts.Progress = run.progress
		rest, err := trzsz.Send(ctx, c, o, src, opts)
		a.xferEnded(id, mode, protoTrzsz, ctx, err, src.count(), "")
		return rest
	}
	sink := newLocalSink(r.dir, true)
	run := &xferRun{proto: protoTrzsz, start: time.Now()}
	x.going(run)
	opts.Progress = run.progress
	rest, err := trzsz.Receive(ctx, c, o, sink, opts)
	a.xferEnded(id, mode, protoTrzsz, ctx, err, sink.count(), r.dir)
	return rest
}

// askXfer asks the user about a transfer and waits for the answer: the
// files dropped for it, when there are; none when the question goes
// unanswered, another is open, or the transfer is stopped.
func (a *App) askXfer(ctx context.Context, x *xferPane, id int, proto string, mode xferMode) xferReply {
	if mode != xferDownload {
		if files := x.takeDropped(proto, time.Now()); files != nil {
			return xferReply{paths: files}
		}
	}
	ask := &xferAsk{pane: id, proto: proto, mode: mode, reply: make(chan xferReply, 1), picking: make(chan struct{})}
	a.post(func() {
		cn := a.conns[id]
		if cn == nil || a.xfer.ask != nil {
			ask.reply <- xferReply{}
			return
		}
		ask.host, ask.open = cn.host.Label(), true
		a.xfer.ask = ask
	})
	t := time.NewTimer(xferAskWait)
	defer t.Stop()
	var r xferReply
	select {
	case r = <-ask.reply:
	case <-ask.picking:
		select {
		case r = <-ask.reply:
		case <-ctx.Done():
		}
	case <-ctx.Done():
	case <-t.C:
	}
	a.post(func() {
		if a.xfer.ask == ask {
			a.xfer.ask = nil
		}
	})
	if ctx.Err() != nil {
		return xferReply{}
	}
	return r
}

// answerXfer answers the question open.
func (a *App) answerXfer(ask *xferAsk, r xferReply) {
	if a.xfer.ask == ask {
		a.xfer.ask = nil
	}
	ask.open = false
	select {
	case ask.reply <- r:
	default:
	}
}

// pickXfer answers ask with what the user chooses in the system's
// dialog: a folder to save in, the files or a folder to send.
func (a *App) pickXfer(ask *xferAsk, m locales.Messages, folder bool) {
	pick := func() ([]string, error) {
		switch {
		case ask.mode == xferDownload:
			dir, err := a.p.PickFolder(m.ChooseDownloadFolder(), a.downloadsDir())
			if dir == "" {
				return nil, err
			}
			return []string{dir}, err
		case folder:
			dir, err := a.p.PickFolder(m.TermXferChooseFolder(), a.localHome())
			if dir == "" {
				return nil, err
			}
			return []string{dir}, err
		}
		return a.p.PickFiles(m.TermXferChooseFiles())
	}
	ask.open, ask.picked = false, true
	if a.xfer.ask == ask {
		a.xfer.ask = nil
	}
	close(ask.picking)
	go func() {
		got, err := pick()
		r := xferReply{m: m}
		switch {
		case err != nil || len(got) == 0:
			r = xferReply{}
		case ask.mode == xferDownload:
			r.dir = got[0]
		default:
			r.paths = got
		}
		select {
		case ask.reply <- r:
		default:
		}
	}()
}

// dropXferAsk refuses the question of pane id, which closes.
func (a *App) dropXferAsk(id int) {
	if ask := a.xfer.ask; ask != nil && ask.pane == id {
		a.answerXfer(ask, xferReply{})
	}
	if d := a.xfer.drop; d != nil && d.pane == id {
		a.xfer.drop = nil
	}
}

// xferEnded tells how a transfer ended: n files went, saved in dir for
// a download. A transfer refused here says nothing.
func (a *App) xferEnded(id int, mode xferMode, proto string, ctx context.Context, err error, n int, dir string) {
	stopped := errors.Is(err, errXferStopped) || errors.Is(context.Cause(ctx), errXferStopped)
	if !stopped && err == nil && n == 0 {
		// Refused, or nothing came.
		return
	}
	why := ""
	if err != nil && !errors.Is(err, context.Canceled) {
		why = err.Error()
	}
	a.post(func() {
		cn := a.conns[id]
		if cn == nil {
			return
		}
		host := cn.host.Label()
		a.toast(func(m locales.Messages) ui.Toast {
			switch {
			case stopped:
				return ui.Toast{Title: m.TermXferStopped(proto)}
			case errors.Is(err, xfer.ErrRefused):
				return ui.Toast{Title: m.TermXferRefused(host, proto), Type: "error"}
			case err != nil:
				return ui.Toast{Title: m.TermXferFailed(proto), Description: why, Type: "error"}
			case mode == xferDownload:
				return ui.Toast{Title: m.TermXferReceived(host, strconv.Itoa(n)), Description: dir, Type: "success"}
			}
			return ui.Toast{Title: m.TermXferSent(host, strconv.Itoa(n)), Type: "success"}
		})
	})
}

// xferKey handles a key on a terminal whose transfer asks or runs: Ctrl+C
// stops it, other keys go nowhere. It reports whether it took ev.
func (a *App) xferKey(cn *conn, ev ui.InputEvent) bool {
	// The pane's own lock, not the session's output's, on each key.
	if cn.xfer == nil || !cn.xfer.active() {
		return false
	}
	switch ev.Kind {
	case ui.InputKeyDown:
		if isAppShortcut(a.settings.Shortcuts, ev.Key, ev.Mods) {
			return false
		}
		if ev.Key == ui.KeyC && ev.Mods&(ui.Ctrl|ui.Alt|ui.Shift|ui.Super) == ui.Ctrl {
			cn.xfer.cancel()
		}
		return true
	case ui.InputKeyUp, ui.InputText, ui.InputCompose:
		return true
	}
	return false
}

// xferAskView is the question of a transfer a terminal's shell started.
func (a *App) xferAskView(c *ui.Context, m locales.Messages) {
	ask := a.xfer.ask
	if ask == nil {
		return
	}
	tok := currentTokens(c)
	sheet(ui.Modal(c, &ask.open, func() {
		ui.Column(c).Gap(14).Width(440).Children(func() {
			if ask.mode == xferDownload {
				sheetTitle(c, m.TermXferReceiveTitle())
				ui.Text(c, m.TermXferReceiveBody(ask.host, ask.proto)).FontSize(theme.Body)
			} else {
				sheetTitle(c, m.TermXferSendTitle())
				ui.Text(c, m.TermXferSendBody(ask.host, ask.proto)).FontSize(theme.Body)
			}
			ui.Text(c, a.oscPaneName(m, ask.pane)).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			if ask.mode == xferDownload {
				ui.Text(c, m.TermXferNoOverwrite()).FontSize(theme.Caption).TextColor(tok.TextMuted)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				if pill(c, m.Cancel(), false, false, false) {
					a.answerXfer(ask, xferReply{})
				}
				ui.Spacer(c)
				switch ask.mode {
				case xferDownload:
					if pill(c, m.TermXferChooseFolder(), false, false, a.p.PickFolder == nil) {
						a.pickXfer(ask, m, true)
					}
					if pill(c, m.TermXferSaveDownloads(), true, false, false) {
						a.answerXfer(ask, xferReply{dir: a.downloadsDir(), m: m})
					}
				default:
					if ask.mode == xferUploadDir {
						if pill(c, m.TermXferChooseFolder(), false, false, a.p.PickFolder == nil) {
							a.pickXfer(ask, m, true)
						}
					}
					if pill(c, m.TermXferChooseFiles(), true, false, a.p.PickFiles == nil) {
						a.pickXfer(ask, m, false)
					}
				}
			})
		})
	}), tok)
	if !ask.open && !ask.picked && a.xfer.ask == ask {
		// Closed with Escape or a click outside.
		a.answerXfer(ask, xferReply{})
	}
}

// xferBanner shows how the transfer of cn's terminal goes, if one does.
func (a *App) xferBanner(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	if cn.xfer == nil {
		return
	}
	run := cn.xfer.running()
	if run == nil {
		return
	}
	s := run.shown(c.Now())
	col := tok.Accent
	ic := icons.Download
	if run.upload {
		ic = icons.Upload
	}
	ui.Row(c.Key("xfer")).Margin(0, 8, 8, 8).Padding(6, 8, 6, 12).MinHeight(36).Gap(8).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		ui.Icon(c, ic.SVG).Size(14, 14).Shrink(0).TextColor(col)
		ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			title := m.TermXferWaiting(run.proto)
			switch {
			case s.name != "" && run.upload:
				title = m.TermXferSending(shownName(path.Base(s.name)))
			case s.name != "":
				title = m.TermXferReceiving(shownName(path.Base(s.name)))
			}
			ui.Text(c, title).FontSize(theme.Footnote).FontWeight(600).SingleLine().Shrink(1).MinWidth(0)
			if s.name == "" {
				return
			}
			parts := []string{run.proto}
			if s.total > 1 {
				parts = append(parts, m.TermXferCount(strconv.Itoa(s.n), strconv.Itoa(s.total)))
			}
			if s.size >= 0 {
				parts = append(parts, m.TermXferBytes(humanSize(s.done), humanSize(s.size)))
			} else {
				parts = append(parts, humanSize(s.done))
			}
			if s.speed > 0 {
				parts = append(parts, humanSize(int64(s.speed))+"/s")
			}
			ui.Text(c, strings.Join(parts, " · ")).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1).MinWidth(0)
			if s.size > 0 {
				ui.Progress(c, min(float64(s.done)/float64(s.size), 1)).FillWidth()
			}
		})
		if (IconButton{Icon: icons.Close, Label: m.TermXferCancel(), Size: Small, Danger: true}).Clicked(c, "xfer-cancel") {
			cn.xfer.cancel()
		}
	})
	c.After(250 * time.Millisecond)
}

// dropOnRemote asks how files dropped on a remote terminal are sent.
func (a *App) dropOnRemote(cn *conn, files []string) {
	if cn.xfer == nil || !a.settings.TerminalTransfers || a.transfersOff(cn.host.ID) || cn.xfer.active() {
		return
	}
	dirs := false
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.IsDir() {
			dirs = true
		}
	}
	a.xfer.drop = &xferDrop{open: true, pane: cn.id, files: files, dirs: dirs}
}

// sendDropped types the command that receives the files dropped, which
// then wait for the transfer it starts.
func (a *App) sendDropped(d *xferDrop, proto string) {
	d.open = false
	a.xfer.drop = nil
	cn := a.conns[d.pane]
	if cn == nil || cn.xfer == nil {
		return
	}
	// rz renames a file whose name is taken (-E); so does trz.
	cmd := "rz -E\r"
	if proto == protoTrzsz {
		cmd = "trz\r"
		if d.dirs {
			cmd = "trz -d\r"
		}
	}
	cn.xfer.drop(d.files, proto, time.Now().Add(xferDropWait))
	if err := cn.s.Send([]byte(cmd)); err != nil {
		cn.xfer.drop(nil, "", time.Time{})
		a.snippetSendResult(err)
	}
}

// xferDropView asks how files dropped on a remote terminal go.
func (a *App) xferDropView(c *ui.Context, m locales.Messages) {
	d := a.xfer.drop
	if d == nil {
		return
	}
	tok := currentTokens(c)
	host := ""
	if cn := a.conns[d.pane]; cn != nil {
		host = cn.host.Label()
	}
	sheet(ui.Modal(c, &d.open, func() {
		ui.Column(c).Gap(14).Width(440).Children(func() {
			sheetTitle(c, m.TermXferDropTitle(strconv.Itoa(len(d.files)), host))
			ui.Text(c, m.TermXferDropBody()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			if d.dirs {
				ui.Text(c, m.TermXferDropDirs()).FontSize(theme.Caption).TextColor(tok.TextMuted)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				if pill(c, m.Cancel(), false, false, false) {
					d.open = false
				}
				ui.Spacer(c)
				if pill(c, m.TermXferDropRz(), false, false, d.dirs) {
					a.sendDropped(d, protoZmodem)
				}
				if pill(c, m.TermXferDropTrz(), true, false, false) {
					a.sendDropped(d, protoTrzsz)
				}
			})
		})
	}), tok)
	if !d.open && a.xfer.drop == d {
		a.xfer.drop = nil
	}
}

// localSink saves received files below dir, never over what is there: a
// name taken gets a number. Each file is written beside its name, as
// .part, until all of it came.
type localSink struct {
	dir string
	// paths keeps the folders below which files were sent; without, a
	// file is saved by its base name.
	paths bool
	// tops are the names at the top as sent, as saved.
	tops map[string]string

	mu    sync.Mutex
	saved int
}

func newLocalSink(dir string, paths bool) *localSink {
	return &localSink{dir: dir, paths: paths, tops: map[string]string{}}
}

// count is how many files were saved.
func (s *localSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

func (s *localSink) Create(f xfer.File) (string, xfer.Writer, error) {
	parts := strings.Split(f.Name, "/")
	if !s.paths {
		// What ZMODEM sends may name the server's folders.
		parts = parts[len(parts)-1:]
	}
	for _, p := range parts {
		if err := sftpsvc.SafeName(p); err != nil {
			if !s.paths {
				return "", nil, xfer.ErrSkip
			}
			return "", nil, err
		}
	}
	top, ok := s.tops[parts[0]]
	if !ok || !s.paths {
		top = freeName(s.dir, parts[0])
		if s.paths {
			s.tops[parts[0]] = top
		}
	}
	rel := path.Join(append([]string{top}, parts[1:]...)...)
	local, err := sftpsvc.LocalPath(s.dir, rel)
	if err != nil {
		return "", nil, err
	}
	if f.Dir {
		return rel, nil, os.MkdirAll(local, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return "", nil, err
	}
	part := local + ".part"
	fh, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	return rel, &localFile{s: s, f: fh, part: part, final: local, file: f}, nil
}

// freeName is name, or name with a number, which nothing in dir has,
// nor its .part.
func freeName(dir, name string) string {
	taken := func(n string) bool {
		for _, p := range []string{n, n + ".part"} {
			if _, err := os.Lstat(filepath.Join(dir, p)); !errors.Is(err, fs.ErrNotExist) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	stem, ext := name, path.Ext(name)
	if ext != "" && ext != name {
		stem = strings.TrimSuffix(name, ext)
	} else {
		ext = ""
	}
	for i := 1; ; i++ {
		if n := fmt.Sprintf("%s (%d)%s", stem, i, ext); !taken(n) {
			return n
		}
	}
}

// localFile is a file being received.
type localFile struct {
	s     *localSink
	f     *os.File
	part  string
	final string
	file  xfer.File
}

func (w *localFile) Write(p []byte) (int, error) { return w.f.Write(p) }

func (w *localFile) Commit() error {
	if err := w.f.Close(); err != nil {
		os.Remove(w.part)
		return err
	}
	mode := fs.FileMode(0o644)
	if w.file.Mode != 0 {
		mode = sftpsvc.LocalMode(w.file.Mode)
	}
	os.Chmod(w.part, mode)
	if !w.file.ModTime.IsZero() {
		os.Chtimes(w.part, w.file.ModTime, w.file.ModTime)
	}
	// Something may have taken the name meanwhile.
	if _, err := os.Lstat(w.final); !errors.Is(err, fs.ErrNotExist) {
		w.final = filepath.Join(filepath.Dir(w.final), freeName(filepath.Dir(w.final), filepath.Base(w.final)))
	}
	if err := os.Rename(w.part, w.final); err != nil {
		os.Remove(w.part)
		return err
	}
	w.s.mu.Lock()
	w.s.saved++
	w.s.mu.Unlock()
	return nil
}

func (w *localFile) Abort() {
	w.f.Close()
	os.Remove(w.part)
}

// localSource sends files of this computer, and the folders chosen with
// what is in them.
type localSource struct {
	files []xfer.File
	paths []string
}

// newLocalSource lists paths; a folder only with dirs, then what is in
// it, below its name. Links and what is neither file nor folder inside a
// folder are left out.
func newLocalSource(paths []string, dirs bool) (*localSource, error) {
	s := &localSource{}
	add := func(name, p string, fi fs.FileInfo) {
		f := xfer.File{Name: name, Size: fi.Size(), ModTime: fi.ModTime(), Mode: fi.Mode().Perm(), Dir: fi.IsDir()}
		if f.Dir {
			f.Size = 0
		}
		s.files = append(s.files, f)
		s.paths = append(s.paths, p)
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(p)
		if !fi.IsDir() {
			add(base, p, fi)
			continue
		}
		if !dirs {
			return nil, fmt.Errorf("%s: %w", base, errXferFolder)
		}
		err = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(p, q)
			if err != nil {
				return err
			}
			name := base
			if rel != "." {
				name = base + "/" + filepath.ToSlash(rel)
			}
			add(name, q, fi)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// errXferFolder is a folder chosen for a transfer that takes files only.
var errXferFolder = errors.New("a folder cannot be sent this way")

// count is how many files are sent, folders aside.
func (s *localSource) count() int {
	n := 0
	for _, f := range s.files {
		if !f.Dir {
			n++
		}
	}
	return n
}

func (s *localSource) Files() []xfer.File { return s.files }

func (s *localSource) Open(i int) (io.ReadCloser, error) { return os.Open(s.paths[i]) }
