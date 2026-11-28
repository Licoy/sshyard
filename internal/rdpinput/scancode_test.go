package rdpinput

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestScancode(t *testing.T) {
	for _, tc := range []struct {
		key      ui.KeyCode
		scan     uint16
		extended bool
	}{
		{ui.CodeKeyA, 0x1e, false}, {ui.CodeKeyQ, 0x10, false}, {ui.CodeDigit0, 0x0b, false},
		{ui.CodeBackslash, 0x2b, false}, {ui.CodeIntlBackslash, 0x56, false}, {ui.CodeIntlYen, 0x7d, false},
		{ui.CodeControlLeft, 0x1d, false}, {ui.CodeControlRight, 0x1d, true},
		{ui.CodeShiftLeft, 0x2a, false}, {ui.CodeShiftRight, 0x36, false},
		{ui.CodeAltLeft, 0x38, false}, {ui.CodeAltRight, 0x38, true},
		{ui.CodeSuperLeft, 0x5b, true}, {ui.CodeSuperRight, 0x5c, true},
		{ui.CodeArrowLeft, 0x4b, true}, {ui.CodeNumpad4, 0x4b, false},
		{ui.CodeArrowUp, 0x48, true}, {ui.CodeNumpad8, 0x48, false},
		{ui.CodeHome, 0x47, true}, {ui.CodeEnd, 0x4f, true}, {ui.CodeDelete, 0x53, true},
		{ui.CodeEnter, 0x1c, false}, {ui.CodeNumpadEnter, 0x1c, true},
		{ui.CodeSlash, 0x35, false}, {ui.CodeNumpadDivide, 0x35, true},
		{ui.CodePause, Pause, false}, {ui.CodePrintScreen, 0x37, true}, {ui.CodeNumLock, 0x45, false},
		{ui.CodeAudioVolumeMute, 0x20, true}, {ui.CodeContextMenu, 0x5d, true},
		{ui.CodeLang1, 0x72, false}, {ui.CodeLang2, 0x71, false},
	} {
		if sc, extended, ok := Scancode(tc.key); !ok || sc != tc.scan || extended != tc.extended {
			t.Errorf("%#x -> (%#x, %t, %t), want (%#x, %t, true)", tc.key, sc, extended, ok, tc.scan, tc.extended)
		}
	}
}

func TestScancodeFunctionKeys(t *testing.T) {
	for _, group := range []struct {
		first ui.KeyCode
		codes []uint16
	}{
		{ui.CodeF1, []uint16{0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40, 0x41, 0x42, 0x43, 0x44, 0x57, 0x58}},
		{ui.CodeF13, []uint16{0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x76}},
	} {
		for i, want := range group.codes {
			code := group.first + ui.KeyCode(i)
			if sc, extended, ok := Scancode(code); !ok || extended || sc != want {
				t.Errorf("%#x -> (%#x, %t, %t), want %#x", code, sc, extended, ok, want)
			}
		}
	}
}

func TestScancodeKnownAndUnknownKeys(t *testing.T) {
	for _, span := range [][2]ui.KeyCode{
		{ui.CodeKeyA, ui.CodeBackslash},
		{ui.CodeSemicolon, ui.CodeF24},
		{ui.CodeAudioVolumeMute, ui.CodeAudioVolumeDown},
		{ui.CodeNumpadComma, ui.CodeNumpadComma},
		{ui.CodeIntlRo, ui.CodeNonConvert},
		{ui.CodeLang1, ui.CodeLang4},
		{ui.CodeControlLeft, ui.CodeSuperRight},
	} {
		for code := span[0]; code <= span[1]; code++ {
			if sc, _, ok := Scancode(code); !ok || sc == 0 {
				t.Errorf("missing code %#x", code)
			}
		}
	}
	for _, code := range []ui.KeyCode{ui.CodeUnknown, 0x32, ui.CodeF24 + 1, ui.CodeSuperRight + 1, 0xffff} {
		if sc, extended, ok := Scancode(code); sc != 0 || extended || ok {
			t.Errorf("unknown %#x -> (%#x, %t, %t)", code, sc, extended, ok)
		}
	}
}
