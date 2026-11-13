package app

import (
	"fmt"
	"runtime"

	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// altIsMeta tells whether Alt with a key sends Esc and the key, as xterm
// does: everywhere but macOS, where Option types accented letters.
var altIsMeta = runtime.GOOS != "darwin"

// xtermMods is xterm's modifier parameter: 1 plus Shift 1, Alt 2, Ctrl 4.
func xtermMods(m ui.Modifiers) int {
	n := 1
	if m&ui.Shift != 0 {
		n++
	}
	if m&ui.Alt != 0 {
		n += 2
	}
	if m&ui.Ctrl != 0 {
		n += 4
	}
	return n
}

// letterOf returns the letter a key types, or 0.
func letterOf(k ui.Key) rune {
	if k >= ui.KeyA && k <= ui.KeyZ {
		return 'a' + rune(k-ui.KeyA)
	}
	return 0
}

// ctrlCodes are the control characters Ctrl types with keys other than
// the letters, as xterm sends them.
var ctrlCodes = map[ui.Key]byte{
	ui.KeySpace: 0, ui.Key2: 0, ui.KeyBracketLeft: 0x1b, ui.Key3: 0x1b,
	ui.KeyBackslash: 0x1c, ui.Key4: 0x1c, ui.KeyBracketRight: 0x1d, ui.Key5: 0x1d,
	ui.Key6: 0x1e, ui.KeyMinus: 0x1f, ui.KeySlash: 0x1f, ui.Key7: 0x1f, ui.Key8: 0x7f,
}

// printable are the keys that type a character, and the character.
var printable = map[ui.Key][2]rune{
	ui.KeyMinus: {'-', '_'}, ui.KeyEqual: {'=', '+'}, ui.KeyComma: {',', '<'}, ui.KeyPeriod: {'.', '>'},
	ui.KeySlash: {'/', '?'}, ui.KeySemicolon: {';', ':'}, ui.KeyQuote: {'\'', '"'},
	ui.KeyBracketLeft: {'[', '{'}, ui.KeyBracketRight: {']', '}'}, ui.KeyBackslash: {'\\', '|'},
	ui.KeyBackquote: {'`', '~'}, ui.KeySpace: {' ', ' '},
	ui.Key0: {'0', ')'}, ui.Key1: {'1', '!'}, ui.Key2: {'2', '@'}, ui.Key3: {'3', '#'}, ui.Key4: {'4', '$'},
	ui.Key5: {'5', '%'}, ui.Key6: {'6', '^'}, ui.Key7: {'7', '&'}, ui.Key8: {'8', '*'}, ui.Key9: {'9', '('},
}

// typesText reports whether a key types a character, which then comes as
// text after it.
func typesText(k ui.Key) bool {
	_, ok := printable[k]
	return ok || letterOf(k) != 0
}

// EncodeKey returns what a key pressed in the terminal sends to the
// program, as xterm sends it (the Linux console's F1–F5, Home and End when
// m.LinuxKeys, without modifiers), and false for keys it leaves: keys that type
// text (the text comes after them, InputText) and the app's own shortcuts.
// Cmd+Left and Cmd+Right (the Windows key elsewhere, when the system lets
// it through) go to the line's start and end, as Home and End.
func EncodeKey(k ui.Key, mods ui.Modifiers, m vt.Modes) ([]byte, bool) {
	if mods == ui.Super {
		switch k {
		case ui.KeyLeft:
			k, mods = ui.KeyHome, 0
		case ui.KeyRight:
			k, mods = ui.KeyEnd, 0
		}
	}
	if mods&ui.Super != 0 {
		return nil, false
	}
	mod := xtermMods(mods)
	alt := mods&ui.Alt != 0 && altIsMeta
	esc := func(b []byte) []byte {
		if alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	}
	// cursor keys: CSI or SS3 letter, CSI 1;m letter with modifiers.
	cursor := func(c byte, app bool) []byte {
		if mod > 1 {
			return []byte(fmt.Sprintf("\x1b[1;%d%c", mod, c))
		}
		if app {
			return []byte{0x1b, 'O', c}
		}
		return []byte{0x1b, '[', c}
	}
	tilde := func(n int) []byte {
		if mod > 1 {
			return []byte(fmt.Sprintf("\x1b[%d;%d~", n, mod))
		}
		return []byte(fmt.Sprintf("\x1b[%d~", n))
	}
	switch k {
	case ui.KeyEnter:
		return esc([]byte{'\r'}), true
	case ui.KeyBackspace:
		if mods&ui.Ctrl != 0 {
			return esc([]byte{0x08}), true
		}
		return esc([]byte{0x7f}), true
	case ui.KeyTab:
		if mods&ui.Shift != 0 {
			return []byte("\x1b[Z"), true
		}
		return esc([]byte{'\t'}), true
	case ui.KeyEscape:
		return esc([]byte{0x1b}), true
	case ui.KeyUp:
		return cursor('A', m.AppCursorKeys), true
	case ui.KeyDown:
		return cursor('B', m.AppCursorKeys), true
	case ui.KeyRight:
		return cursor('C', m.AppCursorKeys), true
	case ui.KeyLeft:
		return cursor('D', m.AppCursorKeys), true
	case ui.KeyHome:
		if m.LinuxKeys && mod == 1 {
			return tilde(1), true
		}
		return cursor('H', m.AppCursorKeys), true
	case ui.KeyEnd:
		if m.LinuxKeys && mod == 1 {
			return tilde(4), true
		}
		return cursor('F', m.AppCursorKeys), true
	case ui.KeyInsert:
		return tilde(2), true
	case ui.KeyDelete:
		return tilde(3), true
	case ui.KeyPageUp:
		return tilde(5), true
	case ui.KeyPageDown:
		return tilde(6), true
	}
	if k >= ui.KeyF1 && k <= ui.KeyF12 {
		n := int(k - ui.KeyF1)
		if m.LinuxKeys && mod == 1 && n < 5 {
			return []byte{0x1b, '[', '[', "ABCDE"[n]}, true
		}
		if n < 4 {
			return cursor("PQRS"[n], true), true
		}
		return tilde([]int{15, 17, 18, 19, 20, 21, 23, 24}[n-4]), true
	}
	if mods&ui.Ctrl != 0 {
		if r := letterOf(k); r != 0 {
			return esc([]byte{byte(r-'a') + 1}), true
		}
		if b, ok := ctrlCodes[k]; ok {
			return esc([]byte{b}), true
		}
		return nil, false
	}
	if alt {
		// Alt as Meta: Esc, then the character.
		r := letterOf(k)
		shifted := mods&ui.Shift != 0
		if r != 0 && shifted {
			r -= 'a' - 'A'
		}
		if p, ok := printable[k]; ok {
			r = p[0]
			if shifted {
				r = p[1]
			}
		}
		if r != 0 {
			return append([]byte{0x1b}, string(r)...), true
		}
	}
	return nil, false
}

// pasteBytes is what pasting text sends: newlines as Enter, wrapped in
// the bracketed-paste markers when the program asked for them.
func pasteBytes(text string, m vt.Modes) []byte {
	b := make([]byte, 0, len(text)+12)
	if m.BracketedPaste {
		b = append(b, "\x1b[200~"...)
	}
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\r' && i+1 < len(text) && text[i+1] == '\n':
		case c == '\n':
			b = append(b, '\r')
		case m.BracketedPaste && c == 0x1b:
			// A pasted Esc could end the paste early.
		default:
			b = append(b, c)
		}
	}
	if m.BracketedPaste {
		b = append(b, "\x1b[201~"...)
	}
	return b
}

// appMods are the modifiers of SSHYard's own shortcuts: Cmd on macOS, and
// Ctrl+Shift elsewhere, since Ctrl alone belongs to the shell.
var appMods = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl | ui.Shift
}()

// saveMods is the modifier of saving in an editor window: Cmd on macOS,
// Ctrl elsewhere.
var saveMods = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}()

// stopMods cancels the AI turn: ⌘. on macOS, Ctrl+. elsewhere.
// Not appMods: on other systems that would be Ctrl+Shift+., which is
// stop-all, and this shortcut is Ctrl+..
var stopMods = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}()

// stopAllMods cancels every AI turn: ⌘⇧. on macOS, Ctrl+Shift+. elsewhere.
// ⌘⇧A / Ctrl+Shift+A is Open AI, so stop-all does not use it.
var stopAllMods = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super | ui.Shift
	}
	return ui.Ctrl | ui.Shift
}()

// aiMods opens an AI pane, the command palette and the tool panes: ⌘⇧A,
// ⌘⇧P, ⌘⇧M and ⌘⇧L on macOS, Ctrl+Shift and the key elsewhere. Not
// appMods: on macOS that is ⌘A, which is Select All in a text field.
var aiMods = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super | ui.Shift
	}
	return ui.Ctrl | ui.Shift
}()
