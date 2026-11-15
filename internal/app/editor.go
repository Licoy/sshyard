package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// edWin is an editor window: the files of a host's server, or of this
// computer, open in tabs beside a tree of their folders. Its conn has the
// window's own (pooled) connection to the host, and a files state for
// what the folders do there: the questions, the transfers, the sheets.
type edWin struct {
	local bool
	tabs  []*editState
	at    int // the tab shown, -1 for none
	// reveal scrolls the tab shown into view, once.
	reveal bool
	tree   fileTree
	// treeW is the width of the folders; treeSeen and treeAt where it was
	// last and since when, to save it once it rests.
	treeW    float32
	treeSeen float32
	treeAt   time.Time
	// win is the window once it is open, title and edited what it was
	// last told.
	win    Window
	title  string
	edited bool
	// dirty is whether a tab has unsaved changes, for the window's close,
	// which asks on no frame.
	dirty atomic.Bool
	// closeOpen asks what to do with the unsaved tabs as the window
	// closes; closing closes it once the saves asked for there are done.
	closeOpen bool
	closing   bool
	// shut is a tab closing with unsaved changes, shutOpen its question.
	shut     *editState
	shutOpen bool
	// toasts show in the window at its next frame.
	toasts []func(m locales.Messages) ui.Toast
}

// editState is a file open in a tab of an editor window: where it is, its
// text (or picture, for an image), and the version of the file it was
// read or last saved as.
type editState struct {
	path, name string
	st         *editor.State // nil for an image
	img        *imageTab
	ver        sftpsvc.Version
	saving     bool
	loading    bool
	err        string
	// lang is the name of the syntax the text is colored as, "" for
	// plain text.
	lang string
	// conflictOpen asks what to do with a file changed since it was read.
	conflictOpen bool
	// closeSaved closes the tab once the save asked for as it closed is
	// done.
	closeSaved bool
	// focused is set once the text took the focus since the tab showed.
	focused bool
}

// modified is whether the tab has unsaved changes.
func (ed *editState) modified() bool { return ed.st != nil && ed.st.Buffer.Modified() }

// imageTab is the picture of an image open in a tab, read once started.
type imageTab struct {
	bmp     *ui.Bitmap
	big     bool // too large to show
	started bool
	cancel  context.CancelFunc
	loads   int // the reads started, the last of which shows
}

// editorReadLimit is how much reading a file again for the editor
// reads: explicitly confirmed files may be large, but every in-memory
// read has a ceiling.
func editorReadLimit(size int64) int64 {
	if size > sftpsvc.DefaultReadLimit {
		return editorHardReadLimit
	}
	return sftpsvc.DefaultReadLimit
}

// editorWin is the editor window on the host's server, or on this
// computer (remote false), whatever host the files came from.
func (a *App) editorWin(hostID string, remote bool) *conn {
	for _, c := range a.conns {
		if ew := c.ed; ew != nil && ew.local == !remote && (!remote || c.host.ID == hostID) {
			return c
		}
	}
	return nil
}

// editorTab is the tab showing the file p, of the host's server or of
// this computer, and its window.
func (a *App) editorTab(hostID string, remote bool, p string) (*conn, *editState) {
	if cn := a.editorWin(hostID, remote); cn != nil {
		for _, ed := range cn.ed.tabs {
			if ed.path == p {
				return cn, ed
			}
		}
	}
	return nil, nil
}

// showTab shows the tab ed of an editor window, with its file in the
// folders, and brings the window to the front.
func (a *App) showTab(cn *conn, ed *editState) {
	ew := cn.ed
	if i := slices.Index(ew.tabs, ed); i >= 0 {
		ew.at, ew.reveal, ed.focused = i, true, false
	}
	a.treeReveal(cn, ed.path)
	if w := ew.win; w != nil {
		go w.Focus()
	}
	a.invalidate()
}

// editorFor is the editor window for files of the pane from (a files pane
// or an editor window) on its host's server (remote), or on this computer:
// the one open, else a new one, showing the folder dir.
func (a *App) editorFor(from *conn, remote bool, dir string) (*conn, error) {
	if cn := a.editorWin(from.host.ID, remote); cn != nil {
		return cn, nil
	}
	if a.p.OpenWindow == nil {
		return nil, errors.New("no window for the editor")
	}
	var cn *conn
	if remote {
		c, err := a.makeConn(from.host.ID, layout.Editor, 0)
		if err != nil {
			return nil, err
		}
		cn = c
		cn.fs.remote.dir = dir
	} else {
		a.nextConn++
		cn = &conn{id: a.nextConn, kind: layout.Editor, host: a.localHost(localID), fs: a.newFiles()}
		a.conns[cn.id] = cn
	}
	cn.ed = &edWin{local: !remote, at: -1, treeW: a.settings.EditorTreeWidth}
	cn.ed.tree.reset(dir)
	if remote {
		a.startConn(cn)
	}
	a.openEditorWindow(cn)
	return cn, nil
}

// openFileEditor opens the file read, data, in a tab of the editor window
// of its host (or of this computer), which opens if there is none.
func (a *App) openFileEditor(from *conn, remote bool, p string, data []byte, ver sftpsvc.Version) {
	name := sideBase(remote, p)
	if cn, ed := a.editorTab(from.host.ID, remote, p); ed != nil {
		a.showTab(cn, ed)
		return
	}
	cn, err := a.editorFor(from, remote, sideDir(remote, p))
	if err != nil {
		a.openFailed(from, name, err)
		return
	}
	buf := editor.NewBuffer(string(data))
	lang := editor.LanguageFor(name)
	buf.SetLanguage(lang)
	ed := &editState{path: p, name: name, ver: ver, st: &editor.State{Buffer: buf}}
	if lang != nil {
		ed.lang = lang.Name
	}
	cn.ed.tabs = append(cn.ed.tabs, ed)
	a.showTab(cn, ed)
}

// openImageTab shows the image p, of size bytes, of an editor window's
// folders in a tab.
func (a *App) openImageTab(cn *conn, p string, size int64) {
	ew := cn.ed
	for _, ed := range ew.tabs {
		if ed.path == p {
			a.showTab(cn, ed)
			return
		}
	}
	ed := &editState{path: p, name: sideBase(!ew.local, p), img: &imageTab{}, ver: sftpsvc.Version{Size: size}}
	ew.tabs = append(ew.tabs, ed)
	a.showTab(cn, ed)
}

// loadImage reads and decodes the picture of an image tab off the UI
// thread.
func (a *App) loadImage(cn *conn, ed *editState) {
	im := ed.img
	if im.cancel != nil {
		im.cancel()
	}
	im.bmp, im.big, im.started, im.cancel, ed.err, ed.loading = nil, false, true, nil, "", false
	if ed.ver.Size > previewLimit {
		im.big = true
		return
	}
	local, sc := cn.ed.local, cn.fs.remote.client
	if !local && sc == nil {
		im.started = false // once connected
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ed.loading, im.cancel = true, cancel
	im.loads++
	p, load := ed.path, im.loads
	go func() {
		defer cancel()
		var data []byte
		var err error
		if local {
			data, err = readLocalLimit(p, previewLimit)
		} else {
			data, _, err = sc.ReadFileContext(ctx, p, previewLimit)
		}
		var bmp *ui.Bitmap
		if err == nil {
			bmp, err = decodePreview(data)
		}
		a.post(func() {
			if im.loads != load {
				return // read again since
			}
			ed.loading = false
			switch {
			case errors.Is(err, sftpsvc.ErrReadLimit):
				im.big = true
			case err != nil:
				ed.err = err.Error()
			default:
				im.bmp = bmp
			}
		})
	}()
}

// closeFileTab closes the tab ed, asking first about its unsaved changes.
func (a *App) closeFileTab(cn *conn, ed *editState) {
	if ed.modified() {
		cn.ed.shut, cn.ed.shutOpen = ed, true
		return
	}
	a.dropTab(cn, ed)
}

// dropTab closes the tab ed, whatever it holds: the tab after it shows
// in its place, or the one before it.
func (a *App) dropTab(cn *conn, ed *editState) {
	ew := cn.ed
	i := slices.Index(ew.tabs, ed)
	if i < 0 {
		return
	}
	if ed.img != nil && ed.img.cancel != nil {
		ed.img.cancel()
	}
	ew.tabs = slices.Delete(ew.tabs, i, i+1)
	if ew.at > i || ew.at == len(ew.tabs) {
		ew.at--
	}
	if ew.at >= 0 {
		ew.tabs[ew.at].focused = false
	}
}

// stepTab shows the tab d after the one shown (d -1 before), around the
// ends.
func (a *App) stepTab(cn *conn, d int) {
	ew := cn.ed
	if n := len(ew.tabs); n > 1 {
		a.showTab(cn, ew.tabs[(ew.at+d+n)%n])
	}
}

// openEditorWindow opens the window of an editor, off the frame.
func (a *App) openEditorWindow(cn *conn) {
	ew := cn.ed
	spec := WindowSpec{
		Title:    ew.title,
		Width:    1000,
		Height:   680,
		StateKey: "editor",
		View:     func(c *ui.Context) { a.editorWindowView(c, cn) },
		Closing:  func() bool { return a.editorClosing(cn) },
		Closed:   func() { a.post(func() { a.editorClosed(cn) }) },
	}
	go func() {
		w := a.p.OpenWindow(spec)
		a.post(func() {
			if a.conns[cn.id] != cn {
				go w.Close() // closed while it opened
				return
			}
			ew.win = w
		})
	}()
}

// editorClosing says whether the editor's window may close, as the user
// closes it: with unsaved changes it stays and asks what to do with them.
func (a *App) editorClosing(cn *conn) bool {
	if !cn.ed.dirty.Load() {
		return true
	}
	a.post(func() { cn.ed.closeOpen = true })
	return false
}

// editorClosed closes the editor whose window is gone.
func (a *App) editorClosed(cn *conn) {
	if cn.ed != nil {
		cn.ed.win = nil
	}
	a.CloseSession(cn.id)
}

// closeEditorWindow closes the window of an editor being closed, and
// forgets it.
func (a *App) closeEditorWindow(cn *conn) {
	a.mu.Lock()
	delete(a.winSvc, cn.id)
	a.mu.Unlock()
	if a.ctx.win == cn.id && a.menu.key == ctxMenuKey {
		a.menu, a.ctx.entries = dropMenu{}, nil
	}
	for _, ed := range cn.ed.tabs {
		if ed.img != nil && ed.img.cancel != nil {
			ed.img.cancel()
		}
	}
	if w := cn.ed.win; w != nil {
		cn.ed.win = nil
		go w.Close()
	}
}

// editorToast shows a toast in the editor's window at its next frame.
func (a *App) editorToast(cn *conn, t func(m locales.Messages) ui.Toast) {
	cn.ed.toasts = append(cn.ed.toasts, t)
	a.invalidate()
}

// saveTab writes a tab's text to its file, unless the file changed since
// (then asks), or anyway when force.
func (a *App) saveTab(cn *conn, ed *editState, force bool) {
	if ed == nil || ed.st == nil || ed.saving || ed.loading {
		return
	}
	ew := cn.ed
	text := ed.st.Buffer.String()
	was := ed.ver
	if force {
		was = sftpsvc.Version{}
	}
	ed.saving, ed.err = true, ""
	local, p := ew.local, ed.path
	var get func(context.Context) (*sftpsvc.Client, error)
	if !local {
		get = cn.fs.transfers.Client
	}
	go func() {
		var ver sftpsvc.Version
		var err error
		if local {
			ver, err = sftpsvc.WriteLocalFile(p, []byte(text), was)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
			defer cancel()
			var c *sftpsvc.Client
			if c, err = get(ctx); err == nil {
				ver, err = c.WriteFile(p, []byte(text), was)
			}
		}
		a.post(func() {
			ed.saving = false
			switch {
			case errors.Is(err, sftpsvc.ErrChanged):
				ed.conflictOpen, ed.closeSaved, ew.closing = true, false, false
				if i := slices.Index(ew.tabs, ed); i >= 0 {
					ew.at = i
				}
			case err != nil:
				ed.err, ed.closeSaved, ew.closing = err.Error(), false, false
			default:
				ed.ver = ver
				if ed.st.Buffer.String() == text {
					ed.st.Buffer.MarkSaved()
				}
				if ew.closing {
					a.closeWhenSaved(cn)
					return
				}
				if ed.closeSaved {
					ed.closeSaved = false
					if !ed.modified() {
						a.dropTab(cn, ed)
						return
					}
				}
				a.editorToast(cn, func(m locales.Messages) ui.Toast {
					return ui.Toast{Title: m.EditorSaved(shownName(ed.name)), Type: "success"}
				})
			}
		})
	}()
}

// closeWhenSaved closes a window closing once its tabs are saved, or
// stops closing it if one could not be.
func (a *App) closeWhenSaved(cn *conn) {
	ew := cn.ed
	saving := false
	for _, ed := range ew.tabs {
		saving = saving || ed.saving
		if !ed.saving && ed.modified() {
			ew.closing = false // changed again since: stays
			return
		}
	}
	if !saving {
		a.CloseSession(cn.id)
	}
}

// reloadTab reads a tab's file again, over what was edited.
func (a *App) reloadTab(cn *conn, ed *editState) {
	if ed.saving || ed.loading {
		return
	}
	if ed.img != nil {
		a.loadImage(cn, ed)
		return
	}
	ed.loading, ed.err = true, ""
	local, p := cn.ed.local, ed.path
	var get func(context.Context) (*sftpsvc.Client, error)
	if !local {
		get = cn.fs.transfers.Client
	}
	limit := editorReadLimit(ed.ver.Size)
	go func() {
		var data []byte
		var ver sftpsvc.Version
		var err error
		if local {
			data, ver, err = sftpsvc.ReadLocalFile(p, limit)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
			defer cancel()
			var c *sftpsvc.Client
			if c, err = get(ctx); err == nil {
				data, ver, err = c.ReadFileContext(ctx, p, limit)
			}
		}
		a.post(func() {
			ed.loading = false
			if err != nil {
				ed.err = err.Error()
				return
			}
			ed.ver = ver
			ed.st.Buffer.Set(string(data))
			ed.st.Buffer.MarkSaved()
		})
	}()
}

// editorWindowView is an editor window: the tabs of the files open, the
// folders beside the file shown, its text with a status line, and the
// questions and sheets of both.
func (a *App) editorWindowView(c *ui.Context, cn *conn) {
	if a.conns[cn.id] != cn || cn.ed == nil {
		return // closed: the window goes too
	}
	a.mu.Lock()
	if a.winSvc == nil {
		a.winSvc = map[int]ui.Services{}
	}
	a.winSvc[cn.id] = c.Services()
	a.mu.Unlock()
	a.drain()
	if a.conns[cn.id] != cn {
		return
	}
	ew := cn.ed
	if !ew.local {
		a.ensureSFTP(cn)
	}
	a.applyZoom(c, false)
	base := *c.Theme()
	base.Font = a.fonts.ui
	tok := theme.For(a.settings.Theme, base.Dark)
	c.SetTheme(tok.Apply(base))
	a.ensureBackground()
	c.Root().Background(tok.Canvas)
	a.drawBackground(c, tok)
	m := locales.In(c.Locale())
	for _, t := range ew.toasts {
		c.AddToast(t(m))
	}
	ew.toasts = nil
	a.refreshForTransfers(c, cn)
	a.pendingOpens(cn)

	var ed *editState
	if ew.at >= 0 && ew.at < len(ew.tabs) {
		ed = ew.tabs[ew.at]
	}
	unsaved := 0
	var first *editState
	for _, t := range ew.tabs {
		if t.modified() {
			if unsaved++; first == nil {
				first = t
			}
		}
	}
	ew.dirty.Store(unsaved > 0)
	place := m.Local()
	if !ew.local {
		place = a.pv(cn.host.Label())
	}
	title := place
	if ed != nil {
		title = m.EditorWindowTitle(shownName(ed.name), place)
	}
	if w := ew.win; w != nil && (title != ew.title || (unsaved > 0) != ew.edited) {
		ew.title, ew.edited = title, unsaved > 0
		edited := ew.edited
		go func() {
			w.SetTitle(title)
			w.SetEdited(edited)
		}()
	}
	saveKey := a.keyOf(actSaveFile)
	switch {
	case c.Shortcut(saveKey.Mods, saveKey.Key):
		a.saveTab(cn, ed, false)
	case c.Shortcut(saveMods, ui.KeyW):
		if ed != nil {
			a.closeFileTab(cn, ed)
		} else {
			a.CloseSession(cn.id)
			return
		}
	case c.Shortcut(ui.Ctrl, ui.KeyTab):
		a.stepTab(cn, 1)
	case c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyTab):
		a.stepTab(cn, -1)
	}
	if ed != nil && ed.img != nil && !ed.img.started {
		a.loadImage(cn, ed)
	}

	ui.Column(c.Key("editorwin")).Fill().TextColor(tok.Text).Children(func() {
		a.editorTabs(c, cn, tok, m)
		a.backgroundSeparator(c, tok.Line, a.editorChromeFill(tok.Sidebar)).Height(1).Shrink(0).FillWidth()
		pane := func() { a.editorPane(c, cn, ed, tok, m) }
		if a.settings.EditorTreeHidden {
			ui.Column(c.Key("edpane")).Grow(1).FillWidth().MinHeight(0).Background(a.editorWindowFill(tok.Canvas)).Children(pane)
			return
		}
		ew.treeW = min(max(ew.treeW, 140), 600)
		ui.Split(c.Key("edsplit"), &ew.treeW, func() {
			ui.Box(c.Key("edtreebg")).Fill().Background(a.editorChromeFill(tok.Sidebar)).Children(func() { a.treeView(c, cn, tok, m) })
		}, func() {
			ui.Column(c.Key("edpane")).Fill().MinWidth(0).Background(a.editorWindowFill(tok.Canvas)).Children(pane)
		}).Grow(1).FillWidth().MinHeight(0)
		a.saveTreeWidth(c, ew)
	})

	if ed != nil {
		for _, t := range ew.tabs {
			if t.conflictOpen {
				ed = t
				break
			}
		}
		title, msg := m.EditorConflictTitle(), m.EditorConflictMessage(shownName(ed.name))
		if ew.local {
			title, msg = m.EditorDiskConflictTitle(), m.EditorDiskConflictMessage(shownName(ed.name))
		}
		switch ui.AlertDialog(c.Key("edconflict"), &ed.conflictOpen, title, msg, m.Cancel(), m.EditorReload(), m.Overwrite()) {
		case 1:
			a.reloadTab(cn, ed)
		case 2:
			a.saveTab(cn, ed, true)
		}
	}
	if s := ew.shut; s != nil {
		switch ui.AlertDialog(c.Key("edshut"), &ew.shutOpen, m.EditorUnsavedTitle(shownName(s.name)), m.EditorUnsavedMessage(), m.Cancel(), m.DontSave(), m.EditorSave()) {
		case 1:
			a.dropTab(cn, s)
		case 2:
			s.closeSaved = true
			a.saveTab(cn, s, false)
		}
		if !ew.shutOpen {
			ew.shut = nil
		}
	}
	if ew.closeOpen && unsaved == 0 {
		ew.closeOpen = false // saved meanwhile: it may close
		a.CloseSession(cn.id)
		return
	}
	closeTitle, save := m.EditorUnsavedManyTitle(unsaved), m.SaveAll()
	if unsaved == 1 {
		closeTitle, save = m.EditorUnsavedTitle(shownName(first.name)), m.EditorSave()
	}
	switch ui.AlertDialog(c.Key("edclose"), &ew.closeOpen, closeTitle, m.EditorUnsavedMessage(), m.Cancel(), m.DontSave(), save) {
	case 1:
		a.CloseSession(cn.id)
		return
	case 2:
		ew.closing = true
		for _, t := range ew.tabs {
			if t.modified() {
				a.saveTab(cn, t, false)
			}
		}
	}
	a.askView(c, cn, m)
	a.deleteView(c, cn, m)
	a.conflictView(c, cn, m)
	a.openAskView(c, cn, m)
	a.withView(c, cn, tok, m)
	a.transfersSheet(c, cn, tok, m)
	a.contextMenuIn(c, cn.id)
}

// saveTreeWidth saves the width of an editor window's folders once it
// rests where it was dragged to, for the windows that open next.
func (a *App) saveTreeWidth(c *ui.Context, ew *edWin) {
	if ew.treeW == a.settings.EditorTreeWidth {
		return
	}
	if ew.treeW != ew.treeSeen {
		ew.treeSeen, ew.treeAt = ew.treeW, c.Now()
	}
	if wait := splitRest - c.Now().Sub(ew.treeAt); wait > 0 {
		c.After(wait)
		return
	}
	st := a.settings
	st.EditorTreeWidth = ew.treeW
	a.ApplySettings(st)
}

// editorTabs is the bar of an editor window: the button showing its
// folders, then a tab for each file open.
func (a *App) editorTabs(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	ew := cn.ed
	ui.Row(c.Key("edtabbar")).Background(a.editorChromeFill(tok.Sidebar)).Height(38).Shrink(0).FillWidth().Padding(0, theme.Space(2)).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
		if (IconButton{Icon: icons.Folders, On: icons.FoldersOn, Label: m.EditorShowFolders(), Toggle: true, Active: !a.settings.EditorTreeHidden, Size: Small}).Clicked(c, "folders") {
			st := a.settings
			st.EditorTreeHidden = !st.EditorTreeHidden
			a.ApplySettings(st)
		}
		ui.ScrollHorizontal(c.Key("edtabs")).Grow(1).Shrink(1).MinWidth(0).FillHeight().Scrollbars(false).Children(func() {
			ui.Row(c.Key("edtabrow")).Gap(theme.Space(1)).FillHeight().AlignItems(ui.Center).Children(func() {
				for i, ed := range slices.Clone(ew.tabs) {
					a.editorTabChip(c, cn, ed, i, tok, m)
				}
			})
		})
	})
	ew.reveal = false
}

// editorTabChip is the tab of a file open in an editor window: its icon
// and name, whether it is edited or busy, and its close button, shown
// while it is the tab shown or under the pointer.
func (a *App) editorTabChip(c *ui.Context, cn *conn, ed *editState, i int, tok theme.Tokens, m locales.Messages) {
	ew := cn.ed
	on := i == ew.at
	chip := ui.Row(c.Key("edtab:"+ed.path)).Height(28).MaxWidth(240).Shrink(0).Gap(theme.Space(2)).Padding(0, theme.Space(1), 0, theme.Space(3)).
		AlignItems(ui.Center).Radius(theme.RadiusControl).Cursor(ui.CursorPointer).Tooltip(shownName(a.editorWhere(cn, ed.path)))
	hovered := chip.Hovered()
	switch {
	case on:
		chip.Background(tok.Inset).Border(1, tok.Line)
	case hovered:
		chip.Background(tok.Hover)
	}
	closed := false
	chip.Children(func() {
		fileImage(c, entryIcon(sftpsvc.Entry{Name: ed.name}), 14)
		txt := ui.Text(c, shownName(ed.name)).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
		if on {
			txt.FontWeight(600)
		} else {
			txt.TextColor(tok.TextMuted)
		}
		switch {
		case ed.saving || ed.loading:
			ui.Spinner(c).Size(12, 12)
		case ed.conflictOpen:
			label := m.EditorConflictTitle()
			if ew.local {
				label = m.EditorDiskConflictTitle()
			}
			ui.Icon(c, icons.Warning.SVG).Size(12, 12).Shrink(0).TextColor(tok.Warning).Label(label)
		case ed.err != "":
			ui.Icon(c, icons.Warning.SVG).Size(12, 12).Shrink(0).TextColor(tok.Warning).Label(m.ErrGeneric(securityText(m, ed.err)))
		case ed.modified():
			dot(c, tok.Accent).Label(m.EditorModified())
		}
		x := (IconButton{Icon: icons.Close, Label: m.EditorCloseTab(shownName(ed.name)), Size: Tiny}).Show(c, "x")
		if !on && !hovered {
			x.Invisible()
		}
		closed = x.Clicked()
	})
	if on && ew.reveal {
		chip.ScrollIntoView()
	}
	switch {
	case closed:
		a.closeFileTab(cn, ed)
	case chip.Clicked() && !on:
		a.showTab(cn, ed)
	}
}

// editorWhere is where the file p of an editor window is: on its host,
// or this computer's path.
func (a *App) editorWhere(cn *conn, p string) string {
	if cn.ed.local {
		return p
	}
	return a.pv(cn.host.Target()) + ":" + p
}

// editorPane is the file shown in an editor window: where it is, with
// its connection and buttons, its text or picture, and a status line.
func (a *App) editorPane(c *ui.Context, cn *conn, ed *editState, tok theme.Tokens, m locales.Messages) {
	ew := cn.ed
	if ed == nil {
		ui.Column(c.Key("noedit")).Grow(1).FillWidth().Center().Padding(theme.Space(4)).Children(func() {
			ui.Text(c, m.EditorNoFile()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
		})
		return
	}
	busy := ed.saving || ed.loading
	modified := ed.modified()
	ui.Row(c.Key("edbar")).Height(32).Shrink(0).FillWidth().Padding(0, theme.Space(2), 0, theme.Space(3)).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, shownName(a.editorWhere(cn, ed.path))).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		if !ew.local {
			st := cn.s.Status()
			dot(c, stateColor(tok, st.State))
			if st.State != session.Connected {
				ui.Text(c, a.pv(a.statusText(m, cn.host, st, c.Now()))).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
			}
		}
		reload := m.EditorReloadFromServer()
		if ew.local {
			reload = m.EditorReloadFromDisk()
		}
		if (IconButton{Icon: icons.Refresh, Label: reload, Size: Small, Disabled: busy}).Clicked(c, "reload") {
			a.reloadTab(cn, ed)
		}
		if ed.st != nil && (IconButton{Icon: icons.Save, Label: m.EditorSave(), Size: Small, Disabled: busy || !modified}).Clicked(c, "save") {
			a.saveTab(cn, ed, false)
		}
	})
	ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	if im := ed.img; im != nil {
		ui.Box(c.Key("edimg:" + ed.path)).Grow(1).FillWidth().MinHeight(0).Center().Clip().Padding(theme.Space(4)).Children(func() {
			switch {
			case ed.loading:
				ui.Spinner(c)
			case im.big || ed.err != "":
				msg := m.ImageTooLarge(humanSize(ed.ver.Size))
				if !im.big {
					msg = m.ImageFailed(securityText(m, ed.err))
				}
				ui.Column(c).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icons.Warning.SVG).Size(22, 22).TextColor(tok.Warning)
					ui.Text(c, msg).FontSize(theme.Footnote).TextColor(tok.TextMuted)
				})
			case im.bmp != nil:
				ui.Image(c, im.bmp).Fill().Fit(ui.ScaleDown).Label(shownName(ed.name))
			}
		})
	} else {
		if a.hasBackground() {
			ed.st.Theme = editor.DefaultTheme(c.Theme())
			// Nonzero RGB with zero alpha avoids the editor's zero-value
			// default substitution while leaving only its default fill clear.
			bg := tok.Canvas
			bg.A = 0
			ed.st.Theme.Background = bg
		} else {
			ed.st.Theme = nil
		}
		v := editor.View(c.Key("ed:"+ed.path), ed.st).Grow(1).FillWidth().MinHeight(0)
		if !ed.focused {
			v.Focus()
			ed.focused = true
		}
	}
	ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	ui.Row(c.Key("edstatus")).Height(28).Shrink(0).FillWidth().Padding(0, theme.Space(3)).Gap(theme.Space(2)).AlignItems(ui.Center).Background(tok.Fill).Children(func() {
		if ed.st != nil {
			a.syntaxSelect(c, ed, m)
		}
		ui.Spacer(c)
		switch {
		case busy:
			ui.Spinner(c)
		case ed.err != "" && ed.st != nil:
			ui.Text(c, m.ErrGeneric(securityText(m, ed.err))).FontSize(theme.Caption).TextColor(tok.Danger).SingleLine().Shrink(1)
		case modified:
			ui.Text(c, m.EditorModified()).FontSize(theme.Caption).TextColor(tok.Accent).SingleLine()
		}
		switch {
		case ed.st != nil:
			ui.Text(c, humanSize(int64(ed.st.Buffer.Len()))).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		case ed.img.bmp != nil:
			bw, bh := ed.img.bmp.Size()
			ui.Text(c, m.ImageDims(strconv.Itoa(bw), strconv.Itoa(bh), humanSize(ed.ver.Size))).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		}
	})
}

// syntaxSelect chooses the syntax the editor colors its text as.
func (a *App) syntaxSelect(c *ui.Context, ed *editState, m locales.Messages) {
	plain := m.PlainText()
	opts := []string{plain}
	for _, l := range editor.Languages() {
		opts = append(opts, l.Name)
	}
	choice := ed.lang
	if choice == "" {
		choice = plain
	}
	formSelect(c.Key("syntax"), &choice, opts).Label(m.EditorSyntax()).Tooltip(m.EditorSyntax()).FontSize(theme.Caption)
	if choice == plain {
		choice = ""
	}
	if choice != ed.lang {
		ed.lang = choice
		ed.st.Buffer.SetLanguage(editor.LanguageNamed(choice))
	}
}

// toast shows a toast at the next frame, from the UI thread.
func (a *App) toast(t func(m locales.Messages) ui.Toast) {
	a.toasts = append(a.toasts, t)
	a.invalidate()
}

// toastIn shows a toast in the window of cn: its editor window's, else
// the main one.
func (a *App) toastIn(cn *conn, t func(m locales.Messages) ui.Toast) {
	if cn != nil && cn.ed != nil {
		a.editorToast(cn, t)
		return
	}
	a.toast(t)
}

// sideDir is the folder of the entry at p, a path of a side.
func sideDir(remote bool, p string) string {
	if remote {
		return sftpsvc.RemoteParent(p)
	}
	return filepath.Dir(p)
}
