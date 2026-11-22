package app

import (
	"errors"
	"strconv"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// groupRowHeight is the height of a group's header in the sidebar, that
// of a host there, so that the guide meets either at its middle.
const groupRowHeight = 32

// groupIndent is how far in from a group its subgroups and hosts sit: past
// its arrow and a step past its folder, so that the level shows.
var groupIndent = 14 + theme.Space(4)

// groupTree is a group of the sidebar, as a folder: its header (an arrow,
// its icon, the folder unless it has its own, its name and how many hosts it holds, its subgroups'
// included), then while it is open its subgroups and hosts, which a guide
// on the left joins to it. gap sets it apart from a section above.
func (a *App) groupTree(c *ui.Context, m locales.Messages, tok theme.Tokens, g string, groups []string, byGroup map[string][]assets.Host, gap bool) {
	var subs []string
	count := 0
	for _, sub := range groups {
		if assets.GroupParent(sub) == g {
			subs = append(subs, sub)
		}
		if assets.InGroupTree(sub, g) {
			count += len(byGroup[sub])
		}
	}
	open := a.section("g:" + g)
	sec := ui.Column(c.Key("s:g:" + g)).Gap(theme.Space(0.5)).Shrink(0)
	if gap {
		sec.Margin(theme.Space(3), 0, 0, 0)
	}
	sec.Children(func() {
		head := ui.Row(c.Key("head")).Height(groupRowHeight).Shrink(0).Padding(0, theme.Space(2)).Gap(theme.Space(1.5)).
			AlignItems(ui.Center).Radius(theme.RadiusControl).Role(ui.RoleTreeItem)
		if head.Clicked() {
			*open = !*open
		}
		if head.Hovered() {
			head.Background(tok.Hover)
		}
		head.Expanded(*open).Children(func() {
			arrow := icons.GroupClosed
			if *open {
				arrow = icons.GroupOpen
			}
			ui.Icon(c, arrow.SVG).Size(14, 14).Shrink(0).TextColor(tok.TextMuted)
			if a.multi.on {
				pickBox(c, "pick", assets.GroupLeaf(g), a.groupPick(g), func(on bool) { a.pickGroup(g, on) })
			}
			groupGlyph(c, tok, a.store.GroupLook(g), 16)
			ui.Text(c, assets.GroupLeaf(g)).Grow(1).Shrink(1).MinWidth(0).SingleLine()
			ui.Text(c, strconv.Itoa(count)).FontSize(theme.Footnote).TextColor(tok.TextMuted).Shrink(0).Label(m.HostCount(count))
		})
		if !*open {
			return
		}
		hosts := byGroup[g]
		n, i := max(len(subs)+len(hosts), 1), 0
		branch := func(key string, fn func()) {
			i++
			first, last := i == 1, i == n
			ui.Column(c.Key(key)).PaddingStart(groupIndent).Shrink(0).
				DrawOver(func(p *ui.Painter, r ui.Rect) { groupGuide(p, r, tok, first, last) }).Children(fn)
		}
		for _, sub := range subs {
			branch("b:g:"+sub, func() { a.groupTree(c, m, tok, sub, groups, byGroup, false) })
		}
		for _, h := range hosts {
			branch("b:h:"+h.ID, func() { a.hostItem(c, m, h, "") })
		}
		if len(subs)+len(hosts) == 0 {
			branch("b:empty", func() {
				ui.Text(c, m.EmptyGroup()).FontSize(theme.Caption).TextColor(tok.TextMuted).Padding(theme.Space(2))
			})
		}
	})
	a.groupMenu(c, m, tok, sec, g)
}

// groupGuide draws the guide of a group beside one of its subgroups or
// hosts: down its whole height, from just under the group's arrow for the
// first, and for the last down to the middle of its first row and into it,
// round at the turn.
func groupGuide(p *ui.Painter, r ui.Rect, tok theme.Tokens, first, last bool) {
	x := r.X + theme.Space(2) + 7 // under the group's arrow
	top := r.Y - theme.Space(0.5) // across the gap above it
	if first {
		top -= theme.Space(1.5)
	}
	color := tok.TextMuted.Alpha(0.35)
	if !last {
		p.Fill(ui.Rect{X: x - 0.5, Y: top, W: 1, H: r.Y + r.H - top}, color, 0)
		return
	}
	mid := r.Y + min(r.H, groupRowHeight)/2
	var elbow ui.Path
	elbow.MoveTo(x, top).LineTo(x, mid-theme.RadiusSmall).
		QuadTo(x, mid, x+theme.RadiusSmall, mid).LineTo(r.X+groupIndent, mid)
	p.StrokePath(&elbow, 1, color)
}

// groupMenu gives a group's section its menu, and takes the hosts and
// groups dropped on it.
func (a *App) groupMenu(c *ui.Context, m locales.Messages, tok theme.Tokens, sec ui.Element, g string) {
	a.contextMenu(sec, func() []menuEntry {
		return []menuEntry{
			{Icon: icons.Add, Label: m.NewHostInGroup(), Do: func() {
				a.openEditor(nil)
				a.form.group = g
			}},
			{Icon: icons.NewFolder, Label: m.NewSubgroup(), Do: func() { a.openSubgroupForm(g) }},
			{Icon: icons.Edit, Label: m.EditGroup(), Do: func() { a.openGroupForm(g, "") }},
			menuSep,
			{Icon: icons.Delete, Label: m.DeleteGroup(), Danger: true, Do: func() { a.groupDelete, a.groupDeleting = g, true }},
		}
	})
	a.groupDragDrop(c, m, tok, sec, g)
}

// Separate value types keep hosts moving into groups and groups reordering.
type dragGroup struct{ Name string }
type sidebarDrop interface{ sidebarDrop() }

func (dragHost) sidebarDrop()  {}
func (dragGroup) sidebarDrop() {}

// groupDrop is where a group dropped on another's section goes: into
// that group, or beside it, before or after.
type groupDrop int

const (
	dropInto groupDrop = iota
	dropBefore
	dropAfter
)

// groupDropEdge is how near the top of a group's section, or its bottom,
// a group dropped goes beside it rather than into it.
const groupDropEdge = 8

// groupDropAt is where a group dropped at y in the section of a group,
// h high, goes.
func groupDropAt(y, h float32) groupDrop {
	switch {
	case y < groupDropEdge:
		return dropBefore
	case y >= h-groupDropEdge:
		return dropAfter
	}
	return dropInto
}

// groupDropOK reports whether group name can go where, beside or into
// target: never into itself, and into target only from another group.
func groupDropOK(name, target string, where groupDrop) bool {
	if assets.InGroupTree(target, name) {
		return false
	}
	return where != dropInto || assets.GroupParent(name) != target
}

// dropGroup moves group name where a drop on target's section put it,
// keeping the open state of its sections, and opens target when it went
// into it.
func (a *App) dropGroup(name, target string, where groupDrop) error {
	parent := assets.GroupParent(target)
	var err error
	switch where {
	case dropInto:
		parent = target
		err = a.store.NestGroup(name, target)
	default:
		err = a.store.MoveGroup(name, target, where == dropAfter)
	}
	if err != nil {
		return err
	}
	if path := assets.SubGroup(parent, assets.GroupLeaf(name)); path != name {
		a.moveSections(name, path)
	}
	if where == dropInto {
		*a.section("g:" + target) = true
	}
	return nil
}

func (a *App) groupDragDrop(c *ui.Context, m locales.Messages, tok theme.Tokens, sec ui.Element, g string) {
	if a.search == "" {
		sec.Drag(dragGroup{g})
	}
	_, y, _ := sec.PointerPosition()
	where := groupDropAt(y, sec.Bounds().H)
	if v, ok := ui.DragOver[sidebarDrop](sec); ok {
		switch d := v.(type) {
		case dragGroup:
			if a.search == "" && groupDropOK(d.Name, g, where) {
				sec.DrawOver(func(p *ui.Painter, r ui.Rect) {
					switch where {
					case dropInto:
						r.H = groupRowHeight
						p.Fill(r, tok.Accent.Alpha(0.12), theme.RadiusControl)
						return
					case dropAfter:
						r.Y += r.H - 2
					}
					r.H = 2
					p.Fill(r, tok.Accent, 1)
				})
			}
		case dragHost:
			if h, ok := a.store.Get(d.ID); ok && h.Group != g {
				sec.Background(tok.Accent.Alpha(0.08))
			}
		}
	}
	if v, ok := ui.Drop[sidebarDrop](sec); ok {
		var err error
		switch d := v.(type) {
		case dragGroup:
			if a.search == "" && groupDropOK(d.Name, g, where) {
				err = a.dropGroup(d.Name, g, where)
			}
		case dragHost:
			if h, ok := a.store.Get(d.ID); ok && h.Group != g {
				err = a.store.SetGroup(d.ID, g)
			}
		}
		if err != nil {
			c.AddToast(ui.Toast{Title: groupMoveError(m, err), Type: "error"})
		}
	}
}

// groupMoveError is the toast of a group or host that could not move.
func groupMoveError(m locales.Messages, err error) string {
	var fe *assets.FieldError
	switch {
	case errors.As(err, &fe) && fe.Reason == "taken":
		return m.GroupTaken()
	case errors.As(err, &fe) && fe.Reason == "inside":
		return m.GroupIntoItself()
	}
	return m.ErrGeneric(err.Error())
}

// Include sections hidden by search, but ignore stale state of deleted groups.
func (a *App) sidebarSectionKeys() []string {
	var keys []string
	for _, g := range a.store.Groups() {
		keys = append(keys, "g:"+g)
	}
	if len(a.store.Favorites()) > 0 {
		keys = append(keys, "fav")
	}
	if len(a.store.InGroup("")) > 0 {
		keys = append(keys, "none")
	}
	if len(a.store.Forwards()) > 0 {
		keys = append(keys, "fwd")
	}
	return keys
}

func (a *App) sidebarSectionsButton(c *ui.Context, m locales.Messages) {
	keys := a.sidebarSectionKeys()
	anyOpen := false
	for _, key := range keys {
		anyOpen = anyOpen || *a.section(key)
	}
	icon, label := icons.ExpandAll, m.ExpandAllGroups()
	if anyOpen {
		icon, label = icons.CollapseAll, m.CollapseAllGroups()
	}
	if (IconButton{Icon: icon, Label: label, Size: Small, Disabled: len(keys) == 0}).Clicked(c, "sections") {
		for _, key := range keys {
			*a.section(key) = !anyOpen
		}
	}
}
