// Package agent is SSHYard's built-in assistant: one host, one tool loop,
// every call checked by a policy.Gate. A Session is not safe for concurrent
// Turn and Continue; cancel with the context instead.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/ai/policy"
)

// MaxToolCalls is how many tool calls one turn runs before it pauses, for a
// Config that names none.
const MaxToolCalls = 25

// maxOutput is the most of one tool result kept for the timeline and the model.
const maxOutput = 1 << 20

// defaultMaxChars is the context budget. Oldest tool outputs are summarised past it.
const defaultMaxChars = 120_000

// ErrPaused is returned when a turn stops after its tool-call limit.
// Continue resumes it.
var ErrPaused = errors.New("agent: paused at the tool call limit")

var errTurnPaused = errors.New("agent: turn is paused")

// Streamer is the model client: one streamed completion in any of its formats.
type Streamer interface {
	Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (llm.Message, error)
}

// Config builds a Session. A nil Tools uses Unavailable.
type Config struct {
	Model    Streamer
	Gate     policy.Gate
	Tools    Tools
	Redact   bool
	MaxChars int
	// Target is what the tools reach; zero is an SSH host.
	Target Target
	// MaxToolCalls caps the tool calls one turn runs before it pauses.
	// Nil keeps MaxToolCalls; a pointer to 0 runs without a limit.
	MaxToolCalls *int
}

// Kind is one timeline event.
type Kind string

const (
	EventText     Kind = "text"
	EventThinking Kind = "thinking"
	EventTool     Kind = "tool"
	EventPause    Kind = "pause"
	EventDone     Kind = "done"
	// EventNotice is a failure the user must see while the turn goes on.
	// Text is a code, such as policy.AuditNotice's.
	EventNotice Kind = "notice"
)

// Event is one thing the UI timeline shows. Tool is set for EventTool;
// Calls, for EventPause, is the tool calls the turn has run.
type Event struct {
	Kind  Kind
	Text  string
	Tool  *ToolCard
	Calls int
}

// ToolCard is one tool call on the timeline. Output is truncated to 1 MB.
// Exit is -1 when no exit code is available.
type ToolCard struct {
	Phase    string // "open" while it runs, "done" when it has a result
	Status   string // "cancel" or "timeout" overrides execution status; Verdict retains the approval source
	ID       string
	Name     string
	Category string
	Risk     string
	Where    string
	Command  string
	Path     string
	Dir      string
	Verdict  string // full, auto, reviewer, user, reject, timeout, cancel
	Review   string // the review model's note, when one was asked
	Refusal  string
	Output   string
	Diff     string
	Exit     int
	DurMS    int64
	Run      bool
}

// Session is one built-in agent conversation. History lives in memory.
type Session struct {
	model    Streamer
	gate     policy.Gate
	tools    Tools
	redact   bool
	maxChars int
	target   Target
	msgs     []llm.Message
	hostSent bool
	// termUser is the HostMeta.TermUser the model was last told of.
	termUser string
	// used counts the tool calls of the turn so far; limit is how many it
	// may run in all, raised by maxCalls on each Continue; maxCalls is the
	// calls one stretch grants, 0 for no limit at all.
	used     int
	limit    int
	maxCalls int
	paused   bool
	pending  []llm.ToolCall
}

// New starts a session with the system prompt. The host context is added
// on the first Turn.
func New(cfg Config) *Session {
	tools := cfg.Tools
	if tools == nil {
		tools = Unavailable{}
	}
	maxChars := cfg.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	maxCalls := MaxToolCalls
	if cfg.MaxToolCalls != nil {
		maxCalls = max(0, *cfg.MaxToolCalls)
	}
	return &Session{
		model:    cfg.Model,
		gate:     cfg.Gate,
		tools:    tools,
		redact:   cfg.Redact,
		maxChars: maxChars,
		target:   cfg.Target,
		msgs:     []llm.Message{{Role: "system", Content: systemPrompt(cfg.Target)}},
		maxCalls: maxCalls,
		limit:    maxCalls,
	}
}

// Configure updates an idle session after its previous turn has stopped:
// the gate it asks, whether it redacts, and the tool calls its next turn
// runs before pausing. A nil maxToolCalls keeps MaxToolCalls.
func (s *Session) Configure(gate policy.Gate, redact bool, maxToolCalls *int) {
	s.gate = gate
	s.redact = redact
	maxCalls := MaxToolCalls
	if maxToolCalls != nil {
		maxCalls = max(0, *maxToolCalls)
	}
	s.maxCalls = maxCalls
	s.limit = maxCalls
}

// Paused reports whether the session is waiting for Continue.
func (s *Session) Paused() bool { return s.paused }

// AbandonPause ends an idle paused turn without running its remaining calls.
func (s *Session) AbandonPause() {
	if s.paused {
		s.cancelRest("cancelled")
		s.paused = false
	}
}

// InterruptTurn removes the interrupted reply from an idle session, keeping
// tool calls and results paired along with any required provider state.
func (s *Session) InterruptTurn() {
	s.AbandonPause()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if s.msgs[i].Role != "user" {
			continue
		}
		keep := s.msgs[:i+1]
		for _, m := range s.msgs[i+1:] {
			if m.Role == "assistant" {
				if len(m.ToolCalls) == 0 {
					continue
				}
				m.Content = ""
			}
			keep = append(keep, m)
		}
		clear(s.msgs[len(keep):])
		s.msgs = keep
		return
	}
}

// Turn sends one user message. terminal, when set, is attached as untrusted
// data. A paused session refuses until Continue or AbandonPause.
func (s *Session) Turn(ctx context.Context, user string, meta HostMeta, terminal string, emit func(Event)) error {
	return s.TurnWith(ctx, user, nil, meta, terminal, emit)
}

// TurnWith is Turn with files attached to the message: images and
// documents the model reads.
func (s *Session) TurnWith(ctx context.Context, user string, parts []llm.Part, meta HostMeta, terminal string, emit func(Event)) error {
	if s.paused {
		return errTurnPaused
	}
	if s.model == nil {
		return errors.New("agent: model is nil")
	}
	if s.gate == nil {
		return errors.New("agent: gate is nil")
	}
	if !s.hostSent {
		s.msgs = append(s.msgs, llm.Message{Role: "system", Content: s.mask(hostMessageFor(s.target, meta))})
		s.hostSent, s.termUser = true, meta.TermUser
	} else if meta.TermUser != s.termUser {
		s.msgs = append(s.msgs, llm.Message{Role: "system", Content: s.mask(termUserNote(s.target, meta, true))})
		s.termUser = meta.TermUser
	}
	text := user
	if terminal != "" {
		if text != "" {
			text += "\n\n"
		}
		text += WrapUntrusted("terminal", terminal)
	}
	s.msgs = append(s.msgs, llm.Message{Role: "user", Content: s.mask(text), Parts: parts})
	s.used = 0
	s.limit = s.maxCalls
	s.pending = nil
	return s.loop(ctx, emit)
}

// Continue runs the tool calls that were held at the pause, then keeps going.
// It grants another stretch of tool calls. It does nothing when the session
// is not paused.
func (s *Session) Continue(ctx context.Context, emit func(Event)) error {
	if !s.paused {
		return nil
	}
	if s.model == nil || s.gate == nil {
		return errors.New("agent: not configured")
	}
	s.paused = false
	if s.maxCalls <= 0 {
		s.limit = 0 // no limit at all: every remaining call runs
	} else {
		s.limit = s.used + s.maxCalls
	}
	if err := s.drain(ctx, emit); err != nil {
		return err
	}
	return s.loop(ctx, emit)
}

func (s *Session) loop(ctx context.Context, emit func(Event)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.msgs = trimParts(trimContext(s.msgs, s.maxChars), MaxPartBytes)
		msg, err := s.model.Stream(ctx, s.request(), func(ev llm.Event) {
			if emit == nil {
				return
			}
			if ev.Content != "" {
				emit(Event{Kind: EventText, Text: ev.Content})
			}
			if ev.Thinking != "" {
				emit(Event{Kind: EventThinking, Text: ev.Thinking})
			}
		})
		if err != nil {
			return err
		}
		if msg.Role == "" {
			msg.Role = "assistant"
		}
		s.msgs = append(s.msgs, msg)
		if len(msg.ToolCalls) == 0 {
			if emit != nil {
				emit(Event{Kind: EventDone, Text: msg.Content})
			}
			return nil
		}
		s.pending = append([]llm.ToolCall(nil), msg.ToolCalls...)
		if err := s.drain(ctx, emit); err != nil {
			return err
		}
	}
}

func (s *Session) drain(ctx context.Context, emit func(Event)) error {
	for len(s.pending) > 0 {
		if err := ctx.Err(); err != nil {
			s.cancelRest("cancelled")
			return err
		}
		// A limit of 0 is no limit: the turn runs every call it is given.
		if s.limit > 0 && s.used >= s.limit {
			s.paused = true
			if emit != nil {
				emit(Event{Kind: EventPause, Calls: s.used})
			}
			return ErrPaused
		}
		call := s.pending[0]
		s.pending = s.pending[1:]
		if err := s.execOne(ctx, call, emit); err != nil {
			return err
		}
		s.used++
	}
	return nil
}

func (s *Session) execOne(ctx context.Context, call llm.ToolCall, emit func(Event)) error {
	raw := strings.TrimSpace(call.Arguments)
	fields, err := toolArgumentsIn(ToolDefsFor(s.target), call.Name, raw)
	if err != nil {
		s.emitTool(emit, ToolCard{ID: call.ID, Name: call.Name, Phase: "done", Exit: 1, Output: err.Error()})
		s.appendTool(call.ID, err.Error())
		return nil
	}
	act := actionFor(call.Name, fields)
	if resolver, ok := s.tools.(interface {
		ResolvePath(context.Context, string) (string, error)
	}); ok && act.Path != "" {
		resolved, err := resolver.ResolvePath(ctx, act.Path)
		if ctx.Err() != nil {
			s.dropCall(call.ID)
			return ctx.Err()
		}
		if err != nil {
			s.emitTool(emit, ToolCard{ID: call.ID, Name: call.Name, Path: act.Path, Phase: "done", Exit: 1, Output: err.Error()})
			s.appendTool(call.ID, err.Error())
			return ctx.Err()
		}
		act.Path = resolved
		fields["path"] = resolved
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		raw = string(encoded)
	}

	diff := ""
	if call.Name == "write_file" {
		if err := ctx.Err(); err != nil {
			s.dropCall(call.ID)
			return err
		}
		content := asString(fields["content"])
		old, ok, err := s.readForDiff(ctx, act, emit)
		if ctx.Err() != nil {
			s.dropCall(call.ID)
			return ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.dropCall(call.ID)
			return err
		}
		if err != nil {
			// A failed read must not become a new-file write: that would
			// pass a zero version and overwrite whatever is there.
			card := ToolCard{
				ID: call.ID, Name: call.Name, Where: string(act.Where), Path: act.Path,
				Phase: "done", Exit: 1, Output: err.Error(),
			}
			if s.auditRefused(emit, card, err) {
				return nil
			}
			var denied *readDenied
			if errors.As(err, &denied) {
				card.Verdict = "reject"
				card.Refusal = err.Error()
			}
			s.emitTool(emit, card)
			s.appendTool(call.ID, err.Error())
			return nil
		}
		if !ok {
			diff = content
		} else {
			diff = Unified(act.Path, old, content)
		}
	}

	verdict, err := s.gate.Authorize(ctx, act, diff)
	if err != nil {
		if ctx.Err() == nil && s.auditRefused(emit, ToolCard{
			ID: call.ID, Name: call.Name, Where: string(act.Where), Command: act.Command,
			Path: act.Path, Dir: act.Dir, Diff: diff,
		}, err) {
			return nil
		}
		s.dropCall(call.ID)
		return err
	}
	card := ToolCard{
		ID:       call.ID,
		Name:     call.Name,
		Category: string(verdict.Category),
		Risk:     riskName(verdict.Risk),
		Where:    string(act.Where),
		Command:  act.Command,
		Path:     act.Path,
		Dir:      act.Dir,
		Verdict:  verdictCode(verdict),
		Review:   verdict.ReviewReason,
		Diff:     diff,
		Run:      verdict.Run,
		Exit:     -1,
	}
	if !verdict.Run {
		reason := verdict.Refusal
		if reason == "" {
			reason = "rejected"
		}
		card.Phase = "done"
		card.Refusal = reason
		card.Output = reason
		s.emitTool(emit, card)
		s.appendTool(call.ID, reason)
		if verdict.Answer == policy.Cancelled || ctx.Err() != nil {
			s.cancelRest("cancelled")
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.dropCall(call.ID)
		return err
	}
	card.Phase = "open"
	s.emitTool(emit, card)

	start := time.Now()
	out, exitCode, callErr := s.tools.Call(ctx, call.Name, json.RawMessage(raw))
	dur := time.Since(start).Milliseconds()
	ctxErr := ctx.Err()
	if callErr != nil && (ctxErr != nil || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded)) {
		// Stop, Stop all, and the runner timeout share this path; the command already started.
		auditErr := s.gate.Done(act, verdict, -1, dur, countedBytes(s.tools, out))
		card.Phase, card.Status = "done", "cancel"
		if errors.Is(ctxErr, context.DeadlineExceeded) || ctxErr == nil && errors.Is(callErr, context.DeadlineExceeded) {
			card.Status = "timeout"
		}
		card.Output, card.DurMS = truncOut(out), dur
		s.emitTool(emit, card)
		_ = s.auditLost(emit, auditErr)
		s.dropCall(call.ID)
		if ctxErr != nil && !errors.Is(callErr, ctxErr) {
			return errors.Join(ctxErr, callErr)
		}
		return callErr
	}
	if callErr != nil && out == "" {
		out = callErr.Error()
		if exitCode == 0 {
			exitCode = 1
		}
	}
	auditErr := s.gate.Done(act, verdict, exitCode, dur, countedBytes(s.tools, out))
	shown := truncOut(out)
	card.Phase = "done"
	card.Output = shown
	card.Exit = exitCode
	card.DurMS = dur
	s.emitTool(emit, card)
	if note := s.auditLost(emit, auditErr); note != "" {
		// The result is real; the note only keeps the model from re-running it.
		shown += "\n\n" + note
	}
	s.appendTool(call.ID, shown)
	return nil
}

// auditRefused handles an action the audit log stopped before it ran: the
// card and a notice show why, and the model gets the reason as the result.
func (s *Session) auditRefused(emit func(Event), card ToolCard, err error) bool {
	code, ok := policy.AuditNotice(err)
	if !ok {
		return false
	}
	card.Phase, card.Verdict, card.Refusal, card.Output, card.Run, card.Exit = "done", "reject", code, "", false, -1
	s.emitTool(emit, card)
	s.emitNotice(emit, code)
	s.appendTool(card.ID, err.Error())
	return true
}

// auditLost shows a notice that an action ran but its audit record was not
// saved, and returns the note for the model ("" when err is nil). The
// action is never run again and the turn goes on.
func (s *Session) auditLost(emit func(Event), err error) string {
	if err == nil {
		return ""
	}
	var ae *policy.AuditError
	if errors.As(err, &ae) {
		err = ae.Err
	}
	ae = &policy.AuditError{Ran: true, Err: err}
	code, _ := policy.AuditNotice(ae)
	s.emitNotice(emit, code)
	return ae.Error()
}

func (s *Session) emitNotice(emit func(Event), code string) {
	if emit == nil {
		return
	}
	emit(Event{Kind: EventNotice, Text: code})
}

// countedBytes is the audit output size. Tools that ran a command report
// the CountingWriter total (negative means "not a command"); other tools
// use the text the model sees.
func countedBytes(t Tools, out string) int64 {
	type counter interface{ OutputBytes() int64 }
	if c, ok := t.(counter); ok {
		if n := c.OutputBytes(); n >= 0 {
			return n
		}
	}
	return int64(len(out))
}

func (s *Session) emitTool(emit func(Event), card ToolCard) {
	if emit == nil {
		return
	}
	emit(Event{Kind: EventTool, Tool: &card})
}

func (s *Session) appendTool(id, content string) {
	s.msgs = append(s.msgs, llm.Message{Role: "tool", ToolCallID: id, Content: s.mask(content)})
}

func (s *Session) dropCall(id string) {
	s.appendTool(id, "cancelled")
	s.cancelRest("cancelled")
}

func (s *Session) cancelRest(reason string) {
	for _, c := range s.pending {
		s.appendTool(c.ID, reason)
	}
	s.pending = nil
}

func (s *Session) mask(text string) string {
	if s.redact {
		return policy.Redact(text)
	}
	return text
}

func (s *Session) request() llm.Request {
	msgs := make([]llm.Message, len(s.msgs))
	for i, m := range s.msgs {
		msgs[i] = m
		if len(m.ToolCalls) > 0 {
			msgs[i].ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
		}
		if s.redact {
			msgs[i].Content = policy.Redact(m.Content)
			for j := range msgs[i].ToolCalls {
				msgs[i].ToolCalls[j].Arguments = policy.Redact(msgs[i].ToolCalls[j].Arguments)
			}
		}
	}
	return llm.Request{Messages: msgs, Tools: ToolDefsFor(s.target)}
}

func actionFor(name string, m map[string]any) policy.Action {
	a := policy.Action{Where: policy.Remote, Tool: name, Reason: asString(m["reason"])}
	switch name {
	case "run_command":
		a.Command = asString(m["command"])
		a.Dir = asString(m["dir"])
	case "terminal_send":
		a.Command = asString(m["command"])
	case "terminal_key":
		a.Command = asString(m["key"])
	default:
		a.Path = asString(m["path"])
	}
	return a
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func verdictCode(v policy.Verdict) string {
	if !v.Run {
		switch v.Answer {
		case policy.TimedOut:
			return "timeout"
		case policy.Cancelled:
			return "cancel"
		default:
			return "reject"
		}
	}
	switch v.By {
	case policy.ByFull:
		return "full"
	case policy.ByReviewer:
		return "reviewer"
	case policy.ByUser:
		return "user"
	default:
		return "auto"
	}
}

func riskName(r policy.Risk) string {
	switch r {
	case policy.RiskReadOnly:
		return "readonly"
	case policy.RiskLow:
		return "low"
	case policy.RiskMedium:
		return "medium"
	case policy.RiskHigh:
		return "high"
	case policy.RiskCritical:
		return "critical"
	default:
		return "unknown"
	}
}

func truncOut(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n…"
}

func trimContext(msgs []llm.Message, max int) []llm.Message {
	if max <= 0 {
		max = defaultMaxChars
	}
	for messageSize(msgs) > max {
		idx := -1
		for i, m := range msgs {
			if m.Role == "tool" && !strings.HasPrefix(m.Content, "omitted tool output") {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		body := msgs[idx].Content
		line, _, _ := strings.Cut(body, "\n")
		if len(line) > 80 {
			line = line[:80]
		}
		msgs[idx].Content = fmt.Sprintf("omitted tool output (%d bytes): %s", len(body), line)
	}
	return msgs
}

// MaxPartBytes is the most the files attached to a conversation weigh
// together in one request; older ones give way to newer ones.
const MaxPartBytes = 30 << 20

// trimParts drops the files attached to the oldest messages, never to the
// last question, until the rest weigh no more than max, and says in their
// place which were dropped.
func trimParts(msgs []llm.Message, max int) []llm.Message {
	total, last := 0, -1
	for i, m := range msgs {
		for _, p := range m.Parts {
			total += len(p.Data)
		}
		if m.Role == "user" {
			last = i
		}
	}
	for i := 0; i < last && total > max; i++ {
		m := &msgs[i]
		if len(m.Parts) == 0 {
			continue
		}
		names := make([]string, len(m.Parts))
		for j, p := range m.Parts {
			total -= len(p.Data)
			names[j] = p.Name
		}
		note := fmt.Sprintf("[attachments omitted: %s]", strings.Join(names, ", "))
		if m.Content != "" {
			note = m.Content + "\n\n" + note
		}
		m.Content, m.Parts = note, nil
	}
	return msgs
}

func messageSize(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, c := range m.ToolCalls {
			n += len(c.Name) + len(c.Arguments)
		}
	}
	return n
}

// History is the conversation so far without its system messages, to be
// kept and given back to Restore. Attached files keep only their names.
func (s *Session) History() []llm.Message {
	var out []llm.Message
	for _, m := range s.msgs {
		if m.Role == "system" {
			continue
		}
		m.ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
		out = append(out, omitParts(m))
	}
	return out
}

// Restore is a session that carries on msgs, as History gave them. A tool
// call left without its result, as at a pause, is answered "cancelled";
// a result without its call is dropped. The host context is sent again
// with the next Turn.
func Restore(cfg Config, msgs []llm.Message) *Session {
	s := New(cfg)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == "system" || m.Role == "tool" {
			continue // a result is kept below, after its call
		}
		s.msgs = append(s.msgs, omitParts(m))
		if len(m.ToolCalls) == 0 {
			continue
		}
		called, answered := map[string]bool{}, map[string]bool{}
		for _, c := range m.ToolCalls {
			called[c.ID] = true
		}
		for i+1 < len(msgs) && msgs[i+1].Role == "tool" {
			i++
			r := msgs[i]
			if called[r.ToolCallID] && !answered[r.ToolCallID] {
				answered[r.ToolCallID] = true
				s.msgs = append(s.msgs, r)
			}
		}
		for _, c := range m.ToolCalls {
			if !answered[c.ID] {
				s.appendTool(c.ID, "cancelled")
			}
		}
	}
	return s
}

// omitParts turns m's attached files into a note naming them.
func omitParts(m llm.Message) llm.Message {
	if len(m.Parts) == 0 {
		return m
	}
	names := make([]string, len(m.Parts))
	for i, p := range m.Parts {
		names[i] = p.Name
	}
	note := fmt.Sprintf("[attachments omitted: %s]", strings.Join(names, ", "))
	if m.Content != "" {
		note = m.Content + "\n\n" + note
	}
	m.Content, m.Parts = note, nil
	return m
}
