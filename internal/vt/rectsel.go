package vt

import "strings"

// Block is a rectangle of cells, for a block selection: lines Top to
// Bottom of the history, absolute as Pos's, and columns Left to Right,
// all inclusive.
type Block struct{ Top, Bottom, Left, Right int }

// BlockOf is the block with corners a and b, whichever way the drag
// between them went.
func BlockOf(a, b Pos) Block {
	return Block{
		Top: min(a.Line, b.Line), Bottom: max(a.Line, b.Line),
		Left: min(a.Col, b.Col), Right: max(a.Col, b.Col),
	}
}

// Span is the columns from left to right of the line, inclusive, widened
// so that a wide character on either edge is taken whole, never half.
func (l Line) Span(left, right int) (from, to int) {
	from, to = left, right
	for from > 0 && from < len(l.Cells) && l.Cells[from].Width == 0 {
		from-- // the right half of a wide character starts a cell before
	}
	if to >= 0 && to+1 < len(l.Cells) && l.Cells[to].Width == 2 {
		to++
	}
	return from, to
}

// BlockText returns the text of the block: a line for each of its rows,
// without the blanks at its end, joined with newlines. A row past the end
// of its text is an empty line, and rows that wrapped are not joined.
func (t *Terminal) BlockText(b Block) string {
	if b.Right < b.Left {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	top := max(b.Top, t.firstLine())
	bottom := min(b.Bottom, t.firstLine()+t.historyLen()-1)
	var sb strings.Builder
	for n := top; n <= bottom; n++ {
		if n > top {
			sb.WriteByte('\n')
		}
		l := t.lineAt(n)
		from, to := l.Span(b.Left, b.Right)
		from, to = max(from, 0), min(to+1, len(l.Cells))
		if from < to {
			sb.WriteString(Line{Cells: l.Cells[from:to]}.text(true))
		}
	}
	return sb.String()
}
