package assets

import (
	"strings"
	"unicode/utf8"
)

// Startup commands are what an SSH host's terminals type, line by line,
// each time their shell starts (Host.Startup). The text is a host-owned
// secret, kept in the encrypted store like a snippet's body.
const (
	startupMaxBytes = 4 << 10
	startupMaxLines = 32
)

// ValidateStartup accepts at most 4 KiB of UTF-8 text in at most 32
// command lines, with CR and LF the only C0 controls: a TAB typed at a
// prompt completes rather than inserts. Errors never contain the text.
func ValidateStartup(text string) error {
	reason := ""
	switch {
	case !utf8.ValidString(text):
		reason = "invalid"
	case len(text) > startupMaxBytes:
		reason = "tooLong"
	case strings.ContainsFunc(text, func(r rune) bool { return r == 0x7f || r < 0x20 && r != '\r' && r != '\n' }):
		reason = "invalid"
	case len(StartupLines(text)) > startupMaxLines:
		reason = "tooLong"
	}
	if reason != "" {
		return &FieldError{"startup", reason}
	}
	return nil
}

// StartupLines are the command lines of text, in order, without the
// blank ones and the trailing spaces.
func StartupLines(text string) []string {
	var out []string
	for line := range strings.Lines(strings.ReplaceAll(text, "\r", "\n")) {
		if line = strings.TrimRight(line, " \t\r\n"); strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
