//go:build !windows

package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/ai/remote"
	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func TestHostFileToolsCancelWhileServerSilent(t *testing.T) {
	for _, name := range []string{"list_dir", "search_files", "write_file"} {
		t.Run(name, func(t *testing.T) {
			a, _, srv, host := bashHost(t)
			fake := &sshtest.FakeFS{Dirs: map[string][]sshtest.FakeEntry{"/": {{Name: "file"}}}, Files: map[string][]byte{"/file": []byte("old")}}
			srv.SFTPHandlers = fake.Handlers
			srv.SFTPConn = fake.Conn
			h := newHostTools(a, &conn{host: host})
			defer h.end()
			if err := h.connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			fake.Stop()
			defer fake.Resume()
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := h.Call(ctx, name, json.RawMessage(`{"path":"/file","query":"old","content":"new"}`))
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				fake.Resume()
				t.Fatal("tool waited for server resume")
			}
			fake.Resume()
			if _, err := h.sftp.Home(); err != nil {
				t.Fatalf("shared SFTP closed: %v", err)
			}
		})
	}
}

func TestHostToolRejectsInvalidTimeoutBeforeConnecting(t *testing.T) {
	h := &hostTools{}
	for _, args := range []string{`{"timeout":1.5}`, `{"timeout":9223372036854775808}`, `{"timeout":"1800"}`} {
		if _, _, err := h.Call(t.Context(), "run_command", json.RawMessage(args)); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

func TestHostRunCommandHonorsTimeoutSeconds(t *testing.T) {
	a, _, _, host := bashHost(t)
	h := newHostTools(a, &conn{host: host})
	defer h.end()
	start := time.Now()
	_, exit, err := h.Call(t.Context(), "run_command", json.RawMessage(`{"command":"sleep 30","timeout":1}`))
	elapsed := time.Since(start)
	if exit != -1 || !errors.Is(err, context.DeadlineExceeded) || elapsed < 4*time.Second || elapsed > 10*time.Second {
		t.Fatalf("timeout elapsed=%s exit=%d err=%v", elapsed, exit, err)
	}
}

func TestRemoteCommandUnconfirmedStopReachesAssistant(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit bool
	}{{name: "user stop"}, {name: "output limit", limit: true}} {
		t.Run(test.name, func(t *testing.T) {
			assertRemoteStopFailure(t, test.limit)
		})
	}
}

func assertRemoteStopFailure(t *testing.T, limit bool) {
	t.Helper()
	a, _, srv, host := bashHost(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	srv.Exec = func(ch ssh.Channel, line string) int {
		if strings.Contains(line, "STILL_ALIVE") {
			io.WriteString(ch, "STILL_ALIVE\n")
			return 1
		}
		io.WriteString(ch.Stderr(), "SSHYARD_PID 1234\n")
		if limit {
			io.WriteString(ch, strings.Repeat("x", 1<<20))
		} else {
			cancel()
		}
		<-release
		return 0
	}
	tools := newHostTools(a, &conn{host: host})
	defer tools.end()
	s := agent.New(agent.Config{
		Model: &replyModel{replies: []llm.Message{toolCall("cmd", "run_command", `{"command":"printf proof"}`)}},
		Gate:  policy.NewGate(policy.GateConfig{Mode: func() policy.Mode { return policy.ModeFull }}),
		Tools: tools,
	})
	var card agent.ToolCard
	err := s.Turn(ctx, "Run a command", agent.HostMeta{}, "", func(ev agent.Event) {
		if ev.Tool != nil {
			card = *ev.Tool
		}
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, remote.ErrStopUnconfirmed) || aiErrText(err) != "ai-stop-unconfirmed" {
		t.Fatalf("stop failure not preserved and mapped to a safe notice: %v (%q)", err, aiErrText(err))
	}
	if card.Phase != "done" || strings.Contains(card.Output, "1234") || limit && (!strings.HasPrefix(card.Output, "ai.command_output_limit\n") || ctx.Err() != nil) {
		t.Fatalf("incomplete or unsafe final card: phase=%s output bytes=%d context=%v", card.Phase, len(card.Output), ctx.Err())
	}
}
