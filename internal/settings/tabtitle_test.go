package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTabTitle: tabs show their host's name by default and for any value
// it does not know; the other sources survive a save.
func TestTabTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, data := range []string{`{}`, `{"tabTitle":""}`, `{"tabTitle":"unknown"}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.TabTitle != "" {
			t.Fatalf("%s: got %q, error %v", data, got.TabTitle, err)
		}
	}
	for _, source := range []string{TabTitleProgram, TabTitleCwd} {
		s := Default()
		s.TabTitle = source
		if err := Save(path, s); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.TabTitle != source {
			t.Fatalf("saved %q: got %q, error %v", source, got.TabTitle, err)
		}
	}
}
