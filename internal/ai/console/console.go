// Package console drives an interactive terminal for the AI: it types one
// line as the user would, waits until the device or shell is back at its
// prompt, pages through "--More--", and stops where the terminal asks for
// a password or an answer. A terminal has no exit codes, so neither does
// a Result.
package console

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Terminal is the terminal the console types into and reads from.
type Terminal interface {
	// Input is where typed bytes go, bound to the connection of the call.
	Input(ctx context.Context) (io.Writer, error)
	// Watch gives fn the bytes printed from now on, until stop.
	Watch(fn func(p []byte)) (stop func())
	// CursorLine is the text of the line the cursor is on.
	CursorLine() string
	// FullScreen reports a full-screen program on the alternate screen.
	FullScreen() bool
}

// Status is how a wait ended.
type Status string

const (
	// AtPrompt is back at a prompt: the command has finished.
	AtPrompt Status = "prompt"
	// Password asks for a password, which only the user types.
	Password Status = "password"
	// Question asks for an answer, as [y/n] or [confirm].
	Question Status = "question"
	// FullScreen is a full-screen program, as an editor or less.
	FullScreen Status = "fullscreen"
	// Quiet printed nothing for a while without a prompt: it may still run.
	Quiet Status = "quiet"
	// Timeout ran out of time; the command may still run.
	Timeout Status = "timeout"
	// Limit printed more than is kept, or more pages than are turned.
	Limit Status = "limit"
)

// Result is what a line or a key printed, as text without the echo of
// the line typed, and how the wait for it ended.
type Result struct {
	Status Status
	Output string
	Pages  int
	Bytes  int64
}

// Options bound a wait. Zero fields take the defaults.
type Options struct {
	// Timeout is the longest wait; 60 seconds.
	Timeout time.Duration
	// Settle is how long the output stays quiet before the cursor's line
	// is read as a prompt; 300 ms.
	Settle time.Duration
	// Idle is how long quiet output without a prompt ends the wait; 10 s.
	Idle time.Duration
	// MaxPages is how many "--More--" pages are turned; 200.
	MaxPages int
	// MaxBytes is how much output is kept; 1 MB less 4 KB.
	MaxBytes int
	// Decode turns the bytes printed into text; nil takes them as UTF-8.
	Decode func([]byte) string
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = time.Minute
	}
	if o.Settle <= 0 {
		o.Settle = 300 * time.Millisecond
	}
	if o.Idle <= 0 {
		o.Idle = 10 * time.Second
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 200
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1<<20 - 4096
	}
	if o.Decode == nil {
		o.Decode = func(p []byte) string { return string(p) }
	}
	return o
}

// MaxLine is the longest line Line types.
const MaxLine = 4096

var (
	// ErrLine is a line that is empty, too long, or has a control
	// character in it.
	ErrLine = errors.New("console: a line is one line of printable text")
	// ErrKey is a key Key does not press.
	ErrKey = errors.New("console: unknown key")
	// ErrStopUnconfirmed means input was sent to a shared terminal and
	// the command's completion could not be confirmed before cancellation.
	ErrStopUnconfirmed = errors.New("console: command stop could not be confirmed")
)

// Keys are what Key presses.
var Keys = map[string][]byte{
	"enter":  {'\r'},
	"space":  {' '},
	"q":      {'q'},
	"ctrl_c": {0x03},
}

// Line types line and Enter, and waits for what it prints.
func Line(ctx context.Context, t Terminal, line string, o Options) (Result, error) {
	if err := checkLine(line); err != nil {
		return Result{}, err
	}
	return run(ctx, t, []byte(line+"\r"), line, o)
}

// Key presses one of Keys and waits for what it prints.
func Key(ctx context.Context, t Terminal, key string, o Options) (Result, error) {
	p, ok := Keys[key]
	if !ok {
		return Result{}, ErrKey
	}
	return run(ctx, t, p, "", o)
}

func checkLine(line string) error {
	if strings.TrimSpace(line) == "" || len(line) > MaxLine || !utf8.ValidString(line) {
		return ErrLine
	}
	for _, r := range line {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ErrLine
		}
	}
	return nil
}

var (
	pagerLine    = regexp.MustCompile(`(?i)^\s*(?:-+\s*\(?\s*|<-+\s*|\(\s*)more\b`)
	passwordLine = regexp.MustCompile(`(?i)\bpass(word|code|phrase)?\b[^:]{0,40}:\s*$`)
	questionLine = regexp.MustCompile(`(?i)(\[\s*(?:y(?:es)?\s*/\s*n(?:o)?|confirm)\s*\]|\(\s*y(?:es)?\s*/\s*n(?:o)?\s*\)|\?)(\s*\[[^\]]{0,40}\])?\s*:?\s*$`)
)

// promptEnds are the characters prompts end with: a shell's $, # and %,
// a device's > and #, Huawei and H3C's ], and the arrows of fancy shells.
const promptEnds = "$#%>]❯»"

// prompt reports whether line looks like a prompt: the one there was
// before the command, or one that ends as prompts do.
func prompt(line, before string) bool {
	l := strings.TrimRightFunc(line, unicode.IsSpace)
	if l == "" {
		return false
	}
	if before != "" && l == before {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(l)
	return len(l) <= 160 && strings.ContainsRune(promptEnds, last)
}

type capture struct {
	mu    sync.Mutex
	buf   []byte
	total int64
	at    time.Time
	max   int
}

func (c *capture) write(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(len(p), room)]...)
	}
	c.total += int64(len(p))
	c.at = time.Now()
}

func (c *capture) state() (last time.Time, total int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at, c.total
}

func (c *capture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf...)
}

func run(ctx context.Context, t Terminal, input []byte, echo string, o Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	o = o.withDefaults()
	before := strings.TrimRightFunc(t.CursorLine(), unicode.IsSpace)
	// A line is not typed into a password prompt or a full-screen
	// program: it would go in as the password, or as keys to the program.
	// A key may be pressed there, to cancel or to quit.
	if echo != "" {
		switch {
		case passwordLine.MatchString(before):
			return Result{Status: Password}, nil
		case t.FullScreen():
			return Result{Status: FullScreen}, nil
		}
	}
	w, err := t.Input(ctx)
	if err != nil {
		return Result{}, err
	}
	c := &capture{max: o.MaxBytes}
	stop := t.Watch(c.write)
	defer stop()
	start := time.Now()
	c.mu.Lock()
	c.at = start
	c.mu.Unlock()
	res := Result{}
	finish := func(s Status) Result {
		res.Status = s
		_, res.Bytes = c.state()
		res.Output = trimEcho(Flatten(o.Decode(c.bytes())), echo)
		return res
	}
	interrupted := func(err error) (Result, error) {
		return finish(Timeout), errors.Join(ctx.Err(), ErrStopUnconfirmed, err)
	}
	if n, err := writeWhenActive(ctx, w, input); err != nil {
		if n > 0 && ctx.Err() != nil {
			return interrupted(err)
		}
		return Result{}, err
	}
	tick := time.NewTicker(min(o.Settle/4, 50*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return interrupted(nil)
		case <-tick.C:
		}
		now := time.Now()
		last, total := c.state()
		quiet := now.Sub(last)
		switch {
		case total >= int64(o.MaxBytes):
			return finish(Limit), nil
		case now.Sub(start) >= o.Timeout:
			return finish(Timeout), nil
		case quiet < o.Settle:
			continue
		case t.FullScreen():
			return finish(FullScreen), nil
		}
		line := t.CursorLine()
		switch {
		case pagerLine.MatchString(line):
			if res.Pages >= o.MaxPages {
				return finish(Limit), nil
			}
			c.mu.Lock()
			c.at = now
			c.mu.Unlock()
			if _, err := writeWhenActive(ctx, w, []byte{' '}); err != nil {
				if ctx.Err() != nil {
					return interrupted(err)
				}
				return finish(Quiet), err
			}
			res.Pages++
		case passwordLine.MatchString(line):
			return finish(Password), nil
		case total > 0 && questionLine.MatchString(line):
			return finish(Question), nil
		case total > 0 && prompt(line, before):
			return finish(AtPrompt), nil
		case quiet >= o.Idle:
			return finish(Quiet), nil
		}
	}
}

func writeWhenActive(ctx context.Context, w io.Writer, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return w.Write(p)
}

// trimEcho drops the first line when it is the line typed, as the
// terminal echoes it back.
func trimEcho(out, echo string) string {
	if echo == "" {
		return out
	}
	first, rest, _ := strings.Cut(out, "\n")
	if strings.TrimSpace(first) == strings.TrimSpace(echo) {
		return rest
	}
	return out
}

// Flatten is terminal output as the lines it leaves: escape sequences
// dropped, carriage returns and backspaces writing over what was there,
// trailing blanks trimmed.
func Flatten(s string) string {
	var lines []string
	var cur []rune
	col := 0
	put := func(r rune) {
		if col < len(cur) {
			cur[col] = r
		} else {
			for len(cur) < col {
				cur = append(cur, ' ')
			}
			cur = append(cur, r)
		}
		col++
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == 0x1b {
			i += escapeLen(s[i:])
			continue
		}
		i += n
		switch {
		case r == '\n':
			lines = append(lines, strings.TrimRightFunc(string(cur), unicode.IsSpace))
			cur, col = cur[:0], 0
		case r == '\r':
			col = 0
		case r == '\b':
			col = max(0, col-1)
		case r == '\t':
			put(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		default:
			put(r)
		}
	}
	lines = append(lines, strings.TrimRightFunc(string(cur), unicode.IsSpace))
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// escapeLen is how long the escape sequence at the start of s is: CSI,
// OSC and the other strings up to their terminator, else ESC and one
// character.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']', 'P', '_', '^', 'X':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	case '(', ')', '*', '+', '#', '%':
		return min(3, len(s))
	default:
		return 2
	}
}
