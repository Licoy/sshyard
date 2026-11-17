package policy

import "testing"

// TestRootEngine: a line typed into a terminal switched to root is graded
// as sudo would make it, at least Medium; what is graded higher keeps its
// grade, and other tools and a terminal no longer root are unchanged.
func TestRootEngine(t *testing.T) {
	root := true
	eng := NewRootEngine(func() bool { return root })
	scope := Scope{Home: "/home/ops"}
	send := func(cmd string) Grade {
		return eng.Classify(Action{Where: Remote, Tool: "terminal_send", Command: cmd}, scope)
	}
	if g := send("ls /root"); g.Risk != RiskMedium || g.Category != "permission" {
		t.Fatalf("a read as root: %+v", g)
	}
	if g := send("rm -rf /"); g.Risk != RiskCritical {
		t.Fatalf("a critical line as root: %+v", g)
	}
	if g := eng.Classify(Action{Where: Remote, Tool: "run_command", Command: "ls /root"}, scope); g.Risk != RiskReadOnly {
		t.Fatalf("run_command does not run as root: %+v", g)
	}
	if g := eng.Classify(Action{Where: Remote, Tool: "terminal_key", Command: "q"}, scope); g.Risk != RiskReadOnly {
		t.Fatalf("a pager key: %+v", g)
	}
	root = false
	if g := send("ls /root"); g.Risk != RiskReadOnly {
		t.Fatalf("back to the login user: %+v", g)
	}
}
