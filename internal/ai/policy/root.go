package policy

import "strings"

// NewRootEngine grades what an agent types into an SSH host's terminal
// that su or sudo -i made root's, root saying so when it asks: each line
// as sudo would make it, never below Medium. Every other action, and every
// line while root reports false, is graded as NewEngine grades it.
func NewRootEngine(root func() bool) Engine { return rootEngine{root: root} }

type rootEngine struct {
	engine
	root func() bool
}

func (r rootEngine) Classify(a Action, scope Scope) Grade {
	if a.Where != Remote || !strings.EqualFold(strings.TrimSpace(a.Tool), "terminal_send") || r.root == nil || !r.root() {
		return r.engine.Classify(a, scope)
	}
	g := gradeRemote(a, normalizeScope(scope))
	if Rank(g.risk) < Rank(RiskMedium) {
		g.risk = RiskMedium
		if g.cat == "read" || g.cat == "" {
			g.cat = "permission"
		}
		g.why = strings.TrimSuffix("root terminal: "+g.why, ": ")
	}
	return g.Grade()
}
