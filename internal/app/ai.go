package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/ai/acp"
	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/audit"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// aiState is one AI pane's session. The timeline is touched on the UI
// thread only; resolved is the mode the gate reads, under mu.
type aiState struct {
	agent    string
	provider string
	// model and effort are the pane's choice of model and thinking level,
	// empty for the provider's default; query filters the model menu.
	model    string
	effort   string
	query    string
	link     int
	mode     string // pane override; empty inherits
	mu       sync.Mutex
	resolved policy.Mode
	dirty    bool
	scope    policy.Scope

	sess     *agent.Session
	box      *modelBox
	cancel   context.CancelFunc
	running  bool
	paused   bool
	items    []aiItem
	input    string
	attach   []*aiAttach // files going with the question being written
	task     string      // the question of the turn running, for the review
	gen      int
	turn     int
	pending  *aiPend
	onlyDiff bool
	tools    agent.Tools
	target   agent.Target // what sess's tools reach
	acp      *acpRuntime
	// acpOpts are the ACP agent's model and thinking selectors as its
	// session last reported them; nil until it reports.
	acpOpts []acp.Option
	// acpSID is the ACP session that last carried the conversation on,
	// of the backend acpAgent and run in the sandbox acpCwd: a new
	// process of that agent loads it when it can, else is told the
	// conversation again (aireplay.go). acpLast is the pane's latest
	// runtime, which has to let go of the sandbox first.
	acpSID, acpAgent, acpCwd string
	acpLast                  *acpRuntime
	// pauseCalls is the tool calls the paused turn ran, for its notice.
	pauseCalls int

	// conv is the conversation's ID in the host's history, from its first
	// question, with its title and when it began; msgs are the built-in
	// agent's messages as its last turn ended, which the history keeps
	// and a new session carries on, nil once another agent took a turn.
	// hquery filters the history; drop is the conversation asked to be
	// deleted.
	conv    string
	title   string
	created time.Time
	msgs    []llm.Message
	hquery  string
	drop    string

	// md is how the pane shows Markdown, its code blocks with its own
	// actions; scroll follows the end of the timeline unless the user
	// scrolled up from it. copied is the code last copied, until when
	// it shows that it was.
	md         *markdown.Options
	scroll     ui.ScrollState
	copied     string
	copiedTill time.Time
	// fullSeen is set once the full access banner is dismissed, until
	// the pane leaves full access.
	fullSeen bool
	// textN and textLen are the reply's trailing text as the timeline last
	// saw it, and textAt when it last grew: a reply quiet for a while is
	// still being worked on, as a tool call being written or reviewed.
	textN, textLen int
	textAt         time.Time
}

type aiItem struct {
	kind  string // user, text, think, tool, err
	text  string
	files []*aiAttach // a question's attached files
	tool  agent.ToolCard
	open  bool // thinking stays folded until the user opens it
}

type aiPend struct {
	prompt policy.Prompt
	ch     chan policy.Answer
}

type fullAsk struct {
	kind string // pane, settings, host
	conn int
	tick bool
	open bool
}

type consentAsk struct {
	host string
	conn int
	text string
	open bool
}

// modelBox is the client the session calls, with the pane's model and
// thinking level. The UI thread replaces them before each turn; Stream
// reads them under mu.
type modelBox struct {
	mu     sync.Mutex
	s      agent.Streamer
	model  string
	effort string
}

func (b *modelBox) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (llm.Message, error) {
	b.mu.Lock()
	s, model, effort := b.s, b.model, b.effort
	b.mu.Unlock()
	if s == nil {
		return llm.Message{}, errAIProvider
	}
	if req.Model == "" {
		req.Model = model
	}
	if req.Effort == "" {
		req.Effort = effort
	}
	return s.Stream(ctx, req, emit)
}

// set replaces the client and the pane's choices.
func (b *modelBox) set(s agent.Streamer, model, effort string) {
	b.mu.Lock()
	b.s, b.model, b.effort = s, model, effort
	b.mu.Unlock()
}

var (
	errAIKey      = errors.New("ai: no api key")
	errAIProvider = errors.New("ai: no provider")
	errAIBackend  = errors.New("ai: unsupported backend")
)

// OpenAI opens an assistant for hostID. link is a terminal pane to attach,
// or 0. It does not open an SSH connection. A tab has one: on a pane of a
// tab that has one already, it shows that one (reuse).
func (a *App) OpenAI(hostID string, at openAt, link int) (int, error) {
	if ex := a.singleAt(layout.AI, at); ex != nil {
		id, err := a.reuse(ex, hostID, link)
		if err == nil {
			a.aiRecent[hostID] = id
		}
		return id, err
	}
	c, err := a.makeConn(hostID, layout.AI, link)
	if err != nil {
		return 0, err
	}
	a.place(c, at)
	a.aiRecent[hostID] = c.id
	return c.id, nil
}

// OpenAIFor opens hostID's assistant in a new tab. One that types into a
// terminal opens beside a new terminal of the host, to type into.
func (a *App) OpenAIFor(hostID string) {
	if h, ok := a.host(hostID); ok && aiTarget(h) != agent.TargetSSH {
		id, err := a.Connect(hostID, openAt{})
		if err != nil {
			return
		}
		pane := 0
		if _, n := a.ws.Locate(id); n != nil {
			pane = n.ID
		}
		a.OpenAI(hostID, openAt{pane: pane, edge: layout.Right}, id)
		return
	}
	a.OpenAI(hostID, openAt{}, 0)
}

// OpenAIBeside splits an assistant next to the focused pane, on its host.
// The link is that pane when it is a terminal.
func (a *App) OpenAIBeside() {
	t := a.ws.ActiveTab()
	cn := a.focused()
	if t == nil || cn == nil {
		return
	}
	link := 0
	if cn.kind == layout.Terminal {
		link = cn.id
	}
	a.OpenAI(cn.host.ID, openAt{pane: t.Focus, edge: layout.Right}, link)
}

// OpenAIBelow splits an assistant under the focused pane.
func (a *App) OpenAIBelow() {
	t := a.ws.ActiveTab()
	cn := a.focused()
	if t == nil || cn == nil {
		return
	}
	link := 0
	if cn.kind == layout.Terminal {
		link = cn.id
	}
	a.OpenAI(cn.host.ID, openAt{pane: t.Focus, edge: layout.Bottom}, link)
}

func (a *App) refreshHost(c *conn) {
	if h, ok := a.host(c.host.ID); ok {
		c.host = h
	}
}

func (a *App) refreshMode(c *conn) {
	if c == nil || c.ai == nil {
		return
	}
	a.refreshHost(c)
	if (c.ai.sess != nil || c.ai.acp != nil) && !reflect.DeepEqual(c.ai.scope, a.aiScope(c)) {
		a.invalidateAI(c)
	}
	m := policy.Resolve(policy.Mode(a.settings.AIDefaultMode), policy.Mode(c.host.AIPolicy.Mode), policy.Mode(c.ai.mode))
	c.ai.mu.Lock()
	changed := c.ai.resolved != "" && c.ai.resolved != m
	c.ai.resolved = m
	c.ai.mu.Unlock()
	if changed && (c.ai.sess != nil || c.ai.acp != nil) {
		a.invalidateAI(c)
	}
}

func (a *App) modeOf(c *conn) policy.Mode {
	if c == nil || c.ai == nil {
		return policy.ModeAsk
	}
	c.ai.mu.Lock()
	defer c.ai.mu.Unlock()
	if c.ai.resolved == "" {
		return policy.ModeAsk
	}
	return c.ai.resolved
}

func (a *App) providerByID(id string) *settings.AIProvider {
	for i := range a.settings.AIProviders {
		if a.settings.AIProviders[i].ID == id {
			return &a.settings.AIProviders[i]
		}
	}
	return nil
}

// defaultProvider is the provider a pane asks until it is given another:
// the one set as the default, else the first that can chat; "" when there
// is none.
func (a *App) defaultProvider() string {
	if p := a.providerByID(a.settings.AIDefaultProvider); p != nil && p.CanChat() {
		return p.ID
	}
	return firstChat(a.settings.AIProviders)
}

// firstChat is the first of providers that can chat, "" when none can.
func firstChat(providers []settings.AIProvider) string {
	for _, p := range providers {
		if p.CanChat() {
			return p.ID
		}
	}
	return ""
}

// paneProvider keeps an explicitly chosen provider pinned, including after
// deletion; only a new, unselected pane inherits the default, as does a
// pane whose provider was changed to one that cannot chat.
func (a *App) paneProvider(c *conn) *settings.AIProvider {
	if c.ai.provider != "" {
		if p := a.providerByID(c.ai.provider); p == nil || p.CanChat() {
			return p
		}
	}
	return a.providerByID(a.defaultProvider())
}

// paneModel is the model the pane asks p: its own choice, else p's default.
func paneModel(c *conn, p *settings.AIProvider) string {
	if c.ai.model != "" {
		return c.ai.model
	}
	return p.Model
}

// paneEffort is the pane's thinking level as p's format takes it: the
// Anthropic format cannot turn thinking off, so none is its default there.
func paneEffort(c *conn, p *settings.AIProvider) string {
	if c.ai.effort == llm.EffortNone && p.Format == settings.AIFormatAnthropic {
		return ""
	}
	return c.ai.effort
}

func (a *App) aiClient(c *conn) (*llm.Client, error) {
	return a.providerClient(a.paneProvider(c))
}

func (a *App) providerClient(p *settings.AIProvider) (*llm.Client, error) {
	if p == nil || p.Model == "" || p.BaseURL == "" {
		return nil, errAIProvider
	}
	b, err := a.kc.Get(keychain.AIKey(p.ID))
	defer clear(b)
	if err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return nil, err
	}
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil, errAIKey
	}
	return a.llmClient(*p, string(bytes.TrimSpace(b))), nil
}

func (a *App) ensureAI(c *conn) error {
	if c.ai.agent != "builtin" {
		return errAIBackend
	}
	if c.ai.provider == "" {
		c.ai.provider = a.defaultProvider()
	}
	a.refreshMode(c)
	if c.ai.sess != nil && c.ai.target != aiTarget(c.host) {
		// The host was changed to another protocol: the conversation goes
		// on with the tools that now reach it.
		c.ai.sess, c.ai.tools = nil, nil
	}
	if c.ai.sess != nil {
		if c.ai.box != nil && a.newModel == nil {
			cl, err := a.aiClient(c)
			if err != nil {
				c.ai.box.set(nil, "", "")
				return err
			}
			p := a.paneProvider(c)
			c.ai.box.set(cl, paneModel(c, p), paneEffort(c, p))
		}
		if c.ai.dirty {
			c.ai.sess.Configure(a.aiGate(c), a.settings.AIRedact, a.settings.AIMaxToolCalls)
			c.ai.dirty = false
		}
		return nil
	}
	var streamer agent.Streamer
	if a.newModel != nil {
		streamer = a.newModel(c)
		if streamer == nil {
			return errAIProvider
		}
	} else {
		cl, err := a.aiClient(c)
		if err != nil {
			return err
		}
		box := &modelBox{}
		p := a.paneProvider(c)
		box.set(cl, paneModel(c, p), paneEffort(c, p))
		c.ai.box = box
		streamer = box
	}
	target := aiTarget(c.host)
	var tools agent.Tools
	if a.newTools != nil {
		tools = a.newTools(c)
	}
	if tools == nil && target == agent.TargetSSH {
		tools = newHostTools(a, c)
	} else if tools == nil {
		tools = newTermTools(a, c)
	}
	c.ai.tools, c.ai.target = tools, target
	msgs := c.ai.msgs
	if len(msgs) == 0 {
		// A conversation saved without its messages, or carried on by
		// another agent, is told again.
		msgs = itemMessages(c.ai.items, replayMax)
	}
	c.ai.sess = agent.Restore(agent.Config{Model: streamer, Gate: a.aiGate(c), Tools: tools, Redact: a.settings.AIRedact, Target: target, MaxToolCalls: a.settings.AIMaxToolCalls}, msgs)
	c.ai.dirty = false
	return nil
}

func (a *App) aiGate(c *conn) policy.Gate {
	c.ai.scope = a.aiScope(c)
	mode := func() policy.Mode { return a.modeOf(c) }
	ask := aiPrompter{a: a, id: c.id}
	var gate policy.Gate
	if a.newGate != nil {
		gate = a.newGate(mode, ask)
	} else {
		var rev policy.Reviewer
		if (a.newModel == nil || settings.IsACP(c.ai.agent)) && a.settings.AIReviewProvider != "" {
			rev = a.newReviewer(c)
		}
		var aud policy.Auditor
		if a.p.DataDir != "" {
			if a.audit == nil {
				a.audit = audit.New(a.p.DataDir)
			}
			aud = a.audit
		}
		gate = policy.NewGate(policy.GateConfig{
			Engine:    aiEngine(c),
			Mode:      mode,
			Scope:     a.aiScope(c),
			Prompter:  ask,
			Reviewer:  rev,
			Auditor:   aud,
			SessionID: strconv.Itoa(c.id),
			PaneID:    strconv.Itoa(c.id),
			Agent:     c.ai.agent,
			Host:      c.host.ID,
			HostMeta:  c.host.Label(),
			Output:    func() string { return a.aiOutput(c) },
			Task:      func() string { return a.aiTask(c) },
			Earlier:   func() []string { return a.aiEarlier(c) },
		})
	}
	return gate
}

func (a *App) aiScope(c *conn) policy.Scope {
	return policy.Scope{
		WritableRoots: append([]string(nil), c.host.AIPolicy.WritableRoots...),
		Sensitive:     append([]string(nil), c.host.AIPolicy.Sensitive...),
	}
}

// aiOutput is the linked terminal text, read on the UI thread.
func (a *App) aiOutput(c *conn) string {
	ch := make(chan string, 1)
	a.post(func() { ch <- a.aiTerminal(c) })
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case s := <-ch:
		return s
	case <-timer.C:
		return ""
	}
}

// SendAI asks the pane's agent. Nothing is sent until the host's first-use
// notice has been confirmed.
func (a *App) SendAI(id int, text string) {
	text = strings.TrimSpace(text)
	c := a.conns[id]
	if c == nil || c.ai == nil || (text == "" && len(c.ai.attach) == 0) || c.ai.running {
		return
	}
	if !a.consented(c.host.ID) {
		a.consent = &consentAsk{host: c.host.ID, conn: id, text: text, open: true}
		return
	}
	if !a.unlocked() && a.aiNeedsSecrets(c) {
		a.unlockAISend(c, text)
		return
	}
	files := c.ai.attach
	if settings.IsACP(c.ai.agent) && !hasSSH(c.host) {
		// An external agent's tools are SSH's; the built-in one types.
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: "acp-ssh"})
		return
	}
	if settings.IsACP(c.ai.agent) {
		a.noteQuestion(c, text, files)
		a.sendACP(c, text, files)
		return
	}
	if err := a.ensureAI(c); err != nil {
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: aiErrText(err)})
		return
	}
	a.noteQuestion(c, text, files)
	a.aiRecent[c.host.ID] = c.id
	c.ai.items = append(c.ai.items, aiItem{kind: "user", text: text, files: files})
	c.ai.input = ""
	c.ai.attach = nil
	c.ai.task = text
	c.ai.gen++
	meta := a.aiMeta(c)
	term := a.aiTerminal(c)
	session := c.ai.sess
	user, parts := attachTurn(text, files)
	a.launch(c, func(ctx context.Context, emit func(agent.Event)) error {
		return session.TurnWith(ctx, user, parts, meta, term, emit)
	})
}

func (a *App) aiNeedsSecrets(c *conn) bool {
	return a.keepsChats(c) ||
		c.ai.agent == "builtin" && a.newModel == nil ||
		settings.IsACP(c.ai.agent) && a.connectionNeedsSecrets(c.host) ||
		a.settings.AIReviewProvider != "" && (a.newModel == nil || settings.IsACP(c.ai.agent))
}

func (a *App) unlockAISend(c *conn, text string) {
	state, input, gen := c.ai, c.ai.input, c.ai.gen
	agentID, provider, model, effort := state.agent, state.provider, state.model, state.effort
	files := cloneAIAttach(state.attach)
	a.withSecrets(func() {
		if a.conns[c.id] != c || c.ai != state || state.gen != gen || state.input != input ||
			state.agent != agentID || state.provider != provider || state.model != model || state.effort != effort ||
			!reflect.DeepEqual(state.attach, files) {
			return
		}
		a.SendAI(c.id, text)
	})
}

// cloneAIAttach pins an authorization action to the draft's file contents.
func cloneAIAttach(files []*aiAttach) []*aiAttach {
	if files == nil {
		return nil
	}
	out := make([]*aiAttach, len(files))
	for i, file := range files {
		copy := *file
		copy.data = bytes.Clone(file.data)
		out[i] = &copy
	}
	return out
}

// ContinueAI resumes a turn that paused at its tool-call limit.
func (a *App) ContinueAI(id int) {
	c := a.conns[id]
	if c == nil || c.ai == nil || c.ai.sess == nil || !c.ai.paused || c.ai.running {
		return
	}
	session := c.ai.sess
	a.launch(c, func(ctx context.Context, emit func(agent.Event)) error {
		return session.Continue(ctx, emit)
	})
}

func (a *App) launch(c *conn, run func(context.Context, func(agent.Event)) error) {
	if err := a.ensureAI(c); err != nil {
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: aiErrText(err)})
		return
	}
	a.refreshMode(c)
	ctx, cancel := context.WithCancel(context.Background())
	c.ai.cancel = cancel
	c.ai.running = true
	c.ai.paused = false
	id := c.id
	c.ai.turn++
	turn := c.ai.turn
	tools := c.ai.tools
	if h, ok := tools.(*hostTools); ok {
		h.snap()
	}
	go func() {
		// end before onAIDone: the next SendAI waits on running, and must
		// not dial while this turn is still releasing the connection.
		err := func() error {
			if h, ok := tools.(*hostTools); ok {
				h.noteTurn(ctx)
				defer h.end()
			}
			return run(ctx, func(ev agent.Event) {
				a.post(func() {
					if current := a.conns[id]; current != nil && current.ai != nil && current.ai.turn == turn {
						a.onAIEvent(id, ev)
					}
				})
			})
		}()
		a.post(func() {
			if current := a.conns[id]; current != nil && current.ai != nil && current.ai.turn == turn {
				a.onAIDone(id, err)
			}
		})
	}()
}

func (a *App) onAIEvent(id int, ev agent.Event) {
	c := a.conns[id]
	if c == nil || c.ai == nil {
		return
	}
	switch ev.Kind {
	case agent.EventText:
		c.ai.items = appendChunk(c.ai.items, "text", ev.Text)
	case agent.EventThinking:
		c.ai.items = appendChunk(c.ai.items, "think", ev.Text)
	case agent.EventTool:
		if ev.Tool == nil {
			return
		}
		for i := range c.ai.items {
			if c.ai.items[i].kind == "tool" && c.ai.items[i].tool.ID == ev.Tool.ID {
				c.ai.items[i].tool = *ev.Tool
				return
			}
		}
		c.ai.items = append(c.ai.items, aiItem{kind: "tool", tool: *ev.Tool})
	case agent.EventNotice:
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: ev.Text})
	case agent.EventPause:
		c.ai.paused = true
		c.ai.pauseCalls = ev.Calls
	case agent.EventDone:
		if ev.Text != "" && trailingText(c.ai.items) != ev.Text {
			c.ai.items = append(c.ai.items, aiItem{kind: "text", text: ev.Text})
		}
	}
}

func (a *App) onAIDone(id int, err error) {
	c := a.conns[id]
	if c == nil || c.ai == nil {
		return
	}
	c.ai.running = false
	c.ai.cancel = nil
	if c.ai.agent == "builtin" && c.ai.sess != nil {
		c.ai.msgs = c.ai.sess.History()
		// An external agent's session no longer carries all of it.
		c.ai.acpSID, c.ai.acpAgent, c.ai.acpCwd = "", "", ""
	}
	defer a.saveChat(c)
	if err == nil {
		c.ai.paused = c.ai.sess != nil && c.ai.sess.Paused()
		return
	}
	if errors.Is(err, agent.ErrPaused) {
		c.ai.paused = true
		return
	}
	var acpError *acpFailure
	if errors.Is(err, context.Canceled) && !errors.As(err, &acpError) {
		return
	}
	c.ai.items = append(c.ai.items, aiItem{kind: "err", text: a.scrubAI(c, err)})
}

func (a *App) scrubAI(c *conn, err error) string { return aiErrText(err) }

// Only typed, local failure details reach the UI; provider bodies, URLs and
// arbitrary tool errors may contain credentials and are never rendered here
func aiErrText(err error) string {
	var acpError *acpFailure
	if errors.As(err, &acpError) {
		return acpError.Error()
	}
	var api *llm.APIError
	var network net.Error
	switch {
	case errors.Is(err, errAIKey):
		return "key"
	case errors.Is(err, errAIBackend):
		return "backend"
	case errors.Is(err, errAIProvider):
		return "provider"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &api):
		// The provider's message says what it refused, as a model that
		// takes no tools or no reasoning level.
		return "http:" + strconv.Itoa(api.Status) + ":" + api.Detail()
	case errors.Is(err, llm.ErrIncompleteResponse):
		return "incomplete"
	case errors.Is(err, llm.ErrProtocol):
		return "protocol"
	case errors.Is(err, llm.ErrTooBig):
		return "size"
	case errors.As(err, &network):
		if network.Timeout() {
			return "timeout"
		}
		return "network"
	default:
		return "request"
	}
}

func appendChunk(items []aiItem, kind, text string) []aiItem {
	if text == "" {
		return items
	}
	if n := len(items); n > 0 && items[n-1].kind == kind {
		items[n-1].text += text
		return items
	}
	if kind == "think" && strings.TrimSpace(text) == "" {
		// Blank lines some models send as thinking, between their text
		// and their tool calls, start nothing.
		return items
	}
	return append(items, aiItem{kind: kind, text: text})
}

// dropAIItem removes the pane's error i, which the user dismissed, from
// the conversation and its history.
func (a *App) dropAIItem(c *conn, i int) {
	if i < 0 || i >= len(c.ai.items) || c.ai.items[i].kind != "err" {
		return
	}
	c.ai.items = append(c.ai.items[:i:i], c.ai.items[i+1:]...)
	if !c.ai.running && a.unlocked() {
		a.saveChat(c)
	}
}

func trailingText(items []aiItem) string {
	start := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].kind != "text" {
			break
		}
		start = i
	}
	var b strings.Builder
	for _, it := range items[start:] {
		b.WriteString(it.text)
	}
	return b.String()
}

// StopAI cancels the pane's turn and any confirmation it is waiting on.
func (a *App) StopAI(id int) {
	c := a.conns[id]
	if c == nil || c.ai == nil {
		return
	}
	if c.ai.cancel != nil {
		c.ai.cancel()
	}
	if c.ai.acp != nil {
		c.ai.acp.close()
		c.ai.acp = nil
	}
	if p := c.ai.pending; p != nil {
		c.ai.pending = nil
		select {
		case p.ch <- policy.Cancelled:
		default:
		}
	}
}

// StopAllAI stops every AI pane.
func (a *App) StopAllAI() {
	for _, c := range a.conns {
		if c.kind == layout.AI {
			a.StopAI(c.id)
		}
	}
}

// stopShortcut is ⌘. / Ctrl+.. Pending sheets close as cancelled and are
// not applied. A focused AI pane stops itself; otherwise the recent AI
// pane on the focused host stops.
func (a *App) stopShortcut() {
	a.consent = nil
	a.full = nil
	for _, c := range a.conns {
		if c.kind == layout.AI && c.ai != nil && c.ai.pending != nil {
			a.answerAI(c, policy.Cancelled)
		}
	}
	if cn := a.focused(); cn != nil && cn.kind == layout.AI {
		a.StopAI(cn.id)
		return
	}
	if cn := a.focused(); cn != nil {
		if id := a.aiRecent[cn.host.ID]; id != 0 {
			a.StopAI(id)
		}
	}
}

// InsertAI writes text into the linked terminal's input, without Enter.
// Control characters, including CR and LF, are removed first.
func (a *App) InsertAI(id int, text string) {
	c := a.conns[id]
	if c == nil || c.ai == nil {
		return
	}
	t := a.conns[c.ai.link]
	if t == nil || t.tv == nil {
		return
	}
	t.tv.send([]byte(stripInsert(text)))
}

func stripInsert(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// syncLink links c, an AI pane in tab t, to a terminal of its host in t:
// the focused one, else the one it has while it is still there, else the
// first. The pane works with its tab's terminal; it is not chosen.
func (a *App) syncLink(t *layout.Tab, c *conn) {
	var focus, kept, first *conn
	for _, p := range t.Panes() {
		o := a.conns[p.Item.ID]
		if o == nil || o.kind != layout.Terminal || o.host.ID != c.host.ID {
			continue
		}
		switch {
		case p.ID == t.Focus:
			focus = o
		case o.id == c.ai.link:
			kept = o
		}
		if first == nil {
			first = o
		}
	}
	c.ai.link = 0
	for _, o := range []*conn{focus, kept, first} {
		if o != nil {
			c.ai.link = o.id
			break
		}
	}
}

// AskAI sends selection to the AI pane of from's tab, opened beside from
// when the tab has none. The selection is marked untrusted.
func (a *App) AskAI(from *conn, selection string) {
	if from == nil || strings.TrimSpace(selection) == "" {
		return
	}
	pane := 0
	if _, n := a.ws.Locate(from.id); n != nil {
		pane = n.ID
	}
	link := 0
	if from.kind == layout.Terminal {
		link = from.id
	}
	id, err := a.OpenAI(from.host.ID, openAt{pane: pane, edge: layout.Right}, link)
	if err != nil {
		return
	}
	a.SendAI(id, agent.WrapUntrusted("selection", selection))
}

func (a *App) consented(hostID string) bool {
	for _, id := range a.settings.AIConsentHosts {
		if id == hostID {
			return true
		}
	}
	return false
}

func (a *App) grantConsent(hostID string) error {
	if a.consented(hostID) {
		return nil
	}
	s := a.settings
	s.AIConsentHosts = append(append([]string{}, s.AIConsentHosts...), hostID)
	return a.ApplySettings(s)
}

func (a *App) aiMeta(c *conn) agent.HostMeta {
	a.refreshHost(c)
	cwd := ""
	if t := a.conns[c.ai.link]; t != nil {
		cwd = t.cwd
	}
	return agent.HostMeta{Name: c.host.Label(), Address: c.host.Addr(), User: c.host.User, Cwd: cwd,
		Protocol: a.aiConnection(c.host), Provider: c.host.Provider, Renewal: aiRenewal(c.host.Renewal, time.Now()), Note: c.host.Note}
}

func (a *App) aiTerminal(c *conn) string {
	t := a.conns[c.ai.link]
	if t == nil || t.kind != layout.Terminal || t.s == nil || t.s.Term == nil {
		return ""
	}
	text := t.s.Term.Text(vt.Pos{}, vt.Pos{Line: 1 << 30, Col: 1 << 20})
	lines := strings.Split(text, "\n")
	n := a.settings.AIContextLines
	if n < 1 {
		n = 200
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (a *App) setPaneMode(c *conn, mode string) {
	if c == nil || c.ai == nil {
		return
	}
	if mode == "full" && c.ai.mode != "full" {
		a.openFull("pane", c.id)
		return
	}
	a.invalidateAI(c)
	c.ai.mode = mode
	a.refreshMode(c)
	a.saveLayout()
}

func (a *App) openFull(kind string, conn int) {
	a.full = &fullAsk{kind: kind, conn: conn, open: true}
}

func (a *App) applyFull() {
	f := a.full
	if f == nil {
		return
	}
	switch f.kind {
	case "pane":
		if c := a.conns[f.conn]; c != nil && c.ai != nil {
			c.ai.mode = "full"
			a.refreshMode(c)
			a.saveLayout()
		}
	case "settings":
		s := a.settings
		s.AIDefaultMode = "full"
		_ = a.ApplySettings(s)
		for _, c := range a.conns {
			a.refreshMode(c)
		}
	case "host":
		if a.form != nil {
			a.form.fullOK = true
			a.form.aiIdx = 3
			a.form.aiPrev = 3
		}
	}
}

// aiPrompter is policy.Prompter. Ask blocks the agent goroutine, never the UI thread.
type aiPrompter struct {
	a  *App
	id int
}

func (p aiPrompter) Ask(ctx context.Context, pr policy.Prompt) policy.Answer {
	if ctx.Err() != nil {
		return policy.Cancelled
	}
	ch := make(chan policy.Answer, 1)
	p.a.post(func() {
		c := p.a.conns[p.id]
		if c == nil || c.ai == nil || ctx.Err() != nil {
			select {
			case ch <- policy.Cancelled:
			default:
			}
			return
		}
		c.ai.pending = &aiPend{prompt: pr, ch: ch}
	})
	select {
	case ans := <-ch:
		return ans
	case <-ctx.Done():
		p.a.post(func() {
			c := p.a.conns[p.id]
			if c != nil && c.ai != nil && c.ai.pending != nil && c.ai.pending.ch == ch {
				c.ai.pending = nil
			}
		})
		return policy.Cancelled
	}
}

func (a *App) answerAI(c *conn, ans policy.Answer) {
	if c == nil || c.ai == nil || c.ai.pending == nil {
		return
	}
	p := c.ai.pending
	c.ai.pending = nil
	select {
	case p.ch <- ans:
	default:
	}
}

func (a *App) pendingAI() *conn {
	if cn := a.focused(); cn != nil && cn.ai != nil && cn.ai.pending != nil {
		return cn
	}
	var best *conn
	for _, c := range a.conns {
		if c.ai == nil || c.ai.pending == nil {
			continue
		}
		if best == nil || c.id < best.id {
			best = c
		}
	}
	return best
}

// invalidateAI cancels the current turn before applying new credentials or permissions
// The session history stays in memory; the next turn gets a fresh gate and no old grants
func (a *App) invalidateAI(c *conn) {
	if c == nil || c.ai == nil {
		return
	}
	a.StopAI(c.id)
	c.ai.dirty = true
	if c.ai.box != nil {
		c.ai.box.mu.Lock()
		c.ai.box.s = nil
		c.ai.box.mu.Unlock()
	}
}

func providerIn(s settings.Settings, id string) *settings.AIProvider {
	if id == "" {
		return nil
	}
	for i := range s.AIProviders {
		if s.AIProviders[i].ID == id {
			return &s.AIProviders[i]
		}
	}
	return nil
}

func (a *App) invalidateAIProvider(id string) {
	for _, c := range a.conns {
		if c.ai != nil && (c.ai.agent == "builtin" && c.ai.provider == id || a.settings.AIReviewProvider == id) {
			a.invalidateAI(c)
		}
	}
}
