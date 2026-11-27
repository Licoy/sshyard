package app

import (
	"strconv"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// barAction is an action of a bar that gives way to its width
// (actionBar): its IconButton while the bar has room for it, else an
// entry of the bar's More menu.
type barAction struct {
	Key    string
	Button IconButton
	// Do runs as it is clicked or chosen from the menu.
	Do func()
	// Show, when set, draws it in place of Button, as a spinner or a
	// button with a menu of its own: it stays in the bar and takes the
	// room it took last frame.
	Show func() ui.Element
}

// entry is the action as an entry of a More menu: its badge as a note,
// checked while it is on.
func (b barAction) entry() menuEntry {
	ic := b.Button.Icon
	if b.Button.Active && b.Button.On != nil {
		ic = b.Button.On
	}
	return menuEntry{Icon: ic, Label: b.Button.Label, InlineNote: b.Button.Badge, Checked: b.Button.Active,
		Disabled: b.Button.Disabled, Danger: b.Button.Danger, Do: b.Do}
}

// barFit is how many of the buttons, as wide as widths and gap apart,
// show in room, from the first, the others going into a More button as
// wide as more: all of them when they fit, else as many as fit beside
// it. menu keeps the More button even when all fit.
func barFit(widths []float32, gap, room, more float32, menu bool) int {
	if !menu {
		var total float32
		for i, w := range widths {
			if i > 0 {
				total += gap
			}
			total += w
		}
		if total <= room {
			return len(widths)
		}
	}
	used, k := more, 0
	for k < len(widths) && used+gap+widths[k] <= room {
		used += gap + widths[k]
		k++
	}
	return k
}

// actionBar draws acts gap apart, under key, unique in the app: as many
// as fit in a row w wide less reserve, what the row's other parts keep,
// and those that do not, from the last, as entries of a More menu after
// them, over menu's own entries (nil for none). w is the row's width last
// frame, 0 before it was laid out, when all show.
func (a *App) actionBar(c *ui.Context, key string, w, reserve, gap float32, acts []barAction, menu func() []menuEntry, m locales.Messages) {
	if a.barWidths == nil {
		a.barWidths = map[string]float32{}
	}
	room := w - reserve
	size := float32(Tiny)
	var widths []float32
	for _, b := range acts {
		if b.Show != nil {
			room -= a.barWidths[key+"/"+b.Key] + gap
			continue
		}
		size = b.Button.Size
		widths = append(widths, b.Button.width())
	}
	more := IconButton{Icon: icons.More, Label: m.MoreActions(), Size: size}
	fit := len(widths)
	if w > 0 {
		fit = barFit(widths, gap, room, more.width(), menu != nil)
	}
	if len(acts) == 0 && menu == nil {
		return
	}
	ui.Row(c.Key("bar:" + key)).Shrink(0).Gap(gap).AlignItems(ui.Center).Children(func() {
		var hidden []barAction
		i := 0
		for _, b := range acts {
			if b.Show != nil {
				a.barWidths[key+"/"+b.Key] = b.Show().Bounds().W
				continue
			}
			if i++; i > fit {
				hidden = append(hidden, b)
				continue
			}
			if b.Button.Clicked(c, b.Key) && b.Do != nil {
				b.Do()
			}
		}
		if len(hidden) == 0 && menu == nil {
			return
		}
		a.menuButton(c, "more:"+key, more, func() []menuEntry {
			var es []menuEntry
			for _, b := range hidden {
				es = append(es, b.entry())
			}
			if menu != nil {
				if len(es) > 0 {
					es = append(es, menuSep)
				}
				es = append(es, menu()...)
			}
			return es
		})
	})
}

// paneTitleMin is the narrowest a pane header's title gives way to its
// actions.
const paneTitleMin = 96

// titleRoom is the room a pane header keeps for title: as wide as it is,
// up to paneTitleMin.
func titleRoom(c *ui.Context, title string) float32 {
	w, _ := c.MeasureText(0, ui.Span{Text: title, Size: theme.Footnote, Weight: 600})
	return min(w, paneTitleMin)
}

// paneHead is the header row of pane cn, which drags it: lead, what the
// pane is, giving way to the actions down to leadMin wide and cut beyond;
// then the pane's actions, as many as fit (actionBar); then its close
// button, always there.
func (a *App) paneHead(c *ui.Context, cn *conn, m locales.Messages, lead func(), leadMin float32, acts []barAction, menu func() []menuEntry) ui.Element {
	row := ui.Row(c.Key("head")).Height(paneHeadHeight).Shrink(0).Padding(0, 6, 0, 10).Gap(8).AlignItems(ui.Center).
		Drag(dragSession{ID: cn.id}).Label(m.PaneActions())
	closeB := IconButton{Icon: icons.Close, Label: m.ClosePane(), Size: Tiny, Danger: true}
	reserve := 10 + 6 + leadMin + 8 + 8 + closeB.width()
	row.Children(func() {
		ui.Row(c.Key("lead")).Grow(1).Shrink(1).MinWidth(0).Clip().Gap(8).AlignItems(ui.Center).Children(lead)
		a.actionBar(c, "head:"+strconv.Itoa(cn.id), row.Bounds().W, reserve, 8, acts, menu, m)
		if closeB.Clicked(c, "close") {
			a.closeSoon(cn.id)
		}
	})
	a.tearPane(c, row, cn.id)
	return row
}
