package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/cmdline"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/shellhist"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
)

// echoTarget is a shell's line editor as far as completing goes: what is
// sent is recorded, and the text of it echoed while echo is set.
type echoTarget struct {
	term *vt.Terminal
	out  *[]byte
	echo *bool
}

func (e echoTarget) Terminal() *vt.Terminal { return e.term }
func (e echoTarget) Resize(cols, rows int)  { e.term.Resize(cols, rows) }
func (e echoTarget) Write(b []byte) (int, error) {
	*e.out = append(*e.out, b...)
	if *e.echo && cmdline.Typeable(string(b)) {
		e.term.Write(b)
	}
	return len(b), nil
}

// fakeSource completes from cmds and lists es, as of the folder /home/u.
type fakeSource struct {
	cmds []string
	runs []string
	es   []cmdline.Entry
}

func (f *fakeSource) suggest(input string) string { return cmdline.Suggest(input, f.cmds) }
func (f *fakeSource) ran(line string)             { f.runs = append(f.runs, line) }
func (f *fakeSource) entries(word string) ([]cmdline.Entry, string, listState) {
	_, base, ok := cmdline.Dir(word, "/home/u", "/home/u")
	if !ok {
		return nil, "", listNone
	}
	return f.es, base, listRead
}

type suggestRig struct {
	tv   *termView
	term *vt.Terminal
	src  *fakeSource
	out  []byte
	echo bool
}

// newSuggestRig is a terminal at a prompt of a shell with bracketed paste.
func newSuggestRig(mode string) *suggestRig {
	r := &suggestRig{term: vt.New(60, 6), src: &fakeSource{}, echo: true}
	r.term.WriteString("\x1b[?2004huser@web:~$ ")
	r.tv = newTermView(echoTarget{r.term, &r.out, &r.echo}, 13)
	s := &r.tv.suggest
	s.mode, s.on, s.lists, s.src = mode, true, true, r.src
	return r
}

func (r *suggestRig) text(s string) {
	r.tv.input(ui.InputEvent{Kind: ui.InputText, Text: s}, nil)
}

func (r *suggestRig) key(k ui.Key, mods ui.Modifiers) {
	r.tv.input(ui.InputEvent{Kind: ui.InputKeyDown, Key: k, Mods: mods}, nil)
}

// frame does what a frame does for the focused terminal.
func (r *suggestRig) frame(open bool) {
	r.tv.suggest.refresh(r.term)
	r.tv.suggest.listPaths(open)
}

func (r *suggestRig) sentSince(n int) string { return string(r.out[n:]) }

// TestGhostAccept: → types the rest of the command suggested, once the
// shell echoed what was typed; Tab goes to the shell.
func TestGhostAccept(t *testing.T) {
	r := newSuggestRig(settings.CompletionInline)
	r.src.cmds = []string{"git status", "git stash"}
	r.text("git s")
	r.frame(false)
	if g := r.tv.suggest.ghost; g != "tatus" {
		t.Fatalf("ghost %q, want %q", g, "tatus")
	}
	n := len(r.out)
	r.key(ui.KeyTab, 0)
	if got := r.sentSince(n); got != "\t" {
		t.Fatalf("Tab sent %q", got)
	}
	r = newSuggestRig(settings.CompletionInline)
	r.src.cmds = []string{"git status"}
	r.text("git s")
	r.frame(false)
	n = len(r.out)
	r.key(ui.KeyRight, 0)
	if got := r.sentSince(n); got != "tatus" {
		t.Fatalf("→ sent %q, want the rest", got)
	}
	r.frame(false)
	if r.tv.suggest.ghost != "" {
		t.Errorf("ghost %q left after accepting", r.tv.suggest.ghost)
	}
	// End accepts too.
	r.text(" ")
	r.src.cmds = []string{"git status -s"}
	r.frame(false)
	n = len(r.out)
	r.key(ui.KeyEnd, 0)
	if got := r.sentSince(n); got != "-s" {
		t.Fatalf("End sent %q", got)
	}

	// A modified → is the shell's.
	r = newSuggestRig(settings.CompletionInline)
	r.src.cmds = []string{"git status"}
	r.text("git s")
	r.frame(false)
	n = len(r.out)
	r.key(ui.KeyRight, ui.Alt)
	if got := r.sentSince(n); strings.Contains(got, "tatus") {
		t.Fatalf("⌥→ sent %q", got)
	}
}

// TestGhostWaitsForEcho: nothing is offered for a line the shell has not
// echoed all of, which would type the rest after keys still on their way.
func TestGhostWaitsForEcho(t *testing.T) {
	r := newSuggestRig(settings.CompletionInline)
	r.src.cmds = []string{"git status"}
	r.text("g")
	r.echo = false
	r.text("i")
	r.frame(false)
	if g := r.tv.suggest.ghost; g != "" {
		t.Fatalf("ghost %q before the echo", g)
	}
	n := len(r.out)
	r.key(ui.KeyRight, 0)
	if got := r.sentSince(n); got != "\x1b[C" {
		t.Fatalf("→ sent %q, want the arrow alone", got)
	}
	r.term.WriteString("i")
	r.frame(false)
	if g := r.tv.suggest.ghost; g != "t status" {
		t.Fatalf("ghost %q after the echo", g)
	}
	// An editing key: the line is taken again once the screen changed.
	r.echo = true
	r.key(ui.KeyBackspace, 0)
	r.frame(false)
	if g := r.tv.suggest.ghost; g != "" {
		t.Fatalf("ghost %q before the shell redrew", g)
	}
	r.term.WriteString("\b \b")
	r.frame(false)
	if g := r.tv.suggest.ghost; g != "it status" {
		t.Fatalf("ghost %q after the shell redrew", g)
	}
	// Text from elsewhere (a snippet inserted): the screen tells the line.
	r.term.WriteString("it s")
	r.frame(false)
	if g := r.tv.suggest.ghost; g != "tatus" {
		t.Fatalf("ghost %q after text from elsewhere", g)
	}
}

// TestSuggestGates: nothing on the alternate screen, with the mouse read,
// once a program runs (bracketed paste off), nor with completion off.
func TestSuggestGates(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(r *suggestRig)
	}{
		{"alternate screen", func(r *suggestRig) { r.term.WriteString("\x1b[?1049h") }},
		{"mouse", func(r *suggestRig) { r.term.WriteString("\x1b[?1000h") }},
		{"program", func(r *suggestRig) { r.term.WriteString("\x1b[?2004l") }},
		{"off", func(r *suggestRig) { r.tv.suggest.mode = settings.CompletionOff }},
		{"not on", func(r *suggestRig) { r.tv.suggest.on = false }},
	} {
		r := newSuggestRig(settings.CompletionAuto)
		r.src.cmds = []string{"git status"}
		r.text("git s")
		tc.do(r)
		r.frame(true)
		n := len(r.out)
		r.key(ui.KeyRight, 0)
		if r.tv.suggest.ghost != "" || r.tv.suggest.list.open || strings.Contains(r.sentSince(n), "tatus") {
			t.Errorf("%s: ghost %q, list %v, sent %q", tc.name, r.tv.suggest.ghost, r.tv.suggest.list.open, r.sentSince(n))
		}
	}
}

// TestReadsLine tells a shell reading a line by bracketed paste, else by
// its directory reports since the last Enter, else by its prompt.
func TestReadsLine(t *testing.T) {
	bp := vt.Modes{BracketedPaste: true}
	for _, tc := range []struct {
		m              vt.Modes
		paste          bool
		reports, enter uint64
		prompt         string
		want           bool
	}{
		{bp, true, 0, 0, "$ ", true},
		{vt.Modes{}, true, 5, 1, "$ ", false}, // paste seen before: a program runs
		{vt.Modes{}, false, 2, 1, "~ ", true}, // reported since Enter
		{vt.Modes{}, false, 2, 2, "$ ", false},
		{vt.Modes{}, false, 0, 0, "user@web:~$ ", true},
		{vt.Modes{}, false, 0, 0, "Password: ", false},
		{vt.Modes{}, false, 0, 0, ">>> ", false},
		{vt.Modes{BracketedPaste: true, AltScreen: true}, true, 0, 0, "$ ", false},
		{vt.Modes{BracketedPaste: true, Mouse: vt.MouseNormal}, true, 0, 0, "$ ", false},
		{bp, true, 0, 0, "   ", false},
	} {
		if got := readsLine(tc.m, tc.paste, tc.reports, tc.enter, tc.prompt); got != tc.want {
			t.Errorf("readsLine(%+v, %v, %d, %d, %q) = %v", tc.m, tc.paste, tc.reports, tc.enter, tc.prompt, got)
		}
	}
}

// TestEnterTellsLine: Enter tells the source the line run, and starts a
// new one; a line typed while a program runs is not told.
func TestEnterTellsLine(t *testing.T) {
	r := newSuggestRig(settings.CompletionInline)
	r.text("ls -la")
	r.key(ui.KeyEnter, 0)
	if !slices.Equal(r.src.runs, []string{"ls -la"}) {
		t.Fatalf("told %q", r.src.runs)
	}
	r.term.WriteString("\x1b[?2004l\r\nfile\r\n")
	r.text("secret")
	r.key(ui.KeyEnter, 0)
	if len(r.src.runs) != 1 {
		t.Fatalf("told %q while a program ran", r.src.runs)
	}
}

// TestPathList: the list opens by itself for a path in the automatic
// mode; Enter runs the line until ↑ or ↓ chose an entry, which Enter
// types escaped; Esc closes it for the word.
func TestPathList(t *testing.T) {
	r := newSuggestRig(settings.CompletionAuto)
	r.src.es = []cmdline.Entry{{Name: "src", Dir: true}, {Name: "My File.txt"}, {Name: "Makefile"}}
	r.text("cat M")
	r.frame(false)
	l := &r.tv.suggest.list
	if !l.shows() || len(l.shown) != 2 {
		t.Fatalf("list open %v, shown %v", l.open, l.shown)
	}
	r.key(ui.KeyDown, 0) // Makefile, then My File.txt
	n := len(r.out)
	r.key(ui.KeyEnter, 0)
	if got := r.sentSince(n); got != "y\\ File.txt" {
		t.Fatalf("Enter typed %q", got)
	}
	if l.open {
		t.Error("the list stays open after a file")
	}

	r = newSuggestRig(settings.CompletionAuto)
	r.src.es = []cmdline.Entry{{Name: "Makefile"}}
	r.text("cat M")
	r.frame(false)
	n = len(r.out)
	r.key(ui.KeyEnter, 0)
	if got := r.sentSince(n); got != "\r" {
		t.Fatalf("Enter with nothing chosen sent %q, want the line run", got)
	}

	r = newSuggestRig(settings.CompletionAuto)
	r.src.es = []cmdline.Entry{{Name: "Makefile"}}
	r.text("cat M")
	r.frame(false)
	r.key(ui.KeyEscape, 0)
	r.text("a")
	r.frame(false)
	if r.tv.suggest.list.open {
		t.Error("the list opened again on the word Esc closed it on")
	}

	// Inline only: none by itself; the shortcut lists folders alone for
	// cd, and Tab types the folder's name, its slash too.
	r = newSuggestRig(settings.CompletionInline)
	r.src.es = []cmdline.Entry{{Name: "src", Dir: true}, {Name: "setup.py"}}
	r.text("cd s")
	r.frame(false)
	if r.tv.suggest.list.open {
		t.Fatal("the list opened by itself in the inline mode")
	}
	r.frame(true)
	l = &r.tv.suggest.list
	if !l.shows() || len(l.shown) != 1 || l.shown[0].Name != "src" {
		t.Fatalf("cd lists %v", l.shown)
	}
	n = len(r.out)
	r.key(ui.KeyTab, 0)
	if got := r.sentSince(n); got != "rc/" {
		t.Fatalf("Tab typed %q", got)
	}
	if !l.open {
		t.Error("the list closed after a folder")
	}
}

// TestConnSuggestOrder: a host's own lines run come first, then its
// history files, then the other hosts' commands; a line starting with a
// blank is not kept.
func TestConnSuggestOrder(t *testing.T) {
	a, _ := newTestApp(t)
	web := connSuggest{a: a, cn: &conn{host: assets.Host{ID: "web"}}}
	db := connSuggest{a: a, cn: &conn{host: assets.Host{ID: "db"}}}
	st := a.suggests()
	st.host("web").files = []string{"tail -f /var/log/syslog"}
	st.host("db").files = []string{"tail -n 50 app.log", "psql -U admin"}

	if got := web.suggest("tail "); got != "-f /var/log/syslog" {
		t.Errorf("own history: %q", got)
	}
	web.ran("tail -n 5 x")
	if got := web.suggest("tail "); got != "-n 5 x" {
		t.Errorf("own line run: %q", got)
	}
	if got := web.suggest("psql"); got != " -U admin" {
		t.Errorf("another host's: %q", got)
	}
	db.ran(" export TOKEN=x")
	if got := db.suggest("export"); got != "" {
		t.Errorf("a line with a leading blank was kept: %q", got)
	}
	web.ran("ls")
	web.ran("tail -n 5 x")
	if !slices.Equal(st.host("web").ran, []string{"tail -n 5 x", "ls"}) {
		t.Errorf("ran %q", st.host("web").ran)
	}
}

// TestHistoryCmds keeps the commands typing can complete to, each once.
func TestHistoryCmds(t *testing.T) {
	h := shellhist.History{Entries: []shellhist.Entry{
		{Cmd: "make test"}, {Cmd: "for i in 1 2\ndo echo $i\ndone"}, {Cmd: "make test  "}, {Cmd: "printf 'a\tb'"}, {Cmd: "ls"},
	}}
	if got := historyCmds(h); !slices.Equal(got, []string{"make test", "ls"}) {
		t.Errorf("historyCmds = %q", got)
	}
}

// TestCanListPaths: not in a container's shell, nor on a server found
// without SFTP.
func TestCanListPaths(t *testing.T) {
	a, _ := newTestApp(t)
	cn := &conn{host: assets.Host{ID: "web"}}
	if !a.canListPaths(cn) {
		t.Error("a server's paths are not listed")
	}
	cn.container = true
	if a.canListPaths(cn) {
		t.Error("a container's paths are listed")
	}
	cn.container = false
	a.suggests().host("web").noSFTP = true
	if a.canListPaths(cn) {
		t.Error("paths listed on a server without SFTP")
	}
}

// TestCompletePathShortcut: the path list's shortcut is the app's.
func TestCompletePathShortcut(t *testing.T) {
	if !isAppShortcut(nil, ui.KeyJ, appMods) {
		t.Error("the path list's shortcut reaches the shell")
	}
}
