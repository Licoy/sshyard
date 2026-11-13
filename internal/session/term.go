package session

import "strings"

// DefaultTerm is the TERM a terminal reports when none is chosen.
const DefaultTerm = "xterm-256color"

// TermOK reports whether term may be reported as TERM: a terminfo name of
// 1 to 64 letters, digits and "._+-", not starting with "-".
func TermOK(term string) bool {
	if term == "" || len(term) > 64 || term[0] == '-' {
		return false
	}
	for _, r := range term {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("._+-", r)) {
			return false
		}
	}
	return true
}

// linuxTerm reports whether term names the Linux console, whose keys the
// terminal then sends (vt.Modes.LinuxKeys).
func linuxTerm(term string) bool {
	return term == "linux" || strings.HasPrefix(term, "linux-")
}
