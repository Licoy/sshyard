package history

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/keychain"
)

func store(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	k, err := keychain.Open(keychain.OpenOptions{Path: filepath.Join(dir, "secrets.enc"), LockPath: filepath.Join(dir, "secrets.lock"), Scope: "test.sshyard", System: keychain.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	root := filepath.Join(dir, "ai-history")
	return New(root, k), root
}

func conv(host, title string, at time.Time) Conversation {
	return Conversation{ID: NewID(), Host: host, Title: title, Model: "gpt", Created: at, Updated: at,
		Items:    []Item{{Kind: "user", Text: "password is hunter2", Files: []File{{Name: "a.png", MIME: "image/png"}}}, {Kind: "tool", Tool: &agent.ToolCard{Name: "run_command", Command: "ls"}}},
		Messages: []llm.Message{{Role: "user", Content: "password is hunter2"}}}
}

func TestSaveLoadListDelete(t *testing.T) {
	st, root := store(t)
	now := time.Now().Round(0)
	a := conv("h1", "first", now)
	b := conv("h1", "second", now.Add(time.Minute))
	other := conv("h2", "elsewhere", now)
	for _, c := range []Conversation{a, b, other} {
		if err := st.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.List("h1")
	if err != nil || len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// Saving again moves a conversation to the top.
	a.Title = "first again"
	if err := st.Save(a); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.List("h1"); list[0].ID != a.ID || list[0].Title != "first again" || len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	got, err := st.Load("h1", a.ID)
	if err != nil || got.Title != "first again" || len(got.Items) != 2 || got.Items[1].Tool.Command != "ls" || got.Messages[0].Content != "password is hunter2" {
		t.Fatalf("load = %+v, %v", got, err)
	}
	// Another host's ID, a bad ID and a missing one are not found.
	for _, id := range []string{other.ID, "../x", "abc"} {
		if _, err := st.Load("h1", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("load %q: %v", id, err)
		}
	}
	// Nothing on disk is readable.
	_ = filepath.Walk(root, func(p string, info os.FileInfo, _ error) error {
		if info.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("hunter2")) || bytes.Contains(b, []byte("first")) {
			t.Errorf("%s is plain text", p)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // Windows has no Unix modes
			t.Errorf("%s mode %v", p, info.Mode())
		}
		return nil
	})
	if err := st.Delete("h1", a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.List("h1"); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("after delete = %+v", list)
	}
	if _, err := st.Load("h1", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted loads: %v", err)
	}
	if err := st.DeleteHost("h1"); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.List("h1"); len(list) != 0 {
		t.Fatalf("host kept %+v", list)
	}
	if list, _ = st.List("h2"); len(list) != 1 {
		t.Fatalf("other host lost: %+v", list)
	}
}

func TestFilesAreBoundToTheirPlace(t *testing.T) {
	st, _ := store(t)
	a := conv("h1", "a", time.Now())
	b := conv("h1", "b", time.Now())
	for _, c := range []Conversation{a, b} {
		if err := st.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	// A file copied over another does not open as it.
	dir := st.hostDir("h1")
	data, _ := os.ReadFile(filepath.Join(dir, a.ID))
	if err := os.WriteFile(filepath.Join(dir, b.ID), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load("h1", b.ID); !errors.Is(err, keychain.ErrCorrupt) {
		t.Fatalf("swapped file: %v", err)
	}
}

func TestLimits(t *testing.T) {
	st, _ := store(t)
	start := time.Now()
	var first string
	for i := range MaxPerHost + 2 {
		c := conv("h", "c", start.Add(time.Duration(i)*time.Second))
		c.Items, c.Messages = nil, nil
		if i == 0 {
			first = c.ID
		}
		if err := st.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := st.List("h")
	if len(list) != MaxPerHost {
		t.Fatalf("kept %d", len(list))
	}
	if _, err := st.Load("h", first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest kept: %v", err)
	}

	big := conv("h", "big", start)
	chunk := strings.Repeat("x", 64<<10)
	big.Items, big.Messages = nil, nil
	for i := range 100 {
		big.Items = append(big.Items, Item{Kind: "text", Text: chunk})
		big.Messages = append(big.Messages, llm.Message{Role: "assistant", Content: chunk})
		if i == 99 {
			big.Items[i].Text = "last"
		}
	}
	if err := st.Save(big); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("h", big.ID)
	if err != nil || len(got.Items) == 0 || got.Items[len(got.Items)-1].Text != "last" || len(got.Items) == 100 {
		t.Fatalf("items %d, %v", len(got.Items), err)
	}
}

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{
		"  why is   disk full?\nmore": "why is disk full?",
		strings.Repeat("磁", 50):       strings.Repeat("磁", 40) + "…",
		"":                            "",
	} {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q", in, got)
		}
	}
}

// TestACPSessionKept checks that the external agent's session is kept
// with the conversation, and that a file saved before it was still opens.
func TestACPSessionKept(t *testing.T) {
	st, _ := store(t)
	now := time.Now().Round(0)
	c := conv("h1", "deploy", now)
	c.Agent, c.Messages = "acp:x", nil
	c.ACP = &ACPSession{ID: "sess-1", Agent: "acp:x", Cwd: "/tmp/sshyard-acp-1"}
	if err := st.Save(c); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("h1", c.ID)
	if err != nil || got.ACP == nil || *got.ACP != *c.ACP {
		t.Fatalf("load = %+v, %v", got.ACP, err)
	}
	old := map[string]any{"id": NewID(), "host": "h1", "title": "old", "agent": "acp:x", "created": now, "updated": now,
		"items": []map[string]any{{"kind": "user", "text": "hi"}, {"kind": "text", "text": "hello"}}}
	if err := st.write("h1", old["id"].(string), old); err != nil {
		t.Fatal(err)
	}
	got, err = st.Load("h1", old["id"].(string))
	if err != nil || got.ACP != nil || len(got.Items) != 2 || got.Title != "old" {
		t.Fatalf("old file = %+v, %v", got, err)
	}
}
