package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/ai/remote"
	"github.com/Licoy/sshyard/internal/keychain"
)

type steerModel func(context.Context, llm.Request, func(llm.Event)) (llm.Message, error)

func (m steerModel) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (llm.Message, error) {
	return m(ctx, req, emit)
}

func steerApp(t *testing.T) (*App, *dispatchPump, *conn) {
	t.Helper()
	dir, pump := t.TempDir(), newDispatchPump()
	a, err := New(Platform{Post: pump.post, DataDir: dir, Secrets: keychain.NewMemory(), HomeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.withSecrets(nil)
	for !a.unlocked() {
		pump.next(t)
	}
	host := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, host.ID)
	return a, pump, a.conns[openAIPane(t, a, host.ID)]
}

func steerReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for turn")
		var zero T
		return zero
	}
}

func TestAISteerWaitsForCleanupAndArchives(t *testing.T) {
	a, pump, c := steerApp(t)
	canceled, release := make(chan struct{}), make(chan struct{}, 1)
	reply := make(chan struct{}, 1)
	defer close(release)
	defer close(reply)
	requests := make(chan llm.Request, 2)
	a.newModel = func(*conn) agent.Streamer {
		return steerModel(func(ctx context.Context, req llm.Request, emit func(llm.Event)) (llm.Message, error) {
			requests <- req
			if req.Messages[len(req.Messages)-1].Content != "first" {
				<-reply
				return llm.Message{Content: "next reply"}, nil
			}
			emit(llm.Event{Content: "partial reply", Thinking: "partial thinking"})
			<-ctx.Done()
			emit(llm.Event{Content: "late reply"})
			close(canceled)
			<-release
			return llm.Message{}, ctx.Err()
		})
	}
	a.SendAI(c.id, "first")
	steerReceive(t, requests)
	for !paneHas(a, c.id, "partial thinking") {
		pump.next(t)
	}
	ready, read := make(chan struct{}, 1), make(chan struct{})
	defer close(ready)
	a.attachRead(c, func(add func(*aiAttach, error)) {
		<-ready
		add(&aiAttach{name: "late.txt", data: []byte("late attachment")}, nil)
		close(read)
	})
	done := agent.ToolCard{ID: "reused", Name: "run_command", Phase: "done", Exit: 0, Output: "completed result"}
	a.onAIEvent(c.id, agent.Event{Kind: agent.EventTool, Tool: &done})
	open := agent.ToolCard{ID: "pending", Name: "run_command", Phase: "open", Exit: -1}
	a.onAIEvent(c.id, agent.Event{Kind: agent.EventTool, Tool: &open})
	c.ai.input = "second"
	c.ai.attach = []*aiAttach{{name: "note.txt", data: []byte("submitted attachment")}}
	a.SendAI(c.id, "outside composer")
	select {
	case <-canceled:
		t.Fatal("background entry point interrupted the turn")
	default:
	}
	a.steerAI(c)
	st := c.ai.steer
	a.steerAI(c)
	if st == nil || c.ai.steer != st || !c.ai.running || c.ai.input != "second" {
		t.Fatal("steering failed to retain exactly one draft while stopping")
	}
	steerReceive(t, canceled)
	pump.next(t)
	if tail := c.ai.items[len(c.ai.items)-1]; tail.text != "late reply" || !tail.hidden || len(requests) != 0 {
		t.Fatal("old output was exposed or the new turn overlapped cleanup")
	}
	release <- struct{}{}
	for c.ai.turn == st.turn {
		pump.next(t)
	}
	req := steerReceive(t, requests)
	got := req.Messages[len(req.Messages)-1].Content
	if !strings.Contains(got, "second") || !strings.Contains(got, "submitted attachment") || strings.Contains(got, "late attachment") {
		t.Fatalf("resent draft %q", got)
	}
	conv, err := a.chatHistory().Load(c.host.ID, c.ai.conv)
	if err != nil || len(conv.Items) != 4 || conv.Items[3].Kind != "interrupted" {
		t.Fatalf("old turn was not archived before resending: %+v, %v", conv.Items, err)
	}
	newTool := done
	newTool.Output = "new tool result"
	a.onAIEvent(c.id, agent.Event{Kind: agent.EventTool, Tool: &newTool})
	reply <- struct{}{}
	for c.ai.running {
		pump.next(t)
	}
	// A read started before submission must not attach to the next draft.
	ready <- struct{}{}
	steerReceive(t, read)
	pump.next(t)
	if len(c.ai.attach) != 0 || c.ai.input != "" || c.ai.steer != nil {
		t.Fatal("submission or a late read left extra drafts or attachments")
	}
	var kinds []string
	for _, it := range c.ai.items {
		kinds = append(kinds, it.kind)
	}
	if !slices.Equal(kinds, []string{"user", "tool", "tool", "interrupted", "user", "tool", "text"}) ||
		c.ai.items[1].tool != done || c.ai.items[2].tool.Status != "cancel" || c.ai.items[5].tool != newTool {
		t.Fatalf("timeline %+v", c.ai.items)
	}
}

func TestAISteerCompletionOutcomes(t *testing.T) {
	for _, outcome := range []string{"natural", "stop unconfirmed", "manual stop", "invalidate", "new draft", "replace pane"} {
		t.Run(outcome, func(t *testing.T) {
			a, pump, c := steerApp(t)
			canceled, release := make(chan struct{}), make(chan struct{}, 1)
			finish := make(chan struct{}, 1)
			defer close(release)
			defer close(finish)
			a.newModel = func(*conn) agent.Streamer {
				return steerModel(func(ctx context.Context, req llm.Request, emit func(llm.Event)) (llm.Message, error) {
					if req.Messages[len(req.Messages)-1].Content != "first" {
						return llm.Message{Content: "second reply"}, nil
					}
					emit(llm.Event{Content: "first "})
					<-ctx.Done()
					close(canceled)
					<-release
					switch outcome {
					case "natural":
						emit(llm.Event{Content: "reply"})
						return llm.Message{Content: "first reply"}, nil
					case "stop unconfirmed":
						return llm.Message{}, errors.Join(ctx.Err(), remote.ErrStopUnconfirmed)
					case "replace pane":
						emit(llm.Event{Content: "stale reply"})
						<-finish
						return llm.Message{}, ctx.Err()
					default:
						emit(llm.Event{Content: "stale reply"})
						return llm.Message{}, ctx.Err()
					}
				})
			}
			a.SendAI(c.id, "first")
			pump.next(t)
			c.ai.input = "second"
			approval := make(chan policy.Answer, 1)
			c.ai.pending = &aiPend{ch: approval}
			a.steerAI(c)
			if c.ai.pending != nil || steerReceive(t, approval) != policy.Cancelled {
				t.Fatal("approval survived interruption")
			}
			steerReceive(t, canceled)
			switch outcome {
			case "manual stop":
				a.StopAllAI()
				a.steerAI(c)
				if c.ai.cancel != nil || c.ai.steer != nil || !c.ai.running {
					t.Fatal("manual stop allowed a handoff during cleanup")
				}
			case "invalidate":
				a.invalidateAI(c)
			case "new draft":
				c.ai.input = "later draft"
			case "replace pane":
				a.CloseSession(c.id)
				if a.conns[c.id] != nil {
					t.Fatal("old pane still open")
				}
				a.conns[c.id] = &conn{id: c.id, host: c.host, ai: &aiState{turn: c.ai.turn, running: true}}
			}
			release <- struct{}{}
			if outcome == "replace pane" {
				pump.next(t)
				finish <- struct{}{}
				pump.next(t)
				if state := a.conns[c.id].ai; !state.running || len(state.items) > 0 {
					t.Fatal("old turn mutated its replacement")
				}
				return
			}
			for c.ai.running {
				pump.next(t)
			}
			if outcome == "natural" {
				if !paneHas(a, c.id, "first reply") || !paneHas(a, c.id, "second reply") || len(c.ai.items) != 4 {
					t.Fatalf("natural completion lost output: %+v", c.ai.items)
				}
				return
			}
			draft := "second"
			if outcome == "new draft" {
				draft = "later draft"
			}
			if c.ai.input != draft || c.ai.steer != nil || c.ai.turn != 1 || len(c.ai.items) < 2 || c.ai.items[1].kind != "interrupted" {
				t.Fatalf("revoked handoff lost draft or sent unexpectedly: input=%q, items=%+v", c.ai.input, c.ai.items)
			}
			if outcome == "stop unconfirmed" && !paneHas(a, c.id, "ai-stop-unconfirmed") {
				t.Fatalf("stop failure was not reported: %+v", c.ai.items)
			}
		})
	}
}
