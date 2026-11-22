package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalScrollOnKey(t *testing.T) {
	if !Default().TerminalScrollOnKey {
		t.Fatal("scrolling to the bottom on a key must default on")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, tc := range []struct {
		body string
		want bool
	}{{`{}`, true}, {`{"terminalScrollOnKey":false}`, false}, {`{"terminalScrollOnKey":true}`, true}} {
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.TerminalScrollOnKey != tc.want {
			t.Fatalf("%s: scroll on key = %v, want %v, error %v", tc.body, got.TerminalScrollOnKey, tc.want, err)
		}
	}
	for _, on := range []bool{false, true} {
		s := Default()
		s.TerminalScrollOnKey = on
		if err := Save(path, s); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.TerminalScrollOnKey != on {
			t.Fatalf("saved %v: scroll on key = %v, error %v", on, got.TerminalScrollOnKey, err)
		}
	}
}
