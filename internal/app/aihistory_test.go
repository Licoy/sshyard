package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/history"
	"github.com/Licoy/sshyard/internal/ai/llm"
)

func chatSummaries(t *testing.T, a *App, host string) []history.Summary {
	t.Helper()
	list, err := a.loadChats(host)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// recModel echoes the last question and keeps what each request sent.
type recModel struct {
	mu   sync.Mutex
	reqs [][]llm.Message
}

func (r *recModel) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (llm.Message, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, append([]llm.Message(nil), req.Messages...))
	r.mu.Unlock()
	return echoModel{}.Stream(ctx, req, emit)
}

func (r *recModel) last() []llm.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

func TestChatHistory(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	model := &recModel{}
	a.newModel = func(*conn) agent.Streamer { return model }
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := a.conns[id]
	ask := func(q string) {
		t.Helper()
		a.SendAI(id, q)
		waitFor(t, tt, "reply to "+q, func() bool { return !c.ai.running && trailingText(c.ai.items) == "echo:"+q })
	}
	tt.Frame()
	if !tt.HasText("New Chat") || !tt.HasText("Chat History") {
		t.Fatalf("header misses its chat buttons: %q", tt.Texts())
	}
	ask("why is the disk full")
	ask("and now?")
	first := c.ai.conv
	if list := chatSummaries(t, a, h.ID); len(list) != 1 || list[0].ID != first || list[0].Title != "why is the disk full" {
		t.Fatalf("history = %+v", list)
	}

	a.NewChat(id)
	if len(c.ai.items) != 0 || c.ai.conv != "" || c.ai.sess != nil {
		t.Fatalf("new chat kept %d items, conv %q", len(c.ai.items), c.ai.conv)
	}
	ask("other question")
	second := c.ai.conv
	if list := chatSummaries(t, a, h.ID); len(list) != 2 || list[0].ID != second || list[1].ID != first {
		t.Fatalf("history = %+v", list)
	}
	for _, m := range model.last() {
		if m.Role == "user" && strings.Contains(m.Content, "disk") {
			t.Fatal("a new chat sent the old one")
		}
	}

	// Opening the first carries it on where it was.
	a.OpenChat(id, first)
	if c.ai.conv != first || len(c.ai.items) != 4 {
		t.Fatalf("opened %q with %d items", c.ai.conv, len(c.ai.items))
	}
	ask("third")
	sent := model.last()
	found := false
	for _, m := range sent {
		found = found || m.Role == "user" && m.Content == "why is the disk full"
	}
	if !found {
		t.Fatalf("the old chat was not sent again: %+v", sent)
	}

	// The history lists them and opens one.
	tt.Frame()
	mustClick(t, tt, "Chat History")
	tt.Frame()
	if !tt.HasText("other question") || !tt.HasText("why is the disk full") {
		t.Fatalf("history menu: %q", tt.Texts())
	}
	mustClick(t, tt, "other question")
	tt.Frame()
	if c.ai.conv != second {
		t.Fatalf("menu opened %q", c.ai.conv)
	}

	// Clearing asks first, then forgets it.
	c.ai.drop = c.ai.conv
	tt.Frame()
	if !tt.HasText("Clear this chat?") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	a.ClearChat(id)
	c.ai.drop = ""
	if list := chatSummaries(t, a, h.ID); len(list) != 1 || list[0].ID != first || len(c.ai.items) != 0 {
		t.Fatalf("after clear: %+v, %d items", list, len(c.ai.items))
	}

	// Closing the pane saves what it showed.
	ask("before close")
	last := c.ai.conv
	a.CloseSession(id)
	if list := chatSummaries(t, a, h.ID); len(list) != 2 || list[0].ID != last {
		t.Fatalf("close did not save: %+v", list)
	}
	dir := filepath.Join(a.p.DataDir, "ai-history")
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, _ error) error {
		if b, _ := os.ReadFile(p); !info.IsDir() && bytes.Contains(b, []byte("disk")) {
			t.Errorf("%s holds plain text", p)
		}
		return nil
	})

	// The empty pane offers the recent ones; deleting the host forgets them.
	id, _ = a.OpenAI(h.ID, openAt{}, 0)
	tt.Frame()
	if !tt.HasText("Recent chats") || !tt.HasText("before close") {
		t.Fatalf("no recent chats: %q", tt.Texts())
	}
	a.CloseSession(id)
	if err := a.DeleteHost(h.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := a.chatHistory().List(h.ID); len(list) != 0 {
		t.Fatalf("host's chats kept: %+v", list)
	}
}

func TestChatOfExternalAgent(t *testing.T) {
	a, _ := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	session := &history.ACPSession{ID: "sess-1", Agent: "acp", Cwd: "/tmp/sshyard-acp-1"}
	conv := history.Conversation{ID: history.NewID(), Host: h.ID, Title: "deploy", Agent: "acp", ACP: session,
		Items: []history.Item{{Kind: "user", Text: "deploy"}, {Kind: "text", Text: "done"}}}
	old := history.Conversation{ID: history.NewID(), Host: h.ID, Title: "older", Agent: "acp",
		Items: []history.Item{{Kind: "user", Text: "older"}}}
	for _, cv := range []history.Conversation{old, conv} {
		if err := a.chatHistory().Save(cv); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := a.OpenAI(h.ID, openAt{}, 0)
	a.OpenChat(id, conv.ID)
	c := a.conns[id]
	if c.ai.agent != "acp" || len(c.ai.items) != 2 || c.ai.acpSID != "sess-1" || c.ai.acpAgent != "acp" || c.ai.acpCwd != session.Cwd {
		t.Fatalf("agent %q, %d items, session %q of %q in %q", c.ai.agent, len(c.ai.items), c.ai.acpSID, c.ai.acpAgent, c.ai.acpCwd)
	}
	// Saving keeps the session; opening one saved without it has none.
	a.OpenChat(id, old.ID)
	if c.ai.conv != old.ID || c.ai.acpSID != "" || c.ai.acpCwd != "" {
		t.Fatalf("opened %q with session %q", c.ai.conv, c.ai.acpSID)
	}
	saved, err := a.chatHistory().Load(h.ID, conv.ID)
	if err != nil || saved.ACP == nil || *saved.ACP != *session {
		t.Fatalf("saved session %+v, %v", saved.ACP, err)
	}
}

// TestChatSavedWithoutMessages checks that the built-in agent opening a
// conversation saved without its messages is told it again.
func TestChatSavedWithoutMessages(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	model := &recModel{}
	a.newModel = func(*conn) agent.Streamer { return model }
	conv := history.Conversation{ID: history.NewID(), Host: h.ID, Title: "disk", Agent: "builtin",
		Items: []history.Item{{Kind: "user", Text: "why is the disk full"}, {Kind: "think", Text: "hm"}, {Kind: "text", Text: "logs fill it"},
			{Kind: "tool", Tool: &agent.ToolCard{Name: "run_command", Command: "du -sh /var/log", Phase: "done"}}, {Kind: "err", Text: "timeout"}}}
	if err := a.chatHistory().Save(conv); err != nil {
		t.Fatal(err)
	}
	id, _ := a.OpenAI(h.ID, openAt{}, 0)
	a.OpenChat(id, conv.ID)
	c := a.conns[id]
	a.SendAI(id, "and now?")
	waitFor(t, tt, "reply", func() bool { return !c.ai.running && trailingText(c.ai.items) == "echo:and now?" })
	var got []string
	for _, m := range model.last() {
		if m.Role == "user" || m.Role == "assistant" {
			got = append(got, m.Role+": "+m.Content)
		}
	}
	want := []string{"user: why is the disk full", "assistant: logs fill it\n[Tool: run_command du -sh /var/log]"}
	if len(got) < 3 || got[0] != want[0] || got[1] != want[1] || !strings.HasPrefix(got[2], "user: ") || !strings.Contains(got[2], "and now?") {
		t.Fatalf("sent %q", got)
	}
}

func TestTempHostKeepsNoChats(t *testing.T) {
	a, _ := newTestApp(t)
	if a.keepsChats(&conn{host: a.localHost(localID), ai: &aiState{}}) {
		t.Fatal("a host that is not saved keeps chats")
	}
}
