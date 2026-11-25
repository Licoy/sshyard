package app

import (
	"errors"
	"strconv"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// multiSelect is the sidebar choosing several hosts and groups at once,
// on while the toolbar shows what can be done with them; hosts and groups
// are those checked, by ID and path; deleting asks to confirm deleting
// them.
type multiSelect struct {
	on       bool
	hosts    map[string]bool
	groups   map[string]bool
	deleting bool
}

// OpenMultiSelect shows a check box before every host and group, and the
// actions on those checked across the toolbar, in place of the search.
func (a *App) OpenMultiSelect() {
	a.search, a.searchOpen = "", false
	a.multi = multiSelect{on: true, hosts: map[string]bool{}, groups: map[string]bool{}}
}

// CloseMultiSelect leaves choosing several, forgetting those checked.
func (a *App) CloseMultiSelect() {
	a.multi = multiSelect{}
}

// clearPicks unchecks every host and group.
func (a *App) clearPicks() {
	a.multi.hosts, a.multi.groups = map[string]bool{}, map[string]bool{}
}

// pickHost checks host id, or unchecks it, and the groups it is in as
// all they hold then is.
func (a *App) pickHost(id string, on bool) {
	a.setHostPick(id, on)
	a.syncGroupPicks()
}

func (a *App) setHostPick(id string, on bool) {
	if on {
		a.multi.hosts[id] = true
	} else {
		delete(a.multi.hosts, id)
	}
}

// syncGroupPicks checks each group holding hosts or subgroups exactly
// when all of them are, the deepest first; an empty group keeps its own
// check.
func (a *App) syncGroupPicks() {
	groups, hosts := a.store.Groups(), a.store.List()
	// In tree order a subgroup comes after its group: backwards, before.
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		holds, all := false, true
		for _, h := range hosts {
			if h.Group == g {
				holds, all = true, all && a.multi.hosts[h.ID]
			}
		}
		for _, sub := range groups {
			if assets.GroupParent(sub) == g {
				holds, all = true, all && a.multi.groups[sub]
			}
		}
		switch {
		case !holds:
		case all:
			a.multi.groups[g] = true
		default:
			delete(a.multi.groups, g)
		}
	}
}

// pickState is how a check box shows: unchecked, checked, or mixed for a
// group of which some is checked and some not.
type pickState int

const (
	pickNone pickState = iota
	pickFull
	pickSome
)

// groupPick is how the check box of group g shows.
func (a *App) groupPick(g string) pickState {
	if a.multi.groups[g] {
		return pickFull
	}
	for _, h := range a.store.List() {
		if a.multi.hosts[h.ID] && h.Group != "" && assets.InGroupTree(h.Group, g) {
			return pickSome
		}
	}
	for sub := range a.multi.groups {
		if assets.InGroupTree(sub, g) {
			return pickSome
		}
	}
	return pickNone
}

// pickGroup checks group g, or unchecks it, with its subgroups and their
// hosts, and the groups it is in as all they hold then is.
func (a *App) pickGroup(g string, on bool) {
	defer a.syncGroupPicks()
	for _, sub := range a.store.Groups() {
		if !assets.InGroupTree(sub, g) {
			continue
		}
		if on {
			a.multi.groups[sub] = true
		} else {
			delete(a.multi.groups, sub)
		}
	}
	for _, h := range a.store.List() {
		if assets.InGroupTree(h.Group, g) {
			a.setHostPick(h.ID, on)
		}
	}
}

// pickAll checks every host and group, or none.
func (a *App) pickAll(on bool) {
	a.clearPicks()
	if !on {
		return
	}
	for _, g := range a.store.Groups() {
		a.multi.groups[g] = true
	}
	for _, h := range a.store.List() {
		a.multi.hosts[h.ID] = true
	}
}

// picked are the hosts checked that still are, the groups checked that
// still are, and the groups at the top of those (whose groups are not
// checked too).
func (a *App) picked() (hosts []assets.Host, groups, tops []string) {
	for _, h := range a.store.List() {
		if a.multi.hosts[h.ID] {
			hosts = append(hosts, h)
		}
	}
	for _, g := range a.store.Groups() {
		if !a.multi.groups[g] {
			continue
		}
		groups = append(groups, g)
		top := true
		for p := assets.GroupParent(g); p != ""; p = assets.GroupParent(p) {
			top = top && !a.multi.groups[p]
		}
		if top {
			tops = append(tops, g)
		}
	}
	return
}

// allPicked reports whether every host and group is checked, and there
// is one at least.
func (a *App) allPicked() bool {
	hosts, groups, _ := a.picked()
	return len(hosts)+len(groups) > 0 && len(hosts) == a.store.Len() && len(groups) == len(a.store.Groups())
}

// pickedPingable reports whether a host checked can be measured.
func (a *App) pickedPingable() bool {
	hosts, _, _ := a.picked()
	for _, h := range hosts {
		if _, ok := a.pingAddr(h); ok {
			return true
		}
	}
	return false
}

// PingPicked measures the hosts checked.
func (a *App) PingPicked() {
	hosts, _, _ := a.picked()
	for _, h := range hosts {
		a.pingHost(h)
	}
}

// MovePicked moves the groups checked into group, "" for the top level,
// each with its subgroups and hosts, and the other hosts checked into it,
// "" for none. It goes on past one that cannot move, and returns the
// first error; the checks are cleared.
func (a *App) MovePicked(group string) error {
	hosts, _, tops := a.picked()
	var first error
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, g := range tops {
		if assets.GroupParent(g) == group {
			continue
		}
		if group != "" && assets.InGroupTree(group, g) {
			keep(&assets.FieldError{Field: "group", Reason: "inside"})
			continue
		}
		if err := a.store.NestGroup(g, group); err != nil {
			keep(err)
			continue
		}
		a.moveSections(g, assets.SubGroup(group, assets.GroupLeaf(g)))
	}
	for _, h := range hosts {
		if h.Group == group || inAnyTree(h.Group, tops) {
			continue
		}
		if err := a.store.SetGroup(h.ID, group); err != nil {
			keep(err)
		}
	}
	if group != "" {
		*a.section("g:" + group) = true
	}
	a.clearPicks()
	return first
}

// inAnyTree reports whether group g is one of groups, or in one of them.
func inAnyTree(g string, groups []string) bool {
	for _, top := range groups {
		if g != "" && assets.InGroupTree(g, top) {
			return true
		}
	}
	return false
}

// DeletePicked deletes the hosts checked, their secrets with them, then
// the groups checked, all they hold checked too, with their subgroups; a
// host left in one, still used by a host kept, stays in no group. A host
// still used by one kept is not deleted: the error says which. The checks
// are cleared.
func (a *App) DeletePicked() error {
	hosts, _, tops := a.picked()
	var ids []string
	for _, h := range hosts {
		ids = append(ids, h.ID)
	}
	err := a.deleteHosts(ids)
	if errors.Is(err, keychain.ErrLocked) {
		return err
	}
	for _, g := range tops {
		if gerr := a.deleteGroup(g); gerr != nil && !errors.Is(gerr, assets.ErrNotFound) && err == nil {
			err = gerr
		}
	}
	a.clearPicks()
	return err
}

// deleteHosts deletes the hosts ids, over as many rounds as it takes for
// those jumped through by others deleted, and returns the error of a
// round that deleted none of those left. Hosts already gone are passed
// over, so that it can run again once the secrets are unlocked.
func (a *App) deleteHosts(ids []string) error {
	for len(ids) > 0 {
		var left []string
		var last error
		for _, id := range ids {
			if _, ok := a.store.Get(id); !ok {
				continue
			}
			if err := a.DeleteHost(id); err != nil {
				if errors.Is(err, keychain.ErrLocked) {
					return err
				}
				left, last = append(left, id), err
			}
		}
		if len(left) == len(ids) {
			return last
		}
		ids = left
	}
	return nil
}

// multiSelectBar is the toolbar while choosing several: a check box
// checking all, how many are checked, then measuring, moving and deleting
// them, and leaving.
func (a *App) multiSelectBar(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	hosts, groups, _ := a.picked()
	n := len(hosts) + len(groups)
	all := pickNone
	switch {
	case a.allPicked():
		all = pickFull
	case n > 0:
		all = pickSome
	}
	pickBox(c, "pickall", m.SelectAll(), all, a.pickAll)
	ui.Text(c, m.SelectedCount(strconv.Itoa(n))).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine().Grow(1).Shrink(1).MinWidth(0)
	if (IconButton{Icon: icons.TestLatency, Label: m.TestLatency(), Disabled: !a.pickedPingable() || a.pinging()}).Clicked(c, "pickping") {
		a.PingPicked()
	}
	a.menuButton(c, "pickmove", IconButton{Icon: icons.MoveTo, Label: m.MoveToGroup(), Disabled: n == 0}, func() []menuEntry {
		return a.movePickedEntries(c, m)
	})
	if (IconButton{Icon: icons.Delete, Label: m.DeleteSelected(), Danger: true, Disabled: n == 0}).Clicked(c, "pickdelete") {
		a.multi.deleting = true
	}
	if (IconButton{Icon: icons.Close, Label: m.CloseMultiSelect()}).Clicked(c, "pickclose") {
		a.CloseMultiSelect()
	}
}

// movePickedEntries are the groups the hosts and groups checked can move
// to: none (the top level), then every group not among those moved.
func (a *App) movePickedEntries(c *ui.Context, m locales.Messages) []menuEntry {
	_, _, tops := a.picked()
	move := func(g string) func() {
		return func() {
			if err := a.MovePicked(g); err != nil {
				c.AddToast(ui.Toast{Title: groupMoveError(m, err), Type: "error"})
			}
		}
	}
	entries := []menuEntry{{Icon: icons.Folder, Label: m.NoGroup(), Do: move("")}}
	groups := a.store.Groups()
	if len(groups) > 0 {
		entries = append(entries, menuSep)
	}
	for _, g := range groups {
		entries = append(entries, menuEntry{Icon: icons.Folder, Label: g, Disabled: inAnyTree(g, tops), Do: move(g)})
	}
	return entries
}

// multiSelectView is the dialog confirming deleting those checked.
func (a *App) multiSelectView(c *ui.Context, m locales.Messages) {
	if !a.multi.deleting {
		return
	}
	hosts, _, tops := a.picked()
	switch ui.AlertDialog(c, &a.multi.deleting, m.DeleteSelected(), m.DeleteSelectedConfirm(strconv.Itoa(len(hosts)+len(tops))), m.Cancel(), m.Delete()) {
	case 1:
		a.runSecretAction(a.DeletePicked, func(err error) { a.toastError(a.inUseText(m, err)) })
	}
}

// pickBox is the check box of a host or group while choosing several,
// toggle told what it is set to: checked from mixed. A toggle changes the
// boxes of the groups above, already built this frame, and those of the
// toolbar: the next frame shows them. MyGo's Checkbox has no mixed look,
// which is drawn as its own: the accent box with a dash.
func pickBox(c *ui.Context, key, label string, state pickState, toggle func(bool)) {
	on := state == pickFull
	if state != pickSome {
		formCheckbox(c.Key(key), &on, "").Label(label).Shrink(0)
		if on != (state == pickFull) {
			toggle(on)
			c.Invalidate()
		}
		return
	}
	t := c.Theme()
	row := ui.CheckboxBase(c.Key(key), &on).Label(label).Mixed().FocusRing(false)
	row.Children(func() {
		ui.Box(c).Size(t.Space(4), t.Space(4)).Radius(t.Space(1)).Shrink(0).Background(t.Accent).
			DrawOver(func(p *ui.Painter, r ui.Rect) {
				w := r.W / 2
				p.Fill(ui.Rect{X: r.X + (r.W-w)/2, Y: r.Y + r.H/2 - 1, W: w, H: 2}, t.AccentText, 1)
				if row.FocusVisible() {
					rad := t.Space(1)
					p.FocusRing(r, [4]float32{rad, rad, rad, rad})
				}
			})
	})
	if on {
		toggle(true)
		c.Invalidate()
	}
}
