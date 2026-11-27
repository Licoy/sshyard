package app

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The broadcast pane sends a command typed once to several terminals:
// those of the active tab, or of every tab, less those left out. Each
// terminal gets it as it takes a paste, as a snippet is sent.

var errBroadcastNoTarget = errors.New("broadcast: no terminal to send to")

// broadcastState is the broadcast pane's. None of it is saved: a
// command never goes anywhere on its own after a restart, nor does typing.
type broadcastState struct {
	// all sends to the terminals of every tab, not the active tab's only.
	all bool
	// left are the terminals left out, by pane (conn) ID.
	left map[int]bool
	// draft is the command typed; noEnter sends it without running it.
	draft   string
	noEnter bool
	// sent is how the last send went, by pane: nil once written. sending
	// counts the writes still going.
	sent    map[int]error
	sending int
	// confirm asks before sending held, several lines, run when
	// heldEnter; heldDraft when they are the draft's.
	confirm              bool
	held                 string
	heldEnter, heldDraft bool
	// live sends what is typed in a target to the other targets too;
	// synced are those targets, as of this frame, by pane ID.
	live   bool
	synced map[int]bool
	// pasteAsk asks before a paste of several lines, paste, into pane
	// pasteFrom while live.
	pasteAsk  bool
	paste     string
	pasteFrom int
	// history are the commands sent from the draft, the last last, kept
	// in memory only; back is how far Up went back in them, from typed.
	history []string
	back    int
	typed   string
}

// historyMax is how many commands sent the broadcast pane remembers.
const historyMax = 100

// remember keeps text, sent, the last of the history.
func (b *broadcastState) remember(text string) {
	b.back, b.typed = 0, ""
	if strings.TrimSpace(text) == "" || len(b.history) > 0 && b.history[len(b.history)-1] == text {
		return
	}
	b.history = append(b.history, text)
	if len(b.history) > historyMax {
		b.history = slices.Delete(b.history, 0, len(b.history)-historyMax)
	}
}

// recall steps through the history from text typed: back to an older
// command when up, else on to a newer one, and from the newest to what
// was typed. It reports false when there is none further.
func (b *broadcastState) recall(text string, up bool) (string, bool) {
	switch {
	case up && b.back < len(b.history):
		if b.back == 0 {
			b.typed = text
		}
		b.back++
	case !up && b.back > 0:
		b.back--
	default:
		return "", false
	}
	if b.back == 0 {
		return b.typed, true
	}
	return b.history[len(b.history)-b.back], true
}

// terminalPanes are the terminal panes of tabs, in order.
func (a *App) terminalPanes(tabs []*layout.Tab) []*conn {
	var out []*conn
	for _, t := range tabs {
		for _, p := range t.Panes() {
			if c := a.conns[p.Item.ID]; c != nil && c.kind == layout.Terminal && c.tv != nil && c.s != nil {
				out = append(out, c)
			}
		}
	}
	return out
}

// paneName names terminal pane c in a list of panes: its host, and its
// number when the host has more than one.
func (a *App) paneName(c *conn) string {
	name := a.pv(a.liveHost(c.host).Label())
	if c.termNo > 0 && a.hostTermPanes(c.host.ID) > 1 {
		name += " #" + strconv.Itoa(c.termNo)
	}
	return name
}

// broadcastPanes are the terminals a command may go to: the active tab's,
// or every tab's.
func (a *App) broadcastPanes() []*conn {
	if a.bc.all {
		return a.terminalPanes(a.allTabs())
	}
	// The active tab of the window the broadcast pane is in.
	ws := a.ws
	if c := a.toolPane(layout.Broadcast); c != nil {
		if w := a.winOf(c.id); w != nil {
			ws = w.ws
		}
	}
	if t := ws.ActiveTab(); t != nil {
		return a.terminalPanes([]*layout.Tab{t})
	}
	return nil
}

// broadcastTargets are the terminals a command goes to: the panes not
// left out.
func (a *App) broadcastTargets() []*conn {
	var out []*conn
	for _, c := range a.broadcastPanes() {
		if !a.bc.left[c.id] {
			out = append(out, c)
		}
	}
	return out
}

// multiLine reports text of several lines, once the line ends that end it
// are gone, as snippetBytes takes them off.
func multiLine(text string) bool {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.Contains(strings.TrimRight(text, "\n"), "\n")
}

// sendBroadcast starts writing text to each target, encoded for its
// terminal, run when enter. Each target's outcome lands in bc.sent: a
// terminal disconnected or unable to take the text safely is skipped, the
// others still get it. The error is why nothing could start.
func (a *App) sendBroadcast(text string, enter bool) error {
	if a.bc.sending > 0 {
		return errSnippetBusy
	}
	if err := assets.ValidateSnippetContent(text); err != nil {
		return err
	}
	targets := a.broadcastTargets()
	if len(targets) == 0 {
		return errBroadcastNoTarget
	}
	a.bc.sent = map[int]error{}
	for _, c := range targets {
		if c.s.Status().State != session.Connected {
			a.bc.sent[c.id] = errSnippetDisconnected
			continue
		}
		payload, err := snippetBytes(text, enter, c.s.Term.Modes())
		if err != nil {
			a.bc.sent[c.id] = err
			continue
		}
		w, err := c.s.CaptureInput()
		if err != nil {
			a.bc.sent[c.id] = errSnippetDisconnected
			continue
		}
		a.bc.sending++
		id, v := c.id, c.tv
		go func() {
			err := writeSnippet(w, payload)
			a.post(func() {
				a.bc.sending--
				a.bc.sent[id] = err
				if err == nil {
					v.offset, v.scrollAcc = 0, 0
					v.selected, v.selecting = false, false
				}
			})
		}()
	}
	return nil
}

// submitBroadcast sends the draft, as broadcastText does.
func (a *App) submitBroadcast() {
	a.broadcastText(a.bc.draft, !a.bc.noEnter, true)
}

// broadcastText sends text to the targets, run when enter; several lines
// it holds until confirmed. The draft's, once sent, goes to the history
// and leaves the draft.
func (a *App) broadcastText(text string, enter, draft bool) {
	if multiLine(text) {
		a.bc.held, a.bc.heldEnter, a.bc.heldDraft, a.bc.confirm = text, enter, draft, true
		return
	}
	a.startBroadcast(text, enter, draft)
}

// confirmBroadcast sends what is held.
func (a *App) confirmBroadcast() {
	text := a.bc.held
	a.bc.held = ""
	a.startBroadcast(text, a.bc.heldEnter, a.bc.heldDraft)
}

// startBroadcast sends text, telling why when it cannot; sent, the
// draft's goes to the history and leaves the draft.
func (a *App) startBroadcast(text string, enter, draft bool) {
	if err := a.sendBroadcast(text, enter); err != nil {
		a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: broadcastErrorText(m, err), Type: "error"} })
		return
	}
	if draft {
		a.bc.remember(text)
		if a.bc.draft == text {
			a.bc.draft = ""
		}
	}
}

// broadcastSnippet sends snippet id to the targets, as it is set to be
// sent, once the secrets are unlocked.
func (a *App) broadcastSnippet(id string) {
	if !a.unlocked() {
		a.withSecrets(func() { a.broadcastSnippet(id) })
		return
	}
	n, ok := a.store.Snippet(id)
	content, err := a.snippetContent(id)
	if err != nil || !ok {
		if err == nil {
			err = assets.ErrNotFound
		}
		a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: snippetErrorText(m, err), Type: "error"} })
		return
	}
	a.broadcastText(content, n.SendEnter, false)
}

// syncSet are the terminals what is typed goes to while live: the
// targets, by pane ID; nil when not live.
func (a *App) syncSet() map[int]bool {
	if !a.bc.live || a.toolPane(layout.Broadcast) == nil {
		return nil
	}
	out := map[int]bool{}
	for _, c := range a.broadcastTargets() {
		out[c.id] = true
	}
	return out
}

// fanoutFrom is how terminal from sends what is typed in it to the other
// targets while live, each encoded for its own terminal; nil when from is
// not a target. A terminal disconnected is passed over.
func (a *App) fanoutFrom(from *conn) func(func(vt.Modes) []byte) {
	if !a.bc.live || a.toolPane(layout.Broadcast) == nil {
		return nil
	}
	targets := a.broadcastTargets()
	if !slices.Contains(targets, from) {
		return nil
	}
	return func(encode func(vt.Modes) []byte) {
		for _, c := range targets {
			if c == from || c.s.Status().State != session.Connected {
				continue
			}
			if b := encode(c.s.Term.Modes()); len(b) > 0 {
				c.tv.offset, c.tv.selected = 0, false
				c.tv.target.Write(b)
			}
		}
	}
}

// holdPaste holds a paste of several lines into terminal from while it
// sends to others, until confirmed. It reports whether it held it.
func (a *App) holdPaste(from *conn, text string) bool {
	if from.tv.fanout == nil || !multiLine(text) {
		return false
	}
	a.bc.paste, a.bc.pasteFrom, a.bc.pasteAsk = text, from.id, true
	return true
}

// pasteHeld pastes what is held into its terminal and the other targets.
func (a *App) pasteHeld() {
	text, from := a.bc.paste, a.conns[a.bc.pasteFrom]
	a.bc.paste = ""
	if from == nil || from.tv == nil {
		return
	}
	from.tv.fanout = a.fanoutFrom(from)
	from.tv.pasteNow(text)
}

// syncPasteDialog asks before a paste held.
func (a *App) syncPasteDialog(c *ui.Context, m locales.Messages) {
	if !a.bc.pasteAsk {
		a.bc.paste = ""
		return
	}
	n := len(a.syncSet())
	if ui.AlertDialog(c, &a.bc.pasteAsk, m.BroadcastPasteTitle(), m.BroadcastPasteConfirm(strconv.Itoa(n)), m.Cancel(), m.Paste()) == 1 {
		a.pasteHeld()
	}
}

// broadcastErrorText says why a send to a terminal, or all of them,
// failed.
func broadcastErrorText(m locales.Messages, err error) string {
	switch {
	case errors.Is(err, errBroadcastNoTarget):
		return m.BroadcastNoTarget()
	case errors.Is(err, errSnippetBusy):
		return m.BroadcastBusy()
	}
	return snippetErrorText(m, err)
}

// broadcastPanel is what the broadcast pane shows: where the command
// goes, the terminals it goes to, each with how the last send went, and
// the command, which takes the keyboard when focus.
func (a *App) broadcastPanel(c *ui.Context, m locales.Messages, tok theme.Tokens, focus bool) {
	panes := a.broadcastPanes()
	targets := a.broadcastTargets()
	ui.Column(c.Key("broadcast")).Fill().Children(func() {
		// Short, the pane keeps the command: what is above it scrolls.
		gutterScroll(c, ui.Scroll(c.Key("bc:list")).Grow(1).Shrink(1).MinHeight(0).FillWidth()).Children(func() {
			ui.Column(c).FillWidth().Padding(theme.Space(2), theme.Space(3)).Gap(theme.Space(2)).Children(func() {
				scope := 0
				if a.bc.all {
					scope = 1
				}
				formSegmented(c.Key("bc:scope"), &scope, false, m.BroadcastThisTab(), m.BroadcastAllTabs()).AlignSelf(ui.Start)
				a.bc.all = scope == 1
				ui.Row(c.Key("bc:live")).FillWidth().Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
					ic := icons.Keyboard
					if a.bc.live {
						ic = icons.KeyboardOn
					}
					ui.Icon(c, ic.SVG).Size(14, 14).Shrink(0).TextColor(tok.Accent)
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(1).Children(func() {
						ui.Text(c, m.BroadcastLive()).FontSize(theme.Footnote).SingleLine()
						ui.Text(c, m.BroadcastLiveNote()).FontSize(theme.Caption).TextColor(tok.TextMuted)
					})
					formSwitch(c.Key("bc:livesw"), &a.bc.live).Label(m.BroadcastLive()).Shrink(0)
				})
				if len(panes) > 0 {
					ui.Text(c, m.BroadcastSelected(strconv.Itoa(len(targets)), strconv.Itoa(len(panes)))).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
				}
			})
			ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
			ui.Column(c).FillWidth().Padding(theme.Space(2)).Gap(theme.Space(0.5)).Children(func() {
				if len(panes) == 0 {
					ui.Column(c).FillWidth().Padding(theme.Space(8), theme.Space(4)).AlignItems(ui.Center).Children(func() {
						panelNote(c, tok, m.BroadcastNoTarget())
					})
					return
				}
				for _, cn := range panes {
					a.broadcastRow(c, m, tok, cn)
				}
			})
		})
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
		a.broadcastComposer(c, m, tok, len(targets), focus)
	})
	if a.bc.confirm && ui.AlertDialog(c, &a.bc.confirm, m.BroadcastConfirmTitle(), m.BroadcastConfirm(strconv.Itoa(len(targets))), m.Cancel(), m.BroadcastSend()) == 1 {
		a.confirmBroadcast()
	}
	if !a.bc.confirm {
		a.bc.held = ""
	}
}

// broadcastRow is a terminal of the broadcast panel: whether the command
// goes to it, and how the last send to it went.
func (a *App) broadcastRow(c *ui.Context, m locales.Messages, tok theme.Tokens, cn *conn) {
	st := cn.s.Status().State
	ui.Row(c.Key("bc:row:"+strconv.Itoa(cn.id))).FillWidth().MinHeight(32).Padding(theme.Space(1), theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Children(func() {
		on := !a.bc.left[cn.id]
		formCheckbox(c.Key("bc:on"), &on, a.paneName(cn)).Grow(1).Shrink(1).MinWidth(0)
		if on == a.bc.left[cn.id] {
			if a.bc.left == nil {
				a.bc.left = map[int]bool{}
			}
			if on {
				delete(a.bc.left, cn.id)
			} else {
				a.bc.left[cn.id] = true
			}
		}
		err, done := a.bc.sent[cn.id]
		switch {
		case st != session.Connected:
			ui.Text(c, m.SnippetDisconnected()).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
		case done && err != nil:
			// Why, in its tooltip: the row keeps one line.
			why := broadcastErrorText(m, err)
			ui.Row(c.Key("bc:why")).Gap(theme.Space(1)).AlignItems(ui.Center).Shrink(0).Label(why).Tooltip(why).Children(func() {
				ui.Icon(c, icons.Warning.SVG).Size(12, 12).TextColor(tok.Danger)
				ui.Text(c, m.BroadcastNotSent()).FontSize(theme.Caption).TextColor(tok.Danger).SingleLine()
			})
		case done:
			ui.Row(c).Gap(theme.Space(1)).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Icon(c, icons.Check.SVG).Size(12, 12).TextColor(tok.Success)
				ui.Text(c, m.BroadcastSent()).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			})
		}
		dot(c, stateColor(tok, st))
	})
}

// broadcastComposer is where the command is typed: Enter sends it,
// Shift+Enter starts a new line.
func (a *App) broadcastComposer(c *ui.Context, m locales.Messages, tok theme.Tokens, targets int, focus bool) {
	ui.Column(c.Key("bc:composer")).FillWidth().Shrink(0).Padding(theme.Space(3)).Gap(theme.Space(2)).Children(func() {
		in := ui.TextAreaBase(c.Key("bc:draft"), &a.bc.draft).Placeholder(m.BroadcastPlaceholder()).Label(m.BroadcastPlaceholder()).
			FontSize(theme.Body).Font(a.termFamily()).Padding(theme.Space(1.5), theme.Space(2)).FillWidth().Lines(3, 8).
			Radius(theme.RadiusControl).Background(tok.Fill).
			OnTextKey(func(k ui.TextKey) bool {
				if k.Mods != 0 {
					return false
				}
				switch k.Key {
				case ui.KeyEnter:
					text := k.Text()
					a.post(func() {
						a.bc.draft = text
						a.submitBroadcast()
					})
					return true
				case ui.KeyUp, ui.KeyDown:
					// A command of one line goes through those sent; in
					// several, the arrows move the caret.
					if text := k.Text(); !strings.Contains(text, "\n") {
						if s, ok := a.bc.recall(text, k.Key == ui.KeyUp); ok {
							k.SetText(s)
							return true
						}
					}
				}
				return false
			})
		if focus {
			in.Focus()
		}
		ui.Row(c).FillWidth().Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
			run := !a.bc.noEnter
			formCheckbox(c.Key("bc:enter"), &run, m.BroadcastRun()).Shrink(1)
			a.bc.noEnter = !run
			ui.Spacer(c)
			if a.bc.sending > 0 {
				ui.Spinner(c)
			}
			a.menuButton(c, "bc:snippets", IconButton{Icon: icons.API, Label: m.BroadcastSnippet(), Disabled: targets == 0 || a.bc.sending > 0}, func() []menuEntry {
				return a.broadcastSnippetEntries(m)
			})
			empty := strings.TrimSpace(a.bc.draft) == ""
			if (IconButton{Icon: icons.Send, Label: m.BroadcastSend(), Prominent: true, Disabled: empty || targets == 0 || a.bc.sending > 0}).Clicked(c, "bc:send") {
				a.submitBroadcast()
			}
		})
	})
}

// broadcastSnippetEntries are the snippets, by group, each sent to the
// targets as it is chosen.
func (a *App) broadcastSnippetEntries(m locales.Messages) []menuEntry {
	all := a.store.Snippets()
	if len(all) == 0 {
		return []menuEntry{{Label: m.SnippetEmptyTitle(), Disabled: true}}
	}
	var out []menuEntry
	for i, n := range all {
		if i > 0 && n.Group != all[i-1].Group {
			out = append(out, menuSep)
		}
		note := m.SnippetInsert()
		if n.SendEnter {
			note = m.SnippetSendEnter()
		}
		id := n.ID
		out = append(out, menuEntry{Icon: icons.API, Label: n.Name, InlineNote: note, Do: func() { a.broadcastSnippet(id) }})
	}
	return out
}
