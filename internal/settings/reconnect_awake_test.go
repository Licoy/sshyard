package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReturnToFolderAndKeepAwake: both default on, also in a settings
// file written before they existed, and keep what was chosen.
func TestReturnToFolderAndKeepAwake(t *testing.T) {
	d := Default()
	if !d.ReturnToFolder || !d.KeepAwake {
		t.Fatalf("defaults: return to folder %v, keep awake %v; want both on", d.ReturnToFolder, d.KeepAwake)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, tc := range []struct {
		body          string
		folder, awake bool
	}{
		{`{}`, true, true},
		{`{"autoReconnect":true}`, true, true},
		{`{"returnToFolder":false}`, false, true},
		{`{"keepAwake":false}`, true, false},
		{`{"returnToFolder":false,"keepAwake":false}`, false, false},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.ReturnToFolder != tc.folder || got.KeepAwake != tc.awake {
			t.Fatalf("%s: return to folder %v, keep awake %v, error %v", tc.body, got.ReturnToFolder, got.KeepAwake, err)
		}
	}
	s := Default()
	s.ReturnToFolder, s.KeepAwake = false, false
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got.ReturnToFolder || got.KeepAwake {
		t.Fatalf("saved off: return to folder %v, keep awake %v, error %v", got.ReturnToFolder, got.KeepAwake, err)
	}
}
