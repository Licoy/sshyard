package app

import (
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/egoist/mygo/ui"
)

func TestHiddenFileToggleKeepsOnlyVisibleSelections(t *testing.T) {
	fs := newFilesState(nil)
	cn := &conn{fs: fs}
	a := &App{}
	a.settings.FilesDual = true
	fs.localActive = true

	es := []sftpsvc.Entry{{Name: "..", IsDir: true}, {Name: ".hidden", IsDir: true}, {Name: "regular"}}
	fs.local.show("/local", es)
	fs.remote.show("/remote", es)
	fs.local.only(1)
	fs.local.chosen.Add("regular")
	fs.local.reveal = "pending"
	fs.local.search = &fileSearch{query: "hidden", results: []sftpsvc.Match{{Entry: es[1]}}, sel: 0}

	a.toggleHidden(cn)
	if !slices.Equal(fs.local.entries, []sftpsvc.Entry{es[0], es[2]}) || !fs.remote.showHidden || len(fs.remote.entries) != len(es) {
		t.Fatalf("wrong visibility after local toggle: local %v, remote %v", fs.local.entries, fs.remote.entries)
	}
	if fs.local.sel != -1 || !slices.Equal(fs.local.picked(), []sftpsvc.Entry{es[2]}) {
		t.Fatalf("wrong selection after hiding: row %d, chosen %v", fs.local.sel, fs.local.picked())
	}
	if fs.local.reveal != "pending" {
		t.Fatal("visibility change consumed the pending reveal")
	}
	if sr := fs.local.search; sr.query != "hidden" || len(sr.results) != 0 || sr.sel != -1 {
		t.Fatalf("search retained old results or dropped its query: %+v", sr)
	}
	fs.local.sort = ui.SortOrder{Column: colSize}
	fs.local.resort()
	if len(fs.local.entries) != 2 {
		t.Fatal("sorting restored hidden entries")
	}
	a.toggleHidden(cn)
	if len(fs.local.entries) != 3 || fs.local.chosen.Has(".hidden") {
		t.Fatal("showing hidden entries did not restore the listing or restored a hidden selection")
	}

	a.settings.FilesDual = false
	a.toggleHidden(cn)
	if fs.remote.showHidden || !fs.local.showHidden {
		t.Fatal("a single-sided pane did not toggle remote visibility")
	}
}

func TestHiddenFileSettingsApplyToExistingAndNewPanes(t *testing.T) {
	a, _ := newTestApp(t)
	fs := a.newFiles()
	a.conns[1] = &conn{id: 1, kind: layout.Files, fs: fs}
	s := a.settings
	s.HideHiddenFiles = true
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	for _, current := range []*filesState{fs, a.newFiles()} {
		if current.remote.showHidden || current.local.showHidden {
			t.Fatal("saved setting did not hide both sides")
		}
	}

	s.HideHiddenFiles = false
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if !fs.remote.showHidden || !fs.local.showHidden {
		t.Fatal("existing pane did not follow the changed setting")
	}
}
