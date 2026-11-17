package app

import (
	"testing"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/session"
)

// TestAITerminalMode: an SSH host's assistant turned to the terminal goes
// on with the terminal's tools, its lines graded as root's while the
// terminal runs as root, and back on its own channels when turned off.
func TestAITerminalMode(t *testing.T) {
	a, _ := newTestApp(t)
	h := savedHost(t, a, "web", "127.0.0.1")
	a.newModel = func(*conn) agent.Streamer { return echoModel{} }
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := a.conns[id]
	if err := a.ensureAI(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ai.tools.(*hostTools); !ok || c.ai.target != agent.TargetSSH {
		t.Fatalf("tools %T target %v", c.ai.tools, c.ai.target)
	}
	sess := c.ai.sess
	c.ai.running = true
	a.SetAITerminal(id, true)
	if c.ai.inTerm {
		t.Fatal("switched during a turn")
	}
	c.ai.running = false
	a.SetAITerminal(id, true)
	if err := a.ensureAI(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ai.tools.(*termTools); !ok || c.ai.target != agent.TargetSSHTerminal || c.ai.sess == sess {
		t.Fatalf("in the terminal: tools %T target %v", c.ai.tools, c.ai.target)
	}
	ls := policy.Action{Where: policy.Remote, Tool: "terminal_send", Command: "ls /root"}
	eng := aiEngine(c)
	if g := eng.Classify(ls, policy.Scope{}); g.Risk != policy.RiskReadOnly {
		t.Fatalf("as the login user: %v", g.Risk)
	}
	c.ai.user.root.Store(true)
	if g := eng.Classify(ls, policy.Scope{}); g.Risk != policy.RiskMedium {
		t.Fatalf("as root: %v", g.Risk)
	}
	a.SetAITerminal(id, false)
	if err := a.ensureAI(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ai.tools.(*hostTools); !ok || c.ai.target != agent.TargetSSH || aiEngine(c) != nil {
		t.Fatalf("back: tools %T target %v", c.ai.tools, c.ai.target)
	}
}

// TestNoteTermUser: a read is kept for the terminal it was of, a read of
// a terminal since left or reconnected is not, and probeTries reads in a
// row finding no shell stop the reads.
func TestNoteTermUser(t *testing.T) {
	s := &session.Session{}
	state := &aiState{}
	st := &state.user
	st.of, st.conn, st.busy = s, 1, true
	a, _ := newTestApp(t)
	root := session.TermUser{LoginUID: 1000, Login: "ops", UID: 0, Name: "root"}
	a.noteTermUser(state, s, 2, root, nil)
	if st.ok || !st.busy {
		t.Fatal("kept a read of another connect")
	}
	a.noteTermUser(state, s, 1, root, nil)
	if !st.ok || st.busy || !st.root.Load() || switchedTo(&conn{ai: state}) != "root" {
		t.Fatalf("not kept: %+v", st.u)
	}
	a.noteTermUser(state, s, 1, session.TermUser{LoginUID: 1000, UID: 1000}, nil)
	if st.root.Load() || switchedTo(&conn{ai: state}) != "" {
		t.Fatal("back to the login user still root")
	}
	for range probeTries {
		if st.none.Load() {
			t.Fatal("stopped early")
		}
		a.noteTermUser(state, s, 1, session.TermUser{}, session.ErrNoUser)
	}
	if !st.none.Load() {
		t.Fatal("reads go on without a shell")
	}
	if userName(session.TermUser{UID: 1001}) != "uid 1001" {
		t.Fatal("a user without a name")
	}
}
