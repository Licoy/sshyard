package app

import (
	"testing"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/Licoy/sshyard/locales"
)

func TestHostTermType(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	a, tt := newTestApp(t)
	m := locales.In("en")
	h := addHost(t, a, srv, "box", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())

	// A TERM the host cannot be given is refused, and nothing is saved.
	a.openEditor(&h)
	a.form.term, a.form.termCustom = "xterm 256color", true
	if a.SaveForm(m) || a.form.err != m.TermTypeInvalid() {
		t.Fatalf("saved an invalid TERM: %q", a.form.err)
	}
	if got, _ := a.Store().Get(h.ID); got.Term != "" {
		t.Fatalf("stored %q", got.Term)
	}
	a.form.term = " linux "
	if !a.SaveForm(m) {
		t.Fatal(a.form.err)
	}
	h, _ = a.Store().Get(h.ID)
	if h.Term != "linux" {
		t.Fatalf("stored %q", h.Term)
	}

	// The host's TERM wins over the setting, which applies without one.
	a.settings.TerminalType = "vt100"
	connectTerm := func(want string) *conn {
		t.Helper()
		if _, err := a.Connect(h.ID, openAt{}); err != nil {
			t.Fatal(err)
		}
		c := a.focused()
		waitFor(t, tt, "the "+want+" pty", func() bool {
			return c.s.Status().State == session.Connected && srv.LastPTY().Term == want
		})
		return c
	}
	if c := connectTerm("linux"); !c.s.Term.Modes().LinuxKeys {
		t.Error("a linux terminal sends xterm keys")
	}
	h.Term = ""
	if _, err := a.Store().Update(h); err != nil {
		t.Fatal(err)
	}
	if c := connectTerm("vt100"); c.s.Term.Modes().LinuxKeys {
		t.Error("a vt100 terminal sends the Linux console's keys")
	}
}
