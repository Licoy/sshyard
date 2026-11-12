package migration

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/unicode"
)

// iniPair is a key=value line; line is its number, from 1.
type iniPair struct {
	key, value string
	line       int
}
type iniSection struct {
	name  string
	pairs []iniPair
}

func (s iniSection) get(key string) string {
	for i := len(s.pairs) - 1; i >= 0; i-- {
		if strings.EqualFold(s.pairs[i].key, key) {
			return s.pairs[i].value
		}
	}
	return ""
}
func iniText(data []byte) (string, error) {
	var out []byte
	var err error
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		if len(data)%2 != 0 {
			return "", ErrFormat
		}
		out, err = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(data)
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		out = data[3:]
	case utf8.Valid(data):
		out = data
	default:
		out, _, err = legacyText(data, Options{})
	}
	if err != nil || bytes.ContainsRune(out, '\x00') || bytes.Contains(out, []byte("\uFFFD")) {
		return "", ErrFormat
	}
	return string(out), nil
}
func iniRead(data []byte) ([]iniSection, error) {
	text, err := iniText(data)
	if err != nil {
		return nil, err
	}
	sections := []iniSection{{}}
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, ";") || (strings.HasPrefix(line, "#") && !strings.Contains(line, "=")) {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || len(line) < 3 {
				return nil, &FormatError{Line: n + 1}
			}
			sections = append(sections, iniSection{name: line[1 : len(line)-1]})
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, &FormatError{Line: n + 1}
		}
		i := len(sections) - 1
		sections[i].pairs = append(sections[i].pairs, iniPair{strings.TrimSpace(key), strings.TrimSpace(value), n + 1})
	}
	return sections, nil
}
func iniPort(value string) int {
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return -1
	}
	return n
}
func iniIssue(e *Entry, code, field string) {
	e.Issues = append(e.Issues, Issue{Code: code, Name: e.Name, Field: field})
}
func iniReport(e *Entry, s iniSection, code string, keys ...string) {
	for _, key := range keys {
		v := s.get(key)
		if v != "" && (code == "encrypted-secret" || v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "none")) {
			iniIssue(e, code, key)
		}
	}
}
func iniFilename(name string) string {
	return strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
}
