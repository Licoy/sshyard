package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/ai/acp"
	"github.com/Licoy/sshyard/internal/ai/acp/detect"
	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/ai/remote"
	"github.com/Licoy/sshyard/internal/session"
)

// acpRuntime owns only this pane's process, SFTP subsystem and pool reference
// Start and Close run off the UI thread; protocol callbacks never read UI state
// The ACP library binds all agent requests to the active Prompt context
type acpRuntime struct {
	mu      sync.Mutex
	session *acp.Session
	cwd     string
	cancel  context.CancelFunc
	ready   chan struct{}
	once    sync.Once
	files   *remote.Files
	runner  *remote.Runner
	release func()
	done    func()
	report  func(error)
	// gone is closed once close has stopped the agent and removed its
	// sandbox; nil when nothing waits for it.
	gone chan struct{}
}

type acpFailure struct {
	stage string
	cause error
}

func (e *acpFailure) Error() string { return "acp-" + e.stage }
func (e *acpFailure) Unwrap() error { return e.cause }

func (r *acpRuntime) follow() string { r.mu.Lock(); defer r.mu.Unlock(); return r.cwd }
func (r *acpRuntime) close() {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.once.Do(func() {
		go func() {
			defer r.done()
			if r.gone != nil {
				defer close(r.gone)
			}
			<-r.ready
			release := func() {
				if r.release != nil {
					r.release()
				}
			}
			cleaned := make(chan error, 1)
			go func() {
				var cleanup error
				if r.session != nil {
					cleanup = errors.Join(r.session.Stop(), r.session.Close())
				}
				if r.files != nil {
					cleanup = errors.Join(cleanup, r.files.Close())
				}
				cleaned <- cleanup
			}()
			var cleanup error
			select {
			case cleanup = <-cleaned:
			case <-time.After(acpCloseGrace):
				// A host that stopped answering keeps the SFTP session and
				// the agent's requests waiting for replies that never come;
				// letting go of the connection ends them.
				release()
				cleanup = <-cleaned
			}
			release()
			if cleanup != nil && r.report != nil {
				r.report(cleanup)
			}
		}()
	})
}

// acpCloseGrace is how long closing waits for the agent and the SFTP
// session before letting go of the connection under them.
var acpCloseGrace = 3 * time.Second

// acpQuitWait bounds how long quitting waits for the agents to stop, so
// that one that never does cannot keep the app open.
var acpQuitWait = 10 * time.Second

func (a *App) waitACP() {
	done := make(chan struct{})
	go func() {
		a.acpWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(acpQuitWait):
	}
}

func (r *acpRuntime) start(ctx context.Context, a *App, cfg session.Config, key, agent string, options acp.Config) error {
	defer close(r.ready)
	locateACP(ctx, agent, &options)
	client, release, err := a.pool.Acquire(ctx, key, cfg)
	if err != nil {
		return &acpFailure{"host", err}
	}
	r.release = release
	r.files, err = remote.OpenFilesContext(ctx, client)
	if err != nil {
		return &acpFailure{"files", err}
	}
	home, err := r.files.ResolvePath(ctx, ".")
	if err != nil {
		return &acpFailure{"files", err}
	}
	r.runner = remote.New(client, remote.Options{})
	options.Files = r.files
	options.Exec = r.runner
	options.RemoteHome = home
	options.CwdFollow = r.follow
	s, err := acp.Start(ctx, options)
	r.session = s
	if err != nil {
		var auth *acp.AuthRequiredError
		if errors.As(err, &auth) || errors.Is(err, acp.ErrUnsupportedAuth) {
			return &acpFailure{"auth", err}
		}
		if errors.Is(err, acp.ErrProtocolVersion) {
			return &acpFailure{"protocol", err}
		}
		var executable *exec.Error
		var path *os.PathError
		if errors.As(err, &executable) || errors.As(err, &path) {
			return &acpFailure{"start", err}
		}
		return &acpFailure{"protocol", err}
	}
	return ctx.Err()
}

// locateACP gives the agent the login shell's search path, proxy and its
// own variables, which an app started from the desktop lacks; finds a
// detected agent again when its executable moved; and looks a bare
// command up on that search path.
func locateACP(ctx context.Context, agent string, options *acp.Config) {
	sh := detect.LoginShell(ctx, false)
	options.Env = detect.Vars(agent, sh, os.Environ())
	if agent != "" {
		if _, err := os.Stat(options.Command); err != nil {
			if found, ok := detect.Find(ctx, agent); ok {
				options.Command, options.Args = found.Command, found.Args
			}
		}
	}
	if !filepath.IsAbs(options.Command) && !strings.ContainsAny(options.Command, `/\`) {
		if bin := detect.LookPath(sh.Path, options.Command); bin != "" {
			options.Command = bin
		}
	}
}

func (a *App) sendACP(c *conn, text string, files []*aiAttach) {
	acpCfg, ok := a.settings.ACP(c.ai.agent)
	if !ok || strings.TrimSpace(acpCfg.Command) == "" {
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: "acp-start"})
		return
	}
	a.refreshMode(c)
	ctx, cancel := context.WithCancel(context.Background())
	r := c.ai.acp
	fresh := r == nil
	// A new process loads the session that carried the conversation on,
	// once the last one has let go of its sandbox; if it cannot, it is
	// told the conversation again, once.
	var resume acp.Resume
	var prev *acpRuntime
	earlier := ""
	backend := c.ai.agent
	if fresh {
		if c.ai.acpSID != "" && c.ai.acpAgent == backend {
			resume = acp.Resume{ID: c.ai.acpSID, Cwd: c.ai.acpCwd}
		}
		prev = c.ai.acpLast
		earlier = acpTranscript(c.ai.items, replayMax)
		a.acpWG.Add(1)
		r = &acpRuntime{ready: make(chan struct{}), done: a.acpWG.Done, gone: make(chan struct{})}
		c.ai.acpLast = r
		r.report = func(err error) {
			a.post(func() {
				if current := a.conns[c.id]; !a.closing && current != nil && current.ai != nil {
					current.ai.items = append(current.ai.items, aiItem{kind: "err", text: "acp-close"})
				}
			})
		}
		c.ai.acp = r
		c.ai.acpOpts = nil
	}
	r.mu.Lock()
	r.cwd = a.aiMeta(c).Cwd
	r.cancel = cancel
	r.mu.Unlock()
	var gate policy.Gate
	if fresh {
		gate = a.aiGate(c)
	}
	agentID := acpCfg.Agent
	options := acp.Config{Command: acpCfg.Command, Args: append([]string(nil), acpCfg.Args...), Type: detect.Type(agentID), Gate: gate, Redact: a.settings.AIRedact, Resume: resume}
	prefer := maps.Clone(acpCfg.Options)
	id := c.id
	c.ai.turn++
	turn := c.ai.turn
	options.OnUpdate = func(update acp.Update) {
		a.post(func() {
			current := a.conns[id]
			if current != nil && current.ai != nil && current.ai.acp == r {
				a.onACPUpdate(current, update)
			}
		})
	}
	host := c.host
	cfg := a.hostConfig(host, c, func(context.Context) (session.Credentials, error) { return a.credentials(c, host) })
	key := a.poolKey(host)
	meta := a.aiMeta(c)
	resources := []acp.Resource{{URI: "sshyard://host", Text: fmt.Sprintf("Host: %s\nAddress: %s\nUser: %s\nWorking directory: %s", meta.Name, meta.Address, meta.User, meta.Cwd)}}
	if terminal := a.aiTerminal(c); terminal != "" {
		resources = append(resources, acp.Resource{URI: "sshyard://terminal", Text: agent.WrapUntrusted("terminal", terminal)})
	}
	for i, f := range files {
		if f.text() {
			uri := "sshyard://attachment/" + strconv.Itoa(i+1) + "/" + url.PathEscape(f.name)
			resources = append(resources, acp.Resource{URI: uri, Text: agent.WrapUntrusted("file "+f.name, string(f.data))})
		}
	}
	c.ai.items = append(c.ai.items, aiItem{kind: "user", text: text, files: files})
	c.ai.input = ""
	c.ai.attach = nil
	c.ai.task = text
	c.ai.gen++
	c.ai.running = true
	c.ai.paused = false
	c.ai.cancel = cancel
	c.ai.dirty = false
	a.aiRecent[host.ID] = id
	go func() {
		defer cancel()
		var err error
		if fresh {
			if resume.ID != "" && prev != nil && prev.gone != nil {
				select {
				case <-prev.gone:
				case <-ctx.Done():
				case <-time.After(acpCloseGrace + 2*time.Second):
				}
			}
			err = r.start(ctx, a, cfg, key, agentID, options)
			if err == nil && earlier != "" && !r.session.Resumed() {
				resources = append([]acp.Resource{{URI: "sshyard://conversation", Text: earlier}}, resources...)
			}
			if err == nil {
				// A choice that fails to apply leaves the agent's own value.
				_ = r.session.Prefer(ctx, prefer)
				opts := r.session.Options()
				a.post(func() {
					if current := a.conns[id]; current != nil && current.ai != nil && current.ai.acp == r {
						a.setACPOptions(current, opts)
					}
				})
			}
		}
		if err == nil {
			r.runner.BeginTurn()
			var reason string
			reason, err = r.session.Prompt(ctx, text, resources)
			if err == nil && reason != "end_turn" {
				err = &acpFailure{"incomplete", errors.New("agent did not finish the turn")}
			}
			if err != nil && ctx.Err() == nil {
				var failure *acpFailure
				if !errors.As(err, &failure) {
					err = &acpFailure{"session", err}
				}
			}
		}
		if ctx.Err() != nil {
			if err == nil || onlyAICancellation(err) {
				err = ctx.Err()
			} else {
				err = &acpFailure{"close", err}
			}
		}
		sid, sandbox := "", ""
		if r.session != nil {
			sid, sandbox = r.session.ID(), r.session.Sandbox()
		}
		if err != nil {
			r.close()
		}
		a.post(func() {
			current := a.conns[id]
			if current == nil || current.ai == nil || current.ai.turn != turn {
				return
			}
			if err != nil && current.ai.acp == r {
				current.ai.acp = nil
			}
			if sid != "" {
				current.ai.acpSID, current.ai.acpAgent, current.ai.acpCwd = sid, backend, sandbox
			}
			// The built-in agent's messages no longer hold all of it.
			current.ai.msgs = nil
			a.onAIDone(id, err)
		})
	}()
}

func (a *App) onACPUpdate(c *conn, u acp.Update) {
	switch u.Kind {
	case "message":
		a.onAIEvent(c.id, agent.Event{Kind: agent.EventText, Text: u.Text})
	case "thought":
		a.onAIEvent(c.id, agent.Event{Kind: agent.EventThinking, Text: u.Text})
	case "plan":
		for _, line := range u.Plan {
			c.ai.items = append(c.ai.items, aiItem{kind: "think", text: line.Content})
		}
	case "tool":
		if u.Tool == nil {
			return
		}
		v := u.Tool
		card := agent.ToolCard{ID: v.ID, Name: v.Title, Category: v.Kind, Phase: "open", Exit: -1, Where: string(policy.Remote)}
		if v.Local() {
			card.Where = string(policy.Local)
		}
		if v.Status == "completed" || v.Status == "failed" {
			card.Phase = "done"
			if v.Status == "completed" {
				card.Exit = 0
			} else {
				card.Exit = 1
			}
		}
		if len(v.Paths) > 0 {
			card.Path = v.Paths[0]
		}
		card.Diff = v.Diff
		// Tool updates omit unchanged fields; keep the initial title/path/diff
		for _, item := range c.ai.items {
			if item.kind == "tool" && item.tool.ID == card.ID {
				if card.Name == "" {
					card.Name = item.tool.Name
				}
				if card.Path == "" {
					card.Path = item.tool.Path
				}
				if card.Diff == "" {
					card.Diff = item.tool.Diff
				}
				if v.Kind == "" {
					card.Category, card.Where = item.tool.Category, item.tool.Where
				}
			}
		}
		a.onAIEvent(c.id, agent.Event{Kind: agent.EventTool, Tool: &card})
	case "config":
		a.setACPOptions(c, u.Options)
	case "warning":
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: "acp-warning"})
	case "notice":
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: u.Text})
	case "error":
		a.StopAI(c.id)
		c.ai.items = append(c.ai.items, aiItem{kind: "err", text: "acp-session"})
	}
}

func (a *App) setAIAgent(c *conn, backend string) {
	if c.ai.agent == backend {
		return
	}
	a.invalidateAI(c)
	c.ai.turn++
	c.ai.running = false
	c.ai.cancel = nil
	c.ai.agent = backend
	c.ai.sess = nil
	c.ai.box = nil
	c.ai.tools = nil
	c.ai.paused = false
	a.saveLayout()
}

// A cancellation can also carry a failed remote cleanup; keep that failure visible
func onlyAICancellation(err error) bool {
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range many.Unwrap() {
			if !onlyAICancellation(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return onlyAICancellation(cause)
	}
	return err == context.Canceled || err == context.DeadlineExceeded
}
