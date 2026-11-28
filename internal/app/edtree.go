package app

import (
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// fileTree is the folders of an editor window: what is in the folder at
// its top, root, and in the folders opened inside it, listed as they
// open. Its rows are paths.
type fileTree struct {
	root string
	out  ui.OutlineState[string]
	sel  int
	// dirs are the folders listed, or being listed, by path; ents what
	// they hold, by path.
	dirs map[string]*treeDir
	ents map[string]sftpsvc.Entry
	// want is the path to choose and scroll to once its row shows.
	want string
	// connects is the connection of the SFTP client it last listed with: a
	// new one lists again what shows.
	connects int
}

// treeDir is a folder of a tree: the paths in it once it is listed, and
// the listing under way, the last of which counts.
type treeDir struct {
	kids    []string
	loaded  bool
	loading bool
	lists   int
	err     string
}

// reset makes the tree start over at the folder root.
func (t *fileTree) reset(root string) {
	t.root, t.want, t.sel = root, "", -1
	t.dirs, t.ents = map[string]*treeDir{}, map[string]sftpsvc.Entry{}
	t.out.Open.Clear()
	t.out.List.Selected = &t.sel
}

// listed takes the listing of the folder dir, a path of a side.
func (t *fileTree) listed(remote bool, dir string, d *treeDir, es []sftpsvc.Entry, err error) {
	d.loaded, d.loading = true, false
	if err != nil {
		d.err = err.Error()
		return
	}
	kids := make([]string, len(es))
	for i, e := range es {
		kids[i] = sidePath(remote, dir, e.Name)
		t.ents[kids[i]] = e
	}
	for _, p := range d.kids {
		if !slices.Contains(kids, p) {
			t.forget(remote, p)
		}
	}
	d.kids, d.err = kids, ""
}

// forget forgets the path p, gone, and what was in it.
func (t *fileTree) forget(remote bool, p string) {
	for k := range t.ents {
		if inside(remote, p, k) {
			delete(t.ents, k)
		}
	}
	for k := range t.dirs {
		if inside(remote, p, k) {
			delete(t.dirs, k)
			t.out.Open.Remove(k)
		}
	}
}

// inside reports whether p, a path of a side, is dir or in it.
func inside(remote bool, dir, p string) bool {
	if p == dir {
		return true
	}
	sep := "/"
	if !remote {
		sep = string(filepath.Separator)
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, sep)+sep)
}

// treeLoad lists the folder dir of an editor window's folders: a local
// one at once, a remote one off the UI thread once connected.
func (a *App) treeLoad(cn *conn, dir string) {
	t := &cn.ed.tree
	d := t.dirs[dir]
	if d == nil {
		d = &treeDir{}
		t.dirs[dir] = d
	}
	if cn.ed.local {
		es, err := sftpsvc.ListLocal(dir)
		t.listed(false, dir, d, es, err)
		return
	}
	c := cn.fs.remote.client
	if c == nil {
		return // once connected
	}
	d.loading = true
	d.lists++
	n := d.lists
	go func() {
		es, err := c.List(dir)
		a.post(func() {
			if cn.ed == nil || t.dirs[dir] != d || d.lists != n {
				return // forgotten, or listed again since
			}
			t.listed(true, dir, d, es, err)
		})
	}()
}

// treeReload lists again the folders of an editor window that show: its
// top and those open, forgetting the others.
func (a *App) treeReload(cn *conn) {
	t := &cn.ed.tree
	for p := range t.dirs {
		if p != t.root && !t.out.Open.Has(p) {
			delete(t.dirs, p)
		}
	}
	for _, p := range slices.Collect(maps.Keys(t.dirs)) {
		if t.dirs[p] != nil { // not forgotten as its folder was listed
			a.treeLoad(cn, p)
		}
	}
}

// treeUp makes the folder holding the top of an editor window's folders
// their top, with the old one open in it.
func (a *App) treeUp(cn *conn) {
	t := &cn.ed.tree
	up := sideDir(!cn.ed.local, t.root)
	if up == t.root {
		return
	}
	t.out.Open.Add(t.root)
	t.root = up
	a.treeLoad(cn, up)
}

// treeReveal shows the path p in an editor window's folders, chosen: the
// folders holding it open, from the top, which becomes the folder of p
// when p is not in it.
func (a *App) treeReveal(cn *conn, p string) {
	t := &cn.ed.tree
	remote := !cn.ed.local
	dir := sideDir(remote, p)
	if !inside(remote, t.root, dir) {
		t.root = dir
	}
	for d := dir; d != t.root; {
		t.out.Open.Add(d)
		up := sideDir(remote, d)
		if up == d {
			break
		}
		d = up
	}
	t.want = p
}

// treeOpen opens the entry at p of an editor window's folders, as a
// double click or Enter does: a folder opens or closes, an image shows
// in a tab, a file made for another app asks how to open it, and another
// file opens in a tab.
func (a *App) treeOpen(cn *conn, p string) {
	t := &cn.ed.tree
	e, ok := t.ents[p]
	remote := !cn.ed.local
	switch {
	case !ok:
	case e.IsDir && t.out.Open.Has(p):
		t.out.Open.Remove(p)
	case e.IsDir:
		t.out.Open.Add(p)
	case remote && cn.fs.remote.client == nil:
	case isImage(e):
		a.openImageTab(cn, p, e.Size)
	case appFile(e.Name):
		a.askOpenWith(cn, &withAsk{remote: remote, path: p, size: e.Size})
	default:
		a.editFile(cn, remote, p, e.Size, false)
	}
}

// treeView is the folders of an editor window: the folder at the top
// with its buttons, and the tree of what is in it.
func (a *App) treeView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	ew := cn.ed
	t := &ew.tree
	fs := cn.fs
	remote := !ew.local
	off := remote && fs.remote.client == nil
	if !off && remote && t.connects != fs.remote.connects {
		t.connects = fs.remote.connects
		a.treeReload(cn)
	}
	if d := t.dirs[t.root]; d == nil || !d.loaded && !d.loading {
		a.treeLoad(cn, t.root)
	}
	top := t.dirs[t.root]
	ui.Column(c.Key("edtree")).Fill().MinWidth(0).Children(func() {
		var acts []barAction
		if len(fs.transfers.List()) > 0 {
			acts = append(acts, a.transfersAction(c, cn, m))
		}
		if len(fs.edits) > 0 {
			acts = append(acts, a.extEditsAction(cn, m))
		}
		acts = append(acts,
			barAction{Key: "treeup", Button: IconButton{Icon: icons.GoUp, Label: m.EditorUpFolder(), Size: Tiny, Disabled: off || sideDir(remote, t.root) == t.root},
				Do: func() { a.treeUp(cn) }},
			barAction{Key: "treefile", Button: IconButton{Icon: icons.NewFile, Label: m.NewFileItem(), Size: Tiny, Disabled: off},
				Do: func() { fs.ask, fs.askOpen = &nameAsk{kind: askNewFile, remote: remote, dir: t.root}, true }},
			barAction{Key: "treedir", Button: IconButton{Icon: icons.NewFolder, Label: m.NewFolderItem(), Size: Tiny, Disabled: off},
				Do: func() { fs.ask, fs.askOpen = &nameAsk{kind: askMkdir, remote: remote, dir: t.root}, true }},
			barAction{Key: "treerefresh", Button: IconButton{Icon: icons.Refresh, Label: m.Refresh(), Size: Tiny, Disabled: off},
				Do: func() { a.treeReload(cn) }})
		name := shownName(sideBase(remote, t.root))
		bar := ui.Row(c.Key("edtreebar")).Height(32).Shrink(0).FillWidth().Padding(0, theme.Space(1), 0, theme.Space(3)).Gap(theme.Space(1)).AlignItems(ui.Center)
		reserve := theme.Space(1) + theme.Space(3) + theme.Space(1) + titleRoom(c, name)
		bar.Children(func() {
			ui.Text(c, name).FontSize(theme.Footnote).FontWeight(600).SingleLine().Shrink(1).MinWidth(0).Tooltip(shownName(a.editorWhere(cn, t.root)))
			ui.Spacer(c)
			a.actionBar(c, "edtree:"+strconv.Itoa(cn.id), bar.Bounds().W, reserve, theme.Space(1), acts, nil, m)
		})
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)

		var roots []string
		if top != nil {
			roots = top.kids
		}
		children := func(p string) []string {
			if !t.ents[p].IsDir {
				return nil
			}
			d := t.dirs[p]
			if t.out.Open.Has(p) && (d == nil || !d.loaded && !d.loading) {
				a.treeLoad(cn, p)
				d = t.dirs[p]
			}
			if d == nil || d.kids == nil {
				return []string{}
			}
			return d.kids
		}
		t.out.List.Label = func(i int) string { return sideBase(remote, t.out.Item(i)) }
		ol := ui.Outline(c.Key("edoutline"), &t.out, roots, children, func(p string) {
			a.treeRow(c, cn, p, tok, m)
		}).Grow(1).FillWidth().MinHeight(0).Label(m.EditorFolders())
		ob := ol.Bounds()
		ol.HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputPointerDown || ev.Button != 1 {
				return false
			}
			t.sel = -1
			a.openContextIn(cn.id, ob.X+ev.X, ob.Y+ev.Y, func() []menuEntry { return a.treeEntries(cn, "", m) })
			return true
		})
		if t.want != "" {
			at := -1
			for i := range t.out.Rows() {
				if t.out.Item(i) == t.want {
					at = i
					break
				}
			}
			d := t.dirs[sideDir(remote, t.want)]
			if at >= 0 {
				t.sel = at
				t.out.List.ScrollIntoView(at)
			}
			if at >= 0 || d != nil && d.loaded && !d.loading {
				t.want = ""
			}
		}
		var p string
		if t.sel >= 0 && t.sel < t.out.Rows() {
			p = t.out.Item(t.sel)
		}
		switch {
		case p == "":
		case ol.Submitted():
			a.treeOpen(cn, p)
		case ol.Shortcut(0, ui.KeyF2) && !off:
			fs.ask, fs.askOpen = &nameAsk{kind: askRename, remote: remote, path: p, value: t.ents[p].Name}, true
		case (ol.Shortcut(deleteMods, deleteKey) || deleteKey != ui.KeyDelete && ol.Shortcut(0, ui.KeyDelete)) && !off:
			a.treeDelete(cn, p)
		}
		if ol.Shortcut(ui.Shift, ui.KeyF10) {
			b := ol.Bounds()
			a.openContextIn(cn.id, b.X+theme.Space(6), b.Y+theme.Space(8), func() []menuEntry { return a.treeEntries(cn, p, m) })
		}
		if remote {
			if files := ol.DroppedFiles(); len(files) > 0 && !off {
				a.Upload(cn, files, t.root)
			}
			if ol.FileDragOver() {
				ol.Background(tok.Accent.Alpha(0.08))
			}
		}

		var msg string
		switch {
		case top != nil && top.err != "":
			msg = top.err
		case off && fs.remote.err != "":
			msg = fs.remote.err
		}
		switch {
		case msg != "":
			ui.Text(c, m.ErrGeneric(securityText(m, msg))).FontSize(theme.Footnote).TextColor(tok.Danger).Padding(theme.Space(2), theme.Space(3))
		case top == nil || !top.loaded:
			ui.Row(c).FillWidth().Padding(theme.Space(2), theme.Space(3)).Children(func() { ui.Spinner(c) })
		case len(top.kids) == 0:
			ui.Text(c, m.Empty()).FontSize(theme.Footnote).TextColor(tok.TextMuted).Padding(theme.Space(2), theme.Space(3))
		}
	})
}

// treeRow is the row of the path p in an editor window's folders: its
// icon and name, whether its tab is edited, whether it is being listed or
// could not be. A right click on it opens its menu.
func (a *App) treeRow(c *ui.Context, cn *conn, p string, tok theme.Tokens, m locales.Messages) {
	t := &cn.ed.tree
	e := t.ents[p]
	row := ui.Row(c).Grow(1).MinWidth(0).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
		fileImage(c, entryIcon(e), 16)
		ui.Text(c, shownName(e.Name)).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		if d := t.dirs[p]; d != nil && t.out.Open.Has(p) {
			switch {
			case d.loading && !d.loaded:
				ui.Spinner(c).Size(12, 12)
			case d.err != "":
				ui.Icon(c, icons.Warning.SVG).Size(12, 12).Shrink(0).TextColor(tok.Warning).Tooltip(m.ErrGeneric(securityText(m, d.err)))
			}
		}
		for _, ed := range cn.ed.tabs {
			if ed.path == p && ed.modified() {
				dot(c, tok.Accent).Label(m.EditorModified())
			}
		}
	})
	b := row.Bounds()
	row.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputPointerDown || ev.Button != 1 {
			return false
		}
		for i := range t.out.Rows() {
			if t.out.Item(i) == p {
				t.sel = i
			}
		}
		a.openContextIn(cn.id, b.X+ev.X, b.Y+ev.Y, func() []menuEntry { return a.treeEntries(cn, p, m) })
		return true
	})
}

// treeEntries are the context menu of the path p in an editor window's
// folders, "" for their top: what can be done to it, then in the folder
// it is, or is in.
func (a *App) treeEntries(cn *conn, p string, m locales.Messages) []menuEntry {
	ew := cn.ed
	if ew == nil {
		return nil
	}
	t := &ew.tree
	fs := cn.fs
	remote := !ew.local
	off := remote && fs.remote.client == nil
	dir := t.root
	e := t.ents[p]
	var out []menuEntry
	if p != "" {
		dir = sideDir(remote, p)
		if e.IsDir {
			dir = p
		} else {
			out = append(out,
				menuEntry{Icon: icons.EditFile, Label: m.EditorOpenFile(), Disabled: off, Do: func() { a.treeOpen(cn, p) }},
				menuEntry{Icon: icons.OpenWith, Label: m.OpenWithDefault(), Disabled: off || a.p.OpenPath == nil, Do: func() { a.openWith(cn, remote, p, e.Size, false, m) }},
				menuEntry{Icon: icons.OpenWithApp, Label: m.OpenWithApp(), Disabled: off || a.p.OpenWith == nil, Do: func() { a.openWith(cn, remote, p, e.Size, true, m) }},
				a.extEditEntry(cn, remote, p, e.Size, off, m))
		}
		if remote {
			out = append(out, menuEntry{Icon: icons.Download, Label: m.DownloadTo(), Disabled: off || a.p.PickFolder == nil, Do: func() { a.downloadTo(cn, []string{p}, m) }})
		}
		out = append(out, menuSep)
	}
	out = append(out,
		menuEntry{Icon: icons.NewFile, Label: m.NewFileItem(), Disabled: off, Do: func() {
			fs.ask, fs.askOpen = &nameAsk{kind: askNewFile, remote: remote, dir: dir}, true
		}},
		menuEntry{Icon: icons.NewFolder, Label: m.NewFolderItem(), Disabled: off, Do: func() {
			fs.ask, fs.askOpen = &nameAsk{kind: askMkdir, remote: remote, dir: dir}, true
		}})
	if remote {
		out = append(out,
			menuEntry{Icon: icons.Upload, Label: m.UploadFiles(), Disabled: off || a.p.PickFiles == nil, Do: func() { a.pickUpload(cn, dir, false, m) }},
			menuEntry{Icon: icons.UploadFolder, Label: m.UploadFolder(), Disabled: off || a.p.PickFolder == nil, Do: func() { a.pickUpload(cn, dir, true, m) }})
	}
	out = append(out, menuSep)
	if p != "" {
		out = append(out,
			menuEntry{Icon: icons.Rename, Label: m.RenameItem(), Key: ui.KeyF2, Disabled: off, Do: func() {
				fs.ask, fs.askOpen = &nameAsk{kind: askRename, remote: remote, path: p, value: e.Name}, true
			}},
			menuEntry{Icon: icons.CopyPath, Label: m.CopyPath(), Do: func() { a.svc.WriteClipboard(p) }},
			menuSep,
			menuEntry{Icon: icons.Delete, Label: m.DeleteItem(), Mods: deleteMods, Key: deleteKey, Danger: true, Disabled: off, Do: func() { a.treeDelete(cn, p) }},
			menuSep)
	}
	return append(out, menuEntry{Icon: icons.Refresh, Label: m.Refresh(), Disabled: off, Do: func() { a.treeReload(cn) }})
}

// treeDelete asks to delete the entry at p of an editor window's folders.
func (a *App) treeDelete(cn *conn, p string) {
	e := cn.ed.tree.ents[p]
	cn.fs.del = &deleteAsk{remote: !cn.ed.local, paths: []string{p}, name: e.Name, isDir: e.IsDir}
	cn.fs.delOpen = true
}

// treeDeleted closes the tabs of the files at or in the paths deleted
// from an editor window's folders, but those with unsaved changes, which
// may still be saved again.
func (a *App) treeDeleted(cn *conn, paths []string) {
	remote := !cn.ed.local
	for _, ed := range slices.Clone(cn.ed.tabs) {
		for _, p := range paths {
			if inside(remote, p, ed.path) && !ed.modified() {
				a.dropTab(cn, ed)
				break
			}
		}
	}
}

// doTreeAsk does what the name dialog of an editor window asks, in its
// folders: a new file opens in a tab, and the tabs of what is renamed
// follow it.
func (a *App) doTreeAsk(cn *conn, ask *nameAsk) error {
	c := cn.fs.remote.client
	if ask.remote && c == nil {
		return errNotConnected
	}
	var p string
	var err error
	switch {
	case ask.kind == askMkdir && ask.remote:
		p, err = c.Mkdir(ask.dir, ask.value)
	case ask.kind == askMkdir:
		p, err = sftpsvc.MkdirLocal(ask.dir, ask.value)
	case ask.kind == askNewFile && ask.remote:
		p, err = c.CreateFile(ask.dir, ask.value)
	case ask.kind == askNewFile:
		p, err = sftpsvc.CreateLocalFile(ask.dir, ask.value)
	case ask.kind == askRename && ask.remote:
		p, err = c.Rename(ask.path, ask.value)
	case ask.kind == askRename:
		p, err = sftpsvc.RenameLocal(ask.path, ask.value)
	default:
		err = errors.New("not in an editor window")
	}
	if err != nil {
		return err
	}
	if ask.kind == askRename {
		a.tabsRenamed(cn, ask.path, p)
	}
	a.treeReload(cn)
	a.treeReveal(cn, p)
	if ask.kind == askNewFile {
		a.editFile(cn, ask.remote, p, 0, true)
	}
	return nil
}

// tabsRenamed moves the tabs of the files at or in from, renamed to to,
// and the folders open there, along with it.
func (a *App) tabsRenamed(cn *conn, from, to string) {
	t := &cn.ed.tree
	remote := !cn.ed.local
	for _, ed := range cn.ed.tabs {
		if inside(remote, from, ed.path) {
			ed.path = to + strings.TrimPrefix(ed.path, from)
			ed.name = sideBase(remote, ed.path)
		}
	}
	for _, k := range slices.Collect(t.out.Open.All()) {
		if inside(remote, from, k) {
			t.out.Open.Remove(k)
			t.out.Open.Add(to + strings.TrimPrefix(k, from))
		}
	}
	t.forget(remote, from)
}
