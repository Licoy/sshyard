package rdp

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func encodeClipboardText(text string) (wide, ansi []byte, err error) {
	size, err := clipboardTextSize(text)
	if err != nil {
		return nil, nil, err
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	wide = make([]byte, 0, size)
	le := binary.LittleEndian
	var units [2]uint16
	for _, r := range text {
		for _, unit := range utf16.AppendRune(units[:0], r) {
			wide = le.AppendUint16(wide, unit)
		}
	}
	wide = append(wide, 0, 0)

	// CF_TEXT has no encoding field. Use Windows-1252 for legacy peers and
	// announce it only when the entire text can be represented losslessly.
	if legacy, encErr := charmap.Windows1252.NewEncoder().String(text); encErr == nil {
		ansi = append([]byte(legacy), 0)
	}
	return wide, ansi, nil
}

// clipboardTextSize validates local text and counts UTF-16LE wire bytes after
// the conversion to CRLF used by Windows clipboard formats.
func clipboardTextSize(text string) (int, error) {
	if len(text) > clipMaxData || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return 0, &clipboardInputError{"RDP clipboard text is invalid or too large"}
	}
	size := 2 // UTF-16LE terminator.
	var previous rune
	for _, r := range text {
		size += 2
		if r > 0xffff || (r == '\n' && previous != '\r') {
			size += 2
		}
		previous = r
	}
	if size > clipMaxData {
		return 0, &clipboardInputError{"RDP clipboard text is too large"}
	}
	return size, nil
}

func decodeClipboardText(format uint32, p []byte) (string, error) {
	if format == clipText {
		end := bytes.IndexByte(p, 0)
		if end < 0 {
			return "", protocolError("RDP CF_TEXT data has no terminator")
		}
		text, err := charmap.Windows1252.NewDecoder().Bytes(p[:end])
		if err != nil {
			return "", protocolError("invalid RDP CF_TEXT data")
		}
		return strings.ReplaceAll(string(text), "\r\n", "\n"), nil
	}
	end := clipboardWideEnd(p)
	if format != clipUnicode || end < 0 {
		return "", protocolError("invalid RDP Unicode clipboard data")
	}
	var text strings.Builder
	text.Grow(end / 2)
	for i := 0; i < end; i += 2 {
		r := rune(binary.LittleEndian.Uint16(p[i:]))
		if utf16.IsSurrogate(r) {
			if i+2 >= end {
				return "", protocolError("truncated RDP clipboard surrogate pair")
			}
			r = utf16.DecodeRune(r, rune(binary.LittleEndian.Uint16(p[i+2:])))
			if r == utf8.RuneError {
				return "", protocolError("invalid RDP clipboard surrogate pair")
			}
			i += 2
		}
		text.WriteRune(r)
	}
	return strings.ReplaceAll(text.String(), "\r\n", "\n"), nil
}

func clipboardWideEnd(p []byte) int {
	for i := 0; i+1 < len(p); i += 2 {
		if p[i] == 0 && p[i+1] == 0 {
			return i
		}
	}
	return -1
}
