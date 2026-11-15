package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The Docker panel: the containers and the images of the focused
// session's server, read again every dockerEvery while the panel shows,
// and what can be done to them: start, stop, restart, pause, remove,
// read a container's logs, open a shell in it.

// dockerEvery is how often the panel reads the server again.
const dockerEvery = 5 * time.Second

// dockerTimeout bounds one docker command; dockerLogLines is how much of
// a container's output the logs sheet shows.
const (
	dockerTimeout  = 30 * time.Second
	dockerLogLines = 500
)

// dockerState is what the Docker panel read, of which session, and what
// it does.
type dockerState struct {
	conn, connects, gen int
	running             bool
	at                  time.Time
	info                session.DockerInfo
	read                bool
	err                 error
	// view is the segment shown: 0 the containers, 1 the images.
	view       int
	ctrSel     int
	imgSel     int
	ctrList    ui.ListState
	imgList    ui.ListState
	busy       string // the ID being acted on, "" for none
	confirm    bool   // the removal is asked about
	confirmRef string
	confirmImg bool
	logsOpen   bool
	logsRef    string
	logsName   string
	logs       string
	logsBusy   bool
	logsErr    string
	// The details sheet of the container detailRef, read again every
	// dockerEvery while it shows. A click on a row opens it, and the
	// row stays chosen only while it shows.
	detailOpen    bool
	detailRef     string
	detail        session.ContainerDetail
	detailRead    bool
	detailRunning bool
	detailAt      time.Time
	detailErr     error
	// The details sheet of the image imgRef, read as it opens.
	imgOpen    bool
	imgRef     string
	img        session.ImageDetail
	imgRead    bool
	imgRunning bool
	imgErr     error
}

// dockerErrText is a failure of the panel in words.
func dockerErrText(m locales.Messages, err error) string {
	switch {
	case errors.Is(err, session.ErrNoDocker):
		return m.DockerMissing()
	case errors.Is(err, session.ErrDockerDenied):
		return m.DockerDenied()
	}
	return m.DockerFailed(err.Error())
}

// readDocker lists tc's containers and images when it is time.
func (a *App) readDocker(c *ui.Context, tc *conn, now bool) {
	d := a.docker
	connects := tc.s.Status().Connects
	if d.conn != tc.id || d.connects != connects {
		d.gen++
		d.conn, d.connects, d.running, d.at = tc.id, connects, false, time.Time{}
		d.info, d.read, d.err, d.busy = session.DockerInfo{}, false, nil, ""
		d.ctrSel, d.imgSel = -1, -1
		d.detailOpen, d.detailRunning = false, false
		d.imgOpen, d.imgRunning = false, false
	}
	if d.running {
		return
	}
	if wait := dockerEvery - c.Now().Sub(d.at); !now && !d.at.IsZero() && wait > 0 {
		c.After(wait)
		return
	}
	if !now && errors.Is(d.err, session.ErrNoDocker) {
		return // no use asking again; the refresh button does
	}
	client := tc.s.Client()
	if client == nil {
		return
	}
	d.running, d.at = true, c.Now()
	gen := d.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
		info, err := session.DockerList(ctx, client)
		cancel()
		a.post(func() {
			if d.gen != gen {
				return
			}
			d.running, d.err = false, err
			if err == nil {
				d.info, d.read = info, true
			}
		})
	}()
}

// dockerDo does action to ref on tc's server, then reads the lists again.
func (a *App) dockerDo(tc *conn, action session.DockerAction, ref string) {
	d := a.docker
	client := tc.s.Client()
	if client == nil || d.busy != "" {
		return
	}
	d.busy = ref
	gen := d.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
		err := session.DockerDo(ctx, client, action, ref)
		cancel()
		a.post(func() {
			if d.gen != gen {
				return
			}
			d.busy, d.at, d.detailAt = "", time.Time{}, time.Time{} // read again at once
			if err != nil {
				a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
					return ui.Toast{Title: dockerErrText(m, err), Type: "error"}
				})
			}
		})
	}()
}

// dockerLogs reads the logs of the container ref into the logs sheet.
func (a *App) dockerLogs(tc *conn, ref, name string) {
	d := a.docker
	client := tc.s.Client()
	if client == nil {
		return
	}
	d.logsOpen, d.logsRef, d.logsName, d.logsBusy, d.logsErr = true, ref, name, true, ""
	gen := d.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
		out, err := session.DockerLogs(ctx, client, ref, dockerLogLines)
		cancel()
		a.post(func() {
			if d.gen != gen || d.logsRef != ref {
				return
			}
			d.logsBusy, d.logs = false, out
			if err != nil {
				d.logsErr = err.Error()
			}
		})
	}()
}

// dockerShell opens a terminal on the host of tc in a new tab, which runs
// a shell in the container ref once it is connected: bash where the
// image has it, else sh.
func (a *App) dockerShell(tc *conn, ref string) {
	if !session.DockerRefOK(ref) {
		return
	}
	docker := "docker"
	if a.docker.info.Sudo {
		docker = "sudo docker"
	}
	// The container's shell is typed once, without the host's tmux
	// session.
	a.openTerminal(tc.host.ID, func(cn *conn) {
		cn.container = true
		if cn.start != nil {
			cn.start.once = docker + " exec -it " + ref + ` sh -c "command -v bash >/dev/null && exec bash || exec sh"`
			cn.start.host = ""
		}
	})
}

// dockerPanel is the Docker panel's body.
func (a *App) dockerPanel(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	if a.docker == nil {
		a.docker = &dockerState{ctrSel: -1, imgSel: -1, ctrList: ui.ListState{RoundRows: true}, imgList: ui.ListState{RoundRows: true}}
	}
	d := a.docker
	cn := a.focused()
	tc := a.sshConn(cn)
	if note := a.usageNote(m, cn, tc); note != "" {
		panelNote(c, tok, note)
		return
	}
	a.readDocker(c, tc, false)
	ui.Row(c.Key("dockertools")).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
		formSegmented(c.Key("dockerview"), &d.view, false, m.DockerContainers(strconv.Itoa(len(d.info.Containers))), m.DockerImages(strconv.Itoa(len(d.info.Images)))).
			Label(m.DockerManager()).Grow(1)
		if d.running {
			ui.Spinner(c)
		}
		if (IconButton{Icon: icons.Refresh, Label: m.Refresh(), Disabled: d.running}).Clicked(c, "dockerrefresh") {
			d.err = nil
			a.readDocker(c, tc, true)
		}
	})
	switch {
	case d.err != nil:
		ui.Row(c.Key("dockererr")).FillWidth().Padding(8, 10).Gap(8).Radius(theme.RadiusControl).
			Background(tok.Warning.Alpha(0.12)).Border(1, tok.Warning.Alpha(0.3)).Children(func() {
			ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(tok.Warning)
			ui.Text(c, a.pv(dockerErrText(m, d.err))).FontSize(theme.Footnote).Shrink(1)
		})
		if !d.read {
			return
		}
	case !d.read:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			panelNote(c, tok, m.MonitorWaiting())
		})
		return
	}
	if d.view == 0 {
		a.containersView(c, m, tok, tc)
	} else {
		a.imagesView(c, m, tok, tc)
	}
	a.dockerImageView(c, m, tok, tc)
	a.dockerDetailView(c, m, tok, tc)
	a.dockerConfirm(c, m, tc)
	a.dockerLogsView(c, m, tok, tc)
}

// stateTint is the color of a container's state.
func stateTint(tok theme.Tokens, state string) ui.Color {
	switch state {
	case "running":
		return tok.Success
	case "paused", "restarting", "created":
		return tok.Warning
	case "dead":
		return tok.Danger
	}
	return tok.TextMuted
}

func (a *App) containersView(c *ui.Context, m locales.Messages, tok theme.Tokens, tc *conn) {
	d := a.docker
	cs := d.info.Containers
	if len(cs) == 0 {
		panelNote(c, tok, m.DockerNoContainers())
		return
	}
	if !d.detailOpen || d.ctrSel >= len(cs) {
		d.ctrSel = -1
	}
	d.ctrList.Selected = &d.ctrSel
	cols := []ui.TableColumn{{Title: m.ColName(), MinWidth: 90}, {Title: m.DockerColImage(), Width: 120}, {Title: m.DockerColStatus(), Width: 120}}
	tbl := ui.Table(c.Key("containers"), &d.ctrList, cols, len(cs), func(row, col int) {
		ct := cs[row]
		muted := tok.TextMuted
		if d.ctrSel == row {
			muted = tok.AccentText
		}
		cell := ui.Row(c).Gap(6).AlignItems(ui.Center).Grow(1).MinWidth(0).AlignSelf(ui.Stretch).Children(func() {
			switch col {
			case 0:
				dot(c, stateTint(tok, ct.State))
				ui.Text(c, ct.Name).FontSize(theme.Footnote).SingleLine()
			case 1:
				ui.Text(c, ct.Image).FontSize(theme.Footnote).TextColor(muted).SingleLine()
			case 2:
				ui.Text(c, ct.Status).FontSize(theme.Footnote).TextColor(muted).SingleLine()
			}
		})
		a.contextMenu(cell, func() []menuEntry { return a.containerEntries(m, tc, ct.ID) })
	}).Grow(1).Shrink(1).MinHeight(0).FillWidth()
	if (tbl.Changed() || tbl.Submitted()) && d.ctrSel >= 0 && d.ctrSel < len(cs) {
		a.openContainer(cs[d.ctrSel])
	}
	if d.ctrSel < 0 {
		panelNote(c, tok, m.DockerPickContainer())
	}
}

// containerActions are the buttons doing to the container ct what its
// menu does.
func (a *App) containerActions(c *ui.Context, m locales.Messages, tc *conn, ct session.Container) {
	d := a.docker
	ui.Row(c.Key("ctracts")).Gap(2).AlignItems(ui.Center).FillWidth().Children(func() {
		busy := d.busy != ""
		if ct.Running() {
			if (IconButton{Icon: icons.Halt, Label: m.DockerStop(), Disabled: busy}).Clicked(c, "stop") {
				a.dockerDo(tc, session.DockerStop, ct.ID)
			}
		} else if (IconButton{Icon: icons.Resume, Label: m.DockerStart(), Disabled: busy}).Clicked(c, "start") {
			a.dockerDo(tc, session.DockerStart, ct.ID)
		}
		if (IconButton{Icon: icons.Reconnect, Label: m.DockerRestart(), Disabled: busy}).Clicked(c, "restart") {
			a.dockerDo(tc, session.DockerRestart, ct.ID)
		}
		if ct.State == "paused" {
			if (IconButton{Icon: icons.Resume, Label: m.DockerUnpause(), Disabled: busy, Active: true}).Clicked(c, "unpause") {
				a.dockerDo(tc, session.DockerUnpause, ct.ID)
			}
		} else if (IconButton{Icon: icons.Pause, Label: m.DockerPause(), Disabled: busy || ct.State != "running"}).Clicked(c, "pause") {
			a.dockerDo(tc, session.DockerPause, ct.ID)
		}
		if (IconButton{Icon: icons.Logs, Label: m.DockerLogs()}).Clicked(c, "logs") {
			a.dockerLogs(tc, ct.ID, ct.Name)
		}
		if (IconButton{Icon: icons.Terminal, Label: m.DockerShell(), Disabled: ct.State != "running"}).Clicked(c, "shell") {
			d.detailOpen = false
			a.dockerShell(tc, ct.ID)
		}
		ui.Spacer(c)
		if busy {
			ui.Spinner(c)
		}
		if (IconButton{Icon: icons.Delete, Label: m.DockerRemove(), Danger: true, Disabled: busy}).Clicked(c, "rm") {
			d.confirm, d.confirmRef, d.confirmImg = true, ct.ID, false
		}
	})
}

// containerEntries are the menu of the container id: what the buttons
// of its details do. None once it is gone.
func (a *App) containerEntries(m locales.Messages, tc *conn, id string) []menuEntry {
	d := a.docker
	i := slices.IndexFunc(d.info.Containers, func(ct session.Container) bool { return ct.ID == id })
	if i < 0 {
		return nil
	}
	ct := d.info.Containers[i]
	busy := d.busy != ""
	do := func(action session.DockerAction) func() { return func() { a.dockerDo(tc, action, ct.ID) } }
	es := []menuEntry{{Icon: icons.Info, Label: m.DockerDetails(), Do: func() {
		d.imgOpen = false
		a.openContainer(ct)
	}}, menuSep}
	if ct.Running() {
		es = append(es, menuEntry{Icon: icons.Halt, Label: m.DockerStop(), Disabled: busy, Do: do(session.DockerStop)})
	} else {
		es = append(es, menuEntry{Icon: icons.Resume, Label: m.DockerStart(), Disabled: busy, Do: do(session.DockerStart)})
	}
	es = append(es, menuEntry{Icon: icons.Reconnect, Label: m.DockerRestart(), Disabled: busy, Do: do(session.DockerRestart)})
	if ct.State == "paused" {
		es = append(es, menuEntry{Icon: icons.Resume, Label: m.DockerUnpause(), Disabled: busy, Do: do(session.DockerUnpause)})
	} else {
		es = append(es, menuEntry{Icon: icons.Pause, Label: m.DockerPause(), Disabled: busy || ct.State != "running", Do: do(session.DockerPause)})
	}
	return append(es, menuSep,
		menuEntry{Icon: icons.Logs, Label: m.DockerLogs(), Do: func() { a.dockerLogs(tc, ct.ID, ct.Name) }},
		menuEntry{Icon: icons.Terminal, Label: m.DockerShell(), Disabled: ct.State != "running", Do: func() {
			d.detailOpen, d.imgOpen = false, false
			a.dockerShell(tc, ct.ID)
		}},
		menuSep,
		menuEntry{Icon: icons.Delete, Label: m.DockerRemove(), Danger: true, Disabled: busy, Do: func() {
			d.confirm, d.confirmRef, d.confirmImg = true, ct.ID, false
		}},
	)
}

// imageEntries are the menu of the image id. None once it is gone.
func (a *App) imageEntries(m locales.Messages, tc *conn, id string) []menuEntry {
	d := a.docker
	i := slices.IndexFunc(d.info.Images, func(im session.Image) bool { return im.ID == id })
	if i < 0 {
		return nil
	}
	return []menuEntry{
		{Icon: icons.Info, Label: m.DockerDetails(), Do: func() { a.openImage(tc, id) }},
		menuSep,
		{Icon: icons.Delete, Label: m.DockerRemoveImage(), Danger: true, Disabled: d.busy != "", Do: func() {
			d.confirm, d.confirmRef, d.confirmImg = true, id, true
		}},
	}
}

func (a *App) imagesView(c *ui.Context, m locales.Messages, tok theme.Tokens, tc *conn) {
	d := a.docker
	is := d.info.Images
	if len(is) == 0 {
		panelNote(c, tok, m.DockerNoImages())
		return
	}
	if !d.imgOpen || d.imgSel >= len(is) {
		d.imgSel = -1
	}
	d.imgList.Selected = &d.imgSel
	cols := []ui.TableColumn{{Title: m.DockerColImage(), MinWidth: 120}, {Title: m.ColSize(), Width: 72, Align: ui.End}, {Title: m.DockerColCreated(), Width: 110}}
	tbl := ui.Table(c.Key("images"), &d.imgList, cols, len(is), func(row, col int) {
		im := is[row]
		muted := tok.TextMuted
		if d.imgSel == row {
			muted = tok.AccentText
		}
		cell := ui.Row(c).AlignItems(ui.Center).Grow(1).MinWidth(0).AlignSelf(ui.Stretch).Children(func() {
			switch col {
			case 0:
				ui.Text(c, im.Ref()).FontSize(theme.Footnote).SingleLine().Tooltip(im.Ref() + " · " + shortID(im.ID))
			case 1:
				ui.Spacer(c)
				ui.Text(c, im.Size).FontSize(theme.Footnote).TextColor(muted).SingleLine()
			case 2:
				ui.Text(c, im.Created).FontSize(theme.Footnote).TextColor(muted).SingleLine()
			}
		})
		a.contextMenu(cell, func() []menuEntry { return a.imageEntries(m, tc, im.ID) })
	}).Grow(1).Shrink(1).MinHeight(0).FillWidth()
	if (tbl.Changed() || tbl.Submitted()) && d.imgSel >= 0 && d.imgSel < len(is) {
		a.openImage(tc, is[d.imgSel].ID)
	}
	if d.imgSel < 0 {
		panelNote(c, tok, m.DockerPickImage())
	}
}

// dockerConfirm asks before a container or an image is removed.
func (a *App) dockerConfirm(c *ui.Context, m locales.Messages, tc *conn) {
	d := a.docker
	if !d.confirm {
		return
	}
	title, msg, action := m.DockerRemove(), m.DockerRemoveConfirm(a.dockerName(d.confirmRef)), session.DockerRemove
	if d.confirmImg {
		title, msg, action = m.DockerRemoveImage(), m.DockerRemoveImageConfirm(a.dockerName(d.confirmRef)), session.DockerRemoveImage
	}
	if ui.AlertDialog(c, &d.confirm, title, msg, m.Cancel(), m.Delete()) == 1 {
		a.dockerDo(tc, action, d.confirmRef)
	}
}

// dockerName is how the panel names the container or image id.
func (a *App) dockerName(id string) string {
	for _, ct := range a.docker.info.Containers {
		if ct.ID == id {
			return ct.Name
		}
	}
	for _, im := range a.docker.info.Images {
		if im.ID == id {
			return im.Ref()
		}
	}
	return shortID(id)
}

// shortID is a Docker ID as docker shows it: 12 hex digits, without the
// "sha256:".
func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// dockerLogsView is the sheet of a container's last lines of output.
func (a *App) dockerLogsView(c *ui.Context, m locales.Messages, tok theme.Tokens, tc *conn) {
	d := a.docker
	if !d.logsOpen {
		return
	}
	sheet(ui.Modal(c, &d.logsOpen, func() {
		ui.Column(c).Gap(12).Width(720).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
				tile(c, icons.Logs, theme.Brand, 36)
				ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
					sheetTitle(c, m.DockerLogsOf(d.logsName))
					ui.Text(c, m.DockerLogsNote(strconv.Itoa(dockerLogLines))).FontSize(theme.Caption).TextColor(tok.TextMuted)
				})
				if d.logsBusy {
					ui.Spinner(c)
				}
				if (IconButton{Icon: icons.Refresh, Label: m.Refresh(), Disabled: d.logsBusy}).Clicked(c, "logsrefresh") {
					a.dockerLogs(tc, d.logsRef, d.logsName)
				}
			})
			ui.Scroll(c.Key("logs")).Height(420).FillWidth().Radius(theme.RadiusControl).Background(tok.Fill).Children(func() {
				text := d.logs
				switch {
				case d.logsErr != "":
					text = d.logsErr
				case !d.logsBusy && strings.TrimSpace(text) == "":
					text = m.DockerNoLogs()
				}
				t := ui.Text(c, text).FontSize(theme.Caption).Selectable().Padding(10, 12)
				if a.fonts.term != "" {
					t.Font(a.fonts.term)
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Copy(), false, false, d.logsBusy || d.logs == "") {
					c.WriteClipboard(d.logs)
					c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
				}
				if pill(c, m.Close(), true, false, false) {
					d.logsOpen = false
				}
			})
		})
	}), tok)
}
