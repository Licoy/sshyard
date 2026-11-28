package app

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"unicode"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
)

var (
	errEditorProgram = errors.New("editor program must be an absolute path")
	errUnsafeEditor  = errors.New("editor program must not be a Windows script")
	errEditorArgs    = errors.New("invalid editor arguments")
	errEditorPattern = errors.New("invalid editor file name pattern")
)

func editorErrorText(m locales.Messages, err error) string {
	switch {
	case errors.Is(err, errEditorProgram):
		return m.ExternalEditorInvalidProgram()
	case errors.Is(err, errUnsafeEditor):
		return m.ExternalEditorUnsafeProgram()
	case errors.Is(err, errEditorArgs):
		return m.ExternalEditorInvalidArgs()
	case errors.Is(err, errEditorPattern):
		return m.ExternalEditorInvalidPattern()
	default:
		return securityErrorText(m, err)
	}
}

func editorPatterns(s string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(s)), func(r rune) bool { return r == ';' || r == ',' })
}

func matchEditor(eds []settings.ExternalEditor, name string) (settings.ExternalEditor, bool) {
	name = strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/")))
	for _, ed := range eds {
		if strings.TrimSpace(ed.Patterns) == "" {
			return ed, true
		}
		for _, pattern := range editorPatterns(ed.Patterns) {
			if matched, _ := path.Match(strings.TrimSpace(pattern), name); matched {
				return ed, true
			}
		}
	}
	return settings.ExternalEditor{}, false
}

// splitArgs groups quoted words and handles escapes without expanding values.
// A backslash before an ordinary character stays literal for Windows paths.
func splitArgs(s string) ([]string, error) {
	if strings.ContainsRune(s, 0) {
		return nil, errEditorArgs
	}
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == 0 && unicode.IsSpace(r):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
			continue
		case r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case r == '\\' && quote != '\'':
			if rest := runes[i+1:]; editorEscape(quote, rest) {
				i++
				r = runes[i]
			}
			word.WriteRune(r)
		default:
			word.WriteRune(r)
		}
		started = true
	}
	if quote != 0 {
		return nil, errEditorArgs
	}
	if started {
		args = append(args, word.String())
	}
	return args, nil
}

func editorEscape(quote rune, rest []rune) bool {
	if len(rest) == 0 {
		return false
	}
	if rest[0] == '\\' {
		// Preserve UNC prefixes, including after an option's equals sign.
		return len(rest) > 1 && (rest[1] == '"' || quote == 0 && rest[1] == '\'')
	}
	return rest[0] == '"' || quote == 0 && (rest[0] == '\'' || unicode.IsSpace(rest[0]))
}

func editorProgram(program, goos string) (string, error) {
	program = strings.TrimSpace(program)
	if strings.ContainsRune(program, 0) {
		return "", errEditorProgram
	}
	if goos != "windows" {
		if !path.IsAbs(program) {
			return "", errEditorProgram
		}
		return program, nil
	}
	p := strings.ReplaceAll(program, `\`, "/")
	switch {
	case strings.HasPrefix(p, "//"):
		server, rest, ok := strings.Cut(p[2:], "/")
		share, _, file := strings.Cut(rest, "/")
		if !ok || !file || server == "" || share == "" || server == "." || server == "?" {
			return "", errEditorProgram
		}
	case len(p) >= 3 && p[1:3] == ":/" && strings.Contains("abcdefghijklmnopqrstuvwxyz", strings.ToLower(p[:1])):
		p = p[2:]
	default:
		return "", errEditorProgram
	}
	if strings.ContainsRune(p, ':') {
		return "", errUnsafeEditor
	}
	base := strings.TrimRight(path.Base(path.Clean(p)), " .")
	switch strings.ToLower(path.Ext(base)) {
	case ".cmd", ".bat", ".ps1", ".vbs", ".js", ".wsf", ".com":
		return "", errUnsafeEditor
	}
	return program, nil
}

func editorOptions(ed settings.ExternalEditor, goos string) (string, []string, error) {
	program, err := editorProgram(ed.Program, goos)
	if err != nil {
		return "", nil, err
	}
	args, err := splitArgs(ed.Args)
	if err != nil {
		return "", nil, err
	}
	for _, pattern := range editorPatterns(ed.Patterns) {
		if _, err := path.Match(strings.TrimSpace(pattern), ""); err != nil {
			return "", nil, errEditorPattern
		}
	}
	return program, args, nil
}

func validateEditor(ed settings.ExternalEditor, goos string) error {
	_, _, err := editorOptions(ed, goos)
	return err
}

func editorCommand(ed settings.ExternalEditor, file, goos string) (string, []string, error) {
	program, args, err := editorOptions(ed, goos)
	if err != nil {
		return "", nil, err
	}
	bundle := goos == "darwin" && strings.EqualFold(path.Ext(program), ".app")
	replaced := false
	expanded := args[:0]
	for _, arg := range args {
		if bundle && arg == "{file}" {
			continue
		}
		if strings.Contains(arg, "{file}") {
			arg = strings.ReplaceAll(arg, "{file}", file)
			replaced = true
		}
		expanded = append(expanded, arg)
	}
	if bundle {
		openArgs := []string{"-a", program, file}
		if len(expanded) > 0 {
			openArgs = append(append(openArgs, "--args"), expanded...)
		}
		return "/usr/bin/open", openArgs, nil
	}
	if !replaced {
		expanded = append(expanded, file)
	}
	return program, expanded, nil
}

type editorCandidate struct {
	name, bundle, bin, winPath string
}

var editorCandidates = []editorCandidate{
	{"VS Code", "Visual Studio Code.app", "code", "Microsoft VS Code/Code.exe"},
	{"VSCodium", "VSCodium.app", "codium", "VSCodium/VSCodium.exe"},
	{"Cursor", "Cursor.app", "cursor", "Cursor/Cursor.exe"},
	{"Sublime Text", "Sublime Text.app", "subl", "Sublime Text/sublime_text.exe"},
	{"Zed", "Zed.app", "zed", "Zed/zed.exe"},
	{"Notepad++", "", "", "Notepad++/notepad++.exe"},
	{"TextMate", "TextMate.app", "mate", ""},
	{"BBEdit", "BBEdit.app", "bbedit", ""},
	{"Kate", "Kate.app", "kate", "Kate/bin/kate.exe"},
	{"gedit", "", "gedit", ""},
}

func (ed editorCandidate) locations(goos string) (paths []string, bin string) {
	switch goos {
	case "darwin":
		if ed.bundle != "" {
			home, _ := os.UserHomeDir()
			if home != "" {
				paths = append(paths, path.Join(home, "Applications", ed.bundle))
			}
			paths = append(paths, path.Join("/Applications", ed.bundle))
		}
	case "linux":
		if ed.bin != "" {
			paths = []string{path.Join("/usr/local/bin", ed.bin), path.Join("/usr/bin", ed.bin)}
		}
	case "windows":
		if ed.winPath == "" {
			return nil, ""
		}
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if root := os.Getenv(env); root != "" {
				if env == "LocalAppData" {
					root = path.Join(root, "Programs")
				}
				paths = append(paths, strings.ReplaceAll(path.Join(root, ed.winPath), "/", `\`))
			}
		}
		return paths, path.Base(ed.winPath)
	}
	return paths, ed.bin
}

func detectEditors(goos string) []settings.ExternalEditor {
	return findEditors(goos, os.Stat, exec.LookPath)
}

func findEditors(goos string, stat func(string) (fs.FileInfo, error), lookPath func(string) (string, error)) []settings.ExternalEditor {
	var editors []settings.ExternalEditor
	for _, ed := range editorCandidates {
		paths, bin := ed.locations(goos)
		if bin != "" {
			if p, err := lookPath(bin); err == nil {
				paths = append(paths, p)
			}
		}
		for _, p := range paths {
			if _, err := editorProgram(p, goos); err != nil {
				continue
			}
			if info, err := stat(p); err == nil && installedEditor(goos, p, info) {
				editors = append(editors, settings.ExternalEditor{Name: ed.name, Program: p, Args: "{file}"})
				break
			}
		}
	}
	return editors
}

func installedEditor(goos, p string, info fs.FileInfo) bool {
	if goos == "darwin" && strings.EqualFold(path.Ext(p), ".app") {
		return info.IsDir()
	}
	return info.Mode().IsRegular() && (goos == "windows" || info.Mode().Perm()&0o111 != 0)
}

func resolveWindowsEditor(name string, lookPath func(string) (string, error)) (string, error) {
	resolved, err := lookPath(name)
	if err != nil {
		return "", err
	}
	// Recheck the suffix after executable lookup can apply PATHEXT.
	return editorProgram(resolved, "windows")
}

var startEditor = func(name string, args []string) error {
	if runtime.GOOS == "windows" {
		var err error
		if name, err = resolveWindowsEditor(name, exec.LookPath); err != nil {
			return err
		}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("external editor exited: %v", err)
		}
	}()
	return nil
}
