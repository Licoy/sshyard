package session

import (
	"strings"
)

// Shell is a shell this computer runs in local sessions.
type Shell struct {
	// ID names the shell across runs: "pwsh" or "wsl:Ubuntu" on Windows,
	// its path elsewhere.
	ID string
	// Name is how the shell is shown.
	Name string
	// Path is the program, Args what follows it.
	Path string
	Args []string
	// Env are entries set over the app's environment.
	Env []string
}

// Shells are the shells of this computer, the default first; there is
// always one.
func Shells() []Shell { return localShells() }

// argsAt is sh's arguments with the folder it starts in set to dir, for a
// shell that takes it as an argument rather than as the process's own
// working folder: WSL's --cd. Other shells, and an empty dir, keep their
// arguments.
func argsAt(sh Shell, dir string) []string {
	if dir == "" || !strings.HasPrefix(sh.ID, "wsl:") {
		return sh.Args
	}
	args := append([]string(nil), sh.Args...)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--cd" {
			args[i+1] = dir
			return args
		}
	}
	return append(args, "--cd", dir)
}

// winProbe is what finds Windows' shells; tests stand in for it.
type winProbe struct {
	getenv   func(string) string
	exists   func(path string) bool
	lookPath func(file string) (string, error)
	// gitRoots are Git for Windows' install folders, from the registry.
	gitRoots []string
	// distros are the WSL distributions, the default first.
	distros []string
}

// windowsShells are PowerShell 7, Windows PowerShell, the command
// interpreter, Git Bash and each WSL distribution, those installed.
func windowsShells(p winProbe) []Shell {
	sys := p.getenv("SystemRoot")
	if sys == "" {
		sys = `C:\Windows`
	}
	system32 := winJoin(sys, "System32")
	pf, local := p.getenv("ProgramFiles"), p.getenv("LOCALAPPDATA")
	found := func(paths ...string) string {
		for _, path := range paths {
			if path != "" && p.exists(path) {
				return path
			}
		}
		return ""
	}
	onPath := func(file string) string {
		if path, err := p.lookPath(file); err == nil {
			return path
		}
		return ""
	}
	var out []Shell
	if path := found(onPath("pwsh.exe"), winJoin(pf, `PowerShell\7\pwsh.exe`)); path != "" {
		out = append(out, Shell{ID: "pwsh", Name: "PowerShell", Path: path})
	}
	if path := found(winJoin(system32, `WindowsPowerShell\v1.0\powershell.exe`), onPath("powershell.exe")); path != "" {
		out = append(out, Shell{ID: "powershell", Name: "Windows PowerShell", Path: path})
	}
	cmd := found(p.getenv("ComSpec"), winJoin(system32, "cmd.exe"))
	if cmd == "" {
		cmd = "cmd.exe"
	}
	out = append(out, Shell{ID: "cmd", Name: "CMD", Path: cmd})
	// Git Bash is Git's bin\bash.exe, which sets up its own environment
	// (git-bash.exe would open a window of its own). It stays in the
	// folder it starts in, the user's home.
	roots := append([]string{}, p.gitRoots...)
	if git := onPath("git.exe"); git != "" {
		roots = append(roots, gitRoot(git))
	}
	if pf != "" {
		roots = append(roots, winJoin(pf, "Git"))
	}
	if local != "" {
		roots = append(roots, winJoin(local, `Programs\Git`))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if bash := winJoin(root, `bin\bash.exe`); p.exists(bash) {
			out = append(out, Shell{ID: "gitbash", Name: "Git Bash", Path: bash, Args: []string{"--login", "-i"}, Env: []string{"CHERE_INVOKING=1"}})
			break
		}
	}
	if wsl := winJoin(system32, "wsl.exe"); len(p.distros) > 0 && p.exists(wsl) {
		// The terminal's names reach the distribution through WSLENV.
		env := []string{"WSLENV=" + strings.TrimPrefix(p.getenv("WSLENV")+":COLORTERM:TERM_PROGRAM", ":")}
		for _, d := range p.distros {
			// Docker Desktop's own distributions are not shells.
			if d == "" || strings.HasPrefix(strings.ToLower(d), "docker-desktop") {
				continue
			}
			out = append(out, Shell{ID: "wsl:" + d, Name: "WSL · " + d, Path: wsl, Args: []string{"-d", d, "--cd", "~"}, Env: env})
		}
	}
	return out
}

// gitRoot is the install folder of Git for Windows' git.exe, which is in
// its cmd, bin or mingw64\bin folder.
func gitRoot(git string) string {
	dir := git[:max(strings.LastIndexAny(git, `\/`), 0)]
	low := strings.ToLower(dir)
	for _, sub := range []string{`\mingw64\bin`, `\cmd`, `\bin`} {
		if strings.HasSuffix(low, sub) {
			return dir[:len(dir)-len(sub)]
		}
	}
	return ""
}

// winJoin joins a Windows path, whatever this computer's separator.
func winJoin(dir, rest string) string {
	if dir == "" {
		return ""
	}
	return strings.TrimRight(dir, `\/`) + `\` + rest
}
