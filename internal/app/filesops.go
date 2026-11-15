package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

var errNotConnected = errors.New("not connected")

// askKind is what a nameAsk asks for.
type askKind int

const (
	askMkdir askKind = iota
	askNewFile
	askRename
	askChmod
)

// nameAsk is the dialog of a files pane asking a name (new folder or
// file, rename) or permissions, on one side.
type nameAsk struct {
	kind   askKind
	remote bool
	// path is the entry renamed or whose permissions change
	path string
	// dir is the folder a new entry goes in, in an editor window's
	// folders
	dir   string
	value string
	err   string
}

// deleteAsk confirms deleting entries of one side: their paths, and, of
// one, its name and whether it is a folder.
type deleteAsk struct {
	remote bool
	paths  []string
	name   string
	isDir  bool
}

// resolution is what to do with a file a transfer would copy over.
type resolution int

const (
	resOverwrite resolution = iota
	resSkip
	resRename
)

// conflictAsk is a transfer planned, going through the files already at
// their destination, asking about each (or once, for all).
type conflictAsk struct {
	dir    sftpsvc.Direction
	items  []sftpsvc.Item
	choice []resolution
	at     int // the item asked about
	all    bool
	last   resolution
	// from is the server a Relay copies from (its pane's transfer
	// client), fromName its host's name.
	from     func(context.Context) (*sftpsvc.Client, error)
	fromName string
}

// left is how many files already there are not yet answered for.
func (k *conflictAsk) left() int {
	n := 0
	for _, it := range k.items[k.at:] {
		if !it.IsDir && it.Exists {
			n++
		}
	}
	return n
}

// fileEntries are the context menu of a side: what can be done to the
// entries chosen, then in the folder.
func (a *App) fileEntries(cn *conn, remote bool, m locales.Messages) []menuEntry {
	fs := cn.fs
	sd := fs.side(remote)
	off := fs.remote.client == nil
	var out []menuEntry
	if es := sd.picked(); len(es) > 0 {
		paths := sd.paths(remote, es)
		e, p, one := es[0], paths[0], len(es) == 1
		switch {
		case !one:
		case e.IsDir:
			out = append(out, menuEntry{Icon: icons.OpenFile, Label: m.OpenFolder(), Do: func() { a.goTo(cn, remote, p) }})
		case isImage(e):
			out = append(out, menuEntry{Icon: icons.Image, Label: m.PreviewItem(), Key: ui.KeySpace, Disabled: remote && off,
				Do: func() { a.openPreview(cn, remote, e.Name) }})
		}
		if one && !e.IsDir {
			roff := remote && off
			out = append(out,
				menuEntry{Icon: icons.EditFile, Label: m.EditInEditor(), Disabled: roff, Do: func() { a.editFile(cn, remote, p, e.Size, false) }},
				menuEntry{Icon: icons.OpenWith, Label: m.OpenWithDefault(), Disabled: roff || a.p.OpenPath == nil, Do: func() { a.openWith(cn, remote, p, e.Size, false, m) }},
				menuEntry{Icon: icons.OpenWithApp, Label: m.OpenWithApp(), Disabled: roff || a.p.OpenWith == nil, Do: func() { a.openWith(cn, remote, p, e.Size, true, m) }})
		}
		if remote {
			label := m.Download()
			if !a.settings.FilesDual {
				label = m.DownloadTo()
			}
			out = append(out, menuEntry{Icon: icons.Download, Label: label, Disabled: off, Do: func() { a.downloadTo(cn, paths, m) }})
		} else {
			out = append(out, menuEntry{Icon: icons.Upload, Label: m.Upload(), Disabled: off, Do: func() { a.Upload(cn, paths, fs.remote.dir) }})
		}
		out = append(out, menuSep)
		if one {
			out = append(out, menuEntry{Icon: icons.Rename, Label: m.RenameItem(), Key: ui.KeyF2, Do: func() {
				fs.ask, fs.askOpen = &nameAsk{kind: askRename, remote: remote, path: p, value: e.Name}, true
			}})
			if remote {
				out = append(out, menuEntry{Icon: icons.Chmod, Label: m.Permissions(), Disabled: off, Do: func() {
					fs.ask, fs.askOpen = &nameAsk{kind: askChmod, remote: true, path: p, value: strconv.FormatUint(uint64(e.Mode.Perm()), 8)}, true
				}})
			}
		}
		out = append(out,
			menuEntry{Icon: icons.Copy, Label: m.Copy(), Mods: toggleMods, Key: ui.KeyC, Do: func() { a.copyEntries(cn, remote, paths) }},
			menuEntry{Icon: icons.CopyPath, Label: m.CopyPath(), Do: func() { a.svc.WriteClipboard(strings.Join(paths, "\n")) }},
			menuEntry{Icon: icons.Info, Label: m.GetInfo(), Mods: toggleMods, Key: ui.KeyI, Disabled: remote && off, Do: func() { a.openInfo(cn, remote, es) }},
			menuSep,
			menuEntry{Icon: icons.Compress, Label: m.CompressItem(), Disabled: remote && off || fs.working != nil, Do: func() { a.askCompress(cn, remote, es, m) }})
		if one && !e.IsDir && sftpsvc.IsArchive(e.Name, remote) {
			out = append(out, menuEntry{Icon: icons.Extract, Label: m.ExtractItem(), Disabled: remote && off || fs.working != nil, Do: func() { a.extract(cn, remote, e.Name) }})
		}
		out = append(out, menuSep,
			menuEntry{Icon: icons.Delete, Label: m.DeleteItem(), Mods: deleteMods, Key: deleteKey, Danger: true, Do: func() { a.askDelete(cn, remote, es) }},
			menuSep)
	}
	return append(out, a.folderEntries(cn, remote, m)...)
}

// folderEntries are the entries of a side's menu for its folder: what
// makes something in it, and the rows.
func (a *App) folderEntries(cn *conn, remote bool, m locales.Messages) []menuEntry {
	fs := cn.fs
	sd := fs.side(remote)
	off := remote && fs.remote.client == nil
	return []menuEntry{
		{Icon: icons.NewFolder, Label: m.NewFolderItem(), Disabled: off, Do: func() { fs.ask, fs.askOpen = &nameAsk{kind: askMkdir, remote: remote}, true }},
		{Icon: icons.NewFile, Label: m.NewFileItem(), Disabled: off, Do: func() { fs.ask, fs.askOpen = &nameAsk{kind: askNewFile, remote: remote}, true }},
		{Icon: icons.Paste, Label: m.Paste(), Mods: toggleMods, Key: ui.KeyV, Disabled: !a.canPaste(cn, remote), Do: func() { a.paste(cn, remote) }},
		menuSep,
		{Icon: icons.SelectAll, Label: m.SelectAll(), Mods: toggleMods, Key: ui.KeyA, Disabled: len(sd.entries) == 0, Do: func() {
			for _, e := range sd.entries {
				sd.chosen.Add(e.Name)
			}
		}},
		{Icon: icons.Refresh, Label: m.Refresh(), Disabled: off, Do: func() { a.listAgain(cn, remote) }},
	}
}

// fileClip is entries Copy took: their paths on one side of a files
// pane. Paste copies them into the folder of a side, the way dropping
// them there does.
type fileClip struct {
	pane   int
	remote bool
	paths  []string
}

// copyEntries takes the paths of entries of a side of cn, for Paste.
func (a *App) copyEntries(cn *conn, remote bool, paths []string) {
	if len(paths) > 0 {
		a.fileClip = &fileClip{pane: cn.id, remote: remote, paths: slices.Clone(paths)}
	}
}

// pasteFrom is the files pane the copied entries are on, and whether
// they can be pasted on a side of cn: a server's into either side (a
// copy from server to server, or a download), this computer's onto a
// server (an upload), but not onto this computer itself.
func (a *App) pasteFrom(cn *conn, remote bool) (*conn, bool) {
	k := a.fileClip
	if k == nil || !k.remote && !remote || cn.fs == nil {
		return nil, false
	}
	from := a.conns[k.pane]
	switch {
	case remote && cn.fs.remote.client == nil, !remote && cn.fs.local.dir == "":
		return nil, false
	case !k.remote:
		return from, true
	case from == nil || from.fs == nil || from.fs.remote.client == nil:
		return nil, false
	}
	return from, true
}

// canPaste says Paste can copy what was copied into the folder of a side
// of cn.
func (a *App) canPaste(cn *conn, remote bool) bool {
	_, ok := a.pasteFrom(cn, remote)
	return ok
}

// paste copies what was copied into the folder of a side of cn.
func (a *App) paste(cn *conn, remote bool) {
	from, ok := a.pasteFrom(cn, remote)
	if !ok {
		return
	}
	k := a.fileClip
	switch {
	case !k.remote:
		a.Upload(cn, k.paths, cn.fs.remote.dir)
	case remote:
		a.Relay(from, cn, k.paths, cn.fs.remote.dir)
	default:
		// a download goes in the queue of the pane on its server, as when
		// its rows are dropped here
		a.Download(from, k.paths, cn.fs.local.dir)
	}
}

// askDelete asks to delete es, entries of a side.
func (a *App) askDelete(cn *conn, remote bool, es []sftpsvc.Entry) {
	if len(es) == 0 {
		return
	}
	sd := cn.fs.side(remote)
	cn.fs.del = &deleteAsk{remote: remote, paths: sd.paths(remote, es), name: es[0].Name, isDir: es[0].IsDir}
	cn.fs.delOpen = true
}

// doAsk does what the name dialog asks, on its side.
func (a *App) doAsk(cn *conn, ask *nameAsk) error {
	if cn.ed != nil {
		return a.doTreeAsk(cn, ask)
	}
	fs := cn.fs
	if ask.kind == askMkdir {
		return a.Mkdir(cn, ask.remote, ask.value)
	}
	c := fs.remote.client
	if ask.remote && c == nil {
		return errNotConnected
	}
	var err error
	switch ask.kind {
	case askNewFile:
		if ask.remote {
			_, err = c.CreateFile(fs.remote.dir, ask.value)
		} else {
			_, err = sftpsvc.CreateLocalFile(fs.local.dir, ask.value)
		}
	case askRename:
		if ask.remote {
			_, err = c.Rename(ask.path, ask.value)
		} else {
			_, err = sftpsvc.RenameLocal(ask.path, ask.value)
		}
	case askChmod:
		mode, perr := sftpsvc.ParseMode(ask.value)
		if err = perr; err == nil {
			err = c.Chmod(ask.path, mode)
		}
	}
	if err != nil {
		return err
	}
	if ask.remote {
		a.listRemote(&fs.remote, fs.remote.dir)
	} else {
		a.listLocal(&fs.local, fs.local.dir)
	}
	return nil
}

func (a *App) askView(c *ui.Context, cn *conn, m locales.Messages) {
	fs := cn.fs
	if fs.ask == nil {
		return
	}
	ask := fs.ask
	var title, label, button string
	ic := icons.NewFolder
	switch ask.kind {
	case askMkdir:
		title, label, button = m.LocalNewFolder(), m.FolderName(), m.Create()
		if ask.remote {
			title = m.RemoteNewFolder()
		}
	case askNewFile:
		title, label, button, ic = m.NewFileTitle(), m.FileName(), m.Create(), icons.NewFile
	case askRename:
		title, label, button, ic = m.RenameTitle(path.Base(filepath.ToSlash(ask.path))), m.NewName(), m.RenameItem(), icons.Rename
	case askChmod:
		title, label, button, ic = m.PermissionsTitle(path.Base(ask.path)), m.ModeOctal(), m.Save(), icons.Chmod
	}
	tok := currentTokens(c)
	sheet(ui.Modal(c.Key("ask"), &fs.askOpen, func() {
		ui.Column(c).Gap(14).Width(360).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				softTile(c, ic, tok.Accent, 30)
				ui.Text(c, title).FontSize(theme.Title3).FontWeight(600).Shrink(1)
			})
			in := formTextInput(c.Key("name"), &ask.value).AutoFocus().Label(label).Placeholder(label).FillWidth()
			a.mkdirHold.apply(c, ask, in)
			if ask.kind == askChmod {
				ui.Text(c, m.ModeHint()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			}
			if ask.err != "" {
				ui.Text(c, m.ErrGeneric(securityText(m, ask.err))).TextColor(tok.Danger).FontSize(theme.Footnote)
			}
			submit := in.Submitted()
			ui.Row(c).Gap(8).Margin(4, 0, 0, 0).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					fs.askOpen = false
				}
				if pill(c, button, true, false, false) {
					submit = true
				}
			})
			if submit {
				if err := a.doAsk(cn, ask); err != nil {
					ask.err = err.Error()
				} else {
					fs.askOpen = false
				}
			}
		})
	}), tok)
	if !fs.askOpen {
		fs.ask = nil
	}
}

func (a *App) deleteView(c *ui.Context, cn *conn, m locales.Messages) {
	fs := cn.fs
	if fs.del == nil {
		return
	}
	del := fs.del
	msg := m.DeleteFileConfirm(del.name)
	switch {
	case len(del.paths) > 1:
		msg = m.DeleteItemsConfirm(len(del.paths))
	case del.isDir:
		msg = m.DeleteFolderConfirm(del.name)
	}
	if ui.AlertDialog(c.Key("del"), &fs.delOpen, m.DeleteItem(), msg, m.Cancel(), m.Delete()) == 1 {
		client := fs.remote.client
		go func() {
			var err error
			var gone []string
			for _, p := range del.paths {
				var perr error
				switch {
				case !del.remote:
					perr = sftpsvc.RemoveLocal(p)
				case client == nil:
					perr = errNotConnected
				default:
					perr = client.Remove(p)
				}
				if perr == nil {
					gone = append(gone, p)
				}
				err = cmp.Or(err, perr) // the first failure; the others go on
			}
			a.post(func() {
				if cn.ed != nil {
					a.treeDeleted(cn, gone)
				}
				// Success clears a previous failure; a failure stays
				// through the refresh below.
				a.filesErr(cn, del.remote, err)
				a.refreshSides(cn)
			})
		}()
	}
	if !fs.delOpen {
		fs.del = nil
	}
}

func (a *App) conflictView(c *ui.Context, cn *conn, m locales.Messages) {
	fs := cn.fs
	ask := fs.conflict
	if ask == nil {
		return
	}
	if ask.at >= len(ask.items) {
		fs.conflict = nil
		return
	}
	it := ask.items[ask.at]
	tok := currentTokens(c)
	answer := func(r resolution) {
		ask.choice[ask.at], ask.last = r, r
		ask.at++
		a.resolveConflicts(cn, ask)
	}
	sheet(ui.Modal(c.Key("conflict"), &fs.conflictOpen, func() {
		ui.Column(c).Gap(14).Width(420).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				softTile(c, icons.Warning, tok.Warning, 30)
				ui.Text(c, m.ConflictTitle()).FontSize(theme.Title3).FontWeight(600)
			})
			where := path.Dir(it.Dst)
			if ask.dir == sftpsvc.Download {
				where = filepath.Dir(it.Dst)
			}
			ui.Text(c, m.ConflictMessage(it.Rel, where)).Wrap()
			if n := ask.left(); n > 1 {
				formCheckbox(c.Key("all"), &ask.all, m.ApplyToAll(strconv.Itoa(n-1)))
			}
			ui.Row(c).Gap(8).Margin(4, 0, 0, 0).Children(func() {
				if pill(c, m.Cancel(), false, false, false) {
					fs.conflictOpen = false
				}
				ui.Spacer(c)
				if pill(c, m.Skip(), false, false, false) {
					answer(resSkip)
				}
				if pill(c, m.KeepBoth(), false, false, false) {
					answer(resRename)
				}
				if pill(c, m.Overwrite(), true, false, false) {
					answer(resOverwrite)
				}
			})
		})
	}), tok)
	if !fs.conflictOpen {
		fs.conflict = nil // canceled: nothing of the plan is queued
		if ask.dir != sftpsvc.Upload {
			fs.skipped = nil // nor is what it left out of interest
		}
	}
}

// transferLabel is what a transfer's row says of its state.
func transferLabel(m locales.Messages, t sftpsvc.Transfer) string {
	switch t.State {
	case sftpsvc.Queued:
		if t.Attempt > 0 {
			return m.TransferRetrying(strconv.Itoa(t.Attempt), strconv.Itoa(sftpsvc.MaxRetries))
		}
		return m.TransferQueued()
	case sftpsvc.Running:
		if t.Attempt > 0 && t.Err != nil {
			return m.TransferRetrying(strconv.Itoa(t.Attempt), strconv.Itoa(sftpsvc.MaxRetries))
		}
		s := strconv.Itoa(t.Percent()) + "%"
		if t.Speed > 0 {
			s += " · " + humanSize(int64(t.Speed)) + "/s"
		}
		if eta := t.ETA(); eta > 0 {
			s += " · " + m.TransferETA(shortDuration(eta))
		}
		return s
	case sftpsvc.Paused:
		return m.TransferPaused(strconv.Itoa(t.Percent()))
	case sftpsvc.Done:
		return m.TransferDoneShort()
	case sftpsvc.Failed:
		return m.TransferFailed(securityErrorText(m, t.Err))
	case sftpsvc.Canceled:
		return m.TransferCanceled()
	}
	return ""
}

// transferTip is the tooltip of a transfer's name: where it went, once
// done, and for a copy from another server, which one and what it copies.
func (a *App) transferTip(m locales.Messages, t sftpsvc.Transfer) string {
	var lines []string
	if t.Dir == sftpsvc.Relay {
		lines = append(lines, m.TransferFromHost(a.pv(t.From), shownName(t.Src)))
	}
	if t.Destination != "" {
		lines = append(lines, t.Destination)
	}
	return strings.Join(lines, "\n")
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Second {
		d = time.Second
	}
	h, mn, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return strconv.Itoa(h) + ":" + pad2(mn) + ":" + pad2(s)
	}
	return strconv.Itoa(mn) + ":" + pad2(s)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// transferCounts is how many of ts are in each state, and how fast the
// running ones go together.
type transferCounts struct {
	active, running, paused, failed, done int
	speed                                 float64
}

func countTransfers(ts []sftpsvc.Transfer) transferCounts {
	var n transferCounts
	for _, t := range ts {
		switch t.State {
		case sftpsvc.Running:
			n.running++
			n.speed += t.Speed
		case sftpsvc.Paused:
			n.paused++
		case sftpsvc.Failed, sftpsvc.Canceled:
			n.failed++
		case sftpsvc.Done:
			n.done++
		}
		if t.Active() {
			n.active++
		}
	}
	return n
}

// transfersAction is the Files pane header's button for its transfer
// queue, with how many are still to go on it.
func (a *App) transfersAction(c *ui.Context, cn *conn, m locales.Messages) barAction {
	n := countTransfers(cn.fs.transfers.List())
	b := IconButton{Icon: icons.Transfers, Label: m.Transfers(), Active: cn.fs.queueOpen, Size: Tiny}
	if n.active > 0 {
		b.Badge = strconv.Itoa(n.active)
	} else if n.failed > 0 {
		b.Badge, b.BadgeDanger = strconv.Itoa(n.failed), true
	}
	if n.running > 0 {
		c.After(250 * time.Millisecond)
	}
	return barAction{Key: "transfers", Button: b, Do: func() { cn.fs.queueOpen = true }}
}

// transfersSheet is the transfer queue of a files pane: what is going
// on, buttons for all, and a row for each transfer.
func (a *App) transfersSheet(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	if !fs.queueOpen {
		return
	}
	trs := fs.transfers
	ts := trs.List()
	n := countTransfers(ts)
	active, paused, failed := n.active, n.paused, n.failed
	sheet(ui.Modal(c, &fs.queueOpen, func() {
		ui.Column(c.Key("transfers")).Gap(14).Width(720).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
				tile(c, icons.Transfers, theme.Brand, 36)
				ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
					sheetTitle(c, m.Transfers())
					summary := m.TransferSummary(strconv.Itoa(active), strconv.Itoa(n.done), strconv.Itoa(len(ts)))
					if n.speed > 0 {
						summary += " · " + humanSize(int64(n.speed)) + "/s"
					}
					if len(ts) > 0 {
						ui.Text(c, summary).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
					}
				})
				if (IconButton{Icon: icons.Pause, Label: m.PauseAll(), Disabled: active == paused, Size: Small}).Clicked(c, "pauseall") {
					trs.PauseAll()
				}
				if (IconButton{Icon: icons.Resume, Label: m.ResumeAll(), Disabled: paused == 0, Size: Small}).Clicked(c, "resumeall") {
					trs.ResumeAll()
				}
				if (IconButton{Icon: icons.Reconnect, Label: m.RetryAll(), Disabled: failed == 0, Size: Small}).Clicked(c, "retryall") {
					for _, t := range ts {
						if t.State == sftpsvc.Failed || t.State == sftpsvc.Canceled {
							trs.Retry(t.ID)
						}
					}
				}
				if (IconButton{Icon: icons.Close, Label: m.CancelAll(), Disabled: active == 0, Size: Small}).Clicked(c, "cancelall") {
					trs.CancelAll()
				}
				if (IconButton{Icon: icons.Clear, Label: m.ClearFinished(), Disabled: len(ts) == active, Size: Small}).Clicked(c, "clear") {
					trs.ClearFinished()
				}
			})
			box := ui.Column(c).Height(320).FillWidth().Padding(6).Radius(theme.RadiusCard - 2).Background(tok.Fill)
			box.Children(func() {
				if len(ts) == 0 {
					ui.Column(c).Grow(1).Center().Children(func() {
						ui.Text(c, m.NoTransfers()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
					})
					return
				}
				// Rows past the box's height scroll, the buttons clear of
				// the bar; a failed row's reason may take a second line.
				end := float32(2)
				if len(ts)*26+failed*10 > 320-2*6 {
					end += scrollGutter
				}
				ui.List(c.Key("queue"), &fs.queue, len(ts), func(i int) {
					t := ts[i]
					// A failed row shows its whole reason where the progress
					// was, wrapping, rather than cut to the status column.
					broke := t.State == sftpsvc.Failed
					row := ui.Row(c.Key("tr:" + strconv.Itoa(t.ID))).Gap(8).AlignItems(ui.Center).FillWidth()
					if broke {
						row.MinHeight(26).Padding(4, end, 4, 2)
					} else {
						row.Height(26).Padding(0, end, 0, 2)
					}
					row.Children(func() {
						ic := icons.Upload
						switch t.Dir {
						case sftpsvc.Download:
							ic = icons.Download
						case sftpsvc.Relay:
							ic = icons.Relay
						}
						ui.Icon(c, ic.SVG).Width(14).Height(14).Shrink(0).TextColor(tok.TextMuted)
						name := ui.Text(c, t.Name).FontSize(theme.Footnote).SingleLine().Width(200).Shrink(0)
						if tip := a.transferTip(m, t); tip != "" {
							name.Tooltip(tip)
						}
						if broke {
							ui.Text(c, transferLabel(m, t)).FontSize(theme.Caption).TextColor(tok.Danger).Grow(1).Shrink(1).MinWidth(0)
						} else {
							ui.Progress(c, float64(t.Percent())/100).Grow(1)
							lbl := ui.Text(c, transferLabel(m, t)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Width(190)
							if t.Err != nil {
								lbl.Tooltip(securityErrorText(m, t.Err))
							}
						}
						id := t.ID
						ui.Row(c).Gap(2).Width(52).Justify(ui.End).Children(func() {
							switch t.State {
							case sftpsvc.Queued, sftpsvc.Running:
								if (IconButton{Icon: icons.Pause, Label: m.PauseTransfer(t.Name), Size: Small}).Clicked(c, "pause") {
									trs.Pause(id)
								}
							case sftpsvc.Paused:
								if (IconButton{Icon: icons.Resume, Label: m.ResumeTransfer(t.Name), Size: Small}).Clicked(c, "resume") {
									trs.Resume(id)
								}
							case sftpsvc.Failed, sftpsvc.Canceled:
								if (IconButton{Icon: icons.Reconnect, Label: m.RetryTransfer(t.Name), Size: Small}).Clicked(c, "retry") {
									trs.Retry(id)
								}
							}
							if t.Active() {
								if (IconButton{Icon: icons.Close, Label: m.CancelTransfer(t.Name), Size: Small}).Clicked(c, "cancel") {
									trs.Cancel(id)
								}
							}
						})
					})
				}).Grow(1).FillWidth()
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Close(), true, false, false) {
					fs.queueOpen = false
				}
			})
		})
	}), tok)
}

// skippedShown is how many skipped items the notice names.
const skippedShown = 8

// planningView shows that a transfer is being planned (folders listed
// to the bottom, which a slow or silent server can make long), with a
// way to cancel it.
func (a *App) planningView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	run := cn.fs.planning
	if run == nil {
		return
	}
	col, icon, text := tok.Accent, icons.Download, m.PlanningDownload()
	switch run.dir {
	case sftpsvc.Upload:
		icon, text = icons.Upload, m.PlanningUpload()
	case sftpsvc.Relay:
		icon, text = icons.Relay, m.PlanningCopy()
	}
	ui.Row(c.Key("planning")).FillWidth().Padding(6, 8, 6, 12).Gap(8).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		ui.Icon(c, icon.SVG).Size(14, 14).Shrink(0).TextColor(col)
		ui.Text(c, text).FontSize(theme.Footnote).Grow(1).Shrink(1)
		if pill(c, m.CancelPlanning(), false, false, false) {
			run.cancel()
			cn.fs.planning = nil
			if run.dir != sftpsvc.Upload {
				cn.fs.skipped = nil // as when its question is canceled
			}
		}
	})
}

// unfinishedView is the notice of a save in the remote folder that a cut
// left undone: the file is missing and its last version at a backup. It
// offers the first such file to restore or discard (the next shows once
// that is done); nothing is put back unless asked.
func (a *App) unfinishedView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	s := &cn.fs.remote
	if len(s.unfinished) == 0 || s.client == nil {
		return
	}
	u := s.unfinished[0]
	col := tok.Warning
	ui.Row(c.Key("unfinished")).FillWidth().Padding(6, 8, 6, 12).Gap(8).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(col)
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, m.UnfinishedSave(shownName(u.Name))).FontSize(theme.Footnote).Selectable()
			if n := len(s.unfinished) - 1; n > 0 {
				ui.Text(c, m.SkippedMore(strconv.Itoa(n))).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			}
		})
		restore := pill(c, m.RestoreUnfinished(), true, false, s.fixing)
		discard := pill(c, m.DiscardUnfinished(), false, false, s.fixing)
		if (restore || discard) && !s.fixing {
			a.fixUnfinished(cn, u, restore)
		}
	})
}

// fixUnfinished restores u, or discards its backup, then lists the
// folder again.
func (a *App) fixUnfinished(cn *conn, u sftpsvc.Unfinished, restore bool) {
	s := &cn.fs.remote
	c, dir := s.client, s.dir
	s.fixing = true
	go func() {
		var err error
		if restore {
			err = c.Restore(dir, u)
		} else {
			err = c.Discard(dir, u)
		}
		a.post(func() {
			s.fixing = false
			if s.client != c || s.dir != dir {
				return
			}
			if err != nil {
				a.filesErr(cn, true, err)
				return
			}
			a.listRemote(s, dir)
		})
	}()
}

// skippedView is the notice of what the last download left out: each
// with why, the first skippedShown of them, and how many more.
func (a *App) skippedView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	sk := cn.fs.skipped
	if len(sk) == 0 {
		return
	}
	col := tok.Warning
	ui.Row(c.Key("skipped")).FillWidth().Padding(6, 8, 6, 12).Gap(8).AlignItems(ui.Start).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(col).Margin(3, 0, 0, 0)
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			head := m.DownloadSkipped(strconv.Itoa(len(sk)))
			if cn.fs.skippedCopy {
				head = m.CopySkipped(strconv.Itoa(len(sk)))
			}
			ui.Text(c, head).FontSize(theme.Footnote)
			for i, s := range sk {
				if i == skippedShown {
					ui.Text(c, m.SkippedMore(strconv.Itoa(len(sk)-i))).FontSize(theme.Footnote).TextColor(tok.TextMuted)
					break
				}
				ui.Text(c, skipText(m, s)).FontSize(theme.Footnote).TextColor(tok.TextMuted).Selectable()
			}
		})
		if (IconButton{Icon: icons.Close, Label: m.DismissSkipped(), Size: Small}).Clicked(c, "dismissskipped") {
			cn.fs.skipped = nil
		}
	})
}

// skipText says what was skipped and why, the name shown as shownName.
func skipText(m locales.Messages, s sftpsvc.Skip) string {
	name := shownName(s.Rel)
	switch s.Reason {
	case sftpsvc.SkipLinkedFolder:
		return m.SkipLinkedFolder(name)
	case sftpsvc.SkipBrokenLink:
		return m.SkipBrokenLink(name)
	}
	return m.SkipName(name)
}

// shownName is a name from the server as text that shows all of it on
// one line, and nothing but it: newlines, other control characters and
// those that reorder text (bidi controls) are written as escapes (\n,
// \u202e), as are bytes that are not UTF-8 (\xff).
func shownName(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == utf8.RuneError:
			if _, n := utf8.DecodeRuneInString(s[i:]); n == 1 {
				fmt.Fprintf(&b, `\x%02x`, s[i])
			} else {
				b.WriteRune(r)
			}
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r <= 0xff && unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
