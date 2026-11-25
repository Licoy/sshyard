package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
)

// A terminal not shown asks for one frame later however often its session
// changes meanwhile; a terminal shown asks at once.
func TestPaneWake(t *testing.T) {
	var w paneWake
	frames := 0
	invalidate := func() { frames++ }
	var later []func()
	after := func(d time.Duration, fn func()) {
		if d != hiddenWake {
			t.Fatalf("asked for a frame after %v", d)
		}
		later = append(later, fn)
	}
	for range 5 {
		w.ask(invalidate, after)
	}
	if frames != 0 || len(later) != 1 {
		t.Fatalf("hidden: %d frames now, %d later", frames, len(later))
	}
	later[0]()
	if frames != 1 {
		t.Fatalf("the frame asked for later: %d", frames)
	}
	w.ask(invalidate, after)
	if len(later) != 2 {
		t.Fatalf("a change after the frame came asked for %d", len(later)-1)
	}

	w.shown.Store(true)
	w.ask(invalidate, after)
	if frames != 2 || len(later) != 2 {
		t.Fatalf("shown: %d frames, %d later", frames, len(later))
	}
}

// Only the terminals of the tab shown count as shown, and they stop being
// as another tab shows.
func TestTerminalShownByTab(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	a, tt := newTestApp(t)
	h := addHost(t, a, srv, "box", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	connect := func() *conn {
		t.Helper()
		if _, err := a.Connect(h.ID, openAt{}); err != nil {
			t.Fatal(err)
		}
		c := a.focused()
		waitFor(t, tt, "connected", func() bool { return c.s.Status().State == session.Connected })
		return c
	}
	first := connect()
	tt.Frame()
	if !first.wake.shown.Load() {
		t.Fatal("the terminal shown is not")
	}
	second := connect()
	if len(a.ws.Tabs) != 2 {
		t.Fatalf("%d tabs", len(a.ws.Tabs))
	}
	tt.Frame()
	if first.wake.shown.Load() || !second.wake.shown.Load() {
		t.Fatalf("shown: first %v, second %v", first.wake.shown.Load(), second.wake.shown.Load())
	}
}

// The sidebar's hosts are grouped again once the store or the search
// changes, and only then.
func TestHostsViewFollowsStore(t *testing.T) {
	st, err := assets.Open("")
	if err != nil {
		t.Fatal(err)
	}
	web, err := st.Add(assets.Host{Name: "web", Address: "10.0.0.1", User: "u", Group: "Prod"})
	if err != nil {
		t.Fatal(err)
	}
	var v hostsView
	_, _, byGroup := v.get(st, "")
	if len(byGroup["Prod"]) != 1 {
		t.Fatalf("grouped %v", byGroup)
	}
	if _, _, again := v.get(st, ""); reflect.ValueOf(again).Pointer() != reflect.ValueOf(byGroup).Pointer() {
		t.Fatal("grouped again with nothing changed")
	}
	if _, err := st.Add(assets.Host{Name: "db", Address: "10.0.0.2", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, _, byGroup = v.get(st, ""); len(byGroup[""]) != 1 {
		t.Fatalf("a host added: %v", byGroup)
	}
	if err := st.SetFavorite(web.ID, true); err != nil {
		t.Fatal(err)
	}
	if favs, _, _ := v.get(st, ""); len(favs) != 1 || favs[0].ID != web.ID {
		t.Fatalf("favorites %v", favs)
	}
	if _, _, byGroup = v.get(st, "db"); len(byGroup["Prod"]) != 0 || len(byGroup[""]) != 1 {
		t.Fatalf("searched: %v", byGroup)
	}
}
