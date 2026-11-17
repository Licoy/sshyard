package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"
)

// TagEnv is the variable that marks a terminal's shell on its server,
// set with the "env" request before the shell starts. OpenSSH takes LC_*
// variables in the default configuration of most distributions.
const TagEnv = "LC_SSHYARD_TERM"

// ErrNoCwd is ShellCwd finding no shell, or no directory for it.
var ErrNoCwd = errors.New("session: the shell's directory could not be read")

func newTag() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Tag is the mark of the session's shell (TagEnv), and whether the server
// took it for the shell running now.
func (s *Session) Tag() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tag, s.tagged && s.shell != nil
}

// cwdBody prints the directory of the shell whose environment holds
// tag, from Linux's /proc: of the processes holding it, the one whose
// parent does not (sshd), as the programs the shell runs inherit it.
// Without a tag (a server that took no LC_* variable, as RHEL's lists
// them one by one) it is the one process on a terminal that the sshd
// serving this connection started: the shell, if the connection runs no
// other. That sshd is found going up from $$, the sh the body runs in
// (cwdScript's, or the probe shell's), which is on no terminal itself.
// It exits 1 when it finds none, and holds no single quote or newline.
func cwdBody(tag string) string { return shellBody(tag, `readlink "$d/cwd"; exit 0`) }

// shellBody finds the shell as cwdBody does and runs then with d its
// directory in /proc (p); it exits 1 when it finds none.
func shellBody(tag, then string) string {
	head := `p=/proc; m=$$; pp() { sed "s/.*) //" "$1/stat" 2>/dev/null | cut -d" " -f2; }; `
	if tag != "" {
		return head + `t="` + TagEnv + `=` + tag + `"; ` +
			`for f in $(grep -lzx "$t" $p/[0-9]*/environ 2>/dev/null); do ` +
			`d=${f%/environ}; grep -qzx "$t" "$p/$(pp $d)/environ" 2>/dev/null || { ` + then + `; }; ` +
			`done; exit 1`
	}
	return head + `s=$m; ` +
		`until case $(cat $p/$s/comm 2>/dev/null) in sshd*|dropbear*) true;; *) false;; esac; do ` +
		`s=$(pp $p/$s); [ "${s:-0}" -gt 1 ] 2>/dev/null || exit 1; done; ` +
		`n=0; w=; for f in $(grep -l ") [A-Za-z] $s " $p/[0-9]*/stat 2>/dev/null); do ` +
		`d=${f%/stat}; [ "$(pp $d)" = "$s" ] || continue; ` +
		`case $(readlink "$d/fd/0" 2>/dev/null) in /dev/pts/*) n=$((n+1)); w=$d;; esac; ` +
		`done; [ "$n" = 1 ] && [ -n "$w" ] && { d=$w; ` + then + `; }; exit 1`
}

// cwdScript is cwdBody as a command of its own, run under sh whatever
// the user's login shell is.
func cwdScript(tag string) string { return "sh -c '" + cwdBody(tag) + "'" }

// ShellCwd asks the server over client which directory the shell tagged
// tag is in, for a shell that does not report it (OSC 7); "" for a shell
// the server did not tag, found as the connection's one terminal. It
// reads Linux's /proc, so it fails on other servers, as when it finds no
// shell, or more than one untagged (ErrNoCwd). It opens a channel of its
// own each time; Session.ProbeCwd keeps one open.
func ShellCwd(ctx context.Context, client *ssh.Client, tag string) (string, error) {
	out, found, err := execBody(ctx, client, cwdBody(tag))
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoCwd
	}
	return cwdPath(out)
}

// execBody runs body under sh on a channel of its own and returns what it
// printed, trimmed; found is false when it exited with an error.
func execBody(ctx context.Context, client *ssh.Client, body string) (out string, found bool, err error) {
	var b bytes.Buffer
	if err := Exec(ctx, client, "sh -c '"+body+"'", &b, nil); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(b.String()), true, nil
}

// cwdPath is the directory d the server printed, cleaned, or ErrNoCwd
// for one that is not an absolute path on a line of its own.
func cwdPath(d string) (string, error) {
	if !path.IsAbs(d) || strings.ContainsAny(d, "\n\x00") {
		return "", ErrNoCwd
	}
	return path.Clean(d), nil
}
