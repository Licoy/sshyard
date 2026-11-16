package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadSessionResumes checks that an agent offering session/load is
// asked for the earlier session in the sandbox it ran in, that the
// history it replays is not passed on, and that it is not introduced
// again.
func TestLoadSessionResumes(t *testing.T) {
	script := "load\nreplay old answer\n---\nmessage fresh answer\n"
	first := mustStart(t, script, Config{Gate: &fixedGate{run: true}})
	if first.Resumed() {
		t.Fatal("a session that named none resumed")
	}
	if _, err := first.Prompt(t.Context(), "q1", nil); err != nil {
		t.Fatal(err)
	}
	if d := waitDiag(t, first, "sshyard://instructions"); !strings.Contains(d, "sshyard://instructions") {
		t.Fatalf("first session not introduced: %s", d)
	}
	id, cwd := first.ID(), first.Sandbox()
	if id == "" {
		t.Fatal("no session ID")
	}
	first.Close()

	b := &bag{}
	s := mustStart(t, script, Config{Gate: &fixedGate{run: true}, OnUpdate: b.add, Resume: Resume{ID: id, Cwd: cwd}})
	if !s.Resumed() || s.ID() != id || s.Sandbox() != cwd {
		t.Fatalf("resumed %v id %q sandbox %q, want %q in %q", s.Resumed(), s.ID(), s.Sandbox(), id, cwd)
	}
	d := waitDiag(t, s, "load ")
	if !strings.Contains(d, "load "+id+" cwd "+cwd) || strings.Contains(d, "sandbox-entries") {
		t.Fatalf("not loaded: %s", d)
	}
	if _, err := s.Prompt(t.Context(), "q2", nil); err != nil {
		t.Fatal(err)
	}
	dead := time.Now().Add(3 * time.Second)
	for !strings.Contains(b.texts(), "fresh answer") && time.Now().Before(dead) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.texts(); !strings.Contains(got, "fresh answer") || strings.Contains(got, "old answer") {
		t.Fatalf("updates %q", got)
	}
	if d := waitDiag(t, s, "prompt "); strings.Contains(d, "sshyard://instructions") {
		t.Fatalf("a loaded session was introduced again: %s", d)
	}
	s.Close()
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatal("the reused sandbox was kept")
	}
}

// TestLoadSessionFallsBack checks that a new session is opened when the
// agent cannot load the one named, does not offer to, or its sandbox is
// taken or not a sandbox.
func TestLoadSessionFallsBack(t *testing.T) {
	gone := func() string {
		t.Helper()
		dir, err := os.MkdirTemp("", sandboxPrefix)
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(dir)
		return dir
	}
	busy, err := os.MkdirTemp("", sandboxPrefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(busy) })
	elsewhere := filepath.Join(t.TempDir(), sandboxPrefix+"x")
	for _, tc := range []struct {
		name, script, cwd string
		asked             bool
	}{
		{"load fails", "load fail\n", gone(), true},
		{"not offered", "", gone(), false},
		{"sandbox taken", "load\n", busy, false},
		{"not a sandbox", "load\n", elsewhere, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := mustStart(t, tc.script, Config{Gate: &fixedGate{run: true}, Resume: Resume{ID: "earlier", Cwd: tc.cwd}})
			d := waitDiag(t, s, "sandbox-entries")
			if s.Resumed() || s.ID() != "sess-fake" || !strings.Contains(d, "sandbox-entries 0") {
				t.Fatalf("resumed %v id %q: %s", s.Resumed(), s.ID(), d)
			}
			if asked := strings.Contains(d, "load earlier"); asked != tc.asked {
				t.Fatalf("load asked %v: %s", asked, d)
			}
			if s.Sandbox() == busy || s.Sandbox() == elsewhere {
				t.Fatalf("ran in %s", tc.cwd)
			}
			if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
				t.Fatal("a folder outside the temporary folder was created")
			}
		})
	}
}
