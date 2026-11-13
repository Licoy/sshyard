package app

import (
	"testing"

	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

func TestEncodeKeySuperArrowsAreHomeEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  ui.Key
		mods ui.Modifiers
		app  bool
		want string
		ok   bool
	}{
		{"cmd left", ui.KeyLeft, ui.Super, false, "\x1b[H", true},
		{"cmd right", ui.KeyRight, ui.Super, false, "\x1b[F", true},
		{"cmd left app cursor", ui.KeyLeft, ui.Super, true, "\x1bOH", true},
		{"cmd right app cursor", ui.KeyRight, ui.Super, true, "\x1bOF", true},
		{"plain left", ui.KeyLeft, 0, false, "\x1b[D", true},
		{"cmd shift left", ui.KeyLeft, ui.Super | ui.Shift, false, "", false},
		{"cmd up", ui.KeyUp, ui.Super, false, "", false},
	} {
		got, ok := EncodeKey(tc.key, tc.mods, vt.Modes{AppCursorKeys: tc.app})
		if ok != tc.ok || string(got) != tc.want {
			t.Errorf("%s: EncodeKey = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEncodeKeyLinuxKeys(t *testing.T) {
	linux := vt.Modes{LinuxKeys: true}
	for _, tc := range []struct {
		name string
		key  ui.Key
		mods ui.Modifiers
		m    vt.Modes
		want string
	}{
		{"F1", ui.KeyF1, 0, linux, "\x1b[[A"},
		{"F5", ui.KeyF5, 0, linux, "\x1b[[E"},
		{"F6 as xterm", ui.KeyF6, 0, linux, "\x1b[17~"},
		{"Home", ui.KeyHome, 0, linux, "\x1b[1~"},
		{"End", ui.KeyEnd, 0, linux, "\x1b[4~"},
		{"Home with app cursor keys", ui.KeyHome, 0, vt.Modes{LinuxKeys: true, AppCursorKeys: true}, "\x1b[1~"},
		{"cmd left is Home", ui.KeyLeft, ui.Super, linux, "\x1b[1~"},
		{"up as xterm", ui.KeyUp, 0, linux, "\x1b[A"},
		{"ctrl Home as xterm", ui.KeyHome, ui.Ctrl, linux, "\x1b[1;5H"},
		{"shift F1 as xterm", ui.KeyF1, ui.Shift, linux, "\x1b[1;2P"},
		{"xterm F1", ui.KeyF1, 0, vt.Modes{}, "\x1bOP"},
		{"xterm F5", ui.KeyF5, 0, vt.Modes{}, "\x1b[15~"},
		{"xterm Home", ui.KeyHome, 0, vt.Modes{}, "\x1b[H"},
	} {
		if got, ok := EncodeKey(tc.key, tc.mods, tc.m); !ok || string(got) != tc.want {
			t.Errorf("%s: EncodeKey = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
}
