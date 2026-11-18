package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/mcp"
	sdk "github.com/coder/acp-go-sdk"
)

// Config starts one agent process for one pane.
type Config struct {
	Command    string
	Args       []string
	Env        map[string]string // the agent's own variables; still filtered
	BaseEnv    []string          // KEY=VALUE. Nil uses os.Environ.
	Type       string            // codex, claude, grok, custom
	Redact     bool              // mask outbound text sent to the external agent
	Gate       policy.Gate
	Files      Files
	Exec       policy.Exec
	RemoteHome string
	CwdFollow  func() string
	// OnUpdate is called from a background goroutine and must not block.
	OnUpdate func(Update)
	// Resume is an earlier session of this agent to load instead of
	// opening a new one, when the agent can; empty opens a new one.
	Resume Resume

	// probe opens the session only to read its options: no remote tools.
	probe bool
}

// Resume names an earlier session: its ID and the sandbox it ran in,
// which the new process runs in again so that the agent finds it.
type Resume struct {
	ID  string
	Cwd string
}

// Resource is an embedded prompt block.
type Resource struct {
	URI  string
	Text string
}

// AuthChoice is one login method the agent offered.
type AuthChoice struct {
	ID   string
	Name string
	Kind string // agent, env_var, terminal
}

// AuthRequiredError means session/new refused with -32000 and at least
// one agent-type method. The Session is alive; call Authenticate, then
// Close when finished.
type AuthRequiredError struct {
	Methods []AuthChoice
}

func (e *AuthRequiredError) Error() string { return "authentication required" }

// Session is one agent process and one ACP connection.
type Session struct {
	cfg     Config
	gate    policy.Gate
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	conn    *sdk.ClientSideConnection
	sandbox string
	stderr  *ring

	stderrDone chan struct{}

	// bridge serves the remote tools; nil when they are not served.
	bridge      *mcp.Server
	bridgeToken string
	mcpHTTP     bool
	// canLoad is whether the agent offered session/load.
	canLoad bool

	mu          sync.Mutex
	sid         sdk.SessionId
	methods     []AuthChoice
	authTried   bool
	pinned      string
	bounced     bool
	options     []Option
	closing     bool
	turn        *activeTurn
	interrupted bool
	introduced  bool
	perms       []context.CancelFunc
	terms       map[string]*termProc
	// loading drops the history an agent replays while it loads a
	// session; resumed is set once it has loaded one.
	loading bool
	resumed bool

	updates     chan sdk.SessionNotification
	updateFlush chan chan struct{}
	closed      chan struct{}
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

// Start launches the agent, initializes protocol version 1, and opens a
// session. A version mismatch or a failed session/new closes the process.
// AuthRequiredError leaves it running.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Command == "" {
		return nil, errors.New("agent command is required")
	}
	if cfg.Gate == nil {
		return nil, errors.New("gate is required")
	}
	sandbox, err := makeSandbox(cfg.Resume.Cwd)
	if err != nil {
		return nil, err
	}
	s := &Session{
		cfg:         cfg,
		gate:        cfg.Gate,
		sandbox:     sandbox,
		stderr:      &ring{cap: stderrCap},
		closed:      make(chan struct{}),
		updates:     make(chan sdk.SessionNotification, 64),
		updateFlush: make(chan chan struct{}),
		terms:       map[string]*termProc{},
	}
	if !cfg.probe {
		// Without the bridge the agent still runs; it has the client's
		// files and terminals only.
		_ = s.startBridge()
	}
	if err := s.spawn(); err != nil {
		if s.bridge != nil {
			err = errors.Join(err, s.bridge.Close())
		}
		return nil, errors.Join(err, os.RemoveAll(sandbox))
	}
	if err := s.initialize(ctx); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if err := s.openSession(ctx); err != nil {
		var auth *AuthRequiredError
		if errors.As(err, &auth) {
			return s, err
		}
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func (s *Session) spawn() error {
	base := s.cfg.BaseEnv
	if base == nil {
		base = os.Environ()
	}
	cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
	cmd.Env = FilterEnv(base, s.agentEnv())
	cmd.Dir = s.sandbox
	setGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	s.stdin = stdin
	s.stderrDone = make(chan struct{})
	s.conn = sdk.NewClientSideConnection(s, stdin, stdout)
	s.conn.SetLogger(slog.New(slog.DiscardHandler))
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(s.stderrDone)
		_, _ = io.Copy(s.stderr, stderr)
	}()
	s.wg.Add(2)
	go s.pump()
	go s.watch()
	return nil
}

// An agent line over 10MB makes the SDK scanner fail and close the
// connection. The writer may then block on the unread tail; Close kills
// the process after a short wait. A notification flood that overflows the
// SDK's 1024-entry queue does the same. Prompt returns either way.
func (s *Session) watch() {
	defer s.wg.Done()
	<-s.conn.Done()
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return
	}
	// Stdout can close before the stderr copy finishes. Wait so the tail
	// includes the crash text. Close unblocks this by killing the process.
	select {
	case <-s.stderrDone:
	case <-s.closed:
		return
	}
	s.cancelPerms()
	s.killTerms()
	tail := TrimTail(s.stderr.String(), crashTail)
	text := "agent process exited"
	if tail != "" {
		text += "\n" + tail
	}
	s.emit(Update{Kind: "error", Text: text})
}

func (s *Session) pump() {
	defer s.wg.Done()
	for {
		select {
		case <-s.closed:
			return
		case n := <-s.updates:
			s.handleUpdate(n)
		case done := <-s.updateFlush:
			for range len(s.updates) {
				select {
				case n := <-s.updates:
					s.handleUpdate(n)
				default:
				}
			}
			close(done)
		}
	}
}

// The SDK queues all earlier updates before delivering a Prompt response.
// Wait for the pump to emit them before the caller can start a new turn.
func (s *Session) flushUpdates() {
	done := make(chan struct{})
	select {
	case s.updateFlush <- done:
		<-done
	case <-s.closed:
	}
}

func (s *Session) handleUpdate(n sdk.SessionNotification) {
	u, ok := decodeUpdate(n.Update)
	if !ok {
		return
	}
	if u.Kind == "mode" && SkipApproval(u.Mode) {
		s.bounceMode(u.Mode)
		return
	}
	if u.Kind == "config" {
		s.mu.Lock()
		s.options = u.Options
		s.mu.Unlock()
	}
	s.emit(u)
}

func (s *Session) bounceMode(id string) {
	s.mu.Lock()
	pinned := s.pinned
	stop := s.bounced || pinned == "" || SkipApproval(pinned)
	if !stop {
		s.bounced = true
	}
	sid := s.sid
	s.mu.Unlock()
	if stop {
		s.emit(Update{Kind: "warning", Text: "agent requested a mode that skips approval; session stopped"})
		_ = s.Stop()
		return
	}
	s.emit(Update{Kind: "warning", Text: "agent requested mode " + id + "; restored " + pinned})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.conn.SetSessionMode(ctx, sdk.SetSessionModeRequest{
		SessionId: sid,
		ModeId:    sdk.SessionModeId(pinned),
	}); err != nil {
		s.emit(Update{Kind: "error", Text: "could not restore session mode"})
		_ = s.Stop()
	}
}

func (s *Session) emit(u Update) {
	if s.cfg.OnUpdate != nil {
		s.cfg.OnUpdate(u)
	}
}

func initRequest() sdk.InitializeRequest {
	return sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientCapabilities: sdk.ClientCapabilities{
			Fs:       sdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
	}
}

func (s *Session) initialize(ctx context.Context) error {
	resp, err := s.conn.Initialize(ctx, initRequest())
	if err != nil {
		return err
	}
	if resp.ProtocolVersion != sdk.ProtocolVersionNumber {
		return fmt.Errorf("%w: agent speaks %d", ErrProtocolVersion, resp.ProtocolVersion)
	}
	s.mcpHTTP = resp.AgentCapabilities.McpCapabilities.Http
	s.canLoad = resp.AgentCapabilities.LoadSession
	return nil
}

// sandboxPrefix names the sandboxes, in the system's temporary folder.
const sandboxPrefix = "sshyard-acp-"

// makeSandbox creates the empty folder the agent runs in: want again, to
// resume a session that ran there, when it is a sandbox's name and is
// free; else a new one. An existing folder is never taken over.
func makeSandbox(want string) (string, error) {
	if want != "" && filepath.Dir(want) == filepath.Clean(os.TempDir()) && strings.HasPrefix(filepath.Base(want), sandboxPrefix) {
		if os.Mkdir(want, 0o700) == nil {
			return want, nil
		}
	}
	return os.MkdirTemp("", sandboxPrefix)
}

// openSession loads the session Config.Resume names when the agent can
// and runs in its sandbox again; failing that, or else, it opens a new
// one.
func (s *Session) openSession(ctx context.Context) error {
	if r := s.cfg.Resume; s.canLoad && !s.cfg.probe && r.ID != "" && r.Cwd == s.sandbox {
		if resp, err := s.loadSession(ctx, sdk.SessionId(r.ID)); err == nil {
			s.mu.Lock()
			s.sid = sdk.SessionId(r.ID)
			s.options = options(resp.ConfigOptions)
			// The session it loaded was already told where it works.
			s.resumed, s.introduced = true, true
			s.mu.Unlock()
			return s.applyMode(ctx, resp.Modes)
		}
	}
	resp, err := s.conn.NewSession(ctx, newSessionRequest(s.sandbox, s.cfg.Type, s.bridgeServer()))
	if err != nil {
		var re *sdk.RequestError
		if errors.As(err, &re) && re.Code == -32000 {
			return s.authRequired(re.Data)
		}
		return err
	}
	s.mu.Lock()
	s.sid = resp.SessionId
	s.options = options(resp.ConfigOptions)
	s.mu.Unlock()
	return s.applyMode(ctx, resp.Modes)
}

// loadSession asks the agent for session id, as session/new would open
// one. The agent replays its history as updates before it answers; the
// pane already shows that conversation, so SessionUpdate drops them. The
// SDK hands over every update sent before an answer before returning it.
func (s *Session) loadSession(ctx context.Context, id sdk.SessionId) (sdk.LoadSessionResponse, error) {
	n := newSessionRequest(s.sandbox, s.cfg.Type, s.bridgeServer())
	s.mu.Lock()
	s.loading = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.loading = false
		s.mu.Unlock()
	}()
	return s.conn.LoadSession(ctx, sdk.LoadSessionRequest{Meta: n.Meta, Cwd: n.Cwd, McpServers: n.McpServers, SessionId: id})
}

// ID is the session's ID, empty until one is open.
func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.sid)
}

// Resumed reports whether the session is the one Config.Resume named,
// which the agent loaded with its history.
func (s *Session) Resumed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumed
}

func (s *Session) authRequired(data any) error {
	choices, err := authChoices(data)
	if err != nil {
		return err
	}
	for _, c := range choices {
		if c.Kind != "agent" {
			return fmt.Errorf("%w: %s", ErrUnsupportedAuth, c.Kind)
		}
	}
	s.mu.Lock()
	s.methods = append([]AuthChoice(nil), choices...)
	s.mu.Unlock()
	return &AuthRequiredError{Methods: choices}
}

func authChoices(data any) ([]AuthChoice, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return nil, errors.New("authentication required")
	}
	var methods []sdk.AuthMethod
	if err := json.Unmarshal(b, &methods); err != nil || len(methods) == 0 {
		return nil, errors.New("authentication required")
	}
	out := make([]AuthChoice, 0, len(methods))
	for _, m := range methods {
		switch {
		case m.EnvVar != nil:
			out = append(out, AuthChoice{ID: m.EnvVar.Id, Name: m.EnvVar.Name, Kind: "env_var"})
		case m.Terminal != nil:
			out = append(out, AuthChoice{ID: m.Terminal.Id, Name: m.Terminal.Name, Kind: "terminal"})
		case m.Agent != nil:
			out = append(out, AuthChoice{ID: m.Agent.Id, Name: m.Agent.Name, Kind: "agent"})
		default:
			return nil, errors.New("authentication required")
		}
	}
	return out, nil
}

func (s *Session) applyMode(ctx context.Context, modes *sdk.SessionModeState) error {
	var ids []string
	current := ""
	if modes != nil {
		current = string(modes.CurrentModeId)
		for _, m := range modes.AvailableModes {
			ids = append(ids, string(m.Id))
		}
	}
	mode, set, stop := PickMode(s.cfg.Type, current, ids)
	if stop {
		return errors.New("agent mode skips approval")
	}
	s.mu.Lock()
	if set {
		s.pinned = mode
	} else {
		s.pinned = current
	}
	sid := s.sid
	s.mu.Unlock()
	if !set {
		return nil
	}
	if _, err := s.conn.SetSessionMode(ctx, sdk.SetSessionModeRequest{
		SessionId: sid,
		ModeId:    sdk.SessionModeId(mode),
	}); err != nil {
		return fmt.Errorf("set session mode %s: %w", mode, err)
	}
	return nil
}

// Authenticate logs in with an agent-type method and retries session/new
// once. A second call fails.
func (s *Session) Authenticate(ctx context.Context, methodID string) error {
	s.mu.Lock()
	if s.sid != "" {
		s.mu.Unlock()
		return errors.New("session already open")
	}
	if s.authTried {
		s.mu.Unlock()
		return errors.New("authentication already attempted")
	}
	ok := false
	for _, m := range s.methods {
		if m.ID == methodID && m.Kind == "agent" {
			ok = true
		}
	}
	s.authTried = true
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedAuth, methodID)
	}
	if _, err := s.conn.Authenticate(ctx, sdk.AuthenticateRequest{MethodId: methodID}); err != nil {
		return err
	}
	return s.openSession(ctx)
}

// Prompt sends text and embedded resources. The stop reason is the
// agent's. Cancelling ctx cancels the turn, answers pending permission
// requests, and stops running terminals.
func (s *Session) Prompt(ctx context.Context, text string, resources []Resource) (string, error) {
	s.mu.Lock()
	sid := s.sid
	s.mu.Unlock()
	if sid == "" {
		return "", errors.New("session is not open")
	}
	blocks := make([]sdk.ContentBlock, 0, 2+len(resources))
	if text != "" {
		blocks = append(blocks, sdk.TextBlock(s.outbound(text)))
	}
	for _, r := range resources {
		if r.URI == "" {
			return "", errInvalid
		}
		blocks = append(blocks, sdk.ResourceBlock(sdk.EmbeddedResourceResource{
			TextResourceContents: &sdk.TextResourceContents{Uri: s.outbound(r.URI), Text: s.outbound(r.Text)},
		}))
	}
	if len(blocks) == 0 {
		return "", errors.New("empty prompt")
	}

	t, err := s.beginTurn(ctx)
	if err != nil {
		return "", err
	}
	defer s.finishTurn(t)
	// An agent without a system prompt of SSHYard's learns where it works
	// before its first request.
	s.mu.Lock()
	introduce := !s.introduced && !ownPrompt(s.cfg.Type)
	s.introduced = true
	s.mu.Unlock()
	if introduce {
		blocks = append([]sdk.ContentBlock{sdk.ResourceBlock(sdk.EmbeddedResourceResource{
			TextResourceContents: &sdk.TextResourceContents{Uri: "sshyard://instructions", Text: Instructions},
		})}, blocks...)
	}
	resp, err := s.conn.Prompt(t.ctx, sdk.PromptRequest{SessionId: sid, Prompt: blocks})
	s.flushUpdates()
	// Inspect cancellation after the SDK call instead of racing a done channel.
	if ctx.Err() != nil || t.ctx.Err() != nil {
		cancelErr := t.ctx.Err()
		stopErr := s.stopTurn(t)
		return "", errors.Join(cancelErr, stopErr)
	}
	s.drainTurn(t)
	s.mu.Lock()
	interrupted := t.interrupted
	s.mu.Unlock()
	if ctx.Err() != nil || interrupted {
		cancelErr := ctx.Err()
		if cancelErr == nil {
			cancelErr = context.Canceled
		}
		stopErr := s.stopTurn(t)
		return "", errors.Join(cancelErr, stopErr)
	}
	if err != nil || t.cleanupErr != nil {
		return "", errors.Join(err, t.cleanupErr)
	}
	return string(resp.StopReason), nil
}

// Stop cancels the turn, answers every pending permission request with
// Cancelled, and stops running terminals. The process keeps running.
func (s *Session) Stop() error {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t == nil {
		return nil
	}
	return s.stopTurn(t)
}

// Close cancels the process, closes its stdin, waits, then kills it and
// removes the sandbox. It is safe to call more than once.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		s.closeErr = s.Stop()
		s.cancelPerms()
		s.killTerms()
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.cmd != nil {
			done := make(chan struct{})
			go func() {
				_ = s.cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				killProc(s.cmd)
				<-done
			}
		}
		s.flushUpdates()
		close(s.closed)
		s.wg.Wait()
		if s.bridge != nil {
			s.closeErr = errors.Join(s.closeErr, s.bridge.Close())
		}
		if s.sandbox != "" {
			s.closeErr = errors.Join(s.closeErr, os.RemoveAll(s.sandbox))
		}
	})
	return s.closeErr
}

// Sandbox is the empty directory the agent was started in.
func (s *Session) Sandbox() string { return s.sandbox }

// Diagnostics is the bounded stderr tail. It is never parsed as protocol.
func (s *Session) Diagnostics() string { return s.stderr.String() }

func (s *Session) checkSession(id sdk.SessionId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.sid == "" || id != s.sid {
		return errSession
	}
	return nil
}

func (s *Session) mapPath(p string) (string, error) {
	return MapPath(s.sandbox, s.cfg.RemoteHome, p, s.cfg.CwdFollow)
}

func (s *Session) trackPerm(cancel context.CancelFunc) {
	s.mu.Lock()
	s.perms = append(s.perms, cancel)
	s.mu.Unlock()
}

func (s *Session) cancelPerms() {
	s.mu.Lock()
	cancels := s.perms
	s.perms = nil
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}
