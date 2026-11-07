package policy

import "strings"

// Engine grades an action and applies the session's permission mode.
// SSHYard is the judge: the agent's reason never changes the result.
type Engine interface {
	Classify(a Action, scope Scope) Grade
	Decide(mode Mode, grade Grade, reviewerConfigured bool) Decision
}

type engine struct{}

// NewEngine returns the built-in grader.
func NewEngine() Engine { return engine{} }

// Rank orders risks for "highest wins":
// Critical > Unknown > High > Medium > Low > ReadOnly.
// The Risk iota is not this order (Unknown is declared last so a zero
// Grade is ReadOnly). Equal rank keeps the earlier command, unless the
// later one has the more severe category.
func Rank(r Risk) int {
	switch r {
	case RiskReadOnly:
		return 0
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	case RiskUnknown:
		return 4
	case RiskCritical:
		return 5
	default:
		return 4
	}
}

// String is the audit token for a risk.
func (r Risk) String() string {
	switch r {
	case RiskReadOnly:
		return "readonly"
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskCritical:
		return "critical"
	case RiskUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

func (engine) Classify(a Action, scope Scope) Grade {
	if a.Where == Local {
		// Writable roots are a remote idea. Local writes are low only inside
		// the sandbox (or WritableRoots the caller set explicitly).
		return gradeLocal(a, scope).Grade()
	}
	return gradeRemote(a, normalizeScope(scope)).Grade()
}

// Decide applies the mode table.
//
// ModeAsk auto-runs only ReadOnly. Every other risk asks. File writes
// (Diff) do not offer "allow similar" in ModeAsk, so the diff is shown
// every time. That is stricter than the spec's table and is an open
// product question.
//
// ModeReview auto-runs ReadOnly and sends every other grade except
// Critical to a configured reviewer; Critical always asks the user, so no
// model can wave through what the rules call catastrophic. Without a
// reviewer, ReadOnly and Low auto-run and all other grades ask the user.
//
// ModeFull auto-runs every grade, Unknown included: the user picked the
// mode that promises to stop asking, and a grade SSHYard could not settle
// is not evidence against the action. Critical and Unknown still set Red,
// which any surface showing the decision reads as "ran unjudged". High
// auto-runs.
// Similar is offered only for Low and Medium on Ask or Review outcomes; the
// gate narrows it to a single command when a reviewer is configured.
func (engine) Decide(mode Mode, g Grade, reviewerConfigured bool) Decision {
	if !mode.Valid() {
		mode = ModeAsk
	}
	d := Decision{
		Grade:   g,
		Mode:    mode,
		Red:     g.Risk == RiskCritical,
		Diff:    g.Category == "write" || g.Category == "low-write",
		By:      ByRule,
		Outcome: Ask,
	}
	switch mode {
	case ModeFull:
		d.Outcome = Allow
		d.By = ByFull
		// An action the rules could not grade still runs; Red records that
		// it ran unjudged.
		if g.Risk == RiskUnknown {
			d.Red = true
		}
	case ModeReview:
		if g.Risk == RiskReadOnly || !reviewerConfigured && g.Risk == RiskLow {
			d.Outcome = Allow
		} else if reviewerConfigured && g.Risk != RiskCritical {
			d.Outcome = Review
		}
	default:
		if g.Risk == RiskReadOnly {
			d.Outcome = Allow
		}
	}
	if (d.Outcome == Ask || d.Outcome == Review) && (g.Risk == RiskLow || g.Risk == RiskMedium) {
		d.Similar = true
	}
	if mode == ModeAsk && d.Diff {
		d.Similar = false
	}
	return d
}

func gradeRemote(a Action, scope Scope) partial {
	env := &shellEnv{scope: scope, dir: firstNonEmpty(a.Dir, scope.Home)}
	switch strings.ToLower(strings.TrimSpace(a.Tool)) {
	case "host_info", "pane_output":
		tool := strings.ToLower(strings.TrimSpace(a.Tool))
		return partial{cat: "read", risk: RiskReadOnly, prefix: tool, why: tool}
	case "search_files":
		g := gradeReadPath(env, a.Path)
		return better(g, partial{cat: "sensitive-read", risk: RiskMedium, prefix: g.prefix, why: "directory content search"})
	case "list_dir", "read_file":
		return gradeReadPath(env, a.Path)
	case "write_file":
		return gradeFileWrite(env, a.Path, writeFileTool)
	case "run_command", "":
		if strings.TrimSpace(a.Command) == "" {
			if a.Path != "" {
				return gradeReadPath(env, a.Path)
			}
			return unk("empty action")
		}
		return env.script(a.Command)
	default:
		if strings.TrimSpace(a.Command) != "" {
			return env.script(a.Command)
		}
		if a.Path != "" {
			return gradeReadPath(env, a.Path)
		}
		return unk("unrecognized tool")
	}
}

func gradeLocal(a Action, scope Scope) partial {
	env := &shellEnv{scope: scope, dir: firstNonEmpty(a.Dir, scope.Sandbox), local: true}
	kind := strings.ToLower(strings.TrimSpace(a.Tool))
	switch kind {
	case "think":
		return partial{cat: "local-read", risk: RiskReadOnly, prefix: "think", why: "think"}
	case "read", "search":
		return localRead(env, a.Path)
	case "edit":
		g := gradeFileWrite(env, a.Path, writeCreate)
		if g.cat == "" {
			return unk("local edit")
		}
		return g
	case "delete":
		return localDelete(env, a.Path)
	case "move":
		g := gradeFileWrite(env, a.Path, writeMutate)
		if g.risk == RiskReadOnly || g.cat == "" {
			return unk("local move")
		}
		return g
	case "execute":
		if strings.TrimSpace(a.Command) == "" {
			return unk("empty execute")
		}
		g := env.script(a.Command)
		if Rank(g.risk) < Rank(RiskLow) {
			g.risk = RiskLow
			g.cat = "local"
			g.why = "local execute"
		}
		return g
	case "fetch":
		g := partial{cat: "network", risk: RiskMedium, prefix: "fetch", why: "fetch", curl: true}
		if strings.TrimSpace(a.Command) != "" {
			g = better(g, env.script(a.Command))
		}
		return g
	default:
		if strings.TrimSpace(a.Command) != "" {
			a.Tool = "execute"
			return gradeLocal(a, scope)
		}
		if a.Path != "" {
			return localRead(env, a.Path)
		}
		return unk("unrecognized local tool")
	}
}

func localRead(env *shellEnv, p string) partial {
	if strings.TrimSpace(p) == "" {
		return unk("missing path")
	}
	abs, ok := env.resolve(p)
	inside := ok && env.scope.Sandbox != "" && localWithin(env.scope.Sandbox, abs)
	if env.sensitive(p) {
		g := sens(p)
		g.prefix = prefixOfPath(env, p)
		return g
	}
	if !ok {
		return unk("unresolved path")
	}
	if inside {
		return partial{cat: "local-read", risk: RiskReadOnly, prefix: prefixOfPath(env, p), why: "sandbox read"}
	}
	return partial{cat: "local", risk: RiskLow, prefix: prefixOfPath(env, p), why: "read outside sandbox"}
}

func localDelete(env *shellEnv, p string) partial {
	if strings.TrimSpace(p) == "" {
		return unk("missing path")
	}
	if !looksLiteral(p) {
		return unk("unresolved path")
	}
	if criticalRaw(p) || env.criticalPath(p) || env.isDisk(p) || windowsCritical(p) {
		return crit("delete", "delete "+p)
	}
	return partial{cat: "delete", risk: RiskHigh, prefix: prefixOfPath(env, p), why: "local delete"}
}

func gradeReadPath(env *shellEnv, p string) partial {
	if strings.TrimSpace(p) == "" {
		return unk("missing path")
	}
	if env.sensitive(p) {
		g := sens(p)
		g.prefix = prefixOfPath(env, p)
		return g
	}
	if _, ok := env.resolve(p); !ok && !pathLike(p) && strings.TrimSpace(p) != "" && !strings.HasPrefix(p, "/") {
		// A relative literal still resolves when dir or home is set.
		if _, ok := env.resolve(p); !ok {
			return unk("unresolved path")
		}
	}
	if _, ok := env.resolve(p); !ok {
		return unk("unresolved path")
	}
	return partial{cat: "read", risk: RiskReadOnly, prefix: prefixOfPath(env, p), why: "read"}
}

func gradeFileWrite(env *shellEnv, p string, class writeClass) partial {
	if strings.TrimSpace(p) == "" {
		return unk("missing path")
	}
	g := env.gradeWrite(p, class)
	if g.prefix == "" {
		g.prefix = prefixOfPath(env, p)
	}
	return g
}

func prefixOfPath(env *shellEnv, p string) string {
	if abs, ok := env.resolve(p); ok {
		return dirPrefix(abs)
	}
	return p
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func looksLiteral(p string) bool {
	return !strings.Contains(p, "$") && !strings.Contains(p, "`")
}
