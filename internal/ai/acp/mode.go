package acp

import (
	"net/url"
	"strings"
)

// skipTokens are matched against a mode id after lowercasing and removing
// '-' and '_'. See the package comment for why the list is wider than the
// four words in the spec.
var skipTokens = []string{
	"bypass", "fullaccess", "auto", "agent",
	"acceptedits", "alwaysapprove", "skippermission",
}

// SkipApproval reports whether a mode id would let the agent act without
// SSHYard's approval.
func SkipApproval(id string) bool {
	s := normalizeMode(id)
	if s == "" {
		return false
	}
	for _, tok := range skipTokens {
		if strings.Contains(s, tok) {
			return true
		}
	}
	return false
}

// normalizeMode lowercases a mode id and removes '-' and '_'. An id that is
// a URI with a fragment, such as GitHub Copilot's
// "https://agentclientprotocol.com/protocol/session-modes#plan", is judged
// by its fragment alone: the host name would otherwise match "agent" in
// every one of them.
func normalizeMode(id string) string {
	if u, err := url.Parse(id); err == nil && u.Scheme != "" && u.Fragment != "" {
		id = u.Fragment
	}
	s := strings.ToLower(id)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

// PickMode chooses the one session/set_mode call made after session/new.
// set is false when the current mode should be left alone.
// stop is true when the only modes skip approval: the session must not run.
//
// codex always requests read-only: its work happens on the remote host
// through SSHYard, never on this computer. claude and grok are left as they
// are. custom prefers read-only, then plan, then default, then the first
// remaining mode that does not skip approval.
func PickMode(kind, current string, available []string) (mode string, set, stop bool) {
	switch kind {
	case "codex":
		return "read-only", true, false
	case "claude", "grok", "builtin", "":
		return "", false, false
	}
	var keep []string
	for _, id := range available {
		if id != "" && !SkipApproval(id) {
			keep = append(keep, id)
		}
	}
	if len(keep) == 0 {
		return "", false, SkipApproval(current)
	}
	for _, want := range []string{"readonly", "plan", "default"} {
		for _, id := range keep {
			if normalizeMode(id) == want {
				if id == current {
					return id, false, false
				}
				return id, true, false
			}
		}
	}
	if keep[0] == current {
		return current, false, false
	}
	return keep[0], true, false
}
