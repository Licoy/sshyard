package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/console"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
)

// aiTarget is what h's assistant reaches: an SSH host's files and
// commands, else the terminal it types into.
func aiTarget(h assets.Host) agent.Target {
	switch {
	case hasSSH(h):
		return agent.TargetSSH
	case isLocal(h.ID):
		return agent.TargetShell
	default:
		return agent.TargetDevice
	}
}

// aiTargetOf is what c's assistant reaches: aiTarget, or an SSH host's
// linked terminal while the pane types into it.
func aiTargetOf(c *conn) agent.Target {
	t := aiTarget(c.host)
	if t == agent.TargetSSH && c.ai != nil && c.ai.inTerm {
		return agent.TargetSSHTerminal
	}
	return t
}

// aiEngine grades c's actions: a device's command line has its own rules,
// and what is typed into an SSH host's terminal switched to root is
// graded as sudo would make it.
func aiEngine(c *conn) policy.Engine {
	switch aiTargetOf(c) {
	case agent.TargetDevice:
		return policy.NewDeviceEngine()
	case agent.TargetSSHTerminal:
		return policy.NewRootEngine(c.ai.user.root.Load)
	}
	return nil
}

// aiConnection says how a terminal target is reached, for the host
// context.
func (a *App) aiConnection(h assets.Host) string {
	switch {
	case isLocal(h.ID):
		return "local shell " + a.shell(h.ID).Name
	case h.Protocol == assets.ProtoSerial && h.Serial != nil:
		return "serial port " + h.Serial.Device
	case h.Protocol == assets.ProtoSerial:
		return "serial port"
	case h.Protocol == assets.ProtoTelnet:
		return "Telnet"
	}
	return ""
}

// errNoFiles is a file read on a terminal target, which has none.
var errNoFiles = errors.New("this assistant has no file access: it types into the terminal")

// termTools is the tool layer of an assistant on a Telnet, serial or
// local terminal: it reads the pane's linked terminal and types into it.
type termTools struct {
	a *App
	c *conn
	// state is c's assistant as the tools were made, which an SSH host's
	// tools (ssh) tell who runs the terminal after each line.
	state   *aiState
	ssh     bool
	counted bool
	nbytes  int64
}

func newTermTools(a *App, c *conn) *termTools {
	return &termTools{a: a, c: c, state: c.ai, ssh: aiTargetOf(c) == agent.TargetSSHTerminal}
}

// OutputBytes is what the last line or key printed; negative when the
// last call typed nothing.
func (t *termTools) OutputBytes() int64 {
	if !t.counted {
		return -1
	}
	return t.nbytes
}

func (t *termTools) ReadFile(context.Context, string) (string, bool, error) {
	return "", false, errNoFiles
}

// termWait is the longest a terminal tool waits, in seconds.
const termWait = 600

func (t *termTools) Call(ctx context.Context, name string, raw json.RawMessage) (string, int, error) {
	t.counted = false
	var args struct {
		Command string `json:"command"`
		Key     string `json:"key"`
		Timeout int64  `json:"timeout"`
		Lines   int    `json:"lines"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "invalid tool arguments", 1, err
		}
	}
	switch name {
	case "pane_output":
		return trimLines(t.a.aiOutput(t.c), args.Lines), 0, nil
	case "terminal_send", "terminal_key":
	default:
		return "unknown tool " + name, 1, nil
	}
	s := t.linked()
	if s == nil {
		return "no terminal is linked: the user opens this host's terminal in the assistant's tab", 1, nil
	}
	o := console.Options{Decode: s.Term.OutputEncoding().Decode}
	if args.Timeout > 0 {
		o.Timeout = time.Duration(min(args.Timeout, termWait)) * time.Second
	}
	term := sessionTerm{s}
	var res console.Result
	var err error
	if name == "terminal_send" {
		res, err = console.Line(ctx, term, args.Command, o)
	} else {
		res, err = console.Key(ctx, term, args.Key, o)
	}
	if t.ssh && ctx.Err() == nil {
		// What was typed may have been su or exit: the next line is
		// graded as whoever runs the terminal now.
		t.a.readTermUser(ctx, t.state, s, "")
	}
	return t.result(ctx, res, err)
}

func (t *termTools) result(ctx context.Context, res console.Result, err error) (string, int, error) {
	if errors.Is(err, console.ErrLine) || errors.Is(err, console.ErrKey) {
		return err.Error(), 1, nil
	}
	t.nbytes, t.counted = res.Bytes, true
	text := "status: " + string(res.Status)
	if res.Pages > 0 {
		text += "\npages turned: " + strconv.Itoa(res.Pages)
	}
	if res.Output != "" {
		text += "\n" + res.Output
	}
	if ctx.Err() != nil {
		return text, -1, ctx.Err()
	}
	if err != nil {
		return joinOut(text, err.Error()), 1, nil
	}
	switch res.Status {
	case console.Timeout, console.Limit:
		return text, 1, nil
	}
	return text, 0, nil
}

// linked is the session of the terminal the pane is linked to, read on
// the UI thread; nil when there is none.
func (t *termTools) linked() *session.Session {
	ch := make(chan *session.Session, 1)
	t.a.post(func() {
		var s *session.Session
		if t.c.ai != nil {
			if l := t.a.conns[t.c.ai.link]; l != nil && l.kind == layout.Terminal {
				s = l.s
			}
		}
		ch <- s
	})
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case s := <-ch:
		return s
	case <-timer.C:
		return nil
	}
}

// sessionTerm is a session as a console.Terminal.
type sessionTerm struct{ s *session.Session }

func (t sessionTerm) Input() (io.Writer, error) {
	w, err := t.s.CaptureInput()
	if err != nil {
		return nil, err
	}
	return busyWriter{w}, nil
}

func (t sessionTerm) Watch(fn func([]byte)) func() { return t.s.Watch(fn) }

func (t sessionTerm) CursorLine() string { return t.s.Term.CursorLine() }

func (t sessionTerm) FullScreen() bool { return t.s.Term.Modes().AltScreen }

// busyWriter waits out a paste being written, as the user's own typing
// would, for up to two seconds.
type busyWriter struct{ w io.Writer }

func (b busyWriter) Write(p []byte) (int, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := b.w.Write(p)
		if !errors.Is(err, session.ErrInputBusy) || time.Now().After(deadline) {
			return n, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
