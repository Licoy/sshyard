package app

import (
	"runtime"
	"testing"

	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// formatClipboard is a clipboard that lists its formats, as ui.Services
// does.
type formatClipboard struct {
	text    string
	formats []transfer.Format
}

func (f formatClipboard) ReadClipboard() string               { return f.text }
func (f formatClipboard) WriteClipboard(string)               {}
func (f formatClipboard) ClipboardFormats() []transfer.Format { return f.formats }

// The window's clipboard lists its formats, as clipboardHasImage asks.
var _ interface{ ClipboardFormats() []transfer.Format } = ui.Services{}

// TestPasteClipboardImage: pasting text sends it as pasted; a clipboard
// with an image and no text sends Ctrl+V to a local terminal alone, for
// its program to read the image itself, and nothing to a remote one or
// to the terminals typed into with it.
func TestPasteClipboardImage(t *testing.T) {
	img := []transfer.Format{transfer.PNG}
	cases := []struct {
		name  string
		local bool
		cb    clipboard
		want  string
	}{
		{"local text", true, formatClipboard{text: "ls", formats: []transfer.Format{transfer.Text}}, "ls"},
		{"local text and image", true, formatClipboard{text: "ls", formats: append([]transfer.Format{transfer.Text}, img...)}, "ls"},
		{"local image", true, formatClipboard{formats: img}, "\x16"},
		{"remote image", false, formatClipboard{formats: img}, ""},
		{"remote text", false, formatClipboard{text: "ls", formats: []transfer.Format{transfer.Text}}, "ls"},
		{"local empty", true, formatClipboard{}, ""},
		{"local without formats", true, fakeClipboard{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out []byte
			tv := newTermView(recordTarget{term: vt.New(20, 4), out: &out}, 13)
			tv.local = tc.local
			var fanned []byte
			tv.fanout = func(encode func(vt.Modes) []byte) { fanned = append(fanned, encode(vt.Modes{})...) }
			if !tv.input(ui.InputEvent{Kind: ui.InputCommand, Text: "paste"}, tc.cb) {
				t.Fatal("paste not taken")
			}
			if string(out) != tc.want {
				t.Errorf("sent %q, want %q", out, tc.want)
			}
			if tc.want == "\x16" && len(fanned) != 0 {
				t.Errorf("Ctrl+V went to the other terminals too: %q", fanned)
			}
		})
	}
}

func TestWindowsTerminalClipboardShortcuts(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows terminal shortcuts")
	}
	const selected = "remote output"
	const copied = "外部文本"
	for _, tc := range []struct {
		name          string
		mods          ui.Modifiers
		key           ui.Key
		selection     bool
		clipboard     string
		wantClipboard string
		wantInput     string
	}{
		{"paste text", ui.Ctrl, ui.KeyV, false, copied, copied, "\x1b[200~" + copied + "\x1b[201~"},
		{"paste empty clipboard", ui.Ctrl, ui.KeyV, false, "", "", ""},
		{"copy selection", ui.Ctrl, ui.KeyC, true, copied, selected, ""},
		{"interrupt without a selection", ui.Ctrl, ui.KeyC, false, copied, copied, "\x03"},
		{"shifted paste", ui.Ctrl | ui.Shift, ui.KeyV, false, copied, copied, "\x1b[200~" + copied + "\x1b[201~"},
		{"shifted copy without a selection", ui.Ctrl | ui.Shift, ui.KeyC, false, copied, copied, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			term := vt.New(20, 4)
			term.Write([]byte("\x1b[?2004h" + selected))
			tv := newTermView(recordTarget{term: term, out: &sent}, 13)
			tv.selected, tv.selB = tc.selection, vt.Pos{Col: len(selected) - 1}
			cb := &keptClipboard{text: tc.clipboard}
			if !tv.input(ui.InputEvent{Kind: ui.InputKeyDown, Key: tc.key, Mods: tc.mods}, cb) {
				t.Fatal("terminal did not handle the shortcut")
			}
			if cb.text != tc.wantClipboard || string(sent) != tc.wantInput {
				t.Errorf("clipboard %q, sent %q; want clipboard %q, sent %q", cb.text, sent, tc.wantClipboard, tc.wantInput)
			}
		})
	}
}
