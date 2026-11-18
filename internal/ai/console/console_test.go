package console

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTerm is a terminal on a device that answers what is typed with
// reply, as the device would, a moment later.
type fakeTerm struct {
	mu           sync.Mutex
	out          string
	typed        strings.Builder
	watchers     map[int]func([]byte)
	next         int
	alt          bool
	reply        func(f *fakeTerm, typed string)
	onWatch      func()
	onCursorLine func(string)
}

func newTerm(prompt string, reply func(f *fakeTerm, typed string)) *fakeTerm {
	return &fakeTerm{out: prompt, watchers: map[int]func([]byte){}, reply: reply}
}

func (f *fakeTerm) Input(context.Context) (io.Writer, error) { return f, nil }

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.typed.Write(p)
	f.mu.Unlock()
	go func() {
		time.Sleep(5 * time.Millisecond)
		f.reply(f, string(p))
	}()
	return len(p), nil
}

func (f *fakeTerm) Watch(fn func([]byte)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next
	f.next++
	f.watchers[id] = fn
	if f.onWatch != nil {
		f.onWatch()
	}
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.watchers, id)
	}
}

func (f *fakeTerm) CursorLine() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := Flatten(f.out)
	line := text[strings.LastIndexByte(text, '\n')+1:]
	if f.onCursorLine != nil {
		f.onCursorLine(line)
	}
	return line
}

func (f *fakeTerm) FullScreen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alt
}

func (f *fakeTerm) print(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out += s
	for _, w := range f.watchers {
		w([]byte(s))
	}
}

func (f *fakeTerm) sent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typed.String()
}

// fast keeps the waits of a test short.
var fast = Options{Settle: 30 * time.Millisecond, Idle: 300 * time.Millisecond, Timeout: 3 * time.Second}

// switchCLI is a switch that knows a few commands.
func switchCLI(f *fakeTerm, typed string) {
	switch typed {
	case "show version\r":
		f.print("show version\r\nCisco IOS 15.2\r\nuptime is 3 days\r\nswitch#")
	case "conf t\r":
		f.print("conf t\r\nEnter configuration commands.\r\nswitch(config)#")
	case "show run\r":
		f.print("show run\r\nhostname switch\r\nvlan 10\r\n --More-- ")
	case " ":
		f.print("\r          \r\x1b[Kinterface Gi0/1\r\nend\r\nswitch#")
	case "enable\r":
		f.print("enable\r\nPassword: ")
	case "reload\r":
		f.print("reload\r\nProceed with reload? [confirm]")
	case "ping 10.0.0.1 repeat 1000\r":
		f.print("ping 10.0.0.1 repeat 1000\r\n!!!!!!!!")
	case "q":
		f.mu.Lock()
		f.alt = false
		f.mu.Unlock()
		f.print("switch#")
	}
}

// TestLineWaitsForPrompt: a line is typed with Enter, and the result is
// what it printed, without its echo, once the device is back at its
// prompt, the same or a new one.
func TestLineWaitsForPrompt(t *testing.T) {
	f := newTerm("switch#", switchCLI)
	res, err := Line(t.Context(), f, "show version", fast)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != AtPrompt || res.Output != "Cisco IOS 15.2\nuptime is 3 days\nswitch#" || f.sent() != "show version\r" {
		t.Fatalf("%+v typed %q", res, f.sent())
	}
	res, err = Line(t.Context(), f, "conf t", fast)
	if err != nil || res.Status != AtPrompt || !strings.HasSuffix(res.Output, "switch(config)#") {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestLinePagesThroughMore: a "--More--" page is turned with a space and
// the pages come back as one output.
func TestLinePagesThroughMore(t *testing.T) {
	f := newTerm("switch#", switchCLI)
	res, err := Line(t.Context(), f, "show run", fast)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != AtPrompt || res.Pages != 1 || f.sent() != "show run\r " {
		t.Fatalf("%+v typed %q", res, f.sent())
	}
	for _, want := range []string{"hostname switch", "vlan 10", "interface Gi0/1", "end"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("output %q lacks %q", res.Output, want)
		}
	}
	if strings.Contains(res.Output, "More") {
		t.Fatalf("pager left in %q", res.Output)
	}
}

// TestLineMaxPages: past the pages it turns, a wait ends at Limit.
func TestLineMaxPages(t *testing.T) {
	f := newTerm("switch#", func(f *fakeTerm, typed string) { f.print("\r\nmore lines\r\n--More--") })
	o := fast
	o.MaxPages = 2
	res, err := Line(t.Context(), f, "show log", o)
	if err != nil || res.Status != Limit || res.Pages != 2 || f.sent() != "show log\r  " {
		t.Fatalf("%+v %v typed %q", res, err, f.sent())
	}
}

// TestLineStopsForPasswordAndQuestion: a password prompt and a question
// end the wait, and a line is never typed into a password prompt.
func TestLineStopsForPasswordAndQuestion(t *testing.T) {
	f := newTerm("switch>", switchCLI)
	res, err := Line(t.Context(), f, "enable", fast)
	if err != nil || res.Status != Password {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = Line(t.Context(), f, "hunter2", fast)
	if err != nil || res.Status != Password || f.sent() != "enable\r" {
		t.Fatalf("typed into the password prompt: %+v %v typed %q", res, err, f.sent())
	}

	f = newTerm("switch#", switchCLI)
	res, err = Line(t.Context(), f, "reload", fast)
	if err != nil || res.Status != Question || !strings.Contains(res.Output, "[confirm]") {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestLineQuietWithoutPrompt: output that stops without a prompt ends
// the wait as Quiet, the command maybe still running.
func TestLineQuietWithoutPrompt(t *testing.T) {
	f := newTerm("switch#", switchCLI)
	res, err := Line(t.Context(), f, "ping 10.0.0.1 repeat 1000", fast)
	if err != nil || res.Status != Quiet || res.Output != "!!!!!!!!" {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestLineTimeoutAndCancel: a wait ends at its timeout, and at once when
// its context is cancelled.
func TestLineTimeoutAndCancel(t *testing.T) {
	busy := func(f *fakeTerm, typed string) {
		for range 20 {
			f.print(".")
			time.Sleep(10 * time.Millisecond)
		}
	}
	o := fast
	o.Timeout = 100 * time.Millisecond
	res, err := Line(t.Context(), newTerm("$ ", busy), "make", o)
	if err != nil || res.Status != Timeout {
		t.Fatalf("%+v %v", res, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = Line(ctx, newTerm("$ ", busy), "make", fast)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCancelBeforeTerminalWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := newTerm("switch#", switchCLI)
	f.onWatch = cancel
	_, err := Line(ctx, f, "show run", fast)
	if err != context.Canceled || f.sent() != "" {
		t.Fatalf("error %v typed %q", err, f.sent())
	}
}

func TestCancelBeforeNextPage(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := newTerm("switch#", switchCLI)
	f.onCursorLine = func(line string) {
		if pagerLine.MatchString(line) {
			cancel()
		}
	}
	res, err := Line(ctx, f, "show run", fast)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrStopUnconfirmed) || res.Pages != 0 || f.sent() != "show run\r" {
		t.Fatalf("result %+v error %v typed %q", res, err, f.sent())
	}
}

// TestLineRefusesControlCharacters: a line is one line of printable
// text; nothing else is typed.
func TestLineRefusesControlCharacters(t *testing.T) {
	f := newTerm("switch#", switchCLI)
	for _, line := range []string{"", "  ", "show\rreload", "show\nreload", "a\x03b", "a\x1b[Ab", "a\u0085b", strings.Repeat("x", MaxLine+1)} {
		if _, err := Line(t.Context(), f, line, fast); !errors.Is(err, ErrLine) {
			t.Errorf("%q: %v", line, err)
		}
	}
	if _, err := Key(t.Context(), f, "f1", fast); !errors.Is(err, ErrKey) {
		t.Errorf("key: %v", err)
	}
	if f.sent() != "" {
		t.Fatalf("typed %q", f.sent())
	}
}

// TestFullScreen: no line goes into a full-screen program, while a key
// may quit it.
func TestFullScreen(t *testing.T) {
	f := newTerm("", switchCLI)
	f.alt = true
	res, err := Line(t.Context(), f, "show version", fast)
	if err != nil || res.Status != FullScreen || f.sent() != "" {
		t.Fatalf("%+v %v typed %q", res, err, f.sent())
	}
	res, err = Key(t.Context(), f, "q", fast)
	if err != nil || res.Status != AtPrompt || f.sent() != "q" {
		t.Fatalf("%+v %v typed %q", res, err, f.sent())
	}
}

// TestFlatten: escape sequences go, and carriage returns and backspaces
// write over the line.
func TestFlatten(t *testing.T) {
	for in, want := range map[string]string{
		"a\x1b[31mb\x1b[0mc\r\n":         "abc",
		"abc\rX":                         "Xbc",
		"ab\bX":                          "aX",
		"\x1b]0;title\x07ok":             "ok",
		"--More--\r        \rnext\r\n":   "next",
		"one\r\ntwo  \r\n\r\nthree\r\n":  "one\ntwo\n\nthree",
		"tab\there":                      "tab here",
		"\x1b(Bplain \x1b=\x1b[?1hthing": "plain thing",
	} {
		if got := Flatten(in); got != want {
			t.Errorf("Flatten(%q) = %q, want %q", in, got, want)
		}
	}
}
