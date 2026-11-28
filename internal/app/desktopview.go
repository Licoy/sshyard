package app

import (
	"image"
	"math"
	"runtime"
	"strconv"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/rdp"
	"github.com/Licoy/sshyard/internal/rdpinput"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// wheelUnits are the RDP wheel units a DIP scrolled is worth: a notch is
// 120, and about 40 DIPs.
const wheelUnits = 3

// desktopStateColor is the dot of a remote desktop's state.
func desktopStateColor(tok theme.Tokens, st rdp.State) ui.Color {
	switch st {
	case rdp.Connected:
		return tok.Success
	case rdp.Connecting, rdp.Reconnecting, rdp.Idle:
		return tok.Warning
	case rdp.Failed:
		return tok.Danger
	}
	return tok.TextMuted
}

// desktopPane draws remote desktop pane n: its header, then the desktop.
func (a *App) desktopPane(c *ui.Context, t *layout.Tab, n *layout.Node, cn *conn, tok theme.Tokens, m locales.Messages) {
	d := cn.rd
	st := d.s.Status()
	focused := t.Focus == n.ID
	multi := len(t.Panes()) > 1
	ui.Column(c.Key("headbar")).Shrink(0).FillWidth().Children(func() {
		title := a.pv(cn.host.Label())
		leadMin := paneHeadIcon + 8 + titleRoom(c, title) + 7 + 8
		head := a.paneHead(c, cn, m, func() {
			dot(c, desktopStateColor(tok, st.State))
			hostAvatar(c, tok, a.liveHost(cn.host), paneHeadIcon)
			tt := ui.Text(c, title).FontWeight(600).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
			if !focused && multi {
				tt.TextColor(tok.TextMuted)
			}
			if isTemp(cn.host.ID) {
				tempBadge(c, m, tok)
			}
			ui.Text(c, a.pv(cn.host.Target())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
			if st.State == rdp.Connected && st.Width > 0 {
				ui.Text(c, strconv.Itoa(st.Width)+"×"+strconv.Itoa(st.Height)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(0)
			}
		}, leadMin, a.desktopActions(t, n, cn, m), nil)
		if head.Clicked() {
			t.Focus = n.ID
			a.focusReq = cn.id
		}
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	})
	a.desktopBody(c, t, n, cn, tok, m)
}

// desktopActions are the actions of a remote desktop's header.
func (a *App) desktopActions(t *layout.Tab, n *layout.Node, cn *conn, m locales.Messages) []barAction {
	on := func(do func()) func() {
		return func() {
			t.Focus = n.ID
			do()
		}
	}
	st := cn.rd.s.Status()
	var acts []barAction
	if isTemp(cn.host.ID) {
		acts = append(acts, barAction{Key: "saveas", Button: IconButton{Icon: icons.Save, Label: m.SaveAsHost(), Size: Tiny},
			Do: on(func() { a.SaveTempAs(cn.id) })})
	}
	acts = append(acts,
		barAction{Key: "cad", Button: IconButton{Icon: icons.SendKeys, Label: m.RdpSendCtrlAltDel(), Size: Tiny, Disabled: st.State != rdp.Connected},
			Do: on(func() {
				cn.rd.s.CtrlAltDel()
				a.focusReq = cn.id
			})},
		barAction{Key: "reconnect", Button: IconButton{Icon: icons.Reconnect, Label: m.Reconnect(), Size: Tiny, Disabled: !a.canReconnectDesktop(cn)},
			Do: on(func() { a.reconnectDesktopPane(cn) })},
		barAction{Key: "splitright", Button: IconButton{Icon: icons.SplitRight, Label: m.SplitRight(), Size: Tiny},
			Do: on(func() { a.SplitFocused(layout.Right) })},
		barAction{Key: "splitdown", Button: IconButton{Icon: icons.SplitDown, Label: m.SplitDown(), Size: Tiny},
			Do: on(func() { a.SplitFocused(layout.Bottom) })},
	)
	return acts
}

// canReconnectDesktop reports whether remote desktop cn can connect again
// (or unlock to start) now.
func (a *App) canReconnectDesktop(cn *conn) bool {
	if cn == nil || cn.rd == nil {
		return false
	}
	st := cn.rd.s.Status()
	if st.State == rdp.Idle && cn.waitingSecrets {
		return !a.unlock.busy
	}
	switch st.State {
	case rdp.Failed, rdp.Disconnected, rdp.Reconnecting:
		return true
	}
	return false
}

// reconnectDesktopPane connects remote desktop cn again, unlocking the
// secrets first when it waits for them.
func (a *App) reconnectDesktopPane(cn *conn) bool {
	if !a.canReconnectDesktop(cn) {
		return false
	}
	a.focusReq = cn.id
	cn.handled, cn.restoredQuiet = nil, false
	if cn.waitingSecrets || a.connectionNeedsSecrets(cn.host) {
		a.withSecrets(func() {
			if a.conns[cn.id] == cn {
				cn.waitingSecrets = false
				a.reconnectDesktop(cn)
			}
		})
		return true
	}
	a.reconnectDesktop(cn)
	return true
}

// desktopBody is the desktop itself: the remote screen, scaled to fit the
// pane where it is larger, taking the pointer and the keys while it has
// the focus; and what it is doing while it is not connected.
func (a *App) desktopBody(c *ui.Context, t *layout.Tab, n *layout.Node, cn *conn, tok theme.Tokens, m locales.Messages) {
	d := cn.rd
	cn.drawn = a.frame
	st := d.s.Status()
	live := st.State == rdp.Connected
	d.refresh()
	body := ui.Box(c.Key("desk")).Grow(1).Shrink(1).MinHeight(0).FillWidth().Focusable().FocusRing(false).Clip().
		Label(m.RemoteDesktop() + " " + cn.host.Label())
	if a.focusReq == cn.id && a.page == pageWorkspace {
		body.Focus()
		a.focusReq = 0
	}
	has := body.Focused()
	svc := c.Services()
	d.focusChanged(has, live, svc)
	if has && t.Focus != n.ID {
		t.Focus = n.ID
	}
	d.over = body.Hovered()
	if live && d.over && d.inView() && d.ownPointer() {
		body.Cursor(ui.CursorNone)
	}
	body.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown {
			t.Focus = n.ID
			if !has {
				a.focusReq = cn.id
			}
		}
		if a.altTabKey(ev) {
			return true
		}
		if !live {
			if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyEnter && ev.Mods == 0 {
				return a.reconnectDesktopPane(cn)
			}
			return false
		}
		return a.desktopInput(d, ev, svc)
	})
	body.Draw(func(p *ui.Painter, r ui.Rect) { d.paint(p, r, tok, live) })
	body.Children(func() {
		if !live {
			a.desktopStatus(c, cn, st, tok, m)
		}
	})
}

// desktopStatus is the card over a remote desktop that is not connected:
// connecting, waiting for the secrets, or why it ended, with a way to
// connect again.
func (a *App) desktopStatus(c *ui.Context, cn *conn, st rdp.Status, tok theme.Tokens, m locales.Messages) {
	d := cn.rd
	waiting := st.State == rdp.Idle && cn.waitingSecrets
	busy := !waiting && (st.State == rdp.Connecting || st.State == rdp.Reconnecting || st.State == rdp.Idle)
	col := tok.Warning
	if st.State == rdp.Failed {
		col = tok.Danger
	}
	text := a.desktopStatusText(m, cn.host, st)
	if waiting {
		text = m.SecretsLockedTitle()
		if a.unlocked() {
			text = m.Connect()
		}
	}
	// Over the last frame, while there is one: a notice at the bottom;
	// over none, a card in the middle.
	if d.bm != nil {
		ui.Column(c.Key("desk-overlay")).Fill().Justify(ui.End).Children(func() {
			ui.Row(c.Key("banner")).Margin(0, 8, 8, 8).Padding(6, 8, 6, 12).MinHeight(36).Gap(8).AlignItems(ui.Center).
				Radius(theme.RadiusControl).Background(tok.Inset).Border(1, col.Alpha(0.4)).Children(func() {
				if busy {
					ui.Spinner(c)
				} else {
					ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(col)
				}
				ui.Text(c, a.pv(text)).FontSize(theme.Footnote).Grow(1).Shrink(1)
				a.desktopStatusButtons(c, cn, waiting, m)
			})
		})
		return
	}
	ui.Column(c.Key("desk-card")).Fill().Center().Children(func() {
		card := ui.Column(c.Key("card")).Width(360).Shrink(1).Margin(theme.Space(4)).Padding(theme.Space(5)).Gap(theme.Space(3)).AlignItems(ui.Center).
			Radius(theme.RadiusCard).Background(tok.Inset).Border(1, tok.Line)
		theme.Shadow(tok.Card).On(card)
		card.Children(func() {
			hostAvatar(c, tok, a.liveHost(cn.host), 44)
			ui.Text(c, a.pv(cn.host.Label())).FontSize(theme.Title3).FontWeight(600).SingleLine()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if busy {
					ui.Spinner(c)
				} else if st.State == rdp.Failed {
					ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(col)
				}
				ui.Text(c, a.pv(text)).FontSize(theme.Footnote).TextColor(tok.TextMuted).TextAlign(ui.Center).Shrink(1)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				a.desktopStatusButtons(c, cn, waiting, m)
			})
		})
	})
}

// desktopStatusButtons are what a remote desktop not connected offers:
// unlocking the secrets it waits for, connecting again, editing its host.
func (a *App) desktopStatusButtons(c *ui.Context, cn *conn, waiting bool, m locales.Messages) {
	if waiting {
		label := m.SecretsRetry()
		if a.unlocked() {
			label = m.Connect()
		}
		if (IconButton{Icon: icons.Key, Label: label, Size: Small, Disabled: a.unlock.busy}).Clicked(c, "unlock-connection") {
			a.reconnectDesktopPane(cn)
		}
		return
	}
	if a.canReconnectDesktop(cn) {
		if (IconButton{Icon: icons.Reconnect, Label: m.Reconnect(), Size: Small}).Clicked(c, "reconnect") {
			a.reconnectDesktopPane(cn)
		}
	}
	if h, ok := a.host(cn.host.ID); ok && !isTemp(h.ID) {
		if (IconButton{Icon: icons.Edit, Label: m.EditHost(), Size: Small}).Clicked(c, "edit-host") {
			a.openEditor(&h)
		}
	}
}

// refresh brings the bitmap the desktop is drawn from up to date, and the
// pointer's.
func (d *desktop) refresh() {
	next := 1 - d.cur
	img, ver, _ := d.s.CopyFrame(d.frames[next], d.vers[next])
	if img != nil && ver != d.shown {
		d.frames[next], d.vers[next] = img, ver
		if d.bm == nil {
			d.bm = ui.NewBitmap(img)
		} else {
			d.bm.Update(img)
		}
		d.cur, d.shown = next, ver
	}
	if p := d.s.Pointer(); p.Version != d.ptr.Version {
		d.ptr = p
		d.ptrBm = nil
		if p.Image != nil && !p.Hidden && !p.Default {
			d.ptrBm = ui.NewBitmap(p.Image)
		}
	}
}

// ownPointer reports whether the desktop draws the pointer itself: a
// shape of the server's, or none.
func (d *desktop) ownPointer() bool { return d.ptr.Hidden || d.ptrBm != nil }

// size is the desktop's size in its pixels, 0 when there is none yet.
func (d *desktop) size() (int, int) {
	if f := d.frames[d.cur]; f != nil && d.bm != nil {
		return f.Rect.Dx(), f.Rect.Dy()
	}
	return 0, 0
}

// inView reports whether the pointer is over the desktop's picture.
func (d *desktop) inView() bool {
	w, h := d.size()
	return w > 0 && d.at.X >= 0 && d.at.Y >= 0 && d.at.X < w && d.at.Y < h
}

// paint draws the desktop in r, asking the server for a desktop of r's
// size in the window's pixels when it fits the pane.
func (d *desktop) paint(p *ui.Painter, r ui.Rect, tok theme.Tokens, live bool) {
	scale := p.Scale()
	if scale <= 0 {
		scale = 1
	}
	d.scale = scale
	d.ask(int(math.Round(float64(r.W*scale))), int(math.Round(float64(r.H*scale))), int(math.Round(float64(scale*100))))
	p.Fill(r, tok.Inset, 0)
	w, h := d.size()
	if w == 0 {
		d.view = ui.Rect{}
		return
	}
	// Shown at its own size, centered; scaled down where it is larger.
	dw, dh := float32(w)/scale, float32(h)/scale
	k := min(float32(1), r.W/dw, r.H/dh)
	vw, vh := dw*k, dh*k
	d.view = ui.Rect{X: r.X + (r.W-vw)/2, Y: r.Y + (r.H-vh)/2, W: vw, H: vh}
	p.Image(d.bm, d.view, ui.FillBox)
	if !live {
		p.Fill(d.view, ui.RGBA(0, 0, 0, 0.35), 0)
		return
	}
	if d.over && d.ptrBm != nil && d.inView() {
		pw, ph := d.ptrBm.Size()
		per := vw / float32(w) // DIPs a desktop pixel
		x := d.view.X + float32(d.at.X-d.ptr.HotX)*per
		y := d.view.Y + float32(d.at.Y-d.ptr.HotY)*per
		p.Image(d.ptrBm, ui.Rect{X: x, Y: y, W: float32(pw) * per, H: float32(ph) * per}, ui.FillBox)
	}
}

// ask asks for a desktop of w×h pixels at scale percent when the host
// fits it to the pane, and starts the session once it knows the size.
func (d *desktop) ask(w, h, scale int) {
	if w < 1 || h < 1 {
		return
	}
	o := rdpOptions(d.h)
	fixed := o.Width > 0 && o.Height > 0
	if fixed {
		w, h = o.Width, o.Height
	}
	w = max(200, min(8192, w&^1))
	h = max(200, min(8192, h))
	scale = max(100, min(500, scale))
	want := [3]int{w, h, scale}
	if want == d.asked {
		return
	}
	first := d.asked[0] == 0
	d.asked = want
	if first || !d.begun {
		cfg := d.cfg
		if cfg != nil {
			d.s.SetConfig(cfg(w, h, scale))
		}
		if d.started && !d.begun {
			d.begun = true
			d.s.Start()
		}
		return
	}
	if !fixed {
		d.s.Resize(w, h, scale)
	}
}

// at maps a point of the pane's body to the desktop's pixels.
func (d *desktop) point(x, y float32) image.Point {
	w, h := d.size()
	if w == 0 || d.view.W <= 0 {
		return image.Point{}
	}
	px := int(math.Floor(float64((x - d.view.X) / d.view.W * float32(w))))
	py := int(math.Floor(float64((y - d.view.Y) / d.view.H * float32(h))))
	return image.Point{X: px, Y: py}
}

// clamp keeps pt on the desktop.
func (d *desktop) clamp(pt image.Point) image.Point {
	w, h := d.size()
	return image.Point{X: max(0, min(w-1, pt.X)), Y: max(0, min(h-1, pt.Y))}
}

// setShown tells the server whether the desktop is seen, so that it
// sends nothing while it is not.
func (d *desktop) setShown(shown bool) {
	if d.hidden == !shown {
		return
	}
	d.hidden = !shown
	d.s.SetVisible(shown)
}

// focusChanged follows the desktop gaining and losing the keyboard: what
// is held is let go when it leaves, and the local clipboard is offered
// when it comes back.
func (d *desktop) focusChanged(has, live bool, svc ui.Services) {
	if has == d.focused {
		return
	}
	d.focused = has
	if !has {
		d.releaseAll()
		return
	}
	if live {
		d.offerClipboard(svc)
	}
}

// releaseAll lets go of every key and button held down on the desktop.
func (d *desktop) releaseAll() {
	d.s.ReleaseAll()
	clear(d.held)
	d.composing, d.winDown, d.winPending = false, false, false
}

// offerClipboard offers the local clipboard's text to the remote side,
// when it changed.
func (d *desktop) offerClipboard(svc ui.Services) {
	if rdpOptions(d.h).NoClipboard {
		return
	}
	if text := svc.ReadClipboard(); text != "" && text != d.clip {
		d.clip = text
		d.s.SetClipboard(text)
	}
}

// pointerButton is the RDP button of a pointer event's.
func pointerButton(b int) (rdp.Button, bool) {
	switch b {
	case 0:
		return rdp.ButtonLeft, true
	case 1:
		return rdp.ButtonRight, true
	case 2:
		return rdp.ButtonMiddle, true
	case 3:
		return rdp.ButtonX1, true
	case 4:
		return rdp.ButtonX2, true
	}
	return 0, false
}

// desktopInput sends an event of the desktop's to the server; it reports
// whether it took it. Keys go by their place on the keyboard, as RDP
// sends them, but for the app's own shortcuts and the text an input
// method composes.
func (a *App) desktopInput(d *desktop, ev ui.InputEvent, svc ui.Services) bool {
	switch ev.Kind {
	case ui.InputPointerMove:
		d.at = d.point(ev.X, ev.Y)
		if len(d.held) > 0 || d.inView() {
			pt := d.clamp(d.at)
			d.s.MouseMove(pt.X, pt.Y)
		}
		return true
	case ui.InputPointerDown:
		b, ok := pointerButton(ev.Button)
		if !ok {
			return false
		}
		d.at = d.point(ev.X, ev.Y)
		if !d.inView() {
			return true
		}
		d.held[b] = true
		d.s.MouseButton(d.at.X, d.at.Y, b, true)
		return true
	case ui.InputPointerUp:
		b, ok := pointerButton(ev.Button)
		if !ok || !d.held[b] {
			return ok
		}
		delete(d.held, b)
		pt := d.clamp(d.point(ev.X, ev.Y))
		d.s.MouseButton(pt.X, pt.Y, b, false)
		return true
	case ui.InputScroll:
		if ev.Mods&ui.Ctrl != 0 && runtime.GOOS == "darwin" {
			return false
		}
		d.at = d.point(ev.X, ev.Y)
		pt := d.clamp(d.at)
		d.wheelX += ev.DX * wheelUnits
		d.wheelY -= ev.DY * wheelUnits
		dx, dy := int(d.wheelX), int(d.wheelY)
		d.wheelX -= float32(dx)
		d.wheelY -= float32(dy)
		if dx != 0 || dy != 0 {
			d.s.Wheel(pt.X, pt.Y, dx, dy)
		}
		return true
	case ui.InputKeyDown:
		if isAppShortcut(a.settings.Shortcuts, ev.Key, ev.Mods) {
			return false
		}
		if d.composing {
			return false
		}
		if ev.Key == ui.KeyV && (ev.Mods&ui.Ctrl != 0 || ev.Mods&ui.Shift != 0) || ev.Key == ui.KeyInsert && ev.Mods&ui.Shift != 0 {
			d.offerClipboard(svc)
		}
		return d.key(ev.Code, true)
	case ui.InputKeyUp:
		if d.composing {
			return false
		}
		return d.key(ev.Code, false)
	case ui.InputCompose:
		d.composing = ev.Text != ""
		if d.composing {
			d.composed = true
		}
		return false
	case ui.InputText:
		// Typed keys went as keys; what an input method commits goes as
		// text.
		if !d.composed && !d.composing {
			return true
		}
		d.composed, d.composing = false, false
		for _, r := range ev.Text {
			d.s.Unicode(r, true)
			d.s.Unicode(r, false)
		}
		return true
	case ui.InputCommand:
		return a.desktopCommand(d, ev.Text, svc)
	}
	return false
}

// desktopCommand runs an edit command of the menus on the desktop as its
// keys do on the server's: ⌘C on macOS copies there with Ctrl+C.
func (a *App) desktopCommand(d *desktop, cmd string, svc ui.Services) bool {
	var key ui.KeyCode
	switch cmd {
	case "copy":
		key = ui.CodeKeyC
	case "cut":
		key = ui.CodeKeyX
	case "paste", "pasteAndMatchStyle":
		d.offerClipboard(svc)
		key = ui.CodeKeyV
	case "selectAll":
		key = ui.CodeKeyA
	case "undo":
		key = ui.CodeKeyZ
	case "redo":
		key = ui.CodeKeyY
	default:
		return false
	}
	// The Command key held for it is not the Windows key here.
	d.winPending = false
	if d.winDown {
		d.winDown = false
		d.sendCode(ui.CodeSuperLeft, false)
	}
	d.sendCode(ui.CodeControlLeft, true)
	d.sendCode(key, true)
	d.sendCode(key, false)
	d.sendCode(ui.CodeControlLeft, false)
	return true
}

// key sends a key pressed or released; it reports whether it took it.
// On macOS the Command key reaches the server as the Windows key only
// once another key is pressed with it, or alone: the menus' edit
// commands take it with C, V and the others.
func (d *desktop) key(code ui.KeyCode, down bool) bool {
	if runtime.GOOS == "darwin" && (code == ui.CodeSuperLeft || code == ui.CodeSuperRight) {
		switch {
		case down:
			d.winPending = true
		case d.winDown:
			d.winDown = false
			d.sendCode(code, false)
		case d.winPending:
			d.winPending = false
			d.sendCode(code, true)
			d.sendCode(code, false)
		}
		return true
	}
	if down && d.winPending {
		d.winPending, d.winDown = false, true
		d.sendCode(ui.CodeSuperLeft, true)
	}
	return d.sendCode(code, down)
}

// sendCode sends the scancode of code; false for a key RDP has none for.
func (d *desktop) sendCode(code ui.KeyCode, down bool) bool {
	sc, ext, ok := rdpinput.Scancode(code)
	if !ok {
		return false
	}
	d.s.Key(sc, ext, down)
	return true
}
