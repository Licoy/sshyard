package app

import (
	"runtime"
	"strconv"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// dragSession is a pane, terminal or files, dragged by its tab or its
// header; Tab is the tab's ID when it is dragged by its tab, which moves
// the tab when it is dropped on another, 0 by its header.
type dragSession struct{ ID, Tab int }

// paneDrop is what a pane takes dropped: a dragSession or a dragHost.
type paneDrop interface{ paneDrop() }

func (dragSession) paneDrop() {}
func (dragHost) paneDrop()    {}

// dropTarget is where a drag over the panes would land.
type dropTarget struct {
	pane int // the node
	edge layout.Edge
}

// paneNode is the node of the pane showing a session, 0 for none.
func (a *App) paneNode(id int) int {
	if _, n := a.ws.Locate(id); n != nil {
		return n.ID
	}
	return 0
}

// edgeFor is where on a pane w×h a drop at x, y lands: the outer quarter
// of a side splits the pane there, the middle replaces it.
func edgeFor(x, y, w, h float32) layout.Edge {
	if w <= 0 || h <= 0 {
		return layout.Center
	}
	fx, fy := x/w, y/h
	type cand struct {
		d float32
		e layout.Edge
	}
	best := cand{1, layout.Center}
	for _, c := range []cand{{fx, layout.Left}, {1 - fx, layout.Right}, {fy, layout.Top}, {1 - fy, layout.Bottom}} {
		if c.d < 0.25 && c.d < best.d {
			best = c
		}
	}
	return best.e
}

// edgeRect is the part of a pane a drop at edge would take.
func edgeRect(r ui.Rect, e layout.Edge) ui.Rect {
	switch e {
	case layout.Left:
		r.W /= 2
	case layout.Right:
		r.X += r.W / 2
		r.W /= 2
	case layout.Top:
		r.H /= 2
	case layout.Bottom:
		r.Y += r.H / 2
		r.H /= 2
	}
	return r
}

func stateColor(tok theme.Tokens, st session.State) ui.Color {
	switch st {
	case session.Connected:
		return tok.Success
	case session.Connecting, session.Reconnecting, session.Idle:
		return tok.Warning
	case session.Failed:
		return tok.Danger
	}
	return tok.TextMuted
}

// tabLabel names a tab by its focused pane's host.
func (a *App) tabLabel(t *layout.Tab) string {
	if t.Title != "" {
		return t.Title
	}
	cn := a.conns[t.Focused().ID]
	if cn == nil {
		return ""
	}
	if n := len(t.Panes()); n > 1 {
		return a.connLabel(cn) + " +" + strconv.Itoa(n-1)
	}
	return a.connLabel(cn)
}

// kindIcon is the icon of a pane kind, the "-fill" one when on.
func kindIcon(k layout.Kind, on bool) *icons.Icon {
	switch {
	case k == layout.Files && on:
		return icons.FolderOn
	case k == layout.Files:
		return icons.Folder
	case k == layout.AI && on:
		return icons.RobotOn
	case k == layout.AI:
		return icons.Robot
	case k == layout.Broadcast && on:
		return icons.BroadcastOn
	case k == layout.Broadcast:
		return icons.Broadcast
	case k == layout.Logs && on:
		return icons.FileTextOn
	case k == layout.Logs:
		return icons.FileText
	case k == layout.Snippets:
		return icons.API
	case k == layout.History:
		return icons.History
	case k == layout.Monitor && on:
		return icons.MonitorOn
	case k == layout.Monitor:
		return icons.Monitor
	case k == layout.Usage && on:
		return icons.UsageOn
	case k == layout.Usage:
		return icons.Usage
	case k == layout.HostInfo && on:
		return icons.InfoOn
	case k == layout.HostInfo:
		return icons.Info
	case k == layout.Docker && on:
		return icons.DockerOn
	case k == layout.Docker:
		return icons.Docker
	case k == layout.Tmux && on:
		return icons.TmuxOn
	case k == layout.Tmux:
		return icons.Tmux
	case on:
		return icons.TerminalOn
	}
	return icons.Terminal
}

// OpenFilesBeside opens a files pane on the focused pane's host, to its
// right.
func (a *App) OpenFilesBeside() {
	t := a.ws.ActiveTab()
	cn := a.focused()
	if t == nil || cn == nil {
		return
	}
	a.OpenFiles(cn.host.ID, openAt{pane: t.Focus, edge: a.filesEdge()}, cn)
}

// filesEdge is where a files pane opened from a terminal goes, as the
// settings say.
func (a *App) filesEdge() layout.Edge {
	if a.settings.FilesEdge == settings.FilesBelow {
		return layout.Bottom
	}
	return layout.Right
}

// openFilesLabel names opening a files pane at edge of a pane.
func openFilesLabel(m locales.Messages, edge layout.Edge) string {
	if edge == layout.Bottom {
		return m.OpenFilesBelow()
	}
	return m.OpenFilesBeside()
}

// SplitFocused opens a terminal on the focused pane's host beside it.
func (a *App) SplitFocused(edge layout.Edge) {
	t := a.ws.ActiveTab()
	cn := a.focused()
	if t == nil || cn == nil {
		return
	}
	a.Connect(cn.host.ID, openAt{pane: t.Focus, edge: edge})
}

// CloseFocused closes the focused pane.
func (a *App) CloseFocused() {
	if cn := a.focusedPane(); cn != nil {
		a.CloseSession(cn.id)
	}
}

func (a *App) closeTab(t *layout.Tab) {
	for _, p := range t.Panes() {
		a.CloseSession(p.Item.ID)
	}
}

// tempBadge marks a tab or pane on a temporary host of quick connect.
func tempBadge(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ui.Box(c.Key("temp")).Shrink(0).Padding(1, 6).Radius(theme.Capsule).Background(tok.Accent.Alpha(0.14)).Children(func() {
		ui.Text(c, m.Temporary()).FontSize(theme.Caption).FontWeight(500).TextColor(tok.Accent).SingleLine()
	})
}

// scpBadge marks a files pane on a server without SFTP, whose files move
// with scp and its shell.
func scpBadge(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ui.Box(c.Key("scp")).Shrink(0).Padding(1, 6).Radius(theme.Capsule).Background(tok.Warning.Alpha(0.14)).
		Label(m.ScpFallbackNote()).Tooltip(m.ScpFallbackNote()).Children(func() {
		ui.Text(c, "SCP").FontSize(theme.Caption).FontWeight(500).TextColor(tok.Warning).SingleLine()
	})
}

// toolbarTrail is the toolbar's right padding: a small gap before the
// window buttons where they sit on the right (Windows, Linux), and the
// toolbar's own margin where they do not (macOS, on the left).
func toolbarTrail(bar ui.TitleBar) float32 {
	if bar.Right > 0 {
		return bar.Right + 6
	}
	return 12
}

// paneHeadHeight is the height of a pane's header: its title's line and
// room round it; paneHeadIcon the size of its host's tile.
const (
	paneHeadHeight = 28
	paneHeadIcon   = 16
)

func (a *App) tabStrip(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	// The title bar's part over the content: the tabs on its foot, the
	// app's own buttons at the end, clear of the window buttons. The
	// settings cover the sessions: no tabs while they show, and the
	// settings button, lit, closes them.
	bar := c.TitleBar()
	ui.Row(c.Key("toolbar")).Grow(1).Shrink(1).MinWidth(0).FillHeight().Padding(0, toolbarTrail(bar), 0, 0).Gap(8).AlignItems(ui.Center).Children(func() {
		if a.page == pageSettings {
			ui.Spacer(c)
		} else {
			a.tabs(c, m, tok)
		}
		barLine(c, "appbtns").Gap(theme.Space(1)).Children(func() {
			a.appButtons(c, m)
		})
	})
}

func (a *App) terminalPage(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	t := a.ws.ActiveTab()
	if t == nil {
		a.finding = false
		f, still := &a.field, c.Preferences().ReduceMotion
		page := ui.Column(c.Key("welcome")).Fill().Center().Gap(12)
		// A background image of the user's own shows without the dots.
		// The page only watches the pointer: as the innermost handler it
		// sees the presses on the buttons too, which must stay theirs.
		if !a.hasBackground() {
			page.Draw(func(p *ui.Painter, r ui.Rect) { f.draw(p, r, tok, still) }).
				HandleInput(func(ev ui.InputEvent) bool {
					if f.handle(ev, still) {
						c.Invalidate()
					}
					return false
				})
		}
		f.pane, f.content, f.controls = page.Bounds(), ui.Rect{}, f.controls[:0]
		page.Children(func() {
			f.hold(ui.Image(c, a.logo).Size(64, 64).Shrink(0).Radius(theme.RadiusSheet).Label(m.AppName()), false)
			f.hold(ui.Text(c, m.WelcomeTitle()).FontWeight(600).FontSize(theme.Title1).MarginY(4), false)
			f.hold(ui.Text(c, m.WelcomeHint()).FontSize(theme.Body).TextColor(tok.TextMuted), false)
			ui.Row(c.Key("start")).FillWidth().Wrap().Justify(ui.Center).Gap(theme.Space(2)).MarginY(theme.Space(2)).Children(func() {
				start := func(label string, primary bool) bool {
					el, ok := pillEl(c, label, primary, false, false)
					f.hold(el, true)
					return ok
				}
				if start(m.QuickMigration(), true) {
					a.openSettings(prefMigration)
				}
				if start(m.QuickConnect(), false) {
					a.OpenQuickConnect()
				}
				if start(m.NewHost(), false) {
					a.openEditor(nil)
				}
			})
		})
		return
	}
	a.field.leave()
	a.settleFocus(t)
	cn := a.focused()
	if cn == nil || cn.kind != layout.Terminal {
		a.finding = false
	} else {
		if a.finding {
			if wait := cn.tv.updateFind(a.query, true, c.Now()); wait > 0 {
				c.After(wait)
			}
		}
		n := len(cn.tv.matches)
		if a.match >= n {
			a.match = max(n-1, 0)
		}
		bar := ui.FindBar(c.Key("find"), &a.finding, &a.query, n, &a.match)
		if bar.Changed() || (a.finding && cn.tv.current != a.match) {
			cn.tv.showMatch(a.match)
		}
		if !a.finding {
			cn.tv.updateFind("", false, c.Now())
		}
	}
	a.dropPreview = nil
	ui.Box(c).Fill().Padding(paneGap / 2).Children(func() { a.nodeView(c, t, t.Root, tok, m) })
	a.syncPasteDialog(c, m)
}

func (a *App) ratio(n *layout.Node) *float32 {
	p, ok := a.pct[n.ID]
	if !ok {
		v := n.Ratio * 100
		if v <= 0 || v >= 100 {
			v = 50
		}
		p = &v
		a.pct[n.ID] = p
	}
	return p
}

func (a *App) nodeView(c *ui.Context, t *layout.Tab, n *layout.Node, tok theme.Tokens, m locales.Messages) {
	if n == nil {
		return
	}
	if n.Leaf() {
		a.paneView(c, t, n, tok, m)
		return
	}
	p := a.ratio(n)
	first := func() { a.nodeView(c, t, n.A, tok, m) }
	second := func() { a.nodeView(c, t, n.B, tok, m) }
	key := c.Key("split:" + strconv.Itoa(n.ID))
	if n.Axis == layout.Row {
		ui.SplitPercent(key, p, first, second).Fill()
	} else {
		ui.SplitVerticalPercent(key, p, first, second).Fill()
	}
	n.Ratio = *p / 100
}

// onTokens are the tokens for what is drawn on bg: tok, unless the other
// appearance's text reads better on it (a dark terminal scheme in light
// mode, or a light one in dark mode).
func onTokens(tok theme.Tokens, bg ui.Color) theme.Tokens {
	other := theme.LightTokens()
	if !tok.Dark {
		other = theme.DarkTokens()
	}
	if theme.Contrast(other.Text, bg) > theme.Contrast(tok.Text, bg) {
		return other
	}
	return tok
}

// paneGap is the gutter between two panes, and from the edges of the
// content: half of it round each pane and half round all.
const paneGap = 4

func (a *App) paneView(c *ui.Context, t *layout.Tab, n *layout.Node, tok theme.Tokens, m locales.Messages) {
	cn := a.conns[n.Item.ID]
	if cn == nil {
		return
	}
	multi := len(t.Panes()) > 1
	focused := t.Focus == n.ID
	pal := a.termPalette(tok)
	bg := pal.Background
	if cn.kind != layout.Terminal {
		bg = tok.Inset
	}
	// Each pane is a card on the canvas: a hairline edge, a soft shadow,
	// and the same gutter all round, paneGap between two panes and from
	// the edges of the content. (The gutter is the padding of a box the
	// card fills: a margin on a card that fills would push it out on the
	// right and at the bottom.)
	var pane ui.Element
	ui.Box(c.Key("pw:" + strconv.Itoa(n.ID))).Fill().Padding(paneGap / 2).Children(func() {
		pane = ui.Column(c.Key("pane:" + strconv.Itoa(n.ID))).Grow(1).Shrink(1).MinHeight(0).Radius(theme.RadiusCard).Clip().Background(a.contentFill(bg, paneAlpha))
		a.paneBackground(c, pane, tok, bg)
		theme.Shadow(tok.Card).On(pane)
		// The ask names a pane by number. Outline that pane in the accent
		// color, wider than the focus hairline, until the question is gone.
		if a.clipAsk != nil && a.clipAsk.open && a.clipAsk.pane == cn.id {
			pane.Border(2, tok.Accent)
		} else if a.bc.synced[cn.id] {
			// What is typed goes to it too.
			pane.Border(2, tok.Accent.Alpha(0.7))
		} else if multi && focused {
			pane.Border(1, tok.Accent.Alpha(0.7))
		} else {
			pane.Border(1, tok.Line)
		}
		var st session.Status
		if cn.s != nil {
			st = cn.s.Status()
		}
		pane.Children(func() {
			if cn.kind == layout.AI {
				a.aiPane(c, t, n, cn, tok, m)
				return
			}
			if isTool(cn.kind) {
				a.toolPaneView(c, cn, tok, m)
				return
			}
			// The header is drawn on the pane's color: on a terminal scheme
			// of the other appearance it takes that appearance's tokens, so
			// its text and buttons keep their contrast.
			headTok := onTokens(tok, bg)
			ui.Themed(c.Key("headbar"), headTok.Apply(*c.Theme())).Background(ui.Transparent).Shrink(0).FillWidth().Children(func() {
				tok := headTok
				title := a.pv(cn.host.Label())
				switch cn.kind {
				case layout.Files:
					title = m.FilesPane(a.pv(cn.host.Label()))
				}
				leadMin := paneHeadIcon + 8 + titleRoom(c, title)
				if cn.s != nil {
					leadMin += 7 + 8
				}
				head := a.paneHead(c, cn, m, func() {
					// How its session is first; then a terminal shows its host
					// as the sidebar does, the others what they are, in its
					// color.
					if cn.s != nil {
						dot(c, stateColor(tok, st.State))
					}
					if cn.kind == layout.Terminal {
						hostAvatar(c, tok, a.liveHost(cn.host), paneHeadIcon)
					} else {
						softTile(c, kindIcon(cn.kind, focused), hostTint(tok, a.liveHost(cn.host)), paneHeadIcon)
					}
					tt := ui.Text(c, title).FontWeight(600).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
					if !focused && multi {
						tt.TextColor(tok.TextMuted)
					}
					if cn.kind == layout.Terminal && cn.s != nil && cn.s.Term.Modes().MouseTracking {
						// The program takes the mouse: say so, and how to select.
						ui.Row(c.Key("mouse")).Shrink(0).AlignItems(ui.Center).Label(m.MouseReporting()).Tooltip(m.MouseReporting()).Children(func() {
							ui.Icon(c, icons.Mouse.SVG).Size(Tiny, Tiny).TextColor(tok.Accent)
						})
					}
					if a.bc.synced[cn.id] {
						ui.Row(c.Key("synced")).Shrink(0).AlignItems(ui.Center).Label(m.BroadcastLiveOn()).Tooltip(m.BroadcastLiveOn()).Children(func() {
							ui.Icon(c, icons.KeyboardOn.SVG).Size(Tiny, Tiny).TextColor(tok.Accent)
						})
					}
					if isTemp(cn.host.ID) {
						tempBadge(c, m, tok)
					}
					if cn.fs != nil && cn.fs.remote.client != nil && cn.fs.remote.client.SCP() {
						scpBadge(c, m, tok)
					}
					if cn.kind == layout.Terminal {
						a.paneForwards(c, m, tok, cn.host.ID)
					}
					ui.Text(c, a.pv(cn.host.Target())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
				}, leadMin, a.paneActions(c, t, n, cn, focused, m), nil)
				if head.Clicked() {
					t.Focus = n.ID
					a.focusReq = cn.id
				}
				ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
			})
			switch cn.kind {
			case layout.Files:
				a.filesBody(c, t, n, cn, pane.Bounds().W, tok, m)
			default:
				a.termElement(c, t, n, cn, &pal, m)
				a.xferBanner(c, cn, tok, m)
				if st.State == session.Connected {
					h := cn.host
					if cur, ok := a.host(cn.host.ID); ok {
						h = cur
					}
					if a.metricsWanted(h) {
						a.metricsBar(c, cn, tok, m)
					}
				}
			}
			if cn.s != nil && st.State != session.Connected {
				a.banner(c, cn, st, tok, m)
			}
		})
	})
	a.paneDrops(n, cn, pane, tok)
}

// paneActions are the actions of a session pane's header, as many as fit
// shown and the others in its More menu: a terminal finds and opens its
// host's files, a files pane shows this computer's, its transfers, follows
// the terminal and opens one; both split.
func (a *App) paneActions(c *ui.Context, t *layout.Tab, n *layout.Node, cn *conn, focused bool, m locales.Messages) []barAction {
	// Its action works on the pane it is in.
	on := func(do func()) func() {
		return func() {
			t.Focus = n.ID
			do()
		}
	}
	var acts []barAction
	if isTemp(cn.host.ID) {
		acts = append(acts, barAction{Key: "saveas", Button: IconButton{Icon: icons.Save, Label: m.SaveAsHost(), Size: Tiny},
			Do: on(func() { a.SaveTempAs(cn.id) })})
	}
	if cn.kind == layout.Terminal {
		finding := a.finding && focused
		acts = append(acts,
			barAction{Key: "find", Button: IconButton{Icon: icons.Search, Label: m.Find(), Size: Tiny, Active: finding},
				Do: on(func() { a.finding = !finding })},
			barAction{Key: "files", Button: IconButton{Icon: icons.Folder, Label: m.OpenFilesHere(), Size: Tiny, Disabled: !remoteOnly(cn)},
				Do: on(func() { a.OpenFiles(cn.host.ID, openAt{pane: n.ID, edge: a.filesEdge()}, cn) })})
	} else {
		acts = append(acts,
			barAction{Key: "dual", Button: IconButton{Icon: icons.LocalDisk, On: icons.LocalDiskOn, Label: m.ShowLocalFiles(), Toggle: true, Active: a.settings.FilesDual, Size: Tiny},
				Do: on(a.toggleDual)},
			a.transfersAction(c, cn, m))
		if f, ok := a.followAction(t, n, cn, m); ok {
			acts = append(acts, f)
		}
		acts = append(acts, barAction{Key: "term", Button: IconButton{Icon: icons.Terminal, Label: m.OpenTerminalBeside(), Size: Tiny},
			Do: on(func() { a.Connect(cn.host.ID, openAt{pane: n.ID, edge: layout.Right}) })})
	}
	return append(acts,
		barAction{Key: "splitright", Button: IconButton{Icon: icons.SplitRight, Label: m.SplitRight(), Size: Tiny},
			Do: on(func() { a.SplitFocused(layout.Right) })},
		barAction{Key: "splitdown", Button: IconButton{Icon: icons.SplitDown, Label: m.SplitDown(), Size: Tiny},
			Do: on(func() { a.SplitFocused(layout.Bottom) })})
}

func (a *App) paneDrops(n *layout.Node, cn *conn, pane ui.Element, tok theme.Tokens) {
	a.paneBounds[cn.id] = pane.Bounds()
	// Drops: a session from a tab or a pane, a host from the sidebar. An
	// element takes the one type it last asked for, so both go as one.
	v, over := ui.DragOver[paneDrop](pane)
	edge := layout.Center
	if x, y, ok := pane.PointerPosition(); ok {
		b := pane.Bounds()
		edge = edgeFor(x, y, b.W, b.H)
	}
	if s, ok := v.(dragSession); ok && (s.ID == cn.id || !a.fits(s.ID, n, edge)) {
		// Onto itself, where it is; or a second files or AI pane into a
		// tab, which has one at most.
		over = false
	}
	if over {
		a.dropPreview = &dropTarget{pane: n.ID, edge: edge}
		pane.DrawOver(func(p *ui.Painter, r ui.Rect) {
			dropPreview(p, r, edge, tok)
		})
	}
	if v, ok := ui.Drop[paneDrop](pane); ok {
		switch v := v.(type) {
		case dragSession:
			if v.ID != cn.id && a.fits(v.ID, n, edge) {
				a.ws.Move(v.ID, n.ID, edge)
				a.focusReq = v.ID
			}
		case dragHost:
			a.Connect(v.ID, openAt{pane: n.ID, edge: edge})
		}
		a.saveLayout()
	}
}

// dropPreview shows on a pane r the region a drop at edge would give
// the item: a translucent accent fill of it, inset as a pane is, with an
// accent outline.
func dropPreview(p *ui.Painter, r ui.Rect, edge layout.Edge, tok theme.Tokens) {
	er := edgeRect(r, edge)
	const inset = 4
	er = ui.Rect{X: er.X + inset, Y: er.Y + inset, W: er.W - 2*inset, H: er.H - 2*inset}
	rad := theme.RadiusCard - inset
	p.Fill(er, tok.Accent.Alpha(0.28), rad)
	p.Stroke(er, tok.Accent, rad, 2)
}

func (a *App) termElement(c *ui.Context, t *layout.Tab, n *layout.Node, cn *conn, pal *theme.Terminal, m locales.Messages) {
	tv := cn.tv
	term := ui.Box(c.Key("term")).Grow(1).FillWidth().Focusable().FocusRing(false).Clip().
		Cursor(ui.CursorText).Label(m.Terminal() + " " + cn.host.Label())
	if a.focusReq == cn.id && a.page == pageWorkspace {
		tv.blink.at = time.Time{}
		term.Focus()
		a.focusReq = 0
	}
	tv.local = isLocal(cn.host.ID)
	// Files dropped on a local terminal type their paths at its prompt,
	// as macOS Terminal does; on a remote one they are sent with rz or
	// trz, as the user chooses.
	if tv.local {
		if files := term.DroppedFiles(); len(files) > 0 {
			// pasteNow writes to this terminal alone, never to those typed
			// into with it: the paths name this computer's files.
			if s := dropText(files, runtime.GOOS, a.shell(cn.host.ID).ID); s != "" {
				tv.pasteNow(s)
			}
			t.Focus = n.ID
			a.focusReq = cn.id
		}
	} else if a.settings.TerminalTransfers && cn.s.Status().State == session.Connected {
		if files := term.DroppedFiles(); len(files) > 0 {
			a.dropOnRemote(cn, files)
			t.Focus = n.ID
		}
	}
	has := term.Focused()
	tv.focusChanged(has)
	if cn.s.Term.Modes().MouseTracking {
		term.Cursor(ui.CursorDefault) // the program's, not a text's
	}
	if tv.link.on {
		term.Cursor(ui.CursorPointer)
	}
	if !term.Hovered() && !tv.barDrag {
		tv.barHover = false
	}
	if tv.barHover || tv.barDrag {
		term.Cursor(ui.CursorDefault) // the scrollbar's, not a text's
	}
	if has && t.Focus != n.ID {
		t.Focus = n.ID
	}
	svc := c.Services()
	tb := term.Bounds()
	entries := func() []menuEntry { return a.termEntries(t, n, cn, tv, svc, m) }
	term.HandleInput(func(ev ui.InputEvent) bool {
		// A left click gives the terminal the keyboard back. A right click
		// must not: focusing here inserts the find bar and the tab dot
		// before the next frame, which would move the menu's place.
		if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
			t.Focus = n.ID
			if !has {
				a.focusReq = cn.id
			}
		}
		tv.shortcuts = a.settings.Shortcuts
		tv.fanout = a.fanoutFrom(cn)
		tv.holdPaste = func(text string) bool { return a.holdPaste(cn, text) }
		tv.rightClick, tv.copyOnSelect = a.settings.TerminalRightClick, a.settings.CopyOnSelect
		tv.openLink = svc.OpenURL
		tv.zoomFont = a.stepTermFont
		// While a transfer has the shell, Ctrl+C stops it and the other
		// keys go nowhere.
		if a.xferKey(cn, ev) {
			return true
		}
		// A program reading the mouse takes the right click too.
		return tv.input(ev, svc) || a.contextPress(tb, ev, entries)
	})
	if term.Shortcut(ui.Shift, ui.KeyF10) {
		a.openContext(tb.X, tb.Y, entries)
	}
	term.TextCaret(tv.caret())
	tv.cursorBlink = a.settings.TerminalCursorBlink
	tv.lineNumbers, tv.timestamps = a.settings.TerminalLineNumbers, a.settings.TerminalTimestamps
	tv.cursorStyle = vt.CursorBlock
	switch a.settings.TerminalCursorStyle {
	case settings.CursorBar:
		tv.cursorStyle = vt.CursorBar
	case settings.CursorUnderline:
		tv.cursorStyle = vt.CursorUnderline
	}
	tv.clear = a.clearContent
	tv.hl, tv.hlOverride, tv.hlFullScreen = a.highlighter(), a.settings.Highlight.Override, a.settings.Highlight.FullScreen
	term.Draw(func(p *ui.Painter, r ui.Rect) { tv.paint(p, r, pal, has) })
}

// termEntries are the context menu of a terminal pane n of tab t.
func (a *App) termEntries(t *layout.Tab, n *layout.Node, cn *conn, tv *termView, svc ui.Services, m locales.Messages) []menuEntry {
	sel := tv.SelectedText()
	noFiles, noAI := !remoteOnly(cn), !canAI(cn)
	edge, other := a.filesEdge(), layout.Bottom
	if edge == layout.Bottom {
		other = layout.Right
	}
	focus := func(do func()) func() {
		return func() {
			t.Focus = n.ID
			do()
		}
	}
	entries := []menuEntry{
		{Icon: icons.Copy, Label: m.Copy(), Disabled: sel == "", Do: func() { svc.WriteClipboard(sel) }},
		{Icon: icons.Paste, Label: m.Paste(), Do: func() { tv.pasteClipboard(svc) }},
		a.bound(actFind, menuEntry{Icon: icons.Search, Label: m.Find(), Do: focus(func() { a.finding = true })}),
		{Icon: icons.Clear, Label: m.ClearScreen(), Do: func() { tv.send([]byte{0x0c}) }},
	}
	if isStream(cn.host) {
		entries = append(entries, a.bound(actSendBreak, menuEntry{Icon: icons.SendBreak, Label: m.SendBreak(), Disabled: !canBreak(cn), Do: func() { a.sendBreak(cn) }}))
	}
	return append(entries, []menuEntry{
		menuSep,
		a.bound(actSplitRight, menuEntry{Icon: icons.SplitRight, Label: m.SplitRight(), Do: focus(func() { a.SplitFocused(layout.Right) })}),
		a.bound(actSplitDown, menuEntry{Icon: icons.SplitDown, Label: m.SplitDown(), Do: focus(func() { a.SplitFocused(layout.Bottom) })}),
		a.bound(actOpenFiles, menuEntry{Icon: icons.Folder, Label: openFilesLabel(m, edge), Disabled: noFiles, Do: focus(a.OpenFilesBeside)}),
		{Label: openFilesLabel(m, other), Disabled: noFiles, Do: focus(func() {
			a.OpenFiles(cn.host.ID, openAt{pane: n.ID, edge: other}, cn)
		})},
		a.bound(actOpenAI, menuEntry{Icon: icons.Robot, Label: m.OpenAIRight(), Disabled: noAI, Do: focus(func() {
			a.OpenAI(cn.host.ID, openAt{pane: n.ID, edge: layout.Right}, cn.id)
		})}),
		{Label: m.OpenAIBelow(), Disabled: noAI, Do: focus(func() {
			a.OpenAI(cn.host.ID, openAt{pane: n.ID, edge: layout.Bottom}, cn.id)
		})},
		{Icon: icons.Robot, Label: m.AskAI(), Disabled: sel == "" || noAI, Do: func() { a.AskAI(cn, sel) }},
		menuSep,
		a.bound(actClosePane, menuEntry{Icon: icons.Close, Label: m.ClosePane(), Do: func() { a.closeSoon(cn.id) }}),
	}...)
}

func (a *App) banner(c *ui.Context, cn *conn, st session.Status, tok theme.Tokens, m locales.Messages) {
	waiting := st.State == session.Idle && cn.waitingSecrets
	col := tok.Warning
	if st.State == session.Failed {
		col = tok.Danger
	}
	// A rounded notice floating inside the bottom of the pane card.
	ui.Row(c.Key("banner")).Margin(0, 8, 8, 8).Padding(6, 8, 6, 12).MinHeight(36).Gap(8).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		if !waiting && (st.State == session.Connecting || st.State == session.Reconnecting || st.State == session.Idle) {
			ui.Spinner(c)
		} else {
			ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(col)
		}
		text := a.pv(a.statusText(m, cn.host, st, c.Now()))
		if waiting {
			text = m.SecretsLockedTitle()
			if a.unlocked() {
				text = m.Connect()
			}
		}
		ui.Text(c, text).FontSize(theme.Footnote).Grow(1).Shrink(1)
		if waiting {
			label := m.SecretsRetry()
			if a.unlocked() {
				label = m.Connect()
			}
			if (IconButton{Icon: icons.Key, Label: label, Size: Small, Disabled: a.unlock.busy}).Clicked(c, "unlock-connection") {
				a.withSecrets(func() {
					if a.conns[cn.id] == cn {
						cn.waitingSecrets = false
						cn.restorePending = false
						cn.restoredQuiet = false
						cn.s.Start()
					}
				})
			}
		}
		switch st.State {
		case session.Failed, session.Disconnected, session.Exited, session.Reconnecting:
			if session.Classify(st.Err) == session.KindHostChanged {
				if (IconButton{Icon: icons.HostKey, Label: m.HostKeyReview(), Size: Small}).Clicked(c, "review-host-key") {
					a.reviewHostKey(cn)
				}
				if !cn.restoredQuiet {
					break
				}
			}
			if (IconButton{Icon: icons.Reconnect, Label: m.Reconnect(), Size: Small}).Clicked(c, "reconnect") {
				cn.handled = nil
				cn.restoredQuiet = false
				if a.connectionNeedsSecrets(cn.host) {
					a.withSecrets(func() {
						if a.conns[cn.id] == cn {
							cn.s.Reconnect()
						}
					})
				} else {
					cn.s.Reconnect()
				}
			}
		}
	})
	if st.State == session.Reconnecting {
		d := st.Retry.Sub(c.Now()) % time.Second
		if d <= 0 {
			d = time.Second
		}
		c.After(d + 50*time.Millisecond)
	}
}
