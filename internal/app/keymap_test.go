package app

import (
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/keymap"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func TestShortcutRule(t *testing.T) {
	quick, _ := actionOf(actQuickConnect)
	save, _ := actionOf(actSaveFile)
	stop, _ := actionOf(actStopAI)
	tab1, _ := actionOf(actTab + "1")
	k := func(m ui.Modifiers, key ui.Key) keymap.Combo { return keymap.Combo{Mods: m, Key: key} }
	for _, tc := range []struct {
		goos string
		ac   action
		k    keymap.Combo
		want rule
	}{
		{"darwin", quick, k(ui.Super|ui.Alt, ui.KeyJ), ruleOK},
		{"darwin", quick, k(ui.Super, ui.KeyW), ruleOK},
		{"darwin", quick, k(ui.Ctrl, ui.KeyJ), ruleMods},
		{"darwin", quick, k(ui.Super, ui.KeyC), ruleReserved},
		{"darwin", quick, k(ui.Super|ui.Shift, ui.KeyZ), ruleReserved},
		{"darwin", quick, k(ui.Super, ui.KeyEnter), ruleKey},
		{"darwin", save, k(ui.Super, ui.KeyW), ruleReserved},
		// The menu bar's Close Window, which takes it before the window.
		{"darwin", quick, k(ui.Super|ui.Shift, ui.KeyW), ruleReserved},
		{"linux", quick, k(ui.Ctrl, ui.KeyJ), ruleMods},
		{"linux", quick, k(ui.Ctrl|ui.Shift, ui.KeyF5), ruleOK},
		{"linux", quick, k(ui.Ctrl|ui.Shift, ui.KeyC), ruleReserved},
		{"windows", quick, k(ui.Ctrl|ui.Shift, ui.KeyI), ruleReserved},
		{"windows", save, k(ui.Ctrl, ui.KeyJ), ruleOK},
		{"windows", save, k(ui.Ctrl, ui.KeyV), ruleReserved},
		// Its own default always may, as Ctrl+. stops the AI elsewhere.
		{runtime.GOOS, stop, stop.def, ruleOK},
		// A tab's may be Alt and a digit alone, as other terminals have it.
		{"darwin", tab1, k(ui.Alt, ui.Key1), ruleOK},
		{"linux", tab1, k(ui.Alt, ui.Key1), ruleOK},
		{"windows", tab1, k(ui.Alt, ui.Key9), ruleOK},
		{"windows", tab1, k(ui.Alt|ui.Shift, ui.Key1), ruleMods},
		{"windows", tab1, k(ui.Alt, ui.KeyA), ruleMods},
		{"darwin", tab1, k(ui.Ctrl, ui.Key1), ruleMods},
		{"linux", quick, k(ui.Alt, ui.Key1), ruleMods},
		{"darwin", quick, k(ui.Alt, ui.Key1), ruleMods},
	} {
		if got := shortcutRule(tc.goos, tc.ac, tc.k); got != tc.want {
			t.Errorf("%s %s %s: %v, want %v", tc.goos, tc.ac.id, tc.k, got, tc.want)
		}
	}
}

// TestRecordedAppShortcut leaves a recorded shortcut to the app, not the
// one it replaced, and ignores one that may not be.
func TestRecordedAppShortcut(t *testing.T) {
	rec := keymap.Combo{Mods: shortcutMods(runtime.GOOS, false) | ui.Alt, Key: ui.KeyJ}
	custom := map[string]string{actQuickConnect: rec.String()}
	if !isAppShortcut(custom, rec.Key, rec.Mods) {
		t.Error("the recorded shortcut must reach the app")
	}
	if isAppShortcut(custom, ui.KeyK, appMods) {
		t.Error("the replaced shortcut must reach the shell")
	}
	if !isAppShortcut(nil, ui.KeyK, appMods) || !isAppShortcut(nil, ui.KeyP, aiMods) {
		t.Error("the defaults must reach the app")
	}
	if isAppShortcut(nil, ui.KeyS, saveMods) {
		t.Error("saving is the editor's: the terminal keeps its key")
	}
	bad := map[string]string{actQuickConnect: "ctrl+j"}
	if shortcutOf(bad, actQuickConnect) != (keymap.Combo{Mods: appMods, Key: ui.KeyK}) || isAppShortcut(bad, ui.KeyJ, ui.Ctrl) {
		t.Error("a shortcut without the app's modifiers must fall back to the default")
	}
}

// TestAltDigitTab leaves a tab's recorded Alt and digit to the app, and
// drops the text the key types after it (⌥1 is ¡ on macOS), while the
// shell keeps Alt and the other digits.
func TestAltDigitTab(t *testing.T) {
	custom := map[string]string{actTab + "1": "alt+1"}
	if shortcutOf(custom, actTab+"1") != (keymap.Combo{Mods: ui.Alt, Key: ui.Key1}) || !isAppShortcut(custom, ui.Key1, ui.Alt) {
		t.Fatal("the recorded Alt+1 must switch the tab")
	}
	if isAppShortcut(custom, ui.Key2, ui.Alt) {
		t.Error("an unrecorded Alt+2 must reach the shell")
	}
	if bad := map[string]string{actQuickConnect: "alt+1"}; isAppShortcut(bad, ui.Key1, ui.Alt) {
		t.Error("only a tab's shortcut may be Alt and a digit alone")
	}

	var sent []byte
	tv := newTermView(recordTarget{term: vt.New(20, 4), out: &sent}, 13)
	tv.shortcuts = custom
	down := ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.Key1, Mods: ui.Alt}
	// The window's shortcut takes the key before the terminal gets it;
	// the terminal sees it go by.
	tv.appKey(down)
	tv.input(ui.InputEvent{Kind: ui.InputText, Text: "¡"}, nil)
	tv.input(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.Key1, Mods: ui.Alt}, nil)
	// Delivered to the terminal, it gives the key back to the app.
	if tv.input(down, nil) {
		t.Error("the terminal must leave the shortcut to the app")
	}
	tv.input(ui.InputEvent{Kind: ui.InputText, Text: "¡"}, nil)
	tv.input(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.Key1, Mods: ui.Alt}, nil)
	if len(sent) != 0 {
		t.Errorf("sent %q, want nothing", sent)
	}
	// Where no text follows, the key's release ends it: the next text
	// is typed.
	tv.appKey(down)
	tv.input(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.Key1, Mods: ui.Alt}, nil)
	tv.input(ui.InputEvent{Kind: ui.InputText, Text: "x"}, nil)
	if string(sent) != "x" {
		t.Errorf("sent %q, want %q", sent, "x")
	}
}

func TestRecordShortcut(t *testing.T) {
	a, tt := newTestApp(t)
	tt.SetSize(1200, 1400)
	m := locales.In("en")
	a.openSettings(prefShortcuts)
	tt.Frame()
	mustClick(t, tt, m.ShortcutRecord(m.QuickConnect()))
	tt.Frame()
	if a.recording != actQuickConnect || !tt.HasText(m.ShortcutPressKeys()) || !tt.HasText(m.ShortcutRecordHint()) {
		t.Fatalf("recording %q; texts %q", a.recording, tt.Texts())
	}

	// Taken by another action, or without the modifiers: refused, and
	// still recording.
	tt.Key(appMods, ui.KeyT)
	tt.Frame()
	if want := m.ShortcutTaken(shortcutText(runtime.GOOS, appMods, ui.KeyT), m.NewTab()); !tt.HasText(want) {
		t.Errorf("no %q in %q", want, tt.Texts())
	}
	tt.Key(0, ui.KeyJ)
	tt.Frame()
	if want := m.ShortcutNeedsMods(shortcutText(runtime.GOOS, shortcutMods(runtime.GOOS, false), 0)); !tt.HasText(want) {
		t.Errorf("no %q in %q", want, tt.Texts())
	}
	if a.recording != actQuickConnect || a.quicking || len(a.settings.Shortcuts) != 0 {
		t.Fatalf("recording %q, quick connect %v, shortcuts %v", a.recording, a.quicking, a.settings.Shortcuts)
	}

	rec := keymap.Combo{Mods: shortcutMods(runtime.GOOS, false) | ui.Alt, Key: ui.KeyJ}
	tt.Key(rec.Mods, rec.Key)
	tt.Frame()
	if a.recording != "" || a.settings.Shortcuts[actQuickConnect] != rec.String() {
		t.Fatalf("recording %q, shortcuts %v", a.recording, a.settings.Shortcuts)
	}
	for _, k := range keyCaps(runtime.GOOS, rec.Mods, rec.Key) {
		if !hasExact(tt, k) {
			t.Errorf("no key %q", k)
		}
	}
	if !tt.HasText(m.ShortcutReset(m.QuickConnect())) || !tt.HasText(m.ShortcutsResetAll()) {
		t.Errorf("no reset in %q", tt.Texts())
	}

	// The new shortcut opens quick connect, the old one no longer does.
	a.closeSettings()
	tt.Frame()
	tt.Key(appMods, ui.KeyK)
	tt.Frame()
	if a.quicking {
		t.Fatal("the replaced shortcut still opens quick connect")
	}
	tt.Key(rec.Mods, rec.Key)
	tt.Frame()
	if !a.quicking {
		t.Fatal("the recorded shortcut does not open quick connect")
	}
	a.quicking = false
	tt.Frame()

	// Esc gives up recording; Reset goes back to the default.
	a.openSettings(prefShortcuts)
	tt.Frame()
	mustClick(t, tt, m.ShortcutRecord(m.Find()))
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.recording != "" || len(a.settings.Shortcuts) != 1 {
		t.Fatalf("after Esc: recording %q, shortcuts %v", a.recording, a.settings.Shortcuts)
	}
	mustClick(t, tt, m.ShortcutReset(m.QuickConnect()))
	tt.Frame()
	if a.settings.Shortcuts != nil || a.keyOf(actQuickConnect) != (keymap.Combo{Mods: appMods, Key: ui.KeyK}) {
		t.Fatalf("after reset: %v", a.settings.Shortcuts)
	}
	if tt.HasText(m.ShortcutsResetAll()) {
		t.Error("restore all shows without a recorded shortcut")
	}
}

// TestRecordShortcutWaitsForKey: a modifier or Caps Lock pressed on its
// own, as before the key of a shortcut, neither records nor refuses
// anything; the key pressed with it then records the shortcut.
func TestRecordShortcutWaitsForKey(t *testing.T) {
	a, _ := newTestApp(t)
	m := locales.In("en")
	mods := shortcutMods(runtime.GOOS, false)
	a.recording = actQuickConnect
	for _, code := range []ui.KeyCode{ui.CodeSuperLeft, ui.CodeShiftLeft, ui.CodeCapsLock} {
		a.recordKey(m, ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyUnknown, Code: code, Mods: mods})
	}
	if a.recording != actQuickConnect || a.recordErr != "" || len(a.settings.Shortcuts) != 0 {
		t.Fatalf("after modifiers alone: recording %q, error %q, shortcuts %v", a.recording, a.recordErr, a.settings.Shortcuts)
	}
	rec := keymap.Combo{Mods: mods | ui.Alt, Key: ui.KeyJ}
	a.recordKey(m, ui.InputEvent{Kind: ui.InputKeyDown, Key: rec.Key, PhysicalKey: rec.Key, Mods: rec.Mods})
	if a.recording != "" || a.settings.Shortcuts[actQuickConnect] != rec.String() {
		t.Fatalf("recording %q, shortcuts %v", a.recording, a.settings.Shortcuts)
	}
}

// TestEditorRecordedSave saves the editor's file with the shortcut
// recorded for it, and no longer with the default.
func TestEditorRecordedSave(t *testing.T) {
	a, tt, fc, root := filesPane(t)
	wins := fakeWindows(a)
	rec := keymap.Combo{Mods: saveMods | ui.Alt, Key: ui.KeyJ}
	s := a.settings
	s.Shortcuts = map[string]string{actSaveFile: rec.String()}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "notes.txt")
	write(t, file, "one\n")
	a.refreshSides(fc)
	waitFor(t, tt, "listing", func() bool { return tt.HasText("notes.txt") })
	editorOn(t, a, tt, "notes.txt")
	_, et, ec := openedWindow(t, a, tt, wins, 1)
	ed := shown(ec)
	waitFor(t, et, "editor connected", func() bool { return ec.fs.remote.client != nil })

	ed.st.Buffer.Set("two\n")
	et.Frame()
	et.Key(saveMods, ui.KeyS)
	et.Frame()
	if ed.saving || read(file) != "one\n" {
		t.Fatal("the replaced shortcut still saves")
	}
	et.Key(rec.Mods, rec.Key)
	waitFor(t, et, "saved", func() bool { return read(file) == "two\n" && !ed.saving })
}

// TestDefaultShortcutsDistinct gives each action of the main window a
// default shortcut of its own, one it may have.
func TestDefaultShortcutsDistinct(t *testing.T) {
	seen := map[keymap.Combo]string{}
	for _, ac := range actions {
		if ac.editor {
			continue
		}
		if other, ok := seen[ac.def]; ok {
			t.Errorf("%s and %s share %s", ac.id, other, ac.def)
		}
		seen[ac.def] = ac.id
		if slices.Contains(reserved(runtime.GOOS, false), ac.def) {
			t.Errorf("%s defaults to the reserved %s", ac.id, ac.def)
		}
	}
}

func TestToolPaneShortcuts(t *testing.T) {
	a, _ := newTestApp(t)
	for _, tc := range []struct {
		id   string
		kind layout.Kind
	}{{actBroadcast, layout.Broadcast}, {actSessionLogs, layout.Logs}, {actHistory, layout.History}} {
		a.runAction(tc.id)
		c := a.toolPane(tc.kind)
		if c == nil || !a.toolHere(tc.kind) {
			t.Fatalf("%s opened no pane", tc.id)
		}
		if a.focused() != nil {
			t.Fatalf("%s: a tool pane is the focused session", tc.id)
		}
		a.runAction(tc.id)
		if a.toolPane(tc.kind) != nil || len(a.ws.Tabs) != 0 {
			t.Fatalf("%s did not close its pane", tc.id)
		}
	}
}
