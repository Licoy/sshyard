package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func appIn(t *testing.T, dir string, secrets keychain.Backend) (*App, *ui.Tester) {
	t.Helper()
	if secrets == nil {
		secrets = keychain.NewMemory()
	}
	a, err := New(Platform{DataDir: dir, Secrets: secrets, HomeDir: dir, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.Start()
	a.withSecrets(nil)
	tt := ui.NewTester(a.View, 1200, 760)
	tt.SetLocale("en")
	tt.Frame()
	waitFor(t, tt, "credential store unlocked", a.unlocked)
	return a, tt
}

// rememberLayoutIn turns layout memory on, as Settings would, for apps
// started on dir; it defaults off.
func rememberLayoutIn(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"rememberLayout":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func savedHost(t *testing.T, a *App, name, addr string) assets.Host {
	t.Helper()
	h, err := a.Store().Add(assets.Host{Name: name, Address: addr, Port: 22, User: "root", Auth: assets.AuthAsk})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func openAIPane(t *testing.T, a *App, hostID string) int {
	t.Helper()
	id, err := a.OpenAI(hostID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func consentHost(t *testing.T, a *App, id string) {
	t.Helper()
	s := a.Settings()
	s.AIConsentHosts = append(append([]string{}, s.AIConsentHosts...), id)
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
}

func aiPaneOf(a *App, id int) *conn {
	c := a.conns[id]
	if c == nil || c.kind != layout.AI || c.ai == nil {
		return nil
	}
	return c
}

func TestAIShortcutMods(t *testing.T) {
	if isAppShortcut(nil, ui.KeyA, ui.Ctrl) || isAppShortcut(nil, ui.KeyB, ui.Ctrl) {
		t.Fatal("plain Ctrl+letter must reach the shell")
	}
	if isAppShortcut(nil, ui.KeyC, ui.Ctrl|ui.Shift) || isAppShortcut(nil, ui.KeyI, ui.Ctrl|ui.Shift) {
		t.Fatal("Ctrl+Shift+C and Ctrl+Shift+I belong to copy or the inspector")
	}
	if runtime.GOOS == "darwin" {
		if aiMods != ui.Super|ui.Shift {
			t.Fatalf("aiMods = %v, want ⌘⇧", aiMods)
		}
		if stopAllMods != ui.Super|ui.Shift {
			t.Fatalf("stopAllMods = %v, want ⌘⇧", stopAllMods)
		}
		if isAppShortcut(nil, ui.KeyA, ui.Super) {
			t.Fatal("⌘A is Select All and must reach the text field")
		}
		if !isAppShortcut(nil, ui.KeyA, ui.Super|ui.Shift) {
			t.Fatal("⌘⇧A must reach the app")
		}
		if !isAppShortcut(nil, ui.KeyPeriod, ui.Super) || !isAppShortcut(nil, ui.KeyPeriod, ui.Super|ui.Shift) {
			t.Fatal("⌘. and ⌘⇧. must reach the app")
		}
		return
	}
	if aiMods != ui.Ctrl|ui.Shift {
		t.Fatalf("aiMods = %v, want Ctrl+Shift", aiMods)
	}
	if stopAllMods != ui.Ctrl|ui.Shift {
		t.Fatalf("stopAllMods = %v, want Ctrl+Shift", stopAllMods)
	}
	if !isAppShortcut(nil, ui.KeyA, ui.Ctrl|ui.Shift) {
		t.Fatal("Ctrl+Shift+A must reach the app")
	}
	if !isAppShortcut(nil, ui.KeyPeriod, ui.Ctrl) || !isAppShortcut(nil, ui.KeyPeriod, ui.Ctrl|ui.Shift) {
		t.Fatal("Ctrl+. and Ctrl+Shift+. must reach the app")
	}
}

func TestStripInsert(t *testing.T) {
	if got := stripInsert("echo hi\r\n\t\x1b[A\x7f"); got != "echo hi[A" {
		t.Fatalf("%q", got)
	}
}

func TestOpenAIBesideTerminal(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	a, tt := newTestApp(t)
	term := connected(t, a, tt, srv)
	tt.Frame()
	tt.Key(aiMods, ui.KeyA)
	tt.Frame()
	ai := a.focused()
	if ai == nil || ai.kind != layout.AI || ai.s != nil || ai.ai == nil || ai.ai.link != term.id {
		t.Fatalf("focused %+v", ai)
	}
	if ai.host.ID != term.host.ID {
		t.Fatalf("host %s, terminal %s", ai.host.ID, term.host.ID)
	}
	tab := a.ws.ActiveTab()
	if tab.Count(layout.AI) != 1 || tab.Count(layout.Terminal) != 1 || len(a.ws.Tabs) != 1 {
		t.Fatalf("tabs %d panes %d", len(a.ws.Tabs), len(tab.Panes()))
	}
	// The assistant does not open its own SSH session.
	if ai.s != nil || ai.tv != nil {
		t.Fatal("AI pane dialed")
	}
}

func TestTwoAIPanesAreIndependent(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	a.newModel = func(*conn) agent.Streamer { return echoModel{} }
	id1, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A tab has one AI pane: the second is in a tab of its own.
	id2, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id1, "alpha question")
	a.SendAI(id2, "beta question")
	waitFor(t, tt, "both answers", func() bool {
		return paneHas(a, id1, "echo:alpha question") && paneHas(a, id2, "echo:beta question")
	})
	if a.conns[id1].ai.sess == a.conns[id2].ai.sess {
		t.Fatal("panes share a session")
	}
	a.CloseSession(id1)
	tt.Frame()
	if aiPaneOf(a, id1) != nil {
		t.Fatal("first pane still open")
	}
	if !paneHas(a, id2, "beta question") || !paneHas(a, id2, "echo:beta question") {
		t.Fatal("closing one pane changed the other")
	}
	if tab := a.ws.ActiveTab(); tab.Count(layout.AI) != 1 {
		t.Fatalf("AI panes left %d", tab.Count(layout.AI))
	}
}

func paneHas(a *App, id int, s string) bool {
	c := a.conns[id]
	if c == nil || c.ai == nil {
		return false
	}
	for _, it := range c.ai.items {
		if strings.Contains(it.text, s) || strings.Contains(it.tool.Command, s) || strings.Contains(it.tool.Output, s) {
			return true
		}
	}
	return false
}

func TestStartupRestoresSavedAILayout(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	dir := t.TempDir()
	rememberLayoutIn(t, dir)
	system := keychain.NewMemory()
	a, tt := appIn(t, dir, system)
	term := connected(t, a, tt, srv)
	tt.Key(aiMods, ui.KeyA)
	tt.Frame()
	ai := a.focused()
	if ai == nil || ai.kind != layout.AI {
		t.Fatal("AI pane missing")
	}
	ai.ai.items = append(ai.ai.items, aiItem{kind: "user", text: "private previous conversation"})
	a.Close()
	logins := srv.Logins()
	again, tt2 := appIn(t, dir, system)
	if len(again.ws.Tabs) != 1 || len(again.conns) != 2 {
		t.Fatalf("workspace not restored: %d %d", len(again.ws.Tabs), len(again.conns))
	}
	var restoredAI, restoredTerm *conn
	for _, c := range again.conns {
		if c.kind == layout.AI {
			restoredAI = c
		}
		if c.kind == layout.Terminal {
			restoredTerm = c
		}
	}
	if restoredAI == nil || restoredTerm == nil || restoredAI.ai.link != restoredTerm.id || len(restoredAI.ai.items) != 0 || restoredAI.ai.sess != nil {
		t.Fatal("AI link or empty session not restored")
	}
	if restoredTerm.host.ID != term.host.ID || startOnce(restoredTerm) != "" || restoredTerm.cwd != "" {
		t.Fatal("restored terminal content")
	}
	waitFor(t, tt2, "fresh startup connection", func() bool { return restoredTerm.s.Status().State == session.Connected })
	if srv.Logins() != logins+1 {
		t.Fatal("startup did not establish exactly one fresh connection")
	}
}

func TestAILayoutRestoresEmptySession(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	dir := t.TempDir()
	rememberLayoutIn(t, dir)
	system := keychain.NewMemory()
	a, tt := appIn(t, dir, system)
	term := connected(t, a, tt, srv)
	tt.Key(aiMods, ui.KeyA)
	tt.Frame()
	ai := a.focused()
	if ai == nil || ai.kind != layout.AI {
		t.Fatal("no AI pane")
	}
	ai.ai.mode = "review"
	ai.ai.agent = "other"
	ai.ai.provider = "prov1"
	ai.ai.items = append(ai.ai.items, aiItem{kind: "user", text: "secret question"})
	a.saveLayout()
	b, err := os.ReadFile(filepath.Join(dir, "layout.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret question") || !strings.Contains(string(b), `"kind": "terminal"`) || !strings.Contains(string(b), `"mode": "review"`) {
		t.Fatalf("layout %s", b)
	}
	a.Close()
	logins := srv.Logins()

	again, tt2 := appIn(t, dir, system)
	again.loadLayout()
	tt2.Frame()
	var got, restoredTerm *conn
	for _, c := range again.conns {
		if c.kind == layout.AI {
			got = c
		}
		if c.kind == layout.Terminal {
			restoredTerm = c
		}
	}
	if got == nil || got.ai.sess != nil || len(got.ai.items) != 0 || got.s != nil {
		t.Fatalf("restored AI %#v", got)
	}
	if got.ai.mode != "review" || got.ai.agent != "other" || got.ai.provider != "prov1" {
		t.Fatalf("restored fields %+v", got.ai)
	}
	if restoredTerm == nil || restoredTerm.host.ID != term.host.ID || got.ai.link != restoredTerm.id {
		t.Fatal("terminal association lost")
	}
	waitFor(t, tt2, "fresh layout connection", func() bool { return restoredTerm.s.Status().State == session.Connected })
	if srv.Logins() != logins+1 {
		t.Fatalf("logins %d to %d", logins, srv.Logins())
	}

}

func TestAILayoutDropsSavedFullMode(t *testing.T) {
	dir := t.TempDir()
	rememberLayoutIn(t, dir)
	system := keychain.NewMemory()
	a, _ := appIn(t, dir, system)
	h := savedHost(t, a, "web", "10.0.0.8")
	fullID, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	askID, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	a.conns[fullID].ai.mode = "full"
	a.conns[askID].ai.mode = "ask"
	a.saveLayout()
	a.Close()

	again, tt := appIn(t, dir, system)
	again.loadLayout()
	tt.Frame()
	var dropped, ask *conn
	for _, c := range again.conns {
		if c.kind != layout.AI || c.ai == nil {
			continue
		}
		if again.modeOf(c) == policy.ModeFull {
			t.Fatal("a restored pane is full access")
		}
		switch c.ai.mode {
		case "":
			dropped = c
		case "ask":
			ask = c
		default:
			t.Fatalf("restored mode %q", c.ai.mode)
		}
	}
	if dropped == nil || ask == nil {
		t.Fatalf("full dropped=%v ask kept=%v", dropped != nil, ask != nil)
	}
}

func TestLayoutSkipsTempHosts(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	dir := t.TempDir()
	rememberLayoutIn(t, dir)
	system := keychain.NewMemory()
	a, tt := appIn(t, dir, system)
	other := savedHost(t, a, "other", "10.0.0.8")
	if _, err := a.OpenAI(other.ID, openAt{}, 0); err != nil {
		t.Fatal(err)
	}
	a.temps["tmp-1"] = assets.Host{ID: "tmp-1", Name: "quick", Address: "10.9.9.9", Port: 22, User: "root", Auth: assets.AuthAsk}
	if _, err := a.OpenAI("tmp-1", openAt{}, 0); err != nil {
		t.Fatal(err)
	}
	term := connected(t, a, tt, srv)
	tt.Key(aiMods, ui.KeyA)
	tt.Frame()
	var termNode int
	for _, p := range a.ws.ActiveTab().Panes() {
		if p.Item.ID == term.id {
			termNode = p.ID
		}
	}
	if termNode == 0 {
		t.Fatal("terminal pane missing")
	}
	termTab := a.ws.Active
	if _, err := a.OpenAI(other.ID, openAt{}, 0); err != nil {
		t.Fatal(err)
	}
	a.ws.Active = termTab
	a.saveLayout()
	b, err := os.ReadFile(filepath.Join(dir, "layout.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tmp-") {
		t.Fatalf("layout recorded a temporary host: %s", b)
	}
	if !strings.Contains(string(b), term.host.ID) || !strings.Contains(string(b), other.ID) {
		t.Fatalf("layout dropped a saved host: %s", b)
	}
	a.Close()

	again, tt2 := appIn(t, dir, system)
	again.loadLayout()
	tt2.Frame()
	if len(again.ws.Tabs) != 3 {
		t.Fatalf("tabs %d", len(again.ws.Tabs))
	}
	var ai, restoredTerm *conn
	for _, c := range again.conns {
		if isTemp(c.host.ID) {
			t.Fatalf("restored temporary host %s", c.host.ID)
		}
		if c.host.ID == term.host.ID && c.kind == layout.AI {
			ai = c
		}
		if c.host.ID == term.host.ID && c.kind == layout.Terminal {
			restoredTerm = c
		}
	}
	if ai == nil || restoredTerm == nil || ai.ai.link != restoredTerm.id {
		t.Fatal("terminal association lost")
	}

	found := false
	for _, p := range again.ws.ActiveTab().Panes() {
		if c := again.conns[p.Item.ID]; c != nil && c.kind == layout.AI && c.host.ID == term.host.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("active tab is not the AI pane that sat with the terminal")
	}
}

// Legacy unversioned snapshots are protected without guessing terminal sessions.
func TestLayoutRestoreDoesNotDial(t *testing.T) {
	srvA, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srvA.Close()
	srvB, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srvB.Close()
	dir := t.TempDir()
	kc := keychain.NewMemory()
	a, _ := appIn(t, dir, kc)
	ha := addHost(t, a, srvA, "alpha", "secret")
	hb := addHost(t, a, srvB, "beta", "secret")
	if err := a.known.Add(srvA.Addr, srvA.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := a.known.Add(srvB.Addr, srvB.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	a.Close()

	snap := layoutSnap{
		Tabs: []tabSnap{
			{
				Focus: 1,
				Root: &nodeSnap{
					Split: "row", Ratio: 0.3,
					A: &nodeSnap{
						Split: "column", Ratio: 0.4,
						A: &nodeSnap{Kind: "terminal", Host: ha.ID},
						B: &nodeSnap{Kind: "ai", Host: ha.ID, Mode: "review", Agent: "other", Provider: "prov1"},
					},
					B: &nodeSnap{
						Split: "row", Ratio: 0.6,
						A: &nodeSnap{Kind: "files", Host: hb.ID},
						B: &nodeSnap{Kind: "ai", Host: hb.ID, Mode: "dropped"},
					},
				},
			},
			{Focus: 1, Root: &nodeSnap{Kind: "ai", Host: hb.ID, Mode: "ask"}},
			{Focus: 1, Root: &nodeSnap{Kind: "editor", Host: ha.ID, Path: "/etc/hostname"}},
		},
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// An old file had no version and may still name a terminal link.
	raw = []byte(strings.Replace(string(raw), `"version": 0,`, "", 1))
	raw = []byte(strings.ReplaceAll(string(raw), `"kind": "ai"`, `"kind": "ai", "link": 1`))
	if err := os.WriteFile(filepath.Join(dir, "layout.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	again, tt := appIn(t, dir, kc)
	again.loadLayout()
	tt.Frame()
	if len(again.ws.Tabs) != 0 || len(again.conns) != 0 || again.layoutBlocked {
		t.Fatal("legacy unversioned layout should not restore or block new recording")
	}
	preserved, err := os.ReadFile(filepath.Join(dir, "layout.json"))
	if err != nil || string(preserved) != string(append(raw, '\n')) {
		t.Fatal("legacy file changed")
	}

	h1, _ := again.host(ha.ID)
	h2, _ := again.host(hb.ID)
	deadline := time.Now().Add(400 * time.Millisecond)
	for !time.Now().After(deadline) {
		if srvA.Logins() != 0 || srvB.Logins() != 0 || again.pool.Shared(again.poolKey(h1)) != 0 || again.pool.Shared(again.poolKey(h2)) != 0 {
			t.Fatalf("dialed logins %d %d", srvA.Logins(), srvB.Logins())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func countExact(tt *ui.Tester, s string) int {
	n := 0
	for _, text := range tt.Texts() {
		if text == s {
			n++
		}
	}
	return n
}

func TestCorruptLayoutIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "layout.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ := appIn(t, dir, nil)
	if len(a.ws.Tabs) != 0 {
		t.Fatalf("%d tabs from a corrupt layout", len(a.ws.Tabs))
	}
	b, err := os.ReadFile(filepath.Join(dir, "layout.json"))
	if err != nil || string(b) != "{" {
		t.Fatalf("file %q err %v", b, err)
	}
}

func TestFullAccessNeedsConfirmation(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	tt.SetSize(1100, 1800)

	t.Run("pane", func(t *testing.T) {
		id, err := a.OpenAI(h.ID, openAt{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		pickFull := func() {
			mustClick(t, tt, "Permission")
			tt.Frame()
			mustClick(t, tt, "Full access")
			tt.Frame()
		}
		pickFull()
		if a.full == nil || !a.full.open || a.full.kind != "pane" {
			t.Fatalf("full access confirmation %+v", a.full)
		}
		mustClick(t, tt, "Turn On Full Access")
		tt.Frame()
		if a.conns[id].ai.mode == "full" {
			t.Fatal("full access without the checkbox")
		}
		if !tt.HasText("I understand all actions will run without confirmation") {
			pickFull()
		}
		mustClick(t, tt, "I understand all actions will run without confirmation")
		tt.Frame()
		mustClick(t, tt, "Turn On Full Access")
		tt.Frame()
		if a.conns[id].ai.mode != "full" || a.modeOf(a.conns[id]) != policy.ModeFull {
			t.Fatalf("mode %q resolved %q", a.conns[id].ai.mode, a.modeOf(a.conns[id]))
		}
		a.CloseSession(id)
		tt.Frame()
	})

	t.Run("settings", func(t *testing.T) {
		a.openSettings(prefAI)
		tt.Frame()
		mustClick(t, tt, "Full access")
		tt.Frame()
		mustClick(t, tt, "Turn On Full Access")
		tt.Frame()
		if a.Settings().AIDefaultMode == "full" {
			t.Fatal("settings full access without the checkbox")
		}
		if !tt.HasText("I understand all actions will run without confirmation") {
			mustClick(t, tt, "Full access")
			tt.Frame()
		}
		mustClick(t, tt, "I understand all actions will run without confirmation")
		tt.Frame()
		mustClick(t, tt, "Turn On Full Access")
		tt.Frame()
		if a.Settings().AIDefaultMode != "full" {
			t.Fatalf("default mode %q", a.Settings().AIDefaultMode)
		}
		a.page = pageWorkspace
		tt.Frame()
	})

	t.Run("host", func(t *testing.T) {
		a.openEditor(&h)
		a.form.aiOpen = true
		tt.Frame()
		tt.Advance(300 * time.Millisecond)
		tt.Frame()
		mustClick(t, tt, "Full access")
		tt.Frame()
		mustClick(t, tt, "Turn On Full Access")
		tt.Frame()
		if a.form == nil || a.form.aiIdx == 3 || a.form.fullOK {
			t.Fatalf("host form %+v", a.form)
		}
		if !tt.HasText("I understand all actions will run without confirmation") {
			mustClick(t, tt, "Full access")
			tt.Frame()
		}
		mustClick(t, tt, "I understand all actions will run without confirmation")
		tt.Frame()
		mustClick(t, tt, "Turn On Full Access")
		tt.Frame()
		if a.form == nil || !a.form.fullOK || a.form.aiIdx != 3 {
			t.Fatalf("after confirm %+v", a.form)
		}
		a.form.roots = "/srv/data\n/tmp"
		a.form.sens = "/etc/*.pem"
		mustClick(t, tt, "Save")
		tt.Frame()
		got, ok := a.Store().Get(h.ID)
		if !ok || got.AIPolicy.Mode != "full" || len(got.AIPolicy.WritableRoots) != 2 || got.AIPolicy.Sensitive[0] != "/etc/*.pem" {
			t.Fatalf("saved %+v", got.AIPolicy)
		}
	})
}

// Full access promises to stop asking, so an action the rules could not
// grade runs like any other and is only marked red.
func TestFullModeUnknownRunsRed(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	s := a.Settings()
	s.AIDefaultMode = "full"
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.newModel = func(*conn) agent.Streamer {
		return &replyModel{replies: []llm.Message{{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "run_command", Arguments: `{"command":"foobar"}`,
			}},
		}}}
	}
	a.newTools = func(*conn) agent.Tools { return diskTools{} }
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id, "run it")
	waitFor(t, tt, "unknown command ran", func() bool {
		for _, it := range a.conns[id].ai.items {
			if it.kind == "tool" && it.tool.Phase == "done" {
				return true
			}
		}
		return false
	})
	if pend := a.conns[id].ai.pending; pend != nil {
		t.Fatalf("full access asked before running an unknown command: %+v", pend.prompt)
	}
	var card agent.ToolCard
	for _, it := range a.conns[id].ai.items {
		if it.kind == "tool" {
			card = it.tool
		}
	}
	if card.Risk != "unknown" || card.Verdict != "full" || !card.Run {
		t.Fatalf("card %+v", card)
	}
	a.StopAI(id)
	waitFor(t, tt, "stopped", func() bool { return !a.conns[id].ai.running })
}

func TestReviewerParsesAllow(t *testing.T) {
	var got string
	r := aiReviewer{ask: func(_ context.Context, prompt string) (string, error) {
		got = prompt
		return `{"decision":"allow","reason":"read only"}`, nil
	}}
	allow, reason, err := r.Review(t.Context(), policy.ReviewInput{Command: "df -h"})
	if err != nil || !allow || reason != "read only" || !strings.Contains(got, "df -h") {
		t.Fatalf("allow %v reason %q err %v prompt %q", allow, reason, err, got)
	}
	r.ask = func(context.Context, string) (string, error) { return "sure, go ahead", nil }
	allow, _, err = r.Review(t.Context(), policy.ReviewInput{})
	if err == nil || allow {
		t.Fatalf("junk allow %v err %v", allow, err)
	}
}

func TestAIConfirmSheetAndStop(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	model := &scriptModel{}
	a.newModel = func(*conn) agent.Streamer { return model }
	a.newTools = func(*conn) agent.Tools { return diskTools{} }
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id, "run df")
	c := a.conns[id]
	waitFor(t, tt, "confirmation", func() bool { return c.ai.pending != nil })
	if c.ai.pending.prompt.Decision.Similar {
		t.Fatal("similar was offered")
	}
	if !tt.Focused("Reject") {
		t.Fatal("Reject is not focused")
	}
	tt.Key(stopMods, ui.KeyPeriod)
	waitFor(t, tt, "turn stopped", func() bool { return !c.ai.running && c.ai.pending == nil })
	if !paneHas(a, id, "cancelled") && !paneHas(a, id, "Cancel") {
		// The refusal is on the tool card.
		found := false
		for _, it := range c.ai.items {
			if it.kind == "tool" && (it.tool.Verdict == "cancel" || strings.Contains(it.tool.Refusal, "cancelled")) {
				found = true
			}
		}
		if !found {
			t.Fatalf("items %#v", c.ai.items)
		}
	}
}

func TestAIShortcutsAndStopAll(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	a.newModel = func(*conn) agent.Streamer { return &blockModel{done: make(chan struct{})} }
	id1, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id1, "hold")
	waitFor(t, tt, "running", func() bool { return a.conns[id1].ai.running })
	tt.Key(stopMods, ui.KeyPeriod)
	waitFor(t, tt, "stopped", func() bool { return !a.conns[id1].ai.running })

	id2, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id1, "hold again")
	a.SendAI(id2, "hold too")
	waitFor(t, tt, "both running", func() bool {
		return a.conns[id1].ai.running && a.conns[id2].ai.running
	})
	tt.Key(stopAllMods, ui.KeyPeriod)
	waitFor(t, tt, "shortcut stopped all", func() bool {
		return !a.conns[id1].ai.running && !a.conns[id2].ai.running
	})

	a.SendAI(id1, "hold again")
	a.SendAI(id2, "hold too")
	waitFor(t, tt, "both running again", func() bool {
		return a.conns[id1].ai.running && a.conns[id2].ai.running
	})
	if err := tt.RightClick("New Tab"); err != nil {
		t.Fatal(err)
	}
	if err := chooseMenu(a, tt, "Stop All AI"); err != nil {
		t.Fatalf("%v menu %q", err, menuOf(a))
	}
	waitFor(t, tt, "all stopped", func() bool {
		return !a.conns[id1].ai.running && !a.conns[id2].ai.running
	})
}

func TestInsertDoesNotExecute(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	a, tt := newTestApp(t)
	term := connected(t, a, tt, srv)
	tt.Key(aiMods, ui.KeyA)
	tt.Frame()
	ai := a.focused()
	a.InsertAI(ai.id, "echo hi\r\n")
	waitFor(t, tt, "echo hi", func() bool {
		return strings.Contains(term.s.Term.ScreenText(), "echo hi")
	})
	if strings.Contains(term.s.Term.ScreenText(), "you said") {
		t.Fatalf("command ran: %q", term.s.Term.ScreenText())
	}
}

func TestAskAIUsesRecentPane(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	a, tt := newTestApp(t)
	term := connected(t, a, tt, srv)
	consentHost(t, a, term.host.ID)
	a.newModel = func(*conn) agent.Streamer { return echoModel{} }
	tt.Frame()
	if err := tt.RightClick("Terminal box"); err != nil {
		t.Fatal(err)
	}
	if err := chooseMenu(a, tt, "Ask AI"); err == nil {
		t.Fatal("Ask AI was enabled with no selection")
	}
	tt.CloseMenu()
	tt.Key(aiMods, ui.KeyA)
	tt.Frame()
	if len(a.conns) != 2 {
		t.Fatalf("%d panes", len(a.conns))
	}
	term.tv.selected = true
	term.tv.selA = vt.Pos{Line: 0, Col: 0}
	term.tv.selB = vt.Pos{Line: 0, Col: 8}
	sel := term.tv.SelectedText()
	if strings.TrimSpace(sel) == "" {
		t.Fatalf("selection %q screen %q", sel, term.s.Term.ScreenText())
	}
	tt.Frame()
	if err := tt.RightClick("Terminal box"); err != nil {
		t.Fatal(err)
	}
	if err := chooseMenu(a, tt, "Ask AI"); err != nil {
		t.Fatalf("%v menu %q", err, menuOf(a))
	}
	ai := a.conns[a.aiRecent[term.host.ID]]
	waitFor(t, tt, "selection sent", func() bool {
		return ai.ai != nil && paneHas(a, ai.id, sel) && paneHas(a, ai.id, "untrusted-data")
	})
	if len(a.conns) != 2 {
		t.Fatalf("Ask AI opened a pane, %d conns", len(a.conns))
	}
}

func TestFirstUseConsentBlocksSend(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{{ID: "p1", Name: "fake", BaseURL: srv.URL + "/v1", Model: "fake-model"}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if err := a.kc.Set(keychain.AIKey("p1"), []byte("test-key-not-real")); err != nil {
		t.Fatal(err)
	}
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id, "hello")
	tt.Frame()
	if a.consent == nil || a.consent.host != h.ID {
		t.Fatalf("consent %+v", a.consent)
	}
	if n.Load() != 0 || len(a.conns[id].ai.items) != 0 {
		t.Fatalf("sent before consent: requests %d items %d", n.Load(), len(a.conns[id].ai.items))
	}
	mustClick(t, tt, "Cancel")
	tt.Frame()
	if n.Load() != 0 || a.consent != nil || len(a.conns[id].ai.items) != 0 {
		t.Fatal("cancel sent the question")
	}
	a.SendAI(id, "hello")
	tt.Frame()
	mustClick(t, tt, "Continue")
	waitFor(t, tt, "reply", func() bool { return paneHas(a, id, "pong") })
	if n.Load() == 0 {
		t.Fatal("consent did not send")
	}
}

func TestForgetAIConsent(t *testing.T) {
	a, tt := newTestApp(t)
	tt.SetSize(1400, 3600)
	h := savedHost(t, a, "web", "10.0.0.8")
	a.openSettings(prefAI)
	tt.Frame()
	consentHost(t, a, h.ID)
	consentHost(t, a, "missing-host")
	tt.Frame()
	mustClick(t, tt, "Forget AI consent for this host")
	tt.Frame()
	if hosts := a.Settings().AIConsentHosts; len(hosts) != 1 || hosts[0] != "missing-host" {
		t.Fatalf("hosts %v", hosts)
	}

	a.page = pageWorkspace
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id, "hello")
	tt.Frame()
	if a.consent == nil || a.consent.host != h.ID {
		t.Fatalf("consent %+v after it was forgotten", a.consent)
	}
	if len(a.conns[id].ai.items) != 0 {
		t.Fatal("sent after consent was forgotten")
	}
	mustClick(t, tt, "Cancel")
	tt.Frame()

	data := a.p.DataDir
	block := filepath.Join(data, "not-a-dir")
	if err := os.WriteFile(block, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.p.DataDir = block
	a.openSettings(prefAI)
	tt.Frame()
	mustClick(t, tt, "Forget AI consent for this host")
	tt.Advance(300 * time.Millisecond)
	if hosts := a.Settings().AIConsentHosts; len(hosts) != 1 || hosts[0] != "missing-host" {
		t.Fatalf("save error changed hosts %v", hosts)
	}
	b, err := os.ReadFile(filepath.Join(data, "settings.json"))
	if err != nil || !strings.Contains(string(b), "missing-host") {
		t.Fatalf("settings file %s %v", b, err)
	}
	if !tt.HasText("not-a-dir") {
		t.Fatalf("toast %q", tt.Texts())
	}
}

func TestBuiltinAgentDiskQuestion(t *testing.T) {
	const fakeKey = "test-key-not-real"
	var n atomic.Int32
	var sawKey atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.Header.Get("Authorization") == "Bearer "+fakeKey {
			sawKey.Store(true)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"role":"tool"`) {
			io.WriteString(w, `{"choices":[{"message":{"content":"4G used"},"finish_reason":"stop"}]}`)
			return
		}
		args, _ := json.Marshal(map[string]string{"command": "df -h", "reason": "disk"})
		resp := map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"tool_calls": []any{map[string]any{
						"id": "c1", "type": "function",
						"function": map[string]any{"name": "run_command", "arguments": string(args)},
					}},
				},
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{{ID: "pdisk", Name: "fake", BaseURL: srv.URL + "/v1", Model: "fake-model"}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if err := a.kc.Set(keychain.AIKey("pdisk"), []byte(fakeKey)); err != nil {
		t.Fatal(err)
	}
	a.newGate = func(func() policy.Mode, policy.Prompter) policy.Gate { return allowReadGate{} }
	a.newTools = func(*conn) agent.Tools { return diskTools{} }
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.SendAI(id, "how much disk is used")
	waitFor(t, tt, "disk answer", func() bool {
		return paneHas(a, id, "df -h") && paneHas(a, id, "Filesystem") && paneHas(a, id, "4G used")
	})
	if a.conns[id].ai.pending != nil || a.full != nil {
		t.Fatal("an approval or full access confirmation was asked")
	}
	if n.Load() < 2 || !sawKey.Load() {
		t.Fatalf("requests %d auth %v", n.Load(), sawKey.Load())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, fakeKey) {
			t.Fatal("the API key is on screen")
		}
	}
}

func TestAISettingsProviderKeychain(t *testing.T) {
	const fakeKey = "test-key-not-real"
	mem := keychain.NewMemory()
	secrets := &failDel{Backend: mem}
	a, tt := appIn(t, t.TempDir(), nil)
	if err := a.kc.Close(); err != nil {
		t.Fatal(err)
	}
	a.kc = keychain.New(secrets)
	tt.SetSize(1200, 3200)
	a.openSettings(prefAI)
	tt.Frame()
	mustClick(t, tt, "Add Provider")
	tt.Frame()
	if a.prov == nil {
		t.Fatal("provider sheet did not open")
	}
	a.prov.name = "fake"
	a.prov.base = "https://api.example.test/v1"
	a.prov.model = "fake-model"
	a.prov.key = fakeKey
	mustClick(t, tt, "Save")
	tt.Frame()
	if a.prov != nil || len(a.Settings().AIProviders) != 1 {
		t.Fatalf("saved %+v sheet %v", a.Settings().AIProviders, a.prov != nil)
	}
	p := a.Settings().AIProviders[0]
	if b, err := os.ReadFile(filepath.Join(a.p.DataDir, "settings.json")); err != nil || strings.Contains(string(b), fakeKey) {
		t.Fatalf("key in settings: %s %v", b, err)
	}
	got, err := a.kc.Get(keychain.AIKey(p.ID))
	if err != nil || string(got) != fakeKey {
		t.Fatalf("keychain %q %v", got, err)
	}

	// A failed keychain delete keeps the profile and the key.
	secrets.fail.Store(true)
	if err := a.deleteProvider(p.ID); err == nil {
		t.Fatal("delete reported success")
	}
	if a.providerByID(p.ID) == nil || !a.kc.Has(keychain.AIKey(p.ID)) {
		t.Fatal("failed delete dropped the provider or the key")
	}
	secrets.fail.Store(false)

	tt.Frame()
	mustClick(t, tt, "Delete")
	tt.Frame()
	if !a.provDeleting || a.provDeleteID != p.ID {
		t.Fatalf("delete confirmation %v for %q", a.provDeleting, a.provDeleteID)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.providerByID(p.ID) != nil || a.kc.Has(keychain.AIKey(p.ID)) {
		t.Fatal("delete left the provider or the key")
	}
}

func TestAIProviderRejectsBadURLAndHeader(t *testing.T) {
	m := locales.In("en")
	a, tt := newTestApp(t)
	tt.SetSize(1200, 3200)
	a.openSettings(prefAI)
	tt.Frame()
	mustClick(t, tt, "Add Provider")
	tt.Frame()
	a.prov.name, a.prov.base, a.prov.model, a.prov.key = "fake", "ftp://example.test/v1", "m", "test-key-not-real"
	mustClick(t, tt, "Save")
	tt.Frame()
	if a.prov.err != m.AiBadURL() || len(a.Settings().AIProviders) != 0 {
		t.Fatalf("providers %d error %q", len(a.Settings().AIProviders), a.prov.err)
	}
	a.prov.base = "http://example.test/v1"
	a.prov.headers = "Authorization: Bearer test-key-not-real"
	mustClick(t, tt, "Save")
	tt.Frame()
	if a.prov.err != m.AiBadHeader() || len(a.Settings().AIProviders) != 0 {
		t.Fatalf("header accepted, error %q", a.prov.err)
	}
	a.prov.headers = ""
	mustClick(t, tt, "Save")
	tt.Frame()
	if len(a.Settings().AIProviders) != 1 || a.prov == nil || a.prov.warn != m.AiHTTPWarn() {
		t.Fatalf("warn missing, providers %d form %+v", len(a.Settings().AIProviders), a.prov)
	}
}

type echoModel struct{}

func (echoModel) Stream(_ context.Context, req llm.Request, _ func(llm.Event)) (llm.Message, error) {
	var last string
	for _, m := range req.Messages {
		if m.Role == "user" {
			last = m.Content
		}
	}
	return llm.Message{Role: "assistant", Content: "echo:" + last}, nil
}

// scriptModel's first reply is a tool call; the rest are short text.
type scriptModel struct{ n int }

func (m *scriptModel) Stream(_ context.Context, _ llm.Request, _ func(llm.Event)) (llm.Message, error) {
	m.n++
	if m.n == 1 {
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID: "c1", Name: "write_file", Arguments: `{"path":"/etc/nginx.conf","content":"worker_processes 1;\n","reason":"edit"}`,
		}}}, nil
	}
	return llm.Message{Role: "assistant", Content: "stopped"}, nil
}

// replyModel returns the scripted messages, then a short close.
type replyModel struct {
	replies []llm.Message
	n       int
}

func (m *replyModel) Stream(_ context.Context, _ llm.Request, _ func(llm.Event)) (llm.Message, error) {
	if m.n >= len(m.replies) {
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	msg := m.replies[m.n]
	m.n++
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	return msg, nil
}

type blockModel struct{ done chan struct{} }

func (m *blockModel) Stream(ctx context.Context, _ llm.Request, _ func(llm.Event)) (llm.Message, error) {
	select {
	case <-ctx.Done():
		return llm.Message{}, ctx.Err()
	case <-m.done:
		return llm.Message{Content: "go"}, nil
	}
}

type allowReadGate struct{}

func (allowReadGate) Authorize(_ context.Context, a policy.Action, _ string) (policy.Verdict, error) {
	if a.Tool == "write_file" {
		return policy.Verdict{Run: false, Refusal: "no writes"}, nil
	}
	return policy.Verdict{Decision: policy.Decision{
		Grade:   policy.Grade{Category: "read", Risk: policy.RiskReadOnly},
		Mode:    policy.ModeAsk,
		Outcome: policy.Allow,
		By:      policy.ByRule,
	}, Run: true}, nil
}

func (allowReadGate) Done(policy.Action, policy.Verdict, int, int64, int64) {}

type diskTools struct{}

func (diskTools) Call(context.Context, string, json.RawMessage) (string, int, error) {
	return "Filesystem Size Used\n/dev/sda 10G 4G", 0, nil
}

func (diskTools) ReadFile(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// failDel is a keychain whose Delete fails while fail is set.
type failDel struct {
	keychain.Backend
	fail atomic.Bool
}

func (f *failDel) Delete(key string) error {
	if f.fail.Load() {
		return errors.New("keychain busy")
	}
	return f.Backend.Delete(key)
}

// TestOneAIPanePerTab: a tab has one AI pane. Opening another there shows
// it on the same host, takes the other host in its place, but not while
// it works; the rail's AI button is lit while there is one and closes it.
func TestOneAIPanePerTab(t *testing.T) {
	a, tt := newTestApp(t)
	ha := savedHost(t, a, "alpha", "10.0.0.8")
	hb := savedHost(t, a, "beta", "10.0.0.9")
	consentHost(t, a, hb.ID)
	block := &blockModel{done: make(chan struct{})}
	a.newModel = func(*conn) agent.Streamer { return block }
	first, err := a.OpenAI(ha.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tab := a.ws.ActiveTab()
	if id, err := a.OpenAI(ha.ID, openAt{pane: a.paneNode(first), edge: layout.Right}, 0); err != nil || id != first {
		t.Fatalf("same host gave %d (want %d), err %v", id, first, err)
	}
	second, err := a.OpenAI(hb.ID, openAt{pane: a.paneNode(first), edge: layout.Bottom}, 0)
	if err != nil || second == first || a.conns[first] != nil || tab.Count(layout.AI) != 1 || a.conns[second].host.ID != hb.ID {
		t.Fatalf("other host: pane %d, first left %v, %d AI panes, err %v", second, a.conns[first] != nil, tab.Count(layout.AI), err)
	}
	tt.Frame()
	a.SendAI(second, "hold")
	waitFor(t, tt, "running", func() bool { return a.conns[second].ai.running })
	if id, err := a.OpenAI(ha.ID, openAt{pane: a.paneNode(second), edge: layout.Right}, 0); err != errPaneBusy || id != second {
		t.Fatalf("a working pane was replaced: %d, err %v", id, err)
	}
	a.StopAI(second)
	waitFor(t, tt, "stopped", func() bool { return !a.conns[second].ai.running })
	mustClick(t, tt, "Close AI")
	tt.Frame()
	if a.conns[second] != nil {
		t.Fatal("the rail did not close the tab's AI pane")
	}
}
