package app

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// crumb is a folder of the path a side's breadcrumbs show: its name and
// its path. The first is the root, whose name is its path.
type crumb struct{ name, path string }

// crumbsOf are the folders of dir from its root, on the server (remote)
// or this computer.
func crumbsOf(dir string, remote bool) []crumb {
	if dir == "" {
		return nil
	}
	if remote {
		dir = path.Clean(dir)
		var out []crumb
		p := ""
		if strings.HasPrefix(dir, "/") {
			out, p = append(out, crumb{"/", "/"}), "/"
		}
		for _, part := range strings.Split(dir, "/") {
			if part == "" {
				continue
			}
			p = path.Join(p, part)
			if len(out) == 0 {
				p = part + "/" // a root of its own, as "C:/"
			}
			out = append(out, crumb{part, p})
		}
		return out
	}
	dir = filepath.Clean(dir)
	vol := filepath.VolumeName(dir)
	root := vol + string(filepath.Separator)
	out := []crumb{{root, root}}
	p := root
	for _, part := range strings.Split(dir[len(vol):], string(filepath.Separator)) {
		if part == "" {
			continue
		}
		p = filepath.Join(p, part)
		out = append(out, crumb{part, p})
	}
	return out
}

// crumbFit is which of crumbs, as wide as widths, show in room: all when
// they fit, else the root, the folders nearest the end that fit and the
// end, with the folders between them (hidden) behind an ellipsis, as
// wide as more. room 0 (not laid out yet) shows all.
func crumbFit(widths []float32, room, more float32) (shown []int, hidden []int) {
	n := len(widths)
	var total float32
	for _, w := range widths {
		total += w
	}
	if room <= 0 || total <= room || n <= 2 {
		for i := range n {
			shown = append(shown, i)
		}
		return shown, nil
	}
	budget := room - widths[0] - widths[n-1] - more
	first := n - 1
	for first > 1 && budget >= widths[first-1] {
		first--
		budget -= widths[first]
	}
	shown = []int{0, -1}
	for i := first; i < n; i++ {
		shown = append(shown, i)
	}
	for i := 1; i < first; i++ {
		hidden = append(hidden, i)
	}
	return shown, hidden
}

// The breadcrumbs' measures: the room around a crumb's text, the
// chevron after it, the root's icon and the ellipsis.
const (
	crumbPad     = 8
	crumbChevron = 16
	crumbIcon    = 12
)

// crumbsBar is the path of a side's folder, between its header and its
// rows: each folder a crumb that goes to it, the folder shown last.
// Resting the pointer on a crumb shows what its folder holds, to go to;
// folders that do not fit hide behind an ellipsis, which shows them the
// same way.
func (a *App) crumbsBar(c *ui.Context, cn *conn, remote bool, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	sd := fs.side(remote)
	if sd.typing {
		a.pathField(c, cn, remote, m)
		return
	}
	cs := crumbsOf(sd.dir, remote)
	if len(cs) == 0 {
		return
	}
	key, rootIcon := "lcrumbs", icons.LocalDisk
	if remote {
		key, rootIcon = "rcrumbs", icons.Host
	}
	bar := ui.Row(c.Key(key)).Height(22).Shrink(0).FillWidth().MinWidth(0).AlignItems(ui.Center).Padding(0, 2).
		Role(ui.RoleGroup).Label(m.CrumbsLabel())
	// A double click beside the crumbs types a path, as the button does.
	bar.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown && ev.Button == 0 && ev.Clicks == 2 {
			sd.startTyping()
			return true
		}
		return false
	})
	widths := make([]float32, len(cs))
	for i, cr := range cs {
		w := float32(crumbIcon)
		if i > 0 {
			w, _ = c.MeasureText(0, ui.Span{Text: shownName(cr.name), Size: theme.Footnote})
		}
		widths[i] = w + crumbPad + crumbChevron
	}
	room := bar.Bounds().W - 4 - crumbTypeButton
	if n := sd.chosen.Len(); n > 1 {
		w, _ := c.MeasureText(0, ui.Span{Text: m.ItemsSelected(n), Size: theme.Caption})
		room -= w + theme.Space(3)
	}
	shown, hidden := crumbFit(widths, room, crumbIcon+crumbPad+crumbChevron)
	menuKey := "crumb:" + strconv.Itoa(cn.id) + ":" + key + ":"
	bar.Children(func() {
		for k, i := range shown {
			if k > 0 {
				ui.Icon(c, icons.Submenu.SVG).Size(12, 12).Shrink(0).Margin(0, 2).TextColor(tok.TextMuted)
			}
			if i < 0 {
				el := crumbButton(c, tok, "crumbmore", m.CrumbsHidden(), icons.More, false, false)
				a.crumbMenu(c, fs, el, menuKey+"\x00more", func() []menuEntry {
					out := make([]menuEntry, len(hidden))
					for j, h := range hidden {
						p := cs[h].path
						out[j] = fileMenuEntry(cs[h].name, true)
						out[j].Do = func() { a.goTo(cn, remote, p) }
					}
					return out
				})
				continue
			}
			cr, last := cs[i], i == len(cs)-1
			var ic *icons.Icon
			name := shownName(cr.name)
			if i == 0 {
				ic, name = rootIcon, cr.path
			}
			el := crumbButton(c, tok, "crumb:"+strconv.Itoa(i), name, ic, last, i > 0)
			if el.Clicked() {
				fs.crumbHold, a.menu = menuKey+cr.path, dropMenu{}
				if !last {
					a.goTo(cn, remote, cr.path)
				}
			}
			next := ""
			if !last {
				next = cs[i+1].name
			}
			a.crumbMenu(c, fs, el, menuKey+cr.path, func() []menuEntry { return a.crumbEntries(cn, remote, cr.path, next, m) })
		}
		ui.Spacer(c)
		if n := sd.chosen.Len(); n > 1 {
			ui.Text(c, m.ItemsSelected(n)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(0).MarginStart(theme.Space(3))
		}
		if (IconButton{Icon: icons.Edit, Label: m.TypePath(), Size: Tiny}).Clicked(c, "typepath") {
			sd.startTyping()
		}
	})
}

// crumbTypeButton is the room the button that types a path takes.
const crumbTypeButton = 20

// startTyping shows the side's path field on the folder shown.
func (sd *sideList) startTyping() {
	sd.typing, sd.typed, sd.typings = true, sd.dir, sd.typings+1
}

// pathField is the field a side's path is typed in, in place of its
// breadcrumbs: it opens on the folder shown, selected, so that a path
// pasted replaces it. Enter goes to the folder typed; Escape, or the
// focus leaving, gives the breadcrumbs back.
func (a *App) pathField(c *ui.Context, cn *conn, remote bool, m locales.Messages) {
	sd := cn.fs.side(remote)
	key := "lpath"
	if remote {
		key = "rpath"
	}
	ui.Row(c.Key(key)).MinHeight(22).Shrink(0).FillWidth().MinWidth(0).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		in := formTextInput(c.Key("in:"+strconv.Itoa(sd.typings)), &sd.typed).Label(m.CrumbsLabel()).
			Placeholder(m.PathPlaceholder()).FontSize(theme.Footnote).Grow(1).MinWidth(0).AutoFocus()
		if sd.typeHold.key != any(sd.typings) {
			in.SetTextSelection(0, utf8.RuneCountInString(sd.typed))
		}
		sd.typeHold.apply(c, sd.typings, in)
		focused := in.Focused()
		switch {
		case in.Submitted():
			a.goToTyped(cn, remote, sd.typed)
		case in.Shortcut(0, ui.KeyEscape), sd.typeFocus && !focused:
			sd.typing = false
		}
		sd.typeFocus = focused && sd.typing
	})
}

// goToTyped goes to the folder typed in a side's path field, and gives
// the breadcrumbs back.
func (a *App) goToTyped(cn *conn, remote bool, typed string) {
	sd := cn.fs.side(remote)
	sd.typing, sd.typeFocus = false, false
	home := a.localHome()
	if remote {
		home = cn.fs.remote.home
	}
	if dir := typedPath(typed, sd.dir, home, remote); dir != "" && dir != sd.dir {
		a.goTo(cn, remote, dir)
	}
}

// typedPath is the folder a path typed on a side names: from the root,
// from home with "~", or else from the folder dir. Quotes round a pasted
// path go; on the server, backslashes are slashes. "" is none.
func typedPath(typed, dir, home string, remote bool) string {
	p := strings.TrimSpace(typed)
	if len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0] {
		p = strings.TrimSpace(p[1 : len(p)-1])
	}
	if p == "" {
		return ""
	}
	if !remote {
		switch {
		case p == "~":
			p = home
		case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)):
			p = filepath.Join(home, p[2:])
		case !filepath.IsAbs(p):
			p = filepath.Join(dir, p)
		}
		return filepath.Clean(p)
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if home == "" {
		home = "/"
	}
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = path.Join(home, p[2:])
	case strings.HasPrefix(p, "/") || len(p) >= 2 && p[1] == ':':
	default:
		p = path.Join(dir, p)
	}
	return path.Clean(p)
}

// crumbButton is a crumb: an icon, or the folder's name (named shows it,
// with the icon as its label), lit under the pointer. The last, the
// folder shown, is in the text's color and bold.
func crumbButton(c *ui.Context, tok theme.Tokens, key, name string, ic *icons.Icon, last, named bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(name).Padding(2, 4).Radius(theme.RadiusSmall).AlignItems(ui.Center).MinWidth(0).Cursor(ui.CursorPointer)
	fg := tok.TextMuted
	switch {
	case b.Pressed():
		b.Background(tok.Text.Alpha(0.14))
		fg = tok.Text
	case b.Hovered():
		b.Background(tok.Text.Alpha(0.08))
		fg = tok.Text
	}
	if last {
		fg = tok.Text
		b.Shrink(1)
	} else {
		b.Shrink(0)
	}
	b.Children(func() {
		if ic != nil && !named {
			ui.Icon(c, ic.SVG).Size(crumbIcon, crumbIcon).TextColor(fg)
			return
		}
		t := ui.Text(c, name).FontSize(theme.Footnote).TextColor(fg).SingleLine().Shrink(1).MinWidth(0)
		if last {
			t.FontWeight(600)
		}
	})
	return b
}

// crumbRest is how long the pointer rests on a crumb before its menu
// shows, crumbLinger how long the menu stays after the pointer leaves the
// crumb and the menu.
const (
	crumbRest   = 350 * time.Millisecond
	crumbLinger = 300 * time.Millisecond
)

// crumbMenu shows the menu of a crumb el under key, from entries, while
// the pointer is on el or on the menu: once it rests on el (at once when
// another crumb's menu shows), until a moment after it leaves both, a
// press outside, Escape or a choice. A crumb clicked shows no menu until
// the pointer leaves it.
func (a *App) crumbMenu(c *ui.Context, fs *filesState, el ui.Element, key string, entries func() []menuEntry) {
	now := c.Now()
	hovered := el.Hovered()
	open := a.menu.key == key
	switch {
	case !hovered:
		if fs.crumbHover == key {
			fs.crumbHover = ""
		}
		if fs.crumbHold == key {
			fs.crumbHold = ""
		}
	case open || fs.crumbHold == key:
	case strings.HasPrefix(a.menu.key, "crumb:"):
		a.openCrumb(fs, key)
		open = true
	case a.menu.key == "":
		if fs.crumbHover != key {
			fs.crumbHover, fs.crumbSince = key, now
		}
		if wait := crumbRest - now.Sub(fs.crumbSince); wait > 0 {
			c.After(wait)
		} else {
			a.openCrumb(fs, key)
			open = true
		}
	}
	if !open {
		return
	}
	tok := currentTokens(c)
	still, over := true, false
	ui.PopoverBase(c.Key("menu:"+key), el, &still, func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).MaxWidth(menuMaxWidth)
		a.menuPanel(c, tok, panel, entries(), 0)
		over = panel.Hovered()
	})
	switch {
	case hovered || over:
		fs.crumbLeft = time.Time{}
	case fs.crumbLeft.IsZero():
		fs.crumbLeft = now
		c.After(crumbLinger)
	case now.Sub(fs.crumbLeft) >= crumbLinger:
		still = false
	default:
		c.After(crumbLinger - now.Sub(fs.crumbLeft))
	}
	if !still && a.menu.key == key {
		a.menu, fs.crumbLeft = dropMenu{}, time.Time{}
	}
}

// openCrumb shows the menu of a crumb, listing its folder anew. It takes
// no focus, so the keys stay where they are.
func (a *App) openCrumb(fs *filesState, key string) {
	a.menu = dropMenu{key: key, sub: -1, focus: -1}
	fs.peek, fs.crumbLeft = peekList{}, time.Time{}
}

// peekList is a folder listed for a crumb's menu, on one side.
type peekList struct {
	remote  bool
	dir     string
	entries []sftpsvc.Entry
	err     string
	loading bool
}

// crumbShown is how many entries a crumb's menu shows.
const crumbShown = 300

// crumbEntries are the menu of the crumb of the folder dir on a side:
// what it holds, folders first, the one the path goes on into (next)
// checked. A folder goes there; a file goes to dir and is chosen there.
func (a *App) crumbEntries(cn *conn, remote bool, dir, next string, m locales.Messages) []menuEntry {
	fs := cn.fs
	sd := fs.side(remote)
	es, failed, loading := sd.entries, "", false
	if dir != sd.dir {
		pk := &fs.peek
		if pk.dir != dir || pk.remote != remote {
			a.peekAt(cn, remote, dir)
		}
		es, failed, loading = visibleEntries(pk.entries, sd.showHidden), pk.err, pk.loading
	}
	switch {
	case loading:
		return []menuEntry{{Label: m.Loading(), Disabled: true}}
	case failed != "":
		return []menuEntry{{Icon: icons.Warning, Label: securityText(m, failed), Disabled: true}}
	case len(es) == 0:
		return []menuEntry{{Label: m.Empty(), Disabled: true}}
	}
	out := make([]menuEntry, 0, min(len(es), crumbShown+1))
	for i, e := range es {
		if i == crumbShown {
			out = append(out, menuEntry{Label: m.SkippedMore(strconv.Itoa(len(es) - i)), Disabled: true})
			break
		}
		p, name := sidePath(remote, dir, e.Name), e.Name
		en := fileMenuEntry(name, e.IsDir)
		en.Checked = e.IsDir && name == next
		if e.IsDir {
			en.Do = func() { a.goTo(cn, remote, p) }
		} else {
			en.Do = func() { a.reveal(cn, remote, dir, name) }
		}
		out = append(out, en)
	}
	return out
}

// reveal goes to the folder dir of a side and chooses name there.
func (a *App) reveal(cn *conn, remote bool, dir, name string) {
	sd := cn.fs.side(remote)
	sd.reveal = name
	if dir == sd.dir {
		sd.show(dir, sd.listed)
		return
	}
	a.goTo(cn, remote, dir)
}

// peekAt lists the folder dir of a side for a crumb's menu: the local
// side at once, the remote side off the UI thread.
func (a *App) peekAt(cn *conn, remote bool, dir string) {
	fs := cn.fs
	if !remote {
		es, err := sftpsvc.ListLocal(dir)
		fs.peek = peekList{dir: dir, entries: es, err: errString(err)}
		return
	}
	c := fs.remote.client
	if c == nil {
		fs.peek = peekList{remote: true, dir: dir, err: errNotConnected.Error()}
		return
	}
	fs.peek = peekList{remote: true, dir: dir, loading: true}
	go func() {
		es, err := c.List(dir)
		a.post(func() {
			if pk := &fs.peek; pk.remote && pk.dir == dir {
				*pk = peekList{remote: true, dir: dir, entries: es, err: errString(err)}
			}
		})
	}()
}
