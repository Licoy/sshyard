package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/vt"
)

// TestTerminalScrollbarDrag: the scrollbar in the right padding drags
// through the scrollback without selecting text, a press on its strip
// brings the thumb there, and on the alternate screen, which has no
// scrollback, the strip is the program's.
func TestTerminalScrollbarDrag(t *testing.T) {
	var out strings.Builder
	for i := range 200 {
		fmt.Fprintf(&out, "line %d\r\n", i)
	}
	srv, sh := startScript(t, out.String())
	a, tt := newTestApp(t)
	c := connected(t, a, tt, srv)
	tv := c.tv
	waitFor(t, tt, "the scrollback", func() bool { return tv.snap.Scrollback > 100 })
	tt.Frame()

	r := mustFind(t, tt, "Terminal box")
	x := r.X + r.W - termPad/2
	_, thumb, ok := tv.scrollBar()
	if !ok {
		t.Fatal("no scrollbar with a scrollback")
	}
	sb := tv.snap.Scrollback

	// the thumb, at the bottom, dragged to the top
	tt.Move(x, r.Y+thumb.Y+thumb.H/2)
	tt.Frame()
	tt.Press(x, r.Y+thumb.Y+thumb.H/2)
	tt.Move(x, r.Y+r.H/2)
	if tv.offset <= 0 || tv.offset >= sb {
		t.Errorf("dragged halfway the offset is %d of %d", tv.offset, sb)
	}
	tt.Move(x, r.Y-100)
	if tv.offset != sb {
		t.Errorf("dragged past the top the offset is %d, want %d", tv.offset, sb)
	}
	tt.Release(x, r.Y-100)
	tt.Frame()
	if tv.barDrag || tv.selected || tv.selecting {
		t.Errorf("after the drag: dragging %v, selected %v, selecting %v", tv.barDrag, tv.selected, tv.selecting)
	}

	// a press at the strip's bottom, off the thumb, goes back to the screen
	tt.ClickAt(x, r.Y+r.H-1)
	tt.Frame()
	if tv.offset != 0 || tv.selected {
		t.Errorf("a press at the bottom: offset %d, selected %v", tv.offset, tv.selected)
	}

	// the alternate screen has no scrollback: the press is the program's
	sh.take()
	sh.out <- "\x1b[?1049h\x1b[?1000h\x1b[?1006hprogram"
	waitFor(t, tt, "the alternate screen", func() bool {
		m := c.s.Term.Modes()
		return m.AltScreen && m.Mouse != vt.MouseOff
	})
	tt.Frame()
	if _, _, ok := tv.scrollBar(); ok {
		t.Error("a scrollbar on the alternate screen")
	}
	tt.ClickAt(x, r.Y+r.H/2)
	expectRead(t, tt, sh, "\x1b[<0;")
}

// TestTerminalScrollHoldsOnOutput: a view scrolled back stays on the lines
// it shows while output pushes more into the scrollback; the live screen
// follows the output.
func TestTerminalScrollHoldsOnOutput(t *testing.T) {
	lines := func(from, n int) string {
		var out strings.Builder
		for i := range n {
			fmt.Fprintf(&out, "line %d\r\n", from+i)
		}
		return out.String()
	}
	srv, sh := startScript(t, lines(0, 200))
	a, tt := newTestApp(t)
	c := connected(t, a, tt, srv)
	tv := c.tv
	waitFor(t, tt, "the scrollback", func() bool { return tv.snap.Scrollback > 100 })
	tt.Frame()

	tv.offset = 20
	tt.Frame()
	first, sb := tv.first, tv.snap.Scrollback
	sh.out <- lines(200, 50)
	waitFor(t, tt, "the output", func() bool { return tv.snap.Scrollback >= sb+50 })
	tt.Frame()
	if tv.first != first || tv.offset != 70 {
		t.Errorf("scrolled back: first line %d, offset %d; want %d, 70", tv.first, tv.offset, first)
	}

	tv.offset = 0
	tt.Frame()
	sb = tv.snap.Scrollback
	sh.out <- lines(250, 30)
	waitFor(t, tt, "more output", func() bool { return tv.snap.Scrollback >= sb+30 })
	tt.Frame()
	if tv.offset != 0 {
		t.Errorf("at the live screen output scrolled it back %d lines", tv.offset)
	}
}

// TestTerminalScrollOnKey: a key sent to the shell brings a view scrolled
// back to the live screen only with settings.TerminalScrollOnKey; either
// way the shell reads it.
func TestTerminalScrollOnKey(t *testing.T) {
	var out strings.Builder
	for i := range 200 {
		fmt.Fprintf(&out, "line %d\r\n", i)
	}
	srv, sh := startScript(t, out.String())
	a, tt := newTestApp(t)
	c := connected(t, a, tt, srv)
	tv := c.tv
	waitFor(t, tt, "the scrollback", func() bool { return tv.snap.Scrollback > 100 })
	sh.take()

	a.settings.TerminalScrollOnKey = false
	tv.offset = 10
	tt.Frame()
	tv.send([]byte("a"))
	expectRead(t, tt, sh, "a")
	if tv.offset != 10 {
		t.Errorf("off: a key moved the view to offset %d", tv.offset)
	}

	a.settings.TerminalScrollOnKey = true
	tt.Frame()
	tv.send([]byte("b"))
	expectRead(t, tt, sh, "b")
	if tv.offset != 0 {
		t.Errorf("on: a key left the view at offset %d", tv.offset)
	}
}
