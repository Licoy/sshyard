package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/ai/audit"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/ai/remote"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/mcp"
	"github.com/Licoy/sshyard/internal/session"
	"golang.org/x/crypto/ssh"
)

// mcpState is the agents' server (open API, MCP and skill) and the SSH
// connections agents opened through it. srv, err, token and skill belong
// to the UI thread; the sessions, which the tools change on the server's
// goroutines, are under mu.
type mcpState struct {
	srv   *mcp.Server
	err   error
	token string
	// skill is the SKILL.md served and installed, for this token and port.
	skill    string
	skillErr error

	mu         sync.Mutex
	sessions   []*mcpSession
	next       int
	pending    int
	generation uint64
	life       context.Context
	cancel     context.CancelFunc
	work       sync.WaitGroup
}

// mcpSession is one connection an agent opened with connect. It holds a
// reference on the host's pooled connection until it is closed.
type mcpSession struct {
	id      string
	host    assets.Host
	client  *ssh.Client
	release func()
	since   time.Time
	life    *mcpSessionLife
}

// The session owns command cancellation, never the shared SSH transport.
type mcpSessionLife struct {
	ctx    context.Context
	cancel context.CancelFunc
	runs   sync.WaitGroup
	done   chan struct{}
}

func (s *mcpSession) close(done func()) {
	s.life.cancel()
	// Remote cancellation may wait for process cleanup; never block the UI thread.
	go func() { defer done(); defer close(s.life.done); s.life.runs.Wait(); s.release() }()
}

// mcpAsk is the risk notice shown before the endpoint is turned on. at
// is when it first showed; confirming waits mcpWait from then.
type mcpAsk struct {
	open bool
	at   time.Time
}

const (
	// mcpWait is how long the risk notice shows before it can be confirmed.
	mcpWait = 3 * time.Second
	// mcpMaxSessions bounds the connections agents hold at once.
	mcpMaxSessions = 16
	// mcpTimeout is a command's limit unless the agent asks for another,
	// within remote's bounds.
	mcpTimeout = 60 * time.Second
)

// errMCPNeedsInput refuses a host that logs in with something the user
// types: the endpoint never asks for a secret, nor hands one out.
var errMCPNeedsInput = errors.New("this host needs a password or passphrase typed in SSHYard; MCP never asks for secrets, so the connection was refused")

// syncMCP starts or stops the endpoint as the settings say, on the
// settings' port. Restarting drops every agent connection.
func (a *App) syncMCP() {
	a.stopMCP()
	if !a.unlocked() || !a.settings.MCPEnabled {
		return
	}
	tok, err := a.mcpToken()
	if err == nil {
		h := a.mcpHandler(tok)
		skill := skillText("http://127.0.0.1:"+strconv.Itoa(a.settings.MCPPort), tok, h.Tools)
		h.Skill = func() string { return skill }
		a.mcp.token, a.mcp.skill = tok, skill
		a.mcp.srv, err = mcp.Listen(a.settings.MCPPort, h)
	}
	a.mcp.err = err
	if err == nil {
		a.mcp.skillErr = a.refreshSkills()
	}
}

// stopMCP closes the endpoint, which cancels the commands running, and
// the agents' connections.
func (a *App) stopMCP() {
	if a.mcp.srv != nil {
		a.mcp.srv.Close()
		a.mcp.srv = nil
	}
	a.mcp.err, a.mcp.skill, a.mcp.skillErr = nil, "", nil
	a.mcp.mu.Lock()
	a.mcp.generation++
	if a.mcp.cancel != nil {
		a.mcp.cancel()
	}
	a.mcp.life, a.mcp.cancel = nil, nil
	ss := a.mcp.sessions
	a.mcp.sessions = nil
	a.mcp.mu.Unlock()
	for _, s := range ss {
		s.close(a.mcp.work.Done)
	}
}

// waitMCP is the final app-close barrier, after stopMCP has cancelled work.
func (a *App) waitMCP() { a.mcp.work.Wait() }

// mcpToken is the endpoint's bearer token, made the first time.
func (a *App) mcpToken() (string, error) {
	if b, err := a.kc.Get(keychain.MCPToken); err == nil && len(b) > 0 {
		return string(b), nil
	} else if err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := "sshyard_" + base64.RawURLEncoding.EncodeToString(b[:])
	return tok, a.kc.Set(keychain.MCPToken, []byte(tok))
}

// newMCPToken replaces the token; agents holding the old one are cut off.
func (a *App) newMCPToken() error {
	if err := a.kc.Delete(keychain.MCPToken); err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return err
	}
	a.syncMCP()
	return errors.Join(a.mcp.err, a.mcp.skillErr)
}

// setMCP turns the endpoint on or off and saves it.
func (a *App) setMCP(on bool) error {
	s := a.settings
	s.MCPEnabled = on
	if on && !a.unlocked() {
		return keychain.ErrLocked
	}
	if err := a.ApplySettings(s); err != nil {
		return err
	}
	return a.mcp.err
}

// mcpConfig is what an agent's MCP settings need to reach the endpoint.
func (a *App) mcpConfig() string {
	if a.mcp.srv == nil {
		return ""
	}
	b, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"sshyard": map[string]any{
		"type":    "http",
		"url":     a.mcp.srv.URL(),
		"headers": map[string]string{"Authorization": "Bearer " + a.mcp.token},
	}}}, "", "  ")
	return string(b)
}

// mcpSessionList is a copy of the agents' connections, oldest first.
func (a *App) mcpSessionList() []mcpSession {
	a.mcp.mu.Lock()
	defer a.mcp.mu.Unlock()
	out := make([]mcpSession, 0, len(a.mcp.sessions))
	for _, s := range a.mcp.sessions {
		out = append(out, mcpSession{id: s.id, host: s.host, since: s.since})
	}
	return out
}

// dropMCPSession closes one agent connection; false when there is none.
func (a *App) dropMCPSession(id string) bool {
	a.mcp.mu.Lock()
	i := slices.IndexFunc(a.mcp.sessions, func(s *mcpSession) bool { return s.id == id })
	var s *mcpSession
	if i >= 0 {
		s = a.mcp.sessions[i]
		a.mcp.sessions = slices.Delete(a.mcp.sessions, i, i+1)
	}
	a.mcp.mu.Unlock()
	if s == nil {
		return false
	}
	s.close(a.mcp.work.Done)
	a.invalidate()
	return true
}

func (a *App) mcpSession(id string) (*mcpSession, error) {
	a.mcp.mu.Lock()
	defer a.mcp.mu.Unlock()
	for _, s := range a.mcp.sessions {
		if s.id == id {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no session %q; call connect first", id)
}

const mcpInstructions = "SSHYard holds the user's saved SSH hosts. Call list_groups or list_hosts to find a host, connect with its id to get a session_id, then run_command on that session and disconnect when done. Passwords, keys and passphrases are never revealed; a host that needs one typed is refused. Every command runs on a real server: prefer read-only commands and ask the user before anything destructive."

func schema(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if len(required) == 0 {
		req = []byte("[]")
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `,"additionalProperties":false}`)
}

// mcpHandler is the endpoint and its tools. The tools run on the server's
// goroutines: they read the store, the keychain and the pool, all safe
// for that, and never the UI's state.
func (a *App) mcpHandler(token string) *mcp.Handler {
	a.mcp.mu.Lock()
	if a.mcp.life == nil {
		a.mcp.life, a.mcp.cancel = context.WithCancel(context.Background())
	}
	life := a.mcp.life
	a.mcp.mu.Unlock()
	if a.audit == nil && a.p.DataDir != "" {
		a.audit = audit.New(a.p.DataDir)
	}
	aud := a.audit
	version := a.p.Version
	if version == "" {
		version = "dev"
	}
	return &mcp.Handler{
		Name: "sshyard", Version: version, Instructions: mcpInstructions, Token: token,
		Tools: []mcp.Tool{
			{
				Name:        "list_groups",
				Description: "List the host groups and how many hosts each has. Hosts outside any group are counted as ungrouped.",
				Schema:      schema(""),
				Call:        func(context.Context, json.RawMessage) (string, error) { return a.mcpGroups(), nil },
			},
			{
				Name:        "list_hosts",
				Description: "List saved SSH hosts: id, name, group, address and port, with the provider, subscription renewal (start, period, next renewal day and days left) and note the user keeps for them. Give group to list one group only (an empty string lists the ungrouped hosts).",
				Schema:      schema(`"group":{"type":"string","description":"Only hosts in this group"}`),
				Call:        a.mcpHosts,
			},
			{
				Name:        "connect",
				Description: "Open an SSH connection to a saved host with the credentials saved in SSHYard, which are never revealed. Returns a session_id for run_command. Fails if the host needs a password or passphrase typed, or its host key is not trusted yet.",
				Schema:      schema(`"host_id":{"type":"string","description":"The id from list_hosts"}`, "host_id"),
				Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
					request := ctx
					ctx, cancel := context.WithCancel(life)
					defer cancel()
					stop := context.AfterFunc(request, cancel)
					defer stop()
					if request.Err() != nil {
						return "", request.Err()
					}
					if life.Err() != nil {
						return "", life.Err()
					}
					return a.mcpConnect(ctx, raw)
				},
			},
			{
				Name:        "run_command",
				Description: "Run a shell command (sh -c) on a connected host and return its exit_code and combined output (at most 1 MiB). The command is stopped when it times out or the request is cancelled.",
				Schema: schema(`"session_id":{"type":"string"},`+
					`"command":{"type":"string","description":"The shell command"},`+
					`"cwd":{"type":"string","description":"Remote working directory; empty keeps the login directory"},`+
					`"timeout_seconds":{"type":"integer","minimum":5,"maximum":1800,"description":"Default 60"}`, "session_id", "command"),
				Call: func(ctx context.Context, raw json.RawMessage) (string, error) { return a.mcpRun(ctx, raw, aud) },
			},
			{
				Name:        "list_sessions",
				Description: "List the open SSH sessions made with connect.",
				Schema:      schema(""),
				Call:        func(context.Context, json.RawMessage) (string, error) { return a.mcpSessions(), nil },
			},
			{
				Name:        "disconnect",
				Description: "Close a session made with connect.",
				Schema:      schema(`"session_id":{"type":"string"}`, "session_id"),
				Call:        a.mcpDisconnect,
			},
		},
	}
}

func toJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

type mcpHostInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	// Protocol is "ssh", "telnet" or "serial": agents connect to SSH
	// hosts only.
	Protocol string `json:"protocol"`
	// Provider, Renewal and Note are what the user keeps of the host's
	// subscription.
	Provider string      `json:"provider,omitempty"`
	Renewal  *mcpRenewal `json:"renewal,omitempty"`
	Note     string      `json:"note,omitempty"`
}

// mcpRenewal is a host's subscription, with its next renewal as of the
// call.
type mcpRenewal struct {
	Start       string `json:"start"`
	EveryMonths int    `json:"every_months"`
	RemindDays  int    `json:"remind_days"`
	Next        string `json:"next"`
	DaysLeft    int    `json:"days_left"`
}

func mcpHostOf(h assets.Host) mcpHostInfo {
	port := h.Port
	if port == 0 {
		port = h.Protocol.DefaultPort()
	}
	proto := string(h.Protocol)
	if h.SSH() {
		proto = "ssh"
	}
	info := mcpHostInfo{ID: h.ID, Name: h.Label(), Group: h.Group, Address: h.Address, Port: port, Protocol: proto, Provider: h.Provider, Note: h.Note}
	now := time.Now()
	if next, ok := h.Renewal.Next(now); ok {
		days, _ := h.Renewal.DaysLeft(now)
		info.Renewal = &mcpRenewal{Start: h.Renewal.Start, EveryMonths: h.Renewal.Months, RemindDays: h.Renewal.Remind, Next: next.Format(assets.DateLayout), DaysLeft: days}
	}
	return info
}

func (a *App) mcpGroups() string {
	type group struct {
		Name  string `json:"name"`
		Hosts int    `json:"hosts"`
	}
	count := map[string]int{}
	for _, h := range a.store.List() {
		count[h.Group]++
	}
	groups := []group{}
	for _, g := range a.store.Groups() {
		groups = append(groups, group{g, count[g]})
	}
	return toJSON(map[string]any{"groups": groups, "ungrouped_hosts": count[""]})
}

func (a *App) mcpHosts(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Group *string `json:"group"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	hosts := []mcpHostInfo{}
	for _, h := range a.store.List() {
		if args.Group == nil || h.Group == *args.Group {
			hosts = append(hosts, mcpHostOf(h))
		}
	}
	return toJSON(map[string]any{"hosts": hosts}), nil
}

// mcpLogin is how h logs in for an agent: only what is saved, never what
// was typed this run. Every hop of h's jump chain is checked up front, as
// the pool may already hold a connection the user typed a password for.
func (a *App) mcpLogin(h assets.Host) error {
	path, err := a.store.JumpPath(h.ID)
	if err != nil {
		return err
	}
	for _, hop := range append(path, h) {
		switch hop.Auth {
		case assets.AuthAgent, assets.AuthNone:
		case assets.AuthPassword, assets.AuthKey:
			if _, err := a.hostCredentials(hop, nil); err != nil {
				if errors.Is(err, session.ErrNoCredentials) || errors.Is(err, errMCPNeedsInput) {
					return errMCPNeedsInput
				}
				return err
			}
		case assets.AuthCredential:
			cr, ok := a.store.Credential(hop.CredentialID)
			if !ok || cr.AskPassphrase {
				return errMCPNeedsInput
			}
		case assets.AuthAsk:
			if !a.lkMCP.Load() {
				return errMCPNeedsInput
			}
		default:
			return errMCPNeedsInput
		}
	}
	return nil
}

// mcpCredentials reads h's saved secrets from the keychain for the dial;
// they go to the SSH handshake and nowhere else.
func (a *App) mcpCredentials(h assets.Host) (session.Credentials, error) {
	var out session.Credentials
	if h.Auth == assets.AuthPassword || h.Auth == assets.AuthKey {
		return a.hostCredentials(h, nil)
	}
	if h.Auth == assets.AuthAgent {
		out.Agent = true
		return out, nil
	}
	if h.Auth == assets.AuthNone {
		out.None = true
		return out, nil
	}
	if h.Auth == assets.AuthAsk && a.lkMCP.Load() {
		// What is typed is never used here; only what signs unasked.
		a.localKeyCredentials(&out, h, nil)
		return out, nil
	}
	cr, ok := a.store.Credential(h.CredentialID)
	if h.Auth != assets.AuthCredential || !ok || cr.AskPassphrase {
		return out, errMCPNeedsInput
	}
	out.User = cr.User
	switch cr.Kind {
	case assets.CredPassword:
		b, err := a.kc.Get(keychain.Cred(cr.ID, keychain.Password))
		if err != nil {
			return out, errMCPNeedsInput
		}
		out.Password = string(b)
	case assets.CredKey:
		key, err := a.kc.Get(keychain.Cred(cr.ID, keychain.PrivateKey))
		if err != nil {
			return out, errMCPNeedsInput
		}
		out.PrivateKey, out.Certificate = key, []byte(cr.Certificate)
		out.Passphrase, err = a.kc.Get(keychain.Cred(cr.ID, keychain.Passphrase))
		if err != nil && cr.Encrypted {
			return out, errMCPNeedsInput
		}
	}
	return out, nil
}

func (a *App) mcpConnect(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		HostID string `json:"host_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	h, ok := a.store.Get(args.HostID)
	if !ok {
		return "", fmt.Errorf("no host %q; see list_hosts", args.HostID)
	}
	if !h.SSH() {
		return "", fmt.Errorf("host %q is a %s host; agents connect to SSH hosts only", args.HostID, h.Protocol)
	}
	if err := a.mcpLogin(h); err != nil {
		return "", err
	}
	a.mcp.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.mcp.mu.Unlock()
		return "", err
	}
	if len(a.mcp.sessions)+a.mcp.pending >= mcpMaxSessions {
		a.mcp.mu.Unlock()
		return "", fmt.Errorf("%d sessions are open or connecting; disconnect one first", mcpMaxSessions)
	}
	a.mcp.pending++
	a.mcp.work.Add(1)
	generation := a.mcp.generation
	if a.mcp.life == nil {
		a.mcp.life, a.mcp.cancel = context.WithCancel(context.Background())
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.mcp.life, cancel)
	a.mcp.mu.Unlock()
	defer cancel()
	defer stop()
	reserved := true
	defer func() {
		if reserved {
			a.mcp.mu.Lock()
			a.mcp.pending--
			a.mcp.mu.Unlock()
		}
		a.mcp.work.Done()
	}()
	cfg := a.hostConfig(h, nil, func(context.Context) (session.Credentials, error) { return a.mcpCredentials(h) })
	for i := range cfg.Jumps {
		id := cfg.Jumps[i].ID
		cfg.Jumps[i].Credentials = func(context.Context) (session.Credentials, error) {
			j, ok := a.store.Get(id)
			if !ok {
				return session.Credentials{}, errMCPNeedsInput
			}
			return a.mcpCredentials(j)
		}
	}
	client, release, err := a.pool.Acquire(ctx, a.poolKey(h), cfg)
	if err != nil {
		return "", mcpDialErr(err)
	}
	a.mcp.mu.Lock()
	if ctx.Err() != nil || generation != a.mcp.generation {
		a.mcp.mu.Unlock()
		release()
		return "", context.Canceled
	}
	lifeCtx, lifeCancel := context.WithCancel(a.mcp.life)
	s := &mcpSession{host: h, client: client, release: release, since: time.Now(), life: &mcpSessionLife{ctx: lifeCtx, cancel: lifeCancel, done: make(chan struct{})}}
	a.mcp.next++
	s.id = "s" + strconv.Itoa(a.mcp.next)
	a.mcp.pending--
	reserved = false
	a.mcp.work.Add(1)
	a.mcp.sessions = append(a.mcp.sessions, s)
	a.mcp.mu.Unlock()
	a.invalidate()
	return toJSON(map[string]any{"session_id": s.id, "host": mcpHostOf(h)}), nil
}

// mcpDialErr says why a connection failed, in words for the agent.
func mcpDialErr(err error) error {
	var unknown *session.UnknownHostError
	var changed *session.HostKeyChangedError
	switch {
	case errors.As(err, &unknown):
		return fmt.Errorf("the host key of %s is not trusted yet; connect to it once in SSHYard and trust the key", unknown.Host)
	case errors.As(err, &changed):
		return fmt.Errorf("the host key of %s changed; SSHYard refused to connect. Only the user can review and trust the new key, in SSHYard", changed.Host)
	case errors.Is(err, errMCPNeedsInput), errors.Is(err, session.ErrNoCredentials),
		errors.Is(err, session.ErrPassphraseNeeded), errors.Is(err, session.ErrPassphraseWrong),
		errors.As(err, new(*session.LocalKeyError)), errors.As(err, new(*session.KeysRefusedError)):
		return errMCPNeedsInput
	}
	return fmt.Errorf("connect failed: %v", err)
}

func (a *App) mcpRun(ctx context.Context, raw json.RawMessage, aud *audit.Log) (string, error) {
	var args struct {
		SessionID string `json:"session_id"`
		Command   string `json:"command"`
		Cwd       string `json:"cwd"`
		Timeout   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", errors.New("empty command")
	}
	a.mcp.mu.Lock()
	var s *mcpSession
	for _, candidate := range a.mcp.sessions {
		if candidate.id == args.SessionID {
			s = candidate
			break
		}
	}
	if s == nil {
		a.mcp.mu.Unlock()
		return "", fmt.Errorf("no session %q; call connect first", args.SessionID)
	}
	s.life.runs.Add(1)
	a.mcp.mu.Unlock()
	defer s.life.runs.Done()
	if aud == nil {
		return "", errors.New("audit unavailable; command was not executed")
	}
	if err := aud.Prepare(time.Now()); err != nil {
		return "", mcpAuditError{"audit unavailable; command was not executed", err}
	}
	timeout := mcpTimeout
	if args.Timeout > 0 {
		timeout = time.Duration(min(max(args.Timeout, int(remote.MinCommandTimeout/time.Second)), int(remote.MaxCommandTimeout/time.Second))) * time.Second
	}
	run := remote.New(s.client, remote.Options{CommandTimeout: timeout, TurnTimeout: remote.MaxCommandTimeout})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.life.ctx, cancel)
	defer stop()
	if s.life.ctx.Err() != nil {
		cancel()
	}
	out := &commandOutput{remaining: (1 << 20) - 4096, cancel: cancel}
	counted := &remote.CountingWriter{W: out}
	start := time.Now()
	if err := aud.Record(policy.AuditRecord{Time: start, SessionID: "mcp-" + s.id, PaneID: "mcp", Agent: "mcp", Host: s.host.ID, Mode: "mcp", Where: string(policy.Remote), Command: policy.Redact(args.Command), Verdict: "started", Decider: "mcp"}); err != nil {
		return "", mcpAuditError{"audit unavailable; command was not executed", err}
	}
	code, err := run.Run(runCtx, args.Command, args.Cwd, counted, counted)
	text, limited := out.result()
	if aud != nil {
		grade := policy.NewEngine().Classify(policy.Action{Where: policy.Remote, Tool: "run_command", Command: args.Command, Dir: args.Cwd},
			policy.Scope{WritableRoots: s.host.AIPolicy.WritableRoots, Sensitive: s.host.AIPolicy.Sensitive})
		exit := code
		recordErr := aud.Record(policy.AuditRecord{
			Time: start, SessionID: "mcp-" + s.id, PaneID: "mcp", Agent: "mcp", Host: s.host.ID, Mode: "mcp",
			Where: string(policy.Remote), Category: string(grade.Category), Risk: grade.Risk.String(),
			Command: policy.Redact(args.Command), Verdict: mcpRunVerdict(err), Decider: "mcp", ExitCode: &exit,
			DurationMS: time.Since(start).Milliseconds(), OutputBytes: counted.Bytes(),
		})
		if recordErr != nil {
			return "", errors.Join(err, mcpAuditError{"command execution finished but audit recording failed; do not retry the command automatically", recordErr})
		}
	}
	switch {
	case limited && ctx.Err() == nil:
		return "", errors.New(joinOut("output passed 1 MiB; the command was stopped", text))
	case errors.Is(err, context.DeadlineExceeded):
		return "", errors.New(joinOut(fmt.Sprintf("timed out after %s; the command was stopped", timeout), text))
	case err != nil:
		return "", errors.New(joinOut(err.Error(), text))
	}
	return toJSON(map[string]any{"exit_code": code, "output": text}), nil
}

func (a *App) mcpSessions() string {
	type info struct {
		SessionID string      `json:"session_id"`
		Host      mcpHostInfo `json:"host"`
		Since     time.Time   `json:"since"`
	}
	list := []info{}
	for _, s := range a.mcpSessionList() {
		list = append(list, info{s.id, mcpHostOf(s.host), s.since.UTC().Truncate(time.Second)})
	}
	return toJSON(map[string]any{"sessions": list})
}

func (a *App) mcpDisconnect(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	s, err := a.mcpSession(args.SessionID)
	if err != nil {
		return "", err
	}
	if !a.dropMCPSession(args.SessionID) {
		return "", fmt.Errorf("no session %q", args.SessionID)
	}
	select {
	case <-s.life.done:
		return toJSON(map[string]any{"disconnected": args.SessionID}), nil
	case <-ctx.Done():
		return "", fmt.Errorf("session cancellation requested; cleanup continues: %w", ctx.Err())
	}
}

func mcpRunVerdict(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if err != nil {
		return "failed"
	}
	return "mcp"
}

// Keep the cause inspectable without exposing local audit paths to the agent.
type mcpAuditError struct {
	stage string
	cause error
}

func (e mcpAuditError) Error() string { return e.stage }
func (e mcpAuditError) Unwrap() error { return e.cause }
