package session

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// ErrNoUser is ProbeUser finding no shell, or no user for it.
var ErrNoUser = errors.New("session: the terminal's user could not be read")

// TermUser is who runs a terminal: its shell, the one the SSH login
// started, and what the terminal runs in the foreground, which su or
// sudo -i make another user. A name is "" when the server has none for
// the uid.
type TermUser struct {
	LoginUID int
	Login    string
	UID      int
	Name     string
}

// Switched reports a terminal whose foreground runs as another user
// than its shell.
func (u TermUser) Switched() bool { return u.UID != u.LoginUID }

// Root reports a terminal switched to root.
func (u TermUser) Root() bool { return u.Switched() && u.UID == 0 }

// userBody prints the effective uids of the shell tagged tag and of the
// foreground process group of its terminal (the leader, read from
// /proc's world-readable stat and status even when it is root's), then
// their names: "1000 0 alice root", "-" for a name not found. Of no
// terminal, or a leader gone, the shell is the foreground. It holds no
// single quote or newline.
func userBody(tag string) string {
	return shellBody(tag, `nm() { n=$(getent passwd "$1" 2>/dev/null | cut -d: -f1); `+
		`[ -n "$n" ] || n=$(while IFS=: read -r a b i r; do [ "$i" = "$1" ] && { echo "$a"; break; }; done </etc/passwd 2>/dev/null); echo "${n:--}"; }; `+
		`eu() { while read -r k a b r; do [ "$k" = Uid: ] && { echo "$b"; return; }; done <"$1/status"; } 2>/dev/null; `+
		`g=$(sed "s/.*) //" "$d/stat" 2>/dev/null | cut -d" " -f6); f=$p/$g; `+
		`{ [ "${g:-0}" -gt 0 ] 2>/dev/null && [ -r "$f/status" ]; } || f=$d; `+
		`l=$(eu "$d"); u=$(eu "$f"); [ -n "$l" ] && [ -n "$u" ] || exit 1; `+
		`echo "$l $u $(nm "$l") $(nm "$u")"; exit 0`)
}

// parseUser reads what userBody printed.
func parseUser(out string) (TermUser, error) {
	f := strings.Fields(out)
	if len(f) != 4 {
		return TermUser{}, ErrNoUser
	}
	login, err1 := strconv.Atoi(f[0])
	uid, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || login < 0 || uid < 0 {
		return TermUser{}, ErrNoUser
	}
	name := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	return TermUser{LoginUID: login, Login: name(f[2]), UID: uid, Name: name(f[3])}, nil
}

// ProbeUser reads from the server who runs the session's terminal, as
// ProbeCwd reads its directory and through the same probe shell. It
// reads Linux's /proc, so it fails on other servers, as when it finds no
// shell (ErrNoUser).
func (s *Session) ProbeUser(ctx context.Context) (TermUser, error) {
	out, found, err := s.probeShell(ctx, userBody)
	if err != nil {
		return TermUser{}, err
	}
	if !found {
		return TermUser{}, ErrNoUser
	}
	return parseUser(out)
}
