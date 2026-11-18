package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/ai/remote"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sftpsvc"
)

// hostTools runs one pane's tools on the host's pooled SSH connection.
// snap runs on the UI thread before the turn goroutine starts. That
// goroutine is the only one that dials, runs commands, and closes.
type hostTools struct {
	a         *App
	c         *conn
	host      assets.Host
	run       *remote.Runner
	files     *remote.Files
	sftp      *sftpsvc.Client
	release   func()
	vers      map[string]sftpsvc.Version
	wantBegin bool
	counted   bool
	nbytes    int64
}

func newHostTools(a *App, c *conn) *hostTools {
	return &hostTools{a: a, c: c, host: c.host, vers: map[string]sftpsvc.Version{}}
}

func (h *hostTools) snap() { h.host = h.c.host }

// noteTurn starts the turn clock. It dials only when a connection is
// already up, so a chat that never calls a remote tool does not connect.
func (h *hostTools) noteTurn(ctx context.Context) {
	h.wantBegin = true
	if h.a.pool.Shared(h.a.poolKey(h.host)) > 0 {
		_ = h.connect(ctx)
	}
}

func (h *hostTools) end() {
	if h.files != nil {
		h.files.Close()
		h.files = nil
	}
	if h.sftp != nil {
		h.sftp.Close()
		h.sftp = nil
	}
	if h.release != nil {
		h.release()
		h.release = nil
	}
	h.run = nil
	h.vers = map[string]sftpsvc.Version{}
	h.wantBegin = false
}

func (h *hostTools) connect(ctx context.Context) error {
	if h.run != nil {
		if h.wantBegin {
			h.run.BeginTurn()
			h.wantBegin = false
		}
		return nil
	}
	cfg := h.a.hostConfig(h.host, h.c, func(context.Context) (session.Credentials, error) {
		return h.a.credentials(h.c, h.host)
	})
	client, release, err := h.a.pool.Acquire(ctx, h.a.poolKey(h.host), cfg)
	if err != nil {
		return err
	}
	files, err := remote.OpenFilesContext(ctx, client)
	if err != nil {
		release()
		return err
	}
	sftp, err := sftpsvc.NewContext(ctx, client)
	if err != nil {
		files.Close()
		release()
		return err
	}
	h.run = remote.New(client, remote.Options{})
	h.files = files
	h.sftp = sftp
	h.release = release
	if h.wantBegin {
		h.run.BeginTurn()
		h.wantBegin = false
	}
	return nil
}

// OutputBytes is the last command's stdout plus stderr, as counted by
// CountingWriter. A negative value means the last call was not a command.
func (h *hostTools) OutputBytes() int64 {
	if !h.counted {
		return -1
	}
	return h.nbytes
}

func (h *hostTools) ReadFile(ctx context.Context, p string) (string, bool, error) {
	if err := h.connect(ctx); err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		return "", false, err
	}
	b, ver, err := h.files.ReadContext(ctx, p, 1<<20)
	if errors.Is(err, fs.ErrNotExist) {
		delete(h.vers, p)
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	h.vers[p] = ver
	return string(b), true, nil
}

type toolArgs struct {
	Command string `json:"command"`
	Timeout int64  `json:"timeout"`
	Dir     string `json:"dir"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Query   string `json:"query"`
	Lines   int    `json:"lines"`
}

func (h *hostTools) Call(ctx context.Context, name string, raw json.RawMessage) (string, int, error) {
	h.counted = false
	var args toolArgs
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "invalid tool arguments", 1, err
		}
	}
	switch name {
	case "pane_output":
		return trimLines(h.a.aiOutput(h.c), args.Lines), 0, nil
	case "list_dir", "read_file", "search_files", "write_file", "run_command":
		if err := h.connect(ctx); err != nil {
			return toolErr(ctx, err)
		}
	default:
		return "unknown tool " + name, 1, nil
	}
	switch name {
	case "list_dir":
		return h.list(ctx, args.Path)
	case "read_file":
		text, ok, err := h.ReadFile(ctx, args.Path)
		if err != nil {
			return toolErr(ctx, err)
		}
		if !ok {
			return "no such file", 1, nil
		}
		return text, 0, nil
	case "search_files":
		return h.search(ctx, args.Path, args.Query)
	case "write_file":
		return h.write(ctx, args.Path, args.Content)
	default:
		return h.commandTimeout(ctx, args.Command, args.Dir, args.Timeout)
	}
}

func toolErr(ctx context.Context, err error) (string, int, error) {
	if ctx.Err() != nil {
		return "", -1, ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", -1, err
	}
	return err.Error(), 1, nil
}

func trimLines(text string, n int) string {
	if n <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (h *hostTools) list(ctx context.Context, dir string) (string, int, error) {
	es, err := h.sftp.ListContext(ctx, dir)
	if err != nil {
		return toolErr(ctx, err)
	}
	var b strings.Builder
	for _, e := range es {
		b.WriteString(e.Name)
		if e.IsDir {
			b.WriteByte('/')
		}
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return "(empty)", 0, nil
	}
	return b.String(), 0, nil
}

// shortcut: one directory, not a tree, until a caller needs a walk.
func (h *hostTools) search(ctx context.Context, dir, query string) (string, int, error) {
	if query == "" {
		return "empty query", 1, nil
	}
	es, err := h.sftp.ListContext(ctx, dir)
	if err != nil {
		return toolErr(ctx, err)
	}
	var b strings.Builder
	n, total := 0, 0
	const maxFiles, maxBytes = 20, 256 << 10
	for _, e := range es {
		if e.IsDir || n >= maxFiles || total >= maxBytes {
			continue
		}
		data, _, err := h.files.ReadContext(ctx, path.Join(dir, e.Name), int64(maxBytes-total))
		if err != nil {
			return toolErr(ctx, err)
		}
		total += len(data)
		if !bytes.Contains(data, []byte(query)) {
			continue
		}
		b.WriteString(path.Join(dir, e.Name))
		b.WriteByte('\n')
		n++
	}
	if b.Len() == 0 {
		return "no matches", 0, nil
	}
	return b.String(), 0, nil
}

func (h *hostTools) write(ctx context.Context, p, content string) (string, int, error) {
	was := h.vers[p]
	if _, err := h.files.WriteContext(ctx, p, []byte(content), was); err != nil {
		if errors.Is(err, sftpsvc.ErrChanged) {
			return "the file changed on the server; read it again before writing", 1, nil
		}
		return toolErr(ctx, err)
	}
	delete(h.vers, p)
	return "wrote " + p, 0, nil
}

func (h *hostTools) command(ctx context.Context, cmd, dir string) (string, int, error) {
	return h.commandTimeout(ctx, cmd, dir, 0)
}

func (h *hostTools) commandTimeout(ctx context.Context, cmd, dir string, seconds int64) (string, int, error) {
	timeout := remote.DefaultCommandTimeout
	if seconds != 0 {
		seconds = max(5, min(1800, seconds))
		timeout = time.Duration(seconds) * time.Second
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &commandOutput{remaining: (1 << 20) - 4096, cancel: cancel}
	cwOut := &remote.CountingWriter{W: output}
	cwErr := &remote.CountingWriter{W: output}
	code, err := h.run.RunWithTimeout(runCtx, cmd, dir, cwOut, cwErr, timeout)
	h.nbytes = cwOut.Bytes() + cwErr.Bytes()
	h.counted = true
	text, limited := output.result()
	if limited && (ctx.Err() == nil || errors.Is(err, remote.ErrStopUnconfirmed)) {
		return commandLimitResult(text, err)
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return text, -1, err
		}
		text = joinOut(text, err.Error())
		if code == 0 {
			code = 1
		}
		return text, code, nil
	}
	return text, code, nil
}

func commandLimitResult(text string, err error) (string, int, error) {
	text = joinOut("ai.command_output_limit", text)
	if err != nil && err != context.Canceled {
		// The outer turn can distinguish a failed stop from the expected
		// cancellation, without putting the remote diagnostic in the card.
		return text, 1, err
	}
	return text, 1, nil
}

func joinOut(a, b string) string {
	if b == "" {
		return a
	}
	if a == "" {
		return b
	}
	if strings.HasSuffix(a, "\n") {
		return a + b
	}
	return a + "\n" + b
}

// ResolvePath verifies the actual remote target before the agent authorizes it.
func (h *hostTools) ResolvePath(ctx context.Context, p string) (string, error) {
	if err := h.connect(ctx); err != nil {
		return "", err
	}
	return h.files.ResolvePath(ctx, p)
}

// Both streams share one bounded allocation; cancellation stops the producer,
// rather than continuing to buffer or silently reporting a successful result.
type commandOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	remaining int
	limited   bool
	cancel    context.CancelFunc
}

func (o *commandOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	if n > o.remaining {
		p = p[:o.remaining]
		o.limited = true
		o.cancel()
	}
	o.data.Write(p)
	o.remaining -= len(p)
	return n, nil
}
func (o *commandOutput) result() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.String(), o.limited
}
