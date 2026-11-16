// Command fakeagent is a scripted ACP agent for tests.
// The script path is argv[1]. SSHYARD_* is not used: the client strips it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	sdk "github.com/coder/acp-go-sdk"
)

// rawConn reads the unexported connection. AgentSideConnection's first
// field is *Connection; tests use it to send a raw line and unknown methods.
func rawConn(asc *sdk.AgentSideConnection) *sdk.Connection {
	type head struct{ conn *sdk.Connection }
	return (*head)(unsafe.Pointer(asc)).conn
}

type authSpec struct{ kind, id, name string }

type script struct {
	version      int
	auth         []authSpec
	modeCurrent  string
	modes        []string
	modelID      string
	modelCurrent string
	modelValues  []string
	effortID     string
	effortCur    string
	effortValues []string
	mcpHTTP      bool
	authFailN    int
	authFailID   string
	// load offers session/load; it replays these messages, or fails
	// when loadFail is set.
	load     bool
	loadFail bool
	replay   []string
	steps    []string
}

func parseScript(s string) script {
	sc := script{version: 1}
	body := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "---" {
			body = true
			continue
		}
		if !body {
			f := strings.Fields(line)
			switch f[0] {
			case "version":
				sc.version, _ = strconv.Atoi(f[1])
			case "auth":
				sc.auth = append(sc.auth, authSpec{f[1], f[2], strings.Join(f[3:], " ")})
			case "modes":
				sc.modeCurrent = f[1]
				sc.modes = append([]string(nil), f[2:]...)
			case "model":
				sc.modelID, sc.modelCurrent = f[1], f[2]
				sc.modelValues = append([]string(nil), f[3:]...)
			case "effort":
				sc.effortID, sc.effortCur = f[1], f[2]
				sc.effortValues = append([]string(nil), f[3:]...)
			case "mcp-http":
				sc.mcpHTTP = true
			case "authfail":
				sc.authFailN, _ = strconv.Atoi(f[1])
				sc.authFailID = f[2]
			case "load":
				sc.load = true
				if len(f) > 1 && f[1] == "fail" {
					sc.loadFail = true
				}
			case "replay":
				sc.replay = append(sc.replay, strings.Join(f[1:], " "))
			default:
				fmt.Fprintf(os.Stderr, "bad script %q\n", line)
			}
			continue
		}
		sc.steps = append(sc.steps, line)
	}
	return sc
}

type agent struct {
	conn      *sdk.AgentSideConnection
	sc        script
	cwd       string
	sid       string
	authed    bool
	failCount int
	servers   []sdk.McpServer
}

func (a *agent) SetAgentConnection(c *sdk.AgentSideConnection) { a.conn = c }

func (a *agent) Initialize(_ context.Context, params sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	b, _ := json.Marshal(params)
	fmt.Fprintf(os.Stderr, "init %s\n", b)
	resp := sdk.InitializeResponse{ProtocolVersion: sdk.ProtocolVersion(a.sc.version)}
	resp.AgentCapabilities.McpCapabilities.Http = a.sc.mcpHTTP
	resp.AgentCapabilities.LoadSession = a.sc.load
	return resp, nil
}

// LoadSession replays the scripted history as agent messages, then
// answers as session/new does.
func (a *agent) LoadSession(ctx context.Context, params sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	mcp, _ := json.Marshal(params.McpServers)
	fmt.Fprintf(os.Stderr, "load %s cwd %s mcp %s\n", params.SessionId, params.Cwd, mcp)
	if a.sc.loadFail {
		return sdk.LoadSessionResponse{}, sdk.NewInvalidParams(map[string]any{"sessionId": params.SessionId})
	}
	a.cwd, a.sid, a.servers = params.Cwd, string(params.SessionId), params.McpServers
	for _, text := range a.sc.replay {
		if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: params.SessionId, Update: sdk.UpdateAgentMessageText(text)}); err != nil {
			return sdk.LoadSessionResponse{}, err
		}
	}
	return sdk.LoadSessionResponse{Modes: a.modeState(), ConfigOptions: a.configOptions()}, nil
}

// modeState is the scripted modes, nil when there are none.
func (a *agent) modeState() *sdk.SessionModeState {
	if len(a.sc.modes) == 0 && a.sc.modeCurrent == "" {
		return nil
	}
	var modes []sdk.SessionMode
	for _, id := range a.sc.modes {
		modes = append(modes, sdk.SessionMode{Id: sdk.SessionModeId(id), Name: id})
	}
	return &sdk.SessionModeState{AvailableModes: modes, CurrentModeId: sdk.SessionModeId(a.sc.modeCurrent)}
}

func (a *agent) Authenticate(_ context.Context, params sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	if params.MethodId == a.sc.authFailID && a.failCount < a.sc.authFailN {
		a.failCount++
		fmt.Fprintf(os.Stderr, "auth-fail %s\n", params.MethodId)
		return sdk.AuthenticateResponse{}, sdk.NewInvalidParams(map[string]any{"methodId": params.MethodId})
	}
	for _, m := range a.sc.auth {
		if m.id == params.MethodId {
			a.authed = true
			fmt.Fprintf(os.Stderr, "authenticated %s\n", params.MethodId)
			return sdk.AuthenticateResponse{}, nil
		}
	}
	return sdk.AuthenticateResponse{}, sdk.NewInvalidParams(map[string]any{"methodId": params.MethodId})
}

func (a *agent) NewSession(_ context.Context, params sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	if len(a.sc.auth) > 0 && !a.authed {
		return sdk.NewSessionResponse{}, sdk.NewAuthRequired(authMethods(a.sc.auth))
	}
	a.cwd = params.Cwd
	a.sid = "sess-fake"
	ents, _ := os.ReadDir(params.Cwd)
	fmt.Fprintf(os.Stderr, "cwd %s\n", params.Cwd)
	fmt.Fprintf(os.Stderr, "sandbox-entries %d\n", len(ents))
	meta, _ := json.Marshal(params.Meta)
	mcp, _ := json.Marshal(params.McpServers)
	fmt.Fprintf(os.Stderr, "meta %s\n", meta)
	fmt.Fprintf(os.Stderr, "mcp %s\n", mcp)
	resp := sdk.NewSessionResponse{SessionId: sdk.SessionId(a.sid), Modes: a.modeState()}
	a.servers = params.McpServers
	resp.ConfigOptions = a.configOptions()
	return resp, nil
}

// configOptions are the scripted model and effort selectors.
func (a *agent) configOptions() []sdk.SessionConfigOption {
	var out []sdk.SessionConfigOption
	add := func(id, cur string, values []string, cat sdk.SessionConfigOptionCategory) {
		if id == "" {
			return
		}
		opts := make(sdk.SessionConfigSelectOptionsUngrouped, 0, len(values))
		for _, v := range values {
			opts = append(opts, sdk.SessionConfigSelectOption{Name: strings.ToUpper(v), Value: sdk.SessionConfigValueId(v)})
		}
		out = append(out, sdk.SessionConfigOption{Select: &sdk.SessionConfigOptionSelect{
			Id: sdk.SessionConfigId(id), Name: string(cat), Category: &cat,
			CurrentValue: sdk.SessionConfigValueId(cur), Options: sdk.SessionConfigSelectOptions{Ungrouped: &opts},
		}})
	}
	add(a.sc.modelID, a.sc.modelCurrent, a.sc.modelValues, sdk.SessionConfigOptionCategoryModel)
	add(a.sc.effortID, a.sc.effortCur, a.sc.effortValues, sdk.SessionConfigOptionCategoryThoughtLevel)
	return out
}

func authMethods(in []authSpec) []sdk.AuthMethod {
	out := make([]sdk.AuthMethod, 0, len(in))
	for _, m := range in {
		switch m.kind {
		case "env_var":
			out = append(out, sdk.AuthMethod{EnvVar: &sdk.AuthMethodEnvVarInline{Id: m.id, Name: m.name, Vars: []sdk.AuthEnvVar{}}})
		case "terminal":
			out = append(out, sdk.AuthMethod{Terminal: &sdk.AuthMethodTerminalInline{Id: m.id, Name: m.name}})
		default:
			out = append(out, sdk.AuthMethod{Agent: &sdk.AuthMethodAgent{Id: m.id, Name: m.name}})
		}
	}
	return out
}

func (a *agent) Prompt(ctx context.Context, params sdk.PromptRequest) (sdk.PromptResponse, error) {
	var kinds []string
	for _, b := range params.Prompt {
		switch {
		case b.Text != nil:
			kinds = append(kinds, "text")
			fmt.Fprintf(os.Stderr, "prompt-text %q\n", b.Text.Text)
		case b.Resource != nil:
			kinds = append(kinds, "resource")
			encoded, _ := json.Marshal(b.Resource)
			fmt.Fprintf(os.Stderr, "prompt-resource %s\n", encoded)
		default:
			kinds = append(kinds, "other")
		}
	}
	fmt.Fprintf(os.Stderr, "prompt %s\n", strings.Join(kinds, " "))
	for _, step := range a.sc.steps {
		if err := a.step(ctx, step); err != nil {
			return sdk.PromptResponse{}, err
		}
	}
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

func (a *agent) step(ctx context.Context, line string) error {
	op, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch op {
	case "read":
		return a.doRead(ctx, rest)
	case "write":
		return a.doWrite(ctx, rest)
	case "write-empty":
		return a.doWriteEmpty(ctx, rest)
	case "term":
		return a.doTerm(ctx, rest)
	case "term-env":
		return a.doTermEnv(ctx)
	case "term-kill":
		return a.doTermKill(ctx)
	case "term-limit":
		return a.doTermLimit(ctx)
	case "term-many":
		return a.doTermMany(ctx)
	case "perm":
		return a.doPerm(ctx, "once")
	case "perm-block":
		return a.doPerm(context.Background(), "block")
	case "perm-no-once":
		return a.doPerm(ctx, "no-once")
	case "bad-update":
		return a.doBadUpdate(ctx)
	case "note":
		return rawConn(a.conn).SendNotification(ctx, rest, map[string]any{"x": 1})
	case "unknown-request":
		return a.doUnknown(ctx)
	case "long-line":
		return a.doLongLine(ctx)
	case "flood":
		return a.doFlood(ctx, rest)
	case "flood-mode":
		return a.doFloodMode(ctx, rest)
	case "ignore-cancel":
		return a.doIgnoreCancel(ctx)
	case "hold-stderr":
		return a.doHoldStderr()
	case "grandchild":
		return a.doGrandchild(ctx)
	case "term-raw":
		return a.doTermRaw(ctx, rest)
	case "term-store":
		return a.doTermStore(ctx)
	case "term-release-running":
		created, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{SessionId: sdk.SessionId(a.sid), Command: "sleep", Args: []string{"30"}})
		if err != nil {
			return err
		}
		_, err = a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: created.TerminalId})
		fmt.Fprintln(os.Stderr, "released-running")
		return err
	case "crash":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(2)
	case "message":
		return a.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: sdk.SessionId(a.sid), Update: sdk.UpdateAgentMessageText(rest)})
	case "thought":
		return a.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: sdk.SessionId(a.sid), Update: sdk.UpdateAgentThoughtText(rest)})
	case "plan":
		return a.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: sdk.SessionId(a.sid),
			Update: sdk.SessionUpdate{Plan: &sdk.SessionUpdatePlan{
				SessionUpdate: "plan",
				Entries:       []sdk.PlanEntry{{Content: rest, Priority: sdk.PlanEntryPriorityHigh, Status: sdk.PlanEntryStatusPending}},
			}},
		})
	case "tool":
		return a.doTool(ctx, rest)
	case "commands":
		return a.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: sdk.SessionId(a.sid),
			Update: sdk.SessionUpdate{AvailableCommandsUpdate: &sdk.SessionAvailableCommandsUpdate{
				SessionUpdate:     "available_commands_update",
				AvailableCommands: []sdk.AvailableCommand{{Name: "nope", Description: "ignored"}},
			}},
		})
	case "mode-switch":
		return a.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: sdk.SessionId(a.sid),
			Update: sdk.SessionUpdate{CurrentModeUpdate: &sdk.SessionCurrentModeUpdate{
				SessionUpdate: "current_mode_update",
				CurrentModeId: sdk.SessionModeId(rest),
			}},
		})
	case "env":
		fmt.Fprintf(os.Stderr, "env %s=%s\n", rest, os.Getenv(rest))
		return nil
	case "mcp-call":
		return a.doMCPCall(ctx, rest)
	case "stderr":
		if rest == "BIG" {
			_, _ = os.Stderr.Write([]byte(strings.Repeat("Z", 200<<10)))
			fmt.Fprintln(os.Stderr, "TAIL")
			return nil
		}
		fmt.Fprintln(os.Stderr, rest)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown step %s\n", op)
		return nil
	}
	return nil
}

func (a *agent) expand(p string) string {
	if p == "@sandbox" {
		return a.cwd
	}
	if rest, ok := strings.CutPrefix(p, "@sandbox/"); ok {
		return filepath.Join(a.cwd, filepath.FromSlash(rest))
	}
	return p
}

func (a *agent) doRead(ctx context.Context, rest string) error {
	f := strings.Fields(rest)
	path := a.expand(f[0])
	req := sdk.ReadTextFileRequest{SessionId: sdk.SessionId(a.sid), Path: path}
	if len(f) > 1 {
		n, _ := strconv.Atoi(f[1])
		req.Line = &n
	}
	if len(f) > 2 {
		n, _ := strconv.Atoi(f[2])
		req.Limit = &n
	}
	resp, err := a.conn.ReadTextFile(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read-err %v\n", err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "read %q\n", resp.Content)
	return nil
}

func (a *agent) doWrite(ctx context.Context, rest string) error {
	path, text, _ := strings.Cut(rest, " ")
	text = strings.ReplaceAll(text, `\n`, "\n")
	_, err := a.conn.WriteTextFile(ctx, sdk.WriteTextFileRequest{
		SessionId: sdk.SessionId(a.sid), Path: a.expand(path), Content: text,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "write-err %v\n", err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "write ok\n")
	return nil
}

func (a *agent) doWriteEmpty(ctx context.Context, rest string) error {
	_, err := a.conn.WriteTextFile(ctx, sdk.WriteTextFileRequest{
		SessionId: sdk.SessionId(a.sid), Path: a.expand(strings.TrimSpace(rest)), Content: "",
	})
	var re *sdk.RequestError
	if errors.As(err, &re) {
		fmt.Fprintf(os.Stderr, "write-empty %d\n", re.Code)
		return nil
	}
	fmt.Fprintf(os.Stderr, "write-empty err %v\n", err)
	return nil
}

func (a *agent) doTerm(ctx context.Context, rest string) error {
	f := strings.Fields(rest)
	resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
		SessionId: sdk.SessionId(a.sid), Command: f[0], Args: f[1:],
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-err %v\n", err)
		return nil
	}
	w, err := a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-wait-err %v\n", err)
		return nil
	}
	out, err := a.conn.TerminalOutput(ctx, sdk.TerminalOutputRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-out-err %v\n", err)
		return nil
	}
	code := -1
	if w.ExitCode != nil {
		code = *w.ExitCode
	}
	fmt.Fprintf(os.Stderr, "term exit=%d out=%q truncated=%v\n", code, out.Output, out.Truncated)
	if _, err := a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId}); err != nil {
		fmt.Fprintf(os.Stderr, "term-release-err %v\n", err)
	}
	_, err = a.conn.TerminalOutput(ctx, sdk.TerminalOutputRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	fmt.Fprintf(os.Stderr, "term-after %v\n", err)
	return nil
}

func (a *agent) doTermEnv(ctx context.Context) error {
	resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
		SessionId: sdk.SessionId(a.sid),
		Command:   "envprobe",
		Env: []sdk.EnvVariable{
			{Name: "LANG", Value: "en_US.UTF-8"},
			{Name: "LC_ALL", Value: "C"},
			{Name: "TERM", Value: "xterm"},
			{Name: "SSH_AUTH_SOCK", Value: "/tmp/agent.sock"},
			{Name: "FOO", Value: "bar"},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-env-err %v\n", err)
		return nil
	}
	_, _ = a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	_, _ = a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	fmt.Fprintf(os.Stderr, "term-env ok\n")
	return nil
}

func (a *agent) doTermKill(ctx context.Context) error {
	resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
		SessionId: sdk.SessionId(a.sid), Command: "sleep", Args: []string{"30"},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-kill-err %v\n", err)
		return nil
	}
	if _, err := a.conn.KillTerminal(ctx, sdk.KillTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId}); err != nil {
		fmt.Fprintf(os.Stderr, "term-kill-err %v\n", err)
		return nil
	}
	w, err := a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-kill-wait %v\n", err)
		return nil
	}
	code := -1
	if w.ExitCode != nil {
		code = *w.ExitCode
	}
	if _, err := a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId}); err != nil {
		fmt.Fprintf(os.Stderr, "term-kill-release %v\n", err)
		return nil
	}
	_, err = a.conn.TerminalOutput(ctx, sdk.TerminalOutputRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	fmt.Fprintf(os.Stderr, "term-kill exit=%d after=%v\n", code, err)
	return nil
}

func (a *agent) doTermLimit(ctx context.Context) error {
	n := 5
	resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
		SessionId: sdk.SessionId(a.sid), Command: "limit", OutputByteLimit: &n,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-limit-err %v\n", err)
		return nil
	}
	_, _ = a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	out, err := a.conn.TerminalOutput(ctx, sdk.TerminalOutputRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-limit-out %v\n", err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "term-limit out=%q truncated=%v\n", out.Output, out.Truncated)
	_, _ = a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	return nil
}

func (a *agent) doTermMany(ctx context.Context) error {
	for i := 0; i < 3; i++ {
		resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
			SessionId: sdk.SessionId(a.sid), Command: "sleep", Args: []string{"30"},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "term-many err %v\n", err)
			return nil
		}
		fmt.Fprintf(os.Stderr, "term-many id %s\n", resp.TerminalId)
	}
	return nil
}

func (a *agent) doPerm(ctx context.Context, mode string) error {
	opts := []sdk.PermissionOption{
		{Kind: sdk.PermissionOptionKindAllowAlways, Name: "always", OptionId: "always-zzz"},
		{Kind: sdk.PermissionOptionKindAllowOnce, Name: "once", OptionId: "once-random"},
		{Kind: sdk.PermissionOptionKindRejectOnce, Name: "reject", OptionId: "rej-random"},
	}
	if mode == "no-once" {
		opts = []sdk.PermissionOption{
			{Kind: sdk.PermissionOptionKindAllowAlways, Name: "always", OptionId: "always-zzz"},
			{Kind: sdk.PermissionOptionKindRejectOnce, Name: "reject", OptionId: "rej-random"},
		}
	}
	resp, err := a.conn.RequestPermission(ctx, sdk.RequestPermissionRequest{
		SessionId: sdk.SessionId(a.sid),
		Options:   opts,
		ToolCall: sdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Title:      sdk.Ptr("run ls"),
			Kind:       sdk.Ptr(sdk.ToolKindExecute),
			RawInput:   map[string]any{"command": "ls", "args": []any{"-l"}, "reason": "because"},
			Locations:  []sdk.ToolCallLocation{{Path: "/tmp/x"}},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "perm-err %v\n", err)
		return nil
	}
	if resp.Outcome.Cancelled != nil || resp.Outcome.Selected == nil {
		fmt.Fprintf(os.Stderr, "picked cancelled\n")
		return nil
	}
	id := string(resp.Outcome.Selected.OptionId)
	kind := "?"
	for _, o := range opts {
		if string(o.OptionId) == id {
			kind = string(o.Kind)
		}
	}
	fmt.Fprintf(os.Stderr, "picked %s id=%s\n", kind, id)
	return nil
}

func (a *agent) doBadUpdate(ctx context.Context) error {
	return rawConn(a.conn).SendNotification(ctx, sdk.ClientMethodSessionUpdate, map[string]any{
		"sessionId": a.sid,
		"update":    map[string]any{"sessionUpdate": "made_up", "title": "nope"},
	})
}

func (a *agent) doUnknown(ctx context.Context) error {
	_, err := sdk.SendRequest[json.RawMessage](rawConn(a.conn), ctx, "sshyard/nope", map[string]any{})
	var re *sdk.RequestError
	if errors.As(err, &re) {
		fmt.Fprintf(os.Stderr, "rpc %d\n", re.Code)
		return nil
	}
	fmt.Fprintf(os.Stderr, "rpc err %v\n", err)
	return nil
}

func (a *agent) doLongLine(ctx context.Context) error {
	blob := strings.Repeat("x", 11<<20)
	return rawConn(a.conn).SendNotification(ctx, "session/update", map[string]any{
		"sessionId": a.sid,
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "blob": blob},
	})
}

func (a *agent) doFloodMode(ctx context.Context, rest string) error {
	f := strings.Fields(rest)
	n := 400
	mode := "bypass"
	if len(f) > 0 {
		if v, err := strconv.Atoi(f[0]); err == nil {
			n = v
		}
	}
	if len(f) > 1 {
		mode = f[1]
	}
	for i := 0; i < n; i++ {
		if i == n/2 {
			if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
				SessionId: sdk.SessionId(a.sid),
				Update: sdk.SessionUpdate{CurrentModeUpdate: &sdk.SessionCurrentModeUpdate{
					SessionUpdate: "current_mode_update",
					CurrentModeId: sdk.SessionModeId(mode),
				}},
			}); err != nil {
				fmt.Fprintf(os.Stderr, "flood-err %v\n", err)
				return err
			}
		}
		if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: sdk.SessionId(a.sid),
			Update:    sdk.UpdateAgentMessageText("z"),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "flood-err %v\n", err)
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "flood done")
	return nil
}

func (a *agent) doIgnoreCancel(ctx context.Context) error {
	fmt.Fprintln(os.Stderr, "ready")
	<-ctx.Done()
	_ = a.doWrite(context.Background(), "/tmp/nope hi")
	_, err := a.conn.CreateTerminal(context.Background(), sdk.CreateTerminalRequest{
		SessionId: sdk.SessionId(a.sid), Command: "echo", Args: []string{"hi"},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-after-cancel %v\n", err)
	}
	_ = a.doPerm(context.Background(), "after")
	select {}
}

func (a *agent) doHoldStderr() error {
	cmd := exec.Command(os.Args[0], "--hold-stderr")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "hold-err %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "held %d\n", cmd.Process.Pid)
	os.Exit(3)
	return nil
}

func (a *agent) doGrandchild(ctx context.Context) error {
	cmd := exec.Command(os.Args[0], "--sleep")
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "child-err %v\n", err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "child %d\n", cmd.Process.Pid)
	<-ctx.Done()
	return nil
}

func (a *agent) doTermRaw(ctx context.Context, rest string) error {
	resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
		SessionId: sdk.SessionId(a.sid), Command: rest,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "term-raw-err %v\n", err)
		return nil
	}
	_, _ = a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	_, _ = a.conn.ReleaseTerminal(ctx, sdk.ReleaseTerminalRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
	fmt.Fprintln(os.Stderr, "term-raw ok")
	return nil
}

func (a *agent) doTermStore(ctx context.Context) error {
	n := 0
	for i := 0; i < 33; i++ {
		resp, err := a.conn.CreateTerminal(ctx, sdk.CreateTerminalRequest{
			SessionId: sdk.SessionId(a.sid), Command: "echo", Args: []string{"x"},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "term-store err %v count %d\n", err, n)
			return nil
		}
		_, _ = a.conn.WaitForTerminalExit(ctx, sdk.WaitForTerminalExitRequest{SessionId: sdk.SessionId(a.sid), TerminalId: resp.TerminalId})
		n++
	}
	fmt.Fprintf(os.Stderr, "term-store count %d\n", n)
	return nil
}

func (a *agent) doFlood(ctx context.Context, rest string) error {
	n := 2000
	if rest != "" {
		if v, err := strconv.Atoi(rest); err == nil {
			n = v
		}
	}
	for i := 0; i < n; i++ {
		if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: sdk.SessionId(a.sid),
			Update:    sdk.UpdateAgentMessageText("z"),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "flood-err %v\n", err)
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "flood done\n")
	return nil
}

func (a *agent) doTool(ctx context.Context, rest string) error {
	if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: sdk.SessionId(a.sid),
		Update: sdk.StartToolCall(sdk.ToolCallId("t1"), rest, sdk.WithStartKind(sdk.ToolKindRead), sdk.WithStartStatus(sdk.ToolCallStatusPending),
			sdk.WithStartLocations([]sdk.ToolCallLocation{{Path: "/tmp/x"}}), sdk.WithStartRawInput(map[string]any{"path": "/tmp/x"})),
	}); err != nil {
		return err
	}
	return a.conn.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: sdk.SessionId(a.sid),
		Update:    sdk.UpdateToolCall(sdk.ToolCallId("t1"), sdk.WithUpdateStatus(sdk.ToolCallStatusCompleted)),
	})
}

func (a *agent) Cancel(context.Context, sdk.CancelNotification) error {
	fmt.Fprintln(os.Stderr, "cancel-notification")
	return nil
}
func (a *agent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, sdk.NewMethodNotFound(sdk.AgentMethodLogout)
}
func (a *agent) CloseSession(context.Context, sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	return sdk.CloseSessionResponse{}, sdk.NewMethodNotFound(sdk.AgentMethodSessionClose)
}
func (a *agent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, sdk.NewMethodNotFound(sdk.AgentMethodSessionList)
}
func (a *agent) ResumeSession(context.Context, sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, sdk.NewMethodNotFound(sdk.AgentMethodSessionResume)
}
func (a *agent) SetSessionConfigOption(_ context.Context, params sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	if params.ValueId != nil {
		fmt.Fprintf(os.Stderr, "set_config %s=%s\n", params.ValueId.ConfigId, params.ValueId.Value)
		switch string(params.ValueId.ConfigId) {
		case a.sc.modelID:
			a.sc.modelCurrent = string(params.ValueId.Value)
		case a.sc.effortID:
			a.sc.effortCur = string(params.ValueId.Value)
		}
	}
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: a.configOptions()}, nil
}
func (a *agent) SetSessionMode(_ context.Context, params sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	fmt.Fprintf(os.Stderr, "set_mode %s\n", params.ModeId)
	return sdk.SetSessionModeResponse{}, nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--sleep", "--hold-stderr":
			time.Sleep(time.Hour)
			return
		case "--tree":
			cmd := exec.Command(os.Args[0], "--sleep")
			if err := cmd.Start(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println(cmd.Process.Pid)
			select {}
		}
	}
	for _, k := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID", "SSHYARD_X", "FOO", "PATH", "AWS_SECRET_ACCESS_KEY", "LANG", "HOME", "DISPLAY", "TERM"} {
		if v, ok := os.LookupEnv(k); ok {
			fmt.Fprintf(os.Stderr, "env %s=%s\n", k, v)
		} else {
			fmt.Fprintf(os.Stderr, "env %s absent\n", k)
		}
	}
	var text string
	if len(os.Args) > 1 {
		b, err := os.ReadFile(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		text = string(b)
	}
	ag := &agent{sc: parseScript(text)}
	asc := sdk.NewAgentSideConnection(ag, os.Stdout, os.Stdin)
	asc.SetLogger(slog.New(slog.DiscardHandler))
	ag.SetAgentConnection(asc)
	<-asc.Done()
}

// doMCPCall calls a tool of the HTTP MCP server given in session/new:
// "mcp-call <tool> <json arguments>". The reply is logged as it came.
func (a *agent) doMCPCall(ctx context.Context, rest string) error {
	tool, args, _ := strings.Cut(rest, " ")
	args = strings.ReplaceAll(args, "@sandbox", a.cwd)
	for _, srv := range a.servers {
		if srv.Http == nil {
			continue
		}
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + strconv.Quote(tool) + `,"arguments":` + args + `}}`
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.Http.Url, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for _, h := range srv.Http.Headers {
			req.Header.Set(h.Name, h.Value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp-err %v\n", err)
			return nil
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Fprintf(os.Stderr, "mcp-result %s\n", strings.TrimSpace(string(out)))
		return nil
	}
	fmt.Fprintln(os.Stderr, "mcp-err no server")
	return nil
}
