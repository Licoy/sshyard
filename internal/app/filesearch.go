package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The scopes of a side's search: under the folder shown, or the whole
// disk.
const (
	searchHere = iota
	searchWhole
)

// searchSettle is how long typing rests before what is typed is
// searched for; Enter searches at once.
const searchSettle = 400 * time.Millisecond

// fileSearch is a side's search, shown in place of its rows: what is
// typed and where, and what it found, which comes in as it is found.
type fileSearch struct {
	query string
	scope int
	// opens keys the field, hold focuses it as it opens.
	opens int
	hold  focusHold
	// seen is the query of the last frame, typedAt when it changed.
	seen    string
	typedAt time.Time
	// ran, root and client are what the results are of: the query
	// searched for, the folder searched under and, on the server, the
	// SFTP client searched on.
	ran     string
	root    string
	client  *sftpsvc.Client
	results []sftpsvc.Match
	// running is the search under way, stopped one stopped before it was
	// done, capped one that stopped at sftpsvc.SearchLimit.
	running, stopped, capped bool
	err                      string
	cancel                   context.CancelFunc
	// gen counts the searches started, so that what an older one finds
	// late is dropped.
	gen  int
	sel  int
	list ui.ListState
}

// toggleSearch opens the side's search, or closes it.
func (sd *sideList) toggleSearch() {
	if sd.search != nil {
		sd.closeSearch()
		return
	}
	sd.searches++
	sr := &fileSearch{opens: sd.searches, sel: -1}
	sr.list.Selected, sr.list.RoundRows = &sr.sel, true
	sd.search = sr
}

// closeSearch stops the side's search and gives its rows back, with the
// keys.
func (sd *sideList) closeSearch() {
	if sd.search == nil {
		return
	}
	sd.search.stop()
	sd.search, sd.refocus = nil, true
}

// stop ends the search under way; what it found stays.
func (sr *fileSearch) stop() {
	if sr.cancel != nil {
		sr.cancel()
		sr.cancel = nil
	}
	sr.gen++
	sr.running = false
}

// clear stops the search and forgets what it found.
func (sr *fileSearch) clear() {
	sr.stop()
	sr.ran, sr.root, sr.client, sr.results = "", "", nil, nil
	sr.stopped, sr.capped, sr.err, sr.sel = false, false, "", -1
}

// due says whether q, typed, is to be searched for under root on client
// now (submit is Enter), or else how long until typing has rested: a
// new query once typing rests, the same one again under another folder
// or on another client (reconnected) at once.
func (sr *fileSearch) due(now time.Time, q, root string, client *sftpsvc.Client, submit bool) (bool, time.Duration) {
	switch {
	case q == "" || root == "":
		return false, 0
	case q == sr.ran:
		return submit || root != sr.root || client != sr.client, 0
	case submit:
		return true, 0
	}
	if wait := searchSettle - now.Sub(sr.typedAt); wait > 0 {
		return false, wait
	}
	return true, 0
}

// searchRoot is the folder a side's search goes under: the folder dir
// shown, or the root of its disk, which on a server is "/" (its drives,
// on Windows).
func searchRoot(remote bool, dir string, whole bool) string {
	switch {
	case dir == "" || !whole:
		return dir
	case remote:
		return "/"
	}
	if v := filepath.VolumeName(dir); v != "" {
		return v + string(filepath.Separator)
	}
	return string(filepath.Separator)
}

// searchPlace is where a search found an entry, its folder dir, as its
// row shows it: under the folder searched (root) from "./", the whole
// path on a whole disk.
func searchPlace(remote bool, root, dir string, whole bool) string {
	if whole {
		return dir
	}
	sep := "/"
	rel := ""
	if remote {
		rel = strings.TrimPrefix(strings.TrimPrefix(dir, root), "/")
		if dir != root && !strings.HasPrefix(dir, strings.TrimSuffix(root, "/")+"/") {
			return dir
		}
	} else {
		sep = string(filepath.Separator)
		r, err := filepath.Rel(root, dir)
		if err != nil || strings.HasPrefix(r, "..") {
			return dir
		}
		if r != "." {
			rel = r
		}
	}
	return "." + sep + rel
}

// searchUpdate starts the side's search as what is typed, its scope,
// the folder shown or the client calls for, and stops it while the
// server is not connected.
func (a *App) searchUpdate(c *ui.Context, cn *conn, remote bool, sr *fileSearch, submit bool) {
	sd := cn.fs.side(remote)
	now := c.Now()
	if sr.query != sr.seen {
		sr.seen, sr.typedAt = sr.query, now
	}
	var client *sftpsvc.Client
	if remote {
		if client = cn.fs.remote.client; client == nil {
			if sr.running {
				sr.stop()
			}
			return
		}
	}
	q := strings.TrimSpace(sr.query)
	if q == "" {
		if sr.ran != "" || sr.running {
			sr.clear()
		}
		return
	}
	root := searchRoot(remote, sd.dir, sr.scope == searchWhole)
	start, wait := sr.due(now, q, root, client, submit)
	if wait > 0 {
		c.After(wait)
	}
	if start {
		a.startSearch(cn, remote, sr, q, root, client)
	}
}

// startSearch searches for q under root, on client on the server, off
// the UI thread, stopping the search before it. What it finds comes in
// as it is found, a frame's worth at a time.
func (a *App) startSearch(cn *conn, remote bool, sr *fileSearch, q, root string, client *sftpsvc.Client) {
	sr.clear()
	ctx, cancel := context.WithCancel(context.Background())
	sr.ran, sr.root, sr.client, sr.running, sr.cancel = q, root, client, true, cancel
	gen := sr.gen
	var mu sync.Mutex
	var found []sftpsvc.Match
	posted := false
	flush := func() {
		mu.Lock()
		got := found
		found, posted = nil, false
		mu.Unlock()
		if sr.gen == gen {
			sr.results = append(sr.results, got...)
		}
	}
	add := func(mt sftpsvc.Match) {
		mu.Lock()
		found = append(found, mt)
		post := !posted
		posted = true
		mu.Unlock()
		if post {
			a.post(flush)
		}
	}
	go func() {
		var capped bool
		var err error
		if remote {
			capped, err = client.Search(ctx, root, q, sftpsvc.SearchLimit, add)
		} else {
			capped, err = sftpsvc.SearchLocal(ctx, root, q, sftpsvc.SearchLimit, add)
		}
		cancel()
		a.post(func() {
			flush()
			if sr.gen != gen {
				return // stopped, or a newer search took its place
			}
			sr.running, sr.capped, sr.cancel = false, capped, nil
			if err != nil && !errors.Is(err, context.Canceled) {
				sr.err = err.Error()
			}
		})
	}()
}

// openMatch opens an entry a search found, as a double click or Enter
// does: the search closes, and a folder lists; a file's folder lists,
// with the file chosen.
func (a *App) openMatch(cn *conn, remote bool, mt sftpsvc.Match) {
	sd := cn.fs.side(remote)
	sd.closeSearch()
	if mt.Entry.IsDir {
		a.goTo(cn, remote, sidePath(remote, mt.Dir, mt.Entry.Name))
		return
	}
	a.reveal(cn, remote, mt.Dir, mt.Entry.Name)
}

// matchEntries are the context menu of an entry a search found.
func (a *App) matchEntries(cn *conn, remote bool, mt sftpsvc.Match, m locales.Messages) []menuEntry {
	fs := cn.fs
	e, p := mt.Entry, sidePath(remote, mt.Dir, mt.Entry.Name)
	off := fs.remote.client == nil
	var out []menuEntry
	if e.IsDir {
		out = append(out, menuEntry{Icon: icons.OpenFile, Label: m.OpenFolder(), Do: func() { a.openMatch(cn, remote, mt) }})
	}
	out = append(out, menuEntry{Icon: icons.Folder, Label: m.FileSearchShowInFolder(), Do: func() {
		fs.side(remote).closeSearch()
		a.reveal(cn, remote, mt.Dir, e.Name)
	}})
	if !e.IsDir {
		out = append(out, menuEntry{Icon: icons.EditFile, Label: m.EditInEditor(), Disabled: remote && off, Do: func() { a.editFile(cn, remote, p, e.Size, false) }})
	}
	if remote {
		label := m.Download()
		if !a.settings.FilesDual {
			label = m.DownloadTo()
		}
		out = append(out, menuEntry{Icon: icons.Download, Label: label, Disabled: off, Do: func() { a.downloadTo(cn, []string{p}, m) }})
	} else {
		out = append(out, menuEntry{Icon: icons.Upload, Label: m.Upload(), Disabled: off, Do: func() { a.Upload(cn, []string{p}, fs.remote.dir) }})
	}
	return append(out, menuSep,
		menuEntry{Icon: icons.CopyPath, Label: m.CopyPath(), Do: func() { a.svc.WriteClipboard(p) }})
}

// searchView is a side's search in place of its rows: the field, the
// scope with how the search goes, and what it found, each row its icon,
// its name and where it is. Escape, or the close button, gives the rows
// back.
func (a *App) searchView(c *ui.Context, cn *conn, remote bool, tok theme.Tokens, m locales.Messages) {
	sd := cn.fs.side(remote)
	key := "lsearch"
	if remote {
		key = "rsearch"
	}
	closing := false
	box := ui.Column(c.Key(key)).Gap(2).Grow(1).FillWidth().MinHeight(0)
	box.Children(func() { closing = a.searchBody(c, cn, remote, key, tok, m) })
	if closing || box.Shortcut(0, ui.KeyEscape) {
		sd.closeSearch()
	}
}

// searchBody is what searchView shows, under key; closing says the close
// button was clicked.
func (a *App) searchBody(c *ui.Context, cn *conn, remote bool, key string, tok theme.Tokens, m locales.Messages) (closing bool) {
	sr := cn.fs.side(remote).search
	submit := false
	ui.Row(c.Key(key+":bar")).Gap(4).MinHeight(26).Shrink(0).FillWidth().MinWidth(0).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		in := formTextInput(c.Key("q:"+strconv.Itoa(sr.opens)), &sr.query).AutoFocus().Label(m.FileSearchLabel()).
			Placeholder(m.FileSearchPlaceholder()).FontSize(theme.Footnote).Grow(1).MinWidth(0)
		sr.hold.apply(c, sr.opens, in)
		submit = in.Submitted()
		if (IconButton{Icon: icons.Close, Label: m.FileSearchClose(), Size: Small}).Clicked(c, "close") {
			closing = true
		}
	})
	if closing {
		return true
	}
	a.searchUpdate(c, cn, remote, sr, submit)
	n := len(sr.results)
	ui.Row(c.Key(key+":status")).Gap(6).MinHeight(24).Shrink(0).FillWidth().MinWidth(0).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		formSegmented(c.Key("scope"), &sr.scope, false, m.FileSearchHere(), m.FileSearchWholeDisk()).Label(m.FileSearchScope()).Shrink(0)
		text, col := "", tok.TextMuted
		switch {
		case sr.err != "":
			text, col = m.ErrGeneric(securityText(m, sr.err)), tok.Danger
		case strings.TrimSpace(sr.query) == "":
			text = m.FileSearchHint()
		case sr.running:
			ui.Spinner(c)
			text = m.FileSearchSearching(n)
		case sr.ran == "":
		case sr.capped:
			text = m.FileSearchCapped(n)
		case sr.stopped:
			text = m.FileSearchStopped(n)
		case n == 0:
			text = m.FileSearchNone()
		default:
			text = m.FileSearchFound(n)
		}
		ui.Text(c, text).FontSize(theme.Caption).TextColor(col).SingleLine().Grow(1).Shrink(1).MinWidth(0).Tooltip(text)
		if sr.running {
			if (IconButton{Icon: icons.Stop, Label: m.FileSearchStop(), Size: Small}).Clicked(c, "stop") {
				sr.stop()
				sr.stopped = true
			}
		}
	})
	rs, whole := sr.results, sr.scope == searchWhole
	sr.list.Key = func(i int) any { return sidePath(remote, rs[i].Dir, rs[i].Entry.Name) }
	zoom := a.settings.FilesScale()
	dense := theme.Dense(*c.Theme())
	dense.FontSize, dense.Spacing = dense.FontSize*zoom, dense.Spacing*zoom
	ui.Themed(c.Key(key+":dense"), dense).Background(ui.Transparent).Grow(1).FillWidth().MinHeight(0).Children(func() {
		list := ui.List(c.Key(key+":results"), &sr.list, len(rs), func(i int) {
			mt := rs[i]
			muted := tok.TextMuted
			if i == sr.sel {
				muted = tok.AccentText // readable on the choice
			}
			row := ui.Row(c).Gap(6).MinHeight(22*zoom).Padding(0, 6).FillWidth().MinWidth(0).AlignItems(ui.Center)
			row.Children(func() {
				fileImage(c, entryIcon(mt.Entry), 14*zoom)
				ui.Text(c, mt.Entry.Name).SingleLine().Shrink(1).MinWidth(0)
				ui.Text(c, searchPlace(remote, sr.root, mt.Dir, whole)).FontSize(theme.Caption * zoom).TextColor(muted).
					SingleLine().Grow(1).Shrink(1).MinWidth(0)
			})
			rb := row.Bounds()
			row.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputPointerDown || ev.Button != 1 {
					return false
				}
				sr.sel = i
				a.openContext(rb.X+ev.X, rb.Y+ev.Y, func() []menuEntry { return a.matchEntries(cn, remote, mt, m) })
				return true
			})
		}).Grow(1).FillWidth().MinHeight(0).Label(m.FileSearchResults())
		if mt, ok := selectedMatch(sr.sel, rs); list.Submitted() && ok {
			a.openMatch(cn, remote, mt)
		}
	})
	return false
}

// selectedMatch is the match of row sel of ms, if any.
func selectedMatch(sel int, ms []sftpsvc.Match) (sftpsvc.Match, bool) {
	if sel < 0 || sel >= len(ms) {
		return sftpsvc.Match{}, false
	}
	return ms[sel], true
}
