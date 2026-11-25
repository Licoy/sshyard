package app

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// groupForm is the group sheet: a new group or subgroup, or a group
// edited, its name and its look.
type groupForm struct {
	// old is the group renamed, "" for a new one
	old string
	// parent is the group of a subgroup, "" for a top-level group; name
	// is the group's own name, without it
	parent, name string
	// icon and color are the group's look (assets.GroupLook), "" for the
	// folder; emoji is the text typed for its icon
	icon, color, emoji string
	// host is moved into the new group once it is made, when the sheet
	// was opened from its menu
	host string
	err  string
}

func (a *App) openGroupForm(old, host string) {
	f := &groupForm{old: old, parent: assets.GroupParent(old), name: assets.GroupLeaf(old), host: host}
	if old != "" {
		look := a.store.GroupLook(old)
		f.icon, f.color = look.Icon, look.Color
		if !strings.HasPrefix(look.Icon, assets.IconPrefix) {
			f.emoji = look.Icon
		}
	}
	a.group, a.grouping = f, true
}

// look is the group's look as the sheet has it so far.
func (f *groupForm) look() assets.GroupLook { return assets.GroupLook{Icon: f.icon, Color: f.color} }

// openSubgroupForm opens the sheet making a subgroup of parent.
func (a *App) openSubgroupForm(parent string) {
	a.group, a.grouping = &groupForm{parent: parent}, true
}

// SaveGroup makes or edits the group of the sheet.
func (a *App) SaveGroup(m locales.Messages) bool {
	f := a.group
	if f == nil {
		return false
	}
	if strings.Contains(f.name, assets.GroupSep) {
		f.err = m.GroupNameSlash()
		return false
	}
	path := f.name
	if f.parent != "" {
		path = assets.SubGroup(f.parent, strings.TrimSpace(f.name))
	}
	a.pruneGroupSections()
	var err error
	if f.old == "" {
		err = a.store.AddGroupLook(path, f.look())
	} else {
		err = a.store.EditGroup(f.old, path, f.look())
	}
	if err != nil {
		var fe *assets.FieldError
		switch {
		case errors.As(err, &fe) && fe.Reason == "taken":
			f.err = m.GroupTaken()
		case errors.As(err, &fe) && fe.Reason == "inside":
			f.err = m.GroupIntoItself()
		case errors.As(err, &fe):
			f.err = m.RequiredField(m.FieldGroup())
		default:
			f.err = m.ErrGeneric(err.Error())
		}
		return false
	}
	name := assets.CleanGroup(path) // as the store saved it
	for _, g := range a.store.Groups() {
		if strings.EqualFold(g, name) {
			name = g
		}
	}
	if f.host != "" {
		a.store.SetGroup(f.host, name)
	}
	if f.old != "" {
		a.moveSections(f.old, name)
	}
	a.group, a.grouping = nil, false
	return true
}

// moveSections keeps the sections of group old, its subgroups' too, as
// open or closed as they were once it is name.
func (a *App) moveSections(old, name string) {
	if old == name {
		return
	}
	moved := map[string]*bool{}
	for key, open := range a.sections {
		if g, ok := strings.CutPrefix(key, "g:"); ok && assets.InGroupTree(g, old) {
			moved["g:"+name+strings.TrimPrefix(g, old)] = open
			delete(a.sections, key)
		}
	}
	maps.Copy(a.sections, moved)
}

// pruneGroupSections forgets groups that no longer exist, including an
// implicit group that lost its last host.
func (a *App) pruneGroupSections() {
	groups := a.store.Groups()
	live := make(map[string]bool, len(groups))
	for _, group := range groups {
		live[group] = true
	}
	maps.DeleteFunc(a.sections, func(key string, _ *bool) bool {
		group, ok := strings.CutPrefix(key, "g:")
		return ok && !live[group]
	})
}

func (a *App) deleteGroup(name string) error {
	err := a.store.DeleteGroup(name)
	if err == nil || errors.Is(err, assets.ErrNotFound) {
		a.pruneGroupSections()
	}
	return err
}

func (a *App) groupView(c *ui.Context, m locales.Messages) {
	if f := a.group; f != nil {
		tok := currentTokens(c)
		sheet(ui.Modal(c, &a.grouping, func() {
			ui.Column(c).Gap(18).Width(460).Children(func() {
				title := m.NewGroup()
				switch {
				case f.old != "":
					title = m.EditGroup()
				case f.parent != "":
					title = m.NewSubgroup()
				}
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					// The group's icon as it will look.
					ui.Box(c).Size(36, 36).Shrink(0).Radius(theme.RadiusControl).Background(tok.Fill).Center().Children(func() {
						groupGlyph(c, tok, f.look(), 20)
					})
					ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						sheetTitle(c, title)
						if f.parent != "" {
							ui.Text(c, m.SubgroupOf(f.parent)).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
						}
					})
				})
				ui.Form(c, func() {
					ui.Field(c, m.FieldGroup(), func() {
						in := formTextInput(c.Key("g:name"), &f.name).Placeholder(m.GroupPlaceholder()).AutoFocus()
						a.groupHold.apply(c, f, in)
						if in.Submitted() {
							a.SaveGroup(m)
						}
					})
					ui.Field(c, m.FieldAppearance(), func() {
						a.lookFields(c, m, tok, &f.icon, &f.emoji, &f.color, m.GroupFolderIcon(), m.GroupDefaultColor())
					})
				})
				if f.err != "" {
					ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
				}
				ui.Row(c).Gap(8).Children(func() {
					ui.Spacer(c)
					if pill(c, m.Cancel(), false, false, false) {
						a.grouping = false
					}
					if pill(c, m.Save(), true, false, false) {
						a.SaveGroup(m)
					}
				})
			})
		}), tok)
		if !a.grouping {
			a.group = nil
		}
	}
	if a.groupDeleting {
		msg := m.DeleteGroupConfirm(a.groupDelete)
		if slices.ContainsFunc(a.store.Groups(), func(g string) bool { return assets.GroupParent(g) == a.groupDelete }) {
			msg = m.DeleteGroupTreeConfirm(a.groupDelete)
		}
		switch ui.AlertDialog(c, &a.groupDeleting, m.DeleteGroup(), msg, m.Cancel(), m.Delete()) {
		case 1:
			if err := a.deleteGroup(a.groupDelete); err != nil {
				c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
			}
		}
	}
}
