package app

import (
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// A selection dragged with ⌥ (Alt elsewhere) held is a block: the same
// columns on every row from the press to the pointer, rather than the
// text running from one to the other. A program reading the mouse keeps
// it unless the key that takes the mouse back for selecting is held too
// (see reports).

// blockSel is the state of a block selection, beside the selection's
// corners selA and selB.
type blockSel struct {
	on bool // the selection is a block
	// pending is a block press the pointer has not yet moved blockSlop
	// from, at x, y: let go there, it stays a click, which ⌥ makes move
	// the shell's cursor.
	pending bool
	x, y    float32
}

// blockSlop is how far, in DIPs, the pointer must go from a block press
// for it to become a drag.
const blockSlop = 4

// blockPress reports whether a press selects a block: a single click
// with ⌥/Alt, and neither Ctrl nor ⌘, which have their own meanings.
// Shift may be held too, as it must for a program reading the mouse.
func blockPress(mods ui.Modifiers, clicks int) bool {
	return clicks == 1 && mods&ui.Alt != 0 && mods&(ui.Ctrl|ui.Super) == 0
}

// pastBlockSlop reports whether the pointer moved dx, dy from the press
// far enough to start a block drag.
func pastBlockSlop(dx, dy float32) bool { return dx*dx+dy*dy >= blockSlop*blockSlop }

// startBlock sets the selection pressed with ev to a block, or not.
func (v *termView) startBlock(ev ui.InputEvent) {
	on := blockPress(ev.Mods, ev.Clicks)
	v.block = blockSel{on: on, pending: on, x: ev.X, y: ev.Y}
}

// holdBlock reports whether a move to x, y of a block press is still
// short of a drag, which leaves the selection as it is.
func (v *termView) holdBlock(x, y float32) bool {
	if !v.block.pending {
		return false
	}
	if !pastBlockSlop(x-v.block.x, y-v.block.y) {
		return true
	}
	v.block.pending = false
	return false
}

// blockCols is the columns of line a block selection takes, from to to
// inclusive, from > to for none; wide characters are not yet widened.
func (v *termView) blockCols(line int) (from, to int) {
	b := vt.BlockOf(v.selA, v.selB)
	if !v.selected || line < b.Top || line > b.Bottom {
		return 1, 0
	}
	return b.Left, b.Right
}

// rowSelection is the columns of l, line n of the history, the selection
// highlights: a block's widened to the wide characters on its edges.
func (v *termView) rowSelection(n int, l vt.Line) (from, to int) {
	from, to = v.selectedCols(n)
	if v.block.on && from <= to {
		from, to = l.Span(from, to)
	}
	return from, to
}

// blockText is the text of a block selection, a line a row.
func (v *termView) blockText() string {
	return v.target.Terminal().BlockText(vt.BlockOf(v.selA, v.selB))
}
