package app

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
)

func TestUploadModeText(t *testing.T) {
	for in, want := range map[string]string{"": "", "  ": "", "644": "644", "0644": "644", "7": "007", " 755 ": "755", "000": "000"} {
		if got, ok := uploadModeText(in); !ok || got != want {
			t.Fatalf("uploadModeText(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"888", "1000", "rw-r--r--", "-1"} {
		if _, ok := uploadModeText(in); ok {
			t.Fatalf("uploadModeText(%q) accepted", in)
		}
	}
}

func TestEditorsSheetSavesInOrder(t *testing.T) {
	old := scanEditors
	scanEditors = func() []settings.ExternalEditor { return nil }
	t.Cleanup(func() { scanEditors = old })
	a, tt := newTestApp(t)
	m := locales.In("en")
	prog, other := "/opt/editor/bin/edit", "/opt/other/bin/other"
	if runtime.GOOS == "windows" {
		prog, other = `C:\Editor\edit.exe`, `C:\Other\other.exe`
	}
	s := a.Settings()
	s.ExternalEditors = []settings.ExternalEditor{{Name: "Old", Program: other}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.openEditors()
	waitFor(t, tt, "scan", func() bool { return !a.edForm.scanning })
	f := a.edForm

	// A relative program is refused, and the draft stays to be fixed.
	f.draft, f.at = &settings.ExternalEditor{Program: "edit"}, -1
	if f.keepDraft(m) || f.err != m.ExternalEditorInvalidProgram() || f.draft == nil {
		t.Fatalf("relative program kept: %q", f.err)
	}
	// Bad quoting too.
	f.draft = &settings.ExternalEditor{Program: prog, Args: `"{file}`}
	if f.keepDraft(m) || f.err != m.ExternalEditorInvalidArgs() {
		t.Fatalf("bad arguments kept: %q", f.err)
	}
	// A name left empty is the program's.
	f.draft = &settings.ExternalEditor{Program: " " + prog + " ", Patterns: " *.conf; *.yml "}
	if !f.keepDraft(m) || f.draft != nil {
		t.Fatalf("editor refused: %q", f.err)
	}
	want := settings.ExternalEditor{Name: "edit", Program: prog, Patterns: "*.conf; *.yml"}
	if got := f.list[1]; got != want {
		t.Fatalf("added %+v, want %+v", got, want)
	}
	// Changing one replaces it in place.
	d := f.list[0]
	d.Args = "--wait {file}"
	f.draft, f.at = &d, 0
	if !f.keepDraft(m) || len(f.list) != 2 || f.list[0].Args != "--wait {file}" {
		t.Fatalf("change not kept: %+v", f.list)
	}
	// Moved first, then saved: the order is the matching order.
	f.list[0], f.list[1] = f.list[1], f.list[0]
	a.saveEditors(m)
	if a.edFormOpen {
		t.Fatalf("sheet stayed open: %q", f.err)
	}
	got := a.Settings().ExternalEditors
	if len(got) != 2 || got[0] != want || got[1].Program != other {
		t.Fatalf("saved %+v", got)
	}
	if ed, ok := matchEditor(got, "nginx.conf"); !ok || ed.Program != prog {
		t.Fatalf("match %+v %v", ed, ok)
	}
	if ed, ok := matchEditor(got, "main.go"); !ok || ed.Program != other {
		t.Fatalf("fallback match %+v %v", ed, ok)
	}

	// Canceling leaves the settings as they were.
	a.openEditors()
	a.edForm.list = nil
	a.edFormOpen = false
	tt.Frame()
	if !reflect.DeepEqual(a.Settings().ExternalEditors, got) {
		t.Fatal("cancel changed the editors")
	}
}
