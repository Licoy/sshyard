package app

import (
	"bytes"
	"errors"
	"math"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/highlight"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// TerminalView is SSHYard's own native terminal widget: it draws a
// vt.Terminal's grid with the ui Painter, glyph by glyph in its cells,
// and turns keys, text, the input method's composition, the pointer and
// the wheel into what the program on the other end reads. No web view
// and no HTML terminal are involved.

// ptyTarget is what a terminal view drives: the terminal it shows, and the
// program's input and window size.
type ptyTarget interface {
	Terminal() *vt.Terminal
	Write(p []byte) (int, error)
	Resize(cols, rows int)
}

const termPad = 10 // DIPs around the grid, inside the pane card

// A selection drag scrolls the view taken past the grid's top or bottom
// (dragMargin): slowest at the margin, and dragScrollRamp rows past it as
// fast as dragScrollFast lines a second.
const (
	dragMargin     = 0.25                   // of a row inside the grid's edge
	dragScrollRamp = 3                      // rows past the margin at the fastest
	dragScrollSlow = 4                      // lines a second at the margin
	dragScrollFast = 30                     // lines a second dragScrollRamp rows past it
	dragScrollRate = 60                     // frames a second while it scrolls
	dragScrollStep = 100 * time.Millisecond // the most a late frame scrolls
)

type termView struct {
	target         ptyTarget
	snippetSending bool // UI-thread state, cleared when the captured write returns
	inputError     func(error)
	// fanout, set while what is typed goes to several terminals, sends
	// it to the others too, encoded by encode for each one's modes. Keys,
	// text and pastes go; mouse and focus reports stay here. holdPaste,
	// set then too, takes a paste to send once asked, when it reports so.
	fanout    func(encode func(vt.Modes) []byte)
	holdPaste func(text string) bool
	// local is a terminal on this computer, whose programs can read its
	// clipboard themselves (see pasteClipboard).
	local bool
	// shortcuts are the shortcuts recorded (settings.Shortcuts), whose
	// keys the terminal leaves to the app
	shortcuts map[string]string
	// rightClick is what a right click does (settings.TerminalRightClick)
	// when no program reads the mouse; copyOnSelect copies a selection as
	// it is made (settings.CopyOnSelect).
	rightClick   string
	copyOnSelect bool
	cursorStyle  vt.CursorStyle // user default, unless the program sets DECSCUSR
	cursorBlink  bool           // user preference can force a steady cursor
	blink        cursorBlinkClock
	// openLink opens a web address clicked with linkMods; link is the one
	// under the pointer while they are held, underlined.
	openLink func(string)
	link     linkSpan
	// zoomFont makes the terminals' font n points larger (smaller when n
	// is negative), as the wheel turns with fontMods held or a touchpad
	// pinches; zoomAcc and pinchAcc are a touchpad's scroll and pinch on
	// their way to the next point.
	zoomFont          func(n int)
	zoomAcc, pinchAcc float32
	// clear leaves the background to the pane's translucent fill, for a
	// content area on the window's material (settings.ContentMaterial)
	clear bool
	// hl colors the keywords of the output (settings.Highlight), nil for
	// none; hlOverride colors those the program colored too, and
	// hlFullScreen those of a full-screen program.
	hl                       *highlight.Highlighter
	hlOverride, hlFullScreen bool
	// lineNumbers and timestamps show, in a gutter left of the grid,
	// each line's number and the time output reached it
	// (settings.TerminalLineNumbers, TerminalTimestamps).
	lineNumbers, timestamps bool

	// font
	fontSize            float32
	family              string     // the font family list; "" is "monospace"
	lineHeight          float32    // the rows' height, a multiple of the font's line; 0 is 1
	charWidth           float32    // the cells' width, a multiple of the font's advance; 0 is 1
	spaced              [2]float32 // lineHeight and charWidth, as last measured
	scale               float32
	cellW, cellH, base  float32 // DIPs, whole device pixels
	fonts               [4]ui.Font
	shaped              [4]map[string][]ui.Glyph // by font, as fonts
	nshaped             int
	cols, rows          int
	sized               bool
	first               int // absolute line of the top row last drawn
	offset              int // lines scrolled into the scrollback
	scrollAcc           float32
	w, h                float32 // the element's size last drawn
	selA, selB          vt.Pos
	selecting, selected bool
	// a selection drag taken past the grid's top or bottom scrolls the
	// view: dragScroll is where (-1 back, 1 forward), dragX the
	// pointer's column, dragOver how far past the margin it is, and
	// dragAt and dragAcc the clock and the lines of the scroll on its way
	dragScroll      int
	dragX, dragOver float32
	dragAt          time.Time
	dragAcc         float32
	preedit         string
	swallow         bool
	// altClick is a plain left click with ⌥ (Alt elsewhere) being held:
	// let go where it was pressed, it moves the shell's cursor there.
	altClick bool

	// mouse reports
	held      bool // a button press was reported, its release not yet
	heldBtn   vt.MouseBtn
	lastMouse vt.Pos // the cell of the last report, for moves
	wheelAcc  float32
	focusSeen int8 // 0 unknown, 1 focused, -1 not

	// the scrollbar: the pointer over its strip, the thumb dragged, and
	// where in the thumb it was taken
	barHover, barDrag bool
	barGrab           float32

	// what paint reuses from frame to frame, so that a frame of a
	// terminal allocates nothing once warm
	snap   vt.Snapshot
	run    []byte
	colOf  []int
	placed []ui.Glyph
	marks  []uint8 // the matches on a row, as rowMatches marks them
	label  []byte  // a gutter label being written

	// find
	query      string
	matches    []vt.Match
	current    int
	findGen    uint64
	findQuery  string
	findActive bool
	findAt     time.Time // when the matches were found
}

func newTermView(t ptyTarget, fontSize float32) *termView {
	return &termView{target: t, fontSize: fontSize, cursorBlink: true}
}

// metrics measures the cells of the terminal font at a scale, again
// when its size or family changes.
func (v *termView) metrics(scale float32) {
	if scale <= 0 {
		scale = 1
	}
	fam := v.family
	if fam == "" {
		fam = "monospace"
	}
	spacing := [2]float32{v.lineHeight, v.charWidth}
	if v.cellW > 0 && v.scale == scale && v.fonts[0].Size == v.fontSize && v.fonts[0].Family == fam && v.spaced == spacing {
		return
	}
	v.scale, v.spaced = scale, spacing
	for i := range v.fonts {
		f := ui.Font{Family: fam, Size: v.fontSize, Weight: 400}
		if i&1 != 0 {
			f.Weight = 700
		}
		f.Italic = i&2 != 0
		v.fonts[i] = f
	}
	m := v.fonts[0].Metrics()
	adv := float32(0)
	for _, g := range ui.Shape(asciiPrintable, v.fonts[0]) {
		adv = max(adv, g.Advance)
	}
	if adv <= 0 {
		adv = v.fontSize * 0.6
	}
	h := m.Ascent + m.Descent + m.LineGap
	if h <= 0 {
		h = v.fontSize * 1.25
		m.Ascent = v.fontSize
	}
	if v.lineHeight > 0 {
		h *= v.lineHeight
	}
	if v.charWidth > 0 {
		adv *= v.charWidth
	}
	cw := max(float32(math.Ceil(float64(adv*scale-0.05))), 1)
	ch := max(float32(math.Ceil(float64(h*scale-0.05))), 1)
	v.cellW, v.cellH = cw/scale, ch/scale
	v.base = float32(math.Round(float64((ch-(m.Ascent+m.Descent)*scale)/2+m.Ascent*scale))) / scale
	v.shaped, v.nshaped = [4]map[string][]ui.Glyph{}, 0
}

const asciiPrintable = " !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"

// gridSize is how many cells fit in w×h DIPs.
func (v *termView) gridSize(w, h float32) (int, int) {
	if v.cellW == 0 {
		return 80, 24
	}
	cols := int((w - 2*termPad - v.gutterW()) / v.cellW)
	rows := int((h - 2*termPad) / v.cellH)
	return max(cols, 2), max(rows, 1)
}

// gutterDigits is the least room line numbers take, so that the grid
// does not narrow, and the program get a resize, each time they grow.
const gutterDigits = 5

// timeLayout is how the gutter shows the time output reached a line.
const timeLayout = "15:04:05"

// gutterCells is how many cells the gutter left of the grid takes: the
// time and the line number, and a cell each side of the line between it
// and the grid; 0 when it shows neither.
func (v *termView) gutterCells() int {
	n := 0
	if v.timestamps {
		n += len(timeLayout)
	}
	if v.lineNumbers {
		if n > 0 {
			n++
		}
		n += v.numberCells()
	}
	if n > 0 {
		n += 2
	}
	return n
}

// numberCells is how many cells the line numbers take: room for the
// newest line's, as last drawn.
func (v *termView) numberCells() int {
	n, d := v.snap.First+v.snap.Offset+v.snap.Rows, 1
	for ; n >= 10; n /= 10 {
		d++
	}
	return max(d, gutterDigits)
}

// gutterW is the gutter's width in DIPs.
func (v *termView) gutterW() float32 { return float32(v.gutterCells()) * v.cellW }

// The scrollbar is drawn in the right padding: thin at rest, wide under
// the pointer, and its thumb never shorter than barMinThumb.
const (
	barThin, barWide = 4, 8
	barMinThumb      = 16
)

// scrollBar returns the scrollbar's strip and thumb in the element's box,
// as last drawn; ok is false without a scrollback (and on the alternate
// screen, which has none).
func (v *termView) scrollBar() (track, thumb ui.Rect, ok bool) {
	s := &v.snap
	if s.Scrollback <= 0 || v.w <= 0 || v.h <= 0 {
		return ui.Rect{}, ui.Rect{}, false
	}
	track = ui.Rect{X: v.w - termPad, W: termPad, H: v.h}
	h := min(max(v.h*float32(s.Rows)/float32(s.Scrollback+s.Rows), barMinThumb), v.h)
	top := (v.h - h) * float32(s.Scrollback-s.Offset) / float32(s.Scrollback)
	return track, ui.Rect{X: track.X, Y: top, W: termPad, H: h}, true
}

// barOffset is the scroll offset that puts the thumb's top at y.
func (v *termView) barOffset(y float32) int {
	_, thumb, ok := v.scrollBar()
	room := v.h - thumb.H
	if !ok || room <= 0 {
		return v.offset
	}
	f := min(max(y/room, 0), 1)
	sb := v.snap.Scrollback
	return sb - int(math.Round(float64(f*float32(sb))))
}

// barPress takes a left press on the scrollbar's strip and starts
// dragging the thumb; a press off the thumb first brings its middle
// there.
func (v *termView) barPress(x, y float32) bool {
	track, thumb, ok := v.scrollBar()
	if !ok || !track.Contains(x, y) {
		return false
	}
	if thumb.Contains(x, y) {
		v.barGrab = y - thumb.Y
	} else {
		v.barGrab = thumb.H / 2
		v.offset = v.barOffset(y - v.barGrab)
	}
	v.barDrag, v.barHover = true, true
	return true
}

// cellAt is the cell under a point in the element's box.
func (v *termView) cellAt(x, y float32) vt.Pos {
	if v.cellW == 0 {
		return vt.Pos{Line: v.first}
	}
	row := int(math.Floor(float64((y - termPad) / v.cellH)))
	row = max(0, min(row, v.rows-1))
	return vt.Pos{Line: v.first + row, Col: v.colAt(x)}
}

// colAt is the column at x in the element's box, within the grid's.
func (v *termView) colAt(x float32) int {
	if v.cellW == 0 {
		return 0
	}
	col := int(math.Floor(float64((x - termPad - v.gutterW()) / v.cellW)))
	return max(0, min(col, v.cols-1))
}

// dragScrollAt is where a selection drag at y scrolls the view: -1 above
// the grid's top margin, 1 below its bottom one, 0 inside; over is how far
// past that margin it is in DIPs.
func (v *termView) dragScrollAt(y float32) (dir int, over float32) {
	if v.cellW == 0 || v.rows <= 0 {
		return 0, 0
	}
	top := termPad + v.cellH*dragMargin
	bottom := termPad + float32(v.rows)*v.cellH - v.cellH*dragMargin
	switch {
	case y < top:
		return -1, top - y
	case y > bottom:
		return 1, y - bottom
	}
	return 0, 0
}

// dragRate is the lines a second a selection drag past the margin scrolls
// the view: slowest at it, fastest dragScrollRamp rows past.
func (v *termView) dragRate() float32 {
	if v.cellH <= 0 || v.rows <= 0 {
		return dragScrollSlow
	}
	f := min(v.dragOver/(dragScrollRamp*v.cellH), 1)
	return dragScrollSlow + (dragScrollFast-dragScrollSlow)*f
}

// autoScroll moves the view on as a selection drag held past its margin
// scrolls it, at the painter's clock, and asks for the frame that moves it
// again.
func (v *termView) autoScroll(p *ui.Painter) {
	if !v.selecting || v.dragScroll == 0 {
		return
	}
	now := p.Now()
	if v.dragAt.IsZero() {
		v.dragAt = now
	}
	// A frame long after the last, as after the window was hid, scrolls no
	// further than dragScrollStep does.
	dt := min(now.Sub(v.dragAt), dragScrollStep)
	v.dragAt = now
	v.dragAcc += float32(dt.Seconds()) * v.dragRate()
	if n := int(v.dragAcc); n > 0 {
		v.dragAcc -= float32(n)
		v.offset = max(v.offset-v.dragScroll*n, 0)
	}
	p.AnimationFrameRate(dragScrollRate)
}

// dragExtend puts the end of a selection drag at the row the view scrolled
// past, at the pointer's column.
func (v *termView) dragExtend() {
	line := v.first
	if v.dragScroll > 0 {
		line = v.first + v.rows - 1
	}
	v.selB = vt.Pos{Line: line, Col: v.colAt(v.dragX)}
	v.selected = true
}

func (v *termView) shape(text string, bold, italic bool) []ui.Glyph {
	return v.shapeBytes([]byte(text), fontIndex(bold, italic))
}

// fontIndex is the index of the font of a style in fonts.
func fontIndex(bold, italic bool) int {
	i := 0
	if bold {
		i |= 1
	}
	if italic {
		i |= 2
	}
	return i
}

// shapeBytes shapes text in font i, the same text once: looking a text
// up does not allocate.
func (v *termView) shapeBytes(text []byte, i int) []ui.Glyph {
	if g, ok := v.shaped[i][string(text)]; ok {
		return g
	}
	if v.nshaped > 4096 {
		v.shaped, v.nshaped = [4]map[string][]ui.Glyph{}, 0
	}
	if v.shaped[i] == nil {
		v.shaped[i] = map[string][]ui.Glyph{}
	}
	g := ui.Shape(string(text), v.fonts[i])
	v.shaped[i][string(text)] = g
	v.nshaped++
	return g
}

// selection returns the selection in order.
func (v *termView) selection() (vt.Pos, vt.Pos, bool) {
	if !v.selected {
		return vt.Pos{}, vt.Pos{}, false
	}
	a, b := v.selA, v.selB
	if b.Less(a) {
		a, b = b, a
	}
	return a, b, true
}

// selectedCols is the columns of line the selection takes, from to to
// inclusive; from > to when it takes none.
func (v *termView) selectedCols(line int) (from, to int) {
	a, b, ok := v.selection()
	if !ok || line < a.Line || line > b.Line {
		return 1, 0
	}
	from, to = 0, math.MaxInt
	if line == a.Line {
		from = a.Col
	}
	if line == b.Line {
		to = b.Col
	}
	return from, to
}

// SelectedText is the text selected, or "".
func (v *termView) SelectedText() string {
	a, b, ok := v.selection()
	if !ok {
		return ""
	}
	return v.target.Terminal().Text(a, b)
}

// findEvery is how often at most the matches are found again as output
// streams in: each time searches the whole scrollback.
const findEvery = 150 * time.Millisecond

// updateFind recomputes the matches of the query as the output changes,
// at most every findEvery for the same query. It returns how long until
// the output that came in since may be searched, 0 for none.
func (v *termView) updateFind(query string, active bool, now time.Time) time.Duration {
	t := v.target.Terminal()
	gen := t.Generation()
	if !active || query == "" {
		v.matches, v.findQuery, v.findActive = nil, "", false
		return 0
	}
	if v.findActive && query == v.findQuery {
		if gen == v.findGen {
			return 0
		}
		if wait := v.findAt.Add(findEvery).Sub(now); wait > 0 {
			return wait
		}
	}
	v.matches = t.Find(query, false)
	v.findGen, v.findQuery, v.findActive, v.findAt = gen, query, true, now
	if v.current >= len(v.matches) {
		v.current = max(len(v.matches)-1, 0)
	}
	return 0
}

// showMatch scrolls match i into view.
func (v *termView) showMatch(i int) {
	if i < 0 || i >= len(v.matches) {
		return
	}
	v.current = i
	v.offset = v.target.Terminal().OffsetFor(v.matches[i].Line, v.offset)
}

// rowMatches marks the cells of the matches on line in cols columns: 1
// for a match, 2 for the current one. It is nil without any.
func (v *termView) rowMatches(line, cols int) []uint8 {
	// The matches are in order, and do not span lines.
	i := sort.Search(len(v.matches), func(i int) bool { return v.matches[i].Line >= line })
	if i == len(v.matches) || v.matches[i].Line != line {
		return nil
	}
	v.marks = slices.Grow(v.marks[:0], cols)[:cols]
	clear(v.marks)
	for ; i < len(v.matches) && v.matches[i].Line == line; i++ {
		m := v.matches[i]
		mark := uint8(1)
		if i == v.current {
			mark = 2
		}
		for x := max(m.Col, 0); x < min(m.Col+m.Len, cols); x++ {
			v.marks[x] = mark
		}
	}
	return v.marks
}

// paint draws the grid, the selection, the matches, the cursor and the
// composition.
func (v *termView) paint(p *ui.Painter, r ui.Rect, pal *theme.Terminal, focused bool) {
	v.metrics(p.Scale())
	gutter := v.gutterCells()
	cols, rows := v.gridSize(r.W, r.H)
	if !v.sized || cols != v.cols || rows != v.rows {
		if v.sized && cols != v.cols {
			// The lines are reflowed: the selection's are not where it was.
			v.selected, v.selecting = false, false
			v.dragScroll = 0
		}
		v.cols, v.rows, v.sized = cols, rows, true
		v.target.Resize(cols, rows)
	}
	t := v.target.Terminal()
	v.autoScroll(p)
	t.SnapshotInto(&v.snap, v.offset)
	snap := &v.snap
	if v.hl != nil && (v.hlFullScreen || !snap.Modes.AltScreen) {
		v.hl.Apply(snap.Lines, v.hlOverride)
	}
	v.offset, v.first = snap.Offset, snap.First
	if v.selecting && v.dragScroll != 0 {
		v.dragExtend()
	}
	v.w, v.h = r.W, r.H
	if !v.clear {
		p.Fill(r, pal.Background, 0)
	}
	s := v.scale
	gx := float32(math.Round(float64((r.X+termPad)*s))) / s
	ox := float32(math.Round(float64((r.X+termPad+float32(gutter)*v.cellW)*s))) / s
	oy := float32(math.Round(float64((r.Y+termPad)*s))) / s
	cw, ch := v.cellW, v.cellH
	if gutter > 0 {
		v.paintGutter(p, r, snap, gx, oy, gutter, pal)
	}

	for y, line := range snap.Lines {
		ry := oy + float32(y)*ch
		abs := snap.First + y
		selFrom, selTo := v.selectedCols(abs)
		marks := v.rowMatches(abs, snap.Cols)
		// backgrounds
		for x := 0; x < len(line.Cells) && x < snap.Cols; x++ {
			c := line.Cells[x]
			_, bg, set := cellColors(c, pal)
			switch {
			case x >= selFrom && x <= selTo:
				bg, set = pal.Selection, true
			case marks != nil && marks[x] == 1:
				bg, set = pal.Match, true
			case marks != nil && marks[x] == 2:
				bg, set = pal.MatchCurrent, true
			}
			if set {
				w := float32(max(int(c.Width), 1))
				p.Fill(ui.Rect{X: ox + float32(x)*cw, Y: ry, W: cw * w, H: ch}, bg, 0)
			}
		}
		// text, in runs of one style whose glyphs go in their cells
		x := 0
		for x < len(line.Cells) && x < snap.Cols {
			c := line.Cells[x]
			if c.Width == 0 || c.Rune == 0 || c.Rune == ' ' {
				x++
				continue
			}
			fg, _, _ := cellColors(c, pal)
			bold, italic := c.Attr&vt.Bold != 0, c.Attr&vt.Italic != 0
			run, colOf := v.run[:0], v.colOf[:0]
			start := x
			for x < len(line.Cells) && x < snap.Cols {
				d := line.Cells[x]
				if d.Width == 0 {
					x++
					continue
				}
				if d.Rune == 0 || d.Rune == ' ' {
					break
				}
				f2, _, _ := cellColors(d, pal)
				if f2 != fg || (d.Attr&vt.Bold != 0) != bold || (d.Attr&vt.Italic != 0) != italic || (d.Attr^c.Attr)&(vt.Underline|vt.Strike) != 0 {
					break
				}
				run = utf8.AppendRune(run, d.Rune)
				colOf = append(colOf, x)
				// what joins the rune is drawn in its cell
				if d.Comb != 0 {
					for _, cr := range d.Comb.String() {
						run = utf8.AppendRune(run, cr)
						colOf = append(colOf, x)
					}
				}
				x++
			}
			v.run, v.colOf = run, colOf
			if len(run) == 0 {
				x = start + 1
				continue
			}
			g := v.shapeBytes(run, fontIndex(bold, italic))
			placed := append(v.placed[:0], g...)
			v.placed = placed
			placeGlyphs(placed, g, colOf, line.Cells, cw)
			p.Glyphs(placed, ox, ry+v.base, fg)
			// decorations
			if d := line.Cells[start].Attr; d&(vt.Underline|vt.Strike) != 0 {
				end := colOf[len(colOf)-1] + 1
				x0, x1 := ox+float32(start)*cw, ox+float32(end)*cw
				th := max(1/s, 1)
				if d&vt.Underline != 0 {
					p.Fill(ui.Rect{X: x0, Y: ry + v.base + 2/s, W: x1 - x0, H: th}, fg, 0)
				}
				if d&vt.Strike != 0 {
					p.Fill(ui.Rect{X: x0, Y: ry + v.base - v.fontSize*0.3, W: x1 - x0, H: th}, fg, 0)
				}
			}
		}
		// the link under the pointer, underlined
		if from, to := v.linkCols(abs, snap.Cols); from <= to {
			x0, x1 := ox+float32(from)*cw, ox+float32(to+1)*cw
			p.Fill(ui.Rect{X: x0, Y: ry + v.base + 2/s, W: x1 - x0, H: max(1/s, 1)}, pal.Foreground, 0)
		}
	}

	// cursor
	if v.showCursor(p, focused) {
		cx := ox + float32(min(snap.CursorX, snap.Cols-1))*cw
		cy := oy + float32(snap.CursorY)*ch
		w := cw
		var under vt.Cell
		if line := snap.Lines[snap.CursorY]; snap.CursorX < len(line.Cells) {
			under = line.Cells[snap.CursorX]
			if under.Width == 2 {
				w = 2 * cw
			}
		}
		cur := ui.Rect{X: cx, Y: cy, W: w, H: ch}
		style := v.cursorStyle
		if snap.CursorStyleSet {
			style = snap.CursorStyle
		}
		switch {
		case !focused:
			p.Stroke(cur, pal.Cursor, 0, 1/s)
		case style == vt.CursorUnderline:
			p.Fill(ui.Rect{X: cx, Y: cy + ch - 2, W: w, H: 2}, pal.Cursor, 0)
		case style == vt.CursorBar:
			p.Fill(ui.Rect{X: cx, Y: cy, W: max(2/s, 1.5), H: ch}, pal.Cursor, 0)
		default:
			p.Fill(cur, pal.Cursor, 0)
			if under.Rune > ' ' {
				g := v.shape(under.Text(), under.Attr&vt.Bold != 0, under.Attr&vt.Italic != 0)
				placed := make([]ui.Glyph, len(g))
				copy(placed, g)
				shift := float32(0)
				for i := range placed {
					if i > 0 && g[i].Cluster == g[i-1].Cluster {
						placed[i].X = g[i].X + shift // a mark on the base
						continue
					}
					placed[i].X = max((w-placed[i].Advance)/2, 0)
					shift = placed[i].X - g[i].X
				}
				p.Glyphs(placed, cx, cy+v.base, pal.CursorText)
			}
		}
	}

	// the input method's composition, at the cursor
	if v.preedit != "" && snap.CursorY >= 0 && snap.CursorY < snap.Rows {
		cx := ox + float32(snap.CursorX)*cw
		cy := oy + float32(snap.CursorY)*ch
		g := v.shape(v.preedit, false, false)
		w := float32(0)
		for _, gl := range g {
			w = max(w, gl.X+gl.Advance)
		}
		p.Fill(ui.Rect{X: cx, Y: cy, W: w, H: ch}, pal.Background, 0)
		p.Glyphs(g, cx, cy+v.base, pal.Foreground)
		p.Fill(ui.Rect{X: cx, Y: cy + ch - 1, W: w, H: 1}, pal.Foreground, 0)
	}

	// where the view is in the scrollback: shown scrolled up, or with the
	// pointer on it
	if _, thumb, ok := v.scrollBar(); ok && (snap.Offset > 0 || v.barHover || v.barDrag) {
		w, alpha := float32(barThin), float32(0.35)
		if v.barHover || v.barDrag {
			w, alpha = barWide, 0.5
		}
		p.Fill(ui.Rect{X: r.X + r.W - 2 - w, Y: r.Y + thumb.Y, W: w, H: thumb.H}, pal.Foreground.Alpha(alpha), w/2)
	}
}

// paintGutter draws, left of each row of the grid, the time output
// reached it and its line number, in cells columns from x, and a line
// between them and the grid. A row output never reached has neither,
// unless it is above the cursor: those are numbered.
func (v *termView) paintGutter(p *ui.Painter, r ui.Rect, snap *vt.Snapshot, x, y float32, cells int, pal *theme.Terminal) {
	cw, ch, s := v.cellW, v.cellH, v.scale
	numAt := 0
	if v.timestamps {
		numAt = len(timeLayout) + 1
	}
	numEnd := cells - 2
	cursor := snap.First + snap.CursorY
	for row, line := range snap.Lines {
		ry := y + float32(row)*ch + v.base
		if v.timestamps && line.Time != 0 {
			v.label = time.Unix(line.Time, 0).AppendFormat(v.label[:0], timeLayout)
			v.gutterText(p, v.label, x, ry, pal.Foreground.Alpha(0.4))
		}
		if abs := snap.First + row; v.lineNumbers && (line.Time != 0 || abs <= cursor) {
			v.label = strconv.AppendInt(v.label[:0], int64(abs+1), 10)
			v.gutterText(p, v.label, x+float32(max(numEnd-len(v.label), numAt))*cw, ry, pal.Foreground.Alpha(0.55))
		}
	}
	lx := float32(math.Round(float64((x+float32(cells-1)*cw)*s))) / s
	p.Fill(ui.Rect{X: lx, Y: r.Y, W: max(1/s, 1), H: r.H}, pal.Foreground.Alpha(0.12), 0)
}

// gutterText draws text, ASCII, one character a cell from x, its
// baseline at y.
func (v *termView) gutterText(p *ui.Painter, text []byte, x, y float32, c ui.Color) {
	placed := v.placed[:0]
	for i := range text {
		for _, g := range v.shapeBytes(text[i:i+1], 0) {
			g.X = float32(i)*v.cellW + max((v.cellW-g.Advance)/2, 0)
			placed = append(placed, g)
		}
	}
	v.placed = placed
	p.Glyphs(placed, x, y, c)
}

// placeGlyphs puts the glyphs g of a run of cells in their cells:
// colOf is the column of each rune of the run. Each cluster is centered
// in its cell(s), as monospace fallback fonts are often narrower or
// wider; the glyphs after the first of a cluster (combining marks) keep
// their place on it.
func placeGlyphs(placed, g []ui.Glyph, colOf []int, cells []vt.Cell, cw float32) {
	shift := float32(0)
	for i := range placed {
		ci := placed[i].Cluster
		if ci < 0 || ci >= len(colOf) {
			continue
		}
		if i > 0 && g[i].Cluster == g[i-1].Cluster {
			placed[i].X = g[i].X + shift
			continue
		}
		col := colOf[ci]
		cellsW := cw * float32(max(int(cells[col].Width), 1))
		dx := (cellsW - placed[i].Advance) / 2
		// A ligature over several cells starts at the first.
		if end := ci + max(placed[i].Runes, 1) - 1; end < len(colOf) && colOf[end] != col || placed[i].Advance > cellsW {
			dx = 0
		}
		placed[i].X = float32(col)*cw + max(dx, 0)
		shift = placed[i].X - g[i].X
	}
}

// caret is the cursor's cell in the element's box, where the input
// method's window goes.
func (v *termView) caret() ui.Rect {
	x, y := v.target.Terminal().Cursor(v.offset)
	return ui.Rect{X: termPad + v.gutterW() + float32(x)*v.cellW, Y: termPad + float32(y)*v.cellH, W: max(v.cellW, 1), H: max(v.cellH, 1)}
}

func (v *termView) send(b []byte) {
	if len(b) == 0 {
		return
	}
	v.blink.at = time.Time{}
	v.offset = 0
	v.selected = false
	v.dragScroll = 0
	if _, err := v.target.Write(b); errors.Is(err, session.ErrInputBusy) && v.inputError != nil {
		v.inputError(errSnippetBusy)
	}
}

// reports says whether the program gets the mouse: it asked, the view is
// at the bottom, and the user is not holding the key that keeps the mouse
// for selecting text (Shift, or ⌥ too on macOS).
func (v *termView) reports(m vt.Modes, mods ui.Modifiers) bool {
	if m.Mouse == vt.MouseOff || v.offset != 0 {
		return false
	}
	if mods&ui.Shift != 0 || runtime.GOOS == "darwin" && mods&ui.Alt != 0 {
		return false
	}
	return true
}

// report sends a mouse event at cell p of the view to the program.
func (v *termView) report(m vt.Modes, btn vt.MouseBtn, act vt.MouseAction, p vt.Pos, mods ui.Modifiers) {
	ev := vt.MouseEvent{Button: btn, Action: act, Col: p.Col, Row: p.Line - v.first,
		Shift: mods&ui.Shift != 0, Alt: mods&ui.Alt != 0 && runtime.GOOS != "darwin", Ctrl: mods&ui.Ctrl != 0}
	if b := vt.EncodeMouse(m, ev); b != nil {
		v.target.Write(b)
	}
}

// moveCursorTo moves the shell's cursor to the cell p, as ⌥-click
// (Alt-click elsewhere) does: the arrows that take it there along the
// line it is on. Only on the main screen of a program that does not read
// the mouse, and only within the cursor's line, lines that wrapped
// counting as one; up and down would page through a shell's history.
func (v *termView) moveCursorTo(p vt.Pos) {
	snap := v.target.Terminal().Snapshot(0)
	if snap.Modes.AltScreen || snap.Modes.Mouse != vt.MouseOff {
		return
	}
	if steps, ok := snap.CursorSteps(p.Line-snap.First, p.Col); ok {
		v.send(cursorMoveBytes(steps, snap.Modes))
	}
}

// cursorMoveBytes are the arrows that move the cursor steps characters
// right, left when negative, in the cursor key mode of m (DECCKM).
func cursorMoveBytes(steps int, m vt.Modes) []byte {
	k := ui.KeyRight
	if steps < 0 {
		k = ui.KeyLeft
	}
	b, _ := EncodeKey(k, 0, m)
	return bytes.Repeat(b, abs(steps))
}

// focusChanged reports the focus coming to or leaving the pane to a
// program that asked for it (1004).
func (v *termView) focusChanged(focused bool) {
	seen := int8(-1)
	if focused {
		seen = 1
	}
	if v.focusSeen == seen {
		return
	}
	first := v.focusSeen == 0
	v.focusSeen = seen
	v.blink.at = time.Time{}
	if first {
		return
	}
	if b := vt.EncodeFocus(v.target.Terminal().Modes(), focused); b != nil {
		v.target.Write(b)
	}
}

// Paste sends text as pasted.
func (v *termView) Paste(text string) {
	if text == "" || v.holdPaste != nil && v.holdPaste(text) {
		return
	}
	v.pasteNow(text)
}

// pasteClipboard pastes the clipboard's text. A local terminal given a
// clipboard with an image and no text sends Ctrl+V instead, as the key
// a program such as Codex CLI or Claude Code takes to read the image
// from the system clipboard itself; it goes to this terminal alone. A
// remote one has nothing to send: its programs cannot read this
// computer's clipboard.
func (v *termView) pasteClipboard(cb clipboard) {
	text := cb.ReadClipboard()
	if text == "" && v.local && clipboardHasImage(cb) {
		v.send([]byte{0x16})
		return
	}
	v.Paste(text)
}

// clipboardHasImage reports an image on the clipboard, which only a
// clipboard that lists its formats (ui.Services) can tell.
func clipboardHasImage(cb clipboard) bool {
	f, ok := cb.(interface{ ClipboardFormats() []transfer.Format })
	return ok && slices.Contains(f.ClipboardFormats(), transfer.PNG)
}

// pasteNow sends text as pasted, without holding it.
func (v *termView) pasteNow(text string) {
	v.send(pasteBytes(text, v.target.Terminal().Modes()))
	v.fan(func(m vt.Modes) []byte { return pasteBytes(text, m) })
}

// fan sends to the other terminals what encode gives for each, while
// what is typed goes to several.
func (v *termView) fan(encode func(vt.Modes) []byte) {
	if v.fanout != nil {
		v.fanout(encode)
	}
}

// onlyControl reports text made of control characters, which keys the
// view encoded itself already sent.
func onlyControl(s string) bool {
	for _, r := range s {
		if r >= ' ' && r != 0x7f {
			return false
		}
	}
	return true
}

// clipboard is the window's clipboard, through ui.Services.
type clipboard interface {
	ReadClipboard() string
	WriteClipboard(string)
}

// input handles an event; it reports whether it took it.
func (v *termView) input(ev ui.InputEvent, cb clipboard) bool {
	t := v.target.Terminal()
	switch ev.Kind {
	case ui.InputKeyDown:
		v.swallow = false
		mods := ev.Mods
		if isAppShortcut(v.shortcuts, ev.Key, mods) {
			return false
		}
		// Ctrl+Shift+C and V copy and paste on Linux and Windows, where
		// Ctrl+C is the interrupt.
		if runtime.GOOS != "darwin" && mods&(ui.Ctrl|ui.Shift) == ui.Ctrl|ui.Shift && mods&ui.Alt == 0 {
			switch ev.Key {
			case ui.KeyC:
				if s := v.SelectedText(); s != "" {
					cb.WriteClipboard(s)
				}
				v.swallow = true
				return true
			case ui.KeyV:
				v.pasteClipboard(cb)
				v.swallow = true
				return true
			}
		}
		if mods == ui.Shift {
			switch ev.Key {
			case ui.KeyPageUp:
				v.offset += max(v.rows-1, 1)
				return true
			case ui.KeyPageDown:
				v.offset = max(v.offset-max(v.rows-1, 1), 0)
				return true
			case ui.KeyInsert:
				v.pasteClipboard(cb)
				return true
			}
		}
		if b, ok := EncodeKey(ev.Key, mods, t.Modes()); ok {
			v.send(b)
			key := ev.Key
			v.fan(func(m vt.Modes) []byte {
				b, _ := EncodeKey(key, mods, m)
				return b
			})
			v.swallow = mods&(ui.Ctrl|ui.Alt) != 0 && typesText(ev.Key)
			return true
		}
		if mods&ui.Super != 0 {
			return false // the app's shortcuts
		}
		return typesText(ev.Key)
	case ui.InputKeyUp:
		v.swallow = false
		return true
	case ui.InputText:
		if v.swallow {
			v.swallow = false
			return true
		}
		v.preedit = ""
		if ev.Text == "" || onlyControl(ev.Text) {
			return true
		}
		b := []byte(strings.ReplaceAll(ev.Text, "\n", "\r"))
		v.send(b)
		v.fan(func(vt.Modes) []byte { return b })
		return true
	case ui.InputCompose:
		v.blink.at = time.Time{}
		v.preedit = ev.Text
		return true
	case ui.InputCommand:
		switch ev.Text {
		case "copy", "cut":
			if s := v.SelectedText(); s != "" {
				cb.WriteClipboard(s)
			}
			return true
		case "paste", "pasteAndMatchStyle":
			v.pasteClipboard(cb)
			return true
		case "selectAll":
			snap := t.Snapshot(0)
			v.selA = vt.Pos{Line: snap.First - snap.Scrollback, Col: 0}
			v.selB = vt.Pos{Line: snap.First + snap.Rows - 1, Col: snap.Cols - 1}
			v.selected = true
			return true
		}
		return false
	case ui.InputPointerDown:
		if ev.Button == 0 && v.barPress(ev.X, ev.Y) {
			return true
		}
		if ev.Button == 0 && ev.Clicks <= 1 && ev.Mods&linkMods() != 0 && v.openLink != nil {
			if url, _, _ := t.LinkAt(v.cellAt(ev.X, ev.Y)); url != "" && ui.SafeToOpen(url) {
				v.openLink(url)
				return true
			}
		}
		if m := t.Modes(); v.reports(m, ev.Mods) {
			btn := vt.MouseLeft
			switch ev.Button {
			case 1:
				btn = vt.MouseRight
			case 2:
				btn = vt.MouseMiddle
			}
			p := v.cellAt(ev.X, ev.Y)
			v.held, v.heldBtn, v.lastMouse = true, btn, p
			v.report(m, btn, vt.MousePress, p, ev.Mods)
			return true
		}
		if ev.Button == 1 {
			return v.rightPress(ev.Mods, cb)
		}
		if ev.Button == 2 {
			v.pasteClipboard(cb)
			return true
		}
		p := v.cellAt(ev.X, ev.Y)
		v.altClick = ev.Button == 0 && ev.Clicks == 1 && ev.Mods&(ui.Alt|ui.Shift|ui.Ctrl|ui.Super) == ui.Alt
		switch {
		case ev.Clicks == 2:
			v.selA, v.selB = t.WordAt(p)
			v.selected = true
		case ev.Clicks >= 3:
			v.selA, v.selB = vt.Pos{Line: p.Line}, vt.Pos{Line: p.Line, Col: max(v.cols-1, 0)}
			v.selected = true
		case ev.Mods&ui.Shift != 0 && v.selected:
			v.selB = p
		default:
			v.selA, v.selB = p, p
			v.selected = false
		}
		v.selecting = true
		v.dragScroll, v.dragAcc, v.dragAt = 0, 0, time.Time{}
		return true
	case ui.InputPointerMove:
		if v.barDrag {
			v.offset = v.barOffset(ev.Y - v.barGrab)
			return true
		}
		track, _, ok := v.scrollBar()
		hover := ok && track.Contains(ev.X, ev.Y)
		if hover != v.barHover && !v.selecting && !v.held {
			v.barHover = hover
			return true
		}
		if v.barHover {
			return true
		}
		v.hoverLink(t, ev.X, ev.Y, ev.Mods)
		if v.held || !v.selecting {
			m := t.Modes()
			btn := vt.MouseNone
			if v.held {
				btn = v.heldBtn
			}
			if p := v.cellAt(ev.X, ev.Y); (v.held || v.reports(m, ev.Mods)) && m.Mouse != vt.MouseOff {
				if p != v.lastMouse {
					v.lastMouse = p
					v.report(m, btn, vt.MouseMotion, p, ev.Mods)
				}
				return v.held || m.Mouse == vt.MouseAny
			}
		}
		if v.selecting && ev.Button == 0 {
			v.dragX = ev.X
			dir, over := v.dragScrollAt(ev.Y)
			switch {
			case dir != v.dragScroll:
				v.dragScroll, v.dragOver = dir, over
				v.dragAt, v.dragAcc = ev.Time, 0
				if dir != 0 {
					// A line at once, the frames after at its rate.
					v.dragAcc = 1
				}
			case dir != 0:
				v.dragOver = over
			}
			if v.dragScroll != 0 {
				// The frame that scrolls puts the end on the row it goes by.
				v.selected = true
				return true
			}
			p := v.cellAt(ev.X, ev.Y)
			if p != v.selB || p != v.selA {
				v.selB = p
				v.selected = v.selA != v.selB || v.selected
			}
			return true
		}
		return false
	case ui.InputPointerUp:
		if v.barDrag {
			v.barDrag = false
			track, _, ok := v.scrollBar()
			v.barHover = ok && track.Contains(ev.X, ev.Y)
			return true
		}
		if v.held {
			v.held = false
			m := t.Modes()
			v.report(m, v.heldBtn, vt.MouseRelease, v.cellAt(ev.X, ev.Y), ev.Mods)
			return true
		}
		if v.selecting {
			v.selecting = false
			v.dragScroll, v.dragAcc, v.dragAt = 0, 0, time.Time{}
			if v.altClick && !v.selected {
				v.moveCursorTo(v.selA)
			}
			v.altClick = false
			if v.copyOnSelect && v.selected {
				if s := v.SelectedText(); s != "" {
					cb.WriteClipboard(s)
				}
			}
			return true
		}
		return false
	case ui.InputScroll:
		if v.cellH == 0 {
			return false
		}
		if ev.Mods&fontMods() != 0 && v.zoomFont != nil {
			v.wheelFont(ev)
			return true
		}
		if m := t.Modes(); v.reports(m, ev.Mods) {
			// One report a notch, or three lines of a precise scroll.
			n := 0
			if ev.Precise {
				v.wheelAcc += ev.DY
				n = int(v.wheelAcc / (3 * v.cellH))
				v.wheelAcc -= float32(n) * 3 * v.cellH
			} else if ev.DY > 0 {
				n = 1
			} else if ev.DY < 0 {
				n = -1
			}
			btn := vt.MouseWheelDown
			if n < 0 {
				btn = vt.MouseWheelUp
			}
			p := v.cellAt(ev.X, ev.Y)
			for range abs(n) {
				v.report(m, btn, vt.MousePress, p, ev.Mods)
			}
			return true
		}
		lines := 0
		if ev.Precise {
			v.scrollAcc += ev.DY
			lines = int(v.scrollAcc / v.cellH)
			v.scrollAcc -= float32(lines) * v.cellH
		} else if ev.DY > 0 {
			lines = 3
		} else if ev.DY < 0 {
			lines = -3
		}
		if lines == 0 {
			return true
		}
		m := t.Modes()
		if m.AltScreen {
			// Full-screen programs scroll with the arrows, as xterm's
			// alternateScroll.
			k := ui.KeyDown
			if lines < 0 {
				k = ui.KeyUp
			}
			b, _ := EncodeKey(k, 0, m)
			for range abs(lines) {
				v.target.Write(b)
			}
			return true
		}
		v.offset = max(v.offset-lines, 0)
		return true
	case ui.InputZoom:
		if v.zoomFont == nil || ev.Scale <= 0 {
			return false
		}
		v.pinchAcc += float32(math.Log(float64(ev.Scale)))
		if n := int(v.pinchAcc / fontPinchStep); n != 0 {
			v.pinchAcc -= float32(n) * fontPinchStep
			v.zoomFont(n)
		}
		return true
	}
	return false
}

// fontMods are the modifiers the wheel changes the terminal's font size
// with: Ctrl, or ⌘ too on macOS.
func fontMods() ui.Modifiers { return ui.Ctrl | linkMods() }

// How far a touchpad scrolls (DIPs) or pinches (the log of its scale)
// for the font to grow or shrink by a point.
const (
	fontScrollStep = 40
	fontPinchStep  = 0.08
)

// wheelFont makes the font a point larger for each notch the wheel turns
// up, smaller down, or for each fontScrollStep a touchpad scrolls.
func (v *termView) wheelFont(ev ui.InputEvent) {
	n := 0
	switch {
	case ev.Precise && ev.Momentum:
		// The glide after the fingers leave would keep on changing it.
	case ev.Precise:
		v.zoomAcc -= ev.DY
		n = int(v.zoomAcc / fontScrollStep)
		v.zoomAcc -= float32(n) * fontScrollStep
	case ev.DY < 0:
		n = 1
	case ev.DY > 0:
		n = -1
	}
	if n != 0 {
		v.zoomFont(n)
	}
}

// linkSpan is the cells a link spans, when on.
type linkSpan struct {
	a, b vt.Pos
	on   bool
}

// linkMods are the modifiers a click opens a link with: ⌘ on macOS,
// Ctrl elsewhere.
func linkMods() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}

// hoverLink keeps the link under the pointer at (x, y) while mods hold
// linkMods and no button is down.
func (v *termView) hoverLink(t *vt.Terminal, x, y float32, mods ui.Modifiers) {
	v.link.on = false
	if mods&linkMods() == 0 || v.held || v.selecting || v.openLink == nil {
		return
	}
	if url, a, b := t.LinkAt(v.cellAt(x, y)); url != "" && ui.SafeToOpen(url) {
		v.link = linkSpan{a, b, true}
	}
}

// linkCols are the columns of line the hovered link spans, from > to for
// none.
func (v *termView) linkCols(line, cols int) (from, to int) {
	if !v.link.on || line < v.link.a.Line || line > v.link.b.Line {
		return 1, 0
	}
	from, to = 0, cols-1
	if line == v.link.a.Line {
		from = v.link.a.Col
	}
	if line == v.link.b.Line {
		to = v.link.b.Col
	}
	return from, to
}

// rightPress handles a right press no program reads, as rightClick
// says; it reports false to leave it to the context menu, as always
// with Shift.
func (v *termView) rightPress(mods ui.Modifiers, cb clipboard) bool {
	if mods&ui.Shift != 0 {
		return false
	}
	switch v.rightClick {
	case settings.RightClickPaste:
		v.pasteClipboard(cb)
		return true
	case settings.RightClickCopyPaste:
		if s := v.SelectedText(); v.selected && s != "" {
			cb.WriteClipboard(s)
			v.selected = false
		} else {
			v.pasteClipboard(cb)
		}
		return true
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
