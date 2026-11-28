package app

import (
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// extView is what a files pane shows of its external edits: the list
// (open), the question asked of one (prompt), the ones put off until
// they change, and the one whose tracking is to stop.
type extView struct {
	open       bool
	prompt     *extEdit
	promptOpen bool
	hushed     map[*extEdit]extHush
	stop       *extEdit
	stopOpen   bool
}

// extHush is what an edit was when its question was put off: it is asked
// again once that changes.
type extHush struct {
	state extState
	sum   [32]byte
}

func hushOf(ed *extEdit) extHush { return extHush{state: ed.state, sum: ed.observed.sum} }

// needsAnswer says an edit waits on the user: a save to upload, in ask
// mode, or a conflict with the server.
func (ed *extEdit) needsAnswer() bool {
	return ed.state == extConflict || ed.state == extChanged && ed.ask
}

// extEditEntry is the menu entry that edits the file p in an external
// editor: the one editor, a submenu of them, or one that sets one up.
func (a *App) extEditEntry(cn *conn, remote bool, p string, size int64, disabled bool, m locales.Messages) menuEntry {
	eds := a.settings.ExternalEditors
	edit := func(ed settings.ExternalEditor) func() {
		return func() { a.editExternally(cn, remote, p, size, extHow{editor: &ed}) }
	}
	switch len(eds) {
	case 0:
		return menuEntry{Icon: icons.ExternalEdit, Label: m.SetUpExternalEditor(), Do: func() { a.openSetting(prefFiles, m.ExternalEditors()) }}
	case 1:
		return menuEntry{Icon: icons.ExternalEdit, Label: m.EditExternallyIn(editorLabel(eds[0])), Disabled: disabled, Do: edit(eds[0])}
	}
	sub := make([]menuEntry, len(eds))
	for i, ed := range eds {
		sub[i] = menuEntry{Label: editorLabel(ed), Do: edit(ed)}
	}
	return menuEntry{Icon: icons.ExternalEdit, Label: m.EditExternally(), Disabled: disabled, Sub: sub}
}

// hiddenEntry switches the dotfiles of a side on and off.
func hiddenEntry(sd *sideList, m locales.Messages) menuEntry {
	return menuEntry{Icon: icons.ShowHidden, Label: m.ShowHiddenFiles(), Checked: sd.showHidden, Do: func() { sd.setShowHidden(!sd.showHidden) }}
}

// extEditsAction is the header's button for the pane's external edits,
// with how many there are; red while one needs a hand.
func (a *App) extEditsAction(cn *conn, m locales.Messages) barAction {
	fs := cn.fs
	b := IconButton{Icon: icons.ExternalEdit, Label: m.ExternalEdits(), Active: fs.extView.open, Size: Tiny, Badge: strconv.Itoa(len(fs.edits))}
	for _, ed := range fs.edits {
		if ed.state == extConflict || ed.state == extFailed {
			b.BadgeDanger = true
		}
	}
	return barAction{Key: "extedits", Button: b, Do: func() { fs.extView.open = true }}
}

// extStateText is what an edit's row says of it, and its color.
func extStateText(m locales.Messages, tok theme.Tokens, ed *extEdit) (string, ui.Color) {
	switch ed.state {
	case extOpening:
		return m.ExtStateOpening(), tok.TextMuted
	case extChanged:
		return m.ExtStateChanged(), tok.Warning
	case extUploading:
		return m.ExtStateUploading(), tok.TextMuted
	case extConflict:
		return m.ExtStateConflict(), tok.Danger
	case extFailed:
		return m.ExtStateFailed(), tok.Danger
	}
	return m.ExtStateWatching(), tok.Success
}

// extViews draws what a pane asks and shows of its external edits.
func (a *App) extViews(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	a.extEditsSheet(c, cn, tok, m)
	a.extPromptView(c, cn, m)
	a.extStopView(c, cn, m)
	a.extCloseView(c, cn, m)
}

// extEditsSheet lists the files of the pane open in other apps: what
// each is at, and what can be done to it.
func (a *App) extEditsSheet(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	v := &fs.extView
	if !v.open {
		return
	}
	sheet(ui.Modal(c, &v.open, func() {
		ui.Column(c.Key("extedits")).Gap(14).Width(600).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
				tile(c, icons.ExternalEdit, theme.Brand, 36)
				ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
					sheetTitle(c, m.ExternalEdits())
					ui.Text(c, m.ExternalEditsNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
				})
			})
			box := ui.Column(c).MinHeight(120).MaxHeight(360).FillWidth().Padding(6).Radius(theme.RadiusCard - 2).Background(tok.Fill)
			box.Children(func() {
				if len(fs.edits) == 0 {
					ui.Column(c).Grow(1).Center().Children(func() {
						ui.Text(c, m.ExternalEditsEmpty()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
					})
					return
				}
				ui.Scroll(c.Key("extscroll")).Grow(1).FillWidth().Children(func() {
					ui.Column(c).FillWidth().Gap(2).Children(func() {
						for i, ed := range fs.edits {
							a.extEditRow(c, cn, tok, m, i, ed)
						}
					})
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Close(), true, false, false) {
					v.open = false
				}
			})
		})
	}), tok)
}

func (a *App) extEditRow(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, i int, ed *extEdit) {
	name := shownName(ed.name)
	ui.Column(c.Key("ext:"+strconv.Itoa(i))).FillWidth().Padding(6, 8).Gap(6).Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			fileImage(c, entryIcon(sftpsvc.Entry{Name: ed.name}), 20)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, name).FontSize(theme.Footnote).FontWeight(600).SingleLine()
				where := a.pv(shownName(ed.remote))
				ui.Text(c, where).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Tooltip(where)
			})
			text, col := extStateText(m, tok, ed)
			ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
				if ed.state == extOpening || ed.state == extUploading {
					ui.Spinner(c).Size(12, 12)
				}
				lbl := ui.Text(c, text).FontSize(theme.Caption).TextColor(col).SingleLine()
				if ed.err != nil {
					lbl.Tooltip(externalErrorText(m, ed.err))
				}
			})
			ui.Row(c).Shrink(0).Gap(2).Children(func() {
				canUpload := ed.state == extChanged || ed.state == extFailed
				if (IconButton{Icon: icons.Upload, Label: m.ExtUploadNow(name), Size: Small, Disabled: !canUpload}).Clicked(c, "upload") {
					a.extUploadNow(cn, ed)
				}
				if (IconButton{Icon: icons.OpenFile, Label: m.ExtReveal(name), Size: Small, Disabled: ed.state == extOpening || a.p.OpenPath == nil}).Clicked(c, "reveal") {
					a.extReveal(cn, ed)
				}
				if (IconButton{Icon: icons.Close, Label: m.ExtStop(name), Size: Small, Danger: true, Disabled: ed.state == extUploading}).Clicked(c, "stop") {
					a.askExtStop(cn, ed)
				}
			})
		})
		if ed.state == extConflict {
			ui.Row(c).FillWidth().Gap(8).Padding(0, 0, 0, 30).Children(func() {
				if pill(c, m.ExtTakeServer(), false, false, false) {
					a.extTakeServer(cn, ed)
				}
				if pill(c, m.ExtOverwrite(), false, true, false) {
					a.extOverwrite(cn, ed)
				}
			})
		}
	})
}

// askExtStop stops tracking an edit, after asking when its copy holds
// changes the server does not have.
func (a *App) askExtStop(cn *conn, ed *extEdit) {
	for _, u := range cn.fs.extUnsynced() {
		if u == ed {
			cn.fs.extView.stop, cn.fs.extView.stopOpen = ed, true
			return
		}
	}
	a.extStop(cn, ed)
}

func (a *App) extStopView(c *ui.Context, cn *conn, m locales.Messages) {
	v := &cn.fs.extView
	ed := v.stop
	if ed == nil {
		return
	}
	if ui.AlertDialog(c.Key("extstop"), &v.stopOpen, m.ExtStopTitle(shownName(ed.name)), m.ExtStopMessage(), m.Cancel(), m.ExtStopConfirm()) == 1 {
		a.extStop(cn, ed)
	}
	if !v.stopOpen {
		v.stop = nil
	}
}

// extPromptView asks about the first edit waiting on the user, unless
// that question was put off and nothing changed since.
func (a *App) extPromptView(c *ui.Context, cn *conn, m locales.Messages) {
	fs := cn.fs
	v := &fs.extView
	var ed *extEdit
	for _, e := range fs.edits {
		if h, ok := v.hushed[e]; ok && h != hushOf(e) {
			delete(v.hushed, e)
		}
		if _, hushed := v.hushed[e]; ed == nil && e.needsAnswer() && !hushed {
			ed = e
		}
	}
	for e := range v.hushed {
		if fs.externalEdit(e.remote) != e {
			delete(v.hushed, e)
		}
	}
	if ed == nil || v.open {
		// The list, when open, answers there.
		v.prompt, v.promptOpen = nil, false
		return
	}
	if v.prompt != ed {
		v.prompt, v.promptOpen = ed, true
	}
	name := shownName(ed.name)
	if ed.state == extConflict {
		switch ui.AlertDialog(c.Key("extconflict"), &v.promptOpen, m.ExtConflictTitle(name), m.ExtConflictMessage(), m.ExtLater(), m.ExtTakeServer(), m.ExtOverwrite()) {
		case 1:
			a.extTakeServer(cn, ed)
		case 2:
			a.extOverwrite(cn, ed)
		}
	} else {
		switch ui.AlertDialog(c.Key("extask"), &v.promptOpen, m.ExtAskTitle(name), m.ExtAskMessage(name), m.ExtDontUpload(), m.Upload()) {
		case 0:
			a.extSkip(cn, ed)
		case 1:
			a.extUploadNow(cn, ed)
		}
	}
	if !v.promptOpen && v.prompt == ed {
		if ed.needsAnswer() {
			if v.hushed == nil {
				v.hushed = map[*extEdit]extHush{}
			}
			v.hushed[ed] = hushOf(ed)
		}
		v.prompt = nil
	}
}

// extCloseView confirms closing a pane whose external edits have changes
// the server does not have; once none is left it closes without asking.
func (a *App) extCloseView(c *ui.Context, cn *conn, m locales.Messages) {
	fs := cn.fs
	if !fs.extCloseAsk {
		return
	}
	left := fs.extUnsynced()
	if len(left) == 0 {
		a.extConfirmClose(cn)
		return
	}
	names := make([]string, 0, 4)
	for i, ed := range left {
		if i == 3 {
			names = append(names, "…")
			break
		}
		names = append(names, shownName(ed.name))
	}
	open := true
	if ui.AlertDialog(c.Key("extclose"), &open, m.ExtCloseTitle(), m.ExtCloseMessage(strings.Join(names, m.ExtNameSeparator())), m.Cancel(), m.ExtCloseAnyway()) == 1 {
		a.extConfirmClose(cn)
		return
	}
	if !open {
		fs.extCloseAsk, fs.extCloseDo = false, nil
	}
}
