package app

import (
	"maps"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keymap"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The actions a shortcut runs, by the ID the settings keep their
// shortcut under (settings.Shortcuts).
const (
	actQuickConnect   = "quickConnect"
	actCommandPalette = "commandPalette"
	actNewTab         = "newTab"
	actClosePane      = "closePane"
	actSplitRight     = "splitRight"
	actSplitDown      = "splitDown"
	actOpenFiles      = "openFiles"
	actFind           = "find"
	actOpenAI         = "openAI"
	actStopAI         = "stopAI"
	actStopAllAI      = "stopAllAI"
	actSaveFile       = "saveFile"
	actZoomIn         = "zoomIn"
	actZoomOut        = "zoomOut"
	actZoomReset      = "zoomReset"
	actSendBreak      = "sendBreak"
	actBroadcast      = "broadcast"
	actSessionLogs    = "sessionLogs"
	actHistory        = "commandHistory"
	// actTab is the prefix of the actions that show the tab of their
	// number, 1 to tabJumps; the last shows the last tab.
	actTab = "selectTab"
)

// tabJumps is how many tabs have a shortcut of their own: the first
// eight by their number, and the last, by the ninth.
const tabJumps = 9

// tabActions are the actions showing a tab by its number.
func tabActions() []action {
	out := make([]action, 0, tabJumps)
	for n := 1; n <= tabJumps; n++ {
		label := func(m locales.Messages) string { return m.GoToTab(strconv.Itoa(n)) }
		if n == tabJumps {
			label = locales.Messages.GoToLastTab
		}
		out = append(out, action{actTab + strconv.Itoa(n), label, icons.OpenInTab, keymap.Combo{Mods: appMods, Key: ui.Key0 + ui.Key(n)}, false})
	}
	return out
}

// tabOfNumber is the place among count tabs of the one the shortcut of
// number n shows: the n-th, the last for tabJumps; -1 for none.
func tabOfNumber(n, count int) int {
	switch {
	case n < 1 || n > tabJumps || count == 0:
		return -1
	case n == tabJumps:
		return count - 1
	case n > count:
		return -1
	}
	return n - 1
}

// tabNumber is the number of the tab action id shows, 0 for another.
func tabNumber(id string) int {
	s, ok := strings.CutPrefix(id, actTab)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > tabJumps {
		return 0
	}
	return n
}

// selectTabNumber shows the tab of number n (tabOfNumber), if there is one.
func (a *App) selectTabNumber(n int) {
	i := tabOfNumber(n, len(a.ws.Tabs))
	if i < 0 {
		return
	}
	a.ws.Active = i
	a.page = pageWorkspace
	a.focusReq = a.ws.Tabs[i].Focused().ID
}

// action is an action with a shortcut: what it is called, its icon in the
// command palette, and the shortcut it has until one is recorded.
type action struct {
	id    string
	label func(locales.Messages) string
	icon  *icons.Icon
	def   keymap.Combo
	// editor is an action of the editor's window, where Ctrl alone is
	// free, rather than of the main window, where it is the shell's.
	editor bool
}

// actions are the actions with a shortcut, in the order the settings
// list them.
var actions = append([]action{
	{actCommandPalette, locales.Messages.CommandPalette, icons.Command, keymap.Combo{Mods: aiMods, Key: ui.KeyP}, false},
	{actQuickConnect, locales.Messages.QuickConnect, icons.Quick, keymap.Combo{Mods: appMods, Key: ui.KeyK}, false},
	{actNewTab, locales.Messages.NewTab, icons.Add, keymap.Combo{Mods: appMods, Key: ui.KeyT}, false},
	{actClosePane, locales.Messages.ClosePane, icons.Close, keymap.Combo{Mods: appMods, Key: ui.KeyW}, false},
	{actSplitRight, locales.Messages.SplitRight, icons.SplitRight, keymap.Combo{Mods: appMods, Key: ui.KeyD}, false},
	{actSplitDown, locales.Messages.SplitDown, icons.SplitDown, keymap.Combo{Mods: appMods, Key: ui.KeyE}, false},
	{actOpenFiles, locales.Messages.OpenFilesSplit, icons.Folder, keymap.Combo{Mods: appMods, Key: ui.KeyO}, false},
	{actFind, locales.Messages.Find, icons.Search, keymap.Combo{Mods: appMods, Key: ui.KeyF}, false},
	{actOpenAI, locales.Messages.OpenAIRight, icons.Robot, keymap.Combo{Mods: aiMods, Key: ui.KeyA}, false},
	{actStopAI, locales.Messages.ShortcutStopAI, icons.Stop, keymap.Combo{Mods: stopMods, Key: ui.KeyPeriod}, false},
	{actStopAllAI, locales.Messages.StopAllAI, icons.Stop, keymap.Combo{Mods: stopAllMods, Key: ui.KeyPeriod}, false},
	{actSaveFile, locales.Messages.ShortcutSaveFile, icons.Save, keymap.Combo{Mods: saveMods, Key: ui.KeyS}, true},
	{actZoomIn, locales.Messages.ZoomIn, icons.ZoomIn, keymap.Combo{Mods: appMods, Key: ui.KeyEqual}, false},
	{actZoomOut, locales.Messages.ZoomOut, icons.ZoomOut, keymap.Combo{Mods: appMods, Key: ui.KeyMinus}, false},
	{actZoomReset, locales.Messages.ZoomReset, icons.Reset, keymap.Combo{Mods: appMods, Key: ui.Key0}, false},
	{actSendBreak, locales.Messages.SendBreak, icons.SendBreak, keymap.Combo{Mods: appMods, Key: ui.KeyB}, false},
	{actBroadcast, locales.Messages.Broadcast, icons.Broadcast, keymap.Combo{Mods: aiMods, Key: ui.KeyM}, false},
	{actSessionLogs, locales.Messages.SessionLogs, icons.FileText, keymap.Combo{Mods: aiMods, Key: ui.KeyL}, false},
	{actHistory, locales.Messages.CommandHistory, icons.History, keymap.Combo{Mods: aiMods, Key: ui.KeyH}, false},
}, tabActions()...)

// actionOf is the action with id.
func actionOf(id string) (action, bool) {
	i := slices.IndexFunc(actions, func(ac action) bool { return ac.id == id })
	if i < 0 {
		return action{}, false
	}
	return actions[i], true
}

// shortcutOf is the shortcut of action id with the shortcuts recorded in
// custom: its own, unless it has none or one it may not have.
func shortcutOf(custom map[string]string, id string) keymap.Combo {
	ac, _ := actionOf(id)
	if s, ok := custom[id]; ok {
		if k, ok := keymap.Parse(s); ok && shortcutRule(runtime.GOOS, ac, k) == ruleOK {
			return k
		}
	}
	return ac.def
}

// keyOf is the shortcut of action id.
func (a *App) keyOf(id string) keymap.Combo { return shortcutOf(a.settings.Shortcuts, id) }

// bound is menu entry e with the shortcut of action id beside it.
func (a *App) bound(id string, e menuEntry) menuEntry {
	k := a.keyOf(id)
	e.Mods, e.Key = k.Mods, k.Key
	return e
}

// isAppShortcut reports a key the terminal leaves to the app, with the
// shortcuts recorded in custom: the main window's actions. Saving is the
// editor's, so Ctrl+S still reaches the shell.
func isAppShortcut(custom map[string]string, k ui.Key, mods ui.Modifiers) bool {
	for _, ac := range actions {
		if !ac.editor && shortcutOf(custom, ac.id) == (keymap.Combo{Mods: mods, Key: k}) {
			return true
		}
	}
	return false
}

// rule is whether a shortcut may be an action's, or why not.
type rule int

const (
	ruleOK       rule = iota
	ruleKey           // the key is not one a shortcut may use
	ruleMods          // without the modifiers the window's shortcuts need
	ruleReserved      // the system's, or editing text's
)

// shortcutMods are the modifiers a shortcut of the main window must hold
// on goos, so the keys without them reach the shell, and those of the
// editor's.
func shortcutMods(goos string, editor bool) ui.Modifiers {
	switch {
	case goos == "darwin":
		return ui.Super
	case editor:
		return ui.Ctrl
	}
	return ui.Ctrl | ui.Shift
}

// reserved are the shortcuts no action may take on goos: editing text
// (Select All, Copy, Paste, Cut, Undo, Redo), the system's and the menu
// bar's (Close Window), and the terminal's copy and paste; for the editor,
// also closing its tab.
func reserved(goos string, editor bool) []keymap.Combo {
	k := func(m ui.Modifiers, key ui.Key) keymap.Combo { return keymap.Combo{Mods: m, Key: key} }
	if goos == "darwin" {
		out := []keymap.Combo{k(ui.Super, ui.KeyA), k(ui.Super, ui.KeyC), k(ui.Super, ui.KeyV), k(ui.Super, ui.KeyX),
			k(ui.Super, ui.KeyZ), k(ui.Super|ui.Shift, ui.KeyZ), k(ui.Super, ui.KeyQ), k(ui.Super, ui.KeyH),
			k(ui.Super, ui.KeyM), k(ui.Super, ui.KeyBackquote), k(ui.Super|ui.Shift, ui.KeyW)}
		if editor {
			out = append(out, k(ui.Super, ui.KeyW))
		}
		return out
	}
	cs := ui.Ctrl | ui.Shift
	out := []keymap.Combo{k(cs, ui.KeyC), k(cs, ui.KeyV), k(cs, ui.KeyZ), k(cs, ui.KeyI)}
	if editor {
		out = append(out, k(ui.Ctrl, ui.KeyA), k(ui.Ctrl, ui.KeyC), k(ui.Ctrl, ui.KeyV), k(ui.Ctrl, ui.KeyX),
			k(ui.Ctrl, ui.KeyZ), k(ui.Ctrl, ui.KeyY), k(ui.Ctrl, ui.KeyW))
	}
	return out
}

// shortcutRule says whether k may be the shortcut of ac on goos. Its own
// default always may.
func shortcutRule(goos string, ac action, k keymap.Combo) rule {
	switch {
	case k == ac.def:
		return ruleOK
	case !keymap.Bindable(k.Key):
		return ruleKey
	case k.Mods&shortcutMods(goos, ac.editor) != shortcutMods(goos, ac.editor):
		return ruleMods
	case slices.Contains(reserved(goos, ac.editor), k):
		return ruleReserved
	}
	return ruleOK
}

// shortcutProblem is why k cannot be the shortcut of action id, or "".
func (a *App) shortcutProblem(m locales.Messages, id string, k keymap.Combo) string {
	ac, _ := actionOf(id)
	switch shortcutRule(runtime.GOOS, ac, k) {
	case ruleKey:
		return m.ShortcutKeyInvalid()
	case ruleMods:
		return m.ShortcutNeedsMods(shortcutText(runtime.GOOS, shortcutMods(runtime.GOOS, ac.editor), 0))
	case ruleReserved:
		return m.ShortcutReserved(shortcutText(runtime.GOOS, k.Mods, k.Key))
	}
	for _, other := range actions {
		if other.id != id && a.keyOf(other.id) == k {
			return m.ShortcutTaken(shortcutText(runtime.GOOS, k.Mods, k.Key), other.label(m))
		}
	}
	return ""
}

// setShortcut records k as the shortcut of action id; its default
// forgets the one recorded.
func (a *App) setShortcut(id string, k keymap.Combo) error {
	s := a.settings
	s.Shortcuts = maps.Clone(s.Shortcuts)
	if ac, _ := actionOf(id); k == ac.def {
		delete(s.Shortcuts, id)
	} else {
		if s.Shortcuts == nil {
			s.Shortcuts = map[string]string{}
		}
		s.Shortcuts[id] = k.String()
	}
	return a.ApplySettings(s)
}

// shortcuts runs the main window's actions by their shortcuts. None
// takes a key while one is being recorded.
func (a *App) shortcuts(c *ui.Context) {
	if a.recording != "" {
		if a.page == pageSettings && a.prefNow() == prefShortcuts {
			return
		}
		a.recording, a.recordErr = "", ""
	}
	for _, ac := range actions {
		if ac.editor {
			continue
		}
		if k := a.keyOf(ac.id); c.Shortcut(k.Mods, k.Key) {
			a.runAction(ac.id)
		}
	}
}

// runAction runs the main window's action id, by its shortcut or from
// the command palette.
func (a *App) runAction(id string) {
	switch id {
	case actCommandPalette:
		a.openPalette()
	case actQuickConnect:
		a.OpenQuickConnect()
	case actFind:
		if a.page == pageSettings {
			a.prefSearch.focus = true
		} else if cn := a.focused(); a.page == pageWorkspace && cn != nil && cn.kind == layout.Terminal {
			a.finding = true
		}
	case actNewTab:
		if cn := a.focused(); cn != nil {
			a.Connect(cn.host.ID, openAt{})
		} else if id := a.selectedHost(); id != "" {
			a.Connect(id, openAt{})
		}
	case actClosePane:
		if a.page == pageWorkspace {
			a.CloseFocused()
		}
	case actSplitRight:
		a.SplitFocused(layout.Right)
	case actSplitDown:
		a.SplitFocused(layout.Bottom)
	case actOpenFiles:
		a.ToggleFiles()
	case actOpenAI:
		if a.page == pageWorkspace {
			a.ToggleAI()
		}
	case actStopAI:
		a.stopShortcut()
	case actStopAllAI:
		a.StopAllAI()
	case actZoomIn:
		a.stepZoom(1)
	case actZoomOut:
		a.stepZoom(-1)
	case actZoomReset:
		a.stepZoom(0)
	case actSendBreak:
		if cn := a.focused(); a.page == pageWorkspace && cn != nil {
			a.sendBreak(cn)
		}
	case actBroadcast:
		a.ToggleTool(layout.Broadcast)
	case actSessionLogs:
		a.ToggleTool(layout.Logs)
	case actHistory:
		a.ToggleTool(layout.History)
	default:
		if n := tabNumber(id); n > 0 {
			a.selectTabNumber(n)
		}
	}
}
