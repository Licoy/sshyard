//go:build !windows

package app

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
)

func TestACPAppProtocolAndLifecycle(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fakeagent")
	command := exec.Command("go", "build", "-o", binary, "github.com/Licoy/sshyard/internal/ai/acp/internal/fakeagent")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fakeagent: %v %s", err, output)
	}
	t.Run("message-file-confirm-redact", func(t *testing.T) {
		a, tt, _, h := bashHost(t)
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "config")
		if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(root, "script")
		body := "---\nthought checking host\nplan update config\nwrite " + target + " after\nterm echo checked\nmessage ACP finished\n"
		if err := os.WriteFile(script, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		s := a.Settings()
		s.AIDefaultAgent = "acp"
		s.AIACPAgents = []settings.ACPConfig{{Command: binary, Args: []string{script}}}
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
		id := openAIPane(t, a, h.ID)
		tt.Frame()
		c := a.conns[id]
		a.SendAI(id, "update AKIA0000000000000000")
		waitFor(t, tt, "ACP write confirmation", func() bool { return c.ai.pending != nil })
		if c.ai.pending.prompt.Action.Path != target {
			t.Fatalf("wrong bound path: %+v", c.ai.pending.prompt.Action)
		}
		if data, _ := os.ReadFile(target); string(data) != "before" {
			t.Fatal("file written before permission")
		}
		mustClick(t, tt, "Allow once")
		waitFor(t, tt, "ACP turn completed", func() bool { return !c.ai.running && paneHas(a, id, "ACP finished") })
		if data, err := os.ReadFile(target); err != nil || string(data) != "after" {
			t.Fatalf("file=%q error=%v", data, err)
		}
		r := c.ai.acp
		if r == nil {
			t.Fatalf("ACP runtime unexpectedly closed: %+v", c.ai.items)
		}
		diagnostic := r.session.Diagnostics()
		if strings.Contains(diagnostic, "AKIA0000000000000000") || !strings.Contains(diagnostic, "[REDACTED-AWS-KEY]") {
			t.Fatalf("prompt was not redacted: %s", diagnostic)
		}
		if !strings.Contains(diagnostic, "term exit=0") {
			t.Fatalf("remote terminal was not invoked: %s", diagnostic)
		}
		cfg := a.hostConfig(h, c, func(context.Context) (session.Credentials, error) { return a.credentials(c, h) })
		shared, release, err := a.pool.Acquire(t.Context(), a.poolKey(h), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		// A second prompt keeps the process and its conversation
		a.SendAI(id, "again")
		waitFor(t, tt, "second permission", func() bool { return c.ai.pending != nil })
		a.StopAI(id)
		waitFor(t, tt, "ACP stopped", func() bool { return !c.ai.running && c.ai.pending == nil })
		a.CloseSession(id)
		waitFor(t, tt, "ACP process sandbox removed", func() bool { _, err := os.Stat(r.session.Sandbox()); return os.IsNotExist(err) })
		if a.pool.Shared(a.poolKey(h)) != 1 {
			t.Fatal("ACP did not release only its own pool reference")
		}
		command, err := shared.NewSession()
		if err != nil {
			t.Fatalf("ACP stop closed shared SSH: %v", err)
		}
		if output, err := command.Output("printf shared"); err != nil || string(output) != "shared" {
			t.Fatalf("shared SSH after ACP stop: %q %v", output, err)
		}
		command.Close()

	})
	t.Run("sensitive-read-and-app-close", func(t *testing.T) {
		a, tt, _, h := bashHost(t)
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(root, "secret")
		if err := os.WriteFile(secret, []byte("token=AKIA0000000000000000"), 0600); err != nil {
			t.Fatal(err)
		}
		h.AIPolicy.Sensitive = []string{secret}
		if _, err := a.store.Update(h); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(root, "script")
		if err := os.WriteFile(script, []byte("---\nread "+secret+"\nmessage read finished\n"), 0600); err != nil {
			t.Fatal(err)
		}
		s := a.Settings()
		s.AIDefaultAgent = "acp"
		s.AIACPAgents = []settings.ACPConfig{{Command: binary, Args: []string{script}}}
		s.AIConsentHosts = nil
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
		id := openAIPane(t, a, h.ID)
		c := a.conns[id]
		a.SendAI(id, "read")
		if a.consent == nil || c.ai.acp != nil {
			t.Fatal("ACP started before first-use consent")
		}
		if err := a.grantConsent(h.ID); err != nil {
			t.Fatal(err)
		}
		a.consent = nil
		a.SendAI(id, "read")
		waitFor(t, tt, "sensitive read confirmation", func() bool { return c.ai.pending != nil })
		if c.ai.pending.prompt.Action.Tool != "read_file" || c.ai.pending.prompt.Action.Path != secret {
			t.Fatal("sensitive read not authorized independently")
		}
		mustClick(t, tt, "Allow once")
		waitFor(t, tt, "read completed", func() bool { return !c.ai.running && paneHas(a, id, "read finished") })
		r := c.ai.acp
		if diagnostic := r.session.Diagnostics(); strings.Contains(diagnostic, "AKIA0000000000000000") || !strings.Contains(diagnostic, "read \"token=[REDACTED]\"") {
			t.Fatal("remote sensitive read was not redacted")
		}
		a.Close()
		if _, err := os.Stat(r.session.Sandbox()); !os.IsNotExist(err) {
			t.Fatal("App.Close retained ACP process sandbox")
		}
		if a.pool.Shared(a.poolKey(h)) != 0 {
			t.Fatal("App.Close retained ACP remote connection")
		}
	})
	t.Run("authentication-visible", func(t *testing.T) {
		a, tt, _, h := bashHost(t)
		script := filepath.Join(t.TempDir(), "script")
		if err := os.WriteFile(script, []byte("auth agent login Sign in\n---\nmessage should not run\n"), 0600); err != nil {
			t.Fatal(err)
		}
		s := a.Settings()
		s.AIDefaultAgent = "acp"
		s.AIACPAgents = []settings.ACPConfig{{Command: binary, Args: []string{script}}}
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
		id := openAIPane(t, a, h.ID)
		a.SendAI(id, "hello")
		waitFor(t, tt, "ACP authentication error", func() bool { return !a.conns[id].ai.running && tt.HasText(locales.In("en").AiACPAuthRequired()) })
	})
	t.Run("conversation-carried-on", func(t *testing.T) {
		a, tt, _, h := bashHost(t)
		script := filepath.Join(t.TempDir(), "script")
		write := func(body string) {
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		write("load\nreplay replayed answer\n---\nmessage answered\n")
		s := a.Settings()
		s.AIDefaultAgent = "acp"
		s.AIACPAgents = []settings.ACPConfig{{Command: binary, Args: []string{script}}}
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
		id := openAIPane(t, a, h.ID)
		c := a.conns[id]
		ask := func(q string) *acpRuntime {
			t.Helper()
			a.SendAI(id, q)
			r := c.ai.acp
			waitFor(t, tt, "answer to "+q, func() bool { return !c.ai.running })
			if r == nil || c.ai.acp != r {
				t.Fatalf("%q: runtime closed: %+v", q, c.ai.items)
			}
			return r
		}
		first := ask("first question")
		cwd := first.session.Sandbox()
		if c.ai.acpSID != "sess-fake" || c.ai.acpCwd != cwd || c.ai.acpAgent != c.ai.agent {
			t.Fatalf("session %q of %q in %q", c.ai.acpSID, c.ai.acpAgent, c.ai.acpCwd)
		}
		if d := first.session.Diagnostics(); strings.Contains(d, "sshyard://conversation") {
			t.Fatalf("a new conversation was told an earlier one: %s", d)
		}
		conv := c.ai.conv
		if saved, err := a.chatHistory().Load(h.ID, conv); err != nil || saved.ACP == nil || saved.ACP.ID != "sess-fake" || saved.ACP.Cwd != cwd {
			t.Fatalf("saved session %+v, %v", saved.ACP, err)
		}

		// Reopened, the agent loads its session in its sandbox; what it
		// replays is not shown again and it is not told the conversation.
		a.NewChat(id)
		a.OpenChat(id, conv)
		if c.ai.acpSID != "sess-fake" {
			t.Fatalf("reopened without its session: %q", c.ai.acpSID)
		}
		second := ask("second question")
		d := second.session.Diagnostics()
		if !second.session.Resumed() || !strings.Contains(d, "load sess-fake cwd "+cwd) || strings.Contains(d, "sshyard://conversation") {
			t.Fatalf("not resumed: %s", d)
		}
		if paneHas(a, id, "replayed answer") || !paneHas(a, id, "first question") {
			t.Fatalf("pane %+v", c.ai.items)
		}

		// An agent that cannot load is told the conversation, once.
		write("---\nmessage answered\n")
		a.StopAI(id)
		third := ask("third question")
		ask("fourth question")
		d = third.session.Diagnostics()
		if third.session.Resumed() || strings.Contains(d, "load ") || strings.Count(d, "sshyard://conversation") != 1 ||
			!strings.Contains(d, "User: first question") || !strings.Contains(d, "User: second question") {
			t.Fatalf("not told the conversation once: %s", d)
		}
		if strings.Contains(d, "User: third question") {
			t.Fatalf("told the question it is asked: %s", d)
		}
	})
	t.Run("scope-update-cancels-process", func(t *testing.T) {
		a, tt, _, h := bashHost(t)
		script := filepath.Join(t.TempDir(), "script")
		if err := os.WriteFile(script, []byte("---\nmessage started\nignore-cancel\n"), 0600); err != nil {
			t.Fatal(err)
		}
		s := a.Settings()
		s.AIDefaultAgent = "acp"
		s.AIACPAgents = []settings.ACPConfig{{Command: binary, Args: []string{script}}}
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
		id := openAIPane(t, a, h.ID)
		a.SendAI(id, "hold")
		waitFor(t, tt, "ACP running", func() bool { return paneHas(a, id, "started") })
		c := a.conns[id]
		r := c.ai.acp
		h.AIPolicy.Sensitive = []string{"/private"}
		if _, err := a.store.Update(h); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		waitFor(t, tt, "scope change stops ACP", func() bool { return !c.ai.running && c.ai.acp == nil })
		waitFor(t, tt, "scope change closes process", func() bool { _, err := os.Stat(r.session.Sandbox()); return os.IsNotExist(err) })
	})
	t.Run("close-with-stalled-host", func(t *testing.T) {
		grace := acpCloseGrace
		acpCloseGrace = 100 * time.Millisecond
		t.Cleanup(func() { acpCloseGrace = grace })
		a, tt, srv, h := bashHost(t)
		stall := stallProxy(t, srv.Addr)
		_, h.Port = splitAddr(t, stall.addr)
		h, err := a.store.Update(h)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.known.Add(h.Addr(), srv.HostKey.PublicKey()); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(t.TempDir(), "script")
		if err := os.WriteFile(script, []byte("---\nmessage answered\n"), 0600); err != nil {
			t.Fatal(err)
		}
		s := a.Settings()
		s.AIDefaultAgent = "acp"
		s.AIACPAgents = []settings.ACPConfig{{Command: binary, Args: []string{script}}}
		if err := a.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
		id := openAIPane(t, a, h.ID)
		a.SendAI(id, "hi")
		waitFor(t, tt, "ACP answer", func() bool { return paneHas(a, id, "answered") })
		r := a.conns[id].ai.acp
		// The host stops answering: closing the SFTP session waits for a
		// reply that never comes.
		stall.stalled.Store(true)
		t.Cleanup(func() { stall.stalled.Store(false) })
		closed := make(chan struct{})
		go func() {
			a.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(acpQuitWait / 2):
			t.Fatal("quitting waits for a host that stopped answering")
		}
		if _, err := os.Stat(r.session.Sandbox()); !os.IsNotExist(err) {
			t.Fatal("agent left running")
		}
	})
}

// stallProxy forwards to addr until stalled, then holds everything, as a
// network that drops without resetting the connection.
type stalling struct {
	addr    string
	stalled atomic.Bool
}

func stallProxy(t *testing.T, addr string) *stalling {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	p := &stalling{addr: ln.Addr().String()}
	pipe := func(dst, src net.Conn) {
		defer dst.Close()
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			for p.stalled.Load() {
				time.Sleep(10 * time.Millisecond)
			}
			if n > 0 {
				if _, err := dst.Write(buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", addr)
			if err != nil {
				c.Close()
				continue
			}
			go pipe(up, c)
			go pipe(c, up)
		}
	}()
	return p
}

func TestACPConfigurationUIAndUnknownBackend(t *testing.T) {
	fakeACPScan(t)
	a, tt := newTestApp(t)
	tt.SetSize(900, 850)
	a.openSettings(prefAI)
	tt.Frame()
	mustClick(t, tt, "Configure ACP Agents")
	tt.Frame()
	mustClick(t, tt, "Add custom command")
	tt.Frame()
	tt.Type("/custom-acp")
	if a.acpForm.command != "/custom-acp" {
		t.Fatal("ACP executable did not autofocus")
	}
	// Drive the form handler because TextInput owns its own frame state
	a.acpForm.command = "/usr/local/bin/custom-acp"
	a.acpForm.args = "--stdio\nargument with spaces"
	if !a.saveACP(locales.In("en")) {
		t.Fatal(a.acpForm.err)
	}
	if got := a.Settings().AIACPAgents; len(got) != 1 || got[0].ID != "custom" || got[0].Command != "/usr/local/bin/custom-acp" || len(got[0].Args) != 2 || got[0].Args[1] != "argument with spaces" {
		t.Fatalf("config=%+v", got)
	}
	h := savedHost(t, a, "unknown", "127.0.0.1")
	consentHost(t, a, h.ID)
	s := a.Settings()
	s.AIDefaultAgent = "missing-agent"
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.page = pageWorkspace
	id := openAIPane(t, a, h.ID)
	a.SendAI(id, "hello")
	tt.Frame()
	if !tt.HasText(locales.In("en").AiBackendUnavailable()) || a.conns[id].ai.sess != nil {
		t.Fatal("unknown backend silently used built-in")
	}
}

func TestUnrelatedProviderChangesKeepTurnRunning(t *testing.T) {
	a, _ := newTestApp(t)
	h := savedHost(t, a, "provider", "127.0.0.1")
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{{ID: "used", Name: "Used"}, {ID: "other", Name: "Other"}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	id := openAIPane(t, a, h.ID)
	c := a.conns[id]
	c.ai.provider = "used"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.ai.cancel = cancel
	c.ai.running = true
	s = a.Settings()
	s.AIProviders = append([]settings.AIProvider(nil), s.AIProviders...)
	s.AIProviders[1].Name = "Changed"
	s.AIDefaultProvider = "other"
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || c.ai.dirty {
		t.Fatal("unrelated provider change cancelled the current turn")
	}
	if err := a.deleteProvider("other"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("deleting an unused provider cancelled the current turn")
	}
}

func TestACPBackendMenuIsScopedToOnePane(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.AIACPAgents = []settings.ACPConfig{{Command: "/opt/first-acp"}, {Command: "/opt/second-acp"}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	h := savedHost(t, a, "backend", "127.0.0.1")
	first := openAIPane(t, a, h.ID)
	second, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.menu = dropMenu{key: "aiagent:" + strconv.Itoa(second), sub: -1}
	tt.Frame()
	mustClick(t, tt, "second-acp")
	tt.Frame()
	if a.conns[first].ai.agent != "builtin" || a.conns[second].ai.agent != "acp:custom-2" {
		t.Fatalf("backend menu changed a different pane: %q %q", a.conns[first].ai.agent, a.conns[second].ai.agent)
	}
}
