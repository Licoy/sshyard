// Package layout keeps SSHYard's workspace: tabs, each a tree of split
// panes, each pane showing one item. An item is of a kind: a terminal
// session, a files (SFTP) browser, an editor of a remote file, an AI
// assistant, or a tool on the sessions (the broadcast, the session logs,
// the snippets, the command history, the system monitor, the process
// usage, the host's details, Docker, tmux). All are panes alike, so any of them can sit in any split
// cell, be dragged onto another pane's edge, be focused and closed. An
// item lives in exactly one pane; moving it (by drag and drop) takes it
// out of its old pane. Item IDs are unique across kinds.
package layout

import "slices"

// Kind is what a pane shows.
type Kind int

const (
	// Terminal is a terminal session.
	Terminal Kind = iota
	// Files is a files (SFTP) browser.
	Files
	// Editor edits a remote file.
	Editor
	// AI is an assistant bound to one host. It does not open a connection.
	AI
	// Broadcast sends a command to several terminals; Logs keeps their
	// session logs; Snippets and History send saved and past commands to
	// the focused terminal; Monitor, Usage, HostInfo, Docker and Tmux show
	// and work on the focused session's host. None is bound to a host.
	Broadcast
	Logs
	Snippets
	History
	Monitor
	Usage
	HostInfo
	Docker
	Tmux
)

func (k Kind) String() string {
	switch k {
	case Files:
		return "files"
	case Editor:
		return "editor"
	case AI:
		return "ai"
	case Broadcast:
		return "broadcast"
	case Logs:
		return "logs"
	case Snippets:
		return "snippets"
	case History:
		return "history"
	case Monitor:
		return "monitor"
	case Usage:
		return "usage"
	case HostInfo:
		return "host"
	case Docker:
		return "docker"
	case Tmux:
		return "tmux"
	}
	return "terminal"
}

// Item is what a pane shows: an ID of the app's and its kind.
type Item struct {
	Kind Kind
	ID   int
}

// Axis is how a split lays its two children out.
type Axis int

const (
	// Row puts the children left and right.
	Row Axis = iota
	// Column puts the children top and bottom.
	Column
)

// Edge is where a dropped item lands relative to a pane.
type Edge int

const (
	Center Edge = iota // swap into the pane
	Left
	Right
	Top
	Bottom
)

// Node is a pane (Item.ID != 0, no children) or a split (A and B).
type Node struct {
	ID    int
	Item  Item
	Axis  Axis
	Ratio float32 // share of A, 0 to 1
	A, B  *Node
}

// Leaf reports whether n is a pane.
func (n *Node) Leaf() bool { return n.A == nil }

// Tab is one tab of the workspace.
type Tab struct {
	ID    int
	Root  *Node
	Focus int // the focused pane
	// Title is the name the user gave the tab, "" for its host's.
	Title string
	// Color is the name of the tint the user gave the tab (the theme's
	// tint names), "" for none.
	Color string
}

// Panes lists the tab's panes in reading order.
func (t *Tab) Panes() []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.Leaf() {
			out = append(out, n)
			return
		}
		walk(n.A)
		walk(n.B)
	}
	walk(t.Root)
	return out
}

// Pane finds a pane of the tab.
func (t *Tab) Pane(id int) *Node {
	for _, p := range t.Panes() {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Focused is the item of the focused pane.
func (t *Tab) Focused() Item {
	if p := t.Pane(t.Focus); p != nil {
		return p.Item
	}
	return Item{}
}

// Items lists the tab's items in reading order.
func (t *Tab) Items() []Item {
	var out []Item
	for _, p := range t.Panes() {
		out = append(out, p.Item)
	}
	return out
}

// Count counts the tab's panes of a kind.
func (t *Tab) Count(k Kind) int {
	n := 0
	for _, p := range t.Panes() {
		if p.Item.Kind == k {
			n++
		}
	}
	return n
}

// Parent is the split holding node n of the tab, nil for its root.
func (t *Tab) Parent(n *Node) *Node { return parent(t.Root, n) }

func parent(root, n *Node) *Node {
	if root == nil || root.Leaf() {
		return nil
	}
	if root.A == n || root.B == n {
		return root
	}
	if p := parent(root.A, n); p != nil {
		return p
	}
	return parent(root.B, n)
}

// Workspace is the set of tabs of one window. Workspaces made with
// Sibling share their IDs, so a tab or a pane moves from one to another
// (TakeTab, AdoptTab, Take) without clashing with the IDs there.
type Workspace struct {
	Tabs   []*Tab
	Active int // index into Tabs, -1 when empty
	next   *int
}

// New makes an empty workspace.
func New() *Workspace { return &Workspace{Active: -1, next: new(int)} }

// Sibling makes an empty workspace whose IDs never clash with w's, for
// another window.
func (w *Workspace) Sibling() *Workspace {
	if w.next == nil {
		w.next = new(int)
	}
	return &Workspace{Active: -1, next: w.next}
}

func (w *Workspace) id() int {
	if w.next == nil {
		w.next = new(int)
	}
	*w.next++
	return *w.next
}

// FreshID is a new id for a node a caller builds itself (layout restore).
func (w *Workspace) FreshID() int { return w.id() }

// AddTab appends a tab whose root the caller built with FreshID, and shows it.
// focus is a pane id in root; an unknown focus lands on the first pane.
func (w *Workspace) AddTab(root *Node, focus int) {
	if root == nil {
		return
	}
	t := &Tab{ID: w.id(), Root: root, Focus: focus}
	if t.Pane(focus) == nil {
		if p := t.Panes(); len(p) > 0 {
			t.Focus = p[0].ID
		}
	}
	w.Tabs = append(w.Tabs, t)
	w.Active = len(w.Tabs) - 1
}

// ActiveTab is the shown tab, or nil.
func (w *Workspace) ActiveTab() *Tab {
	if w.Active < 0 || w.Active >= len(w.Tabs) {
		return nil
	}
	return w.Tabs[w.Active]
}

// Open shows an item in a new tab and makes it active.
func (w *Workspace) Open(item Item) *Tab {
	p := &Node{ID: w.id(), Item: item}
	t := &Tab{ID: w.id(), Root: p, Focus: p.ID}
	w.Tabs = append(w.Tabs, t)
	w.Active = len(w.Tabs) - 1
	return t
}

// Locate finds the tab and pane that show an item, by its ID.
func (w *Workspace) Locate(id int) (*Tab, *Node) {
	for _, t := range w.Tabs {
		for _, p := range t.Panes() {
			if p.Item.ID == id {
				return t, p
			}
		}
	}
	return nil, nil
}

// Items lists every item of the workspace.
func (w *Workspace) Items() []Item {
	var out []Item
	for _, t := range w.Tabs {
		out = append(out, t.Items()...)
	}
	return out
}

// Activate shows the tab and focuses the pane with the item.
func (w *Workspace) Activate(id int) bool {
	t, p := w.Locate(id)
	if t == nil {
		return false
	}
	for i, x := range w.Tabs {
		if x == t {
			w.Active = i
		}
	}
	t.Focus = p.ID
	return true
}

// SplitFocused splits the active tab's focused pane, showing item in the
// new pane (right of or below it), and focuses the new pane. With no tab
// it opens one.
func (w *Workspace) SplitFocused(axis Axis, item Item) *Node {
	t := w.ActiveTab()
	if t == nil {
		return w.Open(item).Root
	}
	edge := Right
	if axis == Column {
		edge = Bottom
	}
	return w.split(t, t.Pane(t.Focus), edge, item)
}

func (w *Workspace) split(t *Tab, target *Node, edge Edge, item Item) *Node {
	if target == nil {
		target = t.Panes()[0]
	}
	moved := &Node{ID: target.ID, Item: target.Item}
	fresh := &Node{ID: w.id(), Item: item}
	target.ID, target.Item, target.Ratio = w.id(), Item{}, 0.5
	switch edge {
	case Left, Top:
		target.A, target.B = fresh, moved
	default:
		target.A, target.B = moved, fresh
	}
	target.Axis = Row
	if edge == Top || edge == Bottom {
		target.Axis = Column
	}
	t.Focus = fresh.ID
	return fresh
}

// remove takes a pane out of its tab, collapsing its parent split, and
// removes the tab when it has no panes left.
func (w *Workspace) remove(t *Tab, p *Node) {
	if t.Root == p {
		for i, x := range w.Tabs {
			if x == t {
				w.Tabs = append(w.Tabs[:i], w.Tabs[i+1:]...)
				switch {
				case len(w.Tabs) == 0:
					w.Active = -1
				case w.Active > i || w.Active >= len(w.Tabs):
					w.Active--
				}
				break
			}
		}
		return
	}
	par := parent(t.Root, p)
	sib := par.A
	if sib == p {
		sib = par.B
	}
	*par = *sib
	if t.Pane(t.Focus) == nil {
		t.Focus = t.Panes()[0].ID
	}
}

// Close removes the pane showing an item.
func (w *Workspace) Close(id int) bool {
	t, p := w.Locate(id)
	if t == nil {
		return false
	}
	w.remove(t, p)
	return true
}

// CloseTab removes a whole tab and returns its items.
func (w *Workspace) CloseTab(tabID int) []Item {
	for _, t := range w.Tabs {
		if t.ID == tabID {
			out := t.Items()
			w.remove(t, t.Root)
			return out
		}
	}
	return nil
}

// Move drops an item on a pane: at the Center the two items swap places;
// at an edge the target pane splits and the item takes the new half. The
// item leaves its old pane, and a tab left empty closes. Kinds mix freely.
func (w *Workspace) Move(id, targetPane int, edge Edge) bool {
	st, sp := w.Locate(id)
	var tt *Tab
	var tp *Node
	for _, t := range w.Tabs {
		if p := t.Pane(targetPane); p != nil {
			tt, tp = t, p
		}
	}
	if st == nil || tt == nil || sp == tp {
		return false
	}
	if edge == Center {
		sp.Item, tp.Item = tp.Item, sp.Item
		tt.Focus = tp.ID
		w.Activate(id)
		return true
	}
	item := sp.Item
	w.remove(st, sp)
	// The target may have been restructured when the source collapsed
	// into the same split; find it again by its item.
	tt, tp = w.Locate(tp.Item.ID)
	w.split(tt, tp, edge, item)
	w.Activate(id)
	return true
}

// Replace shows item, which is in no pane yet, in the pane showing the
// item with ID old, which leaves the workspace. The pane is focused.
func (w *Workspace) Replace(old int, item Item) bool {
	if t, _ := w.Locate(item.ID); t != nil {
		return false
	}
	t, p := w.Locate(old)
	if t == nil {
		return false
	}
	p.Item = item
	w.Activate(item.ID)
	return true
}

// MoveTab puts the tab of ID tabID before the tab of ID target, or after
// it, and keeps the shown tab shown. It reports whether the order changed.
func (w *Workspace) MoveTab(tabID, target int, after bool) bool {
	byID := func(id int) func(*Tab) bool { return func(t *Tab) bool { return t.ID == id } }
	from := slices.IndexFunc(w.Tabs, byID(tabID))
	if from < 0 || tabID == target || !slices.ContainsFunc(w.Tabs, byID(target)) {
		return false
	}
	shown, t := w.ActiveTab(), w.Tabs[from]
	w.Tabs = slices.Delete(w.Tabs, from, from+1)
	to := slices.IndexFunc(w.Tabs, byID(target))
	if after {
		to++
	}
	w.Tabs = slices.Insert(w.Tabs, to, t)
	if shown != nil {
		w.Active = slices.Index(w.Tabs, shown)
	}
	return to != from
}

// TabOf is the tab with the pane of node ID pane, nil for none.
func (w *Workspace) TabOf(pane int) *Tab {
	for _, t := range w.Tabs {
		if t.Pane(pane) != nil {
			return t
		}
	}
	return nil
}

// Detach moves an item out to a tab of its own, just after the tab it
// leaves, and shows it.
func (w *Workspace) Detach(id int) bool {
	t, p := w.Locate(id)
	if t == nil || t.Root == p {
		return false
	}
	w.remove(t, p)
	i := slices.Index(w.Tabs, t) + 1
	w.Tabs = slices.Insert(w.Tabs, i, &Tab{ID: w.id(), Root: p, Focus: p.ID})
	w.Active = i
	return true
}

// Ungroup splits the tab of ID tabID into a tab for each of its panes, in
// reading order: the first keeps the tab, with its name and color, and the
// others follow it. The tab of the pane that was focused is shown. It
// reports whether the tab had more than one pane.
func (w *Workspace) Ungroup(tabID int) bool {
	i := slices.IndexFunc(w.Tabs, func(t *Tab) bool { return t.ID == tabID })
	if i < 0 {
		return false
	}
	t := w.Tabs[i]
	panes := t.Panes()
	if len(panes) < 2 {
		return false
	}
	focus := t.Focus
	t.Root, t.Focus = panes[0], panes[0].ID
	w.Active = i
	var rest []*Tab
	for j, p := range panes[1:] {
		rest = append(rest, &Tab{ID: w.id(), Root: p, Focus: p.ID})
		if p.ID == focus {
			w.Active = i + 1 + j
		}
	}
	w.Tabs = slices.Insert(w.Tabs, i+1, rest...)
	return true
}

// Place puts an item that is in no pane yet on a pane: at an edge the
// pane splits and the item takes the new half; at the Center it takes the
// pane, and the item it showed moves out to a tab of its own, so that
// nothing is lost.
func (w *Workspace) Place(item Item, targetPane int, edge Edge) bool {
	if t, _ := w.Locate(item.ID); t != nil {
		return w.Move(item.ID, targetPane, edge)
	}
	for _, t := range w.Tabs {
		p := t.Pane(targetPane)
		if p == nil {
			continue
		}
		if edge != Center {
			w.split(t, p, edge, item)
			w.Activate(item.ID)
			return true
		}
		old := p.Item
		p.Item = item
		t.Focus = p.ID
		w.Open(old)
		w.Activate(item.ID)
		return true
	}
	return false
}

// TakeTab takes the tab of ID tabID out of the workspace, its items
// still in it, for AdoptTab on a Sibling; nil when there is none.
func (w *Workspace) TakeTab(tabID int) *Tab {
	for _, t := range w.Tabs {
		if t.ID == tabID {
			w.remove(t, t.Root)
			return t
		}
	}
	return nil
}

// AdoptTab adds a tab taken from a Sibling (TakeTab) at the end, and shows
// it. A tab that is already here, or holds no pane, is left.
func (w *Workspace) AdoptTab(t *Tab) bool {
	if t == nil || t.Root == nil || slices.Contains(w.Tabs, t) {
		return false
	}
	w.Tabs = append(w.Tabs, t)
	w.Active = len(w.Tabs) - 1
	return true
}

// Take takes the pane showing the item with ID id out of its tab, as
// Close does, and returns the item, to Open or Place it on a Sibling.
func (w *Workspace) Take(id int) (Item, bool) {
	t, p := w.Locate(id)
	if t == nil {
		return Item{}, false
	}
	item := p.Item
	w.remove(t, p)
	return item, true
}
