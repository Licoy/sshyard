package vt

import (
	"strings"
	"testing"
)

// TestClearScreen clears the scrollback and the screen above the prompt,
// keeping the prompt line and putting it first.
func TestClearScreen(t *testing.T) {
	term := New(10, 4)
	term.WriteString("l1\r\nl2\r\nl3\r\nl4\r\nl5\r\n$ cmd")
	if got := term.Snapshot(0).Scrollback; got != 2 {
		t.Fatalf("scrollback %d, want 2", got)
	}
	if !term.ClearScreen() {
		t.Fatal("ClearScreen did not clear")
	}
	s := term.Snapshot(0)
	if s.Scrollback != 0 {
		t.Errorf("scrollback %d after clearing", s.Scrollback)
	}
	if got := s.Lines[0].String(); got != "$ cmd" {
		t.Errorf("first line %q, want %q", got, "$ cmd")
	}
	for y := 1; y < 4; y++ {
		if got := s.Lines[y].String(); got != "" {
			t.Errorf("line %d %q, want empty", y, got)
		}
	}
	if s.CursorX != 5 || s.CursorY != 0 {
		t.Errorf("cursor at %d,%d, want 5,0", s.CursorX, s.CursorY)
	}

	term.WriteString("\r\n$ next")
	if got := rows(term)[1]; got != "$ next" {
		t.Errorf("line after clearing %q, want %q", got, "$ next")
	}
}

// TestClearScreenAltScreen leaves a full-screen program's screen alone.
func TestClearScreenAltScreen(t *testing.T) {
	term := New(10, 3)
	term.WriteString("$ top")
	term.WriteString("\x1b[?1049hfull")
	before := strings.Join(rows(term), "\n")
	if term.ClearScreen() {
		t.Error("ClearScreen cleared the alternate screen")
	}
	if got := strings.Join(rows(term), "\n"); got != before {
		t.Errorf("alternate screen changed to %q, want %q", got, before)
	}
	term.WriteString("\x1b[?1049l")
	if got := rows(term)[0]; got != "$ top" {
		t.Errorf("primary screen %q, want %q", got, "$ top")
	}
}

// TestClearScreenAtTop clears only the scrollback when the prompt is
// already on the first line.
func TestClearScreenAtTop(t *testing.T) {
	term := New(10, 3)
	term.WriteString("l1\r\nl2\r\nl3\r\nl4\r\n$ ")
	before := rows(term)
	term.WriteString("\x1b[H")
	if !term.ClearScreen() {
		t.Fatal("ClearScreen did not clear")
	}
	s := term.Snapshot(0)
	if s.Scrollback != 0 {
		t.Errorf("scrollback %d after clearing", s.Scrollback)
	}
	// The cursor moved to the top, so the screen above it is empty, but
	// the lines below it stay.
	if got := s.Lines[0].String(); got != "l3" {
		t.Errorf("first line %q, want %q", got, "l3")
	}
	if s.CursorX != 0 || s.CursorY != 0 {
		t.Errorf("cursor at %d,%d, want 0,0", s.CursorX, s.CursorY)
	}
	if len(before) != len(rows(term)) {
		t.Errorf("screen height changed from %d to %d", len(before), len(rows(term)))
	}
}
