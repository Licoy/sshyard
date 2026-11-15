package vt

import "testing"

// TestCursorText: the cursor's line split at the cursor, lines that
// wrapped being one, a wide character once, the blanks ending it dropped.
func TestCursorText(t *testing.T) {
	cases := []struct {
		name, output  string
		before, after string
	}{
		{"at the end", "$ ls -l", "$ ls -l", ""},
		{"moved left", "$ ls -l\x1b[2D", "$ ls ", "-l"},
		{"another line", "out\r\n$ x", "$ x", ""},
		{"wrapped", "$ abcdefghijkl", "$ abcdefghijkl", ""},
		{"wide", "$ 中文ab", "$ 中文ab", ""},
		{"wide character that did not fit", "$ abcdefg中", "$ abcdefg中", ""},
		{"text on the right", "$ \x1b[8G[1]\x1b[3G", "$ ", "     [1]"},
	}
	for _, c := range cases {
		term := New(10, 4)
		term.WriteString(c.output)
		s := term.Snapshot(0)
		before, after, ok := s.CursorText()
		if !ok || before != c.before || after != c.after {
			t.Errorf("%s: %q, %q, %v; want %q, %q", c.name, before, after, ok, c.before, c.after)
		}
	}
}

// TestCwdReportCount counts every directory report, the same directory again
// too, and nothing else.
func TestCwdReportCount(t *testing.T) {
	term := New(10, 4)
	term.WriteString("\x1b]0;title\x07")
	if n := term.CwdReports(); n != 0 {
		t.Fatalf("a title counted: %d", n)
	}
	term.WriteString("\x1b]7;file://h/tmp\x07\x1b]7;file://h/tmp\x07\x1b]1337;CurrentDir=/etc\x07\x1b]7;nonsense\x07")
	if n := term.CwdReports(); n != 3 {
		t.Fatalf("reports %d, want 3", n)
	}
	term.WriteString("\x1bc")
	if n := term.CwdReports(); n != 3 {
		t.Fatalf("a reset lost the count: %d", n)
	}
}
