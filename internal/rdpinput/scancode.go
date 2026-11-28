// Package rdpinput translates physical MyGo keys into RDP keyboard input.
package rdpinput

import "github.com/egoist/mygo/ui"

// Pause marks the E1-prefixed Pause key. The sender must encode it as
// E1 Ctrl and NumLock down, then E1 Ctrl and NumLock up (MS-RDPBCGR).
const Pause uint16 = 0xe11d

// Scancode returns the Set 1 make code and whether it has the E0 prefix.
// Pause is returned as the Pause sentinel. Unknown keys have ok false.
func Scancode(code ui.KeyCode) (sc uint16, extended, ok bool) {
	if int(code) >= len(scan1) || scan1[code] == 0 {
		return 0, false, false
	}
	sc = scan1[code]
	if sc>>8 == 0xe0 {
		return sc & 0xff, true, true
	}
	return sc, false, true
}

// Scan 1 Make codes by HID key position in Microsoft's Keyboard Input Overview:
// https://learn.microsoft.com/en-us/windows/win32/inputdev/about-keyboard-input#scan-codes
var scan1 = [...]uint16{
	ui.CodeKeyA: 0x1e, ui.CodeKeyB: 0x30, ui.CodeKeyC: 0x2e, ui.CodeKeyD: 0x20,
	ui.CodeKeyE: 0x12, ui.CodeKeyF: 0x21, ui.CodeKeyG: 0x22, ui.CodeKeyH: 0x23,
	ui.CodeKeyI: 0x17, ui.CodeKeyJ: 0x24, ui.CodeKeyK: 0x25, ui.CodeKeyL: 0x26,
	ui.CodeKeyM: 0x32, ui.CodeKeyN: 0x31, ui.CodeKeyO: 0x18, ui.CodeKeyP: 0x19,
	ui.CodeKeyQ: 0x10, ui.CodeKeyR: 0x13, ui.CodeKeyS: 0x1f, ui.CodeKeyT: 0x14,
	ui.CodeKeyU: 0x16, ui.CodeKeyV: 0x2f, ui.CodeKeyW: 0x11, ui.CodeKeyX: 0x2d,
	ui.CodeKeyY: 0x15, ui.CodeKeyZ: 0x2c,
	ui.CodeDigit1: 0x02, ui.CodeDigit2: 0x03, ui.CodeDigit3: 0x04, ui.CodeDigit4: 0x05,
	ui.CodeDigit5: 0x06, ui.CodeDigit6: 0x07, ui.CodeDigit7: 0x08, ui.CodeDigit8: 0x09,
	ui.CodeDigit9: 0x0a, ui.CodeDigit0: 0x0b,
	ui.CodeEnter: 0x1c, ui.CodeEscape: 0x01, ui.CodeBackspace: 0x0e, ui.CodeTab: 0x0f,
	ui.CodeSpace: 0x39, ui.CodeMinus: 0x0c, ui.CodeEqual: 0x0d, ui.CodeBracketLeft: 0x1a,
	ui.CodeBracketRight: 0x1b, ui.CodeBackslash: 0x2b, ui.CodeSemicolon: 0x27, ui.CodeQuote: 0x28,
	ui.CodeBackquote: 0x29, ui.CodeComma: 0x33, ui.CodePeriod: 0x34, ui.CodeSlash: 0x35,
	ui.CodeCapsLock: 0x3a,
	ui.CodeF1:       0x3b, ui.CodeF2: 0x3c, ui.CodeF3: 0x3d, ui.CodeF4: 0x3e,
	ui.CodeF5: 0x3f, ui.CodeF6: 0x40, ui.CodeF7: 0x41, ui.CodeF8: 0x42,
	ui.CodeF9: 0x43, ui.CodeF10: 0x44, ui.CodeF11: 0x57, ui.CodeF12: 0x58,
	ui.CodeF13: 0x64, ui.CodeF14: 0x65, ui.CodeF15: 0x66, ui.CodeF16: 0x67,
	ui.CodeF17: 0x68, ui.CodeF18: 0x69, ui.CodeF19: 0x6a, ui.CodeF20: 0x6b,
	ui.CodeF21: 0x6c, ui.CodeF22: 0x6d, ui.CodeF23: 0x6e, ui.CodeF24: 0x76,
	ui.CodePrintScreen: 0xe037, ui.CodeScrollLock: 0x46, ui.CodePause: Pause,
	ui.CodeInsert: 0xe052, ui.CodeHome: 0xe047, ui.CodePageUp: 0xe049, ui.CodeDelete: 0xe053,
	ui.CodeEnd: 0xe04f, ui.CodePageDown: 0xe051, ui.CodeArrowRight: 0xe04d, ui.CodeArrowLeft: 0xe04b,
	ui.CodeArrowDown: 0xe050, ui.CodeArrowUp: 0xe048, ui.CodeNumLock: 0x45,
	ui.CodeNumpadDivide: 0xe035, ui.CodeNumpadMultiply: 0x37, ui.CodeNumpadSubtract: 0x4a,
	ui.CodeNumpadAdd: 0x4e, ui.CodeNumpadEnter: 0xe01c, ui.CodeNumpad1: 0x4f,
	ui.CodeNumpad2: 0x50, ui.CodeNumpad3: 0x51, ui.CodeNumpad4: 0x4b, ui.CodeNumpad5: 0x4c,
	ui.CodeNumpad6: 0x4d, ui.CodeNumpad7: 0x47, ui.CodeNumpad8: 0x48, ui.CodeNumpad9: 0x49,
	ui.CodeNumpad0: 0x52, ui.CodeNumpadDecimal: 0x53, ui.CodeNumpadEqual: 0x59, ui.CodeNumpadComma: 0x7e,
	ui.CodeContextMenu: 0xe05d, ui.CodePower: 0xe05e,
	ui.CodeAudioVolumeMute: 0xe020, ui.CodeAudioVolumeUp: 0xe030, ui.CodeAudioVolumeDown: 0xe02e,
	ui.CodeIntlBackslash: 0x56, ui.CodeIntlRo: 0x73, ui.CodeKanaMode: 0x70,
	ui.CodeIntlYen: 0x7d, ui.CodeConvert: 0x79, ui.CodeNonConvert: 0x7b,
	ui.CodeLang1: 0x72, ui.CodeLang2: 0x71, ui.CodeLang3: 0x78, ui.CodeLang4: 0x77,
	ui.CodeControlLeft: 0x1d, ui.CodeControlRight: 0xe01d, ui.CodeShiftLeft: 0x2a, ui.CodeShiftRight: 0x36,
	ui.CodeAltLeft: 0x38, ui.CodeAltRight: 0xe038, ui.CodeSuperLeft: 0xe05b, ui.CodeSuperRight: 0xe05c,
}
