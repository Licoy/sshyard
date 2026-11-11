package acp

import (
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

func TestFilterEnvStripsSecretsEvenWhenConfigured(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"LANG=C",
		"LC_ALL=C",
		"TERM=xterm",
		"SSH_AUTH_SOCK=/tmp/sock",
		"SSH_AGENT_PID=9",
		"SSHYARD_X=1",
		"AWS_SECRET_ACCESS_KEY=secret",
		"DISPLAY=:0",
		"FOO=frombase",
		"PATH=/second",
	}
	extra := map[string]string{
		"FOO":           "bar",
		"SSH_AUTH_SOCK": "/tmp/agent.sock",
		"LANG":          "en_US.UTF-8",
		"SSHYARD_Y":     "no",
		"CUSTOM":        "yes",
	}
	got := strings.Join(FilterEnv(base, extra), "\n")
	for _, banned := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID", "SSHYARD_", "AWS_SECRET", "DISPLAY", "frombase", "/second"} {
		if strings.Contains(got, banned) {
			t.Fatalf("env contains %q:\n%s", banned, got)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/me", "LC_ALL=C", "TERM=xterm", "FOO=bar", "LANG=en_US.UTF-8", "CUSTOM=yes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("env missing %q:\n%s", want, got)
		}
	}
}

func TestShellQuoteMetacharacters(t *testing.T) {
	got := QuoteCommand("echo", []string{"a'b", "c d", "$HOME"})
	want := `'echo' 'a'\''b' 'c d' '$HOME'`
	if got != want {
		t.Fatalf("quoted %q, want %q", got, want)
	}
}

func TestMapPathSandboxAndRemote(t *testing.T) {
	sandbox := t.TempDir()
	inside := filepath.Join(sandbox, "note")
	got, err := MapPath(sandbox, "/home/me", inside, nil)
	if err != nil || got != "/home/me/note" {
		t.Fatalf("sandbox path %q err %v", got, err)
	}
	got, err = MapPath(sandbox, "/home/me", "/var/log/syslog", nil)
	if err != nil || got != "/var/log/syslog" {
		t.Fatalf("posix path %q err %v", got, err)
	}
	got, err = MapPath(sandbox, "/home/me", inside, func() string { return "/srv/work" })
	if err != nil || got != "/srv/work/note" {
		t.Fatalf("follow %q err %v", got, err)
	}
	got, err = MapPath(sandbox, "/home/me", inside, func() string { return "relative" })
	if err != nil || got != "/home/me/note" {
		t.Fatalf("bad follow should use home, got %q err %v", got, err)
	}
	for _, bad := range []string{"", "note", "C:\\Windows\\system32", "has\x00nul", "/" + strings.Repeat("a", maxPathLen)} {
		if _, err := MapPath(sandbox, "/home/me", bad, nil); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := MapPath(sandbox, "", inside, nil); err == nil {
		t.Fatal("sandbox path without a remote directory")
	}
}

func TestPickModeConservative(t *testing.T) {
	if m, set, stop := PickMode("codex", "agent", nil); m != "read-only" || !set || stop {
		t.Fatalf("codex %q set %v stop %v", m, set, stop)
	}
	if _, set, stop := PickMode("claude", "default", []string{"bypass"}); set || stop {
		t.Fatalf("claude set %v stop %v", set, stop)
	}
	if _, set, stop := PickMode("grok", "", nil); set || stop {
		t.Fatalf("grok set %v stop %v", set, stop)
	}
	m, set, stop := PickMode("custom", "bypass", []string{"full-access", "my-agent", "read-only", "plan"})
	if m != "read-only" || !set || stop {
		t.Fatalf("custom %q set %v stop %v", m, set, stop)
	}
	if _, set, stop := PickMode("custom", "read-only", []string{"read-only", "bypass"}); set || stop {
		t.Fatal("already on the conservative mode")
	}
	if _, _, stop := PickMode("custom", "bypassPermissions", []string{"auto", "agent"}); !stop {
		t.Fatal("only skip-approval modes must stop")
	}
	if _, _, stop := PickMode("custom", "default", []string{"auto"}); stop {
		t.Fatal("a non-skip current mode is kept when nothing safer is offered")
	}
	if SkipApproval("") || SkipApproval("workspace-write") || !SkipApproval("accept_edits") {
		t.Fatal("skip-approval matcher")
	}
}

// GitHub Copilot CLI names its modes with URIs whose host contains "agent".
func TestPickModeURIModeIDs(t *testing.T) {
	const base = "https://agentclientprotocol.com/protocol/session-modes#"
	agent, plan, autopilot := base+"agent", base+"plan", base+"autopilot"
	if SkipApproval(plan) || !SkipApproval(agent) || !SkipApproval(autopilot) {
		t.Fatal("a URI mode id must be judged by its fragment")
	}
	m, set, stop := PickMode("custom", agent, []string{agent, plan, autopilot})
	if m != plan || !set || stop {
		t.Fatalf("copilot %q set %v stop %v", m, set, stop)
	}
	if !SkipApproval("https://agentclientprotocol.com/protocol/session-modes") {
		t.Fatal("a URI without a fragment is matched as a whole")
	}
}

func TestTrimTailUTF8(t *testing.T) {
	if got := TrimTail("éhello", 6); got != "hello" {
		t.Fatalf("tail %q", got)
	}
	if got := TrimTail("abc", 0); got != "" {
		t.Fatalf("zero limit %q", got)
	}
	if got := TrimTail("abc", 10); got != "abc" {
		t.Fatalf("short %q", got)
	}
}

func TestPermissionChoiceByKind(t *testing.T) {
	opts := []sdk.PermissionOption{
		{Kind: sdk.PermissionOptionKindAllowAlways, OptionId: "always-zzz"},
		{Kind: sdk.PermissionOptionKindAllowOnce, OptionId: "once-random"},
		{Kind: sdk.PermissionOptionKindRejectOnce, OptionId: "rej-random"},
	}
	id, cancelled := PermissionChoice(true, opts)
	if cancelled || id != "once-random" {
		t.Fatalf("allow id %q cancelled %v", id, cancelled)
	}
	noOnce := []sdk.PermissionOption{
		{Kind: sdk.PermissionOptionKindAllowAlways, OptionId: "always-zzz"},
		{Kind: sdk.PermissionOptionKindRejectOnce, OptionId: "rej-random"},
	}
	id, cancelled = PermissionChoice(true, noOnce)
	if cancelled || id != "rej-random" {
		t.Fatalf("missing allow_once id %q cancelled %v", id, cancelled)
	}
	id, cancelled = PermissionChoice(false, opts)
	if cancelled || id != "rej-random" {
		t.Fatalf("reject id %q cancelled %v", id, cancelled)
	}
	onlyAlways := []sdk.PermissionOption{{Kind: sdk.PermissionOptionKindAllowAlways, OptionId: "always-zzz"}}
	if _, cancelled = PermissionChoice(false, onlyAlways); !cancelled {
		t.Fatal("allow_always alone must cancel")
	}
}

func TestSliceLines(t *testing.T) {
	line, limit := 2, 1
	got, err := sliceLines("a\nb\nc\n", &line, &limit)
	if err != nil || got != "b" {
		t.Fatalf("slice %q err %v", got, err)
	}
	bad := 0
	if _, err := sliceLines("a", &bad, nil); err == nil {
		t.Fatal("line 0")
	}
}
