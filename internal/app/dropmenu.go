package app

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/aibrands"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// menuEntry is an entry of a menu, the same in the app's drop-down menus
// (menuButton) and context menus (contextMenu), drawn with its icon.
type menuEntry struct {
	Icon *icons.Icon
	// Image keeps a content icon's original colors; DarkImage is its dark-theme variant.
	Image, DarkImage ui.ImageSource
	// Lead, when set, builds what shows in place of the icon, 16 DIPs
	// square, as a host's tile.
	Lead  func()
	Label string
	// Mods and Key are the shortcut shown beside it; Key 0 shows none.
	Mods ui.Modifiers
	Key  ui.Key
	// Note is a line below the label that says more of it; "" for none.
	Note       string
	InlineNote string
	// Tint colors its icon; zero is the muted text color.
	Tint ui.Color
	// Checked marks the entry with a check, Danger tints it red.
	Disabled, Checked, Danger bool
	// Sep makes the entry a separator.
	Sep bool
	// Sub are the entries of its submenu, nil for none.
	Sub []menuEntry
	// SubWidth is the narrowest that submenu grows to; 0 for menuWidth.
	SubWidth float32
	// Do runs as the entry is chosen.
	Do func()
}

// menuSep is a separator between entries.
var menuSep = menuEntry{Sep: true}

// menuWidth is the narrowest a drop-down menu is, menuMaxWidth the widest
// a context menu grows to; menuHeight the most it grows to before it
// scrolls.
const (
	menuWidth    = 200
	menuMaxWidth = 420
	menuHeight   = 480
)

// dropMenu is the drop-down menu open, at most one: the key of its
// button, the entry whose submenu shows (-1 for none), and the level, 0
// the menu and 1 its submenu, whose first entry takes the focus in the
// next frame as it opens, for the arrows (-1 for none). The focus shows
// on it only when it came from the keyboard.
type dropMenu struct {
	key   string
	sub   int
	focus int
}

// ctxMenuKey is the key of the menu open while it is a context menu.
const ctxMenuKey = "\x00context"

// ctxMenu is the context menu open (contextMenu): where it opened, in the
// window's DIPs, and its entries, which run while it shows. n counts the
// menus opened, so that one opened over another is a new popover. win is
// the window it shows in: 0 the main one, else the pane of an editor
// window.
type ctxMenu struct {
	x, y    float32
	win     int
	n       int
	entries func() []menuEntry
	// fresh is set until the menu has shown once: the press that opened
	// it is still a press outside it in that frame.
	fresh bool
}

// contextMenu gives el a context menu drawn as the app's drop-down menus
// are, an icon on each entry, in place of one of the system's: it opens
// where el is right-clicked (the innermost element with one gets the
// click), or below el on Shift+F10 while the focus is in it. entries runs
// as it opens and while it shows.
func (a *App) contextMenu(el ui.Element, entries func() []menuEntry) {
	b := el.Bounds()
	el.HandleInput(func(ev ui.InputEvent) bool { return a.contextPress(b, ev, entries) })
	if el.Shortcut(ui.Shift, ui.KeyF10) {
		a.openContext(b.X, b.Y+b.H, entries)
	}
}

// contextPress opens the context menu of entries for ev, a right press on
// the element at b, and reports whether it did, for an element's own
// HandleInput.
func (a *App) contextPress(b ui.Rect, ev ui.InputEvent, entries func() []menuEntry) bool {
	if ev.Kind != ui.InputPointerDown || ev.Button != 1 {
		return false
	}
	a.openContext(b.X+ev.X, b.Y+ev.Y, entries)
	return true
}

// openContext opens the context menu of entries at (x, y) in the main
// window, closing any other menu.
func (a *App) openContext(x, y float32, entries func() []menuEntry) {
	a.openContextIn(0, x, y, entries)
}

// openContextIn opens the context menu of entries at (x, y) in the window
// win (see ctxMenu), closing any other menu.
func (a *App) openContextIn(win int, x, y float32, entries func() []menuEntry) {
	a.ctx = ctxMenu{x: x, y: y, win: win, n: a.ctx.n + 1, entries: entries, fresh: true}
	a.menu = dropMenu{key: ctxMenuKey, sub: -1, focus: 0}
}

// contextMenuView shows the context menu open in the main window.
func (a *App) contextMenuView(c *ui.Context) { a.contextMenuIn(c, 0) }

// contextMenuIn shows the context menu open in the window win (see
// ctxMenu), above everything else. A press outside it or Escape closes
// it, as a choice does.
func (a *App) contextMenuIn(c *ui.Context, win int) {
	if a.menu.key != ctxMenuKey || a.ctx.entries == nil || a.ctx.win != win {
		return
	}
	entries := a.ctx.entries()
	open := len(entries) > 0
	if open {
		tok := currentTokens(c)
		ui.Overlay(c, func() {
			at := ui.Box(c.Key("ctxat")).Absolute().Left(a.ctx.x).Top(a.ctx.y).Size(1, 1)
			ui.PopoverBase(c.Key("ctxmenu:"+strconv.Itoa(a.ctx.n)), at, &open, func(panel ui.Element) {
				panel.AttachTo(at, ui.AnchorTopStart, ui.AnchorTopStart).MaxWidth(menuMaxWidth)
				a.menuPanel(c, tok, panel, entries, 0)
			})
		})
		if a.ctx.fresh {
			a.ctx.fresh, open = false, true
		}
	}
	if !open && a.menu.key == ctxMenuKey {
		a.menu, a.ctx.entries = dropMenu{}, nil
	}
}

// menuLabels are the labels of entries, "-" for separators, as a test
// reads a menu.
func menuLabels(entries []menuEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Sep {
			out = append(out, "-")
		} else {
			out = append(out, e.Label)
		}
	}
	return out
}

// menuButton is b opening a drop-down menu of entries below it, under
// key, which is unique in the window: the app's own, drawn as its
// popovers are, an icon on each entry. entries runs only while it shows.
func (a *App) menuButton(c *ui.Context, key string, b IconButton, entries func() []menuEntry) ui.Element {
	b.Active = b.Active || a.menu.key == key
	return a.dropDown(c, key, b.Show(c, key), b.Disabled, entries)
}

// menuChip is ch opening a drop-down menu of entries below it, under key,
// as menuButton is an IconButton: for a choice that shows what is chosen.
func (a *App) menuChip(c *ui.Context, key string, ch chip, entries func() []menuEntry) ui.Element {
	ch.open = a.menu.key == key
	el := ch.show(c, key)
	layout := menuLayout{compact: ch.CompactMenu}
	if ch.FitMenu {
		layout.maxHeight = menuHeightAt(c, el.Bounds())
	}
	return a.dropDown(c, key, el, false, entries, layout)
}

// dropDown makes el, a button under key, open a drop-down menu of entries
// below it while it is not disabled.
func (a *App) dropDown(c *ui.Context, key string, el ui.Element, disabled bool, entries func() []menuEntry, options ...menuLayout) ui.Element {
	open := a.menu.key == key
	el.Role(ui.RoleMenuButton).Expanded(open)
	if el.Clicked() && !disabled {
		if open {
			a.menu = dropMenu{}
		} else {
			a.menu = dropMenu{key: key, sub: -1, focus: 0}
		}
		open = !open
	}
	if !open {
		return el
	}
	tok := currentTokens(c)
	ui.PopoverBase(c.Key("menu:"+key), el, &open, func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0)
		a.menuPanel(c, tok, panel, entries(), 0, options...)
	})
	if !open {
		a.menu = dropMenu{}
	}
	return el
}

type menuLayout struct {
	compact   bool
	maxHeight float32
	// width is the narrowest the panel grows to; 0 for menuWidth.
	width float32
}

// menuHeightAt reserves the popover gap, window margin and panel insets
// on the roomier side of the anchor, without changing other menus.
func menuHeightAt(c *ui.Context, anchor ui.Rect) float32 {
	_, height := c.Size()
	return min(menuHeight, max(1, max(anchor.Y, height-anchor.Y-anchor.H)-theme.Space(5)))
}

// menuPanel lays out the panel of a drop-down menu at level, 0 the menu
// and 1 a submenu of it, with its entries.
func (a *App) menuPanel(c *ui.Context, tok theme.Tokens, panel ui.Element, entries []menuEntry, level int, options ...menuLayout) {
	layout := menuLayout{maxHeight: menuHeight}
	if len(options) > 0 {
		layout.compact = options[0].compact
		if options[0].maxHeight > 0 {
			layout.maxHeight = options[0].maxHeight
		}
		if options[0].width > 0 {
			layout.width = options[0].width
		}
	}
	compact := layout.compact
	for _, e := range entries {
		if e.Icon != nil || e.Image != nil || e.Lead != nil || e.Note != "" {
			compact = false // Illustrated or multi-line menus retain their icon column.
		}
	}
	width := float32(menuWidth)
	if layout.width > 0 {
		width = layout.width
	}
	if compact {
		width = theme.Space(28)
	}
	panel.Role(ui.RoleMenu).MinWidth(width).Padding(5).Radius(theme.RadiusCard).Background(tok.Inset).
		Border(1, tok.GlassEdge).FocusGroup(ui.Vertical)
	tok.Lift.On(panel)
	focus := a.menu.focus == level
	if focus {
		a.menu.focus = -1
	}
	panel.Children(func() {
		gutterScroll(c, ui.Scroll(c.Key("scroll")).MaxHeight(layout.maxHeight).FillWidth()).Children(func() {
			ui.Column(c).FillWidth().Children(func() {
				for i, e := range entries {
					if e.Sep {
						ui.Box(c.Key("sep:"+strconv.Itoa(i))).Height(1).Margin(4, 8).Background(tok.Line)
						continue
					}
					row := a.menuRow(c, tok, e, i, level, compact)
					if focus && !e.Disabled {
						row.Focus()
						focus = false
					}
				}
			})
		})
	})
}

// menuRow is the i-th entry e of a drop-down menu at level: lit in a
// neutral tint (a danger entry's red) under the pointer or the keyboard's
// focus; its submenu after it, which the pointer or the right arrow opens.
func (a *App) menuRow(c *ui.Context, tok theme.Tokens, e menuEntry, i, level int, compactMenu ...bool) ui.Element {
	row := ui.ButtonBase(c.Key("mi:"+strconv.Itoa(i))).Role(ui.RoleMenuItem).FillWidth().Justify(ui.Start).
		Padding(6, 8).Gap(10).Radius(theme.RadiusSmall).FocusRing(false).Disabled(e.Disabled)
	if !row.IsDisabled() {
		row.Cursor(ui.CursorPointer)
	}
	if e.Checked {
		row.Role(ui.RoleMenuItemCheckBox).Checked(true)
	}
	strong := float32(0.12)
	if tok.Dark {
		strong = 0.22
	}
	subOpen := level == 0 && e.Sub != nil && a.menu.sub == i
	if level == 0 && row.Hovered() && a.menu.sub != i && !e.Disabled {
		// The pointer moves the submenu shown onto its entry's, or
		// closes it over another.
		a.menu.sub = -1
		if e.Sub != nil {
			a.menu.sub = i
		}
		c.Invalidate()
	}
	lit := !e.Disabled && (row.Hovered() || row.FocusVisible() || subOpen)
	fg, muted := tok.Text, tok.TextMuted
	switch {
	case e.Disabled:
		row.Opacity(0.4)
	case e.Danger:
		fg, muted = tok.Danger, tok.Danger
		if lit && row.Pressed() {
			row.Background(tok.Danger.Alpha(strong * 1.8))
		} else if lit {
			row.Background(tok.Danger.Alpha(strong))
		}
	case lit:
		row.Background(tok.Press)
	}
	row.Children(func() {
		var ic ui.Element
		if len(compactMenu) == 0 || !compactMenu[0] {
			ic = ui.Box(c).Size(16, 16).Shrink(0)
		}
		tint := muted
		if e.Tint != (ui.Color{}) && !(lit && e.Danger) && !e.Disabled {
			tint = e.Tint
		}
		switch {
		case e.Lead != nil:
			ic.Children(e.Lead)
		case e.Image != nil:
			src := e.Image
			if tok.Dark && e.DarkImage != nil {
				src = e.DarkImage
			}
			ic.Children(func() { ui.Image(c, src).Size(16, 16) })
		case e.Icon != nil:
			ic.Children(func() { ui.Icon(c, e.Icon.SVG).Size(16, 16).TextColor(tint) })
		}
		if e.Note == "" {
			ui.Text(c, e.Label).FontSize(theme.Body).TextColor(fg).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		} else {
			ic.AlignSelf(ui.Start).Margin(1, 0, 0, 0)
			ui.Column(c).Gap(1).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, e.Label).FontSize(theme.Body).TextColor(fg).SingleLine()
				ui.Text(c, e.Note).FontSize(theme.Caption).TextColor(muted).SingleLine()
			})
		}
		if e.InlineNote != "" {
			ui.Text(c, e.InlineNote).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(0)
		}
		if e.Key != 0 {
			ui.Text(c, shortcutText(runtime.GOOS, e.Mods, e.Key)).FontSize(theme.Footnote).TextColor(muted).SingleLine().
				Shrink(0).MarginStart(theme.Space(4))
		}
		if e.Checked {
			ui.Icon(c, icons.Check.SVG).Size(14, 14).Shrink(0).TextColor(tok.Accent)
		}
		if e.Sub != nil {
			ui.Icon(c, icons.Submenu.SVG).Size(14, 14).Shrink(0).TextColor(muted)
		}
	})
	if e.Disabled {
		return row
	}
	if e.Sub != nil {
		if row.Clicked() || row.Shortcut(0, ui.KeyRight) {
			a.menu.sub, a.menu.focus = i, 1
			subOpen = true
		}
		if subOpen {
			open := true
			ui.PopoverBase(c.Key("sub:"+strconv.Itoa(i)), row, &open, func(panel ui.Element) {
				panel.AttachTo(row, ui.AnchorTopEnd, ui.AnchorTopStart).Margin(0, 0, 0, 6).Top(-6)
				a.menuPanel(c, tok, panel, e.Sub, 1, menuLayout{width: e.SubWidth})
				if panel.OverlayShortcut(0, ui.KeyLeft) {
					open = false
					row.Focus()
				}
			})
			if !open && a.menu.sub == i {
				a.menu.sub = -1
			}
		}
		return row
	}
	if row.Clicked() {
		a.menu = dropMenu{}
		if e.Do != nil {
			e.Do()
		}
	}
	return row
}

// shortcutText is a shortcut as a menu shows it on goos: "⌘K" on macOS,
// "Ctrl+Shift+K" elsewhere.
func shortcutText(goos string, mods ui.Modifiers, key ui.Key) string {
	sep := "+"
	if goos == "darwin" {
		sep = ""
	}
	return strings.Join(keyCaps(goos, mods, key), sep)
}

// chip is a capsule button showing a choice, its icon and label in tint
// over a light wash of it, and a chevron: what opens a drop-down menu of
// the choices (menuChip), as a permission or a model.
type chip struct {
	Icon  *icons.Icon
	Brand *aibrands.Icon
	Label string
	// Tip names what it chooses, from the locale catalog: its tooltip
	// and accessible label.
	Tip string
	// Tint colors it; zero is the muted text color.
	Tint ui.Color
	// Bare shows the icon alone, where there is no room for the label;
	// LabelMax narrows the label, 0 for chipLabelWidth.
	Bare     bool
	LabelMax float32
	open     bool
	// CompactMenu narrows a text-only choice menu and omits its empty icon column.
	CompactMenu bool
	// FitMenu limits the scroll height to the room above or below this chip.
	FitMenu bool
}

// fit is ch at fit, in a pane of controls: bare at aiIcons, its label
// at most short wide at aiShort when it shows bare otherwise. A bare
// chip names what is chosen in its tooltip.
func (ch chip) fit(m locales.Messages, fit aiFit, short float32) chip {
	switch {
	case fit == aiIcons || fit == aiShort && short == 0:
		ch.Bare = true
		ch.Tip = m.AiChipValue(ch.Tip, ch.Label)
	case fit == aiShort:
		ch.LabelMax = short
	}
	return ch
}

// chipLabelWidth is the widest a chip's label grows; a longer one is cut.
const chipLabelWidth = 150

// show builds the chip under key and returns it.
func (ch chip) show(c *ui.Context, key string) ui.Element {
	tok := currentTokens(c)
	tint, wash := ch.Tint, float32(0.12)
	if tint == (ui.Color{}) {
		tint, wash = tok.TextMuted, 0.10
	}
	el := ui.ButtonBase(c.Key("chip:"+key)).Label(ch.Tip).Tooltip(ch.Tip).Padding(3, 6, 3, 8).Gap(4).
		AlignItems(ui.Center).Radius(theme.Capsule).Shrink(0).Cursor(ui.CursorPointer)
	if ch.Bare {
		el.Padding(3, 4, 3, 6).Gap(2)
	}
	switch {
	case el.Pressed():
		wash *= 2.4
	case el.Hovered() || ch.open:
		wash *= 1.7
	}
	el.Background(tint.Alpha(wash))
	el.Children(func() {
		if ch.Brand != nil {
			brandImage(c, ch.Brand, 14)
		} else {
			ui.Icon(c, ch.Icon.SVG).Size(14, 14).Shrink(0).TextColor(tint)
		}
		if !ch.Bare {
			w := ch.LabelMax
			if w == 0 {
				w = chipLabelWidth
			}
			ui.Text(c, ch.Label).FontSize(theme.Footnote).FontWeight(500).TextColor(tint).SingleLine().MaxWidth(w)
		}
		ui.Icon(c, icons.Collapse.SVG).Size(12, 12).Shrink(0).TextColor(tint)
	})
	return el
}
