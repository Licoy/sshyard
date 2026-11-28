package app

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/settings"
)

func TestMatchEditor(t *testing.T) {
	editors := []settings.ExternalEditor{
		{Name: "shell", Patterns: " *.sh ; .env, README.?D "},
		{Name: "other", Patterns: "*.md;*.txt"},
		{Name: "default"},
	}
	for _, tt := range []struct{ file, want string }{
		{"/home/user/README.MD", "shell"},
		{`C:\Users\me\script.SH`, "shell"},
		{".ENV", "shell"},
		{"notes.txt", "other"},
		{"no-extension", "default"},
	} {
		if ed, ok := matchEditor(editors, tt.file); !ok || ed.Name != tt.want {
			t.Errorf("matchEditor(%q) = %q, %v, want %q", tt.file, ed.Name, ok, tt.want)
		}
	}
	if _, ok := matchEditor(editors[:2], "no-extension"); ok {
		t.Fatal("unexpected rule for a file that matches none")
	}
}

func TestSplitEditorArgs(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  []string
	}{
		{" \t\n ", nil},
		{`--title "one \"two\"" '$(echo literal)' an\ argument a'b'c '' ""`, []string{"--title", `one "two"`, "$(echo literal)", "an argument", "abc", "", ""}},
		{`C:\Users\me\work "C:\Program Files\Editor" --path=\\server\share\folder "\\server\share name\folder"`, []string{`C:\Users\me\work`, `C:\Program Files\Editor`, `--path=\\server\share\folder`, `\\server\share name\folder`}},
		{`"C:\Work Directory\\" C:\Work\`, []string{`C:\Work Directory\`, `C:\Work\`}},
	} {
		got, err := splitArgs(tt.input)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("splitArgs(%q) = %q, %v, want %q", tt.input, got, err, tt.want)
		}
	}
	for _, in := range []string{`"unterminated`, `'unterminated`, "a\x00b"} {
		if _, err := splitArgs(in); !errors.Is(err, errEditorArgs) {
			t.Errorf("splitArgs(%q) error = %v", in, err)
		}
	}
}

func TestEditorCommand(t *testing.T) {
	file := `/tmp/project/notes 'too many & spaces; $(date)'.txt`
	bundle := "/Applications/Example Editor.APP"
	for _, tt := range []struct {
		goos, program, template, name string
		args                          []string
	}{
		{"linux", "/usr/bin/editor", "", "/usr/bin/editor", []string{file}},
		{"linux", "/usr/bin/editor", `--reuse "two words"`, "/usr/bin/editor", []string{"--reuse", "two words", file}},
		{"linux", "/usr/bin/editor", `--goto={file}:12 "--file={file}:{file}"`, "/usr/bin/editor", []string{"--goto=" + file + ":12", "--file=" + file + ":" + file}},
		{"darwin", bundle, "", "/usr/bin/open", []string{"-a", bundle, file}},
		{"darwin", bundle, `"{file}"`, "/usr/bin/open", []string{"-a", bundle, file}},
		{"darwin", bundle, `--reuse {file} --goto={file}:2`, "/usr/bin/open", []string{"-a", bundle, file, "--args", "--reuse", "--goto=" + file + ":2"}},
		{"windows", `C:\Program Files\Editor.exe`, "-- {file}", `C:\Program Files\Editor.exe`, []string{"--", file}},
		{"windows", `\\server\share\Editor.exe`, "", `\\server\share\Editor.exe`, []string{file}},
	} {
		name, args, err := editorCommand(settings.ExternalEditor{Program: tt.program, Args: tt.template}, file, tt.goos)
		if err != nil || name != tt.name || !slices.Equal(args, tt.args) {
			t.Errorf("%s %q %q = %q %q, %v, want %q %q", tt.goos, tt.program, tt.template, name, args, err, tt.name, tt.args)
		}
	}
}

func TestValidateEditor(t *testing.T) {
	for _, tt := range []struct {
		goos string
		ed   settings.ExternalEditor
		want error
	}{
		{"linux", settings.ExternalEditor{Program: "/usr/bin/editor", Patterns: "*.txt;*.MD"}, nil},
		{"linux", settings.ExternalEditor{Program: ""}, errEditorProgram},
		{"linux", settings.ExternalEditor{Program: "editor"}, errEditorProgram},
		{"linux", settings.ExternalEditor{Program: "/usr/bin/editor", Patterns: "*.txt,[a-"}, errEditorPattern},
		{"linux", settings.ExternalEditor{Program: "/usr/bin/editor", Args: `--arg="bad`}, errEditorArgs},
		{"windows", settings.ExternalEditor{Program: `C:editor.exe`}, errEditorProgram},
		{"windows", settings.ExternalEditor{Program: `\Editor.exe`}, errEditorProgram},
		{"windows", settings.ExternalEditor{Program: `C:/Editors/Editor.ExE`}, nil},
		{"windows", settings.ExternalEditor{Program: `\\server\share\Editor.exe`}, nil},
		{"windows", settings.ExternalEditor{Program: `\\?\C:\editor.exe`}, errEditorProgram},
		{"windows", settings.ExternalEditor{Program: `C:\Editors\Helper.CmD. . `}, errUnsafeEditor},
		{"windows", settings.ExternalEditor{Program: `C:\Editors\Helper.cMd\.`}, errUnsafeEditor},
		{"windows", settings.ExternalEditor{Program: `C:\Editors\Helper.bat:stream`}, errUnsafeEditor},
	} {
		if err := validateEditor(tt.ed, tt.goos); !errors.Is(err, tt.want) {
			t.Errorf("validateEditor(%+v, %s) = %v, want %v", tt.ed, tt.goos, err, tt.want)
		}
	}
	for _, suffix := range []string{"CMD", "bat", "ps1", "vbs", "js", "wsf", "com"} {
		ed := settings.ExternalEditor{Program: `C:\Tools\helper.` + suffix}
		if _, _, err := editorCommand(ed, `C:\Temp\file.txt`, "windows"); !errors.Is(err, errUnsafeEditor) {
			t.Errorf("editorCommand(%q) = %v, want unsafe editor error", ed.Program, err)
		}
	}
}

func TestResolveWindowsEditor(t *testing.T) {
	for _, suffix := range []string{".exe", ".CMD"} {
		p := `C:\Editors\editor`
		got, err := resolveWindowsEditor(p, func(name string) (string, error) {
			if name != p {
				t.Fatalf("looked up %q, want %q", name, p)
			}
			return p + suffix, nil
		})
		if suffix == ".exe" && (got != p+suffix || err != nil) || suffix == ".CMD" && !errors.Is(err, errUnsafeEditor) {
			t.Errorf("resolve %q = %q, %v", suffix, got, err)
		}
	}
}

type editorFileInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (i editorFileInfo) Mode() fs.FileMode { return i.mode }

func TestDetectEditors(t *testing.T) {
	t.Setenv("HOME", "/home/guest")
	t.Setenv("USERPROFILE", "/home/guest")
	dir, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const code = "/home/guest/Applications/Visual Studio Code.app"
	const kate = "/usr/bin/kate"
	const codium = "/usr/local/bin/codium"
	const zed = "/usr/bin/zed"
	exists := map[string]fs.FileInfo{
		code:                      dir,
		"/Applications/Zed.app":   dir,
		"/usr/local/bin/cursor":   editorFileInfo{mode: 0o644},
		"/usr/local/bin/disabled": editorFileInfo{mode: fs.ModeDir},
		kate:                      editorFileInfo{mode: 0o755},
		codium:                    editorFileInfo{mode: 0o755},
		zed:                       editorFileInfo{mode: 0o755},
	}
	stat := func(p string) (fs.FileInfo, error) {
		if info, ok := exists[p]; ok {
			return info, nil
		}
		return nil, fs.ErrNotExist
	}
	lookup := func(name string) (string, error) {
		switch name {
		case "code":
			return "/usr/bin/code", nil
		case "cursor":
			return "/usr/local/bin/cursor", nil
		case "subl":
			return "/usr/local/bin/disabled", nil
		case "kate":
			return kate, nil
		case "gedit":
			return "/usr/bin/gedit", nil
		default:
			return "", fs.ErrNotExist
		}
	}
	// An installed bundle or executable is enough; a vanished PATH result,
	// a directory at an executable path and a nonexecutable file are skipped.
	want := []settings.ExternalEditor{
		{Name: "VS Code", Program: code, Args: "{file}"},
		{Name: "Zed", Program: "/Applications/Zed.app", Args: "{file}"},
		{Name: "Kate", Program: kate, Args: "{file}"},
	}
	if got := findEditors("darwin", stat, lookup); !slices.Equal(got, want) {
		t.Fatalf("detected macOS editors = %+v, want %+v", got, want)
	}
	// VSCodium and Zed are installed outside the supplied PATH lookup.
	want = []settings.ExternalEditor{
		{Name: "VSCodium", Program: codium, Args: "{file}"},
		{Name: "Zed", Program: zed, Args: "{file}"},
		{Name: "Kate", Program: kate, Args: "{file}"},
	}
	if got := findEditors("linux", stat, lookup); !slices.Equal(got, want) {
		t.Fatalf("detected Linux editors = %+v, want %+v", got, want)
	}
}
