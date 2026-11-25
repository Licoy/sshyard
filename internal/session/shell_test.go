package session

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeWindows is a Windows of the files given, with its usual variables.
func fakeWindows(files []string, gitRoots, distros []string, env map[string]string) winProbe {
	vars := map[string]string{"SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`, "ComSpec": `C:\Windows\System32\cmd.exe`}
	for k, v := range env {
		vars[k] = v
	}
	has := func(path string) bool {
		return slices.ContainsFunc(files, func(f string) bool { return strings.EqualFold(f, path) })
	}
	return winProbe{
		getenv: func(k string) string { return vars[k] },
		exists: has,
		lookPath: func(file string) (string, error) {
			for _, dir := range strings.Split(vars["PATH"], ";") {
				if dir != "" && has(dir+`\`+file) {
					return dir + `\` + file, nil
				}
			}
			return "", errors.New("not found")
		},
		gitRoots: gitRoots,
		distros:  distros,
	}
}

func shellIDs(shells []Shell) []string {
	var ids []string
	for _, sh := range shells {
		ids = append(ids, sh.ID)
	}
	return ids
}

// TestWindowsShells: the installed shells are found in order, PowerShell 7
// first; Git Bash is Git's bin\bash.exe, from the registry or git.exe on
// the PATH, and each WSL distribution but Docker Desktop's is one shell.
func TestWindowsShells(t *testing.T) {
	files := []string{
		`C:\Program Files\PowerShell\7\pwsh.exe`,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		`C:\Windows\System32\cmd.exe`,
		`C:\Windows\System32\wsl.exe`,
		`D:\Git\bin\bash.exe`,
	}
	got := windowsShells(fakeWindows(files, []string{`D:\Git`}, []string{"Ubuntu-24.04", "docker-desktop", "Debian"}, map[string]string{"WSLENV": "USERPROFILE/p"}))
	if want := []string{"pwsh", "powershell", "cmd", "gitbash", "wsl:Ubuntu-24.04", "wsl:Debian"}; !slices.Equal(shellIDs(got), want) {
		t.Fatalf("shells %q, want %q", shellIDs(got), want)
	}
	if got[0].Path != files[0] || got[0].Name != "PowerShell" {
		t.Fatalf("PowerShell 7 is %+v", got[0])
	}
	bash := got[3]
	if bash.Path != `D:\Git\bin\bash.exe` || !slices.Equal(bash.Args, []string{"--login", "-i"}) || !slices.Contains(bash.Env, "CHERE_INVOKING=1") {
		t.Fatalf("Git Bash is %+v", bash)
	}
	wsl := got[4]
	if wsl.Name != "WSL · Ubuntu-24.04" || wsl.Path != `C:\Windows\System32\wsl.exe` || !slices.Equal(wsl.Args, []string{"-d", "Ubuntu-24.04", "--cd", "~"}) {
		t.Fatalf("WSL is %+v", wsl)
	}
	if !slices.Equal(wsl.Env, []string{"WSLENV=USERPROFILE/p:COLORTERM:TERM_PROGRAM"}) {
		t.Fatalf("WSL env %q", wsl.Env)
	}

	// Git found from git.exe on the PATH, PowerShell 7 from the PATH too.
	files = []string{`E:\tools\pwsh\pwsh.exe`, `E:\Git\cmd\git.exe`, `E:\Git\bin\bash.exe`, `C:\Windows\System32\cmd.exe`}
	got = windowsShells(fakeWindows(files, nil, nil, map[string]string{"PATH": `E:\tools\pwsh;E:\Git\cmd`}))
	if want := []string{"pwsh", "cmd", "gitbash"}; !slices.Equal(shellIDs(got), want) {
		t.Fatalf("shells %q, want %q", shellIDs(got), want)
	}
	if got[0].Path != `E:\tools\pwsh\pwsh.exe` || got[2].Path != `E:\Git\bin\bash.exe` {
		t.Fatalf("found %q and %q", got[0].Path, got[2].Path)
	}

	// Nothing installed is still the command interpreter; WSL without a
	// distribution is no shell.
	got = windowsShells(fakeWindows([]string{`C:\Windows\System32\wsl.exe`}, nil, nil, map[string]string{"ComSpec": ""}))
	if len(got) != 1 || got[0].ID != "cmd" || got[0].Path != "cmd.exe" {
		t.Fatalf("bare Windows has %+v", got)
	}
}

// TestGitRoot: Git's folder is found from git.exe in cmd, bin or
// mingw64\bin, and not from elsewhere.
func TestGitRoot(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Program Files\Git\cmd\git.exe`:         `C:\Program Files\Git`,
		`C:\Program Files\Git\bin\git.exe`:         `C:\Program Files\Git`,
		`C:\Program Files\Git\mingw64\bin\git.exe`: `C:\Program Files\Git`,
		`C:\scoop\shims\git.exe`:                   "",
	} {
		if got := gitRoot(in); got != want {
			t.Errorf("gitRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestArgsAt: WSL's --cd takes the folder asked for, and other shells keep
// their arguments.
func TestArgsAt(t *testing.T) {
	wsl := Shell{ID: "wsl:Ubuntu", Args: []string{"-d", "Ubuntu", "--cd", "~"}}
	if got, want := argsAt(wsl, `C:\work`), []string{"-d", "Ubuntu", "--cd", `C:\work`}; !slices.Equal(got, want) {
		t.Errorf("WSL args = %q, want %q", got, want)
	}
	if got, want := argsAt(wsl, ""), wsl.Args; !slices.Equal(got, want) {
		t.Errorf("empty dir args = %q, want %q", got, want)
	}
	bash := Shell{ID: "gitbash", Args: []string{"--login", "-i"}}
	if got := argsAt(bash, `C:\work`); !slices.Equal(got, bash.Args) {
		t.Errorf("Git Bash args = %q, want %q", got, bash.Args)
	}
}
