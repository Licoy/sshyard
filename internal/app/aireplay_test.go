package app

import (
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/ai/agent"
)

// TestACPTranscript checks what a new ACP session is told of the
// conversation: questions, answers and tools named, without thinking or
// errors, framed as earlier context.
func TestACPTranscript(t *testing.T) {
	if got := acpTranscript(nil, replayMax); got != "" {
		t.Fatalf("empty conversation told %q", got)
	}
	items := []aiItem{
		{kind: "user", text: "why is the disk full", files: []*aiAttach{{name: "df.txt"}}},
		{kind: "think", text: "secret reasoning"},
		{kind: "tool", tool: agent.ToolCard{Name: "Read", Path: "/var/log/syslog", Phase: "done", Exit: 1}},
		{kind: "text", text: "logs fill it"},
		{kind: "err", text: "acp-session"},
		{kind: "user", text: "clean them"},
		{kind: "text", text: "done"},
	}
	got := acpTranscript(items, replayMax)
	for _, want := range []string{
		"User: why is the disk full\n[Attached: df.txt]",
		"Assistant: [Tool: Read /var/log/syslog (failed)]\nlogs fill it",
		"User: clean them\nAssistant: done",
		"do not answer it again",
		`<untrusted-data source="earlier conversation">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript misses %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"secret reasoning", "acp-session", "left out"} {
		if strings.Contains(got, not) {
			t.Errorf("transcript holds %q", not)
		}
	}
}

// TestACPTranscriptCapped checks that a long conversation keeps its
// latest whole turns, and that a latest turn too long alone is cut.
func TestACPTranscriptCapped(t *testing.T) {
	var items []aiItem
	for _, q := range []string{"first", "second", "third"} {
		items = append(items, aiItem{kind: "user", text: q}, aiItem{kind: "text", text: q + " " + strings.Repeat("x", 400)})
	}
	got := acpTranscript(items, 1000)
	if strings.Contains(got, "User: first") || !strings.Contains(got, "User: second") || !strings.Contains(got, "User: third") || !strings.Contains(got, "left out") {
		t.Fatalf("capped transcript:\n%s", got)
	}
	long := []aiItem{{kind: "user", text: "big"}, {kind: "text", text: strings.Repeat("é", 2000)}}
	got = acpTranscript(long, 1000)
	if !strings.Contains(got, "User: big") || !strings.Contains(got, "left out") || len(got) > 1400 || !strings.Contains(got, "é…") {
		t.Fatalf("cut transcript (%d bytes):\n%s", len(got), got)
	}
}

// TestItemMessages checks the built-in agent's messages rebuilt from what
// a pane showed: alternating questions and answers, the latest that fit.
func TestItemMessages(t *testing.T) {
	items := []aiItem{
		{kind: "text", text: "a reply without a question"},
		{kind: "user", text: "one"},
		{kind: "text", text: "1"},
		{kind: "user", text: "two"},
		{kind: "err", text: "timeout"},
		{kind: "user", text: "three"},
		{kind: "tool", tool: agent.ToolCard{Name: "run_command", Command: "ls\n  -la"}},
	}
	got := itemMessages(items, replayMax)
	want := []string{"user:one", "assistant:1", "user:two", "assistant:(no reply)", "user:three", "assistant:[Tool: run_command ls -la]"}
	if len(got) != len(want) {
		t.Fatalf("messages %+v", got)
	}
	for i, m := range got {
		if m.Role+":"+m.Content != want[i] {
			t.Fatalf("message %d = %s:%s, want %s", i, m.Role, m.Content, want[i])
		}
	}
	if got := itemMessages(items, 10); len(got) != 2 || got[0].Content != "three" {
		t.Fatalf("capped messages %+v", got)
	}
	if got := itemMessages(nil, replayMax); got != nil {
		t.Fatalf("no conversation rebuilt %+v", got)
	}
}
