package app

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/aibrands"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// aiPane is an AI pane: its header, with the model it asks, the timeline
// of the conversation, and the composer, with the permission it runs under.
// It works with the terminal of its host in its tab (syncLink).
func (a *App) aiPane(c *ui.Context, t *layout.Tab, n *layout.Node, cn *conn, tok theme.Tokens, m locales.Messages) {
	a.refreshMode(cn)
	a.syncLink(t, cn)
	if t.Focus == n.ID {
		a.aiRecent[cn.host.ID] = cn.id
	}
	if cn.ai.md == nil {
		cn.ai.md = a.aiMarkdown(cn)
	}
	// The pane's width as last laid out, 0 before: its controls give way
	// to it, labels first.
	w := a.paneBounds[cn.id].W
	headFit := aiFull
	if w > 0 && w < aiHeadWidth {
		headFit = aiIcons
	}
	head := ui.Column(c.Key("aihead")).Shrink(0).FillWidth()
	head.Children(func() {
		row := ui.Row(c.Key("head")).Height(paneHeadHeight).Shrink(0).Padding(0, 6, 0, 10).Gap(8).AlignItems(ui.Center).Drag(dragSession{ID: cn.id}).Label(m.PaneActions())
		row.Children(func() {
			softTile(c, kindIcon(layout.AI, t.Focus == n.ID), hostTint(tok, a.liveHost(cn.host)), paneHeadIcon)
			title := ui.Text(c, m.AiPane(a.pv(cn.host.Label()))).FontWeight(600).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
			if t.Focus != n.ID && len(t.Panes()) > 1 {
				title.TextColor(tok.TextMuted)
			}
			if isTemp(cn.host.ID) {
				tempBadge(c, m, tok)
			}
			ui.Spacer(c).Shrink(1)
			a.agentChip(c, cn, tok, m, headFit)
			split := func(edge layout.Edge) func() {
				return func() {
					t.Focus = n.ID
					a.SplitFocused(edge)
				}
			}
			empty := len(cn.ai.items) == 0
			if (IconButton{Icon: icons.NewChat, Label: m.AiNewChat(), Size: Tiny, Disabled: cn.ai.running || empty}).Clicked(c, "newchat") {
				a.NewChat(cn.id)
			}
			a.historyButton(c, cn, tok, m)
			narrow := w > 0 && w < aiHeadMenuWidth
			if !narrow {
				if (IconButton{Icon: icons.SplitRight, Label: m.SplitRight(), Size: Tiny}).Clicked(c, "splitright") {
					split(layout.Right)()
				}
				if (IconButton{Icon: icons.SplitDown, Label: m.SplitDown(), Size: Tiny}).Clicked(c, "splitdown") {
					split(layout.Bottom)()
				}
			}
			a.menuButton(c, "aimore:"+strconv.Itoa(cn.id), IconButton{Icon: icons.More, Label: m.MoreActions(), Size: Tiny}, func() []menuEntry {
				var es []menuEntry
				if narrow {
					es = append(es,
						menuEntry{Icon: icons.SplitRight, Label: m.SplitRight(), Do: split(layout.Right)},
						menuEntry{Icon: icons.SplitDown, Label: m.SplitDown(), Do: split(layout.Bottom)},
						menuSep)
				}
				return append(es, menuEntry{Icon: icons.Clear, Label: m.AiClearChat(), Danger: true, Disabled: cn.ai.running || empty, Do: func() {
					if cn.ai.conv == "" {
						a.ClearChat(cn.id)
						return
					}
					cn.ai.drop = cn.ai.conv
				}})
			})
			if (IconButton{Icon: icons.Close, Label: m.ClosePane(), Size: Tiny, Danger: true}).Clicked(c, "close") {
				a.closeSoon(cn.id)
			}
		})
		if row.Clicked() {
			t.Focus = n.ID
			a.focusReq = cn.id
			a.aiRecent[cn.host.ID] = cn.id
		}
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	})
	a.chatConfirm(c, cn, m)
	a.aiTimeline(c, cn, tok, m)
	a.aiComposer(c, cn, tok, m, composerFit(w))
}

// aiFit is how much of an AI pane's controls show at its width: all of
// them labelled; the model's name cut short and the other choices as
// their icons; or every choice as its icon alone.
type aiFit int

const (
	aiFull aiFit = iota
	aiShort
	aiIcons
)

// Widths of an AI pane: from aiFullWidth its composer's choices all show
// their labels, from aiShortWidth the model's; under aiHeadWidth its
// header shows the backend as an icon, under aiHeadMenuWidth its split
// buttons go into its menu.
const (
	aiFullWidth     = 472
	aiShortWidth    = 342
	aiHeadWidth     = 420
	aiHeadMenuWidth = 360
)

// composerFit is how much of the composer's controls show in a pane w
// wide, 0 for not laid out yet.
func composerFit(w float32) aiFit {
	switch {
	case w == 0 || w >= aiFullWidth:
		return aiFull
	case w >= aiShortWidth:
		return aiShort
	}
	return aiIcons
}

// aiModelShort is the widest the model's name shows at aiShort.
const aiModelShort = 110

// providerName is the short name of a provider: its name, else its model.
func providerName(p settings.AIProvider) string {
	switch {
	case p.Name != "":
		return p.Name
	case p.Model != "":
		return p.Model
	}
	return p.ID
}

// effortLevels are the thinking levels a pane chooses from, in the order
// its menu lists them; "" leaves the provider's default.
var effortLevels = []string{"", llm.EffortNone, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh}

func effortName(m locales.Messages, e string) string {
	switch e {
	case llm.EffortNone:
		return m.AiEffortNone()
	case llm.EffortLow:
		return m.AiEffortLow()
	case llm.EffortMedium:
		return m.AiEffortMedium()
	case llm.EffortHigh:
		return m.AiEffortHigh()
	case llm.EffortXHigh:
		return m.AiEffortXHigh()
	}
	return m.AiEffortAuto()
}

// effortChip shows the pane's thinking level and chooses another. The
// Anthropic format cannot turn thinking off, so it offers no "off".
func (a *App) effortChip(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fit aiFit) {
	p := a.paneProvider(cn)
	cur := ""
	if p != nil {
		cur = paneEffort(cn, p)
	}
	ch := chip{Icon: icons.Thinking, Label: effortName(m, cur), Tip: m.AiThinkingLevel()}.fit(m, fit, 0)
	a.menuChip(c, "aieffort:"+strconv.Itoa(cn.id), ch, func() []menuEntry {
		out := make([]menuEntry, 0, len(effortLevels))
		for _, e := range effortLevels {
			if e == llm.EffortNone && p != nil && p.Format == settings.AIFormatAnthropic {
				continue
			}
			e := e
			entry := menuEntry{Label: effortName(m, e), Checked: e == cur, Do: func() {
				cn.ai.effort = e
				a.saveLayout()
			}}
			if e == "" {
				entry.Note = m.AiEffortAutoNote()
			}
			out = append(out, entry)
		}
		return out
	})
}

// modelChoice is one model in the model menu, of provider p.
type modelChoice struct {
	p     *settings.AIProvider
	model string
}

// modelChoices are the models of each provider matching query, the
// provider's default first, in the order the settings list providers.
func (a *App) modelChoices(query string) [][]modelChoice {
	query = strings.ToLower(strings.TrimSpace(query))
	var out [][]modelChoice
	for i := range a.settings.AIProviders {
		p := &a.settings.AIProviders[i]
		if !p.CanChat() {
			continue
		}
		var group []modelChoice
		seen := map[string]bool{}
		for _, id := range append([]string{p.Model}, p.Models...) {
			if id == "" || seen[id] || !strings.Contains(strings.ToLower(id), query) {
				continue
			}
			seen[id] = true
			group = append(group, modelChoice{p: p, model: id})
		}
		out = append(out, group)
	}
	return out
}

// chooseModel makes the pane ask model of provider p. Another provider
// starts a new session; another model of the same one keeps it.
func (a *App) chooseModel(cn *conn, p *settings.AIProvider, model string) {
	if cn.ai.provider != p.ID && !(cn.ai.provider == "" && a.defaultProvider() == p.ID) {
		a.invalidateAI(cn)
	}
	cn.ai.provider = p.ID
	cn.ai.model = model
	if model == p.Model {
		cn.ai.model = ""
	}
	a.saveLayout()
}

// modelMenuMax is the most models one provider lists before a search.
const modelMenuMax = 200

// modelMenuWidth is the width of the model menu and of each provider's
// submenu, wide enough for a long model id beside its check.
const modelMenuWidth = 300

// modelChip shows the pane's model and chooses another, of any provider,
// from a menu of providers, each opening its own models: a search field
// above them takes the first match on Enter, or the name typed when
// nothing matches. Without a provider it shows in the warning color.
func (a *App) modelChip(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fit aiFit) {
	key := "aimodel:" + strconv.Itoa(cn.id)
	p := a.paneProvider(cn)
	ch := chip{Icon: icons.Model, Label: m.AiNoProvider(), Tip: m.AiModel(), Tint: tok.Warning}
	cur := ""
	if p != nil {
		cur = paneModel(cn, p)
		ch.Label, ch.Tint = cur, ui.Color{}
		ch.Brand = aibrands.Model(cur, *p)
	}
	ch = ch.fit(m, fit, aiModelShort)
	ch.open = a.menu.key == key
	el := ch.show(c, key).Role(ui.RoleMenuButton).Expanded(ch.open)
	open := ch.open
	if el.Clicked() {
		if open {
			a.menu = dropMenu{}
		} else {
			a.menu = dropMenu{key: key, sub: -1, focus: 0}
			cn.ai.query = ""
		}
		open = !open
	}
	if !open {
		return
	}
	ui.PopoverBase(c.Key("menu:"+key), el, &open, func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Role(ui.RoleMenu).Width(modelMenuWidth).Padding(5).Radius(theme.RadiusCard).Background(tok.Inset).
			Border(1, tok.GlassEdge).FocusGroup(ui.Vertical)
		tok.Lift.On(panel)
		panel.Children(func() {
			groups := a.modelChoices(cn.ai.query)
			first := modelChoice{}
			for _, g := range groups {
				if len(g) > 0 {
					first = g[0]
					break
				}
			}
			in := formTextInput(c.Key("q"), &cn.ai.query).Placeholder(m.AiSearchModels()).Label(m.AiSearchModels()).
				FontSize(theme.Footnote).FillWidth().Margin(0, 0, 4, 0)
			if a.menu.focus == 0 {
				in.Focus()
				a.menu.focus = -1
			}
			typed := strings.TrimSpace(cn.ai.query)
			if in.Submitted() {
				switch {
				case first.p != nil:
					a.chooseModel(cn, first.p, first.model)
					open = false
				case typed != "" && p != nil:
					a.chooseModel(cn, p, typed)
					open = false
				}
			}
			gutterScroll(c, ui.Scroll(c.Key("scroll")).MaxHeight(menuHeight).FillWidth()).Children(func() {
				ui.Column(c).FillWidth().Children(func() {
					a.modelMenuRows(c, cn, tok, m, p, cur, groups, typed)
				})
			})
		})
	})
	if !open || a.menu.key != key {
		if a.menu.key == key {
			a.menu = dropMenu{}
		}
	}
}

// modelMenuRows lists the model menu under the search field: one entry per
// provider, whose submenu holds its models, so the first level names the
// groups and only the second one the models. One provider alone has nothing
// to name first, and a query has already narrowed the list, so both show
// their models flat rather than one step further away.
func (a *App) modelMenuRows(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, p *settings.AIProvider, cur string, groups [][]modelChoice, typed string) {
	n := 0
	row := func(e menuEntry) {
		a.menuRow(c, tok, e, n, 0)
		n++
	}
	model := func(mc modelChoice) menuEntry {
		e := menuEntry{
			Lead:    func() { brandImage(c, aibrands.Model(mc.model, *mc.p), 16) },
			Label:   mc.model,
			Checked: p != nil && mc.p.ID == p.ID && mc.model == cur,
			Do:      func() { a.chooseModel(cn, mc.p, mc.model) },
		}
		if mc.model == mc.p.Model {
			e.InlineNote = m.AiDefaultModel()
		}
		return e
	}
	providers := 0
	for _, g := range groups {
		if len(g) > 0 {
			providers++
		}
	}
	flat := typed != "" || providers <= 1
	for _, g := range groups {
		if len(g) == 0 {
			continue
		}
		if flat {
			for i, mc := range g {
				if i >= modelMenuMax {
					break
				}
				row(model(mc))
			}
			continue
		}
		gp := g[0].p
		sub := make([]menuEntry, 0, len(g))
		for i, mc := range g {
			if i >= modelMenuMax {
				break
			}
			sub = append(sub, model(mc))
		}
		row(menuEntry{
			Lead:     func() { brandImage(c, aibrands.Provider(*gp), 16) },
			Label:    providerName(*gp),
			Checked:  p != nil && gp.ID == p.ID,
			Sub:      sub,
			SubWidth: modelMenuWidth,
		})
	}
	if typed != "" {
		switch {
		case n == 0 && p != nil:
			pm := *p
			row(menuEntry{Lead: func() { brandImage(c, aibrands.Model(typed, pm), 16) }, Label: m.AiUseModel(typed), Do: func() { a.chooseModel(cn, p, typed) }})
		case n == 0:
			ui.Text(c, m.AiNoModels()).FontSize(theme.Footnote).TextColor(tok.TextMuted).Padding(6, 8)
		}
	}
	ui.Box(c.Key("sep")).Height(1).Margin(4, 8).Background(tok.Line)
	row(menuEntry{Icon: icons.Settings, Label: m.AiManageProviders(), Do: func() { a.openSettings(prefAI) }})
}

// aiModes are the permissions a pane runs under, in the order its menu
// lists them, with their icons; modeTint colors each.
var aiModes = []struct {
	mode policy.Mode
	icon *icons.Icon
}{
	{policy.ModeAsk, icons.ModeAsk},
	{policy.ModeReview, icons.ModeReview},
	{policy.ModeFull, icons.ModeFull},
}

func modeTint(tok theme.Tokens, mode policy.Mode) ui.Color {
	switch mode {
	case policy.ModeFull:
		return tok.Danger
	case policy.ModeReview:
		return tok.Warning
	default:
		return tok.Success
	}
}

func modeName(m locales.Messages, mode policy.Mode) (name, note string) {
	switch mode {
	case policy.ModeFull:
		return m.AiModeFull(), m.AiModeFullNote()
	case policy.ModeReview:
		return m.AiModeReview(), m.AiModeReviewNote()
	default:
		return m.AiModeAsk(), m.AiModeAskNote()
	}
}

// modeChip shows the permission the pane runs under, in its color, and
// chooses another from a menu. Full access asks to be confirmed first.
func (a *App) modeChip(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fit aiFit) {
	cur := a.modeOf(cn)
	ch := chip{Icon: icons.ModeAsk, Tip: m.AiPermission(), Tint: modeTint(tok, cur)}
	for _, md := range aiModes {
		if md.mode == cur {
			ch.Icon = md.icon
		}
	}
	ch.Label, _ = modeName(m, cur)
	ch = ch.fit(m, fit, 0)
	a.menuChip(c, "aimode:"+strconv.Itoa(cn.id), ch, func() []menuEntry {
		out := make([]menuEntry, 0, len(aiModes))
		for _, md := range aiModes {
			mode := md.mode
			name, note := modeName(m, mode)
			out = append(out, menuEntry{Icon: md.icon, Tint: modeTint(tok, mode), Label: name, Note: note,
				Checked: mode == cur, Danger: mode == policy.ModeFull, Do: func() { a.setPaneMode(cn, string(mode)) }})
		}
		return out
	})
}

// aiTimeline is the conversation, which follows its end as it grows
// unless the user scrolled up from it; before the first question, a word
// on what the pane does.
func (a *App) aiTimeline(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	if len(cn.ai.items) == 0 && !cn.ai.running {
		ui.Column(c.Key("aiempty")).Grow(1).FillWidth().Padding(theme.Space(6)).Gap(theme.Space(2)).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			softTile(c, icons.Robot, tok.Accent, 44).Margin(0, 0, theme.Space(1), 0)
			ui.Text(c, m.AiEmptyTitle(a.pv(cn.host.Label()))).FontSize(theme.Title3).FontWeight(600).TextAlign(ui.Center).MaxWidth(320)
			ui.Text(c, m.AiEmpty()).FontSize(theme.Footnote).TextColor(tok.TextMuted).TextAlign(ui.Center).MaxWidth(300)
			a.recentChats(c, cn, tok, m)
		})
		return
	}
	st := &cn.ai.scroll
	ui.Scroll(c.Key("aitime")).TrackScroll(st).Grow(1).FillWidth().Children(func() {
		ui.Column(c).Padding(theme.Space(3), theme.Space(3), theme.Space(4), theme.Space(3)).Gap(theme.Space(3)).FillWidth().Children(func() {
			// The agent does not remember what comes before forgot.
			forgot := func(i int) {
				if cn.ai.forgot > 0 && i == cn.ai.forgot {
					aiBanner(c.Key("aiforgot"), tok.Accent, icons.Info, m.AiAgentForgot())
				}
			}
			for i := range cn.ai.items {
				forgot(i)
				a.aiCard(c, cn, i, tok, m)
			}
			forgot(len(cn.ai.items))
			n := len(cn.ai.items)
			if cn.ai.running && (n == 0 || cn.ai.items[n-1].kind != "text") {
				ui.Row(c.Key("aiworking")).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Label(m.AiWorking())
					ui.Text(c, m.AiWorking()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
				})
			}
			if cn.ai.paused && !cn.ai.running {
				ui.Row(c.Key("aipaused")).Gap(theme.Space(2)).AlignItems(ui.Center).FillWidth().Children(func() {
					ui.Text(c, m.AiPaused()).FontSize(theme.Footnote).TextColor(tok.TextMuted).Shrink(1)
					if pill(c, m.AiContinue(), true, false, false) {
						a.ContinueAI(cn.id)
					}
				})
			}
		})
		// Reveal the end only when it moves out of view, so an idle
		// timeline never resets the offset and cancels a wheel glide.
		end := ui.Box(c.Key("aiend")).Height(0)
		if st.Y == st.MaxY {
			end.ScrollIntoView()
		}
	})
}

// aiComposer is the question being written, on two to eight lines of its
// own that grow with it, then scroll. Under it, in the same box: the
// permission and the attachment button, and on the right, for the built-in
// agent, the model and thinking level, then the send button, which stops
// the turn while one runs. Enter sends; Shift+Enter breaks the line. Full
// access shows a banner above it. The choices show as fit allows.
func (a *App) aiComposer(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fit aiFit) {
	ui.Column(c.Key("aifoot")).Shrink(0).FillWidth().Padding(0, theme.Space(3), theme.Space(3), theme.Space(3)).Gap(theme.Space(2)).Children(func() {
		if a.modeOf(cn) == policy.ModeFull {
			aiBanner(c.Key("fullbar"), tok.Danger, icons.Warning, m.AiFullBar())
		}
		a.aiApproval(c, cn, tok, m)
		box := ui.Column(c.Key("aicompose")).FillWidth().Padding(theme.Space(1)).Gap(theme.Space(1)).
			Radius(theme.RadiusCard).Background(tok.Fill)
		edge := tok.Line
		if box.FocusWithin() {
			edge = tok.Focus
		}
		drag := box.FileDragOver()
		if drag {
			edge = tok.Accent
		}
		if paths := box.DroppedFiles(); len(paths) > 0 {
			a.attachPaths(cn, paths)
		}
		id := cn.id
		box.Border(1, edge).Children(func() {
			if drag {
				ui.Text(c.Key("aidrop"), m.AiDropFiles()).FontSize(theme.Footnote).TextColor(tok.Accent).Padding(theme.Space(1), theme.Space(2), 0, theme.Space(2))
			}
			if len(cn.ai.attach) > 0 {
				ui.Column(c.Key("aiattach")).FillWidth().Padding(theme.Space(1), theme.Space(1), 0, theme.Space(1)).Children(func() {
					a.attachStrip(c, tok, m, cn.ai.attach, func(i int) {
						cn.ai.attach = append(cn.ai.attach[:i:i], cn.ai.attach[i+1:]...)
					})
				})
			}
			in := ui.TextAreaBase(c.Key("aiin:"+strconv.Itoa(cn.id)+":"+strconv.Itoa(cn.ai.gen)), &cn.ai.input).
				Placeholder(m.AiPlaceholder()).Label(m.AiPlaceholder()).FontSize(theme.Body).Padding(theme.Space(1), theme.Space(2)).
				FillWidth().Lines(2, 8).
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key != ui.KeyEnter || k.Mods != 0 {
						return false
					}
					text := k.Text()
					a.post(func() {
						if cn := a.conns[id]; cn != nil && cn.ai != nil && !cn.ai.running {
							cn.ai.input = text
							a.sendAI(cn)
						}
					})
					return true
				})
			if a.focusReq == cn.id && a.page == pageWorkspace {
				in.Focus()
				a.focusReq = 0
			}
			ui.Row(c.Key("aitools")).FillWidth().Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
				a.modeChip(c, cn, tok, m, fit)
				if (IconButton{Icon: icons.Attach, Label: m.AiAttach(), Size: Small, Disabled: a.p.PickFiles == nil}).Clicked(c, "aiattachbtn") {
					a.pickAttach(cn, m)
				}
				ui.Spacer(c).Shrink(1)
				if cn.ai.agent == "builtin" {
					a.modelChip(c, cn, tok, m, fit)
					a.effortChip(c, cn, tok, m, fit)
				} else {
					a.acpOptionChips(c, cn, tok, m, fit)
				}
				if cn.ai.running {
					if (IconButton{Icon: icons.Stop, Label: m.StopAI(), Size: Small, Danger: true, Active: true}).Clicked(c, "aistop") {
						a.StopAI(cn.id)
					}
					return
				}
				empty := strings.TrimSpace(cn.ai.input) == "" && len(cn.ai.attach) == 0
				if (IconButton{Icon: icons.Send, Label: m.AiSend(), Size: Small, Active: !empty, Disabled: empty}).Clicked(c, "aisend") {
					a.sendAI(cn)
				}
			})
		})
	})
}

// sendAI sends the question written in the pane, and follows the reply.
func (a *App) sendAI(cn *conn) {
	if cn.ai.running || (strings.TrimSpace(cn.ai.input) == "" && len(cn.ai.attach) == 0) {
		return
	}
	cn.ai.scroll.Y = math.MaxFloat32
	a.SendAI(cn.id, cn.ai.input)
}

// aiBanner is a rounded notice tinted with col, as the panes' banners are.
func aiBanner(c *ui.Context, col ui.Color, ic *icons.Icon, text string) ui.Element {
	return ui.Row(c).FillWidth().Padding(6, 10).Gap(theme.Space(2)).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		ui.Icon(c, ic.SVG).Size(14, 14).Shrink(0).TextColor(col)
		ui.Text(c, text).FontSize(theme.Footnote).Grow(1).Shrink(1)
	})
}

// shellLangs are the languages of code blocks that are commands to run,
// which a pane can insert into its terminal; a block that names none is
// taken for one.
var shellLangs = map[string]bool{"": true, "sh": true, "bash": true, "zsh": true, "shell": true, "console": true, "fish": true}

// aiMarkdown is how the pane shows Markdown: its code blocks in a box of
// its own, with buttons to copy them and, for a command of one line, to
// insert it into the terminal.
func (a *App) aiMarkdown(cn *conn) *markdown.Options {
	return &markdown.Options{CodeBlock: func(c *ui.Context, b *markdown.CodeBlockView) { a.aiCodeBlock(c, cn, b) }}
}

func (a *App) aiCodeBlock(c *ui.Context, cn *conn, b *markdown.CodeBlockView) {
	tok := currentTokens(c)
	m := locales.In(c.Locale())
	code := b.Code()
	ui.Column(c).Direction(ui.LeftToRight).FillWidth().Radius(theme.RadiusControl).Background(tok.Fill).Border(1, tok.Line).Clip().Children(func() {
		ui.Row(c).Padding(2, 4, 0, 10).Gap(2).AlignItems(ui.Center).Height(26).Children(func() {
			ui.Text(c, b.Language).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
			ui.Spacer(c)
			a.codeActions(c, cn, code, shellLangs[b.Language], b.Open, m)
		})
		b.Body(c)
	})
}

// codeActions are the buttons on code: Copy, which shows a check for a
// moment once it has, and, for a command of one line, Insert into the
// pane's terminal, without Enter.
func (a *App) codeActions(c *ui.Context, cn *conn, code string, shell, open bool, m locales.Messages) {
	code = strings.TrimRight(code, "\n")
	ic := icons.Copy
	if cn.ai.copied == code && c.Now().Before(cn.ai.copiedTill) {
		ic = icons.Check
	}
	if (IconButton{Icon: ic, Label: m.Copy(), Size: Small, Disabled: open || code == ""}).Clicked(c, "copy") {
		c.WriteClipboard(code)
		cn.ai.copied, cn.ai.copiedTill = code, c.Now().Add(2*time.Second)
		c.After(2 * time.Second)
	}
	t := a.conns[cn.ai.link]
	if shell && !strings.Contains(code, "\n") && t != nil && t.tv != nil {
		if (IconButton{Icon: icons.Terminal, Label: m.AiInsert(), Size: Small, Disabled: open || code == ""}).Clicked(c, "insert") {
			a.InsertAI(cn.id, code)
		}
	}
}

func (a *App) aiCard(c *ui.Context, cn *conn, i int, tok theme.Tokens, m locales.Messages) {
	it := &cn.ai.items[i]
	key := c.Key("item:" + strconv.Itoa(i))
	switch it.kind {
	case "user":
		ui.Row(key).FillWidth().Justify(ui.End).Children(func() {
			strong := float32(0.12)
			if tok.Dark {
				strong = 0.22
			}
			ui.Column(c).Shrink(1).MinWidth(0).MarginStart(theme.Space(8)).Padding(7, 12).Gap(theme.Space(1)).Radius(theme.RadiusCard).
				Background(tok.Accent.Alpha(strong)).Selectable().Children(func() {
				if len(it.files) > 0 {
					a.attachStrip(c, tok, m, it.files, nil)
				}
				if it.text != "" {
					ui.Text(c, it.text).FontSize(theme.Body)
				}
			})
		})
	case "text":
		ui.Column(key).FillWidth().Children(func() {
			if cn.ai.running && i == len(cn.ai.items)-1 {
				markdown.Stream(c, it.text, cn.ai.md).FillWidth()
			} else {
				markdown.View(c, it.text, cn.ai.md).FillWidth()
			}
		})
	case "think":
		ui.Column(key).FillWidth().Children(func() {
			ui.Collapsible(c.Key("think"), m.AiThinking(), &it.open, func() {
				ui.Row(c).Gap(theme.Space(2)).FillWidth().Children(func() {
					ui.Box(c).Width(2).Shrink(0).AlignSelf(ui.Stretch).Radius(1).Background(tok.Line)
					ui.Text(c, it.text).FontSize(theme.Footnote).TextColor(tok.TextMuted).Grow(1).Shrink(1)
				})
			})
		})
	case "err":
		aiBanner(key, tok.Danger, icons.Warning, aiErrLine(m, it.text))
	case "tool":
		a.toolCard(key, cn, it, tok, m)
	}
}

// riskTint is the color of a tool's risk: green to red, with an ungraded
// action marked amber so full access still shows what ran unjudged.
func riskTint(tok theme.Tokens, s string) ui.Color {
	switch s {
	case "readonly", "low":
		return tok.Success
	case "medium", "unknown":
		return tok.Warning
	case "high", "critical":
		return tok.Danger
	default:
		return tok.TextMuted
	}
}

func (a *App) tabAIMarks(c *ui.Context, t *layout.Tab, tok theme.Tokens, m locales.Messages) {
	full, running := false, false
	for _, p := range t.Panes() {
		cn := a.conns[p.Item.ID]
		if cn == nil || cn.ai == nil {
			continue
		}
		a.refreshMode(cn)
		if a.modeOf(cn) == policy.ModeFull {
			full = true
		}
		if cn.ai.running {
			running = true
		}
	}
	if running {
		ui.Icon(c, icons.Robot.SVG).Size(14, 14).Shrink(0).TextColor(tok.Accent).Label(m.AiRunning()).Tooltip(m.AiRunning())
	}
	if full {
		ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(tok.Danger).Label(m.AiTabWarning()).Tooltip(m.AiTabWarning())
	}
}

func modeIndex(mode string) int {
	switch mode {
	case "review":
		return 1
	case "full":
		return 2
	default:
		return 0
	}
}

func riskText(m locales.Messages, s string) string {
	switch s {
	case "readonly":
		return m.AiRiskReadonly()
	case "low":
		return m.AiRiskLow()
	case "medium":
		return m.AiRiskMedium()
	case "high":
		return m.AiRiskHigh()
	case "critical":
		return m.AiRiskCritical()
	default:
		return m.AiRiskUnknown()
	}
}

func riskOf(m locales.Messages, r policy.Risk) string {
	switch r {
	case policy.RiskReadOnly:
		return m.AiRiskReadonly()
	case policy.RiskLow:
		return m.AiRiskLow()
	case policy.RiskMedium:
		return m.AiRiskMedium()
	case policy.RiskHigh:
		return m.AiRiskHigh()
	case policy.RiskCritical:
		return m.AiRiskCritical()
	default:
		return m.AiRiskUnknown()
	}
}

func verdictText(m locales.Messages, v string) string {
	switch v {
	case "full":
		return m.AiAutoFull()
	case "auto":
		return m.AiVerdictAuto()
	case "reviewer":
		return m.AiVerdictReviewer()
	case "user":
		return m.AiVerdictUser()
	case "timeout":
		return m.AiVerdictTimeout()
	case "cancel":
		return m.AiVerdictCancel()
	default:
		return m.AiVerdictReject()
	}
}

func toolOutput(m locales.Messages, s string) string {
	if s == "remote tools are not connected yet" {
		return m.AiToolsPending()
	}
	first, rest, multiline := strings.Cut(s, "\n")
	if first == "ai.command_output_limit" {
		text := m.AiCommandOutputLimit()
		if multiline {
			text += "\n" + rest
		}
		return text
	}
	return securityText(m, s)
}

func aiErrLine(m locales.Messages, s string) string {
	if rest, ok := strings.CutPrefix(s, "http:"); ok {
		status, detail, _ := strings.Cut(rest, ":")
		if detail != "" {
			return m.AiHTTPFailedDetail(status, detail)
		}
		return m.AiHTTPFailed(status)
	}
	switch s {
	case "key":
		return m.AiNoKey()
	case "backend":
		return m.AiBackendUnavailable()
	case "acp-auth":
		return m.AiACPAuthRequired()
	case "acp-host":
		return m.AiACPHostFailed()
	case "acp-files":
		return m.AiACPFilesFailed()
	case "acp-close":
		return m.AiACPCloseFailed()
	case "acp-start":
		return m.AiACPStartFailed()
	case "acp-protocol":
		return m.AiACPProtocolFailed()
	case "acp-session":
		return m.AiACPSessionFailed()
	case "acp-incomplete":
		return m.AiACPIncomplete()
	case "acp-warning":
		return m.AiACPSafetyWarning()
	case "provider":
		return m.AiNoModel()
	case "timeout":
		return m.AiFailureTimeout()
	case "network":
		return m.AiFailureNetwork()
	case "protocol":
		return m.AiFailureProtocol()
	case "incomplete":
		return m.AiFailureIncomplete()
	case "size":
		return m.AiFailureSize()
	case "request":
		return m.AiFailureRequest()
	default:
		return m.AiRequestFailed(toolOutput(m, s))
	}
}

// aiSheetStop is the stop shortcut from inside a sheet. The window
// shortcut is not delivered while a dialog is up, and Stop has to close
// that dialog.
func (a *App) aiSheetStop(c *ui.Context) {
	if k := a.keyOf(actStopAI); c.Shortcut(k.Mods, k.Key) {
		a.stopShortcut()
	}
	if k := a.keyOf(actStopAllAI); c.Shortcut(k.Mods, k.Key) {
		a.StopAllAI()
	}
}

func (a *App) aiSheets(c *ui.Context, m locales.Messages) {
	tok := currentTokens(c)
	a.consentSheet(c, m, tok)
	a.fullSheet(c, m, tok)
	a.askElsewhere(c, m)
	a.providerSheet(c, m, tok)
	a.acpSheet(c, m, tok)
	a.providerDelete(c, m)
	a.mcpSheet(c, m, tok)
}

func (a *App) consentSheet(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	if a.consent == nil {
		return
	}
	ask := a.consent
	sheet(ui.Modal(c.Key("aiconsent"), &ask.open, func() {
		a.aiSheetStop(c)
		ui.Column(c).Gap(14).Width(420).Children(func() {
			sheetTitle(c, m.AiConsentTitle())
			ui.Text(c, m.AiConsentBody()).FontSize(theme.Body).FillWidth()
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					ask.open = false
				}
				if pill(c, m.AiConsentConfirm(), true, false, false) {
					if err := a.grantConsent(ask.host); err != nil {
						c.AddToast(ui.Toast{Title: m.AiSettingsSaveFailed(), Type: "error"})
						return
					}
					id, text := ask.conn, ask.text
					ask.open = false
					a.consent = nil
					a.SendAI(id, text)
				}
			})
		})
	}), tok)
	if a.consent == ask && !ask.open {
		a.consent = nil
	}
}

func (a *App) fullSheet(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	f := a.full
	if f == nil {
		return
	}
	sheet(ui.Modal(c.Key("aifull"), &f.open, func() {
		a.aiSheetStop(c)
		ui.Column(c).Gap(14).Width(440).Children(func() {
			sheetTitle(c, m.AiFullTitle())
			ui.Text(c, m.AiFullBody()).FontSize(theme.Body).FillWidth()
			box := formCheckbox(c.Key("aifullcheck"), &f.tick, m.AiFullCheck())
			a.fullHold.apply(c, f, box)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					f.open = false
				}
				if pill(c, m.AiFullConfirm(), false, true, !f.tick) {
					a.applyFull()
					f.open = false
				}
			})
		})
	}), tok)
	if a.full == f && !f.open {
		a.full = nil
	}
}

// aiApproval asks, above the composer of the pane whose agent waits, to
// allow what the agent wants to do. It stays until answered, or the turn
// stops: clicking elsewhere leaves it waiting.
func (a *App) aiApproval(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	p := cn.ai.pending
	if p == nil {
		return
	}
	pr := p.prompt
	tint := tok.Accent
	if pr.Decision.Red {
		tint = tok.Danger
	}
	ui.Column(c.Key("aiask")).FillWidth().Padding(theme.Space(2), theme.Space(3)).Gap(theme.Space(2)).
		Radius(theme.RadiusCard).Background(tint.Alpha(0.12)).Border(1, tint.Alpha(0.22)).Label(m.AiApprovalTitle()).Children(func() {
		where := m.AiWhereRemote(a.pv(cn.host.Label()))
		if pr.Action.Where == policy.Local {
			where = m.AiWhereLocal()
		}
		cat := string(pr.Decision.Category)
		if cat == "" {
			cat = pr.Action.Tool
		}
		ui.Row(c.Key("head")).FillWidth().Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.ModeAsk.SVG).Size(14, 14).Shrink(0).TextColor(tint)
			title := ui.Text(c, where+" · "+cat+" · "+riskOf(m, pr.Decision.Risk)).FontSize(theme.Footnote).FontWeight(600).Shrink(1).MinWidth(0)
			if pr.Decision.Red {
				title.TextColor(tok.Danger)
			}
		})
		if pr.Action.Command != "" {
			gutterScroll(c, ui.Scroll(c.Key("cmd")).MaxHeight(96).FillWidth()).Children(func() {
				toolDetailText(c, pr.Action.Command, tok.Text, theme.Footnote)
			})
		}
		if pr.Action.Dir != "" {
			ui.Text(c, m.AiDir()+": "+pr.Action.Dir).FontSize(theme.Caption).TextColor(tok.TextMuted)
		}
		if pr.Diff != "" {
			formCheckbox(c.Key("onlydiff"), &cn.ai.onlyDiff, m.AiOnlyChanges())
			diff := pr.Diff
			if cn.ai.onlyDiff {
				diff = agent.OnlyChanges(diff)
			}
			gutterScroll(c, ui.Scroll(c.Key("diff")).MaxHeight(180).FillWidth()).Children(func() {
				toolDetailText(c, diff, tok.Text, theme.Caption)
			})
		} else if pr.Action.Path != "" {
			ui.Text(c, pr.Action.Path).Font("monospace").FontSize(theme.Footnote).FillWidth()
		}
		if pr.Action.Reason != "" {
			ui.Text(c, m.AiReason()+": "+pr.Action.Reason).FontSize(theme.Footnote).FillWidth()
		}
		if pr.ReviewNote != "" {
			ui.Text(c, m.AiReviewNote()+": "+reviewNoteText(m, pr.ReviewNote)).FontSize(theme.Footnote).TextColor(tok.TextMuted).FillWidth()
		}
		ui.Row(c.Key("answers")).FillWidth().Wrap().Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
			if pill(c, m.AiAllowOnce(), false, pr.Decision.Red, false) {
				a.answerAI(cn, policy.AllowOnce)
			}
			if pr.Decision.Similar {
				if pill(c, m.AiAllowSimilar(), false, pr.Decision.Red, false) {
					a.answerAI(cn, policy.AllowSimilar)
				}
			}
			ui.Spacer(c)
			el, ok := pillEl(c, m.AiReject(), false, true, false)
			// Reject takes the keyboard in the focused pane, unless a
			// question is being written there.
			if a.focused() == cn && cn.ai.input == "" {
				a.aiHold.apply(c, p, el)
			}
			if ok {
				a.answerAI(cn, policy.Reject)
			}
		})
	})
}

// askElsewhere tells of an approval waiting in an AI pane out of sight
// with a toast that shows the pane, and closes the toast once the pane
// shows or the approval is no longer waiting.
func (a *App) askElsewhere(c *ui.Context, m locales.Messages) {
	for id, p := range a.askToasts {
		cn := a.conns[id]
		if cn == nil || cn.ai == nil || cn.ai.pending != p || a.paneShown(id) {
			c.CloseToast(askToastID(id))
			delete(a.askToasts, id)
		}
	}
	for id, cn := range a.conns {
		if cn.ai == nil || cn.ai.pending == nil || a.askToasts[id] == cn.ai.pending || a.paneShown(id) {
			continue
		}
		if a.askToasts == nil {
			a.askToasts = map[int]*aiPend{}
		}
		a.askToasts[id] = cn.ai.pending
		c.AddToast(ui.Toast{ID: askToastID(id), Title: m.AiApprovalWaiting(a.pv(cn.host.Label())), Timeout: -1,
			Action: m.AiShowApproval(), OnAction: func() {
				if cn := a.conns[id]; cn != nil {
					a.showPane(cn)
				}
			}})
	}
}

func askToastID(id int) string { return "aiask:" + strconv.Itoa(id) }

// paneShown reports whether pane id is in the workspace's tab on screen.
func (a *App) paneShown(id int) bool {
	t, _ := a.ws.Locate(id)
	return a.page == pageWorkspace && t != nil && t == a.ws.ActiveTab()
}
