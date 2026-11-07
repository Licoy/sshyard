package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/egoist/mygo/ui"
)

// TestAIPaneLayout checks the pane's controls: the backend in its header,
// and the composer's text on top with, under it, permission on the left
// and the model, thinking level and send button on the right.
func TestAIPaneLayout(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{
		{ID: "p1", Name: "Alpha", Model: "alpha-1", BaseURL: "https://alpha.test/v1"},
		{ID: "p2", Name: "Beta", Model: "beta-2", Models: []string{"beta-1", "beta-2", "beta-mini"}, BaseURL: "https://beta.test/v1"},
	}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, gone := range []string{"Linked terminal", "No terminal", "Agent", "Alpha"} {
		if tt.HasText(gone) {
			t.Fatalf("%q still shows", gone)
		}
	}
	head := mustFind(t, tt, "Close Pane")
	backend := mustFind(t, tt, "Built-in")
	if backend.Y > head.Y+head.H || backend.Y+backend.H < head.Y {
		t.Fatal("backend not in header")
	}
	perm := mustFind(t, tt, "Permission")
	input := mustFind(t, tt, "Ask about this host")
	model := mustFind(t, tt, "Model")
	effort := mustFind(t, tt, "Thinking level")
	send := mustFind(t, tt, "Send")
	for name, r := range map[string]ui.Rect{"permission": perm, "model": model, "thinking": effort, "send": send} {
		if r.Y < input.Y+input.H {
			t.Fatalf("%s %v not under the input %v", name, r, input)
		}
	}
	if !(perm.X < model.X && model.X < effort.X && effort.X < send.X) || send.X+send.W > input.X+input.W+8 {
		t.Fatalf("permission %v model %v thinking %v send %v not in order", perm, model, effort, send)
	}
	if !tt.HasText("alpha-1") || !tt.HasText("Auto") {
		t.Fatalf("chips lack the model or level: %q", tt.Texts())
	}

	mustClick(t, tt, "Model")
	tt.Frame()
	// The first level names the providers; their models live one level down.
	for _, s := range []string{"Alpha", "Beta", "Manage Providers…"} {
		if !tt.HasText(s) {
			t.Fatalf("model menu lacks %q: %q", s, tt.Texts())
		}
	}
	for _, s := range []string{"beta-1", "beta-2", "beta-mini"} {
		if tt.HasText(s) {
			t.Fatalf("model %q is listed outside its provider: %q", s, tt.Texts())
		}
	}
	if a.conns[id].ai.provider != "p1" {
		t.Fatalf("with several providers the pane starts on %q, not the first", a.conns[id].ai.provider)
	}
	tt.Type("mini")
	tt.Frame()
	// alpha-1 stays on the chip, but not in the menu.
	if countExact(tt, "alpha-1") != 1 || tt.HasText("beta-1") || tt.HasText("Alpha") || !tt.HasText("beta-mini") {
		t.Fatalf("search does not filter: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	cn := a.conns[id]
	if cn.ai.provider != "p2" || cn.ai.model != "beta-mini" || a.menu.key != "" || !tt.HasText("beta-mini") {
		t.Fatalf("provider %q model %q menu %q", cn.ai.provider, cn.ai.model, a.menu.key)
	}

	mustClick(t, tt, "Model")
	tt.Frame()
	openModelSubmenu(t, a, tt, "Beta")
	mustClick(t, tt, "beta-2")
	tt.Frame()
	if cn.ai.provider != "p2" || cn.ai.model != "" {
		t.Fatalf("the default is kept as %q of %q", cn.ai.model, cn.ai.provider)
	}

	mustClick(t, tt, "Model")
	tt.Frame()
	tt.Type("beta-x")
	tt.Frame()
	mustClick(t, tt, "Use “beta-x”")
	tt.Frame()
	if cn.ai.provider != "p2" || cn.ai.model != "beta-x" {
		t.Fatalf("typed model %q of %q", cn.ai.model, cn.ai.provider)
	}

	mustClick(t, tt, "Thinking level")
	tt.Frame()
	for _, s := range []string{"Off", "Low", "Medium", "High", "Extra high"} {
		if !tt.HasText(s) {
			t.Fatalf("thinking menu lacks %q: %q", s, tt.Texts())
		}
	}
	mustClick(t, tt, "High")
	tt.Frame()
	if cn.ai.effort != "high" || !tt.HasText("High") {
		t.Fatalf("effort %q", cn.ai.effort)
	}

	mustClick(t, tt, "Permission")
	tt.Frame()
	for _, s := range []string{"Asks you before every action", "Read-only actions run; the configured model reviews the rest", "Every action runs without asking"} {
		if !tt.HasText(s) {
			t.Fatalf("permission menu lacks %q: %q", s, tt.Texts())
		}
	}
	mustClick(t, tt, "Review")
	tt.Frame()
	if cn.ai.mode != "review" || a.modeOf(cn) != policy.ModeReview || a.full != nil {
		t.Fatalf("mode %q", cn.ai.mode)
	}

	mustClick(t, tt, "Model")
	tt.Frame()
	mustClick(t, tt, "Manage Providers…")
	tt.Frame()
	if a.page != pageSettings || a.pref != prefAI {
		t.Fatalf("page %v pref %v", a.page, a.pref)
	}
}

// TestAIPaneLinksTabTerminal checks that a pane works with the terminal
// of its host in its own tab, and with none in a tab without one.
func TestAIPaneLinksTabTerminal(t *testing.T) {
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	a, tt := newTestApp(t)
	term := connected(t, a, tt, srv)
	tab := a.ws.ActiveTab()
	beside, err := a.OpenAI(term.host.ID, openAt{pane: tab.Focus, edge: layout.Right}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.conns[beside].ai.link; got != term.id {
		t.Fatalf("link %d, terminal %d", got, term.id)
	}
	alone, err := a.OpenAI(term.host.ID, openAt{}, term.id)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ws.ActiveTab() == tab {
		t.Fatal("the pane opened in the terminal's tab")
	}
	if got := a.conns[alone].ai.link; got != 0 {
		t.Fatalf("a pane in another tab linked %d", got)
	}
}

// TestAIPaneMarkdownAndSend checks that replies show as Markdown with
// their code's buttons, that Enter sends, and that the send button stops
// a turn while it runs.
func TestAIPaneMarkdownAndSend(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "web", "10.0.0.8")
	consentHost(t, a, h.ID)
	model := &blockModel{done: make(chan struct{})}
	a.newModel = func(*conn) agent.Streamer { return model }
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	cn := a.conns[id]
	cn.ai.items = []aiItem{{kind: "text", text: "Disk is **almost full**.\n\n```sh\ndf -h\n```\n"}}
	tt.Frame()
	if !tt.HasText("almost full") || tt.HasText("**almost full**") {
		t.Fatalf("not Markdown: %q", tt.Texts())
	}
	if !tt.HasText("df -h") || countExact(tt, "Copy") == 0 {
		t.Fatalf("code block: %q", tt.Texts())
	}
	mustClick(t, tt, "Copy")
	tt.Frame()
	if tt.Clipboard() != "df -h" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	if tt.HasText("Insert into terminal") {
		t.Fatal("insert shows without a terminal")
	}

	cn.ai.items = nil
	tt.Frame()
	mustClick(t, tt, "Ask about this host")
	tt.Type("how full")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("is the disk")
	tt.Frame()
	if cn.ai.running || cn.ai.input != "how full\nis the disk" {
		t.Fatalf("Shift+Enter: running %v input %q", cn.ai.running, cn.ai.input)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !cn.ai.running || !paneHas(a, id, "how full\nis the disk") || cn.ai.input != "" {
		t.Fatalf("Enter did not send: running %v items %+v", cn.ai.running, cn.ai.items)
	}
	if tt.HasText("Send") || !tt.HasText("Stop") || !tt.HasText("Working…") {
		t.Fatalf("running composer: %q", tt.Texts())
	}
	mustClick(t, tt, "Stop")
	waitFor(t, tt, "stopped", func() bool { return !cn.ai.running })
	tt.Frame()
	if !tt.HasText("Send") || strings.Contains(strings.Join(tt.Texts(), "\n"), "Working…") {
		t.Fatalf("after stop: %q", tt.Texts())
	}
}

// TestAIDefaultProvider checks that a pane asks the provider set as the
// default, else the first, and that a star in the settings sets it.
func TestAIDefaultProvider(t *testing.T) {
	a, tt := newTestApp(t)
	tt.SetSize(1200, 2400)
	h := savedHost(t, a, "web", "10.0.0.8")
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{
		{ID: "p1", Name: "Alpha", Model: "alpha-1", BaseURL: "https://alpha.test/v1"},
		{ID: "p2", Name: "Beta", Model: "beta-2", BaseURL: "https://beta.test/v1"},
	}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.openSettings(prefAI)
	tt.Frame()
	if countExact(tt, "Default provider") == 0 || countExact(tt, "Set as Default Provider") == 0 {
		t.Fatalf("stars: %q", tt.Texts())
	}
	mustClick(t, tt, "Set as Default Provider")
	tt.Frame()
	if got := a.Settings().AIDefaultProvider; got != "p2" {
		t.Fatalf("default %q", got)
	}
	a.page = pageWorkspace
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p := a.paneProvider(a.conns[id]); p == nil || p.ID != "p2" || !tt.HasText("beta-2") {
		t.Fatalf("pane provider %+v", p)
	}
	if err := a.deleteProvider("p2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Settings().AIDefaultProvider != "" {
		t.Fatalf("deleted default kept: %q", a.Settings().AIDefaultProvider)
	}
	if p := a.paneProvider(a.conns[id]); p != nil {
		t.Fatalf("after delete %+v", p)
	}
}

// TestAIPaneNarrow: in a narrow AI pane the choices show as their icons,
// each naming what is chosen in its tooltip, and the split buttons go
// into a menu; a wide one labels them.
func TestAIPaneNarrow(t *testing.T) {
	a, tt := newTestApp(t)
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{{ID: "p", Name: "Lab", BaseURL: "http://127.0.0.1:1", Model: "lab-model"}}
	s.AIDefaultMode = "review"
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	h := savedHost(t, a, "web", "10.0.0.8")
	if _, err := a.OpenAI(h.ID, openAt{}, 0); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	for _, s := range []string{"Review", "lab-model", "Built-in", "Split Right"} {
		if !tt.HasText(s) {
			t.Errorf("wide pane misses %q: %q", s, tt.Texts())
		}
	}
	tt.SetSize(540, 600) // a pane under aiHeadMenuWidth
	tt.Frame()
	tt.Frame()
	if w := a.paneBounds[a.focused().id].W; w >= aiHeadMenuWidth {
		t.Fatalf("pane %v wide", w)
	}
	for _, s := range []string{"Review", "lab-model", "Built-in", "Split Right"} {
		if slices.Contains(tt.Texts(), s) {
			t.Errorf("narrow pane shows %q: %q", s, tt.Texts())
		}
	}
	for _, s := range []string{"Permission: Review", "Model: lab-model", "Thinking level: Auto", "Assistant backend: Built-in", "More Actions"} {
		if !tt.HasText(s) {
			t.Errorf("narrow pane misses %q: %q", s, tt.Texts())
		}
	}
	mustClick(t, tt, "More Actions")
	tt.Frame()
	mustClick(t, tt, "Split Right")
	tt.Frame()
	if n := a.ws.ActiveTab().Count(layout.Terminal); n != 1 {
		t.Fatalf("split from the menu opened %d terminals", n)
	}
}

// TestModelMenuGroupsProviders: the model menu names the providers first,
// each opening its own models in a submenu, so the models of different
// providers never share one flat list. A search still lists its matches
// directly, since the query has already narrowed them.
func TestModelMenuGroupsProviders(t *testing.T) {
	a, tt := newTestApp(t)
	tt.SetSize(1200, 2400)
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{
		{ID: "p1", Name: "Alpha", Model: "alpha-1", Models: []string{"alpha-1", "alpha-2"}, BaseURL: "https://alpha.test/v1"},
		{ID: "p2", Name: "Beta", Model: "beta-1", Models: []string{"beta-1", "beta-2"}, BaseURL: "https://beta.test/v1"},
	}
	s.AIDefaultProvider = "p1"
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	h := savedHost(t, a, "web", "10.0.0.8")
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	mustClick(t, tt, "alpha-1")
	tt.Frame()
	if a.menu.key == "" {
		t.Fatal("the model menu did not open")
	}
	if !tt.HasText("Alpha") || !tt.HasText("Beta") {
		t.Fatalf("providers not listed first: %q", tt.Texts())
	}
	if tt.HasText("alpha-2") || tt.HasText("beta-1") || tt.HasText("beta-2") {
		t.Fatal("models were listed outside their provider")
	}
	openModelSubmenu(t, a, tt, "Beta")
	mustClick(t, tt, "beta-2")
	tt.Frame()
	if cn := a.conns[id]; cn.ai.provider != "p2" || cn.ai.model != "beta-2" {
		t.Fatalf("chose %q %q", cn.ai.provider, cn.ai.model)
	}
}
