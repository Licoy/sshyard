package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// tabHeight and tabWidth size a tab, which stands on the title bar's
// foot; tabs narrow alike when there are many, as a browser's, down to
// tabMinWidth. tabInset is the track's padding at its sides, room for the
// active tab's flare, which curves as wide: the content's gutter. Beside
// the sidebar the track has none at its start: the first tab stands on the
// sidebar's separator, its side going on up the line. tabGap is the room
// between two; tabRadius rounds a tab's top.
const (
	tabHeight   = 36
	tabWidth    = 200
	tabMinWidth = 132
	tabInset    = paneGap
	tabGap      = 2
	tabRadius   = theme.RadiusControl
)

// tabMoreWidth is the room the button listing the tabs that do not fit
// takes after the track, and tabAddWidth the new tab's: a 28 DIP
// IconButton and its gap.
const (
	tabMoreWidth = 28 + tabGap
	tabAddWidth  = 28 + tabGap
)

// tabsShown is the first tab and how many the title bar shows in room
// DIPs (0 before it was measured: all), keeping the active one among them
// and, as far as it can, the first it showed before.
func tabsShown(n, active, start int, room float32) (int, int) {
	fit := func(room float32) int {
		return max(int((room-2*tabInset+tabGap)/(tabMinWidth+tabGap)), 1)
	}
	if room <= 0 || fit(room) >= n {
		return 0, n
	}
	k := fit(room - tabMoreWidth)
	if active >= 0 && active < start {
		start = active
	}
	if active >= start+k {
		start = active - k + 1
	}
	return min(max(start, 0), n-k), k
}

// tabWheelRoom is how far a touchpad scrolls the title bar's tabs by one.
const tabWheelRoom = tabMinWidth / 2

// tabWheelStep is how many tabs a scroll of d DIPs moves the title bar's
// by, toward the last for a positive d, and what of it is left for the
// next: a notch of a mouse wheel moves one, a touchpad's small moves
// (precise) one for each tabWheelRoom they add up to. rest is what the
// last left.
func tabWheelStep(rest, d float32, precise bool) (int, float32) {
	if !precise {
		switch {
		case d > 0:
			return 1, 0
		case d < 0:
			return -1, 0
		}
		return 0, 0
	}
	if rest*d < 0 {
		rest = 0 // turned back
	}
	rest += d
	n := int(rest / tabWheelRoom)
	return n, rest - float32(n)*tabWheelRoom
}

// tabs stand on the title bar's foot as a browser's: the active one in
// the content's fill, its top rounded and its foot flared into the
// content it joins; the others flat, muted and apart by short lines, lit
// on hover in a rounded box clear of the foot. Each shows the host as the sidebar does, its close button
// while it is active or under the pointer, and at once as the pointer
// comes, a tip of where its panes are. The tabs that do not fit go in the
// popover of a button after them, and the button for a new tab follows;
// the wheel, or a touchpad either way, moves along them, and the active
// one comes back into view once another is chosen.
func (a *App) tabs(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	area := ui.Row(c.Key("tabarea")).Grow(1).Shrink(1).MinWidth(0).FillHeight().Gap(tabGap).AlignItems(ui.End).ClipX()
	active := -1
	if a.page == pageWorkspace {
		active = a.ws.Active
	}
	room := area.Bounds().W - tabAddWidth
	keep := active
	if active >= 0 && active < len(a.ws.Tabs) {
		if id := a.ws.Tabs[active].ID; id != a.tabKept {
			a.tabKept, a.tabWheeled = id, false
		}
	}
	if a.tabWheeled {
		keep = -1 // scrolled away from the active one
	}
	n := len(a.ws.Tabs)
	start, k := tabsShown(n, keep, a.tabStart, room)
	a.tabStart = start
	area.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputScroll || k >= n || ev.Mods&ui.Ctrl != 0 {
			return false
		}
		d := ev.DY
		if ev.DX*ev.DX > ev.DY*ev.DY {
			d = ev.DX
		}
		step, rest := tabWheelStep(a.tabWheel, d, ev.Precise)
		a.tabWheel = rest
		if to := min(max(a.tabStart+step, 0), n-k); to != a.tabStart {
			a.tabStart, a.tabWheeled = to, true
		}
		return true
	})
	if room != a.tabRoom {
		// Measured anew (a resize): lay the tabs out again for it.
		a.tabRoom = room
		c.Invalidate()
	}
	// A copy: closing tabs from one's menu changes the list as it builds.
	shown := slices.Clone(a.ws.Tabs[start : start+k])
	// Read when the tabs paint, once all have been built.
	hovered := make([]bool, len(shown))
	fill := a.contentFill(tok.Canvas, canvasAlpha)
	area.Children(func() {
		if len(shown) > 0 {
			lead := float32(tabInset)
			if !a.sidebarHidden() {
				lead = 0 // the first tab stands on the sidebar's separator
			}
			track := ui.Row(c.Key("tabs")).Shrink(1).MinWidth(0).Height(tabHeight).Padding(0, tabInset, 0, lead).Gap(tabGap).AlignItems(ui.End)
			track.Children(func() {
				for j, t := range shown {
					i := start + j
					a.tab(c, m, tok, fill, t, i, i == active, j, hovered, active-start)
				}
			})
		}
		// The buttons after the tabs, level with their text.
		barLine(c, "tabbtns").Gap(tabGap).Children(func() {
			if k < len(a.ws.Tabs) {
				a.moreTabs(c, m, tok, start, k)
			} else {
				a.tabsOpen = false
			}
			// With no tab, the welcome page leads to a new one.
			if len(a.ws.Tabs) > 0 {
				a.newTabButton(c, m)
			}
		})
	})
}

// newTabButton is the "+" after the tabs, whose drop-down opens a new
// tab: a new connection (quick connect, a new host, a local terminal) or
// one to a saved host. Its context menu opens the AI on the focused
// session's host.
func (a *App) newTabButton(c *ui.Context, m locales.Messages) {
	cn := a.focused()
	add := a.menuButton(c, "newtab", IconButton{Icon: icons.Add, Label: m.NewTab()}, func() []menuEntry {
		return a.newTabEntries(c, m)
	})
	if a.menu.key != "newtab" {
		a.newTabQuery = ""
	}
	if cn == nil {
		return
	}
	a.contextMenu(add, func() []menuEntry {
		link := 0
		if cn.kind == layout.Terminal {
			link = cn.id
		}
		noAI := !canAI(cn)
		beside := func(edge layout.Edge) func() {
			return func() {
				if t := a.ws.ActiveTab(); t != nil {
					a.OpenAI(cn.host.ID, openAt{pane: t.Focus, edge: edge}, link)
				}
			}
		}
		return []menuEntry{
			a.bound(actNewTab, menuEntry{Icon: icons.Add, Label: m.NewTab(), Do: func() { a.Connect(cn.host.ID, openAt{}) }}),
			{Icon: icons.Robot, Label: m.OpenAI(), Disabled: noAI, Do: func() { a.OpenAIFor(cn.host.ID) }},
			{Icon: icons.SplitRight, Label: m.OpenAIRight(), Disabled: noAI, Do: beside(layout.Right)},
			{Icon: icons.SplitDown, Label: m.OpenAIBelow(), Disabled: noAI, Do: beside(layout.Bottom)},
			menuSep,
			a.bound(actStopAllAI, menuEntry{Icon: icons.Stop, Label: m.StopAllAI(), Do: a.StopAllAI}),
		}
	})
}

// newTabEntries are the entries of the "+" after the tabs: a search of
// the saved hosts, a new connection, then the saved hosts, each with its
// tile. While searching, only the hosts found show, and Enter connects to
// the first.
func (a *App) newTabEntries(c *ui.Context, m locales.Messages) []menuEntry {
	fixed := []menuEntry{
		a.bound(actQuickConnect, menuEntry{Icon: icons.Quick, Label: m.QuickConnectMenu(), Do: a.OpenQuickConnect}),
		{Icon: icons.Host, Label: m.NewHost(), Do: func() { a.openEditor(nil) }},
		a.localEntry(m),
	}
	hosts := a.store.List()
	if len(hosts) == 0 {
		return fixed
	}
	query := strings.TrimSpace(a.newTabQuery)
	if query != "" {
		hosts = a.store.Search(query)
	}
	entries := []menuEntry{{Search: &a.newTabQuery, Label: m.SearchHosts(), Do: func() {
		if query != "" && len(hosts) > 0 {
			a.Connect(hosts[0].ID, openAt{})
		}
	}}}
	if query == "" {
		entries = append(entries, fixed...)
	}
	if len(hosts) == 0 {
		return append(entries, menuEntry{Label: m.PaletteNoResults(), Disabled: true})
	}
	entries = append(entries, menuSep)
	for _, h := range hosts {
		entries = append(entries, menuEntry{
			Lead:  func() { hostAvatar(c, currentTokens(c), h, 16) },
			Label: a.pv(h.Label()),
			Do:    func() { a.Connect(h.ID, openAt{}) },
		})
	}
	return entries
}

// tab is the tab t, the i-th, the j-th of those shown, whose hover it
// keeps in hovered[j]; activeAt is the place of the active one among them,
// fill the content's, which the active one takes.
func (a *App) tab(c *ui.Context, m locales.Messages, tok theme.Tokens, fill ui.Color, t *layout.Tab, i int, on bool, j int, hovered []bool, activeAt int) {
	cn := a.conns[t.Focused().ID]
	// Its contents center on the band's middle, with the traffic lights
	// and the buttons, over room at its foot, which joins the content.
	foot := max(bandHeight(c)-tabHeight, 0)
	chip := ui.Row(c.Key("tab:"+strconv.Itoa(t.ID))).Height(tabHeight).Gap(8).Padding(0, 6, foot, 10).Width(tabWidth).Shrink(1).MinWidth(tabMinWidth).
		AlignItems(ui.Center).Radius(tabRadius, tabRadius, 0, 0).Cursor(ui.CursorPointer)
	if cn != nil {
		chip.Drag(dragSession{ID: cn.id, Tab: t.ID})
		a.tearTab(c, chip, t)
	}
	over := a.tabDrops(c, chip, t, tok, foot)
	hovered[j] = chip.Hovered() && !over
	lit := tok.Hover
	if chip.Pressed() {
		lit = tok.Press
	}
	if on {
		chip.Background(fill)
	}
	tint, tinted := tok.TintNamed(t.Color)
	chip.Draw(func(p *ui.Painter, r ui.Rect) {
		box := ui.Rect{X: r.X, Y: r.Y + 3, W: r.W, H: r.H - foot - 6}
		switch {
		case on:
			tabEdge(p, r, fill, tok.Line, j == 0 && !a.sidebarHidden())
			tabTintBar(p, r, r.Y+2, tint, tinted)
			return
		case hovered[j]:
			// Lit as a capsule round its contents, not joining the content.
			p.Fill(box, lit, tabRadius)
			tabTintBar(p, r, box.Y, tint, tinted)
			return
		}
		if tinted {
			// A tab of the user's color, faintly washed in it.
			wash := float32(0.08)
			if tok.Dark {
				wash = 0.14
			}
			p.Fill(box, tint.Alpha(wash), tabRadius)
			tabTintBar(p, r, box.Y, tint, tinted)
		}
		if next := j + 1; next < len(hovered) && next != activeAt && !hovered[j] && !hovered[next] {
			x, mid := r.X+r.W+tabGap/2, r.Y+(r.H-foot)/2
			p.Line(x, mid-7, x, mid+7, 1, tok.Line)
		}
	})
	chip.Children(func() {
		switch {
		case cn != nil && isTool(cn.kind):
			softTile(c, kindIcon(cn.kind, false), tok.Accent, 20)
		case cn != nil:
			hostAvatar(c, tok, a.liveHost(cn.host), 20)
		}
		txt := ui.Text(c, a.tabLabel(t)).SingleLine().Grow(1).Shrink(1).MinWidth(0).FontSize(theme.Body)
		switch {
		case on:
			txt.FontWeight(600)
		case !hovered[j]:
			txt.TextColor(tok.TextMuted)
		}
		if cn != nil && isTemp(cn.host.ID) {
			tempBadge(c, m, tok)
		}
		if cn != nil && cn.s != nil {
			dot(c, stateColor(tok, cn.s.Status().State))
		}
		a.tabAIMarks(c, t, tok, m)
		a.tabSyncMark(c, t, tok, m)
		x := (IconButton{Icon: icons.Close, Label: m.CloseTab(), Size: Small}).Show(c, "x")
		if !on && !hovered[j] {
			x.Invisible()
		}
		if x.Clicked() {
			a.closeTab(t)
		}
	})
	a.contextMenuOr(chip, func() []menuEntry { return a.tabEntries(m, i) }, a.middleClose(t, chip.Bounds()))
	if chip.Clicked() {
		a.ws.Active = i
		a.page = pageWorkspace
		a.focusReq = t.Focused().ID
	}
	if hovered[j] && !chip.Pressed() && !chip.Dragging() {
		a.tabTip(c, m, tok, chip, t)
	}
}

// tabDrops makes the tab t, whose chip it is, take another tab dropped on
// it, which goes before t or after it as the pointer is over t's first half
// or its second; while one is dragged over, an accent line shows where. It
// reports whether one is.
func (a *App) tabDrops(c *ui.Context, chip ui.Element, t *layout.Tab, tok theme.Tokens, foot float32) bool {
	x, _, _ := chip.PointerPosition()
	after := x >= chip.Bounds().W/2
	s, over := ui.DragOver[dragSession](chip)
	over = over && s.Tab != 0 && s.Tab != t.ID
	if over {
		chip.DrawOver(func(p *ui.Painter, r ui.Rect) {
			if after {
				r.X += r.W - 2
			}
			p.Fill(ui.Rect{X: r.X, Y: r.Y + 6, W: 2, H: r.H - foot - 12}, tok.Accent, 1)
		})
	}
	if s, ok := ui.Drop[dragSession](chip); ok && s.Tab != 0 && a.ws.MoveTab(s.Tab, t.ID, after) {
		a.saveLayout()
		c.Invalidate()
	}
	return over
}

// tabEdge draws the active tab in r into the content below it: its flare,
// in the content's fill, curving out on to the title bar's foot either
// side, and its hairline round the sides and the top, where it stands on
// the title bar. A flush tab, the first beside the sidebar, has no flare
// on its left: its side runs straight down the sidebar's separator, just
// left of it, into the line below.
func tabEdge(p *ui.Painter, r ui.Rect, fill, line ui.Color, flush bool) {
	const R, F = tabRadius, tabInset
	l, rt, top, b := r.X, r.X+r.W, r.Y, r.Y+r.H
	var flare ui.Path
	if !flush {
		flare.MoveTo(l-F, b).QuadTo(l, b, l, b-F).LineTo(l, b).Close()
	}
	flare.MoveTo(rt+F, b).QuadTo(rt, b, rt, b-F).LineTo(rt, b).Close()
	p.FillPath(&flare, fill)
	var edge ui.Path
	if flush {
		edge.MoveTo(l-0.5, b).LineTo(l-0.5, top+R).QuadTo(l-0.5, top+0.5, l+R, top+0.5)
	} else {
		edge.MoveTo(l-F, b-0.5).QuadTo(l+0.5, b-0.5, l+0.5, b-F).LineTo(l+0.5, top+R).QuadTo(l+0.5, top+0.5, l+R, top+0.5)
	}
	edge.LineTo(rt-R, top+0.5).QuadTo(rt-0.5, top+0.5, rt-0.5, top+R).LineTo(rt-0.5, b-F).QuadTo(rt-0.5, b-0.5, rt+F, b-0.5)
	p.StrokePath(&edge, 1, line)
}

// tabTintBar draws the bar of a tab of the user's color (SetTabColor)
// across the top of the tab in r, at y, clear of its rounded corners.
func tabTintBar(p *ui.Painter, r ui.Rect, y float32, tint ui.Color, tinted bool) {
	if tinted {
		p.Fill(ui.Rect{X: r.X + 10, Y: y, W: max(r.W-20, 0), H: 3}, tint, 1.5)
	}
}

// middleClose closes tab t, whose element is at b, as its close button
// does, when the middle button is pressed and let go over it; it is the
// element's HandleInput, taking only the middle button.
func (a *App) middleClose(t *layout.Tab, b ui.Rect) func(ui.InputEvent) bool {
	return func(ev ui.InputEvent) bool {
		if ev.Button != 2 {
			return false
		}
		switch ev.Kind {
		case ui.InputPointerDown:
			return true // and so the release
		case ui.InputPointerUp:
			if !ev.Canceled && ev.X >= 0 && ev.Y >= 0 && ev.X < b.W && ev.Y < b.H && slices.Contains(a.ws.Tabs, t) {
				a.closeTab(t)
			}
			return true
		}
		return false
	}
}

// tabTip is a tab's tip, shown at once under it: the host of each of its
// panes, where it logs in and how its connection is.
func (a *App) tabTip(c *ui.Context, m locales.Messages, tok theme.Tokens, chip ui.Element, t *layout.Tab) {
	ui.Overlay(c, func() {
		tip := ui.Column(c.Key("tabtip")).AttachTo(chip, ui.AnchorBottomLeft, ui.AnchorTopLeft).Margin(6, 0, 0, 0).
			MaxWidth(360).Padding(8, 10).Gap(6).Radius(theme.RadiusControl).Background(tok.Inset).Border(1, tok.GlassEdge).PassThrough()
		tok.Lift.On(tip)
		tip.Children(func() {
			for _, p := range t.Panes() {
				cn := a.conns[p.Item.ID]
				if cn == nil {
					continue
				}
				h := a.liveHost(cn.host)
				ui.Row(c.Key("tip:" + strconv.Itoa(cn.id))).Gap(8).AlignItems(ui.Center).Children(func() {
					a.paneTile(c, tok, cn, 22)
					ui.Column(c).Gap(1).Shrink(1).MinWidth(0).Children(func() {
						title := a.connLabel(cn)
						switch cn.kind {
						case layout.Files:
							title = m.FilesPane(title)
						}
						ui.Text(c, title).FontSize(theme.Footnote).FontWeight(600).SingleLine()
						// In full what the tab's title shows in short.
						if src := a.tabSource(cn); src != "" {
							ui.Text(c, src).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
						}
						var parts []string
						if target := h.Target(); target != "" {
							parts = append(parts, a.pv(target))
						}
						if cn.s != nil {
							parts = append(parts, a.pv(a.statusText(m, h, cn.s.Status(), c.Now())))
						}
						line := strings.Join(parts, " · ")
						ui.Text(c, line).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
					})
				})
			}
		})
	})
}

// moreTabWidth is the width of the popover listing the tabs that do not
// fit; moreTabsHeight the most it grows to before it scrolls.
const (
	moreTabWidth   = 300
	moreTabsHeight = 420
)

// moreTabs is the button after the tabs, whose popover lists those that
// do not fit, the start .. start+k shown: each as its tip shows it, the
// active one lit; choosing one shows it.
func (a *App) moreTabs(c *ui.Context, m locales.Messages, tok theme.Tokens, start, k int) {
	hidden := len(a.ws.Tabs) - k
	b := (IconButton{Icon: icons.Collapse, Label: m.MoreTabs(hidden), Active: a.tabsOpen}).Show(c, "moretabs")
	if b.Clicked() {
		a.tabsOpen = !a.tabsOpen
	}
	ui.PopoverBase(c.Key("moretabspop"), b, &a.tabsOpen, func(panel ui.Element) {
		panel.AttachTo(b, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(6, 0, 0, 0).Width(moreTabWidth).Padding(5).
			Radius(theme.RadiusCard).Background(tok.Inset).Border(1, tok.GlassEdge)
		tok.Lift.On(panel)
		// A copy: closing a tab from its row changes the list as it builds.
		tabs := slices.Clone(a.ws.Tabs)
		panel.Children(func() {
			gutterScroll(c, ui.Scroll(c.Key("list")).MaxHeight(moreTabsHeight).FillWidth()).Children(func() {
				ui.Column(c).Gap(2).FillWidth().Children(func() {
					for i, t := range tabs {
						if i < start || i >= start+k {
							a.moreTab(c, m, tok, t, i)
						}
					}
				})
			})
		})
	})
}

// moreTab is the row of the i-th tab t in the popover of moreTabs: its
// host, where it logs in and how it is connected, and its close button
// under the pointer.
func (a *App) moreTab(c *ui.Context, m locales.Messages, tok theme.Tokens, t *layout.Tab, i int) {
	cn := a.conns[t.Focused().ID]
	on := a.page == pageWorkspace && i == a.ws.Active
	label := a.tabLabel(t)
	row := ui.Row(c.Key("mt:"+strconv.Itoa(t.ID))).FillWidth().Padding(6, 6, 6, 8).Gap(10).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Cursor(ui.CursorPointer).Label(label)
	hov := row.Hovered()
	switch {
	case on, row.Pressed():
		row.Background(tok.Press)
	case hov:
		row.Background(tok.Hover)
	}
	row.HandleInput(a.middleClose(t, row.Bounds()))
	closed := false
	row.Children(func() {
		var sub []string
		if cn != nil {
			h := a.liveHost(cn.host)
			a.paneTile(c, tok, cn, 26)
			if target := h.Target(); target != "" {
				sub = append(sub, a.pv(target))
			}
			if cn.s != nil {
				sub = append(sub, a.pv(a.statusText(m, h, cn.s.Status(), c.Now())))
			}
		}
		ui.Column(c).Gap(1).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, label).FontSize(theme.Footnote).FontWeight(600).SingleLine()
			if len(sub) > 0 {
				ui.Text(c, strings.Join(sub, " · ")).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			}
		})
		if cn != nil && cn.s != nil {
			dot(c, stateColor(tok, cn.s.Status().State))
		}
		x := (IconButton{Icon: icons.Close, Label: m.CloseTab(), Size: Small}).Show(c, "x")
		if !hov {
			x.Invisible()
		}
		if x.Clicked() {
			a.closeTab(t)
			closed = true
		}
	})
	if row.Clicked() && !closed {
		a.ws.Active = i
		a.page = pageWorkspace
		a.focusReq = t.Focused().ID
		a.tabsOpen = false
	}
}

// tabSyncMark marks tab t while what is typed goes to its terminals.
func (a *App) tabSyncMark(c *ui.Context, t *layout.Tab, tok theme.Tokens, m locales.Messages) {
	for _, p := range t.Panes() {
		if a.bc.synced[p.Item.ID] {
			ui.Icon(c, icons.KeyboardOn.SVG).Size(14, 14).Shrink(0).TextColor(tok.Accent).Label(m.BroadcastLiveOn()).Tooltip(m.BroadcastLiveOn())
			return
		}
	}
}

// paneTile is pane cn's tile in a list of panes: a terminal's host, the
// kind of another in its host's color, or in the accent for a tool pane.
func (a *App) paneTile(c *ui.Context, tok theme.Tokens, cn *conn, size float32) ui.Element {
	switch {
	case cn.kind == layout.Terminal:
		return hostAvatar(c, tok, a.liveHost(cn.host), size)
	case isTool(cn.kind):
		return softTile(c, kindIcon(cn.kind, false), tok.Accent, size)
	}
	return softTile(c, kindIcon(cn.kind, false), hostTint(tok, a.liveHost(cn.host)), size)
}

// tabHost is the host of a tab: its focused pane's.
func (a *App) tabHost(t *layout.Tab) string {
	if cn := a.conns[t.Focused().ID]; cn != nil {
		return cn.host.ID
	}
	return ""
}

// tabEntries are the context menu of the i-th tab: closing it, or the tabs
// round it, others, on its host or on others.
func (a *App) tabEntries(m locales.Messages, i int) []menuEntry {
	n := len(a.ws.Tabs)
	if i >= n {
		return nil
	}
	host := a.tabHost(a.ws.Tabs[i])
	same, others := 0, 0
	for _, t := range a.ws.Tabs {
		if a.tabHost(t) == host {
			same++
		} else {
			others++
		}
	}
	closing := func(close func(j int, t *layout.Tab) bool) func() {
		return func() { a.closeTabs(close) }
	}
	t := a.ws.Tabs[i]
	_, saved := a.store.Get(host)
	return append(append([]menuEntry{
		{Icon: icons.Rename, Label: m.RenameTabEllipsis(), Do: func() { a.openTabRename(t) }},
		{Icon: icons.Appearance, Label: m.TabColor(), Sub: a.tabColorEntries(m, t)},
		menuSep,
		{Icon: icons.Duplicate, Label: m.DuplicateSession(), Disabled: host == "", Do: func() { a.duplicateTab(m, t) }},
		{Icon: icons.Copy, Label: m.CopyHostIP(), Disabled: a.tabAddress(t) == "", Do: func() { a.copyTabAddress(t) }},
		{Icon: icons.Locate, Label: m.RevealHostInSidebar(), Disabled: !saved || a.winState != a.main, Do: func() { a.locateHost(host) }},
		menuSep,
	}, a.windowEntries(m, t.ID, 0)...), []menuEntry{
		menuSep,
		{Icon: icons.Close, Label: m.CloseTab(), Do: closing(func(j int, _ *layout.Tab) bool { return j == i })},
		{Label: m.CloseOtherTabs(), Disabled: n < 2, Do: closing(func(j int, _ *layout.Tab) bool { return j != i })},
		{Label: m.CloseTabsLeft(), Disabled: i == 0, Do: closing(func(j int, _ *layout.Tab) bool { return j < i })},
		{Label: m.CloseTabsRight(), Disabled: i == n-1, Do: closing(func(j int, _ *layout.Tab) bool { return j > i })},
		menuSep,
		{Icon: icons.Host, Label: m.CloseHostTabs(), Disabled: same == 0, Do: closing(func(_ int, t *layout.Tab) bool { return a.tabHost(t) == host })},
		{Label: m.CloseOtherHostTabs(), Disabled: others == 0, Do: closing(func(_ int, t *layout.Tab) bool { return a.tabHost(t) != host })},
		menuSep,
		{Icon: icons.Clear, Label: m.CloseAllTabs(), Danger: true, Do: closing(func(int, *layout.Tab) bool { return true })},
	}...)
}

// tabColorEntries are the entries of tab t's color submenu: none, then
// each of the theme's tints, the one it has checked.
func (a *App) tabColorEntries(m locales.Messages, t *layout.Tab) []menuEntry {
	out := []menuEntry{{Label: m.TabColorNone(), Checked: t.Color == "", Do: func() { a.SetTabColor(t.ID, "") }}}
	names := colorNames(m)
	for i, key := range theme.TintNames {
		out = append(out, menuEntry{Swatch: key, Label: names[i], Checked: t.Color == key, Do: func() { a.SetTabColor(t.ID, key) }})
	}
	return out
}

// SetTabColor gives the tab of id the tint of name (theme.TintNames), or
// none for "" or a name it does not know.
func (a *App) SetTabColor(id int, name string) {
	if !slices.Contains(theme.TintNames, name) {
		name = ""
	}
	for _, t := range a.allTabs() {
		if t.ID == id {
			t.Color = name
		}
	}
}

// duplicateTab opens a new session on tab t's host in a new tab: a files
// pane for a files pane, a terminal for any other.
func (a *App) duplicateTab(m locales.Messages, t *layout.Tab) {
	cn := a.conns[t.Focused().ID]
	if cn == nil || cn.host.ID == "" {
		return
	}
	kind := layout.Terminal
	if cn.kind == layout.Files {
		kind = layout.Files
	}
	if _, err := a.open(cn.host.ID, kind, openAt{}, cn); err != nil {
		a.toastError(m.ErrGeneric(err.Error()))
	}
}

// tabAddress is the address of tab t's host, "" for a local terminal or a
// serial device.
func (a *App) tabAddress(t *layout.Tab) string {
	cn := a.conns[t.Focused().ID]
	if cn == nil || cn.host.ID == "" || isLocal(cn.host.ID) {
		return ""
	}
	h := a.liveHost(cn.host)
	if h.Protocol == assets.ProtoSerial {
		return ""
	}
	return h.Address
}

// copyTabAddress copies the address of tab t's host.
func (a *App) copyTabAddress(t *layout.Tab) {
	if addr := a.tabAddress(t); addr != "" {
		a.svc.WriteClipboard(addr)
		a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.Copied(), Type: "success"} })
	}
}

// closeTabs closes the tabs that close says to, by their place and tab.
func (a *App) closeTabs(close func(i int, t *layout.Tab) bool) {
	var items []layout.Item
	for i, t := range a.ws.Tabs {
		if close(i, t) {
			items = append(items, t.Items()...)
		}
	}
	a.closeItems(items)
}

// tabRenameForm is the tab rename sheet: the tab, by its ID, and the name
// typed.
type tabRenameForm struct {
	tab  int
	name string
}

func (a *App) openTabRename(t *layout.Tab) {
	a.tabRename, a.renaming = &tabRenameForm{tab: t.ID, name: t.Title}, true
}

// RenameTab names the tab of id for as long as it is open; "" gives it
// back its host's name.
func (a *App) RenameTab(id int, name string) {
	for _, t := range a.allTabs() {
		if t.ID == id {
			t.Title = strings.TrimSpace(name)
		}
	}
}

// saveTabRename names the sheet's tab and closes the sheet.
func (a *App) saveTabRename() {
	if f := a.tabRename; f != nil {
		a.RenameTab(f.tab, f.name)
	}
	a.renaming = false
}

func (a *App) tabRenameView(c *ui.Context, m locales.Messages) {
	f := a.tabRename
	if f == nil {
		return
	}
	tok := currentTokens(c)
	sheet(ui.Modal(c, &a.renaming, func() {
		ui.Column(c).Gap(18).Width(380).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, icons.Rename, theme.Brand, 36)
				sheetTitle(c, m.RenameTab())
			})
			ui.Form(c, func() {
				ui.Field(c, m.TabName(), func() {
					in := formTextInput(c.Key("tabname"), &f.name).Placeholder(m.TabNamePlaceholder()).AutoFocus()
					a.tabRenameHold.apply(c, f, in)
					if in.Submitted() {
						a.saveTabRename()
					}
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.renaming = false
				}
				if pill(c, m.Save(), true, false, false) {
					a.saveTabRename()
				}
			})
		})
	}), tok)
	if !a.renaming {
		a.tabRename = nil
	}
}
