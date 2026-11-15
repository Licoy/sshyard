package vt

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// decGraphics maps the DEC special graphics set (ESC ( 0), which programs
// use for line drawing, to Unicode.
var decGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'f': '°', 'g': '±', 'j': '┘', 'k': '┐', 'l': '┌',
	'm': '└', 'n': '┼', 'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│', 'y': '≤', 'z': '≥',
	'{': 'π', '|': '≠', '}': '£', '~': '·',
}

var widthCond = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = false
	return c
}()

// RuneWidth is the number of cells r takes: 0, 1 or 2.
func RuneWidth(r rune) int {
	if r < 0x20 {
		return 0
	}
	if r < 0x7f {
		return 1
	}
	return widthCond.RuneWidth(r)
}

func (t *Terminal) print(r rune) {
	if t.onlyOSC {
		return
	}
	if (t.shiftOut && t.g1Graphics) || (!t.shiftOut && t.g0Graphics) {
		if g, ok := decGraphics[r]; ok {
			r = g
		}
	}
	joins := t.joinNext
	t.joinNext = false
	w := RuneWidth(r)
	if isRegional(r) && t.pairFlag(r) {
		return
	}
	if w == 0 || joins || isModifier(r) {
		if isJoining(r) || joins || isModifier(r) {
			if t.join(r) {
				t.joinNext = r == zwj
				return
			}
		}
		if w == 0 {
			// Other zero-width characters are not drawn.
			return
		}
	}
	ls := t.lines()
	if t.cur.wrapPending {
		if t.modes.AutoWrap {
			ls[t.cur.y].Wrapped, ls[t.cur.y].widePad = true, false
			t.cur.x = 0
			t.index()
		}
		t.cur.wrapPending = false
	}
	if w == 2 && t.cur.x == t.cols-1 {
		if t.modes.AutoWrap {
			t.clearCell(t.cur.y, t.cur.x)
			ls[t.cur.y].Wrapped, ls[t.cur.y].widePad = true, true
			t.cur.x = 0
			t.index()
		} else {
			w = 1
			r = ' '
		}
	}
	if t.cols < 2 {
		w = 1
	}
	t.stamp()
	line := ls[t.cur.y].Cells
	if t.modes.Insert {
		copy(line[t.cur.x+w:], line[t.cur.x:])
	}
	t.clearCell(t.cur.y, t.cur.x)
	if w == 2 {
		t.clearCell(t.cur.y, t.cur.x+1)
	}
	pen := t.cur.pen
	line[t.cur.x] = Cell{Rune: r, Width: uint8(w), FG: pen.FG, BG: pen.BG, Attr: pen.Attr}
	if w == 2 {
		line[t.cur.x+1] = Cell{Width: 0, FG: pen.FG, BG: pen.BG, Attr: pen.Attr}
	}
	t.lastRune = r
	t.cur.x += w
	if t.cur.x >= t.cols {
		t.cur.x = t.cols - 1
		t.cur.wrapPending = true
	}
}

const zwj = 0x200d

// isJoining reports a rune that goes in the cell of the one before it:
// a combining mark, a variation selector, ZWJ.
func isJoining(r rune) bool {
	switch {
	case r == zwj, r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return true
	case r >= 0xe0020 && r <= 0xe007f: // emoji tag sequences
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc)
}

// isModifier reports an emoji skin tone modifier, which joins the emoji
// before it.
func isModifier(r rune) bool { return r >= 0x1f3fb && r <= 0x1f3ff }

// isRegional reports a regional indicator; two in a row make a flag.
func isRegional(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }

// pairFlag makes r and the regional indicator just before it one flag:
// a wide cell holding both, so it takes the two columns the program
// counts for the two letters. A lone indicator stays one column.
func (t *Terminal) pairFlag(r rune) bool {
	x := t.cur.x - 1
	if t.cur.wrapPending || x < 0 {
		return false
	}
	line := t.lines()[t.cur.y].Cells
	base := line[x]
	if base.Width != 1 || !isRegional(base.Rune) || base.Comb != 0 {
		return false
	}
	t.clearCell(t.cur.y, t.cur.x)
	base.Comb, base.Width = CombOf(string(r)), 2
	line[x] = base
	line[t.cur.x] = Cell{Width: 0, FG: base.FG, BG: base.BG, Attr: base.Attr}
	t.lastRune = r
	t.cur.x++
	if t.cur.x >= t.cols {
		t.cur.x = t.cols - 1
		t.cur.wrapPending = true
	}
	return true
}

// join adds r to the cell last printed, if there is one: the cell
// before the cursor, or under it when a wrap is pending.
func (t *Terminal) join(r rune) bool {
	x := t.cur.x - 1
	if t.cur.wrapPending {
		x = t.cur.x
	}
	line := t.lines()[t.cur.y].Cells
	if x >= 0 && x < len(line) && line[x].Width == 0 && x > 0 {
		x-- // the second half of a wide character
	}
	if x < 0 || x >= len(line) || line[x].Width == 0 || line[x].Rune == 0 {
		return false
	}
	if isModifier(r) && RuneWidth(line[x].Rune) < 2 && line[x].Comb == 0 {
		return false // a modifier alone, after a non-emoji
	}
	comb := line[x].Comb.String()
	if len(comb) >= 32 {
		return true // bounded: drop the rest
	}
	if id := CombOf(comb + string(r)); id != 0 {
		line[x].Comb = id
	}
	return true
}

// clearCell blanks the cell at x of row y, and the other half of a wide
// character it belongs to.
func (t *Terminal) clearCell(y, x int) {
	if x < 0 || x >= t.cols {
		return
	}
	line := t.lines()[y].Cells
	switch line[x].Width {
	case 0:
		if x > 0 {
			line[x-1] = blank(Cell{BG: line[x-1].BG})
		}
	case 2:
		if x+1 < t.cols {
			line[x+1] = blank(Cell{BG: line[x+1].BG})
		}
	}
	line[x] = blank(Cell{BG: line[x].BG})
}

func (t *Terminal) tab(n int) {
	for ; n > 0 && t.cur.x < t.cols-1; n-- {
		t.cur.x++
		for t.cur.x < t.cols-1 && !t.tabs[t.cur.x] {
			t.cur.x++
		}
	}
	for ; n < 0 && t.cur.x > 0; n++ {
		t.cur.x--
		for t.cur.x > 0 && !t.tabs[t.cur.x] {
			t.cur.x--
		}
	}
	t.cur.wrapPending = false
}

// index moves the cursor down a line, scrolling at the bottom margin.
func (t *Terminal) index() {
	t.leaveLine()
	// An empty line is stamped as it is left: printing stamps the others.
	t.stamp()
	t.cur.wrapPending = false
	if t.cur.y == t.bot {
		t.scrollUp(1)
	} else if t.cur.y < t.rows-1 {
		t.cur.y++
	}
}

func (t *Terminal) reverseIndex() {
	t.cur.wrapPending = false
	if t.cur.y == t.top {
		t.scrollDown(1)
	} else if t.cur.y > 0 {
		t.cur.y--
	}
}

// scrollUp scrolls the scroll region up n lines; on the primary screen
// with the region at the top, the lines go to the scrollback.
func (t *Terminal) scrollUp(n int) {
	n = min(n, t.bot-t.top+1)
	if n <= 0 {
		return
	}
	ls := t.lines()
	if !t.altActive && t.top == 0 && t.maxScrollback > 0 {
		for _, l := range ls[:n] {
			if t.scrollback.push(compact(l), t.maxScrollback) {
				t.dropped++
			}
		}
	}
	// The lines that went come back blank at the bottom, in their cells:
	// the scrollback has copies.
	rotate(ls[t.top:t.bot+1], n)
	for i := t.bot - n + 1; i <= t.bot; i++ {
		ls[i] = blankLine(ls[i], t.cols, t.cur.pen)
	}
}

func (t *Terminal) scrollDown(n int) {
	n = min(n, t.bot-t.top+1)
	if n <= 0 {
		return
	}
	ls := t.lines()
	rotate(ls[t.top:t.bot+1], len(ls[t.top:t.bot+1])-n)
	for i := t.top; i < t.top+n; i++ {
		ls[i] = blankLine(ls[i], t.cols, t.cur.pen)
	}
}

// rotate moves the first n lines of ls to its end, in place.
func rotate(ls []Line, n int) {
	slices.Reverse(ls[:n])
	slices.Reverse(ls[n:])
	slices.Reverse(ls)
}

// blankLine is newLine in l's cells, when it has as many.
func blankLine(l Line, cols int, pen Cell) Line {
	if len(l.Cells) != cols {
		return newLine(cols, pen)
	}
	b := blank(pen)
	for i := range l.Cells {
		l.Cells[i] = b
	}
	return Line{Cells: l.Cells}
}

func (t *Terminal) saveCursor() { t.saved = t.cur }

func (t *Terminal) restoreCursor() {
	t.cur = t.saved
	t.cur.x = min(t.cur.x, t.cols-1)
	t.cur.y = min(t.cur.y, t.rows-1)
}

func (t *Terminal) oscDone() {
	t.state = sGround
	if t.osc52 && t.oscScan.dropped() {
		big := t.oscScan.big
		t.osc52, t.oscScan = false, osc52Scan{}
		t.osc = nil
		if big && t.OnClipboardDrop != nil {
			t.OnClipboardDrop()
		}
		return
	}
	t.osc52 = false
	s := string(t.osc)
	num, rest, _ := strings.Cut(s, ";")
	// A tmux passthrough may carry only a clipboard write or a title.
	if t.onlyOSC && num != "0" && num != "2" && num != "52" {
		return
	}
	switch num {
	case "0", "2":
		t.title = rest
	case "52":
		t.osc52Done(rest)
	case "7":
		if h, d := cwdURL(rest); d != "" {
			t.cwd, t.cwdHost = d, h
			t.cwdReports++
		}
	case "1337":
		if d, ok := strings.CutPrefix(rest, "CurrentDir="); ok && strings.HasPrefix(d, "/") {
			t.cwd, t.cwdHost = d, t.remoteHost
			t.cwdReports++
		} else if h, ok := strings.CutPrefix(rest, "RemoteHost="); ok {
			// user@host: the host CurrentDir reports are on.
			if i := strings.LastIndexByte(h, '@'); i >= 0 {
				h = h[i+1:]
			}
			t.remoteHost = h
		}
	}
}

// cwdURL is the host and path of an OSC 7 report, file://host/path (the
// path percent-encoded, though shells often send it raw), or "", "".
func cwdURL(s string) (host, dir string) {
	rest, ok := strings.CutPrefix(s, "file://")
	if !ok {
		return "", ""
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", ""
	}
	host, p := rest[:i], rest[i:]
	if d, err := url.PathUnescape(p); err == nil {
		p = d
	}
	if h, err := url.PathUnescape(host); err == nil {
		host = h
	}
	return host, p
}

func (t *Terminal) param(i, def int) int {
	// Sub-parameters do not count as parameters.
	k := -1
	for j := range t.params {
		if !t.sub[j] {
			k++
		}
		if k == i {
			if t.params[j] <= 0 {
				return def
			}
			return t.params[j]
		}
	}
	return def
}

func (t *Terminal) reply(s string) {
	if t.Reply != nil {
		t.Reply([]byte(s))
	}
}

func (t *Terminal) moveTo(x, y int) {
	minY, maxY := 0, t.rows-1
	if t.cur.origin {
		minY, maxY = t.top, t.bot
		y += t.top
	}
	t.cur.x = max(0, min(x, t.cols-1))
	t.cur.y = max(minY, min(y, maxY))
	t.cur.wrapPending = false
}

func (t *Terminal) csi(final byte) {
	if t.onlyOSC {
		return
	}
	if len(t.inter) > 0 {
		switch {
		case t.inter[0] == ' ' && final == 'q':
			param := t.param(0, 1)
			switch param {
			case 1, 2:
				t.style, t.styleSet = CursorBlock, true
			case 3, 4:
				t.style, t.styleSet = CursorUnderline, true
			case 5, 6:
				t.style, t.styleSet = CursorBar, true
			}
			if param >= 1 && param <= 6 {
				t.cursorBlink = param%2 == 1
			}
		case t.inter[0] == '!' && final == 'p':
			t.softReset()
		}
		return
	}
	if t.private == '?' {
		switch final {
		case 'h':
			t.setPrivateModes(true)
		case 'l':
			t.setPrivateModes(false)
		}
		return
	}
	if t.private != 0 {
		if t.private == '>' && final == 'c' {
			t.reply("\x1b[>0;10;1c")
		}
		return
	}
	n := t.param(0, 1)
	ls := t.lines()
	switch final {
	case '@':
		line := ls[t.cur.y].Cells
		n = min(n, t.cols-t.cur.x)
		copy(line[t.cur.x+n:], line[t.cur.x:])
		for i := t.cur.x; i < t.cur.x+n; i++ {
			line[i] = blank(t.cur.pen)
		}
	case 'A':
		top := 0
		if t.cur.y >= t.top {
			top = t.top
		}
		t.cur.y = max(top, t.cur.y-n)
		t.cur.wrapPending = false
	case 'B', 'e':
		bot := t.rows - 1
		if t.cur.y <= t.bot {
			bot = t.bot
		}
		t.cur.y = min(bot, t.cur.y+n)
		t.cur.wrapPending = false
	case 'C', 'a':
		t.cur.x = min(t.cols-1, t.cur.x+n)
		t.cur.wrapPending = false
	case 'D':
		t.cur.x = max(0, t.cur.x-n)
		t.cur.wrapPending = false
	case 'E':
		t.cur.x = 0
		t.cur.y = min(t.rows-1, t.cur.y+n)
		t.cur.wrapPending = false
	case 'F':
		t.cur.x = 0
		t.cur.y = max(0, t.cur.y-n)
		t.cur.wrapPending = false
	case 'G', '`':
		t.cur.x = max(0, min(n-1, t.cols-1))
		t.cur.wrapPending = false
	case 'H', 'f':
		t.moveTo(t.param(1, 1)-1, t.param(0, 1)-1)
	case 'I':
		t.tab(n)
	case 'Z':
		t.tab(-n)
	case 'J':
		t.eraseDisplay(t.param(0, 0))
	case 'K':
		t.eraseLine(t.param(0, 0))
	case 'L', 'M':
		if t.cur.y < t.top || t.cur.y > t.bot {
			return
		}
		top := t.top
		t.top = t.cur.y
		if final == 'L' {
			t.scrollDown(n)
		} else {
			// Deleted lines never go to the scrollback.
			saved := t.maxScrollback
			t.maxScrollback = 0
			t.scrollUp(n)
			t.maxScrollback = saved
		}
		t.top = top
		t.cur.x = 0
		t.cur.wrapPending = false
	case 'P':
		line := ls[t.cur.y].Cells
		n = min(n, t.cols-t.cur.x)
		copy(line[t.cur.x:], line[t.cur.x+n:])
		for i := t.cols - n; i < t.cols; i++ {
			line[i] = blank(t.cur.pen)
		}
		t.cur.wrapPending = false
	case 'S':
		if t.top == 0 {
			saved := t.maxScrollback
			t.maxScrollback = 0
			t.scrollUp(n)
			t.maxScrollback = saved
		} else {
			t.scrollUp(n)
		}
	case 'T':
		t.scrollDown(n)
	case 'X':
		line := ls[t.cur.y].Cells
		for i := t.cur.x; i < min(t.cols, t.cur.x+n); i++ {
			line[i] = blank(t.cur.pen)
		}
		t.cur.wrapPending = false
	case 'b':
		if t.lastRune != 0 {
			for range min(n, t.cols*t.rows) {
				t.print(t.lastRune)
			}
		}
	case 'c':
		t.reply("\x1b[?62;22c")
	case 'd':
		t.moveTo(t.cur.x, n-1)
	case 'g':
		switch t.param(0, 0) {
		case 0:
			if t.cur.x < t.cols {
				t.tabs[t.cur.x] = false
			}
		case 3:
			clear(t.tabs)
		}
	case 'h', 'l':
		for i := range t.params {
			if t.params[i] == 4 {
				t.modes.Insert = final == 'h'
			}
		}
	case 'm':
		t.sgr()
	case 'n':
		switch t.param(0, 0) {
		case 5:
			t.reply("\x1b[0n")
		case 6:
			y := t.cur.y
			if t.cur.origin {
				y -= t.top
			}
			t.reply(fmt.Sprintf("\x1b[%d;%dR", y+1, t.cur.x+1))
		}
	case 'r':
		top, bot := t.param(0, 1)-1, t.param(1, t.rows)-1
		bot = min(bot, t.rows-1)
		if top < bot {
			t.top, t.bot = top, bot
			t.moveTo(0, 0)
		}
	case 's':
		t.saveCursor()
	case 'u':
		t.restoreCursor()
	case 't':
		if t.param(0, 0) == 18 {
			t.reply("\x1b[8;" + strconv.Itoa(t.rows) + ";" + strconv.Itoa(t.cols) + "t")
		}
	}
}

func (t *Terminal) softReset() {
	t.modes.CursorVisible = true
	t.modes.Origin = false
	t.modes.AutoWrap = true
	t.modes.Insert = false
	t.modes.AppCursorKeys = false
	t.top, t.bot = 0, t.rows-1
	t.cur.pen = Cell{}
	t.cur.origin = false
	t.saved = cursor{}
}

func (t *Terminal) setPrivateModes(on bool) {
	for _, p := range t.params {
		switch p {
		case 1:
			t.modes.AppCursorKeys = on
		case 6:
			t.modes.Origin = on
			t.cur.origin = on
			t.moveTo(0, 0)
		case 7:
			t.modes.AutoWrap = on
		case 25:
			t.modes.CursorVisible = on
		case 9, 1000, 1002, 1003:
			// Resetting any of the four turns tracking off, as xterm
			// does: a program that leaves with ?1000l after enabling
			// 1003 must not keep receiving motion reports.
			if on {
				t.modes.Mouse = map[int]MouseMode{9: MouseX10, 1000: MouseNormal, 1002: MouseButton, 1003: MouseAny}[p]
			} else {
				t.modes.Mouse = MouseOff
			}
			t.modes.MouseTracking = t.modes.Mouse != MouseOff
		case 1006:
			t.modes.MouseSGR = on
		case 1004:
			t.modes.FocusReport = on
		case 2004:
			t.modes.BracketedPaste = on
		case 47, 1047:
			t.switchScreen(on, false)
		case 1048:
			if on {
				t.saveCursor()
			} else {
				t.restoreCursor()
			}
		case 1049:
			t.switchScreen(on, true)
		}
	}
}

func (t *Terminal) switchScreen(alt, saveCursor bool) {
	if alt == t.altActive {
		return
	}
	if alt {
		if saveCursor {
			t.saveCursor()
		}
		t.altActive = true
		t.alt.lines = t.blankLines(t.rows)
	} else {
		t.altActive = false
		if saveCursor {
			t.restoreCursor()
		}
	}
	t.top, t.bot = 0, t.rows-1
	t.cur.wrapPending = false
}

func (t *Terminal) eraseDisplay(mode int) {
	ls := t.lines()
	switch mode {
	case 0:
		t.eraseLine(0)
		for y := t.cur.y + 1; y < t.rows; y++ {
			ls[y] = newLine(t.cols, t.cur.pen)
		}
	case 1:
		t.eraseLine(1)
		for y := 0; y < t.cur.y; y++ {
			ls[y] = newLine(t.cols, t.cur.pen)
		}
	case 2:
		for y := range ls {
			ls[y] = newLine(t.cols, t.cur.pen)
		}
	case 3:
		t.dropped += t.scrollback.len()
		t.scrollback = history{}
	}
}

func (t *Terminal) eraseLine(mode int) {
	line := t.lines()[t.cur.y].Cells
	from, to := 0, t.cols
	switch mode {
	case 0:
		from = t.cur.x
		t.lines()[t.cur.y].Wrapped = false
	case 1:
		to = t.cur.x + 1
	}
	t.clearCell(t.cur.y, from)
	t.clearCell(t.cur.y, to-1)
	for i := from; i < to; i++ {
		line[i] = blank(t.cur.pen)
	}
	t.cur.wrapPending = false
}

// sgr applies Select Graphic Rendition.
func (t *Terminal) sgr() {
	p := &t.cur.pen
	if len(t.params) == 0 {
		*p = Cell{}
		return
	}
	for i := 0; i < len(t.params); i++ {
		v := max(t.params[i], 0)
		// Sub-parameters (4:3, 38:2::r:g:b) belong to the parameter before.
		subs := []int{}
		for j := i + 1; j < len(t.params) && t.sub[j]; j++ {
			subs = append(subs, max(t.params[j], 0))
		}
		next := i + len(subs)
		switch {
		case v == 0:
			*p = Cell{}
		case v == 1:
			p.Attr |= Bold
		case v == 2:
			p.Attr |= Faint
		case v == 3:
			p.Attr |= Italic
		case v == 4:
			if len(subs) > 0 && subs[0] == 0 {
				p.Attr &^= Underline
			} else {
				p.Attr |= Underline
			}
		case v == 5 || v == 6:
			p.Attr |= Blink
		case v == 7:
			p.Attr |= Inverse
		case v == 8:
			p.Attr |= Hidden
		case v == 9:
			p.Attr |= Strike
		case v == 21:
			p.Attr |= Underline
		case v == 22:
			p.Attr &^= Bold | Faint
		case v == 23:
			p.Attr &^= Italic
		case v == 24:
			p.Attr &^= Underline
		case v == 25:
			p.Attr &^= Blink
		case v == 27:
			p.Attr &^= Inverse
		case v == 28:
			p.Attr &^= Hidden
		case v == 29:
			p.Attr &^= Strike
		case v >= 30 && v <= 37:
			p.FG = Indexed(uint8(v - 30))
		case v == 39:
			p.FG = DefaultColor
		case v >= 40 && v <= 47:
			p.BG = Indexed(uint8(v - 40))
		case v == 49:
			p.BG = DefaultColor
		case v >= 90 && v <= 97:
			p.FG = Indexed(uint8(v - 90 + 8))
		case v >= 100 && v <= 107:
			p.BG = Indexed(uint8(v - 100 + 8))
		case v == 38 || v == 48 || v == 58:
			var c Color
			var ok bool
			if len(subs) > 0 {
				c, ok = extColor(subs, true)
			} else {
				// Semicolon form: 38;5;n or 38;2;r;g;b.
				rest := []int{}
				for j := i + 1; j < len(t.params) && len(rest) < 4; j++ {
					rest = append(rest, max(t.params[j], 0))
				}
				var used int
				c, ok, used = extColorSemi(rest)
				next = i + used
			}
			if ok {
				switch v {
				case 38:
					p.FG = c
				case 48:
					p.BG = c
				}
			}
		}
		i = next
	}
}

// extColor parses the sub-parameters of 38:5:n or 38:2:[cs]:r:g:b.
func extColor(subs []int, colon bool) (Color, bool) {
	switch {
	case len(subs) >= 2 && subs[0] == 5:
		return Indexed(uint8(min(subs[1], 255))), true
	case len(subs) >= 5 && subs[0] == 2:
		rgb := subs[len(subs)-3:]
		return RGB(uint8(min(rgb[0], 255)), uint8(min(rgb[1], 255)), uint8(min(rgb[2], 255))), true
	case len(subs) == 4 && subs[0] == 2:
		return RGB(uint8(min(subs[1], 255)), uint8(min(subs[2], 255)), uint8(min(subs[3], 255))), true
	}
	return 0, false
}

func extColorSemi(rest []int) (Color, bool, int) {
	switch {
	case len(rest) >= 2 && rest[0] == 5:
		return Indexed(uint8(min(rest[1], 255))), true, 2
	case len(rest) >= 4 && rest[0] == 2:
		return RGB(uint8(min(rest[1], 255)), uint8(min(rest[2], 255)), uint8(min(rest[3], 255))), true, 4
	}
	return 0, false, len(rest)
}
