package app

import (
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func addGroupedHost(t *testing.T, a *App, name, group string) assets.Host {
	t.Helper()
	h, err := a.store.Add(assets.Host{Name: name, Address: name, User: "u", Group: group})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Begin at a real label, move through several pointer frames, then let callers
// inspect the preview, release, or cancel with Escape.
func dragSidebarTo(t *testing.T, tt *ui.Tester, from string, x, y float32) {
	t.Helper()
	r := mustFind(t, tt, from)
	sx, sy := r.X+r.W/2, r.Y+r.H/2
	tt.Press(sx, sy)
	for i := 1; i <= 8; i++ {
		f := float32(i) / 8
		tt.Move(sx+(x-sx)*f, sy+(y-sy)*f)
		tt.Frame()
	}
}

// groupHeadAt is a point on the header of group name, by the name's
// text: dy from the header's top, or from its bottom when negative.
func groupHeadAt(t *testing.T, tt *ui.Tester, name string, dy float32) (float32, float32) {
	t.Helper()
	r := mustFind(t, tt, name)
	top := r.Y + r.H/2 - groupRowHeight/2
	if dy < 0 {
		dy += groupRowHeight
	}
	return r.X + 8, top + dy
}

func TestSidebarGroupDragDrop(t *testing.T) {
	a, tt := newTestApp(t)
	addGroupedHost(t, a, "alpha", "A")
	addGroupedHost(t, a, "bravo", "B")
	addGroupedHost(t, a, "charlie", "C")
	tt.Frame()
	x, y := groupHeadAt(t, tt, "A", 2)
	dragSidebarTo(t, tt, "C", x, y)
	tt.Release(x, y)
	tt.Frame()
	if got := a.store.Groups(); !slices.Equal(got, []string{"C", "A", "B"}) {
		t.Fatalf("drop: %q", got)
	}
	if !*a.section("g:C") {
		t.Fatal("dragging a header also collapsed it")
	}

	// A click remains a collapse, and collapsed groups still drag to either end.
	mustClick(t, tt, "C")
	if *a.section("g:C") {
		t.Fatal("header click did not collapse")
	}
	mustClick(t, tt, "B")
	x, y = groupHeadAt(t, tt, "B", -2)
	dragSidebarTo(t, tt, "C", x, y)
	tt.Release(x, y)
	tt.Frame()
	if got := a.store.Groups(); !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Fatalf("drop at end: %q", got)
	}
	if *a.section("g:C") {
		t.Fatal("move lost collapsed state")
	}

	x, y = groupHeadAt(t, tt, "A", 2)
	dragSidebarTo(t, tt, "C", x, y)
	tt.Key(0, ui.KeyEscape)
	tt.Release(x, y)
	tt.Frame()
	if got := a.store.Groups(); !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Fatalf("cancelled drag: %q", got)
	}

	// Hosts retain their own drag value inside a draggable group.
	dst := mustFind(t, tt, "B")
	x, y = dst.X+8, dst.Y+dst.H/2
	dragSidebarTo(t, tt, "alpha", x, y)
	tt.Release(x, y)
	tt.Frame()
	if len(a.store.InGroup("B")) != 2 {
		t.Fatal("host drop did not move the host")
	}
	if !slices.Equal(a.store.Groups(), []string{"B", "C"}) {
		t.Fatal("host drag reordered groups or preserved implicit A")
	}
}

// TestSidebarGroupDropAcrossLevels: a group dropped on the middle of
// another's header goes into it, which opens; on its edge, beside it at
// its level; on the ungrouped hosts, to the top level. The sections
// moved keep their state, and a group never goes into itself.
func TestSidebarGroupDropAcrossLevels(t *testing.T) {
	a, tt := newTestApp(t)
	addGroupedHost(t, a, "web", "Prod/Web")
	addGroupedHost(t, a, "db", "Dev/DB")
	addGroupedHost(t, a, "loose", "")
	*a.section("g:Prod/Web") = false
	tt.Frame()

	// A subgroup into another group, its closed state along.
	mustClick(t, tt, "Dev")
	x, y := groupHeadAt(t, tt, "Dev", groupRowHeight/2)
	dragSidebarTo(t, tt, "Web", x, y)
	tt.Release(x, y)
	tt.Frame()
	if got := a.store.Groups(); !slices.Equal(got, []string{"Dev", "Dev/DB", "Dev/Web", "Prod"}) {
		t.Fatalf("subgroup into another group: %q", got)
	}
	if !*a.section("g:Dev") || *a.section("g:Dev/Web") {
		t.Fatal("drop target not opened, or moved section reopened")
	}

	// A top-level group, its subgroups with it, beside a subgroup.
	x, y = groupHeadAt(t, tt, "DB", 2)
	dragSidebarTo(t, tt, "Prod", x, y)
	tt.Release(x, y)
	tt.Frame()
	if got := a.store.Groups(); !slices.Equal(got, []string{"Dev", "Dev/Prod", "Dev/DB", "Dev/Web"}) {
		t.Fatalf("group beside a subgroup: %q", got)
	}

	// Into itself: nothing moves.
	if groupDropOK("Dev", "Dev/Prod", dropInto) || groupDropOK("Dev", "Dev/Prod", dropBefore) || groupDropOK("Dev/DB", "Dev", dropInto) {
		t.Fatal("drop into itself, or where it is, allowed")
	}

	// Onto the ungrouped hosts: back to the top level.
	x, y = groupHeadAt(t, tt, "loose", groupRowHeight/2)
	dragSidebarTo(t, tt, "Prod", x, y)
	tt.Release(x, y)
	tt.Frame()
	if got := a.store.Groups(); !slices.Equal(got, []string{"Dev", "Dev/DB", "Dev/Web", "Prod"}) {
		t.Fatalf("group onto the ungrouped hosts: %q", got)
	}
}

func TestSidebarGroupSearchOrder(t *testing.T) {
	a, tt := newTestApp(t)
	addGroupedHost(t, a, "match-a", "A")
	addGroupedHost(t, a, "hidden", "B")
	addGroupedHost(t, a, "match-c", "C")
	if err := a.store.AddGroup("Empty"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.MoveGroup("C", "A", false); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Use input events so the bound search field owns the value.
	mustClick(t, tt, "Search hosts")
	tt.Type("match")
	tt.Frame()
	if _, groups, _ := hostsBy(a.store, a.search); a.search != "match" || slices.Contains(groups, "B") || slices.Contains(groups, "Empty") {
		t.Fatalf("search %q included unmatched groups: %q", a.search, groups)
	}
	x, y := groupHeadAt(t, tt, "C", 2)
	dragSidebarTo(t, tt, "A", x, y)
	tt.Release(x, y)
	tt.Frame()
	if !slices.Equal(a.store.Groups(), []string{"C", "A", "B", "Empty"}) {
		t.Fatal("search drag changed the full order")
	}
}

func TestSidebarSectionsToggle(t *testing.T) {
	a, tt := newTestApp(t)
	h := addGroupedHost(t, a, "alpha", "A")
	addGroupedHost(t, a, "bravo", "B")
	addGroupedHost(t, a, "ungrouped-host", "")
	if err := a.store.SetFavorite(h.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddForward(assets.Forward{Name: "forward", HostID: h.ID, Kind: assets.ForwardLocal, ListenPort: 32123, TargetAddr: "localhost", TargetPort: 22}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	mustClick(t, tt, "Collapse all groups")
	for _, key := range []string{"g:A", "g:B", "fav", "none", "fwd"} {
		if *a.section(key) {
			t.Fatalf("section %s was not collapsed", key)
		}
	}
	mustClick(t, tt, "Expand all groups")
	for _, key := range []string{"g:A", "g:B", "fav", "none", "fwd"} {
		if !*a.section(key) {
			t.Fatalf("section %s was not expanded", key)
		}
	}
	mustClick(t, tt, "A") // A mixed state must still offer collapse.
	mustClick(t, tt, "Collapse all groups")
	if *a.section("g:B") || *a.section("none") {
		t.Fatal("sections were left open")
	}
	mustClick(t, tt, "Expand all groups")
	for _, key := range a.sidebarSectionKeys() {
		if !*a.section(key) {
			t.Fatalf("section %s stayed closed", key)
		}
	}
}

func TestSidebarSubgroups(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	web := addGroupedHost(t, a, "web", "Prod")
	tt.Frame()

	// A subgroup made from its group's sheet, with the host it was opened for.
	a.openSubgroupForm("Prod")
	a.group.name, a.group.host = " Cache ", web.ID
	if !a.SaveGroup(m) {
		t.Fatalf("subgroup not saved: %q", a.group.err)
	}
	if h, _ := a.store.Get(web.ID); h.Group != "Prod/Cache" {
		t.Fatalf("host moved into %q", h.Group)
	}
	a.openSubgroupForm("Prod")
	a.group.name = "a/b"
	if a.SaveGroup(m) || a.group.err != m.GroupNameSlash() {
		t.Fatalf("nested subgroup: err %q", a.group.err)
	}
	a.grouping, a.group = false, nil

	// Renaming a group carries its subgroups' open state along.
	*a.section("g:Prod/Cache") = false
	a.openGroupForm("Prod", "")
	if a.group.parent != "" || a.group.name != "Prod" {
		t.Fatalf("rename form %+v", a.group)
	}
	a.group.name = "Live"
	if !a.SaveGroup(m) {
		t.Fatalf("rename: %q", a.group.err)
	}
	if *a.section("g:Live/Cache") {
		t.Fatal("subgroup reopened after renaming its group")
	}
	a.openGroupForm("Live/Cache", "")
	if a.group.parent != "Live" || a.group.name != "Cache" {
		t.Fatalf("subgroup rename form %+v", a.group)
	}
	a.grouping, a.group = false, nil

	// A search shows the group of a subgroup holding a match.
	addGroupedHost(t, a, "other", "Dev")
	if _, groups, byGroup := hostsBy(a.store, "web"); !slices.Equal(groups, []string{"Live", "Live/Cache"}) || len(byGroup["Live/Cache"]) != 1 {
		t.Fatalf("search groups %q", groups)
	}
	tt.Frame()
}

func TestGroupEditLook(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	addGroupedHost(t, a, "web", "Prod")
	tt.Frame()

	a.openGroupForm("Prod", "")
	a.group.name, a.group.icon, a.group.color = "Live", assets.IconPrefix+"database", "purple"
	if !a.SaveGroup(m) {
		t.Fatalf("edit not saved: %q", a.group.err)
	}
	if got := a.store.GroupLook("Live"); got != (assets.GroupLook{Icon: "icon:database", Color: "purple"}) {
		t.Fatalf("saved look %+v", got)
	}
	// The sheet opens on the look saved; an emoji shows in its field.
	a.openGroupForm("Live", "")
	if a.group.icon != "icon:database" || a.group.color != "purple" || a.group.emoji != "" {
		t.Fatalf("edit form %+v", a.group)
	}
	a.group.icon, a.group.emoji = "🚀", "🚀"
	if !a.SaveGroup(m) {
		t.Fatal(a.group.err)
	}
	a.openGroupForm("Live", "")
	if a.group.emoji != "🚀" {
		t.Fatalf("emoji not in its field: %+v", a.group)
	}
	a.grouping, a.group = false, nil

	// A new subgroup made with its look.
	a.openSubgroupForm("Live")
	a.group.name, a.group.color = "Cache", "green"
	if !a.SaveGroup(m) {
		t.Fatal(a.group.err)
	}
	if got := a.store.GroupLook("Live/Cache"); got != (assets.GroupLook{Color: "green"}) {
		t.Fatalf("new subgroup look %+v", got)
	}
	tt.Frame()
}

// TestRevealHostInSidebar: the tab's menu reveals its host in the
// sidebar: shown again, the search emptied, the host's groups unfolded,
// the host chosen and blinking a moment.
func TestRevealHostInSidebar(t *testing.T) {
	a, tt := newTestApp(t)
	for _, g := range []string{"ops", "ops" + assets.GroupSep + "db"} {
		if err := a.store.AddGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	h := addGroupedHost(t, a, "pg", "ops"+assets.GroupSep+"db")
	if _, err := a.open(localID, layout.Terminal, openAt{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.open(h.ID, layout.Terminal, openAt{}, nil); err != nil {
		t.Fatal(err)
	}
	*a.section("g:ops") = false
	*a.section("g:ops" + assets.GroupSep + "db") = false
	a.search = "nothing"
	a.setSidebarHidden(true)
	tt.Frame()
	if _, saved := a.store.Get(a.tabHost(a.ws.Tabs[0])); saved {
		t.Fatal("a local terminal is not in the sidebar")
	}
	a.openContext(10, 10, func() []menuEntry { return a.tabEntries(locales.In("en"), 1) })
	tt.Frame()
	if err := chooseMenu(a, tt, "Reveal Host in Sidebar"); err != nil {
		t.Fatalf("%v: %q", err, menuOf(a))
	}
	tt.Frame()
	if a.settings.SidebarHidden || a.search != "" || !*a.section("g:ops") || !*a.section("g:ops" + assets.GroupSep + "db") {
		t.Fatalf("hidden %v, search %q, groups %v %v", a.settings.SidebarHidden, a.search, *a.section("g:ops"), *a.section("g:ops" + assets.GroupSep + "db"))
	}
	if a.selected != h.ID || a.locate.id != h.ID || a.locate.scroll {
		t.Fatalf("selected %q, locate %+v: the host was not drawn", a.selected, a.locate)
	}
	tt.Advance(2 * locateFlashes * locateBlink)
	tt.Frame()
	if a.locate.id != "" {
		t.Errorf("still blinking: %+v", a.locate)
	}
}
