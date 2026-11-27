package app

import (
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The tool panes, the broadcast, the session logs, the snippets, the
// command history and the panels on the focused session's host (the
// system monitor, the process usage, the host's details, Docker and
// tmux), work on the sessions, not on one of their own. They are panes as the others are, dragged by their
// header to any edge of a pane or another tab, but on no host, and one of
// each in the whole workspace, which the rail opens and closes. The layout
// saves where they are, never what they show. A tool pane never keeps a tab's focus: the
// focused pane stays the session the panels and the shortcuts work on.

// isTool reports a tool pane's kind.
func isTool(k layout.Kind) bool {
	switch k {
	case layout.Broadcast, layout.Logs, layout.Snippets, layout.History,
		layout.Monitor, layout.Usage, layout.HostInfo, layout.Docker, layout.Tmux:
		return true
	}
	return false
}

// toolPane is the tool pane of kind k, nil when it is not open.
func (a *App) toolPane(k layout.Kind) *conn {
	for _, c := range a.conns {
		if c.kind == k {
			return c
		}
	}
	return nil
}

// toolHere reports whether the tool pane of kind k is in the active tab.
func (a *App) toolHere(k layout.Kind) bool {
	c := a.toolPane(k)
	if c == nil {
		return false
	}
	t, _ := a.ws.Locate(c.id)
	return t != nil && t == a.ws.ActiveTab()
}

// connLabel names pane cn: its host, or what a tool pane is.
func (a *App) connLabel(cn *conn) string {
	if isTool(cn.kind) {
		return a.toolNames[cn.kind]
	}
	return a.pv(cn.host.Label())
}

// nameTools keeps what the tool panes are called in m's language.
func (a *App) nameTools(m locales.Messages) {
	if a.toolNames == nil {
		a.toolNames = map[layout.Kind]string{}
	}
	a.toolNames[layout.Broadcast] = m.Broadcast()
	a.toolNames[layout.Logs] = m.SessionLogs()
	a.toolNames[layout.Snippets] = m.Snippets()
	a.toolNames[layout.History] = m.CommandHistory()
	a.toolNames[layout.Monitor] = m.SystemMonitor()
	a.toolNames[layout.Usage] = m.ProcessUsage()
	a.toolNames[layout.HostInfo] = m.HostDetails()
	a.toolNames[layout.Docker] = m.DockerManager()
	a.toolNames[layout.Tmux] = m.TmuxManager()
}

// ShowTool opens the tool pane of kind k in the active tab, or brings it
// there, leaving it open when it already is.
func (a *App) ShowTool(k layout.Kind) {
	if !a.toolHere(k) {
		a.ToggleTool(k)
	}
}

// ToggleTool closes the tool pane of kind k when it is in the active tab;
// else it opens it, or brings it from the tab it is in, beside the
// session the tab works on (see railAnchor), or under one of the rail's once the tab shows enough of
// them (see railUnder), or, with the rail's panes as tabs, in the place
// of the one the tab shows (see railOld).
func (a *App) ToggleTool(k layout.Kind) {
	t := a.ws.ActiveTab()
	a.settleFocus(t)
	defer func() { a.settleFocus(a.ws.ActiveTab()) }()
	old, under := a.railOld(t, k), a.railUnder(t, k)
	at := 0
	if p := a.railAnchor(t); p != nil {
		at = p.ID
	} else if t != nil {
		at = t.Focus
	}
	if c := a.toolPane(k); c != nil {
		// One of each in the app: from another window it comes here.
		if src := a.winOf(c.id); src != nil && src != a.winState {
			back := a.enter(src)
			a.keepRailWidth(c)
			back()
			src.ws.Take(c.id)
			a.placeTool(c, t, at, old, under)
			return
		}
		if ct, _ := a.ws.Locate(c.id); ct == t || t == nil {
			a.CloseSession(c.id)
			return
		}
		a.keepRailWidth(c)
		if a.ws.Move(c.id, at, layout.Right) {
			a.fitRailWidth(c)
		}
		a.settleRail(c, old, under)
		a.focusReq = c.id
		a.page = pageWorkspace
		a.saveLayout()
		return
	}
	if k == layout.Logs {
		a.listLogs()
	}
	a.nextConn++
	c := &conn{id: a.nextConn, kind: k}
	a.conns[c.id] = c
	a.placeTool(c, t, at, old, under)
}

// placeTool puts tool pane c, in no pane, beside the pane at of the active
// tab t, or in a tab of its own when there is none.
func (a *App) placeTool(c *conn, t *layout.Tab, at int, old, under *conn) {
	if t == nil || !a.ws.Place(c.item(), at, layout.Right) {
		a.ws.Open(c.item())
	} else {
		a.fitRailWidth(c)
	}
	a.settleRail(c, old, under)
	a.focusReq = c.id
	a.page = pageWorkspace
	a.saveLayout()
}

// railPane reports whether the rail opens panes of kind k: the tool
// panes, the files pane and the AI pane.
func railPane(k layout.Kind) bool { return isTool(k) || single(k) }

// railOld is, with the rail's panes as tabs, the one in tab t the rail
// opening kind k replaces: of another kind, opened last, and not busy.
// There is none while the tab has no pane but the rail's, which keeps
// its session.
func (a *App) railOld(t *layout.Tab, k layout.Kind) *conn {
	if t == nil || a.settings.ToolPanes == settings.ToolPanesSplit {
		return nil
	}
	var last *conn
	session := false
	for _, it := range t.Items() {
		c := a.conns[it.ID]
		switch {
		case c == nil:
		case !railPane(c.kind):
			session = true
		case c.kind != k && !paneBusy(c) && (last == nil || c.id > last.id):
			last = c
		}
	}
	if !session {
		return nil
	}
	return last
}

// replaceRail puts c, just opened beside, in old's place, and closes old.
func (a *App) replaceRail(c, old *conn) {
	if _, p := a.ws.Locate(old.id); p != nil && a.ws.Move(c.id, p.ID, layout.Center) {
		a.CloseSession(old.id)
	}
}

// railColumns is how many of the rail's columns a row shows beside its
// sessions before the next pane opens under one of them.
const railColumns = 3

// railMinWidth is the narrowest a column of a row is still of use.
const railMinWidth = 280

// railUnder is, with the rail's panes side by side, the one in tab t the
// rail opening kind k goes under, once the row of its anchor shows
// railColumns columns of the rail's, or has no room for one more even
// shared out evenly: the tallest in those columns, the first in reading
// order of those as tall, so they fill in turn rather than narrowing.
// There is none while the tab has no pane but the rail's, or the row has
// no column of the rail's.
func (a *App) railUnder(t *layout.Tab, k layout.Kind) *conn {
	anchor := a.railAnchor(t)
	if anchor == nil || a.settings.ToolPanes != settings.ToolPanesSplit {
		return nil
	}
	row := rowOf(t, anchor)
	cols := rowColumns(row)
	var under *conn
	var tallest float32
	rail := 0
	for _, col := range cols {
		if !a.allRail(col.n) {
			continue
		}
		rail++
		var walk func(n *layout.Node, h float32)
		walk = func(n *layout.Node, h float32) {
			switch {
			case n.Leaf():
				if c := a.conns[n.Item.ID]; c != nil && c.kind != k && (under == nil || h > tallest) {
					under, tallest = c, h
				}
			case n.Axis == layout.Column:
				walk(n.A, h*share(n))
				walk(n.B, h*(1-share(n)))
			default:
				walk(n.A, h)
				walk(n.B, h)
			}
		}
		walk(col.n, 1)
	}
	if rail == 0 {
		return nil
	}
	if rail < railColumns {
		w := a.rowWidth(row, 0)
		if w <= 0 || w/float32(len(cols)+1)-paneGap >= railMinWidth {
			return nil
		}
	}
	return under
}

// railAnchor is the pane of tab t the rail opens its panes beside, one it
// does not open, as the files and AI panes it opens take the focus: the
// focused pane, else the one focused last, else the first; nil when the
// tab has no pane but the rail's.
func (a *App) railAnchor(t *layout.Tab) *layout.Node {
	if t == nil {
		return nil
	}
	session := func(p *layout.Node) bool {
		c := a.conns[p.Item.ID]
		return c != nil && !railPane(c.kind)
	}
	if p := t.Pane(t.Focus); p != nil && session(p) {
		return p
	}
	if p := t.Pane(a.toolFocus[t.ID]); p != nil && session(p) {
		return p
	}
	for _, p := range t.Panes() {
		if session(p) {
			return p
		}
	}
	return nil
}

// settleRail finishes opening c, just opened beside: in old's place,
// under pane under, or else as a column of its own, the row shared out
// evenly when one of its columns is narrower than railMinWidth.
func (a *App) settleRail(c, old, under *conn) {
	switch {
	case c == nil:
	case old != nil:
		a.replaceRail(c, old)
	case under != nil:
		if _, p := a.ws.Locate(under.id); p != nil {
			a.ws.Move(c.id, p.ID, layout.Bottom)
		}
	default:
		a.fitRow(c)
	}
}

// fitRow shares the row of pane c out evenly when one of its columns is
// narrower than railMinWidth, as wide as the row was last frame.
func (a *App) fitRow(c *conn) {
	t, p := a.ws.Locate(c.id)
	if t == nil {
		return
	}
	row := rowOf(t, p)
	w := a.rowWidth(row, c.id)
	if w <= 0 {
		return
	}
	for _, col := range rowColumns(row) {
		if col.share*w-paneGap < railMinWidth {
			a.evenRow(row)
			return
		}
	}
}

// evenRow gives each column of row n the same width, and reports how
// many columns it has.
func (a *App) evenRow(n *layout.Node) int {
	if n.Leaf() || n.Axis != layout.Row {
		return 1
	}
	l, r := a.evenRow(n.A), a.evenRow(n.B)
	n.Ratio = float32(l) / float32(l+r)
	// The split's divider keeps its own copy of the ratio.
	delete(a.pct, n.ID)
	return l + r
}

// rowOf is the split of tab t, side by side all the way down, holding
// node n: n itself when it is split, or sits, top and bottom.
func rowOf(t *layout.Tab, n *layout.Node) *layout.Node {
	for p := t.Parent(n); p != nil && p.Axis == layout.Row; p = t.Parent(p) {
		n = p
	}
	return n
}

// column is a column of a row, a pane or a split top and bottom, and its
// share of the row's width.
type column struct {
	n     *layout.Node
	share float32
}

// rowColumns lists the columns of row n, left to right.
func rowColumns(n *layout.Node) []column {
	var out []column
	var walk func(n *layout.Node, w float32)
	walk = func(n *layout.Node, w float32) {
		if !n.Leaf() && n.Axis == layout.Row {
			walk(n.A, w*share(n))
			walk(n.B, w*(1-share(n)))
			return
		}
		out = append(out, column{n, w})
	}
	walk(n, 1)
	return out
}

// share is split n's share of its first side, half when it has none yet,
// as its divider reads it.
func share(n *layout.Node) float32 {
	if n.Ratio <= 0 || n.Ratio >= 1 {
		return 0.5
	}
	return n.Ratio
}

// allRail reports whether every pane under n is one the rail opens.
func (a *App) allRail(n *layout.Node) bool {
	if n.Leaf() {
		c := a.conns[n.Item.ID]
		return c != nil && railPane(c.kind)
	}
	return a.allRail(n.A) && a.allRail(n.B)
}

// rowWidth is how wide row n was last frame, by where its panes but skip
// were, gutters included; 0 when none was drawn.
func (a *App) rowWidth(n *layout.Node, skip int) float32 {
	var left, right float32
	seen := false
	var walk func(n *layout.Node)
	walk = func(n *layout.Node) {
		if !n.Leaf() {
			walk(n.A)
			walk(n.B)
			return
		}
		b, ok := a.paneBounds[n.Item.ID]
		if !ok || n.Item.ID == skip || b.W <= 0 {
			return
		}
		if !seen || b.X < left {
			left = b.X
		}
		if !seen || b.X+b.W > right {
			right = b.X + b.W
		}
		seen = true
	}
	walk(n)
	if !seen {
		return 0
	}
	return right - left + paneGap
}

// keepRailWidth remembers how wide pane c is, when the rail opens its
// kind and it sits beside another pane: alone in its tab, or only above
// or below others, its width is the tab's, not one the user chose.
func (a *App) keepRailWidth(c *conn) {
	t, p := a.ws.Locate(c.id)
	w := a.paneBounds[c.id].W
	if !railPane(c.kind) || t == nil || w <= 0 {
		return
	}
	for n := t.Parent(p); n != nil; n = t.Parent(n) {
		if n.Axis == layout.Row {
			if a.railWidths == nil {
				a.railWidths = map[layout.Kind]float32{}
			}
			a.railWidths[c.kind] = w
			return
		}
	}
}

// fitRailWidth gives pane c, just opened at the left or right edge of a
// pane, the width remembered for its kind instead of half the room, and
// never more than most of it nor less than a sliver.
func (a *App) fitRailWidth(c *conn) {
	w, ok := a.railWidths[c.kind]
	t, p := a.ws.Locate(c.id)
	if !ok || t == nil {
		return
	}
	split := t.Parent(p)
	if split == nil || split.Axis != layout.Row || !split.A.Leaf() || !split.B.Leaf() {
		return
	}
	other := split.A
	if other == p {
		other = split.B
	}
	// The split shares what the other pane had last frame: each cell is a
	// card and its gutter, with a one-DIP line between the two.
	room := a.paneBounds[other.Item.ID].W + paneGap - 1
	if room <= 0 {
		return
	}
	share := min(max((w+paneGap)/room, 0.15), 0.85)
	if split.A == p {
		split.Ratio = share
	} else {
		split.Ratio = 1 - share
	}
}

// settleFocus keeps tab t's focus off its tool panes: on the pane it
// focused last, or else its first other pane. A tab of tool panes alone
// keeps it where it is.
func (a *App) settleFocus(t *layout.Tab) {
	if t == nil {
		return
	}
	if a.toolFocus == nil {
		a.toolFocus = map[int]int{}
	}
	if !isTool(t.Focused().Kind) {
		a.toolFocus[t.ID] = t.Focus
		return
	}
	if p := t.Pane(a.toolFocus[t.ID]); p != nil && !isTool(p.Item.Kind) {
		t.Focus = p.ID
		return
	}
	for _, p := range t.Panes() {
		if !isTool(p.Item.Kind) {
			t.Focus = p.ID
			return
		}
	}
}

// toolPaneView is tool pane cn: its header, which drags it, with the
// pane's own actions, over what it shows.
func (a *App) toolPaneView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	// Opened, or its tab chosen: the keyboard goes to its field, if any.
	focus := a.focusReq == cn.id && a.page == pageWorkspace
	if focus {
		a.focusReq = 0
	}
	lead := func() {}
	var acts []barAction
	switch cn.kind {
	case layout.Snippets:
		lead, acts = a.snippetsHead(c, m, tok)
	case layout.History:
		lead, acts = a.historyHead(c, m, tok)
	}
	title := a.connLabel(cn)
	a.paneHead(c, cn, m, func() {
		softTile(c, kindIcon(cn.kind, true), tok.Accent, paneHeadIcon)
		ui.Text(c, title).FontWeight(600).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
		lead()
	}, paneHeadIcon+8+titleRoom(c, title), acts, nil)
	ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	ui.Column(c.Key("body")).Grow(1).Shrink(1).MinHeight(0).FillWidth().Children(func() {
		switch cn.kind {
		case layout.Broadcast:
			a.broadcastPanel(c, m, tok, focus)
		case layout.Logs:
			a.logPanel(c, m, tok)
		case layout.Snippets:
			a.snippetsPanel(c, m, tok)
		case layout.History:
			a.historyPanel(c, m, tok)
		default:
			a.hostToolPanel(c, m, tok, cn.kind)
		}
	})
}
