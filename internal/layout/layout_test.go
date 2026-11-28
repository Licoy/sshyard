package layout

import (
	"slices"
	"testing"
)

func sessions(t *Tab) []int {
	var out []int
	for _, p := range t.Panes() {
		out = append(out, p.Item.ID)
	}
	return out
}

// T and F are a terminal and a files item.
func T(id int) Item { return Item{Terminal, id} }
func F(id int) Item { return Item{Files, id} }

func TestTabsAndSplits(t *testing.T) {
	w := New()
	if w.ActiveTab() != nil {
		t.Fatal("empty workspace has a tab")
	}
	w.Open(T(1))
	w.Open(T(2))
	if w.Active != 1 || w.ActiveTab().Focused().ID != 2 {
		t.Fatal("open")
	}
	p := w.SplitFocused(Row, T(3))
	tab := w.ActiveTab()
	if tab.Root.Axis != Row || !slices.Equal(sessions(tab), []int{2, 3}) || tab.Focus != p.ID {
		t.Fatalf("split row %v", sessions(tab))
	}
	w.SplitFocused(Column, T(4))
	if !slices.Equal(sessions(tab), []int{2, 3, 4}) || tab.Root.B.Axis != Column || tab.Focused().ID != 4 {
		t.Fatalf("split column %v", sessions(tab))
	}
	if !w.Activate(1) || w.Active != 0 {
		t.Fatal("activate")
	}
	w.Close(3)
	if !slices.Equal(sessions(w.Tabs[1]), []int{2, 4}) || w.Tabs[1].Root.B.Item.ID != 4 {
		t.Fatalf("close %v", sessions(w.Tabs[1]))
	}
	w.Activate(4)
	w.Close(4)
	if !w.Tabs[1].Root.Leaf() || w.Tabs[1].Focused().ID != 2 {
		t.Fatal("focus after close")
	}
	w.Close(1)
	if len(w.Tabs) != 1 || w.Active != 0 {
		t.Fatalf("tab close %d %d", len(w.Tabs), w.Active)
	}
	if got := w.CloseTab(w.Tabs[0].ID); !slices.Equal(got, []Item{T(2)}) || w.Active != -1 {
		t.Fatal("close tab")
	}
}

func TestMove(t *testing.T) {
	w := New()
	w.Open(T(1))
	w.SplitFocused(Row, T(2))
	w.Open(T(3))
	target := w.Tabs[0].Panes()[1].ID // session 2

	// Drag tab 3 to the bottom of pane 2: tab 3 closes.
	if !w.Move(3, target, Bottom) {
		t.Fatal("move")
	}
	if len(w.Tabs) != 1 || !slices.Equal(sessions(w.Tabs[0]), []int{1, 2, 3}) || w.Tabs[0].Focused().ID != 3 {
		t.Fatalf("after move %d %v", len(w.Tabs), sessions(w.Tabs[0]))
	}
	// Swap 1 and 3.
	_, p3 := w.Locate(3)
	w.Move(1, p3.ID, Center)
	if !slices.Equal(sessions(w.Tabs[0]), []int{3, 2, 1}) {
		t.Fatalf("swap %v", sessions(w.Tabs[0]))
	}
	// Move 2, whose sibling is 1, to the left of 1: the split collapses
	// first and the target must still be found.
	_, p1 := w.Locate(1)
	if !w.Move(2, p1.ID, Left) {
		t.Fatal("move into sibling")
	}
	if !slices.Equal(sessions(w.Tabs[0]), []int{3, 2, 1}) {
		t.Fatalf("sibling move %v", sessions(w.Tabs[0]))
	}
	_, p2 := w.Locate(2)
	if w.Move(2, p2.ID, Left) {
		t.Error("moved onto itself")
	}
	if !w.Detach(2) || len(w.Tabs) != 2 || w.Active != 1 {
		t.Fatal("detach")
	}
	if w.Detach(2) {
		t.Error("detached a lone pane")
	}
	if len(w.Items()) != 3 {
		t.Error("sessions")
	}
}

func TestMoveTab(t *testing.T) {
	w := New()
	w.Open(T(1))
	w.Open(T(2))
	w.Open(T(3))
	order := func() (out []int) {
		for _, tab := range w.Tabs {
			out = append(out, tab.Focused().ID)
		}
		return out
	}
	id := func(i int) int { return w.Tabs[i].ID }
	shown := w.ActiveTab()
	if !w.MoveTab(id(2), id(0), false) || !slices.Equal(order(), []int{3, 1, 2}) || w.ActiveTab() != shown {
		t.Fatalf("before: %v, active %d", order(), w.Active)
	}
	if !w.MoveTab(id(0), id(2), true) || !slices.Equal(order(), []int{1, 2, 3}) || w.ActiveTab() != shown {
		t.Fatalf("after: %v, active %d", order(), w.Active)
	}
	if w.MoveTab(id(0), id(1), false) || w.MoveTab(id(1), id(0), true) || w.MoveTab(id(1), id(1), true) || w.MoveTab(-1, id(0), false) || w.MoveTab(id(0), -1, false) {
		t.Errorf("a move that changes nothing reported one: %v", order())
	}
	if !slices.Equal(order(), []int{1, 2, 3}) {
		t.Errorf("order %v", order())
	}
}

func TestPlace(t *testing.T) {
	w := New()
	tab := w.Open(T(1))
	pane := tab.Root.ID
	if !w.Place(T(2), pane, Right) {
		t.Fatal("place at an edge failed")
	}
	if got := len(tab.Panes()); got != 2 || tab.Focused().ID != 2 {
		t.Fatalf("panes %d, focus %d", got, tab.Focused().ID)
	}
	_, p1 := w.Locate(1)
	if !w.Place(T(3), p1.ID, Center) {
		t.Fatal("place at the center failed")
	}
	if tt, _ := w.Locate(3); tt != tab {
		t.Fatal("3 is not in the first tab")
	}
	if tt, _ := w.Locate(1); tt == nil || tt == tab {
		t.Fatal("1 did not move out to a tab of its own")
	}
	if w.Active != 0 {
		t.Fatalf("active tab %d, want the one showing 3", w.Active)
	}
	if w.Place(T(4), 999, Center) {
		t.Fatal("placed on a pane that does not exist")
	}
}

func TestMixedKinds(t *testing.T) {
	w := New()
	tab := w.Open(T(1))
	// Files beside the terminal, then a second terminal under the files.
	w.SplitFocused(Row, F(2))
	w.SplitFocused(Column, T(3))
	if got := tab.Items(); !slices.Equal(got, []Item{T(1), F(2), T(3)}) {
		t.Fatalf("items %v", got)
	}
	if tab.Count(Files) != 1 || tab.Count(Terminal) != 2 {
		t.Fatalf("counts %d %d", tab.Count(Files), tab.Count(Terminal))
	}
	// Swapping keeps each item's kind with it.
	_, p1 := w.Locate(1)
	w.Move(2, p1.ID, Center)
	if got := tab.Items(); !slices.Equal(got, []Item{F(2), T(1), T(3)}) || tab.Focused() != F(2) {
		t.Fatalf("swap %v focus %v", got, tab.Focused())
	}
	// A files pane dragged onto an edge of a terminal in another tab.
	w.Open(T(4))
	_, p4 := w.Locate(4)
	if !w.Move(2, p4.ID, Left) {
		t.Fatal("move files")
	}
	if got := w.ActiveTab().Items(); !slices.Equal(got, []Item{F(2), T(4)}) || w.ActiveTab().Root.Axis != Row {
		t.Fatalf("moved %v", got)
	}
	if got := tab.Items(); !slices.Equal(got, []Item{T(1), T(3)}) {
		t.Fatalf("left behind %v", got)
	}
	// Placing a new files pane at the center sends the terminal it covers
	// to a tab of its own.
	_, p3 := w.Locate(3)
	w.Place(F(5), p3.ID, Center)
	if got := tab.Items(); !slices.Equal(got, []Item{T(1), F(5)}) {
		t.Fatalf("placed %v", got)
	}
	if tt, p := w.Locate(3); tt == nil || p.Item != T(3) || len(tt.Panes()) != 1 {
		t.Fatal("covered terminal lost")
	}
	// Closing the tab returns every item with its kind.
	if got := w.CloseTab(w.Tabs[1].ID); !slices.Equal(got, []Item{F(2), T(4)}) {
		t.Fatalf("closed %v", got)
	}
	if Files.String() != "files" || Terminal.String() != "terminal" || Editor.String() != "editor" {
		t.Error("kind names")
	}
}

// three is a tab of 1 | 2 | 3, split right twice.
func three() (*Workspace, *Tab) {
	w := New()
	w.Open(T(1))
	w.SplitFocused(Row, T(2))
	w.SplitFocused(Row, T(3))
	return w, w.Tabs[0]
}

// shape writes a tree as (a|b) for rows and (a/b) for columns.
func shape(n *Node) string {
	if n.Leaf() {
		return string(rune('0' + n.Item.ID))
	}
	sep := "|"
	if n.Axis == Column {
		sep = "/"
	}
	return "(" + shape(n.A) + sep + shape(n.B) + ")"
}

func TestMoveEveryZone(t *testing.T) {
	for _, tc := range []struct {
		edge Edge
		want string
	}{
		// The user's case: the rightmost pane to the bottom of the first,
		// which becomes top and bottom.
		{Bottom, "((1/3)|2)"},
		{Top, "((3/1)|2)"},
		{Left, "((3|1)|2)"},
		{Right, "((1|3)|2)"},
		{Center, "(3|(2|1))"},
	} {
		w, tab := three()
		_, p1 := w.Locate(1)
		if !w.Move(3, p1.ID, tc.edge) {
			t.Fatalf("edge %d: no move", tc.edge)
		}
		if got := shape(tab.Root); got != tc.want {
			t.Errorf("edge %d: %s, want %s", tc.edge, got, tc.want)
		}
		if tab.Focused().ID != 3 {
			t.Errorf("edge %d: focus on %d", tc.edge, tab.Focused().ID)
		}
		// No empty split is left: every split has two children, every
		// leaf an item, and each item shows once.
		var walk func(n *Node)
		seen := map[int]int{}
		walk = func(n *Node) {
			if n.Leaf() {
				if n.Item.ID == 0 {
					t.Errorf("edge %d: an empty pane", tc.edge)
				}
				seen[n.Item.ID]++
				return
			}
			if n.A == nil || n.B == nil {
				t.Errorf("edge %d: a split with one child", tc.edge)
				return
			}
			walk(n.A)
			walk(n.B)
		}
		walk(tab.Root)
		if len(seen) != 3 || seen[1] != 1 || seen[2] != 1 || seen[3] != 1 {
			t.Errorf("edge %d: items %v", tc.edge, seen)
		}
	}
}

func TestMoveWithinASplit(t *testing.T) {
	// 2 beside its sibling 3, to 3's bottom: their split turns a column.
	w, tab := three()
	_, p3 := w.Locate(3)
	w.Move(2, p3.ID, Bottom)
	if got := shape(tab.Root); got != "(1|(3/2))" {
		t.Errorf("got %s", got)
	}
	// And back to the right of the first: the column collapses.
	_, p1 := w.Locate(1)
	w.Move(2, p1.ID, Right)
	if got := shape(tab.Root); got != "((1|2)|3)" {
		t.Errorf("got %s", got)
	}
}

func TestMoveAcrossTabs(t *testing.T) {
	w, tab := three()
	w.Open(T(4))
	_, p2 := w.Locate(2)
	if !w.Move(4, p2.ID, Top) {
		t.Fatal("no move")
	}
	if len(w.Tabs) != 1 || shape(tab.Root) != "(1|((4/2)|3))" || w.ActiveTab() != tab {
		t.Errorf("%d tabs, %s", len(w.Tabs), shape(tab.Root))
	}
	// Out of the tab and onto another: the pane left behind collapses.
	w.Open(T(5))
	_, p5 := w.Locate(5)
	w.Move(1, p5.ID, Left)
	if shape(tab.Root) != "((4/2)|3)" || shape(w.Tabs[1].Root) != "(1|5)" {
		t.Errorf("%s and %s", shape(tab.Root), shape(w.Tabs[1].Root))
	}
}

func TestAIKindMixesWithTheOthers(t *testing.T) {
	if AI.String() != "ai" || Files.String() != "files" || Editor.String() != "editor" || Terminal.String() != "terminal" {
		t.Fatalf("%s %s %s %s", Terminal, Files, Editor, AI)
	}
	w := New()
	w.Open(T(1))
	w.SplitFocused(Row, Item{AI, 2})
	w.SplitFocused(Column, F(3))
	tab := w.ActiveTab()
	if !slices.Equal(sessions(tab), []int{1, 2, 3}) || tab.Count(AI) != 1 || tab.Count(Terminal) != 1 {
		t.Fatalf("panes %v", sessions(tab))
	}
	// A caller-built tree (layout restore) uses the same ids and focus.
	w2 := New()
	a := &Node{ID: w2.FreshID(), Item: T(1)}
	b := &Node{ID: w2.FreshID(), Item: Item{AI, 9}}
	root := &Node{ID: w2.FreshID(), Axis: Row, Ratio: 0.5, A: a, B: b}
	w2.AddTab(root, b.ID)
	if w2.ActiveTab().Focused().Kind != AI || w2.ActiveTab().Count(AI) != 1 {
		t.Fatalf("restored focus %v", w2.ActiveTab().Focused())
	}
	if w2.FreshID() == root.ID {
		t.Fatal("FreshID reused an id")
	}
}

func TestReplaceKeepsThePane(t *testing.T) {
	w := New()
	w.Open(T(1))
	n := w.SplitFocused(Row, F(2))
	w.Open(T(3))
	if !w.Replace(2, F(4)) {
		t.Fatal("replace")
	}
	tab := w.Tabs[0]
	if !slices.Equal(sessions(tab), []int{1, 4}) || tab.Focus != n.ID || w.Active != 0 || w.TabOf(n.ID) != tab {
		t.Fatalf("panes %v focus %d active %d", sessions(tab), tab.Focus, w.Active)
	}
	if w.Replace(9, F(5)) || w.Replace(1, F(4)) || w.TabOf(99) != nil {
		t.Fatal("replaced a missing item, or with one already shown")
	}
}

// TestSiblingMoves moves a tab and a pane between two windows' workspaces:
// the IDs stay unique across both and the items keep their panes.
func TestSiblingMoves(t *testing.T) {
	a := New()
	b := a.Sibling()
	a.Open(T(1))
	a.SplitFocused(Row, T(2))
	a.Open(T(3))
	b.Open(T(4))
	ids := map[int]bool{}
	for _, w := range []*Workspace{a, b} {
		for _, tab := range w.Tabs {
			for _, n := range append(tab.Panes(), &Node{ID: tab.ID}) {
				if ids[n.ID] {
					t.Fatalf("id %d used twice", n.ID)
				}
				ids[n.ID] = true
			}
		}
	}
	tab := a.TakeTab(a.Tabs[0].ID)
	if tab == nil || len(a.Tabs) != 1 || a.Active != 0 || !slices.Equal(sessions(tab), []int{1, 2}) {
		t.Fatal("take tab")
	}
	if !b.AdoptTab(tab) || b.ActiveTab() != tab || b.AdoptTab(tab) || b.AdoptTab(nil) {
		t.Fatal("adopt tab")
	}
	if a.TakeTab(tab.ID) != nil {
		t.Fatal("took a tab it no longer has")
	}
	item, ok := b.Take(2)
	if !ok || item != T(2) || !slices.Equal(sessions(tab), []int{1}) {
		t.Fatal("take pane")
	}
	if _, ok := b.Take(2); ok {
		t.Fatal("took a pane twice")
	}
	if !a.Place(item, a.Tabs[0].Root.ID, Right) || !slices.Equal(sessions(a.Tabs[0]), []int{3, 2}) {
		t.Fatal("place on the other workspace")
	}
	b.Take(4)
	b.Take(1)
	if len(b.Tabs) != 0 || b.Active != -1 {
		t.Fatal("last pane taken leaves the tab")
	}
}

func TestDetachGoesBesideItsTab(t *testing.T) {
	w := New()
	w.Open(T(1))
	w.SplitFocused(Row, T(2))
	w.Open(T(3))
	if !w.Detach(2) {
		t.Fatal("detach")
	}
	var got [][]int
	for _, tab := range w.Tabs {
		got = append(got, sessions(tab))
	}
	if len(got) != 3 || !slices.Equal(got[0], []int{1}) || !slices.Equal(got[1], []int{2}) || !slices.Equal(got[2], []int{3}) {
		t.Fatalf("tabs %v", got)
	}
	if w.Active != 1 || w.ActiveTab().Focused().ID != 2 {
		t.Fatalf("shown %d", w.Active)
	}
}

func TestUngroup(t *testing.T) {
	w := New()
	w.Open(T(9))
	w.Open(T(1))
	w.SplitFocused(Row, T(2))
	w.SplitFocused(Column, F(3))
	w.Open(T(8))
	tab := w.Tabs[1]
	tab.Title, tab.Color = "web", "blue"
	w.Activate(2)
	if !w.Ungroup(tab.ID) {
		t.Fatal("ungroup")
	}
	var got [][]int
	for _, x := range w.Tabs {
		got = append(got, sessions(x))
	}
	want := [][]int{{9}, {1}, {2}, {3}, {8}}
	if !slices.EqualFunc(got, want, slices.Equal[[]int]) {
		t.Fatalf("tabs %v, want %v", got, want)
	}
	if w.Tabs[1] != tab || tab.Title != "web" || tab.Color != "blue" || w.Tabs[2].Title != "" || w.Tabs[2].Color != "" {
		t.Error("the first pane keeps the tab, its name and color")
	}
	if w.Active != 2 {
		t.Errorf("shown %d, want the focused pane's tab", w.Active)
	}
	ids := map[int]bool{}
	for _, x := range w.Tabs {
		if ids[x.ID] {
			t.Fatalf("tab ID %d twice", x.ID)
		}
		ids[x.ID] = true
		if x.Focus != x.Root.ID || !x.Root.Leaf() {
			t.Errorf("tab %d is not one focused pane", x.ID)
		}
	}
	if w.Ungroup(tab.ID) || w.Ungroup(-1) {
		t.Error("ungrouped a lone pane or a missing tab")
	}
}
