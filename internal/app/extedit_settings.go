package app

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The Files page's external editing settings, and the sheet listing the
// editors remote files open in.

// fileEditors are FileEditor's choices, in the order its control shows.
var fileEditors = []string{settings.FileEditorBuiltin, settings.FileEditorSystem, settings.FileEditorExternal}

// prefExternal is the Files page's section on editing files in other
// apps: what a text file opens in, the editors, and what a save does.
func (a *App) prefExternal(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	a.settingsSection(c, tok, icons.ExternalEdit, m.ExternalEdits(), func(g *settingsGroup) {
		g.row(m.FileEditor(), m.FileEditorNote(), func() {
			at := max(slices.Index(fileEditors, s.FileEditor), 0)
			if formSegmented(c.Key("fileeditor"), &at, false, m.FileEditorBuiltin(), m.FileEditorSystem(), m.FileEditorExternal()).Label(m.FileEditor()).Changed() {
				if at >= 0 && at < len(fileEditors) {
					s.FileEditor, *changed = fileEditors[at], true
				}
			}
		})
		g.row(m.ExternalEditors(), m.ExternalEditorsNote(), func() {
			sum := m.ExternalEditorsNone()
			if len(s.ExternalEditors) > 0 {
				names := make([]string, len(s.ExternalEditors))
				for i, ed := range s.ExternalEditors {
					names[i] = editorLabel(ed)
				}
				sum = strings.Join(names, m.ExtNameSeparator())
			}
			ui.Text(c, sum).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine().MaxWidth(200).Tooltip(sum)
			if pill(c, m.ExternalEditorsManage(), false, false, false) {
				a.openEditors()
			}
		})
		g.row(m.ExternalUpload(), m.ExternalUploadNote(), func() {
			at := 0
			if s.ExternalUpload == settings.ExternalUploadAsk {
				at = 1
			}
			if formSegmented(c.Key("extupload"), &at, false, m.ExternalUploadAuto(), m.ExternalUploadAsk()).Label(m.ExternalUpload()).Changed() {
				s.ExternalUpload, *changed = settings.ExternalUploadAuto, true
				if at == 1 {
					s.ExternalUpload = settings.ExternalUploadAsk
				}
			}
		})
		g.row(m.ExternalNotify(), m.ExternalNotifyNote(), func() {
			on := s.ExternalNotify
			if formSwitch(c.Key("extnotify"), &on).Label(m.ExternalNotify()).Changed() {
				s.ExternalNotify, *changed = on, true
			}
		})
	})
	a.editorsSheet(c, m, tok)
}

// prefFilesMore are the Files page's rows on what the panes list and
// what an upload's files get.
func (a *App) prefFilesMore(c *ui.Context, g *settingsGroup, m locales.Messages, s *settings.Settings, changed *bool) {
	g.row(m.ShowHiddenFiles(), m.ShowHiddenFilesNote(), func() {
		on := !s.HideHiddenFiles
		if formSwitch(c.Key("showhidden"), &on).Label(m.ShowHiddenFiles()).Changed() {
			s.HideHiddenFiles, *changed = !on, true
		}
	})
	g.row(m.UploadFileMode(), m.UploadFileModeNote(), func() {
		in := formTextInput(c.Key("upmode"), &a.upMode).Label(m.UploadFileMode()).Placeholder(m.UploadFileModeKeep()).Width(96)
		focused := in.Focused()
		// Enter, or the caret leaving the field: a half-typed mode is
		// not applied to the uploads queued meanwhile.
		if in.Submitted() || (a.upModeFocus && !focused) {
			if mode, ok := uploadModeText(a.upMode); !ok {
				a.upMode = s.UploadFileMode
				a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.UploadFileModeInvalid(), Type: "error"} })
			} else if mode != s.UploadFileMode {
				s.UploadFileMode, *changed = mode, true
				a.upMode = mode
			}
		}
		a.upModeFocus = focused
	})
}

// uploadModeText is the mode typed, as UploadFileMode keeps it: "" to
// keep the files' own, else three octal digits. ok is false for what is
// not a mode.
func uploadModeText(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	mode, ok := settings.ParseUploadFileMode(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%03o", mode), true
}

// editorLabel names an editor: its name, else its program's.
func editorLabel(ed settings.ExternalEditor) string {
	if ed.Name != "" {
		return ed.Name
	}
	base := filepath.Base(strings.ReplaceAll(ed.Program, `\`, "/"))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// editorsForm is the external editors' sheet: the editors as they will
// be saved, those found on this computer, and the one being added or
// changed (at, -1 for a new one; draft nil for none).
type editorsForm struct {
	list     []settings.ExternalEditor
	found    []settings.ExternalEditor
	scanning bool
	draft    *settings.ExternalEditor
	at       int
	err      string
	hold     focusHold
}

// scanEditors finds the editors on this computer; tests replace it.
var scanEditors = func() []settings.ExternalEditor { return detectEditors(runtime.GOOS) }

func (a *App) openEditors() {
	f := &editorsForm{list: slices.Clone(a.settings.ExternalEditors)}
	a.edForm, a.edFormOpen = f, true
	a.scanEditorsFor(f)
}

func (a *App) scanEditorsFor(f *editorsForm) {
	if f.scanning {
		return
	}
	f.scanning = true
	go func() {
		found := scanEditors()
		a.post(func() { f.found, f.scanning = found, false })
	}()
}

// has says an editor with the program p is in the list.
func (f *editorsForm) has(p string) bool {
	return slices.ContainsFunc(f.list, func(ed settings.ExternalEditor) bool { return ed.Program == p })
}

// keepDraft checks the editor being added or changed and puts it in the
// list; false (and err) when it cannot run.
func (f *editorsForm) keepDraft(m locales.Messages) bool {
	ed := *f.draft
	ed.Name, ed.Program = strings.TrimSpace(ed.Name), strings.TrimSpace(ed.Program)
	ed.Args, ed.Patterns = strings.TrimSpace(ed.Args), strings.TrimSpace(ed.Patterns)
	if err := validateEditor(ed, runtime.GOOS); err != nil {
		f.err = editorErrorText(m, err)
		return false
	}
	if ed.Name == "" {
		ed.Name = editorLabel(ed)
	}
	if f.at >= 0 && f.at < len(f.list) {
		f.list[f.at] = ed
	} else {
		f.list = append(f.list, ed)
	}
	f.draft, f.err = nil, ""
	return true
}

func (a *App) saveEditors(m locales.Messages) {
	f := a.edForm
	if f.draft != nil && strings.TrimSpace(f.draft.Program) != "" && !f.keepDraft(m) {
		return
	}
	s := a.settings
	s.ExternalEditors = slices.Clone(f.list)
	s.Normalize()
	if err := a.ApplySettings(s); err != nil {
		f.err = m.ExternalEditorsSaveFailed()
		return
	}
	a.edFormOpen = false
}

func (a *App) chooseEditorProgram(f *editorsForm, m locales.Messages) {
	pick, title := a.p.PickFile, m.ExternalEditorChooseTitle()
	go func() {
		p, err := pick(title)
		if err != nil || p == "" {
			return
		}
		a.post(func() {
			if f.draft != nil {
				f.draft.Program, f.err = p, ""
			}
		})
	}()
}

// editorsSheet lists the external editors in the order they match, with
// those found on this computer to add, and the fields of one to add or
// change.
func (a *App) editorsSheet(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	f := a.edForm
	if f == nil {
		return
	}
	sheet(ui.Modal(c.Key("editors"), &a.edFormOpen, func() {
		ui.Column(c).Gap(theme.Space(3)).Width(520).Children(func() {
			sheetTitle(c, m.ExternalEditors())
			ui.Text(c, m.ExternalEditorsSheetNote()).FontSize(theme.Footnote).FillWidth().TextColor(tok.TextMuted)
			ui.Text(c, m.ExternalEditorsYours()).FontSize(theme.Footnote).FontWeight(600).TextColor(tok.TextMuted)
			card(c, tok).FillWidth().Padding(theme.Space(1)).Gap(theme.Space(0.5)).Children(func() {
				if len(f.list) == 0 {
					ui.Text(c, m.ExternalEditorsEmpty()).FontSize(theme.Footnote).FillWidth().TextColor(tok.TextMuted).Padding(theme.Space(2))
				}
				for i, ed := range f.list {
					a.editorRow(c, m, tok, f, i, ed)
				}
			})
			if f.draft != nil {
				a.editorDraft(c, m, tok, f)
			} else {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, m.ExternalEditorsDetected()).FontSize(theme.Footnote).FontWeight(600).TextColor(tok.TextMuted)
					ui.Spacer(c)
					if (IconButton{Icon: icons.Refresh, Label: m.ExternalEditorsDetectAgain(), Disabled: f.scanning, Size: Small}).Clicked(c, "editors:rescan") {
						a.scanEditorsFor(f)
					}
				})
				card(c, tok).FillWidth().Padding(theme.Space(1)).Gap(theme.Space(0.5)).Children(func() {
					switch {
					case f.scanning && len(f.found) == 0:
						ui.Row(c).Padding(theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c).Size(14, 14)
							ui.Text(c, m.ExternalEditorsDetecting()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
						})
					case len(f.found) == 0:
						ui.Text(c, m.ExternalEditorsNoneFound()).FontSize(theme.Footnote).FillWidth().TextColor(tok.TextMuted).Padding(theme.Space(2))
					}
					for i, ed := range f.found {
						a.foundEditorRow(c, m, tok, f, i, ed)
					}
				})
				ui.Row(c).Gap(theme.Space(2)).Children(func() {
					if pill(c, m.ExternalEditorAddCustom(), false, false, false) {
						f.draft, f.at, f.err = &settings.ExternalEditor{}, -1, ""
					}
				})
			}
			if f.err != "" {
				ui.Text(c, f.err).FontSize(theme.Footnote).FillWidth().TextColor(tok.Danger)
			}
			ui.Row(c).Gap(theme.Space(2)).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.edFormOpen = false
				}
				if pill(c, m.Save(), true, false, false) {
					a.saveEditors(m)
				}
			})
		})
	}), tok)
	if !a.edFormOpen {
		a.edForm = nil
	}
}

// editorDetail is the line under an editor: its program and the files
// it takes.
func editorDetail(m locales.Messages, ed settings.ExternalEditor) string {
	files := ed.Patterns
	if files == "" {
		files = m.ExternalEditorAllFiles()
	}
	return ed.Program + " · " + files
}

func (a *App) editorRow(c *ui.Context, m locales.Messages, tok theme.Tokens, f *editorsForm, i int, ed settings.ExternalEditor) {
	name := editorLabel(ed)
	ui.Row(c.Key("editor:" + strconv.Itoa(i))).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
		softTile(c, icons.ExternalEdit, tok.TextMuted, 20)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(theme.Space(0.5)).Children(func() {
			ui.Text(c, name).FontSize(theme.Body).FontWeight(600).SingleLine()
			detail := editorDetail(m, ed)
			ui.Text(c, detail).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Tooltip(detail)
		})
		busy := f.draft != nil
		if (IconButton{Icon: icons.MoveUp, Label: m.ExternalEditorMoveUp(name), Size: Small, Disabled: busy || i == 0}).Clicked(c, "up") {
			f.list[i-1], f.list[i] = f.list[i], f.list[i-1]
		}
		if (IconButton{Icon: icons.MoveDown, Label: m.ExternalEditorMoveDown(name), Size: Small, Disabled: busy || i == len(f.list)-1}).Clicked(c, "down") {
			f.list[i+1], f.list[i] = f.list[i], f.list[i+1]
		}
		if (IconButton{Icon: icons.Edit, Label: m.ExternalEditorEdit(name), Size: Small, Disabled: busy}).Clicked(c, "edit") {
			d := ed
			f.draft, f.at, f.err = &d, i, ""
		}
		if (IconButton{Icon: icons.Delete, Label: m.ExternalEditorRemove(name), Size: Small, Danger: true, Disabled: busy}).Clicked(c, "remove") {
			f.list = slices.Delete(slices.Clone(f.list), i, i+1)
		}
	})
}

func (a *App) foundEditorRow(c *ui.Context, m locales.Messages, tok theme.Tokens, f *editorsForm, i int, ed settings.ExternalEditor) {
	added := f.has(ed.Program)
	ui.Row(c.Key("found:" + strconv.Itoa(i))).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
		softTile(c, icons.ExternalEdit, tok.TextMuted, 20)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(theme.Space(0.5)).Children(func() {
			ui.Text(c, ed.Name).FontSize(theme.Body).FontWeight(600).SingleLine()
			ui.Text(c, ed.Program).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Tooltip(ed.Program)
		})
		if added {
			ui.Icon(c, icons.Check.SVG).Size(16, 16).Shrink(0).TextColor(tok.Accent)
			return
		}
		if (IconButton{Icon: icons.Add, Label: m.ExternalEditorAddFound(ed.Name), Size: Small}).Clicked(c, "add") {
			f.list = append(f.list, ed)
		}
	})
}

// editorDraft are the fields of the editor being added or changed.
func (a *App) editorDraft(c *ui.Context, m locales.Messages, tok theme.Tokens, f *editorsForm) {
	d := f.draft
	ui.Field(c, m.ExternalEditorProgram(), func() {
		ui.Row(c).FillWidth().Gap(theme.Space(1.5)).AlignItems(ui.Center).Children(func() {
			in := formTextInput(c.Key("edprogram"), &d.Program).Label(m.ExternalEditorProgram()).Grow(1).AutoFocus()
			f.hold.apply(c, d, in)
			if (IconButton{Icon: icons.OpenFile, Label: m.ExternalEditorChoose(), Disabled: a.p.PickFile == nil}).Clicked(c, "edchoose") {
				a.chooseEditorProgram(f, m)
			}
		})
	})
	ui.Field(c, m.ExternalEditorName(), func() {
		formTextInput(c.Key("edname"), &d.Name).Label(m.ExternalEditorName()).Placeholder(editorLabel(*d)).FillWidth()
	})
	ui.Field(c, m.ExternalEditorArgs(), func() {
		formTextInput(c.Key("edargs"), &d.Args).Label(m.ExternalEditorArgs()).Placeholder("{file}").FillWidth()
	})
	ui.Text(c, m.ExternalEditorArgsNote("{file}")).FontSize(theme.Caption).FillWidth().TextColor(tok.TextMuted)
	ui.Field(c, m.ExternalEditorPatterns(), func() {
		formTextInput(c.Key("edpatterns"), &d.Patterns).Label(m.ExternalEditorPatterns()).Placeholder(m.ExternalEditorPatternsHint()).FillWidth()
	})
	ui.Row(c).Gap(theme.Space(2)).Children(func() {
		if pill(c, m.Cancel(), false, false, false) {
			f.draft, f.err = nil, ""
		}
		label := m.ExternalEditorAddDone()
		if f.at >= 0 {
			label = m.ExternalEditorApply()
		}
		if pill(c, label, false, false, strings.TrimSpace(d.Program) == "") {
			f.keepDraft(m)
		}
	})
}
