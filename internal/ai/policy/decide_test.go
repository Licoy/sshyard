package policy

import "testing"

func TestDecide(t *testing.T) {
	eng := NewEngine()
	scope := Scope{Home: "/home/user"}
	grade := func(cmd string) Grade {
		t.Helper()
		return eng.Classify(Action{Where: Remote, Tool: "run_command", Command: cmd}, scope)
	}
	type tc struct {
		name     string
		mode     Mode
		grade    Grade
		reviewer bool
		outcome  Outcome
		by       Decider
		similar  bool
		diff     bool
		red      bool
	}
	ro := grade("ls")
	low := grade("mkdir -p /tmp/x")
	write := grade("echo hi > /etc/x")
	sens := grade("cat /etc/shadow")
	install := grade("apt-get install nginx")
	svc := grade("systemctl restart nginx")
	del := grade("rm -rf /tmp/x")
	perm := grade("chmod 644 /tmp/a")
	net := grade("iptables -L")
	crit := grade("rm -rf /")
	unk := grade("foobar")
	curl := grade("curl http://example.com")
	localLow := Grade{Category: "local", Risk: RiskLow, Prefix: "/tmp"}
	pager := grade("git -c core.pager='rm -rf /' log")
	localUnk := eng.Classify(Action{Where: Local, Tool: "execute", Command: "foobar"}, Scope{Sandbox: t.TempDir()})

	cases := []tc{
		{"ask readonly", ModeAsk, ro, false, Allow, ByRule, false, false, false},
		{"ask low", ModeAsk, low, false, Ask, ByRule, false, true, false},
		{"ask write no similar", ModeAsk, write, false, Ask, ByRule, false, true, false},
		{"ask sensitive similar", ModeAsk, sens, false, Ask, ByRule, true, false, false},
		{"ask install", ModeAsk, install, false, Ask, ByRule, true, false, false},
		{"ask service", ModeAsk, svc, false, Ask, ByRule, true, false, false},
		{"ask delete", ModeAsk, del, false, Ask, ByRule, false, false, false},
		{"ask permission", ModeAsk, perm, false, Ask, ByRule, false, false, false},
		{"ask network high", ModeAsk, net, false, Ask, ByRule, false, false, false},
		{"ask curl similar", ModeAsk, curl, false, Ask, ByRule, true, false, false},
		{"ask critical red", ModeAsk, crit, false, Ask, ByRule, false, false, true},
		{"ask unknown", ModeAsk, unk, false, Ask, ByRule, false, false, false},
		{"ask local low", ModeAsk, localLow, false, Ask, ByRule, true, false, false},
		{"ask invalid mode", Mode(""), ro, false, Allow, ByRule, false, false, false},
		{"ask invalid on write", Mode("nope"), write, false, Ask, ByRule, false, true, false},

		{"review readonly", ModeReview, ro, true, Allow, ByRule, false, false, false},
		{"review readonly no reviewer", ModeReview, ro, false, Allow, ByRule, false, false, false},
		{"review mkdir with reviewer", ModeReview, low, true, Review, ByRule, true, true, false},
		{"review mkdir no reviewer", ModeReview, low, false, Allow, ByRule, false, true, false},
		{"review write records diff", ModeReview, write, true, Review, ByRule, true, true, false},
		{"review sensitive", ModeReview, sens, true, Review, ByRule, true, false, false},
		{"review service with reviewer", ModeReview, svc, true, Review, ByRule, true, false, false},
		{"review service no reviewer", ModeReview, svc, false, Ask, ByRule, true, false, false},
		{"review install no reviewer", ModeReview, install, false, Ask, ByRule, true, false, false},
		{"review delete with reviewer", ModeReview, del, true, Review, ByRule, false, false, false},
		{"review delete no reviewer", ModeReview, del, false, Ask, ByRule, false, false, false},
		{"review permission", ModeReview, perm, true, Review, ByRule, false, false, false},
		{"review network", ModeReview, net, true, Review, ByRule, false, false, false},
		{"review critical asks the user", ModeReview, crit, true, Ask, ByRule, false, false, true},
		{"review unknown", ModeReview, unk, true, Review, ByRule, false, false, false},
		{"review local low with reviewer", ModeReview, localLow, true, Review, ByRule, true, false, false},

		{"full readonly", ModeFull, ro, false, Allow, ByFull, false, false, false},
		{"full service", ModeFull, svc, false, Allow, ByFull, false, false, false},
		{"full delete", ModeFull, del, false, Allow, ByFull, false, false, false},
		{"full unknown runs red", ModeFull, unk, false, Allow, ByFull, false, false, true},
		{"full local unknown runs red", ModeFull, localUnk, false, Allow, ByFull, false, false, true},
		{"full write diff", ModeFull, write, false, Allow, ByFull, false, true, false},
		{"full critical allows red", ModeFull, crit, true, Allow, ByFull, false, false, true},
		{"full low", ModeFull, low, false, Allow, ByFull, false, true, false},
		{"ask git pager", ModeAsk, pager, false, Ask, ByRule, false, false, false},
		{"review git pager", ModeReview, pager, true, Review, ByRule, false, false, false},
		{"full git pager allows", ModeFull, pager, false, Allow, ByFull, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := eng.Decide(c.mode, c.grade, c.reviewer)
			if d.Outcome != c.outcome || d.By != c.by || d.Similar != c.similar || d.Diff != c.diff || d.Red != c.red {
				t.Fatalf("got outcome=%s by=%s similar=%v diff=%v red=%v; want outcome=%s by=%s similar=%v diff=%v red=%v (grade %s %s)",
					d.Outcome, d.By, d.Similar, d.Diff, d.Red, c.outcome, c.by, c.similar, c.diff, c.red, c.grade.Category, c.grade.Risk)
			}
		})
	}
}
