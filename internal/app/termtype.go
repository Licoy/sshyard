package app

import (
	"cmp"
	"slices"
	"strings"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// termPresets are the terminal types the selects offer, the default
// first; Custom takes any other TERM.
var termPresets = []string{session.DefaultTerm, "xterm", "screen-256color", "tmux-256color", "linux", "vt220", "vt100"}

// termTypeRow is the setting's terminal type: a preset, or a custom TERM
// saved once Enter is pressed or the field is left, if TermOK takes it.
func (a *App) termTypeRow(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	cur := cmp.Or(s.TerminalType, session.DefaultTerm)
	if !a.termCustom && !slices.Contains(termPresets, cur) {
		a.termCustom, a.termText = true, cur
	}
	label := cur
	if a.termCustom {
		label = m.TermTypeCustom()
	}
	ui.Column(c).Gap(4).Children(func() {
		if formSelect(c.Key("termtype"), &label, append(slices.Clone(termPresets), m.TermTypeCustom())).Label(m.TermType()).Width(180).Changed() {
			if a.termCustom = label == m.TermTypeCustom(); a.termCustom {
				a.termText = cur
			} else if label != cur {
				s.TerminalType, *changed = label, true
			}
		}
		if !a.termCustom {
			return
		}
		in := formTextInput(c.Key("termtypetext"), &a.termText).Placeholder(session.DefaultTerm).Label(m.TermType()).Width(180)
		focused := in.Focused()
		text := strings.TrimSpace(a.termText)
		if (in.Submitted() || a.termFocus && !focused) && session.TermOK(text) && text != cur {
			s.TerminalType, *changed = text, true
		}
		a.termFocus = focused
		if text != "" && !session.TermOK(text) {
			ui.Text(c, m.TermTypeInvalid()).FontSize(theme.Caption).TextColor(tok.Danger).Width(180)
		}
	})
}

// termTypeField is the TERM the host's new terminals report: the
// setting's, a preset, or custom text the save checks.
func (a *App) termTypeField(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.TermType(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			global := cmp.Or(a.settings.TerminalType, session.DefaultTerm)
			follow := m.TermTypeFollow(global)
			label := f.term
			switch {
			case f.termCustom:
				label = m.TermTypeCustom()
			case f.term == "":
				label = follow
			}
			options := append(append([]string{follow}, termPresets...), m.TermTypeCustom())
			if formSelect(c.Key("f:term"), &label, options).Label(m.TermType()).FillWidth().Changed() {
				f.termCustom = label == m.TermTypeCustom()
				switch {
				case label == follow:
					f.term = ""
				case !f.termCustom:
					f.term = label
				}
			}
			if f.termCustom {
				formTextInput(c.Key("f:termtext"), &f.term).Placeholder(global).Label(m.TermType()).FillWidth()
			}
			ui.Text(c, m.TermTypeHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}
