package vt

import (
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"
)

// DefaultScrollback is how many lines scroll back by default.
const DefaultScrollback = 10000

// CursorStyle is the cursor's shape, as DECSCUSR sets it.
type CursorStyle int

const (
	CursorBlock CursorStyle = iota
	CursorUnderline
	CursorBar
)

// Modes are the terminal modes the UI needs to know about.
type Modes struct {
	AppCursorKeys  bool // DECCKM: arrows send ESC O A
	AppKeypad      bool
	BracketedPaste bool
	CursorVisible  bool
	AltScreen      bool
	AutoWrap       bool
	Origin         bool
	Insert         bool
	MouseTracking  bool // a mouse mode is on (9/1000/1002/1003)
	Mouse          MouseMode
	MouseSGR       bool // 1006: SGR encoding of mouse reports
	FocusReport    bool // 1004
	// LinuxKeys sends the keys as the Linux console does (TERM=linux):
	// F1–F5 ESC [ [ A–E, Home and End ESC [ 1 ~ and ESC [ 4 ~. It is the
	// terminal's, set with SetLinuxKeys, which no program changes.
	LinuxKeys bool
}

type cursor struct {
	x, y        int
	pen         Cell
	origin      bool
	wrapPending bool
	graphics    bool
}

type screen struct {
	lines []Line
}

// Terminal is a terminal emulator. Write feeds it the output of a
// program; the UI reads it with Snapshot. It is safe for concurrent use.
type Terminal struct {
	mu sync.Mutex

	cols, rows int
	primary    screen
	alt        screen
	altActive  bool
	linuxKeys  bool // Modes.LinuxKeys

	scrollback    history
	maxScrollback int
	// dropped counts the lines that fell off the top of the scrollback,
	// so that absolute line numbers stay stable as it is trimmed.
	dropped int
	// pushed counts the lines a resize pushed from the screen into the
	// scrollback, which a later resize that makes room brings back.
	pushed int

	cur         cursor
	saved       cursor
	savedAlt    cursor
	top, bot    int // scroll region, inclusive
	tabs        []bool
	modes       Modes
	style       CursorStyle
	styleSet    bool // the program explicitly selected a shape with DECSCUSR
	cursorBlink bool
	title       string
	cwd         string
	cwdHost     string // the host the cwd report named
	remoteHost  string // OSC 1337 RemoteHost's host
	g0Graphics  bool
	g1Graphics  bool
	shiftOut    bool
	lastRune    rune
	joinNext    bool // the last rune printed was ZWJ

	// parser
	csiBytes int
	state    pstate
	params   []int
	sub      []bool // sub[i]: params[i] followed a colon
	inter    []byte
	private  byte
	paramCur int
	nextSub  bool
	hasParam bool
	osc      []byte
	osc52    bool      // this OSC started with "52;"
	oscScan  osc52Scan // its body; once dropped, swallow and keep nothing
	escInter byte
	utf8buf  []byte

	outputEncoding OutputEncoding
	outputBuf      [4]byte
	outputLen      int
	outputDecoder  *encoding.Decoder
	// onlyOSC is set while a tmux passthrough payload is parsed: OSC 52
	// and OSC 0/2 apply, and nothing else changes the screen.
	onlyOSC bool
	// DCS, for tmux passthrough (ESC P tmux ; … ST). Other DCS is ignored.
	dcs        []byte
	dcsTmux    bool
	dcsSkip    bool // swallow to ST, do not keep
	dcsTooBig  bool // the swallowed OSC 52 was over the limit
	dcsChecked bool
	dcsIs52    bool
	dcsScan    osc52Scan // the body of a passed-through OSC 52
	dcsEnded   bool      // that OSC 52 has ended (BEL or ESC)
	dcsTail    int       // bytes kept after it ended

	// Reply, when set, receives what the terminal answers the program
	// (device status and attributes reports). It is called with the lock
	// held, so it must not call back into the terminal.
	Reply func([]byte)
	// OnClipboard reports an OSC 52. sel is the selection parameter
	// (c, p, s or 0–7); each of those is the system clipboard. data is
	// the text. query is true when the program asked to read the
	// clipboard (Pd is "?"); data is then empty and nothing is answered.
	// Called with the lock held, like Reply.
	OnClipboard func(sel, data string, query bool)
	// OnClipboardDrop reports an OSC 52 bigger than the size limit. The
	// whole sequence is discarded. Called with the lock held.
	OnClipboardDrop func()
	// Bell, when set, is called on BEL.
	Bell func()
	// lineHook, set by SetLineHook, takes the lines the cursor leaves.
	lineHook func(text string, wrapped bool)

	// osc52Max is the largest decoded OSC 52, or 0 for the default.
	osc52Max int

	// now is the clock lines are stamped with (Line.Time), nil for the
	// system's.
	now func() time.Time

	gen uint64
}

// New returns a terminal of cols×rows cells.
func New(cols, rows int) *Terminal {
	t := &Terminal{maxScrollback: DefaultScrollback, outputEncoding: EncodingUTF8}
	t.cols, t.rows = max(cols, 1), max(rows, 1)
	t.reset()
	return t
}

func (t *Terminal) reset() {
	t.primary = screen{lines: t.blankLines(t.rows)}
	t.alt = screen{lines: t.blankLines(t.rows)}
	t.altActive = false
	t.cur = cursor{}
	t.saved = cursor{}
	t.savedAlt = cursor{}
	t.top, t.bot = 0, t.rows-1
	t.modes = Modes{CursorVisible: true, AutoWrap: true}
	t.style, t.styleSet = CursorBlock, false
	t.cursorBlink = true
	t.g0Graphics, t.g1Graphics, t.shiftOut = false, false, false
	t.resetTabs()
	t.onlyOSC = false
	t.osc52, t.oscScan = false, osc52Scan{}
	t.resetDCS()
	t.state = sGround
}

func (t *Terminal) blankLines(n int) []Line {
	ls := make([]Line, n)
	for i := range ls {
		ls[i] = newLine(t.cols, Cell{})
	}
	return ls
}

func (t *Terminal) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for i := 8; i < t.cols; i += 8 {
		t.tabs[i] = true
	}
}

func (t *Terminal) lines() []Line {
	if t.altActive {
		return t.alt.lines
	}
	return t.primary.lines
}

// SetScrollback sets how many lines scroll back.
func (t *Terminal) SetScrollback(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxScrollback = max(n, 0)
	t.trimScrollback()
}

func (t *Terminal) trimScrollback() {
	t.dropped += t.scrollback.trim(t.maxScrollback)
}

// Size returns the terminal's size in cells.
func (t *Terminal) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// Title is the window title the program set (OSC 0 or 2).
func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// Cwd is the working directory the shell last reported (OSC 7
// file://host/path or OSC 1337 CurrentDir=), or "" if it never did.
func (t *Terminal) Cwd() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

// CwdHost is the host the last directory report named: OSC 7's
// file://host/, or for OSC 1337 CurrentDir the host of the last OSC 1337
// RemoteHost=user@host; "" when it named none.
func (t *Terminal) CwdHost() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwdHost
}

// Modes returns the terminal's modes.
func (t *Terminal) Modes() Modes {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.modes
	m.AltScreen = t.altActive
	m.LinuxKeys = t.linuxKeys
	return m
}

// SetLinuxKeys makes the keys those of the Linux console (Modes.LinuxKeys).
func (t *Terminal) SetLinuxKeys(on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.linuxKeys = on
}

// Generation increases every time the terminal changes.
func (t *Terminal) Generation() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gen
}

// Resize changes the terminal's size. On the primary screen lines are
// reflowed: a line that wrapped is joined with the lines it continued on
// and wrapped again at the new width, so narrowing and widening again
// loses nothing. When the screen gets too short, the blank lines below
// the cursor go first, then the lines at the top go to the scrollback;
// when it gets taller, they come back from it. The alternate screen,
// which its program redraws, is only cut or padded.
func (t *Terminal) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols == t.cols && rows == t.rows {
		return
	}
	t.gen++
	primary, alt := &t.cur, &t.savedAlt
	if t.altActive {
		primary, alt = &t.saved, &t.cur
	}
	t.resizePrimary(cols, rows, primary)
	ls := t.alt.lines
	for i := range ls {
		ls[i] = fitLine(ls[i], cols)
	}
	if len(ls) > rows {
		// Drop blank lines below the cursor first, then lines at the top.
		for len(ls) > rows && len(ls)-1 > alt.y && ls[len(ls)-1].String() == "" {
			ls = ls[:len(ls)-1]
		}
		if n := len(ls) - rows; n > 0 {
			ls = ls[n:]
			alt.y -= n
		}
	}
	for len(ls) < rows {
		ls = append(ls, newLine(cols, Cell{}))
	}
	t.alt.lines = ls
	alt.x = min(alt.x, cols-1)
	alt.y = max(0, min(alt.y, rows-1))
	alt.wrapPending = false
	t.cols, t.rows = cols, rows
	t.top, t.bot = 0, rows-1
	t.resetTabs()
	t.trimScrollback()
}

// resizePrimary reflows the scrollback and the primary screen to cols
// columns and fits them to rows lines. c is the primary screen's cursor;
// it stays on the cell it was on.
func (t *Terminal) resizePrimary(cols, rows int, c *cursor) {
	old := append(t.scrollback.lines(), t.primary.lines...)
	top, y := t.scrollback.len(), t.scrollback.len()+c.y
	ls, x := old, c.x
	if cols != t.cols {
		var pending bool
		ls, top, y, x, pending = reflow(old, cols, top, y, c.x, c.wrapPending)
		if c.wrapPending {
			// The cursor was past the last character: it is again, at
			// the margin or just after the character.
			if x == cols-1 {
				pending = true
			} else {
				x++
			}
		}
		c.wrapPending = pending
	} else {
		for i := range ls {
			ls[i] = clipLine(ls[i], cols)
		}
		c.wrapPending = false
	}
	switch n := len(ls) - top; {
	case n > rows:
		// Drop blank lines below the cursor first, then push lines off
		// the top, keeping the cursor on the screen.
		for len(ls)-top > rows && len(ls)-1 > y && ls[len(ls)-1].String() == "" {
			ls = ls[:len(ls)-1]
		}
		if extra := len(ls) - top - rows; extra > 0 {
			push := min(extra, y-top)
			top += push
			t.pushed += push
			ls = ls[:top+rows]
		}
	case n < rows:
		// Bring back as many lines as the screen grew, and those that
		// narrowing it pushed into the scrollback.
		back := min(rows-n, top, max(rows-t.rows, 0)+t.pushed)
		top -= back
		t.pushed = max(t.pushed-back, 0)
		for len(ls)-top < rows {
			ls = append(ls, newLine(cols, Cell{}))
		}
	}
	// The scrollback keeps its lines compact (lines the screen pushed
	// in were full), the screen full.
	for i := range ls[:top] {
		ls[i] = trimmed(ls[i])
	}
	for i := top; i < len(ls); i++ {
		ls[i] = fitLine(ls[i], cols)
	}
	t.scrollback.set(ls[:top])
	t.primary.lines = ls[top:]
	t.pushed = min(t.pushed, top)
	c.x = min(x, cols-1)
	c.y = max(0, min(y-top, rows-1))
}

// reflow wraps lines again at cols columns. top is the index of the
// screen's first line and (x, y) the cursor; it returns the lines and
// where those are now. Blanks at the end of a line are dropped, except
// up to the cursor. A cursor just after the text that would start a row
// of its own is put on the margin with the wrap pending instead, unless
// it was pending already (pending): it then stays on its character.
func reflow(old []Line, cols, top, y, x int, pending bool) (out []Line, ntop, ny, nx int, wrap bool) {
	out = make([]Line, 0, len(old))
	ntop = len(old)
	for i := 0; i < len(old); {
		j := i
		for j < len(old)-1 && old[j].Wrapped {
			j++
		}
		// The cells of the cursor and of the screen's first line in the
		// joined line, or -1.
		cur, first := -1, -1
		if i == y {
			cur = x
		}
		if i == top {
			ntop = len(out)
		}
		if i == j && trimmedLen(old[i].Cells) <= cols && cur < cols {
			// The usual case: a line that fits as it is.
			if cur >= 0 {
				ny, nx = len(out), cur
			}
			l := clipLine(old[i], cols)
			l.Wrapped, l.widePad = false, false
			out = append(out, l)
			i++
			continue
		}
		var cells []Cell
		tm := old[i].Time
		for k := i; k <= j; k++ {
			cs := old[k].Cells
			if k == y {
				cur = len(cells) + x
			}
			if k == top {
				first = len(cells)
			}
			if k < j && old[k].widePad && len(cs) > 0 && isBlank(cs[len(cs)-1]) {
				cs = cs[:len(cs)-1]
			}
			cells = append(cells, cs...)
		}
		text := trimmedLen(cells)
		n := max(text, cur+1)
		for len(cells) < n {
			cells = append(cells, blank(Cell{}))
		}
		line := Line{Cells: make([]Cell, 0, cols), Time: tm}
		for k := 0; k < n; {
			w := 1
			if cells[k].Width == 2 && k+1 < n && cells[k+1].Width == 0 {
				w = 2
			}
			c := cells[k : k+w]
			if w > cols {
				// A wide character in a one-column terminal.
				c = []Cell{cells[k]}
				c[0].Width = 1
			}
			if len(line.Cells)+len(c) > cols {
				if !pending && k == cur && k == n-1 && k >= text {
					ny, nx, wrap = len(out), cols-1, true
					break
				}
				line.widePad = len(line.Cells) < cols
				for len(line.Cells) < cols {
					line.Cells = append(line.Cells, blank(Cell{}))
				}
				line.Wrapped = true
				out = append(out, line)
				line = Line{Cells: make([]Cell, 0, cols), Time: tm}
			}
			if cur >= k && cur < k+w {
				ny, nx = len(out), len(line.Cells)+min(cur-k, len(c)-1)
			}
			if first >= k && first < k+w {
				ntop = len(out)
			}
			line.Cells = append(line.Cells, c...)
			k += w
		}
		if first >= n {
			ntop = len(out)
		}
		out = append(out, clipLine(line, cols))
		i = j + 1
	}
	return out, min(ntop, ny), ny, nx, wrap
}

// isBlank reports a cell that only erased.
func isBlank(c Cell) bool {
	return (c.Rune == ' ' || c.Rune == 0) && c.Width == 1 && c.Comb == 0 && c.Attr == 0
}

// trimmedLen is the length of cells without the blanks at the end.
func trimmedLen(cells []Cell) int {
	n := len(cells)
	for n > 0 && isBlank(cells[n-1]) {
		n--
	}
	return n
}

// clipLine cuts l to cols cells, as fitLine does, without filling it.
func clipLine(l Line, cols int) Line {
	if len(l.Cells) > cols {
		l.Cells = l.Cells[:cols]
		if cols > 0 && l.Cells[cols-1].Width == 2 {
			l.Cells[cols-1] = blank(Cell{})
		}
		l.Wrapped, l.widePad = false, false
	}
	return l
}

// fitLine makes l cols cells: cut, or filled with blanks.
func fitLine(l Line, cols int) Line {
	l = clipLine(l, cols)
	for len(l.Cells) < cols {
		l.Cells = append(l.Cells, blank(Cell{}))
	}
	return l
}

// Write feeds the terminal a program's output.
func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gen++
	if t.outputEncoding != EncodingUTF8 {
		return t.writeEncoded(p)
	}
	for i := 0; i < len(p); i++ {
		b := p[i]
		if len(t.utf8buf) > 0 || (b >= 0x80 && t.state != sOSC && t.state != sString && t.state != sDCS && t.state != sDCSEsc) {
			if b < 0x80 || (len(t.utf8buf) > 0 && b&0xC0 != 0x80) {
				// An invalid sequence: drop what we had.
				t.utf8buf = t.utf8buf[:0]
				if b >= 0x80 {
					t.utf8buf = append(t.utf8buf, b)
					continue
				}
			} else {
				t.utf8buf = append(t.utf8buf, b)
				if !utf8.FullRune(t.utf8buf) {
					continue
				}
				r, _ := utf8.DecodeRune(t.utf8buf)
				t.utf8buf = t.utf8buf[:0]
				t.rune(r)
				continue
			}
		}
		t.byte(b)
	}
	return len(p), nil
}

// WriteString is Write for a string.
func (t *Terminal) WriteString(s string) { t.Write([]byte(s)) }

// SetLineHook has hook told each line of the primary screen the cursor
// leaves, by a line feed or by wrapping, as the line reads then: a line
// edited in place (a progress bar, a command line) as it ended up.
// wrapped says the text goes on in the next line. A full-screen
// program's alternate screen is left out. hook is called with the lock
// held, so it must not call back into the terminal, nor block; nil
// stops.
func (t *Terminal) SetLineHook(hook func(text string, wrapped bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lineHook = hook
}

// CursorLine is the text of the line the cursor is on, the end of what
// SetLineHook told; "" on the alternate screen.
func (t *Terminal) CursorLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.altActive {
		return ""
	}
	return t.primary.lines[t.cur.y].text(true)
}

// stamp notes when output first reaches the cursor's line.
func (t *Terminal) stamp() {
	l := &t.lines()[t.cur.y]
	if l.Time != 0 {
		return
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	l.Time = now().Unix()
}

// leaveLine tells the line hook of the cursor's line, as the cursor
// leaves it.
func (t *Terminal) leaveLine() {
	if t.lineHook == nil || t.altActive {
		return
	}
	l := t.primary.lines[t.cur.y]
	if !l.Wrapped {
		t.lineHook(l.text(true), false)
		return
	}
	cells := l.Cells
	if l.widePad && len(cells) > 0 {
		cells = cells[:len(cells)-1] // the blank a wide character left
	}
	t.lineHook(Line{Cells: cells}.text(false), true)
}

type pstate int

const (
	sGround pstate = iota
	sEscape
	sCSI
	sCSIDrop
	sOSC
	sOSCEsc
	sString // SOS, PM, APC, and a DCS that is not tmux: ignored up to ST
	sStringEsc
	sDCS // ESC P, possibly tmux passthrough
	sDCSEsc
	sCharset
)

// rune handles a decoded non-ASCII rune.
func (t *Terminal) rune(r rune) {
	switch t.state {
	case sGround:
		t.print(r)
	case sCSIDrop:
		// An oversized CSI stays discarded even across UTF-8 input.
	case sOSC:
		for _, b := range utf8.AppendRune(nil, r) {
			t.oscPush(b)
		}
	default:
		// Not part of any sequence: abort it and print.
		t.state = sGround
		t.print(r)
	}
}

func (t *Terminal) byte(b byte) {
	if t.state == sCSIDrop {
		if b >= 0x40 && b <= 0x7e {
			t.state = sGround
		}
		return
	}
	// C0 controls act in the middle of most sequences.
	if b < 0x20 && t.state != sOSC && t.state != sString && t.state != sOSCEsc && t.state != sStringEsc && t.state != sDCS && t.state != sDCSEsc {
		if b == 0x1b {
			t.state = sEscape
			t.escInter = 0
			return
		}
		t.control(b)
		return
	}
	switch t.state {
	case sGround:
		if b == 0x7f {
			return
		}
		t.print(rune(b))
	case sEscape:
		t.escape(b)
	case sCharset:
		g := b == '0'
		if t.escInter == '(' {
			t.g0Graphics = g
		} else if t.escInter == ')' {
			t.g1Graphics = g
		}
		t.state = sGround
	case sCSI:
		t.csiByte(b)
	case sOSC:
		switch b {
		case 0x07:
			t.oscDone()
		case 0x1b:
			t.state = sOSCEsc
		default:
			t.oscPush(b)
		}
	case sOSCEsc:
		if b == '\\' {
			t.oscDone()
		} else {
			t.state = sGround
		}
	case sDCS:
		if b == 0x1b {
			t.state = sDCSEsc
			return
		}
		// BEL ends an ordinary DCS. Inside tmux passthrough it belongs
		// to the wrapped sequence (OSC 52 often ends with BEL).
		if !t.dcsTmux && b == 0x07 {
			t.resetDCS()
			t.state = sGround
			return
		}
		t.dcsPush(b)
	case sDCSEsc:
		switch b {
		case '\\':
			t.finishDCS()
		case 0x1b:
			// tmux writes ESC as ESC ESC, so this is one escaped ESC
			// and not the start of ST.
			if t.dcsTmux {
				t.dcsPush(0x1b)
				t.state = sDCS
			} else {
				t.resetDCS()
				t.state = sString
			}
		default:
			if t.dcsTmux {
				t.dcsPush(0x1b)
				t.state = sDCS
				t.dcsPush(b)
			} else {
				t.resetDCS()
				t.state = sString
			}
		}
	case sString:
		if b == 0x1b {
			t.state = sStringEsc
		} else if b == 0x07 {
			t.state = sGround
		}
	case sStringEsc:
		if b == '\\' {
			t.state = sGround
		} else {
			t.state = sString
		}
	}
}

func (t *Terminal) control(b byte) {
	if t.onlyOSC {
		return
	}
	switch b {
	case 0x07:
		if t.Bell != nil {
			t.Bell()
		}
	case 0x08:
		if t.cur.x > 0 {
			t.cur.x--
		}
		t.cur.wrapPending = false
	case 0x09:
		t.tab(1)
	case 0x0a, 0x0b, 0x0c:
		t.index()
	case 0x0d:
		t.cur.x = 0
		t.cur.wrapPending = false
	case 0x0e:
		t.shiftOut = true
	case 0x0f:
		t.shiftOut = false
	case 0x18, 0x1a:
		t.state = sGround
	}
}

func (t *Terminal) escape(b byte) {
	if t.onlyOSC && b != ']' {
		t.state = sGround
		return
	}
	t.state = sGround
	switch b {
	case '[':
		t.state = sCSI
		t.csiBytes = 0
		t.params = t.params[:0]
		t.sub = t.sub[:0]
		t.inter = t.inter[:0]
		t.private = 0
		t.paramCur = 0
		t.hasParam = false
		t.nextSub = false
	case ']':
		t.state = sOSC
		t.osc = t.osc[:0]
		t.osc52, t.oscScan = false, osc52Scan{}
	case 'P':
		t.resetDCS()
		t.state = sDCS
	case 'X', '^', '_':
		t.state = sString
	case '(', ')', '*', '+':
		t.escInter = b
		t.state = sCharset
	case '7':
		t.saveCursor()
	case '8':
		t.restoreCursor()
	case 'D':
		t.index()
	case 'E':
		t.cur.x = 0
		t.index()
	case 'M':
		t.reverseIndex()
	case 'H':
		if t.cur.x < t.cols {
			t.tabs[t.cur.x] = true
		}
	case 'c':
		sb, dropped, title, cwd, cwdHost, remoteHost := t.scrollback, t.dropped, t.title, t.cwd, t.cwdHost, t.remoteHost
		t.reset()
		t.scrollback, t.dropped, t.title, t.cwd, t.cwdHost, t.remoteHost = sb, dropped, title, cwd, cwdHost, remoteHost
	case '=':
		t.modes.AppKeypad = true
	case '>':
		t.modes.AppKeypad = false
	case '#', ' ', '%':
		// ESC # 8 (DECALN) and friends take one more byte.
		t.escInter = b
		t.state = sCharset
	}
}

const (
	maxCSIBytes         = 4096
	maxCSIParams        = 64
	maxCSIIntermediates = 4
)

func (t *Terminal) csiByte(b byte) {
	t.csiBytes++
	if t.csiBytes > maxCSIBytes ||
		((b == ';' || b == ':') && len(t.params) >= maxCSIParams-1) ||
		(b >= 0x20 && b <= 0x2f && len(t.inter) >= maxCSIIntermediates) {
		t.params, t.sub, t.inter = t.params[:0], t.sub[:0], t.inter[:0]
		t.state = sCSIDrop
		if b >= 0x40 && b <= 0x7e {
			t.state = sGround
		}
		return
	}
	switch {
	case b >= '0' && b <= '9':
		t.paramCur = t.paramCur*10 + int(b-'0')
		if t.paramCur > 65535 {
			t.paramCur = 65535
		}
		t.hasParam = true
	case b == ';' || b == ':':
		t.params = append(t.params, t.paramValue())
		t.sub = append(t.sub, t.nextSub)
		t.nextSub = b == ':'
		t.paramCur, t.hasParam = 0, false
	case b >= '<' && b <= '?':
		if len(t.params) == 0 && !t.hasParam {
			t.private = b
		}
	case b >= 0x20 && b <= 0x2f:
		t.inter = append(t.inter, b)
	case b >= 0x40 && b <= 0x7e:
		if t.hasParam || len(t.params) > 0 {
			t.params = append(t.params, t.paramValue())
			t.sub = append(t.sub, t.nextSub)
		}
		t.state = sGround
		t.csi(b)
	default:
		t.state = sGround
	}
}

// paramValue is the parameter being parsed, -1 when it was left empty.
func (t *Terminal) paramValue() int {
	if !t.hasParam {
		return -1
	}
	return t.paramCur
}
