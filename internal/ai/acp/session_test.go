package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	sdk "github.com/coder/acp-go-sdk"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sshyard-fakeagent-")
	if err != nil {
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "fakeagent")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	cmd := exec.Command("go", "build", "-mod=mod", "-o", fakeBin, "github.com/Licoy/sshyard/internal/ai/acp/internal/fakeagent")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestInitializeRequestAuthObject(t *testing.T) {
	b, err := json.Marshal(initRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"auth":{}`, `"readTextFile":true`, `"writeTextFile":true`, `"terminal":true`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("initialize JSON missing %s: %s", want, b)
		}
	}
	nb, err := json.Marshal(newSessionRequest("/work", "claude", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(nb, []byte(`"mcpServers":[]`)) || !bytes.Contains(nb, []byte(`"allowDangerouslySkipPermissions":false`)) {
		t.Fatalf("session JSON %s", nb)
	}
}

type doneRec struct {
	exit int
	n    int64
}

type fixedGate struct {
	mu        sync.Mutex
	run       bool
	refusal   string
	hold      bool
	entered   chan struct{}
	actions   []policy.Action
	diffs     []string
	dones     []doneRec
	enterOnce sync.Once
}

func (g *fixedGate) Authorize(ctx context.Context, a policy.Action, diff string) (policy.Verdict, error) {
	if g.entered != nil {
		g.enterOnce.Do(func() { close(g.entered) })
	}
	if g.hold {
		<-ctx.Done()
		return policy.Verdict{}, ctx.Err()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actions = append(g.actions, a)
	g.diffs = append(g.diffs, diff)
	v := policy.Verdict{Run: g.run, Refusal: g.refusal}
	if !g.run && v.Refusal == "" {
		v.Refusal = "refused"
	}
	return v, nil
}

func (g *fixedGate) Done(a policy.Action, v policy.Verdict, exit int, durMS int64, outBytes int64) error {
	g.mu.Lock()
	g.dones = append(g.dones, doneRec{exit: exit, n: outBytes})
	g.mu.Unlock()
	return nil
}

func (g *fixedGate) snapshot() (actions []policy.Action, diffs []string, dones []doneRec) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]policy.Action(nil), g.actions...), append([]string(nil), g.diffs...), append([]doneRec(nil), g.dones...)
}

type memFile struct {
	data []byte
	ver  sftpsvc.Version
}

type memFiles struct {
	mu           sync.Mutex
	files        map[string]memFile
	reads        int
	writes       int
	conflictOnce bool
	didConflict  bool
}

func (m *memFiles) seed(path, data string) {
	b := []byte(data)
	if m.files == nil {
		m.files = map[string]memFile{}
	}
	m.files[path] = memFile{data: b, ver: sftpsvc.Version{Size: int64(len(b)), ModTime: time.Unix(10, 0).UTC()}}
}

func (m *memFiles) Read(path string) ([]byte, sftpsvc.Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	f, ok := m.files[path]
	if !ok {
		return nil, sftpsvc.Version{}, fs.ErrNotExist
	}
	return append([]byte(nil), f.data...), f.ver, nil
}

func (m *memFiles) Write(path string, data []byte, was sftpsvc.Version) (sftpsvc.Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	cur := m.files[path]
	if m.conflictOnce && !m.didConflict {
		m.didConflict = true
		cur.data = []byte("other")
		cur.ver.Size = int64(len(cur.data))
		cur.ver.ModTime = cur.ver.ModTime.Add(time.Second)
		m.files[path] = cur
		return sftpsvc.Version{}, sftpsvc.ErrChanged
	}
	if was != (sftpsvc.Version{}) {
		if cur.ver.Size != was.Size || !cur.ver.ModTime.Equal(was.ModTime) {
			return sftpsvc.Version{}, sftpsvc.ErrChanged
		}
	}
	ver := sftpsvc.Version{Size: int64(len(data)), ModTime: time.Unix(50, 0).UTC()}
	if m.files == nil {
		m.files = map[string]memFile{}
	}
	m.files[path] = memFile{data: append([]byte(nil), data...), ver: ver}
	return ver, nil
}

func (m *memFiles) get(path string) (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.files[path]
	return string(f.data), m.writes
}

type fakeExec struct {
	mu   sync.Mutex
	cmds []string
	dirs []string
	envs [][]string
	out  string
}

func (e *fakeExec) Run(ctx context.Context, cmd, dir string, stdout, stderr io.Writer) (int, error) {
	return e.run(ctx, cmd, dir, nil, stdout)
}

func (e *fakeExec) RunEnv(ctx context.Context, cmd, dir string, env []string, stdout, stderr io.Writer) (int, error) {
	return e.run(ctx, cmd, dir, env, stdout)
}

func (e *fakeExec) run(ctx context.Context, cmd, dir string, env []string, stdout io.Writer) (int, error) {
	e.mu.Lock()
	e.cmds = append(e.cmds, cmd)
	e.dirs = append(e.dirs, dir)
	e.envs = append(e.envs, append([]string(nil), env...))
	out := e.out
	e.mu.Unlock()
	if strings.Contains(cmd, "sleep") {
		<-ctx.Done()
		return 1, ctx.Err()
	}
	if strings.Contains(cmd, "limit") {
		out = "hello world"
	}
	if out == "" {
		out = "ok\n"
	}
	_, _ = io.WriteString(stdout, out)
	return 0, nil
}

type bag struct {
	mu sync.Mutex
	up []Update
}

func (b *bag) add(u Update) {
	b.mu.Lock()
	b.up = append(b.up, u)
	b.mu.Unlock()
}

func (b *bag) texts() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var s strings.Builder
	for _, u := range b.up {
		if u.Text != "" {
			s.WriteString(u.Text)
			s.WriteByte('\n')
		}
		if u.Tool != nil {
			s.WriteString(u.Tool.Kind)
			s.WriteByte(' ')
			s.WriteString(u.Tool.Status)
			s.WriteByte('\n')
		}
	}
	return s.String()
}

func (b *bag) kinds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.up))
	for i, u := range b.up {
		out[i] = u.Kind
	}
	return out
}

func scriptFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func startAgent(t *testing.T, script string, cfg Config) (*Session, error) {
	t.Helper()
	if cfg.Command == "" {
		cfg.Command = fakeBin
		cfg.Args = []string{scriptFile(t, script)}
	}
	if cfg.Type == "" {
		cfg.Type = "custom"
	}
	if cfg.RemoteHome == "" {
		cfg.RemoteHome = "/home/me"
	}
	return Start(t.Context(), cfg)
}

func mustStart(t *testing.T, script string, cfg Config) *Session {
	t.Helper()
	s, err := startAgent(t, script, cfg)
	if err != nil || s == nil {
		if s != nil {
			s.Close()
		}
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func waitDiag(t *testing.T, s *Session, sub string) string {
	t.Helper()
	dead := time.Now().Add(3 * time.Second)
	for {
		d := s.Diagnostics()
		if strings.Contains(d, sub) || time.Now().After(dead) {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFSReadAllowedAndRefused(t *testing.T) {
	files := &memFiles{}
	files.seed("/home/me/note", "a\nb\nc\n")
	gate := &fixedGate{run: true}
	s := mustStart(t, "---\nread @sandbox/note 2 1\n", Config{Gate: gate, Files: files})
	if _, err := s.Prompt(t.Context(), "read", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "read ")
	if !strings.Contains(d, `read "b"`) {
		t.Fatalf("diag %s", d)
	}
	actions, _, dones := gate.snapshot()
	if len(actions) != 1 || actions[0].Tool != "read_file" || actions[0].Where != policy.Remote || actions[0].Path != "/home/me/note" {
		t.Fatalf("action %+v", actions)
	}
	if len(dones) != 1 || dones[0].exit != 0 {
		t.Fatalf("dones %+v", dones)
	}

	refused := &fixedGate{run: false, refusal: "no"}
	files2 := &memFiles{}
	files2.seed("/home/me/note", "secret")
	s2 := mustStart(t, "---\nread @sandbox/note\nread note.txt\n", Config{Gate: refused, Files: files2})
	if _, err := s2.Prompt(t.Context(), "read", nil); err != nil {
		t.Fatal(err)
	}
	d = waitDiag(t, s2, "invalid path")
	if !strings.Contains(d, "no") || files2.reads != 0 {
		t.Fatalf("refused diag %s reads %d", d, files2.reads)
	}
	if !strings.Contains(d, "read-err") || !strings.Contains(d, "invalid path") {
		t.Fatalf("relative path diag %s", d)
	}
	actions, _, _ = refused.snapshot()
	if len(actions) != 1 {
		t.Fatalf("relative path reached the gate: %+v", actions)
	}
}

func TestFSWriteAllowedRefusedAndConflict(t *testing.T) {
	files := &memFiles{}
	gate := &fixedGate{run: true}
	s := mustStart(t, "---\nwrite @sandbox/a hello\n", Config{Gate: gate, Files: files})
	if _, err := s.Prompt(t.Context(), "w", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "write ok")
	if !strings.Contains(d, "write ok") {
		t.Fatalf("diag %s", d)
	}
	got, _ := files.get("/home/me/a")
	if got != "hello" {
		t.Fatalf("file %q", got)
	}
	_, diffs, dones := gate.snapshot()
	if len(diffs) != 2 || diffs[0] != "" || diffs[1] != "hello" || len(dones) != 2 || dones[0].exit != 1 || dones[1].exit != 0 {
		t.Fatalf("diffs %q dones %+v", diffs, dones)
	}

	refused := &fixedGate{run: false, refusal: "no"}
	files2 := &memFiles{}
	s2 := mustStart(t, "---\nwrite @sandbox/a hello\n", Config{Gate: refused, Files: files2})
	if _, err := s2.Prompt(t.Context(), "w", nil); err != nil {
		t.Fatal(err)
	}
	d = waitDiag(t, s2, "write-err")
	if !strings.Contains(d, "no") || files2.writes != 0 {
		t.Fatalf("refused diag %s writes %d", d, files2.writes)
	}

	conflict := &memFiles{conflictOnce: true}
	conflict.seed("/home/me/a", "old\n")
	cg := &fixedGate{run: true}
	s3 := mustStart(t, "---\nwrite @sandbox/a hello\nwrite @sandbox/a hello\n", Config{Gate: cg, Files: conflict})
	if _, err := s3.Prompt(t.Context(), "w", nil); err != nil {
		t.Fatal(err)
	}
	d = waitDiag(t, s3, "write ok")
	if !strings.Contains(d, "file changed") || !strings.Contains(d, "write ok") {
		t.Fatalf("conflict diag %s", d)
	}
	got, writes := conflict.get("/home/me/a")
	if got != "hello" || writes != 2 {
		t.Fatalf("after conflict %q writes %d", got, writes)
	}
	_, diffs, dones = cg.snapshot()
	if len(diffs) != 4 || diffs[0] != "" || !strings.Contains(diffs[1], "old") || diffs[2] != "" || !strings.Contains(diffs[3], "other") {
		t.Fatalf("diffs %#v", diffs)
	}
	if len(dones) != 4 || dones[0].exit != 0 || dones[1].exit != 1 || dones[2].exit != 0 || dones[3].exit != 0 {
		t.Fatalf("dones %+v", dones)
	}
}

func TestFSWriteEmptyIsRejected(t *testing.T) {
	files := &memFiles{}
	files.seed("/home/me/a", "keep")
	s := mustStart(t, "---\nwrite-empty @sandbox/a\n", Config{Gate: &fixedGate{run: true}, Files: files})
	if _, err := s.Prompt(t.Context(), "w", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "write-empty")
	if !strings.Contains(d, "write-empty -32602") {
		t.Fatalf("diag %s", d)
	}
	got, writes := files.get("/home/me/a")
	if got != "keep" || writes != 0 {
		t.Fatalf("file %q writes %d", got, writes)
	}
}

func TestFSReadWhenNotConnected(t *testing.T) {
	gate := &fixedGate{run: true}
	s := mustStart(t, "---\nread @sandbox/note\n", Config{Gate: gate})
	if _, err := s.Prompt(t.Context(), "r", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "not connected")
	if !strings.Contains(d, "not connected") {
		t.Fatalf("diag %s", d)
	}
	_, _, dones := gate.snapshot()
	if len(dones) != 1 || dones[0].exit != 1 {
		t.Fatalf("dones %+v", dones)
	}
}

func TestTerminalLifecycleKillAndRefusal(t *testing.T) {
	ex := &fakeExec{out: "hello\n"}
	gate := &fixedGate{run: true}
	s := mustStart(t, "---\nterm echo hello\nterm echo a'b\n", Config{Gate: gate, Exec: ex})
	if _, err := s.Prompt(t.Context(), "t", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "term-after")
	if !strings.Contains(d, `term exit=0 out="hello\n" truncated=false`) || !strings.Contains(d, "unknown terminal") {
		t.Fatalf("diag %s", d)
	}
	ex.mu.Lock()
	cmds := append([]string(nil), ex.cmds...)
	dirs := append([]string(nil), ex.dirs...)
	ex.mu.Unlock()
	if len(cmds) != 2 || cmds[0] != `'echo' 'hello'` || cmds[1] != `'echo' 'a'\''b'` {
		t.Fatalf("cmds %#v", cmds)
	}
	if dirs[0] != "/home/me" {
		t.Fatalf("dir %q", dirs[0])
	}
	actions, _, dones := gate.snapshot()
	if len(actions) != 2 || actions[0].Tool != "run_command" || actions[0].Where != policy.Remote || len(dones) != 2 || dones[0].exit != 0 {
		t.Fatalf("actions %+v dones %+v", actions, dones)
	}

	ex2 := &fakeExec{}
	s2 := mustStart(t, "---\nterm-kill\n", Config{Gate: &fixedGate{run: true}, Exec: ex2})
	if _, err := s2.Prompt(t.Context(), "t", nil); err != nil {
		t.Fatal(err)
	}
	d = waitDiag(t, s2, "term-kill exit=")
	if !strings.Contains(d, "term-kill exit=-1") || !strings.Contains(d, "unknown terminal") {
		t.Fatalf("kill diag %s", d)
	}

	ex3 := &fakeExec{}
	s3 := mustStart(t, "---\nterm echo x\n", Config{Gate: &fixedGate{run: false, refusal: "no"}, Exec: ex3})
	if _, err := s3.Prompt(t.Context(), "t", nil); err != nil {
		t.Fatal(err)
	}
	d = waitDiag(t, s3, "term-err")
	if !strings.Contains(d, "no") || len(ex3.cmds) != 0 {
		t.Fatalf("refused diag %s cmds %v", d, ex3.cmds)
	}

	ex4 := &fakeExec{}
	s4 := mustStart(t, "---\nterm-env\nterm-limit\nterm-many\n", Config{Gate: &fixedGate{run: true}, Exec: ex4})
	if _, err := s4.Prompt(t.Context(), "t", nil); err != nil {
		t.Fatal(err)
	}
	d = waitDiag(t, s4, "term-many err")
	if !strings.Contains(d, `term-limit out="world" truncated=true`) || !strings.Contains(d, "too many") {
		t.Fatalf("limit/many diag %s", d)
	}
	if strings.Count(d, "term-many id ") != 2 {
		t.Fatalf("cap diag %s", d)
	}
	ex4.mu.Lock()
	var env []string
	for i, c := range ex4.cmds {
		if strings.Contains(c, "envprobe") {
			env = ex4.envs[i]
		}
	}
	ex4.mu.Unlock()
	got := strings.Join(env, "\n")
	if !strings.Contains(got, "LANG=en_US.UTF-8") || !strings.Contains(got, "LC_ALL=C") || !strings.Contains(got, "TERM=xterm") || strings.Contains(got, "SSH_AUTH_SOCK") || strings.Contains(got, "FOO") {
		t.Fatalf("term env %q", got)
	}
}

func TestAuthAgentFlowAndUnsupported(t *testing.T) {
	s, err := startAgent(t, "auth agent login Please log in\n---\nmessage hi\n", Config{Gate: &fixedGate{run: true}})
	if s != nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	var auth *AuthRequiredError
	if !errors.As(err, &auth) || s == nil || len(auth.Methods) != 1 || auth.Methods[0].ID != "login" || auth.Methods[0].Kind != "agent" {
		t.Fatalf("start err %v session %v", err, s != nil)
	}
	if err := s.Authenticate(t.Context(), "login"); err != nil {
		t.Fatal(err)
	}
	if err := s.Authenticate(t.Context(), "login"); err == nil {
		t.Fatal("second authenticate")
	}
	if _, err := s.Prompt(t.Context(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waitDiag(t, s, "authenticated login"), "authenticated login") {
		t.Fatal(s.Diagnostics())
	}

	s2, err := startAgent(t, "auth env_var key A key\n", Config{Gate: &fixedGate{run: true}})
	if s2 != nil {
		s2.Close()
		t.Fatal("unsupported auth left the session open")
	}
	if !errors.Is(err, ErrUnsupportedAuth) {
		t.Fatalf("err %v", err)
	}
}

func TestPermissionByKindAndStop(t *testing.T) {
	gate := &fixedGate{run: true}
	s := mustStart(t, "---\nperm\nperm-no-once\n", Config{Gate: gate})
	if _, err := s.Prompt(t.Context(), "p", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "rej-random")
	if !strings.Contains(d, "picked allow_once id=once-random") || !strings.Contains(d, "picked reject_once id=rej-random") || strings.Contains(d, "picked allow_always") {
		t.Fatalf("diag %s", d)
	}
	actions, _, _ := gate.snapshot()
	if len(actions) != 2 || actions[0].Where != policy.Local || actions[0].Tool != "execute" || actions[0].Command != `'ls' '-l'` || actions[0].Path != "/tmp/x" || actions[0].Reason != "because" {
		t.Fatalf("action %+v", actions)
	}

	hold := &fixedGate{run: true, hold: true, entered: make(chan struct{})}
	s2 := mustStart(t, "---\nperm-block\n", Config{Gate: hold})
	errc := make(chan error, 1)
	go func() {
		_, err := s2.Prompt(context.Background(), "p", nil)
		errc <- err
	}()
	select {
	case <-hold.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("permission did not reach the gate")
	}
	if err := s2.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not finish the turn")
	}
	if !strings.Contains(waitDiag(t, s2, "picked cancelled"), "picked cancelled") {
		t.Fatalf("diag %s", s2.Diagnostics())
	}
}

func TestUnknownUpdatesIgnoredAndSessionEvents(t *testing.T) {
	b := &bag{}
	planned := make(chan struct{}, 1)
	onUpdate := func(u Update) {
		b.add(u)
		if u.Kind == "plan" {
			select {
			case planned <- struct{}{}:
			default:
			}
		}
	}
	s := mustStart(t, "---\nbad-update\ncommands\nnote _x.ai/ping\nnote not_extension\nunknown-request\nmessage ok\nthought hmm\ntool readme\nplan ship\nstderr {not-json\n", Config{Gate: &fixedGate{run: true}, OnUpdate: onUpdate})
	reason, err := s.Prompt(t.Context(), "hi", []Resource{{URI: "file:///a", Text: "body"}})
	if err != nil || reason != "end_turn" {
		t.Fatalf("prompt %q %v", reason, err)
	}
	// Prompt and stderr run independently of the notification pump. Wait
	// for the script's last known update before inspecting all prior updates.
	select {
	case <-planned:
	case <-time.After(5 * time.Second):
		t.Fatalf("plan update not delivered; kinds %v", b.kinds())
	}
	d := waitDiag(t, s, "{not-json")
	if strings.Contains(b.texts(), "nope") || !strings.Contains(b.texts(), "ok") || !strings.Contains(b.texts(), "hmm") {
		t.Fatalf("updates %q kinds %v", b.texts(), b.kinds())
	}
	if !strings.Contains(d, "rpc -32601") || !strings.Contains(d, "{not-json") || !strings.Contains(d, "prompt resource text resource") || !strings.Contains(d, "sshyard://instructions") {
		t.Fatalf("diag %s", d)
	}
	joined := strings.Join(b.kinds(), " ")
	if !strings.Contains(joined, "tool") || !strings.Contains(joined, "plan") {
		t.Fatalf("kinds %v", b.kinds())
	}
}

func TestStderrBounded(t *testing.T) {
	s := mustStart(t, "---\nstderr BIG\nmessage x\n", Config{Gate: &fixedGate{run: true}})
	if _, err := s.Prompt(t.Context(), "s", nil); err != nil {
		t.Fatal(err)
	}
	d := waitDiag(t, s, "TAIL")
	if len(d) > stderrCap || !strings.Contains(d, "TAIL") {
		t.Fatalf("len %d", len(d))
	}
}

func TestCrashSurfacesStderrAndRemovesSandbox(t *testing.T) {
	b := &bag{}
	s := mustStart(t, "---\ncrash\n", Config{Gate: &fixedGate{run: true}, OnUpdate: b.add})
	sand := s.Sandbox()
	if _, err := s.Prompt(t.Context(), "c", nil); err == nil {
		t.Fatal("crash prompt succeeded")
	}
	dead := time.Now().Add(3 * time.Second)
	for !strings.Contains(b.texts(), "boom") && time.Now().Before(dead) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(b.texts(), "boom") || !strings.Contains(b.texts(), "agent process exited") {
		t.Fatalf("updates %q diag %s", b.texts(), s.Diagnostics())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sand); !os.IsNotExist(err) {
		t.Fatalf("sandbox %s err %v", sand, err)
	}
}

func TestOverlongLineDoesNotHang(t *testing.T) {
	s := mustStart(t, "---\nlong-line\n", Config{Gate: &fixedGate{run: true}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _ = s.Prompt(ctx, "long", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("prompt hung on an over-long line")
	}
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("close hung")
	}
}

func TestNotificationFloodDoesNotHang(t *testing.T) {
	s := mustStart(t, "---\nflood 2000\n", Config{Gate: &fixedGate{run: true}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Prompt(ctx, "f", nil); done <- err }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("prompt hung on a notification flood")
	}
}

func TestEnvStripsSSHAuthSock(t *testing.T) {
	cfg := Config{
		Gate: &fixedGate{run: true},
		BaseEnv: []string{
			"PATH=/usr/bin",
			"HOME=/home/me",
			"LANG=C",
			"TERM=xterm",
			"SSH_AUTH_SOCK=/tmp/sock",
			"SSH_AGENT_PID=9",
			"SSHYARD_X=1",
			"AWS_SECRET_ACCESS_KEY=secret",
			"DISPLAY=:0",
		},
		Env: map[string]string{"FOO": "bar", "SSH_AUTH_SOCK": "/tmp/agent.sock", "LANG": "en_US.UTF-8"},
	}
	s := mustStart(t, "", cfg)
	d := waitDiag(t, s, "sandbox-entries")
	for _, want := range []string{"env SSH_AUTH_SOCK absent", "env SSH_AGENT_PID absent", "env SSHYARD_X absent", "env AWS_SECRET_ACCESS_KEY absent", "env DISPLAY absent", "env FOO=bar", "env LANG=en_US.UTF-8", "env PATH=/usr/bin", "sandbox-entries 0"} {
		if !strings.Contains(d, want) {
			t.Fatalf("missing %q in %s", want, d)
		}
	}
	sand := s.Sandbox()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sand); !os.IsNotExist(err) {
		t.Fatalf("sandbox remains: %v", err)
	}
}

func TestCloseIsPrompt(t *testing.T) {
	s := mustStart(t, "---\nmessage hi\n", Config{Gate: &fixedGate{run: true}})
	if _, err := s.Prompt(t.Context(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("close took %s", time.Since(start))
	}
}

func TestModeAndModel(t *testing.T) {
	b := &bag{}
	s := mustStart(t, "modes default read-only bypass\n---\nmode-switch bypass\n", Config{Gate: &fixedGate{run: true}, OnUpdate: b.add})
	if _, err := s.Prompt(t.Context(), "m", nil); err != nil {
		t.Fatal(err)
	}
	dead := time.Now().Add(3 * time.Second)
	var d string
	for {
		d = s.Diagnostics()
		if strings.Count(d, "set_mode read-only") >= 2 && strings.Contains(b.texts(), "restored") || time.Now().After(dead) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Count(d, "set_mode read-only") < 2 || !strings.Contains(b.texts(), "restored") {
		t.Fatalf("diag %s updates %q", d, b.texts())
	}

	cs := mustStart(t, "modes agent read-only workspace-write\n", Config{Type: "codex", Gate: &fixedGate{run: true}})
	if !strings.Contains(waitDiag(t, cs, "set_mode read-only"), "set_mode read-only") {
		t.Fatal(cs.Diagnostics())
	}
	cl := mustStart(t, "modes default default\n", Config{Type: "claude", Gate: &fixedGate{run: true}})
	cd := waitDiag(t, cl, "\nmcp ")
	if strings.Contains(cd, "set_mode") || !strings.Contains(cd, `"allowDangerouslySkipPermissions":false`) || !strings.Contains(cd, "mcp []") {
		t.Fatalf("claude diag %s", cd)
	}

	ms := mustStart(t, "model mdl v1 v2\n", Config{Gate: &fixedGate{run: true}})
	if _, err := ms.SetOption(t.Context(), "mdl", "v2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waitDiag(t, ms, "set_config mdl=v2"), "set_config mdl=v2") {
		t.Fatal(ms.Diagnostics())
	}
}

func TestProtocolRejected(t *testing.T) {
	s, err := startAgent(t, "version 2\n", Config{Gate: &fixedGate{run: true}})
	if s != nil {
		s.Close()
		t.Fatal("bad protocol left a session")
	}
	if !errors.Is(err, ErrProtocolVersion) {
		t.Fatalf("err %v", err)
	}
}

func TestSkipApprovalModesStop(t *testing.T) {
	s, err := startAgent(t, "modes bypass auto agent\n", Config{Gate: &fixedGate{run: true}})
	if s != nil {
		s.Close()
		t.Fatal("skip-approval session stayed open")
	}
	if err == nil || !strings.Contains(err.Error(), "skips approval") {
		t.Fatalf("err %v", err)
	}
}

func TestExampleAgentPromptPermission(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "example-agent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-mod=mod", "-o", bin, "github.com/coder/acp-go-sdk/example/agent")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	b := &bag{}
	gate := &fixedGate{run: true}
	s, err := Start(t.Context(), Config{Command: bin, Type: "custom", Gate: gate, RemoteHome: "/home/me", OnUpdate: b.add})
	if err != nil {
		if s != nil {
			s.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	reason, err := s.Prompt(ctx, "hello", nil)
	if err != nil || reason != "end_turn" {
		t.Fatalf("prompt %q %v\n%s", reason, err, b.texts())
	}
	// The last chunk is queued after the prompt response is delivered.
	dead := time.Now().Add(2 * time.Second)
	for !strings.Contains(b.texts(), "successfully updated") && time.Now().Before(dead) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(b.texts(), "successfully updated") {
		t.Fatalf("updates %q", b.texts())
	}
	actions, _, _ := gate.snapshot()
	if len(actions) != 1 || actions[0].Where != policy.Local || actions[0].Tool != "edit" {
		t.Fatalf("permission action %+v", actions)
	}
}

// seenGate records the action that reached the real gate.
type seenGate struct {
	inner   policy.Gate
	mu      sync.Mutex
	actions []policy.Action
}

func (g *seenGate) Authorize(ctx context.Context, a policy.Action, diff string) (policy.Verdict, error) {
	v, err := g.inner.Authorize(ctx, a, diff)
	g.mu.Lock()
	g.actions = append(g.actions, a)
	g.mu.Unlock()
	return v, err
}

func (g *seenGate) Done(a policy.Action, v policy.Verdict, exit int, durMS int64, outBytes int64) error {
	return g.inner.Done(a, v, exit, durMS, outBytes)
}

func (g *seenGate) snapshot() []policy.Action {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]policy.Action(nil), g.actions...)
}

type rejectPrompt struct{}

func (rejectPrompt) Ask(context.Context, policy.Prompt) policy.Answer { return policy.Reject }

func TestRequestPermissionSpoofedReadIsLocalAndAsked(t *testing.T) {
	gate := &seenGate{inner: policy.NewGate(policy.GateConfig{
		Mode:     func() policy.Mode { return policy.ModeAsk },
		Prompter: rejectPrompt{},
	})}
	s := mustStart(t, "", Config{Gate: gate})
	kind := sdk.ToolKindExecute
	turn, turnErr := s.beginTurn(t.Context())
	if turnErr != nil {
		t.Fatal(turnErr)
	}
	defer s.finishTurn(turn)
	resp, err := s.RequestPermission(t.Context(), sdk.RequestPermissionRequest{
		SessionId: s.sid,
		Options: []sdk.PermissionOption{
			{Kind: sdk.PermissionOptionKindAllowOnce, OptionId: "once"},
			{Kind: sdk.PermissionOptionKindRejectOnce, OptionId: "rej"},
		},
		ToolCall: sdk.ToolCallUpdate{
			ToolCallId: "spoof",
			Kind:       &kind,
			RawInput:   map[string]any{"name": "read_file", "command": "rm -rf ~"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actions := gate.snapshot()
	if len(actions) != 1 || actions[0].Where != policy.Local || actions[0].Tool != "execute" || actions[0].Command != "rm -rf ~" || actions[0].Path != "" {
		t.Fatalf("action %+v", actions)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != "rej" {
		t.Fatalf("auto-allowed: %+v", resp.Outcome)
	}
}

// appearFile is missing on the first read and present after that.
type appearFile struct {
	mu     sync.Mutex
	reads  int
	writes int
}

func (m *appearFile) Read(string) ([]byte, sftpsvc.Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.reads == 1 {
		return nil, sftpsvc.Version{}, fs.ErrNotExist
	}
	b := []byte("planted")
	return b, sftpsvc.Version{Size: int64(len(b)), ModTime: time.Unix(1, 0).UTC()}, nil
}

func (m *appearFile) Write(string, []byte, sftpsvc.Version) (sftpsvc.Version, error) {
	m.mu.Lock()
	m.writes++
	m.mu.Unlock()
	return sftpsvc.Version{}, nil
}

func TestWriteNewFileRefusesWhenItAppears(t *testing.T) {
	files := &appearFile{}
	s := mustStart(t, "", Config{Gate: &fixedGate{run: true}, Files: files})
	turn, turnErr := s.beginTurn(t.Context())
	if turnErr != nil {
		t.Fatal(turnErr)
	}
	defer s.finishTurn(turn)
	_, err := s.WriteTextFile(t.Context(), sdk.WriteTextFileRequest{
		SessionId: s.sid,
		Path:      "/home/me/new.txt",
		Content:   "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "file appeared since the diff was made") {
		t.Fatalf("err %v", err)
	}
	files.mu.Lock()
	writes := files.writes
	files.mu.Unlock()
	if writes != 0 {
		t.Fatalf("writes %d", writes)
	}
}

func (m *memFiles) ResolvePath(_ context.Context, p string) (string, error) { return p, nil }
func (m *memFiles) ReadContext(ctx context.Context, p string, limit int64) ([]byte, sftpsvc.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, sftpsvc.Version{}, err
	}
	data, v, err := m.Read(p)
	if int64(len(data)) > limit {
		return nil, sftpsvc.Version{}, sftpsvc.ErrReadLimit
	}
	return data, v, err
}
func (m *appearFile) ResolvePath(_ context.Context, p string) (string, error) { return p, nil }
func (m *appearFile) ReadContext(ctx context.Context, p string, limit int64) ([]byte, sftpsvc.Version, error) {
	return m.Read(p)
}

func (m *memFiles) WriteContext(ctx context.Context, p string, data []byte, was sftpsvc.Version) (sftpsvc.Version, error) {
	if err := ctx.Err(); err != nil {
		return sftpsvc.Version{}, err
	}
	return m.Write(p, data, was)
}
func (m *appearFile) WriteContext(ctx context.Context, p string, data []byte, was sftpsvc.Version) (sftpsvc.Version, error) {
	if err := ctx.Err(); err != nil {
		return sftpsvc.Version{}, err
	}
	return m.Write(p, data, was)
}
