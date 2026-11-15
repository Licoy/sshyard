package app

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/cmdline"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// A terminal completes the command typed at a shell's prompt as
// settings.TerminalCompletion says: the rest of the newest command the
// host's shell ran that starts as the line does shows dimmed after the
// cursor, and → or End types it; the entries of the folder of the path
// being typed list beside the cursor on the path list's shortcut, or by
// themselves, ↑ and ↓ choosing and Enter or Tab typing the rest of the
// name. Nothing reaches the shell before a key or a click accepts, and
// Tab goes to the shell when no list shows.
//
// What is typed is read off the screen, as the line editor shows it: the
// text of the cursor's line after the prompt, which is what was before
// the cursor when the first key of a line was typed. The shell reads a
// line, as far as the terminal can tell, while its line editor has
// bracketed paste on, once it had at a prompt; else, for a shell that
// reports its directory at each prompt (OSC 7), once it reported since
// the last Enter; and for a shell giving neither sign, while its prompt
// looks like one. Never on the alternate screen or while a program reads
// the mouse, and nothing for a line the shell has not echoed all of yet
// (see line): what is not echoed, a password, is never offered on.

// suggestSource is what a terminal completes from, its pane's host's.
type suggestSource interface {
	// suggest is the rest of the newest command run before that starts
	// as input does, "" for none.
	suggest(input string) string
	// ran is told the line Enter runs at a prompt.
	ran(line string)
	// entries are the entries of the folder of the path word, and the
	// start of the name typed there (base), as far as they are read.
	entries(word string) (es []cmdline.Entry, base string, st listState)
}

// listState is how far reading the folder of a path list went.
type listState int

const (
	listNone    listState = iota // it cannot be read: no folder known, nor a way to read it
	listReading                  // it is being read
	listRead                     // es holds it
)

// termSuggest is a terminal view's completion.
type termSuggest struct {
	// mode is settings.TerminalCompletion; on is set while the pane may
	// complete (canSuggest), lists while it may list folders; src is
	// what it completes from and gen changes as what src knows does.
	mode      string
	on, lists bool
	src       suggestSource
	gen       int
	// prompt is the text before the input on the prompt's line, as the
	// first key typed since Enter found it; typed counts the characters
	// typed since; enterAt is the terminal's CwdReports at the last
	// Enter; paste is set once a line editor had bracketed paste on as a
	// line was typed.
	prompt  string
	typed   int
	enterAt uint64
	paste   bool
	// line is what the input reads once the shell echoed all that was
	// sent, while known: text typed adds to it, and another key leaves it
	// unknown until the screen changed after it (sentGen, the terminal's
	// Generation as the last key was sent). Nothing is offered on an
	// input that is not yet line, which would complete a line the shell
	// has more of.
	line    string
	known   bool
	sentGen uint64
	// fresh is set as text is typed, for the path list to open by itself
	fresh bool
	// What refresh found last: the input typed at the prompt (at), at
	// the line's end or not (atEnd), and the rest of the command
	// suggested for it (ghost), shown at the cursor's cell gx, gy. hint
	// is the suggestion for hintIn and hintGen, kept as long as they are.
	at, atEnd    bool
	input, ghost string
	gx, gy       int
	hint, hintIn string
	hintGen      int
	snap         vt.Snapshot
	list         pathList
	// what paintGhost reuses from frame to frame
	ghostRun   []byte
	ghostCol   []int
	ghostCells []vt.Cell
}

// pathList is the list of the entries of the folder of the path typed.
type pathList struct {
	open bool
	// auto is set for a list that opened by itself: it shows only what
	// matches, closes when nothing does, and Enter takes its entry only
	// once ↑ or ↓ chose one (chose), else running the line.
	auto, chose bool
	// the start of the name typed (base), in quote ('\'', '"' or 0),
	// folders alone (dirs); shown the entries matching, sel the one
	// chosen and reveal set to scroll it into view
	base   string
	quote  byte
	dirs   bool
	shown  []cmdline.Entry
	sel    int
	reveal bool
	state  listState
	// word is where the word listed for starts in the input, of input;
	// dismissed is 1 more than the start of the one Esc closed the list
	// on, which it does not open on by itself again, 0 for none
	word, dismissed int
	input           string
}

// readsLine reports whether the shell's line editor reads what is typed,
// as far as the terminal's modes m and its directory reports tell (see
// above); prompt is the text before the input.
func readsLine(m vt.Modes, paste bool, reports, enterAt uint64, prompt string) bool {
	switch {
	case m.AltScreen || m.Mouse != vt.MouseOff || strings.TrimSpace(prompt) == "":
		return false
	case paste:
		return m.BracketedPaste
	case reports > 0:
		return reports > enterAt
	}
	return cmdline.LooksLikePrompt(prompt)
}

// enabled reports whether the view completes at all.
func (s *termSuggest) enabled() bool {
	return s.on && s.src != nil && s.mode != settings.CompletionOff
}

// clear forgets what refresh found, for a view that does not complete.
func (s *termSuggest) clear() {
	s.at, s.input, s.ghost = false, "", ""
	s.list.open = false
}

// capture takes, before the first key of a line, the text before the
// cursor as the prompt, when the shell reads a line there.
func (s *termSuggest) capture(t *vt.Terminal) {
	if s.typed != 0 {
		return
	}
	s.prompt, s.line, s.known = "", "", false
	if !s.enabled() {
		return
	}
	t.SnapshotInto(&s.snap, 0)
	m := s.snap.Modes
	before, after, ok := s.snap.CursorText()
	// at the end of the line, a shell's right prompt aside
	if ok && (after == "" || after[0] == ' ') {
		paste := s.paste || m.BracketedPaste
		if readsLine(m, paste, t.CwdReports(), s.enterAt, before) {
			s.prompt, s.paste, s.known = before, paste, true
		}
	}
}

// sent notes what is sent to the shell, typed or a key's, before it is:
// Enter runs the line, told to src, and Ctrl+C drops it; text typed adds
// to line, and may open the path list by itself.
func (s *termSuggest) sent(t *vt.Terminal, b []byte) {
	switch {
	case len(b) == 0:
		return
	case bytes.ContainsAny(b, "\r\n"):
		if s.enabled() {
			s.refresh(t)
			if s.at && s.known && s.atEnd {
				s.src.ran(s.line)
			}
		}
		s.reset(t)
		return
	case len(b) == 1 && b[0] == 0x03:
		s.typed, s.prompt, s.known = 0, "", false
		s.clear()
		return
	}
	s.capture(t)
	s.typed += max(utf8.RuneCount(b), 1)
	if typed := cmdline.Typeable(string(b)); typed {
		s.line += string(b)
		s.fresh = true
	} else {
		s.known = false
	}
	s.sentGen = t.Generation()
}

// pasted notes text pasted, before it is sent: lines run, none told to
// src, or text inserted, as the shell takes it.
func (s *termSuggest) pasted(t *vt.Terminal, text string) {
	if strings.ContainsAny(text, "\r\n") {
		s.reset(t)
		return
	}
	s.capture(t)
	s.typed += max(utf8.RuneCountInString(text), 1)
	s.known, s.sentGen = false, t.Generation()
}

// reset starts a new line, once Enter ran the one typed.
func (s *termSuggest) reset(t *vt.Terminal) {
	s.typed, s.enterAt, s.prompt = 0, t.CwdReports(), ""
	s.line, s.known, s.list.dismissed = "", false, 0
	s.clear()
}

// settled reports an input the shell echoed all that was sent of.
func (s *termSuggest) settled() bool { return s.at && s.known && s.input == s.line }

// refresh reads what is typed at the prompt, and the suggestion for it.
func (s *termSuggest) refresh(t *vt.Terminal) {
	s.at, s.atEnd, s.input, s.ghost = false, false, "", ""
	if !s.enabled() || s.prompt == "" {
		return
	}
	snap := &s.snap
	t.SnapshotInto(snap, 0)
	if !readsLine(snap.Modes, s.paste, t.CwdReports(), s.enterAt, s.prompt) {
		return
	}
	before, after, ok := snap.CursorText()
	if !ok {
		return
	}
	input, ok := strings.CutPrefix(before, s.prompt)
	if !ok {
		return
	}
	s.at, s.input = true, input
	// at the line's end, but for a right prompt
	s.atEnd = after == "" || strings.HasPrefix(after, "  ")
	// The screen changed after the last key: past another key, or to what
	// is not the start of line, sent from elsewhere (a snippet), the
	// screen tells the line.
	if t.Generation() != s.sentGen && (!s.known || !strings.HasPrefix(s.line, input)) {
		s.line, s.known = input, true
	}
	if !s.settled() {
		return
	}
	if s.hintIn != input || s.hintGen != s.gen {
		s.hint, s.hintIn, s.hintGen = s.src.suggest(input), input, s.gen
	}
	if s.hint != "" && snap.CursorVisible && ghostFits(after, s.hint) {
		s.ghost, s.gx, s.gy = s.hint, snap.CursorX, snap.CursorY
	}
}

// ghostFits reports whether the suggestion rest fits after the cursor,
// where the text from it is after: none, or a shell's right prompt far
// enough, a blank kept before it.
func ghostFits(after, rest string) bool {
	if after == "" {
		return true
	}
	gap := len(after) - len(strings.TrimLeft(after, " "))
	w := 0
	for _, r := range rest {
		w += max(vt.RuneWidth(r), 0)
	}
	return w < gap
}

// listPaths updates the path list for the input refresh found: it opens
// by itself for a path typed in the automatic mode, once the shell echoed
// it, and closes when the line is no longer at a prompt; open opens it as
// its shortcut does.
func (s *termSuggest) listPaths(open bool) {
	l := &s.list
	if !s.at || !s.lists {
		l.open, s.fresh = false, false
		return
	}
	if !s.settled() && !open {
		return // what it lists for is not on the screen yet
	}
	fresh := s.fresh
	s.fresh = false
	arg := cmdline.Last(s.input)
	ok, dirs := arg.Path()
	if open && !ok && arg.Cmd != "" && !strings.HasPrefix(arg.Word, "-") {
		ok = true // asked for: any argument is a path
	}
	switch {
	case open:
		l.open, l.auto, l.chose, l.dismissed, l.sel = true, false, false, 0, 0
	case !l.open && fresh && ok && arg.Word != "" && s.mode == settings.CompletionAuto && arg.Start+1 != l.dismissed:
		l.open, l.auto, l.chose, l.sel = true, true, false, 0
	}
	if !l.open {
		return
	}
	if !ok || l.auto && arg.Start != l.word && arg.Word == "" {
		l.open = false
		return
	}
	es, base, st := s.src.entries(arg.Word)
	if st == listRead {
		l.shown = cmdline.Matches(es, base, dirs)
	} else {
		l.shown = nil
	}
	l.base, l.quote, l.dirs, l.state, l.word, l.input = base, arg.Quote, dirs, st, arg.Start, s.input
	if l.auto && (st == listNone || st == listRead && len(l.shown) == 0) {
		l.open = false
		return
	}
	l.sel = min(max(l.sel, 0), max(len(l.shown)-1, 0))
}

// shows reports whether the path list shows: its entries, or for one
// asked for, why there are none.
func (l *pathList) shows() bool { return l.open && (len(l.shown) > 0 || !l.auto) }

// suggestKey takes a key for the completion, before the shell has it: the
// path list's while it shows, and the keys accepting the suggestion while
// one shows. It reports whether it took it.
func (v *termView) suggestKey(k ui.Key, mods ui.Modifiers) bool {
	s := &v.suggest
	if !s.enabled() || v.fanout != nil || v.offset != 0 || v.preedit != "" {
		return false
	}
	t := v.target.Terminal()
	l := &s.list
	if l.shows() && mods == 0 {
		n := len(l.shown)
		switch {
		case k == ui.KeyEscape:
			l.open, l.dismissed = false, l.word+1
			return true
		case n == 0:
		case k == ui.KeyUp || k == ui.KeyDown:
			d := 1
			if k == ui.KeyUp {
				d = -1
			}
			l.sel, l.reveal, l.chose = (l.sel+d+n)%n, true, true
			return true
		case k == ui.KeyTab || k == ui.KeyEnter && (!l.auto || l.chose):
			// Only for the input the list shows the entries of, all of it
			// on the screen: else the next frame lists again.
			s.refresh(t)
			if s.settled() && s.input == l.input {
				v.acceptPath(l.sel)
			}
			return true
		}
	}
	if !acceptsGhost(k, mods) {
		return false
	}
	s.refresh(t)
	if s.ghost == "" {
		return false
	}
	v.typeText(s.ghost)
	return true
}

// acceptsGhost reports the keys that type the rest of the command
// suggested: →, End, and ⌘→ (the line's end).
func acceptsGhost(k ui.Key, mods ui.Modifiers) bool {
	return mods == 0 && (k == ui.KeyRight || k == ui.KeyEnd) || mods == ui.Super && k == ui.KeyRight
}

// acceptPath types the rest of the name of the path list's entry i: a
// folder's with its slash, the list going on with what it holds; a
// file's closes the list.
func (v *termView) acceptPath(i int) {
	l := &v.suggest.list
	if i < 0 || i >= len(l.shown) {
		return
	}
	e := l.shown[i]
	ins, ok := cmdline.Insert(e.Name, l.base, e.Dir, l.quote)
	if !ok {
		return
	}
	if !e.Dir {
		l.open = false
	}
	l.sel, l.chose = 0, false
	v.typeText(ins)
}

// typeText sends text as if typed, as completing does: the path list
// does not open by itself for it.
func (v *termView) typeText(text string) {
	s := &v.suggest
	s.sent(v.target.Terminal(), []byte(text))
	s.ghost, s.fresh = "", false
	v.send([]byte(text))
}

// paintGhost draws the rest of the command suggested after the cursor,
// dimmed, as far as the cursor's row goes; ox and oy are the grid's
// origin.
func (v *termView) paintGhost(p *ui.Painter, snap *vt.Snapshot, ox, oy float32, pal *theme.Terminal, focused bool) {
	s := &v.suggest
	if !focused || s.ghost == "" || v.offset != 0 || v.preedit != "" || snap.CursorX != s.gx || snap.CursorY != s.gy || snap.CursorY >= len(snap.Lines) {
		return
	}
	cols := snap.Cols
	if cap(s.ghostCells) < cols {
		s.ghostCells = make([]vt.Cell, cols)
	}
	cells := s.ghostCells[:cols]
	run, colOf := s.ghostRun[:0], s.ghostCol[:0]
	x := s.gx
	for _, r := range s.ghost {
		w := vt.RuneWidth(r)
		if w <= 0 {
			if len(colOf) > 0 {
				run = utf8.AppendRune(run, r)
				colOf = append(colOf, colOf[len(colOf)-1])
			}
			continue
		}
		if x+w > cols {
			break
		}
		cells[x] = vt.Cell{Rune: r, Width: uint8(w)}
		run = utf8.AppendRune(run, r)
		colOf = append(colOf, x)
		x += w
	}
	s.ghostRun, s.ghostCol = run, colOf
	if len(run) == 0 {
		return
	}
	g := v.shapeBytes(run, 0)
	placed := append(v.placed[:0], g...)
	v.placed = placed
	placeGlyphs(placed, g, colOf, cells, v.cellW)
	p.Glyphs(placed, ox, oy+float32(snap.CursorY)*v.cellH+v.base, pal.Foreground.Alpha(0.4))
}
