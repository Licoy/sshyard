package vt

import (
	"fmt"
	"strings"
	"testing"
)

// TestBlockOf: a block has the same corners whichever way the drag
// between them went.
func TestBlockOf(t *testing.T) {
	want := Block{Top: 2, Bottom: 5, Left: 3, Right: 7}
	for _, c := range [][2]Pos{
		{{2, 3}, {5, 7}}, // down and right
		{{5, 7}, {2, 3}}, // up and left
		{{2, 7}, {5, 3}}, // down and left
		{{5, 3}, {2, 7}}, // up and right
	} {
		if got := BlockOf(c[0], c[1]); got != want {
			t.Errorf("BlockOf(%v, %v) = %+v, want %+v", c[0], c[1], got, want)
		}
	}
	if got := BlockOf(Pos{4, 6}, Pos{4, 6}); got != (Block{4, 4, 6, 6}) {
		t.Errorf("one cell: %+v", got)
	}
}

// TestBlockText: a line a row of the block, trailing blanks trimmed,
// wide characters on either edge taken whole, empty lines for rows past
// their text, and wrapped rows not joined.
func TestBlockText(t *testing.T) {
	term := New(12, 5)
	term.WriteString("abcdefgh\r\n")
	term.WriteString("a中文bcd\r\n") // 中 at 1–2, 文 at 3–4
	term.WriteString("ab   \r\n")
	term.WriteString("\r\n")
	term.WriteString("0123456789AB")
	term.WriteString("CD")
	for _, c := range []struct {
		name string
		b    Block
		want string
	}{
		{"columns of every row", Block{0, 0, 2, 4}, "cde"},
		{"right half on the left edge", Block{1, 1, 2, 2}, "中"},
		{"first half on the right edge", Block{1, 1, 0, 1}, "a中"},
		{"wide on both edges", Block{1, 1, 2, 3}, "中文"},
		{"trailing blanks trimmed", Block{2, 2, 0, 4}, "ab"},
		{"rows past their text", Block{0, 3, 6, 8}, "gh\ncd\n\n"},
		{"wrapped rows not joined", Block{4, 5, 0, 2}, "012\nCD"},
		{"past the history", Block{4, 99, 10, 11}, "AB\n"},
	} {
		if got := term.BlockText(c.b); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := term.BlockText(Block{1, 1, 4, 4}); got != "文" {
		t.Errorf("one wide cell's right half: %q", got)
	}
}

// TestBlockTextScrollback: rows in the scrollback are copied as those on
// the screen.
func TestBlockTextScrollback(t *testing.T) {
	term := New(20, 3)
	var out strings.Builder
	for i := range 10 {
		fmt.Fprintf(&out, "row %d xyz\r\n", i)
	}
	term.WriteString(out.String())
	if s := term.Snapshot(0); s.Scrollback == 0 {
		t.Fatal("no scrollback")
	}
	if got, want := term.BlockText(Block{Top: 1, Bottom: 3, Left: 4, Right: 8}), "1 xyz\n2 xyz\n3 xyz"; got != want {
		t.Errorf("scrollback block: %q, want %q", got, want)
	}
}

// TestLineSpan: the columns of a row widen to the wide characters on
// their edges, and stay within the row.
func TestLineSpan(t *testing.T) {
	term := New(8, 1)
	term.WriteString("中a文")
	l := term.Snapshot(0).Lines[0]
	for _, c := range []struct{ left, right, from, to int }{
		{1, 2, 0, 2}, // the right half of 中, a
		{2, 3, 2, 4}, // a, the first half of 文
		{1, 3, 0, 4}, // both
		{2, 2, 2, 2}, // a alone
		{7, 7, 7, 7}, // a blank at the margin
		{6, 9, 6, 9}, // past the row, kept as given
		{0, 0, 0, 1}, // the first half of 中 alone
		{-1, -1, -1, -1},
	} {
		if from, to := l.Span(c.left, c.right); from != c.from || to != c.to {
			t.Errorf("Span(%d, %d) = %d–%d, want %d–%d", c.left, c.right, from, to, c.from, c.to)
		}
	}
}
