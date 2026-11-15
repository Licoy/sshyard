package app

import (
	"bytes"
	"context"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/session"
	"golang.org/x/crypto/ssh"
)

// After an SSH terminal reconnects, its new shell is taken back to the
// folder the old one last reported (OSC 7, or read from the server, see
// followCwd), when the user lets it (settings.ReturnToFolder): a cd typed
// once the shell is at its prompt, as the cd to the host's folder is
// (session.Options.Start). It is not typed when a tmux session is
// attached instead (the session keeps its own folders), in a terminal
// opened for a command (a shell in a container: its folders are not the
// server's), nor for a shell whose quoting is not known.

// returnDirTimeout bounds asking the server for the user's shell and home.
const returnDirTimeout = 5 * time.Second

// returnDirScript prints the user's login shell and home folder, a line
// each. Every POSIX shell, fish and csh run it; others fail, and so type
// nothing.
const returnDirScript = `printf '%s\n%s\n' "$SHELL" "$HOME"`

// shellFamily is how a shell reads a quoted word.
type shellFamily int

const (
	// shellOther is a shell whose quoting is not known (csh, PowerShell,
	// cmd.exe, nushell…): nothing is typed into it.
	shellOther shellFamily = iota
	shellPOSIX
	// shellFish reads single quotes as POSIX shells do, but for a
	// backslash in them.
	shellFish
)

// familyOf is the family of the shell at path shell ($SHELL).
func familyOf(shell string) shellFamily {
	name := strings.TrimPrefix(path.Base(strings.TrimSpace(shell)), "-")
	switch name {
	case "sh", "bash", "rbash", "zsh", "ksh", "ksh93", "mksh", "pdksh", "oksh", "dash", "ash", "yash", "posh", "busybox":
		return shellPOSIX
	case "fish":
		return shellFish
	}
	return shellOther
}

// returnDirWanted says whether a shell starting at its terminal's connect
// number connects is to be taken back to back, the folder the terminal
// was in before: the setting is on, the shell is a reconnect's, the old
// one reported a folder, no tmux session is being attached (tmux, the
// command typed for it) and the terminal was not opened for a command
// (ran).
func returnDirWanted(on bool, connects int, back, tmux string, ran bool) bool {
	return on && connects > 1 && back != "" && tmux == "" && !ran
}

// startDirOf is the folder a new shell is in once started: the
// terminal's own (Host.TerminalDir, "~" and "~/…" under home), else
// home, where the server starts it.
func startDirOf(termDir, home string) string {
	switch {
	case termDir == "" || termDir == "~":
		return path.Clean(home)
	case strings.HasPrefix(termDir, "~/"):
		return path.Join(home, termDir[2:])
	case path.IsAbs(termDir):
		return path.Clean(termDir)
	}
	return path.Join(home, termDir)
}

// returnDirCmd is the command taking a shell, at path shell, that starts
// in start back to dir; "" when it is there already, or dir is not a
// folder that can be typed safely for that shell: not absolute, with a
// control character, not UTF-8, or a shell whose quoting is not known.
func returnDirCmd(dir, start, shell string) string {
	dir = followDir(dir)
	if dir == "" || dir == start || !utf8.ValidString(dir) {
		return ""
	}
	if strings.ContainsFunc(dir, func(r rune) bool { return r < 0x20 || r >= 0x7f && r < 0xa0 }) {
		return ""
	}
	switch familyOf(shell) {
	case shellFish:
		if strings.Contains(dir, `\`) {
			return ""
		}
	case shellOther:
		return ""
	}
	return "cd " + session.ShellQuote(dir)
}

// returnDirOn asks client's server for the user's shell and home, then
// is the command taking the shell that starts in termDir back to dir, ""
// for none (see returnDirCmd); a server that cannot tell gets none.
func returnDirOn(ctx context.Context, client *ssh.Client, dir, termDir string) string {
	ctx, cancel := context.WithTimeout(ctx, returnDirTimeout)
	defer cancel()
	var out bytes.Buffer
	if err := session.Exec(ctx, client, returnDirScript, &out, nil); err != nil {
		return ""
	}
	shell, home, _ := strings.Cut(strings.TrimRight(out.String(), "\r\n"), "\n")
	home = strings.TrimSpace(home)
	if !path.IsAbs(home) {
		home = ""
	}
	return returnDirCmd(dir, startDirOf(termDir, home), shell)
}

// noteCwd keeps dir, the folder c's terminal reported at its connect
// number connects, for its shell's next start (shellStart.backDir).
func (c *conn) noteCwd(dir string, connects int) {
	if st := c.start; st != nil {
		st.mu.Lock()
		if connects != st.cwdConn {
			st.prevCwd, st.prevConn = st.cwd, st.cwdConn
		}
		st.cwd, st.cwdConn = dir, connects
		st.mu.Unlock()
	}
}

// backDir is the folder last reported by a shell started before the one
// of connect connects, "" for none. Under st.mu.
func (st *shellStart) backDir(connects int) string {
	switch {
	case st.cwd != "" && st.cwdConn < connects:
		return st.cwd
	case st.prevCwd != "" && st.prevConn < connects:
		return st.prevCwd
	}
	return ""
}
