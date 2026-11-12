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
