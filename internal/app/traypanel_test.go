package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
)

func TestTrayHostGroupingAndSearch(t *testing.T) {
	st, err := assets.Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"Production", "Empty"} {
		if err := st.AddGroup(group); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []assets.Host{
		{Name: "Primary", Address: "primary.example", User: "root", Group: "Production", Favorite: true},
		{Name: "Console", Protocol: assets.ProtoTelnet, Address: "console.example"},
	} {
		if _, err := st.Add(h); err != nil {
			t.Fatal(err)
		}
	}
	favs, groups, byGroup := hostsBy(st, "")
	if len(favs) != 1 || len(groups) != 2 || len(byGroup["Production"]) != 1 || len(byGroup[""]) != 1 {
		t.Fatalf("unexpected grouping: %v %v %v", favs, groups, byGroup)
	}
	favs, groups, byGroup = hostsBy(st, "primary")
	if len(favs) != 1 || len(groups) != 1 || groups[0] != "Production" || len(byGroup[""]) != 0 {
		t.Fatalf("unexpected search: %v %v %v", favs, groups, byGroup)
	}
	_, groups, byGroup = hostsBy(st, "missing")
	if len(groups) != 0 || len(byGroup) != 0 {
		t.Fatalf("empty search retained groups: %v %v", groups, byGroup)
	}
}

func TestTrayConnectionRejectsUnavailableHosts(t *testing.T) {
	a := &App{winState: testWin()}
	if err := a.ConnectFromTray("missing"); !errors.Is(err, keychain.ErrLocked) {
		t.Fatalf("storage unavailable: %v", err)
	}
	st, err := assets.Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.store = st
	a.unlock.ready.Store(true)
	for _, id := range []string{"missing", localID, "tmp:unknown"} {
		if err := a.ConnectFromTray(id); !errors.Is(err, assets.ErrNotFound) {
			t.Fatalf("%q: %v", id, err)
		}
	}
}

func TestTrayConnectionDeferredAndOpensMainFirst(t *testing.T) {
	pump := newDispatchPump()
	st, err := assets.Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{winState: testWin(), p: Platform{Post: pump.post}, store: st}
	a.unlock.ready.Store(true)
	calls := 0
	panel := a.NewTrayPanel(func(action TrayAction) {
		if action != TrayOpenMain || len(a.toasts) != 0 {
			t.Fatal("connection ran before main opened")
		}
		calls++
	})
	panel.connect("deleted")
	if calls != 0 || len(a.toasts) != 0 {
		t.Fatal("tray handler ran inline")
	}
	pump.next(t)
	if calls != 1 || len(a.toasts) != 1 {
		t.Fatalf("dispatch/error not delivered: %d %d", calls, len(a.toasts))
	}
}

func TestTrayConnectionPreservesProtocolAndSecretGate(t *testing.T) {
	b := &authorizationBackend{Memory: keychain.NewMemory()}
	a, _ := lockedTestApp(t, b)
	t.Cleanup(func() {
		// This test marks an unlock busy without starting its cleanup worker.
		a.unlock.mu.Lock()
		a.unlock.busy = false
		a.unlock.mu.Unlock()
		a.Close()
		kc, err := a.openSecrets()
		if err != nil {
			t.Fatalf("credential store lock was not released: %v", err)
		}
		if err := kc.Close(); err != nil {
			t.Fatalf("close reopened credential store: %v", err)
		}
	})
	a.unlock.stored = true
	// A pending explicit unlock must hold all sessions without bypassing it.
	a.unlock.busy = true
	for _, proto := range []assets.Protocol{assets.ProtoSSH, assets.ProtoTelnet, assets.ProtoSerial} {
		h := assets.Host{Name: "saved", Address: "localhost", User: "test", Auth: assets.AuthPassword, Protocol: proto}
		if proto == assets.ProtoSerial {
			h.Serial = &assets.SerialLine{Device: "/dev/sshyard-tray-missing", Baud: 9600, DataBits: 8, StopBits: "1"}
		}
		saved, err := a.store.Add(h)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.ConnectFromTray(saved.ID); err != nil {
			t.Fatal(err)
		}
		cn := a.focused()
		if cn == nil || cn.host.ID != saved.ID || cn.host.Protocol != proto || !cn.waitingSecrets {
			t.Fatalf("protocol %q bypassed normal session/unlock path: %+v", proto, cn)
		}
	}
	if b.calls.Load() != 0 || a.unlocked() {
		t.Fatal("tray metadata/held connection accessed saved secrets")
	}
}

func TestTrayPanelStateAndActionsAreIndependent(t *testing.T) {
	b := &authorizationBackend{Memory: keychain.NewMemory()}
	a, _ := lockedTestApp(t, b)
	first, second := a.NewTrayPanel(func(TrayAction) {}), a.NewTrayPanel(func(TrayAction) {})
	first.search = "query"
	*first.section("g:test") = false
	if second.search != "" || !*second.section("g:test") || a.search != "" {
		t.Fatal("tray state leaked between windows")
	}
	a.RunTrayAction(TraySettings)
	if a.page != pageSettings {
		t.Fatal("settings action not handled")
	}
	a.RunTrayAction(TrayOpenMain)
	if a.page != pageWorkspace {
		t.Fatal("main action not handled")
	}
	a.RunTrayAction(TrayQuickConnect)
	if !a.quicking {
		t.Fatal("quick connect action not handled")
	}
	a.RunTrayAction(TrayNewHost)
	if !a.editing {
		t.Fatal("new host action not handled")
	}
	if a.unlocked() || b.calls.Load() != 0 {
		t.Fatal("metadata actions authorized secrets")
	}
}
