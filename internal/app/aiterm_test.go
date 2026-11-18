package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/console"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
)

type busyTestWriter func([]byte) (int, error)

func (w busyTestWriter) Write(p []byte) (int, error) { return w(p) }

func TestBusyTerminalInputDoesNotRetryAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	w := busyWriter{ctx: ctx, w: busyTestWriter(func(p []byte) (int, error) {
		attempts++
		if attempts == 1 {
			cancel()
			return 0, session.ErrInputBusy
		}
		return len(p), nil
	})}
	if n, err := w.Write([]byte("show run\r")); n != 0 || err != context.Canceled || attempts != 1 {
		t.Fatalf("bytes=%d error=%v attempts=%d", n, err, attempts)
	}
}

func TestTerminalToolCancellationKeepsResultAndStopCause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tools := &termTools{}
	res := console.Result{Status: console.AtPrompt, Output: "finished"}
	if out, code, err := tools.result(ctx, res, nil); code != 0 || err != nil || !strings.Contains(out, "finished") {
		t.Fatalf("completed result lost: %q %d %v", out, code, err)
	}
	cleanup := errors.Join(context.Canceled, console.ErrStopUnconfirmed)
	if out, code, err := tools.result(ctx, console.Result{Status: console.Timeout}, cleanup); code != -1 || !errors.Is(err, console.ErrStopUnconfirmed) || !errors.Is(err, context.Canceled) || out != "status: timeout" {
		t.Fatalf("unconfirmed stop lost: %q %d %v", out, code, err)
	}
}

// fakeSwitch is a Telnet switch on ln that answers a few commands; lines
// is what was typed into it, a line at a time.
func fakeSwitch(t *testing.T, ln net.Listener) (lines func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("switch#"))
		var line []byte
		// iac is the Telnet command being skipped: 0 none, 1 its command
		// byte, 2 its option byte, 3 inside a subnegotiation, 4 an IAC there.
		iac := 0
		buf := make([]byte, 256)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			for _, b := range buf[:n] {
				switch {
				case iac == 1 && b == 250:
					iac = 3
				case iac == 1 && b >= 251:
					iac = 2
				case iac == 1, iac == 2:
					iac = 0
				case iac == 3 && b == 255:
					iac = 4
				case iac == 3:
				case iac == 4:
					iac = 3
					if b == 240 {
						iac = 0
					}
				case b == 255:
					iac = 1
				case b == '\r' || b == '\n':
					if len(line) == 0 {
						continue
					}
					cmd := string(line)
					line = nil
					mu.Lock()
					got = append(got, cmd)
					mu.Unlock()
					switch cmd {
					case "show version":
						c.Write([]byte(cmd + "\r\nCisco IOS 15.2\r\n --More-- "))
					case "reload":
						c.Write([]byte(cmd + "\r\nProceed with reload? [confirm]"))
					default:
						c.Write([]byte(cmd + "\r\n% Invalid input\r\nswitch#"))
					}
				case b == ' ' && len(line) == 0:
					c.Write([]byte("\r          \ruptime is 3 days\r\nswitch#"))
				case b == 0:
				default:
					line = append(line, b)
				}
			}
		}
	}()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// TestTelnetAssistant: an assistant opens on a Telnet host beside its
// terminal and types into it: a command's output comes back once the
// switch is back at its prompt, a page turned, and a confirmation stops
// the wait. What it types is graded by the device's rules.
func TestTelnetAssistant(t *testing.T) {
	a, tt := newTestApp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lines := fakeSwitch(t, ln)
	h, err := a.store.Add(assets.Host{Name: "switch", Protocol: assets.ProtoTelnet, Address: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenFiles(h.ID, openAt{}, nil); !errors.Is(err, errTerminalOnly) {
		t.Fatalf("files on telnet: %v", err)
	}
	a.OpenAIFor(h.ID)
	var term, ai *conn
	for _, c := range a.conns {
		switch c.kind {
		case layout.Terminal:
			term = c
		case layout.AI:
			ai = c
		}
	}
	if term == nil || ai == nil || ai.ai.link != term.id {
		t.Fatalf("terminal %v assistant %v", term, ai)
	}
	if aiTarget(ai.host) != agent.TargetDevice || ai.ai.agent != "builtin" {
		t.Fatalf("target %v agent %q", aiTarget(ai.host), ai.ai.agent)
	}
	waitFor(t, tt, "the prompt", func() bool {
		return term.s.Status().State == session.Connected && strings.Contains(term.s.Term.ScreenText(), "switch#")
	})

	tools := newTermTools(a, ai)
	call := func(name, args string) (string, int, error) {
		type result struct {
			out  string
			exit int
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			out, exit, err := tools.Call(context.Background(), name, []byte(args))
			ch <- result{out, exit, err}
		}()
		var r result
		waitFor(t, tt, name+" "+args, func() bool {
			select {
			case r = <-ch:
				return true
			default:
				return false
			}
		})
		return r.out, r.exit, r.err
	}
	out, exit, err := call("terminal_send", `{"command":"show version"}`)
	if err != nil || exit != 0 || !strings.HasPrefix(out, "status: prompt\npages turned: 1\n") ||
		!strings.Contains(out, "Cisco IOS 15.2") || !strings.Contains(out, "uptime is 3 days") {
		t.Fatalf("show version: %q %d %v", out, exit, err)
	}
	out, _, err = call("terminal_send", `{"command":"reload"}`)
	if err != nil || !strings.HasPrefix(out, "status: question") {
		t.Fatalf("reload: %q %v", out, err)
	}
	if got := lines(); strings.Join(got, "|") != "show version|reload" {
		t.Fatalf("typed %q", got)
	}
	if out, exit, _ := call("terminal_send", `{"command":"a\u0003b"}`); exit != 1 || len(lines()) != 2 {
		t.Fatalf("control character typed: %q", out)
	}
	if _, _, err := tools.ReadFile(context.Background(), "/etc/hosts"); !errors.Is(err, errNoFiles) {
		t.Fatalf("read file: %v", err)
	}

	eng := aiEngine(ai)
	if eng == nil {
		t.Fatal("no device engine")
	}
	if g := eng.Classify(policy.Action{Where: policy.Remote, Tool: "terminal_send", Command: "reload"}, policy.Scope{}); g.Risk != policy.RiskCritical {
		t.Fatalf("reload graded %v", g.Risk)
	}
}

// TestTerminalAssistantHosts: an assistant opens on this computer and on
// a serial host whose port is open, where it types into the terminal,
// and an external agent, whose tools need SSH, is refused there.
func TestTerminalAssistantHosts(t *testing.T) {
	a, _ := newTestApp(t)
	id, err := a.OpenAI(localID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := a.conns[id]
	if aiTarget(c.host) != agent.TargetShell || aiEngine(c) != nil {
		t.Fatalf("local target %v", aiTarget(c.host))
	}
	if _, err := a.OpenFiles(localID, openAt{}, nil); !errors.Is(err, errLocalTerminalOnly) {
		t.Fatalf("files on this computer: %v", err)
	}

	sr, err := a.store.Add(assets.Host{Protocol: assets.ProtoSerial, Serial: &assets.SerialLine{Device: "/dev/sshyard-test-missing"}})
	if err != nil {
		t.Fatal(err)
	}
	term, err := a.Connect(sr.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	pane := 0
	if _, n := a.ws.Locate(term); n != nil {
		pane = n.ID
	}
	aiID, err := a.OpenAI(sr.ID, openAt{pane: pane, edge: layout.Right}, term)
	if err != nil || a.conns[aiID].kind != layout.AI {
		t.Fatalf("assistant on an open serial port: %v", err)
	}

	c = a.conns[aiID]
	if err := a.grantConsent(sr.ID); err != nil {
		t.Fatal(err)
	}
	c.ai.agent = "acp:someone"
	a.SendAI(aiID, "hello")
	if n := len(c.ai.items); n == 0 || c.ai.items[n-1].text != "acp-ssh" {
		t.Fatalf("external agent on serial: %+v", c.ai.items)
	}
}
