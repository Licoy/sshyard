package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// TestBlockPress: a single ⌥/Alt press selects a block, with Shift too;
// Ctrl, ⌘, a plain press and a double click do not. A move shorter than
// blockSlop keeps it a click.
func TestBlockPress(t *testing.T) {
	for _, c := range []struct {
		mods   ui.Modifiers
		clicks int
		want   bool
	}{
		{ui.Alt, 1, true},
		{ui.Alt | ui.Shift, 1, true},
		{0, 1, false},
		{ui.Shift, 1, false},
		{ui.Alt | ui.Ctrl, 1, false},
		{ui.Alt | ui.Super, 1, false},
		{ui.Alt, 2, false},
		{ui.Alt, 3, false},
	} {
		if got := blockPress(c.mods, c.clicks); got != c.want {
			t.Errorf("blockPress(%v, %d) = %v, want %v", c.mods, c.clicks, got, c.want)
		}
	}
	for _, c := range []struct {
		dx, dy float32
		want   bool
	}{
		{0, 0, false}, {2, 2, false}, {-3, 0, false}, {0, 3.9, false},
		{4, 0, true}, {0, -4, true}, {3, 3, true}, {-20, 15, true},
	} {
		if got := pastBlockSlop(c.dx, c.dy); got != c.want {
			t.Errorf("pastBlockSlop(%v, %v) = %v, want %v", c.dx, c.dy, got, c.want)
		}
	}
}

// recordClipboard keeps what is written to it.
type recordClipboard struct{ text *string }

func (recordClipboard) ReadClipboard() string     { return "" }
func (c recordClipboard) WriteClipboard(s string) { *c.text = s }

// TestTerminalBlockSelection: an ⌥/Alt drag in any direction selects the
// same columns of every row it spans, which copy, copy-on-select and the
// highlight follow; a wobble short of a drag stays a click and moves the
// cursor, and a plain click clears the block.
func TestTerminalBlockSelection(t *testing.T) {
	term := vt.New(20, 5)
	var out []byte
	v := newTermView(recordTarget{term, &out}, 13)
	v.cellW, v.cellH, v.cols, v.rows = 10, 20, 20, 5
	var copied string
	cb := recordClipboard{&copied}
	at := func(col, row int) (float32, float32) {
		return termPad + float32(col)*10 + 5, termPad + float32(row)*20 + 5
	}
	drag := func(mods ui.Modifiers, c0, r0, c1, r1 int) {
		t.Helper()
		x0, y0 := at(c0, r0)
		x1, y1 := at(c1, r1)
		v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: x0, Y: y0, Mods: mods, Clicks: 1}, cb)
		v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x1, Y: y1, Mods: mods}, cb)
		v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: x1, Y: y1, Mods: mods}, cb)
	}
	term.WriteString("alpha one\r\nb中文 two   \r\ngamma\r\n$ ")

	want := "lpha\n中文\namma"
	for _, d := range [][4]int{{1, 0, 4, 2}, {4, 2, 1, 0}, {4, 0, 1, 2}, {1, 2, 4, 0}} {
		drag(ui.Alt, d[0], d[1], d[2], d[3])
		if !v.block.on || !v.selected {
			t.Fatalf("drag %v: block %v, selected %v", d, v.block.on, v.selected)
		}
		if got := v.SelectedText(); got != want {
			t.Errorf("drag %v selected %q, want %q", d, got, want)
		}
	}
	if from, to := v.selectedCols(1); from != 1 || to != 4 {
		t.Errorf("row 1 selects %d–%d, want 1–4", from, to)
	}
	if from, to := v.selectedCols(3); from <= to {
		t.Errorf("row 3, below the block, selects %d–%d", from, to)
	}

	// The copy command copies the block.
	copied = ""
	v.input(ui.InputEvent{Kind: ui.InputCommand, Text: "copy"}, cb)
	if copied != want {
		t.Errorf("copy: %q, want %q", copied, want)
	}

	// Wide characters on the edges are taken whole, in the copy and the
	// highlight: column 2 is the right half of 中, column 3 the first of 文.
	drag(ui.Alt, 2, 1, 3, 2)
	if got := v.SelectedText(); got != "中文\nmm" {
		t.Errorf("wide edges selected %q", got)
	}
	line := term.Snapshot(0).Lines[1]
	if from, to := v.rowSelection(1, line); from != 1 || to != 4 {
		t.Errorf("wide edges highlight %d–%d, want 1–4", from, to)
	}

	// Rows past their text and trailing blanks give empty lines.
	drag(ui.Alt, 6, 0, 14, 2)
	if got := v.SelectedText(); got != "one\ntwo\n" {
		t.Errorf("past the text selected %q", got)
	}

	// Copy-on-select copies it as it is made.
	v.copyOnSelect, copied = true, ""
	drag(ui.Alt, 0, 0, 0, 2)
	if copied != "a\nb\ng" {
		t.Errorf("copy-on-select: %q", copied)
	}
	v.copyOnSelect = false

	// A wobble within the slop is a click: no selection, and the cursor
	// moves (it is at column 2 of row 3).
	out = out[:0]
	x, y := at(0, 3)
	x += 4 // a DIP short of column 1
	v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: x, Y: y, Mods: ui.Alt, Clicks: 1}, cb)
	v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x + 3, Y: y, Mods: ui.Alt}, cb) // into column 1
	v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: x + 3, Y: y, Mods: ui.Alt}, cb)
	if v.selected {
		t.Errorf("a wobble selected %q", v.SelectedText())
	}
	if got, want := string(out), strings.Repeat("\x1b[D", 2); got != want {
		t.Errorf("a wobble sent %q, want %q", got, want)
	}

	// A plain click clears it, and a plain drag is not a block.
	drag(ui.Alt, 1, 0, 4, 2)
	x, y = at(5, 1)
	v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: x, Y: y, Clicks: 1}, cb)
	v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: x, Y: y}, cb)
	if v.selected || v.block.on {
		t.Errorf("a click left the block: selected %v, block %v", v.selected, v.block.on)
	}
	drag(0, 1, 0, 4, 1)
	if v.block.on || v.SelectedText() != "lpha one\nb中文" {
		t.Errorf("a plain drag: block %v, selected %q", v.block.on, v.SelectedText())
	}
	// Select all is never a block.
	drag(ui.Alt, 1, 0, 4, 2)
	v.input(ui.InputEvent{Kind: ui.InputCommand, Text: "selectAll"}, cb)
	if v.block.on {
		t.Error("select all kept the block")
	}
}

// TestTerminalBlockSelectionMouseReporting: a program reading the mouse
// gets an ⌥/Alt drag only where Alt alone does not take the mouse back;
// with Shift too, it is a block selection everywhere.
func TestTerminalBlockSelectionMouseReporting(t *testing.T) {
	term := vt.New(20, 4)
	var out []byte
	v := newTermView(recordTarget{term, &out}, 13)
	v.cellW, v.cellH, v.cols, v.rows = 10, 20, 20, 4
	term.WriteString("one two\r\nthree four\r\n\x1b[?1000h\x1b[?1006h")
	out = out[:0]
	x0, y0 := termPad+1*10+5, termPad+0*20+5
	x1, y1 := termPad+3*10+5, termPad+1*20+5
	mods := ui.Alt | ui.Shift
	v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: float32(x0), Y: float32(y0), Mods: mods, Clicks: 1}, nil)
	v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: float32(x1), Y: float32(y1), Mods: mods}, nil)
	v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: float32(x1), Y: float32(y1), Mods: mods}, nil)
	if len(out) != 0 {
		t.Errorf("the program got %q", out)
	}
	if !v.block.on || v.SelectedText() != "ne\nhre" {
		t.Errorf("Shift+Alt drag: block %v, selected %q", v.block.on, v.SelectedText())
	}
}

// TestTerminalBlockDragScrolls: a block drag held past the grid's bottom
// scrolls the view as a line drag does, and keeps the same columns of
// every row it goes by, those in the scrollback too.
func TestTerminalBlockDragScrolls(t *testing.T) {
	f := newDragScrollFixture(t)
	v, tt := f.v, f.tt
	v.offset = 50
	tt.Frame()
	top := v.first
	x0, x1 := termPad+2*v.cellW+1, termPad+6*v.cellW+1
	last := termPad + float32(v.rows)*v.cellH
	v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: x0, Y: termPad + v.cellH/2, Mods: ui.Alt, Clicks: 1}, fakeClipboard{})
	v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x1, Y: last + 40, Mods: ui.Alt}, fakeClipboard{})
	tt.Frame()
	tt.Advance(300 * time.Millisecond)
	if v.dragScroll != 1 || v.offset >= 50 {
		t.Fatalf("the block drag did not scroll: direction %d, offset %d", v.dragScroll, v.offset)
	}
	b := vt.BlockOf(v.selA, v.selB)
	if !v.block.on || b.Top != top || b.Bottom != v.first+v.rows-1 || b.Left != 2 || b.Right != 6 {
		t.Fatalf("block %+v (on %v), want lines %d–%d, columns 2–6", b, v.block.on, top, v.first+v.rows-1)
	}
	if from, to := v.selectedCols(top); from != 2 || to != 6 {
		t.Errorf("the first row, now in the scrollback, selects %d–%d", from, to)
	}
	v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: x1, Y: last + 40, Mods: ui.Alt}, fakeClipboard{})
	rows := strings.Split(v.SelectedText(), "\n")
	if len(rows) != b.Bottom-b.Top+1 {
		t.Fatalf("copied %d rows, want %d", len(rows), b.Bottom-b.Top+1)
	}
	for i, r := range rows {
		if want := fmt.Sprintf("line %d", b.Top+i); len(want) > 2 && r != strings.TrimRight(want[2:min(len(want), 7)], " ") {
			t.Errorf("row %d copied %q, from %q", i, r, want)
		}
	}
}
