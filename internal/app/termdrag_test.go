package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// dragScrollFixture is a terminal view drawn in a window taller than its
// box, so that a drag can be taken past its bottom and its top.
type dragScrollFixture struct {
	v  *termView
	tt *ui.Tester
}

func newDragScrollFixture(t *testing.T) *dragScrollFixture {
	t.Helper()
	term := vt.New(40, 6)
	var out strings.Builder
	for i := range 300 {
		fmt.Fprintf(&out, "line %d\r\n", i)
	}
	term.WriteString(out.String())
	f := &dragScrollFixture{v: newTermView(paintTarget{term}, 13)}
	pal := theme.For(theme.Auto, false).Terminal
	f.tt = ui.NewTester(func(c *ui.Context) {
		ui.Box(c).FillWidth().Height(200).
			HandleInput(func(ev ui.InputEvent) bool { return f.v.input(ev, fakeClipboard{}) }).
			Draw(func(p *ui.Painter, r ui.Rect) { f.v.paint(p, r, &pal, true) })
	}, 300, 500)
	f.tt.Frame()
	return f
}

// TestTerminalDragScrollsSelection: a selection drag taken past the grid's
// bottom scrolls the view towards the live screen and its end covers the
// lines it goes by; past the top it scrolls back through the scrollback.
// Letting go stops it.
func TestTerminalDragScrollsSelection(t *testing.T) {
	f := newDragScrollFixture(t)
	v, tt := f.v, f.tt
	if v.rows < 2 || v.cellH <= 0 || v.cellW <= 0 {
		t.Fatalf("no grid: %d rows, cells %vx%v", v.rows, v.cellW, v.cellH)
	}
	v.offset = 50
	tt.Frame()
	if v.offset != 50 {
		t.Fatalf("the view starts scrolled to %d", v.offset)
	}
	x := termPad + 5*v.cellW
	last := termPad + float32(v.rows)*v.cellH

	// Past the bottom: the view follows the pointer down.
	tt.Press(x, termPad+v.cellH/2)
	before := v.offset
	tt.Move(x, last+40)
	if v.dragScroll != 1 {
		t.Fatalf("a drag past the bottom scrolls the view %d", v.dragScroll)
	}
	tt.Frame()
	if v.offset >= before {
		t.Fatalf("the drag did not scroll: offset %d, was %d", v.offset, before)
	}
	scrolled, end := v.offset, v.selB.Line
	tt.Advance(300 * time.Millisecond)
	if v.offset >= scrolled {
		t.Errorf("holding the drag did not scroll on: offset %d, was %d", v.offset, scrolled)
	}
	if want := v.first + v.rows - 1; v.selB.Line != want || v.selB.Line <= end {
		t.Errorf("the selection ends at line %d, want %d (%d before)", v.selB.Line, want, end)
	}
	tt.Release(x, last+40)
	if v.selecting || v.dragScroll != 0 {
		t.Fatalf("let go: selecting %v, scrolling %d", v.selecting, v.dragScroll)
	}
	stopped := v.offset
	tt.Advance(300 * time.Millisecond)
	if v.offset != stopped {
		t.Errorf("the view scrolled to %d after the release, from %d", v.offset, stopped)
	}

	// Past the top: back through the scrollback, the selection's end with it.
	tt.Press(x, termPad+float32(v.rows-1)*v.cellH)
	before = v.offset
	tt.Move(x, 0)
	if v.dragScroll != -1 {
		t.Fatalf("a drag past the top scrolls the view %d", v.dragScroll)
	}
	tt.Frame()
	if v.offset <= before {
		t.Fatalf("the drag did not scroll back: offset %d, was %d", v.offset, before)
	}
	tt.Advance(300 * time.Millisecond)
	if want := v.first; v.selB.Line != want {
		t.Errorf("the selection ends at line %d, want the top row %d", v.selB.Line, want)
	}
	tt.Release(x, 0)
	if v.selecting || v.dragScroll != 0 {
		t.Fatalf("let go: selecting %v, scrolling %d", v.selecting, v.dragScroll)
	}
}
