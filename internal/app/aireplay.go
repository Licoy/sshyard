package app

import (
	"strings"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
)

// An agent that does not remember a conversation the pane shows, as an
// external one whose session could not be loaded or the built-in one
// opening a conversation saved without its messages, is told it again:
// its questions and answers, the tools it used named, the latest turns
// that fit in replayMax bytes.

// replayMax bounds the conversation an agent is told again, in bytes.
const replayMax = 32 << 10

// chatTurn is a question and the answer to it, tools named in it.
type chatTurn struct {
	user, answer string
}

func (t chatTurn) size() int { return len(t.user) + len(t.answer) }

// chatTurns are the questions and answers of items. Thinking and errors
// are left out.
func chatTurns(items []aiItem) []chatTurn {
	var out []chatTurn
	add := func(s string) {
		if len(out) == 0 {
			out = append(out, chatTurn{})
		}
		t := &out[len(out)-1]
		if t.answer != "" {
			t.answer += "\n"
		}
		t.answer += s
	}
	for _, it := range items {
		switch it.kind {
		case "user":
			q := strings.TrimSpace(it.text)
			var names []string
			for _, f := range it.files {
				names = append(names, f.name)
			}
			if len(names) > 0 {
				q = strings.TrimSpace(q + "\n[Attached: " + strings.Join(names, ", ") + "]")
			}
			out = append(out, chatTurn{user: q})
		case "text":
			if s := strings.TrimSpace(it.text); s != "" {
				add(s)
			}
		case "tool":
			add(toolLine(it.tool))
		}
	}
	return out
}

// toolLine names a tool call in a retold conversation.
func toolLine(t agent.ToolCard) string {
	s := "[Tool: " + strings.TrimSpace(t.Name)
	switch {
	case t.Command != "":
		cmd := strings.Join(strings.Fields(t.Command), " ")
		if r := []rune(cmd); len(r) > 200 {
			cmd = string(r[:200]) + "…"
		}
		s += " " + cmd
	case t.Path != "":
		s += " " + t.Path
	}
	if t.Phase == "done" && t.Exit != 0 {
		s += " (failed)"
	}
	return s + "]"
}

// latestTurns is where the latest whole turns that fit in max bytes
// start; the latest alone when even it does not fit.
func latestTurns(turns []chatTurn, max int) int {
	start, total := len(turns), 0
	for start > 0 && total+turns[start-1].size() <= max {
		start--
		total += turns[start].size()
	}
	if start == len(turns) && start > 0 {
		start--
	}
	return start
}

// acpTranscript is the conversation in items as a new ACP session is
// told it, at most about max bytes; empty when there is none.
func acpTranscript(items []aiItem, max int) string {
	turns := chatTurns(items)
	if len(turns) == 0 {
		return ""
	}
	start := latestTurns(turns, max)
	var b strings.Builder
	for _, t := range turns[start:] {
		if t.user != "" {
			b.WriteString("User: " + t.user + "\n")
		}
		if t.answer != "" {
			b.WriteString("Assistant: " + t.answer + "\n")
		}
		b.WriteString("\n")
	}
	body, cut := strings.TrimSpace(b.String()), start > 0
	if len(body) > max {
		body, cut = strings.ToValidUTF8(body[:max], "")+"…", true
	}
	head := "This conversation so far, from an earlier session that has ended and that you do not otherwise remember. " +
		"Take it as context for the new message; do not answer it again."
	if cut {
		head += " Its oldest part is left out."
	}
	return agent.WrapUntrusted("earlier conversation", head+"\n\n"+body)
}

// itemMessages are the built-in agent's messages rebuilt from items, for
// a conversation it does not have them for: each question and its
// answer, the latest that fit in max bytes.
func itemMessages(items []aiItem, max int) []llm.Message {
	var turns []chatTurn
	for _, t := range chatTurns(items) {
		if t.user != "" {
			turns = append(turns, t)
		}
	}
	var out []llm.Message
	for _, t := range turns[latestTurns(turns, max):] {
		answer := t.answer
		if answer == "" {
			answer = "(no reply)"
		}
		out = append(out, llm.Message{Role: "user", Content: t.user}, llm.Message{Role: "assistant", Content: answer})
	}
	return out
}
