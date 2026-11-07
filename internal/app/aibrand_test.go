package app

import (
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/aibrands"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func TestProviderDefaultURLSwitch(t *testing.T) {
	a, tt := newTestApp(t)
	tt.SetSize(1200, 1600)
	a.openProvider(nil)
	tt.Frame()
	tt.Advance(time.Second)
	m := locales.In("en")
	for _, i := range []int{2, 3, 1, 0} {
		labels := []string{m.AiFormatChat(), m.AiFormatResponses(), m.AiFormatAnthropic(), m.AiFormatTypeSafe()}
		mustClick(t, tt, labels[i])
		tt.Frame()
		if a.prov.base != providerURLHint(i) || inputText(t, tt, m.AiProviderURL()) != providerURLHint(i) {
			t.Fatalf("format %d bound %q texts %q", i, a.prov.base, tt.Texts())
		}
	}
	a.prov.base = "https://custom.example/v1"
	tt.Frame()
	mustClick(t, tt, m.AiFormatAnthropic())
	tt.Frame()
	if a.prov.base != "https://custom.example/v1" || inputText(t, tt, m.AiProviderURL()) != a.prov.base {
		t.Fatalf("custom URL changed: %q", a.prov.base)
	}
	p := settings.AIProvider{ID: "old", Format: settings.AIFormatAnthropic, BaseURL: "https://existing.example/v1"}
	a.openProvider(&p)
	tt.Frame()
	if a.prov.base != p.BaseURL {
		t.Fatalf("existing changed %q", a.prov.base)
	}
}

func TestAIModelMenu(t *testing.T) {
	a, tt := newTestApp(t)
	h := savedHost(t, a, "brands", "10.0.0.8")
	s := a.Settings()
	s.AIProviders = []settings.AIProvider{{ID: "p", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Model: "gpt-5.1", Models: []string{"claude-sonnet-4", "unknown-model"}}}
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	id, err := a.OpenAI(h.ID, openAt{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// One provider has no group to name first, so its models are the menu.
	mustClick(t, tt, "Model")
	tt.Frame()
	if tt.HasText("OpenAI") {
		t.Fatalf("a lone provider was listed as a group: %q", tt.Texts())
	}
	mustClick(t, tt, "claude-sonnet-4")
	tt.Frame()
	if a.conns[id].ai.model != "claude-sonnet-4" {
		t.Fatalf("model selection: %q", a.conns[id].ai.model)
	}
	mustClick(t, tt, "Model")
	tt.Frame()
	mustClick(t, tt, "unknown-model")
	tt.Frame()
	if a.conns[id].ai.model != "unknown-model" {
		t.Fatalf("unknown model selection: %q", a.conns[id].ai.model)
	}
}

// openModelSubmenu moves the pointer onto a provider's entry so that its
// models show in the submenu, as the menu does when a pointer comes on it.
func openModelSubmenu(t *testing.T, a *App, tt *ui.Tester, provider string) {
	t.Helper()
	r := mustFind(t, tt, provider)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Frame()
	if a.menu.sub < 0 {
		t.Fatalf("%q's models did not open: %q", provider, tt.Texts())
	}
}

func inputText(t *testing.T, tt *ui.Tester, label string) string {
	t.Helper()
	r := mustFind(t, tt, label)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H-14)
	tt.Command("selectAll")
	tt.Command("copy")
	return tt.Clipboard()
}

func TestBrandControlsMouseChoices(t *testing.T) {
	p := settings.AIProvider{BaseURL: "https://api.openai.com/v1"}
	value, selected := "", "first"
	submitted := ""
	models := []string{"gpt-5.1", "claude-sonnet-4", "unknown-model"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(theme.Space(4)).Gap(theme.Space(4)).Children(func() {
			if brandAutocomplete(c.Key("model"), &value, models, p).Label("Model ID").Width(240).Submitted() {
				submitted = value
			}
			brandSelect(c.Key("select"), &selected, []string{"first", "second", "third"}, func(string) *aibrands.Icon { return aibrands.Provider(p) }).Label("Provider").Width(240)
		})
	}, 500, 500)
	tt.Frame()
	mustClick(t, tt, "Model ID")
	tt.Frame()
	mustClick(t, tt, models[1])
	tt.Frame()
	if value != models[1] {
		t.Fatalf("second model picked %q", value)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != value {
		t.Fatalf("editor %q differs from selected %q", tt.Clipboard(), value)
	}
	mustClick(t, tt, "Model ID")
	tt.Frame()
	mustClick(t, tt, models[2])
	tt.Frame()
	if value != models[2] {
		t.Fatalf("third model picked %q", value)
	}
	mustClick(t, tt, "Model ID")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("custom-model-id")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if submitted != "custom-model-id" {
		t.Fatalf("typed model submit %q value %q", submitted, value)
	}
	for _, want := range []string{"second", "third"} {
		mustClick(t, tt, selected)
		tt.Frame()
		mustClick(t, tt, want)
		tt.Frame()
		if selected != want {
			t.Fatalf("selected %q want %q", selected, want)
		}
	}
}
