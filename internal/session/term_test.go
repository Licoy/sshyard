package session

import "testing"

func TestTermOK(t *testing.T) {
	for term, want := range map[string]bool{
		"xterm-256color": true, "linux": true, "vt100": true, "screen.xterm-256color": true, "xterm+256color": true, "x_y": true,
		"": false, "-xterm": false, "xterm 256": false, "xterm\n": false, "TERM=x": false, "ünicode": false,
		string(make([]byte, 65)): false,
	} {
		if got := TermOK(term); got != want {
			t.Errorf("TermOK(%q) = %v, want %v", term, got, want)
		}
	}
	long := "a"
	for len(long) < 64 {
		long += "a"
	}
	if !TermOK(long) || TermOK(long+"a") {
		t.Error("64 characters is the limit")
	}
}

func TestNewTerm(t *testing.T) {
	for _, tc := range []struct {
		term, want string
		linux      bool
	}{
		{"", DefaultTerm, false},
		{"bad\nterm", DefaultTerm, false},
		{"vt100", "vt100", false},
		{"linux", "linux", true},
		{"linux-16color", "linux-16color", true},
		{"linuxish", "linuxish", false},
	} {
		s := New(Config{}, Options{Term: tc.term}, 80, 24)
		if s.opts.Term != tc.want || s.Term.Modes().LinuxKeys != tc.linux {
			t.Errorf("Term %q: requests %q, Linux keys %v; want %q, %v", tc.term, s.opts.Term, s.Term.Modes().LinuxKeys, tc.want, tc.linux)
		}
	}
}

func TestShellRequestsTerm(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()}, Options{Term: "linux"}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	waitText(t, s, "Welcome")
	if p := srv.LastPTY(); p.Term != "linux" {
		t.Errorf("pty %+v", p)
	}
}
