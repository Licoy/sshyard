package app

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/ai/acp"
	"github.com/Licoy/sshyard/internal/ai/acp/detect"
	"github.com/Licoy/sshyard/internal/aibrands"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// scanACPAgents finds the ACP agents on this computer; tests replace it.
var scanACPAgents = detect.Scan

// probeACP starts an agent only to read its model and thinking selectors;
// tests replace it.
var probeACP = acp.Probe

// acpProbeTimeout bounds a probe, which may first download the agent's
// adapter.
const acpProbeTimeout = 2 * time.Minute

type acpForm struct {
	// saved is the configuration as the sheet opened; added are the ids
	// of the detected agents kept, customs the commands the user typed.
	saved   []settings.ACPConfig
	added   map[string]bool
	customs []settings.ACPConfig
	// adding shows the fields of a new custom command.
	adding             bool
	command, args, err string
	agents             []detect.Agent
	scanning           bool
	hold               focusHold
}

func (a *App) openACP() {
	f := &acpForm{saved: slices.Clone(a.settings.AIACPAgents), added: map[string]bool{}}
	for _, cfg := range f.saved {
		if cfg.Agent != "" {
			f.added[cfg.Agent] = true
		} else {
			f.customs = append(f.customs, cfg)
		}
	}
	a.acpForm = f
	a.acpOpen = true
	a.scanACP(f, false)
}

// scanACP detects the agents off the UI thread; refresh reads the login
// shell again.
func (a *App) scanACP(f *acpForm, refresh bool) {
	if f.scanning {
		return
	}
	f.scanning = true
	a.acpWG.Add(1)
	go func() {
		defer a.acpWG.Done()
		agents := scanACPAgents(context.Background(), refresh)
		a.post(func() {
			if a.acpForm != f {
				return
			}
			f.agents, f.scanning = agents, false
			// Nothing configured yet: the first agent that can run is offered.
			if len(f.saved) == 0 && len(f.added) == 0 && len(f.customs) == 0 {
				for _, ag := range agents {
					if !ag.Missing {
						f.added[ag.ID] = true
						break
					}
				}
			}
		})
	}()
}

// addCustom keeps the command typed in the form.
func (f *acpForm) addCustom(m locales.Messages) bool {
	command := strings.TrimSpace(f.command)
	if command == "" || strings.ContainsRune(command, 0) || strings.ContainsRune(f.args, 0) {
		f.err = m.AiACPCommandRequired()
		return false
	}
	var args []string
	if f.args != "" {
		args = strings.Split(strings.ReplaceAll(f.args, "\r\n", "\n"), "\n")
	}
	f.customs = append(f.customs, settings.ACPConfig{Command: command, Args: args})
	f.command, f.args, f.adding, f.err = "", "", false, ""
	return true
}

func (a *App) saveACP(m locales.Messages) bool {
	f := a.acpForm
	if f.adding && strings.TrimSpace(f.command) != "" && !f.addCustom(m) {
		return false
	}
	prev := map[string]settings.ACPConfig{}
	for _, cfg := range f.saved {
		if cfg.Agent != "" {
			if _, ok := prev[cfg.Agent]; !ok {
				prev[cfg.Agent] = cfg
			}
		}
	}
	var list []settings.ACPConfig
	keep := func(id string) {
		cfg, ok := prev[id]
		if ok && cfg.Command != "" {
			list = append(list, cfg)
		}
		delete(prev, id)
	}
	for _, ag := range f.agents {
		if !f.added[ag.ID] {
			delete(prev, ag.ID)
			continue
		}
		if ag.Missing {
			keep(ag.ID)
			continue
		}
		cfg := prev[ag.ID]
		if cfg.ID == "" {
			cfg.ID = ag.ID
		}
		cfg.Agent, cfg.Command, cfg.Args = ag.ID, ag.Command, ag.Args
		list = append(list, cfg)
		delete(prev, ag.ID)
	}
	// An agent kept before that this scan did not find stays as it was.
	for _, cfg := range f.saved {
		if cfg.Agent != "" && f.added[cfg.Agent] {
			keep(cfg.Agent)
		}
	}
	list = append(list, f.customs...)
	s := a.settings
	s.AIACPAgents, s.AIACP = list, nil
	s.Normalize()
	if _, ok := s.ACP(s.AIDefaultAgent); settings.IsACP(s.AIDefaultAgent) && !ok {
		s.AIDefaultAgent = "builtin"
	}
	if err := a.ApplySettings(s); err != nil {
		f.err = m.AiSettingsSaveFailed()
		return false
	}
	// A changed command line may offer other models.
	a.stopACPProbes()
	a.acpOpen = false
	return true
}

// acpName names a configured agent: the detected agent, or the program a
// command the user typed runs.
func acpName(cfg settings.ACPConfig) string {
	if name := detect.Name(cfg.Agent); name != "" {
		return name
	}
	return filepath.Base(cfg.Command)
}

// acpNames are the configured agents' names, told apart by their ids
// where two share a name.
func acpNames(list []settings.ACPConfig) []string {
	count := map[string]int{}
	for _, cfg := range list {
		count[acpName(cfg)]++
	}
	out := make([]string, len(list))
	for i, cfg := range list {
		out[i] = acpName(cfg)
		if count[out[i]] > 1 {
			out[i] += " (" + cfg.ID + ")"
		}
	}
	return out
}

// acpSummary describes the configured ACP agents in a settings row.
func (a *App) acpSummary(m locales.Messages) string {
	if len(a.settings.AIACPAgents) == 0 {
		return m.AiACPConfigNote()
	}
	return strings.Join(acpNames(a.settings.AIACPAgents), ", ")
}

// commandLine shows an executable by its name, then its arguments.
func commandLine(command string, args []string) string {
	return strings.Join(append([]string{filepath.Base(command)}, args...), " ")
}

func (a *App) acpSheet(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	if a.acpForm == nil {
		return
	}
	f := a.acpForm
	sheet(ui.Modal(c.Key("acpconfig"), &a.acpOpen, func() {
		ui.Column(c).Gap(theme.Space(3)).Width(480).Children(func() {
			sheetTitle(c, m.AiACPConfigure())
			ui.Text(c, m.AiACPConfigNote()).FontSize(theme.Footnote).FillWidth().TextColor(tok.TextMuted)
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.AiACPDetected()).FontSize(theme.Footnote).FontWeight(600).TextColor(tok.TextMuted)
				ui.Spacer(c)
				if (IconButton{Icon: icons.Refresh, Label: m.AiACPDetectAgain(), Disabled: f.scanning, Size: Small}).Clicked(c, "acp:rescan") {
					a.scanACP(f, true)
				}
			})
			card(c, tok).FillWidth().Padding(theme.Space(1)).Gap(theme.Space(0.5)).Children(func() {
				switch {
				case f.scanning && len(f.agents) == 0:
					ui.Row(c).Padding(theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c).Size(14, 14)
						ui.Text(c, m.AiACPDetecting()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
					})
				case len(f.agents) == 0:
					ui.Text(c, m.AiACPNoneDetected()).FontSize(theme.Footnote).FillWidth().TextColor(tok.TextMuted).Padding(theme.Space(2))
				}
				for _, ag := range f.agents {
					a.acpAgentRow(c, m, tok, f, ag)
				}
			})
			ui.Text(c, m.AiACPCustom()).FontSize(theme.Footnote).FontWeight(600).TextColor(tok.TextMuted)
			if len(f.customs) > 0 {
				card(c, tok).FillWidth().Padding(theme.Space(1)).Gap(theme.Space(0.5)).Children(func() {
					for i, cfg := range f.customs {
						a.acpCustomRow(c, m, tok, f, i, cfg)
					}
				})
			}
			if f.adding {
				ui.Field(c, m.AiACPCommand(), func() {
					in := formTextInput(c.Key("acpcommand"), &f.command).Label(m.AiACPCommand()).AutoFocus()
					f.hold.apply(c, f, in)
				})
				ui.Field(c, m.AiACPArgs(), func() { formTextArea(c.Key("acpargs"), &f.args).Label(m.AiACPArgs()).FillWidth().Height(100) })
				ui.Text(c, m.AiACPArgsNote()).FontSize(theme.Caption).FillWidth().TextColor(tok.TextMuted)
				ui.Row(c).Gap(theme.Space(2)).Children(func() {
					if pill(c, m.AiACPAddCommand(), false, false, false) {
						f.addCustom(m)
					}
				})
			} else {
				ui.Row(c).Gap(theme.Space(2)).Children(func() {
					if pill(c, m.AiACPAddCustom(), false, false, false) {
						f.adding, f.err = true, ""
					}
				})
			}
			if f.err != "" {
				ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
			}
			ui.Row(c).Gap(theme.Space(2)).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.acpOpen = false
				}
				if pill(c, m.Save(), true, false, false) {
					a.saveACP(m)
				}
			})
		})
	}), tok)
	if !a.acpOpen {
		a.acpForm = nil
	}
}

func (a *App) acpAgentRow(c *ui.Context, m locales.Messages, tok theme.Tokens, f *acpForm, ag detect.Agent) {
	detail, tint := commandLine(ag.Command, ag.Args), ui.Color{}
	switch {
	case ag.Missing:
		detail, tint = m.AiACPNeedsAdapter(ag.Package), tok.Warning
	case ag.Fetch:
		detail = m.AiACPViaNpx()
	}
	on := f.added[ag.ID]
	// An agent missing its adapter can be taken off, not added.
	acpChoiceRow(c, tok, "acp:agent:"+ag.ID, ag.Name, ag.Version, detail, tint, on, ag.Missing && !on, func() {
		if icon := aibrands.Agent(ag.ID); icon != nil {
			brandImage(c, icon, 20)
		} else {
			softTile(c, icons.Robot, tok.TextMuted, 20)
		}
	}, func() { f.added[ag.ID], f.err = !on, "" })
}

func (a *App) acpCustomRow(c *ui.Context, m locales.Messages, tok theme.Tokens, f *acpForm, i int, cfg settings.ACPConfig) {
	ui.Row(c.Key("acp:custom:" + strconv.Itoa(i))).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(2.5)).AlignItems(ui.Center).Children(func() {
		softTile(c, icons.Terminal, tok.TextMuted, 20)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(theme.Space(0.5)).Children(func() {
			ui.Text(c, acpName(cfg)).FontSize(theme.Body).FontWeight(600).SingleLine()
			ui.Text(c, commandLine(cfg.Command, cfg.Args)).FontSize(theme.Caption).TextColor(tok.TextMuted).FillWidth()
		})
		if (IconButton{Icon: icons.Delete, Label: m.AiACPRemove(acpName(cfg)), Size: Small, Danger: true}).Clicked(c, "acp:remove:"+strconv.Itoa(i)) {
			f.customs = slices.Delete(slices.Clone(f.customs), i, i+1)
		}
	})
}

// acpChoiceRow is one agent of the ACP sheet's list: its icon, name,
// version and a line below; a click adds or takes it off.
func acpChoiceRow(c *ui.Context, tok theme.Tokens, key, name, version, detail string, tint ui.Color, on, disabled bool, lead, toggle func()) {
	row := ui.ButtonBase(c.Key(key)).Label(name).Checked(on).Disabled(disabled).FillWidth().
		Padding(theme.Space(2)).Gap(theme.Space(2.5)).AlignItems(ui.Center).Justify(ui.Start).Radius(theme.RadiusControl)
	switch {
	case on:
		row.Background(tok.Accent.Alpha(accentWash(tok)))
	case disabled:
		row.Opacity(0.7)
	case row.Hovered():
		row.Background(tok.Hover)
	}
	if !disabled {
		row.Cursor(ui.CursorPointer)
	}
	if tint == (ui.Color{}) {
		tint = tok.TextMuted
	}
	row.TextColor(tok.Text).Children(func() {
		lead()
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(theme.Space(0.5)).Children(func() {
			ui.Row(c).Gap(theme.Space(1.5)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).FontSize(theme.Body).FontWeight(600).SingleLine()
				if version != "" {
					ui.Text(c, version).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
				}
			})
			if detail != "" {
				ui.Text(c, detail).FontSize(theme.Caption).TextColor(tint).FillWidth()
			}
		})
		if on {
			ui.Icon(c, icons.Check.SVG).Size(16, 16).Shrink(0).TextColor(tok.Accent)
		}
	})
	if row.Clicked() && !disabled {
		toggle()
	}
}

func (a *App) agentChip(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fit aiFit) ui.Element {
	ch := chip{Icon: icons.Robot, Tip: m.AiBackend(), Label: m.AiBackendUnavailable(), Tint: tok.Warning}
	cur, curOK := a.settings.ACP(cn.ai.agent)
	switch {
	case cn.ai.agent == "builtin":
		ch.Label, ch.Tint = m.AiBuiltin(), ui.Color{}
	case curOK:
		ch.Label, ch.Tint = acpName(cur), ui.Color{}
		ch.Brand = aibrands.Agent(cur.Agent)
	}
	return a.menuChip(c, "aiagent:"+strconv.Itoa(cn.id), ch.fit(m, fit, 0), func() []menuEntry {
		builtin := cn.ai.agent == "builtin"
		out := []menuEntry{{Icon: icons.Robot, Label: m.AiBuiltin(), Checked: builtin, Disabled: !builtin && cn.ai.interrupting,
			Do: func() { a.setAIAgent(cn, "builtin") }}}
		names := acpNames(a.settings.AIACPAgents)
		for i, cfg := range a.settings.AIACPAgents {
			on := curOK && cur.ID == cfg.ID
			backend := "acp:" + cfg.ID
			// An external agent's tools reach SSH hosts only.
			e := menuEntry{Icon: icons.Robot, Label: names[i], Checked: on, Disabled: !on && (!hasSSH(cn.host) || cn.ai.interrupting), Do: func() {
				if !on {
					a.setAIAgent(cn, backend)
				}
			}}
			if icon := aibrands.Agent(cfg.Agent); icon != nil {
				e.Image, e.DarkImage = icon.Light(), icon.Dark()
			}
			out = append(out, e)
		}
		return append(out, menuSep, menuEntry{Icon: icons.Settings, Label: m.AiACPConfigure(), Do: a.openACP})
	})
}

// acpProbe is what a probe of one configured agent found: its selectors
// as the agent starts, before any choice is applied.
type acpProbe struct {
	opts    []acp.Option
	running bool
	failed  bool
	cancel  context.CancelFunc
}

// acpOptions are the selectors an ACP pane shows: its session's while it
// has one, else the agent's as last seen with the choices kept for it.
// The probe is nil while the session's are shown or nothing is known.
func (a *App) acpOptions(cn *conn, cfg settings.ACPConfig) ([]acp.Option, *acpProbe) {
	if cn.ai.acp != nil && cn.ai.acpOpts != nil {
		return cn.ai.acpOpts, nil
	}
	p := a.acpProbes[cfg.ID]
	if p == nil {
		return nil, nil
	}
	out := make([]acp.Option, len(p.opts))
	for i, o := range p.opts {
		if v, ok := cfg.Options[o.ID]; ok {
			if _, offered := o.Value(v); offered {
				o.Current = v
			}
		}
		out[i] = o
	}
	return out, p
}

// setACPOptions takes the selectors a pane's session reports, which are
// also what a new pane of that agent shows until its session starts.
func (a *App) setACPOptions(c *conn, opts []acp.Option) {
	if opts == nil {
		opts = []acp.Option{}
	}
	c.ai.acpOpts = opts
	cfg, ok := a.settings.ACP(c.ai.agent)
	if !ok || len(opts) == 0 {
		return
	}
	if p := a.acpProbes[cfg.ID]; p != nil && p.cancel != nil {
		p.cancel()
	}
	if a.acpProbes == nil {
		a.acpProbes = map[string]*acpProbe{}
	}
	a.acpProbes[cfg.ID] = &acpProbe{opts: opts}
}

// startACPProbe reads an agent's selectors off the UI thread.
func (a *App) startACPProbe(cfg settings.ACPConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), acpProbeTimeout)
	p := &acpProbe{running: true, cancel: cancel}
	if a.acpProbes == nil {
		a.acpProbes = map[string]*acpProbe{}
	}
	a.acpProbes[cfg.ID] = p
	a.acpWG.Add(1)
	go func() {
		defer a.acpWG.Done()
		defer cancel()
		options := acp.Config{Command: cfg.Command, Args: slices.Clone(cfg.Args), Type: detect.Type(cfg.Agent), Redact: true}
		locateACP(ctx, cfg.Agent, &options)
		opts, err := probeACP(ctx, options)
		a.post(func() {
			if a.acpProbes[cfg.ID] == p {
				p.opts, p.running, p.failed, p.cancel = opts, false, err != nil, nil
			}
		})
	}()
}

// stopACPProbes forgets every agent's selectors and stops the probes
// still running.
func (a *App) stopACPProbes() {
	for _, p := range a.acpProbes {
		if p.cancel != nil {
			p.cancel()
		}
	}
	a.acpProbes = nil
}

// acpOptionChips show an ACP pane's model and thinking level and choose
// others, among the values the agent offers. Before the agent has said
// what it offers, opening the model menu asks it.
func (a *App) acpOptionChips(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fit aiFit) {
	cfg, ok := a.settings.ACP(cn.ai.agent)
	if !ok {
		return
	}
	opts, probe := a.acpOptions(cn, cfg)
	model, hasModel := firstOption(opts, acp.CategoryModel)
	ch := chip{Icon: icons.Model, Tip: m.AiModel(), Label: m.AiACPAgentDefault()}
	if hasModel {
		ch.Label = optionName(model)
		ch.Brand = acpModelBrand(model.Current)
	}
	a.menuChip(c, "acpmodel:"+strconv.Itoa(cn.id), ch.fit(m, fit, aiModelShort), func() []menuEntry {
		if hasModel {
			return a.acpOptionEntries(cn, cfg, model, true)
		}
		switch {
		case cn.ai.acp != nil && cn.ai.acpOpts != nil:
		case probe == nil:
			a.startACPProbe(cfg)
			return []menuEntry{{Label: m.AiACPLoadingOptions(), Disabled: true}}
		case probe.running:
			return []menuEntry{{Label: m.AiACPLoadingOptions(), Disabled: true}}
		case probe.failed:
			return []menuEntry{{Label: m.AiACPOptionsFailed(), Disabled: true}, {Icon: icons.Refresh, Label: m.AiACPRetryOptions(), Do: func() { a.startACPProbe(cfg) }}}
		}
		return []menuEntry{{Label: m.AiACPNoOptions(), Disabled: true}}
	})
	if thought, ok := firstOption(opts, acp.CategoryThought); ok {
		tc := chip{Icon: icons.Thinking, Tip: m.AiThinkingLevel(), Label: optionName(thought)}.meter(tok, thought.Current).fit(m, fit, 0)
		a.menuChip(c, "acpeffort:"+strconv.Itoa(cn.id), tc, func() []menuEntry {
			return a.acpOptionEntries(cn, cfg, thought, false)
		})
	}
}

func (a *App) acpOptionEntries(cn *conn, cfg settings.ACPConfig, o acp.Option, brands bool) []menuEntry {
	out := make([]menuEntry, 0, len(o.Values))
	for _, v := range o.Values {
		e := menuEntry{Label: v.Name, Note: v.Description, Checked: v.Value == o.Current, Do: func() { a.chooseACPOption(cn, cfg.ID, o.ID, v.Value) }}
		if brands {
			if icon := acpModelBrand(v.Value); icon != nil {
				e.Image, e.DarkImage = icon.Light(), icon.Dark()
			}
		}
		out = append(out, e)
	}
	return out
}

// chooseACPOption keeps value for the agent's selector, which every new
// session of the agent then starts with, and sets it in the pane's own
// session when it has one.
func (a *App) chooseACPOption(cn *conn, agentID, optID, value string) {
	s := a.settings
	s.AIACPAgents = slices.Clone(s.AIACPAgents)
	for i := range s.AIACPAgents {
		if s.AIACPAgents[i].ID == agentID {
			opts := maps.Clone(s.AIACPAgents[i].Options)
			if opts == nil {
				opts = map[string]string{}
			}
			opts[optID] = value
			s.AIACPAgents[i].Options = opts
		}
	}
	if err := a.ApplySettings(s); err != nil {
		a.acpOptionFailed()
	}
	r := cn.ai.acp
	if r == nil || cn.ai.acpOpts == nil {
		return
	}
	// Shown at once; the agent's answer replaces it.
	cn.ai.acpOpts = slices.Clone(cn.ai.acpOpts)
	for i := range cn.ai.acpOpts {
		if cn.ai.acpOpts[i].ID == optID {
			cn.ai.acpOpts[i].Current = value
		}
	}
	id := cn.id
	a.acpWG.Add(1)
	go func() {
		defer a.acpWG.Done()
		<-r.ready
		if r.session == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opts, err := r.session.SetOption(ctx, optID, value)
		a.post(func() {
			current := a.conns[id]
			if current == nil || current.ai == nil || current.ai.acp != r {
				return
			}
			if err != nil {
				a.acpOptionFailed()
				return
			}
			a.setACPOptions(current, opts)
		})
	}()
}

func (a *App) acpOptionFailed() {
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
		return ui.Toast{Title: m.AiACPOptionFailed(), Type: "error"}
	})
}

func firstOption(opts []acp.Option, category string) (acp.Option, bool) {
	for _, o := range opts {
		if o.Category == category {
			return o, true
		}
	}
	return acp.Option{}, false
}

// optionName is the name of the option's current value.
func optionName(o acp.Option) string {
	if v, ok := o.Value(o.Current); ok {
		return v.Name
	}
	return o.Current
}

// acpModelBrand is the brand of a model an agent names, if its name has one.
func acpModelBrand(model string) *aibrands.Icon {
	if icon := aibrands.Model(model, settings.AIProvider{}); icon != aibrands.Provider(settings.AIProvider{}) {
		return icon
	}
	return nil
}
