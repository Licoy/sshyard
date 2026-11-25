package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// sideList is what each side of a files pane lists: its folder, the
// entries in it, those chosen, by name, and the row last chosen (sel),
// which the keys move from.
type sideList struct {
	dir     string
	entries []sftpsvc.Entry
	sel     int
	chosen  ui.Selection[string]
	list    ui.ListState
	// listed are the entries as the folder listed them, folders first by
	// name. entries are them in the order sorted says, which follows sort,
	// the table's: a click on a column's header sorts by it, another
	// reverses it and a third goes back to the order listed (resort).
	listed       []sftpsvc.Entry
	sort, sorted ui.SortOrder
	// reveal is the name chosen once the folder lists, for an entry gone
	// to from the breadcrumbs.
	reveal string
	// rows are where the rows showed in the last frame, for the row a
	// right-click is on.
	rows []rowMark
	// refocus gives the rows the keys back as their menu closes.
	refocus bool
	// width is the table's width in the last frame, which the columns it
	// shows follow.
	width float32
	// sizes are the sizes of folders here calculated on request, by name.
	sizes map[string]*dirSize
	// typing shows a field to type a path in place of the breadcrumbs,
	// typed its text; typings counts the times it opened, which keys the
	// field, and typeFocus is whether it had the focus in the last frame.
	typing    bool
	typed     string
	typings   int
	typeFocus bool
	typeHold  focusHold
	// search, while set, shows in place of the rows; searches counts the
	// times it opened, which keys its field.
	search   *fileSearch
	searches int
}

// dirSize is the size of a folder, calculated on request.
type dirSize struct {
	size     int64
	counting bool
	cancel   context.CancelFunc
}

// dropSizes stops the folder sizes being calculated and forgets them all.
func (sd *sideList) dropSizes() {
	for _, d := range sd.sizes {
		if d.cancel != nil {
			d.cancel()
		}
	}
	sd.sizes = nil
}

// rowMark is where a row of a side showed: its entry's name and the top
// and bottom of its name, in the window's DIPs.
type rowMark struct {
	name        string
	top, bottom float32
}

// show lists es as the side's folder dir. The entries chosen stay chosen
// in the folder they were chosen in, while they are there; reveal, when
// set, is chosen instead.
func (sd *sideList) show(dir string, es []sftpsvc.Entry) {
	if dir != sd.dir {
		sd.chosen.Clear()
		sd.dropSizes()
	} else {
		there := make(map[string]bool, len(es))
		for _, e := range es {
			there[e.Name] = true
		}
		for name := range sd.chosen.All() {
			if !there[name] {
				sd.chosen.Remove(name)
			}
		}
	}
	sd.dir, sd.listed, sd.entries, sd.sel = dir, es, sortedEntries(es, sd.sorted), -1
	if name := sd.reveal; name != "" {
		sd.reveal = ""
		if i := slices.IndexFunc(sd.entries, func(e sftpsvc.Entry) bool { return e.Name == name }); i >= 0 {
			sd.only(i)
			sd.list.ScrollTo(i, ui.Center)
		}
	}
}

// resort sorts the entries as the table's header was clicked to: by a
// column, ascending, then descending, then in the order listed. The row
// last chosen stays so.
func (sd *sideList) resort() {
	if sd.sort == sd.sorted {
		return
	}
	if sd.sort.Column == sd.sorted.Column && sd.sorted.Descending && !sd.sort.Descending {
		sd.sort = ui.SortOrder{} // the third click
	}
	sd.sorted = sd.sort
	name := ""
	if e, ok := selected(sd.sel, sd.entries); ok {
		name = e.Name
	}
	sd.entries = sortedEntries(sd.listed, sd.sorted)
	if name != "" {
		sd.sel = slices.IndexFunc(sd.entries, func(e sftpsvc.Entry) bool { return e.Name == name })
	}
}

// sortedEntries are es sorted by the column o names, folders still first
// and names breaking ties; es as they are for no column.
func sortedEntries(es []sftpsvc.Entry, o ui.SortOrder) []sftpsvc.Entry {
	if o.Column == "" {
		return es
	}
	out := slices.Clone(es)
	slices.SortStableFunc(out, func(a, b sftpsvc.Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		var r int
		switch o.Column {
		case colSize:
			if !a.IsDir { // a folder's size is not its contents'
				r = cmp.Compare(a.Size, b.Size)
			}
		case colModified:
			r = a.ModTime.Compare(b.ModTime)
		case colPerm:
			r = cmp.Compare(a.Mode.Perm(), b.Mode.Perm())
		}
		r = cmp.Or(r, cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Name, b.Name))
		if o.Descending {
			r = -r
		}
		return r
	})
	return out
}

// only chooses row i alone.
func (sd *sideList) only(i int) {
	sd.chosen.Clear()
	if i >= 0 && i < len(sd.entries) {
		sd.chosen.Add(sd.entries[i].Name)
	}
	sd.sel = i
}

// picked are the entries chosen, in the order the side lists them.
func (sd *sideList) picked() []sftpsvc.Entry {
	if sd.chosen.Len() == 0 {
		return nil
	}
	var out []sftpsvc.Entry
	for _, e := range sd.entries {
		if sd.chosen.Has(e.Name) {
			out = append(out, e)
		}
	}
	return out
}

// paths are the paths of es in the side's folder.
func (sd *sideList) paths(remote bool, es []sftpsvc.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = sidePath(remote, sd.dir, e.Name)
	}
	return out
}

// rowAt is the entry of the row at the window's y, from where the rows
// showed in the last frame: the row whose name is nearest, within the
// room around a name in its row.
func (sd *sideList) rowAt(y float32) (string, bool) {
	const room = 8
	best, dist := "", float32(room+1)
	for _, r := range sd.rows {
		d := max(r.top-y, y-r.bottom, 0)
		if d < dist {
			best, dist = r.name, d
		}
	}
	return best, dist <= room
}

// remoteSide is the remote half of a files pane, on its SFTP client.
type remoteSide struct {
	sideList
	client   *sftpsvc.Client
	home     string // the server's home folder, once connected
	connects int    // the connection the client is on
	opening  bool
	err      string
	// listErr says err came from listing this side. A list that works
	// clears only that, so a plan or transfer error survives the refresh
	// a finished transfer triggers in the same frame.
	// shortcut: it also stays across a later folder change, until the next plan, delete, or reconnect.
	listErr bool
	// unfinished are saves in dir a cut left undone: the file missing,
	// its old version at a backup, to restore or discard.
	unfinished []sftpsvc.Unfinished
	// fixing is a restore or discard under way.
	fixing bool
	// sudoHeld stops SFTP opening again on this connection after sudo
	// failed (Host.SFTPSudo), until the user answers or retries.
	sudoHeld bool
	// listSeq counts the listings asked for: only the last one shows.
	listSeq int
	// cache is the last listing of the folders gone to on client, the
	// latest last, at most dirCacheMax: one gone to again shows at once,
	// while it is listed anew.
	cache []dirListing
}

// dirCacheMax is how many folders a files pane remembers the listing of.
const dirCacheMax = 32

// dirListing is a folder as it last listed.
type dirListing struct {
	dir        string
	entries    []sftpsvc.Entry
	unfinished []sftpsvc.Unfinished
}

// cached is the listing of dir remembered.
func (s *remoteSide) cached(dir string) (dirListing, bool) {
	i := slices.IndexFunc(s.cache, func(l dirListing) bool { return l.dir == dir })
	if i < 0 {
		return dirListing{}, false
	}
	return s.cache[i], true
}

// remember keeps l as the latest listing of its folder, forgetting the
// one used longest ago past dirCacheMax.
func (s *remoteSide) remember(l dirListing) {
	s.cache = slices.DeleteFunc(s.cache, func(c dirListing) bool { return c.dir == l.dir })
	if len(s.cache) >= dirCacheMax {
		s.cache = slices.Delete(s.cache, 0, len(s.cache)-dirCacheMax+1)
	}
	s.cache = append(s.cache, l)
}

// forget drops the listings of dir, and of the folders under it when
// under is set.
func (s *remoteSide) forget(dir string, under bool) {
	s.cache = slices.DeleteFunc(s.cache, func(l dirListing) bool {
		return l.dir == dir || under && remoteUnder(l.dir, dir)
	})
}

// remoteUnder says the server path p is inside the folder dir.
func remoteUnder(p, dir string) bool {
	if dir == "/" {
		return p != "/" && strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// localSide is the local half of a files pane.
type localSide struct {
	sideList
	err string
	// listErr says err came from listing this side. A list that works
	// clears only that, so a plan or transfer error survives the refresh
	// a finished transfer triggers in the same frame.
	// shortcut: it also stays across a later folder change, until the next plan, delete, or reconnect.
	listErr bool
	loaded  bool
}

// filesState is what a files pane keeps: both sides, its transfers and
// its dialogs. Each files pane has its own.
type filesState struct {
	remote    remoteSide
	local     localSide
	transfers *sftpsvc.Transfers
	// ask is the open name or mode dialog (new folder or file, rename,
	// permissions), del the delete confirmation, conflict the same-name
	// dialog of a transfer being planned.
	ask          *nameAsk
	askOpen      bool
	del          *deleteAsk
	delOpen      bool
	conflict     *conflictAsk
	conflictOpen bool
	// open confirms opening a large file in the editor
	open     *openAsk
	openOpen bool
	// with asks how to open a file the app does not show; opens are
	// remote files downloading to open in another app, and copies the
	// copies they open.
	with     *withAsk
	withOpen bool
	opens    []pendingOpen
	copies   localCopies
	// names are the server's user and group names, by ID, once read.
	names *idNames
	// skipped is what the last download planned left out, shown until
	// dismissed or the next download (planned, failing to be, or its
	// question about files already there canceled).
	skipped []sftpsvc.Skip
	// skippedCopy says skipped is from a copy from another server (or
	// folder of this one) rather than a download.
	skippedCopy bool
	// planning is the transfer being planned, until it is ready, fails or
	// is canceled (a newer one cancels it too).
	planning *planRun
	// noFollow is set when the pane stopped following the terminal, and
	// followed is the directory it last went to for it
	noFollow bool
	followed string
	// noReportHidden hides the line saying the terminal followed does not
	// report its directory, snippetOpen is its sheet with the snippet.
	// slowHidden hides the line saying it would follow faster if the
	// terminal reported its directory.
	noReportHidden bool
	slowHidden     bool
	snippetOpen    bool
	// queueOpen is the transfers sheet, queue its list.
	queueOpen bool
	queue     ui.ListState
	// done is how many transfers were done when the sides were last
	// listed again for them, at refreshed.
	done      int
	refreshed time.Time
	// working is the compression or extraction under way; compress,
	// info and preview are their dialogs.
	working      *fileWork
	compress     *compressAsk
	compressOpen bool
	info         *infoAsk
	infoOpen     bool
	preview      *previewState
	previewOpen  bool
	// crumbHover is the crumb the pointer rests on, since crumbSince;
	// crumbLeft when it left the crumb whose menu shows and the menu;
	// crumbHold a crumb clicked, which shows no menu until left. peek is
	// the folder a crumb's menu lists.
	crumbHover string
	crumbSince time.Time
	crumbLeft  time.Time
	crumbHold  string
	peek       peekList
	// opening is set, per side (local, remote), from a double click on a
	// chosen row's name, which opens on the press, to its release, whose
	// click is not a choice in the folder just opened.
	opening [2]bool
	// pub is the SFTP client for the transfers, which run off the UI
	// thread.
	pub struct {
		sync.Mutex
		client   *sftpsvc.Client
		connects int
	}
}

func newFilesState(onChange func()) *filesState {
	fs := &filesState{transfers: &sftpsvc.Transfers{OnChange: onChange}}
	for _, sd := range []*sideList{&fs.remote.sideList, &fs.local.sideList} {
		sd.sel = -1
		sd.list.Selected, sd.list.Selection = &sd.sel, &sd.chosen
		sd.list.DragSelect, sd.list.RoundRows = true, true
		sd.list.Sort = &sd.sort
	}
	return fs
}

// side is the local or the remote side's list.
func (fs *filesState) side(remote bool) *sideList {
	if remote {
		return &fs.remote.sideList
	}
	return &fs.local.sideList
}

func (fs *filesState) close() {
	fs.transfers.CancelAll()
	if fs.working != nil {
		fs.working.cancel()
	}
	if fs.info != nil && fs.info.cancel != nil {
		fs.info.cancel()
	}
	fs.closePreview()
	fs.remote.dropSizes()
	fs.local.dropSizes()
	fs.remote.closeSearch()
	fs.local.closeSearch()
	fs.copies.close()
	fs.pub.Lock()
	fs.pub.client = nil
	fs.pub.Unlock()
	if fs.remote.client != nil {
		fs.remote.client.Close()
	}
}

// publish makes c the client transfers use.
func (fs *filesState) publish(c *sftpsvc.Client, connects int) {
	fs.pub.Lock()
	fs.pub.client, fs.pub.connects = c, connects
	fs.pub.Unlock()
}

// transferClient is a files pane's Transfers.Client: the pane's SFTP
// client once it is open on the connection there is now, waiting while
// the pane connects or reconnects.
func (a *App) transferClient(cn *conn) func(context.Context) (*sftpsvc.Client, error) {
	return func(ctx context.Context) (*sftpsvc.Client, error) {
		for {
			st := cn.s.Status()
			switch st.State {
			case session.Failed, session.Disconnected, session.Closed, session.Exited:
				if st.Err != nil {
					return nil, st.Err
				}
				return nil, errors.New("sftp: " + st.State.String())
			}
			cn.fs.pub.Lock()
			c, n := cn.fs.pub.client, cn.fs.pub.connects
			cn.fs.pub.Unlock()
			if c != nil && st.State == session.Connected && n == st.Connects {
				return c, nil
			}
			if st.State == session.Connected {
				// the pane may not be showing, to open SFTP itself
				a.post(func() { a.ensureSFTP(cn) })
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

// filesProtocol is how a host's files panes reach its files.
func filesProtocol(p assets.FileProtocol) sftpsvc.Protocol {
	switch p {
	case assets.FileSFTP:
		return sftpsvc.SFTPOnly
	case assets.FileSCP:
		return sftpsvc.SCPOnly
	}
	return sftpsvc.Auto
}

// ensureSFTP opens SFTP on the pane's connection, again after it
// reconnected.
func (a *App) ensureSFTP(cn *conn) {
	s := &cn.fs.remote
	st := cn.s.Status()
	h := a.liveHost(cn.host)
	sudo := h.SFTPSudo && h.SSH()
	proto := filesProtocol(h.FileProtocol)
	if st.State != session.Connected || s.opening || (s.client != nil && s.connects == st.Connects && s.client.Sudo() == sudo && (sudo || s.client.Protocol() == proto)) {
		return
	}
	if sudo && s.sudoHeld && s.client == nil && s.connects == st.Connects {
		return
	}
	if s.client != nil {
		cn.fs.publish(nil, 0)
		s.client.Close()
		s.client = nil
	}
	// What was listed on another connection may have changed since.
	s.cache = nil
	client := cn.s.Client()
	if client == nil {
		return
	}
	s.opening, s.err, s.listErr = true, "", false
	connects := st.Connects
	dir, start := s.dir, cn.host.RemoteDir
	go func() {
		var c *sftpsvc.Client
		var err error
		var pass string
		var typed bool
		if sudo {
			pass, typed = a.sudoPassword(h)
			c, err = sftpsvc.NewSudo(context.Background(), client, pass)
		} else {
			c, err = sftpsvc.NewProtocol(context.Background(), client, proto)
		}
		var home string
		var es []sftpsvc.Entry
		var un []sftpsvc.Unfinished
		if err == nil {
			home, err = c.Home()
		}
		listed := false
		if err == nil && dir == "" && start != "" {
			// The host's folder, or home when it cannot be listed.
			d := remoteStart(home, start)
			if es, un, err = c.ListUnfinished(d); err == nil {
				dir, listed = d, true
			} else {
				err = nil
			}
		}
		if err == nil && !listed {
			if dir == "" {
				dir = home
			}
			es, un, err = c.ListUnfinished(dir)
		}
		a.post(func() {
			s.opening = false
			if err != nil {
				s.err, s.listErr = err.Error(), true
				if c != nil {
					c.Close()
				}
				s.connects = connects
				if errors.Is(err, sftpsvc.ErrSudo) || errors.Is(err, sftpsvc.ErrSudoPassword) {
					a.sudoFailed(cn, h, err, pass != "", typed)
				}
				return
			}
			s.sudoHeld = false
			s.client, s.connects, s.unfinished, s.home = c, connects, un, home
			s.listSeq++
			s.cache = []dirListing{{dir: dir, entries: es, unfinished: un}}
			s.show(dir, es)
			cn.fs.publish(c, connects)
			if cn.kind == layout.Files {
				a.readNames(cn, c)
			}
		})
	}()
}

// idNames are a server's user and group names, by ID.
type idNames struct{ users, groups map[uint32]string }

// readNames reads the names of the users and groups of the server of c,
// the pane's SFTP client, for the owners of its files.
func (a *App) readNames(cn *conn, c *sftpsvc.Client) {
	go func() {
		users, groups := c.Names()
		a.post(func() {
			if cn.fs.remote.client == c {
				cn.fs.names = &idNames{users, groups}
			}
		})
	}()
}

// listUnfinished lists a server folder and the saves left undone in it;
// tests stand in for it.
var listUnfinished = (*sftpsvc.Client).ListUnfinished

// listRemote lists the server folder dir. A folder gone to again shows as
// it last listed at once, then as it lists now; only the listing asked
// for last shows. Listing the folder shown again, as after a change in
// it, forgets what was remembered of the folders under it.
func (a *App) listRemote(s *remoteSide, dir string) {
	c := s.client
	if c == nil {
		return
	}
	s.listSeq++
	seq := s.listSeq
	var back *dirListing
	if dir == s.dir {
		s.forget(dir, true)
	} else if l, ok := s.cached(dir); ok {
		back = &dirListing{dir: s.dir, entries: s.listed, unfinished: s.unfinished}
		name := s.reveal
		s.unfinished = l.unfinished
		s.show(dir, l.entries)
		if name != "" && s.sel < 0 {
			// Not there yet: maybe in the listing to come.
			s.reveal = name
		}
	}
	go func() {
		es, un, err := listUnfinished(c, dir)
		a.post(func() {
			if s.listSeq != seq || s.client != c {
				return
			}
			if err != nil {
				s.forget(dir, true)
				if back != nil && s.dir == dir {
					// Gone since: back where it was, as when nothing
					// was remembered.
					s.unfinished = back.unfinished
					s.show(back.dir, back.entries)
				}
				s.err, s.listErr = err.Error(), true
				return
			}
			if s.listErr {
				s.err, s.listErr = "", false
			}
			s.remember(dirListing{dir: dir, entries: es, unfinished: un})
			s.unfinished = un
			s.show(dir, es)
		})
	}()
}

// remoteStart is the server folder a host's RemoteDir names: "~" and a
// relative path are under home.
func remoteStart(home, dir string) string {
	if dir == "~" {
		return home
	}
	dir = strings.TrimPrefix(dir, "~/")
	if path.IsAbs(dir) {
		return path.Clean(dir)
	}
	return path.Join(home, dir)
}

// localStart is the folder of this computer a host's LocalDir names: "~"
// and a relative path are under home.
func localStart(home, dir string) string {
	if dir == "~" {
		return home
	}
	if len(dir) > 1 && dir[0] == '~' && os.IsPathSeparator(dir[1]) {
		dir = dir[2:]
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(home, dir)
}

// listLocalFirst lists the local side of the files pane cn for the first
// time: in its host's LocalDir, or the download folder when unset or unavailable.
func (a *App) listLocalFirst(cn *conn) {
	l := &cn.fs.local
	if l.dir == "" && cn.host.LocalDir != "" {
		dir := localStart(a.localHome(), cn.host.LocalDir)
		if es, err := sftpsvc.ListLocal(dir); err == nil {
			l.loaded = true
			l.show(dir, es)
			return
		}
	}
	dir := l.dir
	if dir == "" {
		dir = a.downloadsDir()
	}
	a.listLocal(l, dir)
}

func (a *App) listLocal(l *localSide, dir string) {
	if dir == "" {
		dir = a.localHome()
	}
	es, err := sftpsvc.ListLocal(dir)
	l.loaded = true
	if err != nil {
		l.err, l.listErr = err.Error(), true
		return
	}
	if l.listErr {
		l.err, l.listErr = "", false
	}
	l.show(dir, es)
}

// goTo lists the folder dir on a side.
func (a *App) goTo(cn *conn, remote bool, dir string) {
	if remote {
		a.listRemote(&cn.fs.remote, dir)
	} else {
		a.listLocal(&cn.fs.local, dir)
	}
}

// listAgain lists a side's folder again.
func (a *App) listAgain(cn *conn, remote bool) {
	a.goTo(cn, remote, cn.fs.side(remote).dir)
}

// Upload copies local files and folders into the remote folder dir,
// asking about those already there.
func (a *App) Upload(cn *conn, paths []string, dir string) {
	a.plan(cn, nil, sftpsvc.Upload, paths, dir)
}

// Download copies remote files and folders into the local folder dir,
// asking about those already there.
func (a *App) Download(cn *conn, paths []string, dir string) {
	a.plan(cn, nil, sftpsvc.Download, paths, dir)
}

// Relay copies files and folders of the server of the files pane from
// into the folder dir of the server of the files pane to, asking about
// those already there, in to's queue. Each file streams from one
// server's SFTP channel to the other's through this app's memory:
// nothing is saved on this computer, and neither server logs into the
// other or is given any credential. from may be to (another folder of
// the same server), where a folder is not copied into itself.
func (a *App) Relay(from, to *conn, paths []string, dir string) {
	if from == nil || from.fs == nil || to.fs == nil || len(paths) == 0 || dir == "" {
		return
	}
	if sameServer(from, to) {
		if err := sftpsvc.CheckInto(paths, dir); err != nil {
			a.filesErr(to, true, err)
			return
		}
	}
	a.plan(to, from, sftpsvc.Relay, paths, dir)
}

// sameServer says the files panes from and to are on the same server as
// the same user: the same pane, connection or saved host.
func sameServer(from, to *conn) bool {
	return from == to || from.s == to.s || from.host.ID != "" && from.host.ID == to.host.ID
}

// plan lists what a transfer of paths into dir copies, then asks about
// what is already there and queues the rest in cn's queue. A Relay's
// paths are on the server of from.
func (a *App) plan(cn, from *conn, d sftpsvc.Direction, paths []string, dir string) {
	c := cn.fs.remote.client
	if c == nil || len(paths) == 0 || dir == "" {
		return
	}
	var src *sftpsvc.Client
	if d == sftpsvc.Relay {
		if src = from.fs.remote.client; src == nil {
			return
		}
	}
	if cn.fs.planning != nil {
		cn.fs.planning.cancel()
	}
	// The previous failure is done with; this plan's own failure, if it
	// has one, is set when it finishes and must survive the refresh.
	remote := d != sftpsvc.Download
	a.filesErr(cn, remote, nil)
	ctx, cancel := context.WithCancel(context.Background())
	run := &planRun{dir: d, cancel: cancel}
	cn.fs.planning = run
	go func() {
		defer cancel()
		var items []sftpsvc.Item
		var skips []sftpsvc.Skip
		var err error
		switch d {
		case sftpsvc.Upload:
			items, err = c.PlanUpload(ctx, paths, dir)
		case sftpsvc.Relay:
			items, skips, err = src.PlanRelay(ctx, paths, c, dir)
		default:
			items, skips, err = c.PlanDownload(ctx, paths, dir)
		}
		a.post(func() {
			if cn.fs.planning != run {
				return // canceled, or a newer plan took its place
			}
			cn.fs.planning = nil
			if d != sftpsvc.Upload {
				// this plan's skips, none when it failed: not the last one's
				cn.fs.skipped, cn.fs.skippedCopy = skips, d == sftpsvc.Relay
			}
			if err != nil {
				a.filesErr(cn, remote, err)
				return
			}
			ask := &conflictAsk{dir: d, items: items, choice: make([]resolution, len(items))}
			if d == sftpsvc.Relay {
				ask.from, ask.fromName = a.transferClient(from), from.host.Label()
			}
			a.resolveConflicts(cn, ask)
		})
	}()
}

// planRun is a transfer being planned.
type planRun struct {
	dir    sftpsvc.Direction
	cancel context.CancelFunc
}

// filesErr shows err on the side a step was on. A nil err clears that
// side. The error is not a listing error, so a refresh leaves it.
// In an editor window, which shows no sides, a failure shows as a toast.
func (a *App) filesErr(cn *conn, remote bool, err error) {
	if cn.ed != nil {
		if err != nil {
			a.editorToast(cn, func(m locales.Messages) ui.Toast {
				return ui.Toast{Title: m.ErrGeneric(securityErrorText(m, err)), Type: "error"}
			})
		}
		return
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if remote {
		cn.fs.remote.err, cn.fs.remote.listErr = msg, false
	} else {
		cn.fs.local.err, cn.fs.local.listErr = msg, false
	}
	a.invalidate()
}

// resolveConflicts goes on through ask's items from ask.at: it asks about
// the next one already there, or, with none left, queues them.
func (a *App) resolveConflicts(cn *conn, ask *conflictAsk) {
	for ; ask.at < len(ask.items); ask.at++ {
		it := ask.items[ask.at]
		if it.IsDir || !it.Exists {
			continue
		}
		if ask.all {
			ask.choice[ask.at] = ask.last
			continue
		}
		cn.fs.conflict, cn.fs.conflictOpen = ask, true
		return
	}
	cn.fs.conflict, cn.fs.conflictOpen = nil, false
	a.enqueue(cn, ask)
}

// enqueue makes the folders of a plan resolved and queues its files.
func (a *App) enqueue(cn *conn, ask *conflictAsk) {
	ts := cn.fs.transfers
	get := ts.Client
	up := ask.dir != sftpsvc.Download // made on the server: an upload's or a relay's
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c, err := get(ctx)
		if err != nil {
			a.post(func() { a.filesErr(cn, up, err) })
			return
		}
		chosen := map[string]bool{}
		taken := func(p string) bool {
			if chosen[p] {
				return true
			}
			if up {
				return c.Exists(p)
			}
			return sftpsvc.LocalExists(p)
		}
		for i, it := range ask.items {
			switch {
			case it.IsDir && up:
				err = c.MkdirAll(it.Dst)
			case it.IsDir:
				err = sftpsvc.MkdirAllLocal(it.Dst)
			case ask.choice[i] == resSkip:
			default:
				dst := it.Dst
				if ask.choice[i] == resRename {
					dst = sftpsvc.UniqueName(dst, up, taken)
				}
				chosen[dst] = true
				if ask.dir == sftpsvc.Relay {
					ts.AddRelay(ask.from, ask.fromName, it.Src, dst, it.Rel)
				} else {
					ts.Add(ask.dir, it.Src, dst, it.Rel)
				}
			}
			if err != nil {
				a.post(func() { a.filesErr(cn, up, err) })
				return
			}
		}
		a.post(func() { a.refreshSides(cn) })
	}()
}

// refreshSides lists both sides of a files pane again: the local side
// once it has shown. An editor window lists its folders again.
func (a *App) refreshSides(cn *conn) {
	if cn.ed != nil {
		a.treeReload(cn)
		return
	}
	fs := cn.fs
	if fs.local.loaded {
		a.listLocal(&fs.local, fs.local.dir)
	}
	if fs.remote.client != nil {
		a.listRemote(&fs.remote, fs.remote.dir)
	}
}

// Mkdir makes a folder on one side of a files pane and lists it again.
func (a *App) Mkdir(cn *conn, remote bool, name string) error {
	fs := cn.fs
	if !remote {
		if _, err := sftpsvc.MkdirLocal(fs.local.dir, name); err != nil {
			return err
		}
		a.listLocal(&fs.local, fs.local.dir)
		return nil
	}
	s := &fs.remote
	if s.client == nil {
		return session.ErrNoCredentials
	}
	if _, err := s.client.Mkdir(s.dir, name); err != nil {
		return err
	}
	a.listRemote(s, s.dir)
	return nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	f, i := float64(n), 0
	for f >= unit && i < 4 {
		f /= unit
		i++
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + "BKMGT"[i:i+1] + "B"
}

// filesDrag is rows dragged from one side of a files pane to the other
// side, of the same files pane or another one.
type filesDrag struct {
	pane   int
	remote bool
	paths  []string
}

// sidePath is the path of the entry in the folder dir of a side.
func sidePath(remote bool, dir, name string) string {
	if remote {
		return sftpsvc.RemoteJoin(dir, name)
	}
	return filepath.Join(dir, name)
}

// toggleMods add a row to the rows chosen or take it out with a click, as
// lists do: Cmd on macOS, Ctrl elsewhere.
var toggleMods = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}()

// deleteMods and deleteKey delete the entries chosen: Cmd+Backspace on
// macOS, as the system does, Delete elsewhere.
var deleteMods, deleteKey = func() (ui.Modifiers, ui.Key) {
	if runtime.GOOS == "darwin" {
		return ui.Super, ui.KeyBackspace
	}
	return 0, ui.KeyDelete
}()

// fileTable is one side's entries, several of which are chosen by clicks
// (with Shift or toggleMods), the keys or a drag over the rows. The name
// of a row chosen drags the entries chosen to the other side. A right
// click opens the menu of the entries chosen (fileEntries), or of the
// folder off the rows. opened reports a double click on a chosen row's
// name, which opens the row as Submitted does. The table shows the
// columns its width has room for (tableColumns), whose headers sort the
// rows (resort).
func (a *App) fileTable(c *ui.Context, cn *conn, remote bool, m locales.Messages, tok theme.Tokens) (tb ui.Element, opened bool) {
	fs := cn.fs
	sd := fs.side(remote)
	sd.resort()
	es, list := sd.entries, &sd.list
	list.Key = func(i int) any { return es[i].Name }
	list.Label = func(i int) string { return es[i].Name }
	ids := tableColumns(sd.width, remote)
	cols := make([]ui.TableColumn, len(ids))
	for i, id := range ids {
		switch id {
		case colName:
			cols[i] = ui.TableColumn{ID: id, Title: m.ColName(), Sortable: true}
		case colSize:
			cols[i] = ui.TableColumn{ID: id, Title: m.ColSize(), Width: 80, Align: ui.End, Sortable: true}
		case colModified:
			cols[i] = ui.TableColumn{ID: id, Title: m.ColModified(), Width: 130, Sortable: true}
		case colPerm:
			cols[i] = ui.TableColumn{ID: id, Title: m.InfoPermissions(), Width: 150, Sortable: true}
		}
	}
	key, side := "ltable", 0
	if remote {
		key, side = "rtable", 1
	}
	entries := func() []menuEntry { return a.fileEntries(cn, remote, m) }
	// menu opens the menu at the window's (x, y) for a right press: on a
	// row not chosen, it chooses that row; off the rows, it
	// chooses none, for the folder's menu.
	menu := func(x, y float32) {
		if name, ok := sd.rowAt(y); !ok {
			sd.chosen.Clear()
			sd.sel = -1
		} else if !sd.chosen.Has(name) {
			sd.only(slices.IndexFunc(sd.entries, func(e sftpsvc.Entry) bool { return e.Name == name }))
		}
		a.openContext(x, y, entries)
		sd.refocus = true
	}
	var chosen []string // the paths a chosen row drags
	if sd.chosen.Len() > 0 {
		chosen = sd.paths(remote, sd.picked())
	}
	extend := -1
	sd.rows = sd.rows[:0]
	// The rows are dense, in the Footnote size, so that a pane shows many,
	// both as large as the files zoom says.
	zoom := a.settings.FilesScale()
	dense := theme.Dense(*c.Theme())
	dense.FontSize, dense.Spacing = dense.FontSize*zoom, dense.Spacing*zoom
	ui.Themed(c.Key(key+":dense"), dense).Background(ui.Transparent).Grow(1).FillWidth().MinHeight(0).Children(func() {
		tb = ui.Table(c.Key(key), list, cols, len(es), func(row, col int) {
			e := es[row]
			on := sd.chosen.Has(e.Name)
			muted := tok.TextMuted
			if on {
				muted = tok.AccentText // readable on the choice
			}
			switch ids[col] {
			case colName:
				ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
					name := ui.Row(c).Gap(6).Shrink(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
						fileImage(c, entryIcon(e), 14*zoom)
						ui.Text(c, e.Name).SingleLine().Shrink(1)
					})
					nb := name.Bounds()
					if nb.H > 0 {
						sd.rows = append(sd.rows, rowMark{e.Name, nb.Y, nb.Y + nb.H})
					} else {
						c.Invalidate() // a row new here: where it is shows next frame
					}
					// The name counts the clicks on it whether its row is chosen
					// or not, so that a double click opens a row the first
					// click chose, and takes the right clicks over it.
					name.HandleInput(func(ev ui.InputEvent) bool {
						if ev.Kind != ui.InputPointerDown || ev.Button != 1 {
							return false
						}
						menu(nb.X+ev.X, nb.Y+ev.Y)
						return true
					})
					if !on {
						return // the list chooses it, or drags a choice over the rows
					}
					// Chosen, the name drags the rows chosen, and takes its clicks.
					name.Drag(filesDrag{pane: cn.id, remote: remote, paths: chosen})
					dbl, clk := name.DoubleClicked(), name.Clicked()
					switch {
					case dbl:
						sd.only(row)
						opened, fs.opening[side] = true, !clk // released yet?
					case clk && fs.opening[side]:
						fs.opening[side] = false
					case clk:
						switch mods := name.ClickModifiers(); {
						case mods&ui.Shift != 0:
							extend = row
						case mods&toggleMods != 0:
							sd.chosen.Remove(e.Name)
							sd.sel = row
						default:
							sd.only(row)
						}
						list.Focus(c)
					}
				})
			case colSize:
				ui.Row(c).Grow(1).Justify(ui.End).AlignItems(ui.Center).Children(func() {
					if !e.IsDir {
						ui.Text(c, humanSize(e.Size)).TextColor(muted).SingleLine()
					} else {
						a.dirSizeCell(c, cn, remote, e.Name, on, tok, m)
					}
				})
			case colModified:
				ui.Text(c, e.ModTime.Format("2006-01-02 15:04")).TextColor(muted).SingleLine().Grow(1)
			case colPerm:
				text, detail := a.permText(fs, remote, e, m)
				ui.Text(c, text).TextColor(muted).SingleLine().Shrink(1).Grow(1).Tooltip(detail)
			}
		}).Grow(1).FillWidth()
	})
	if extend >= 0 {
		list.ExtendTo(extend)
	}
	b := tb.Bounds()
	if b.W != sd.width {
		sd.width = b.W
		if len(tableColumns(b.W, remote)) != len(ids) {
			c.Invalidate() // the columns for this width show next frame
		}
	}
	tb.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputPointerDown || ev.Button != 1 {
			return false
		}
		menu(b.X+ev.X, b.Y+ev.Y)
		return true
	})
	if sd.refocus && a.menu.key != ctxMenuKey {
		// The menu closed: the keys go back to the rows, unless a dialog
		// it opened takes them.
		sd.refocus = false
		if !fs.askOpen && !fs.delOpen && !fs.compressOpen && !fs.infoOpen && !fs.previewOpen {
			tb.Focus()
		}
	}
	a.fileKeys(c, cn, remote, tb, entries)
	return tb, opened
}

// The columns of a side's table, by ID.
const (
	colName     = "name"
	colSize     = "size"
	colModified = "modified"
	colPerm     = "perm"
)

// tableColumns are the columns a side's table has room for at width w (0
// before it has shown): the name, then its size, when it was modified and
// its permissions as the table widens. Windows has no permissions to show
// on the local side.
func tableColumns(w float32, remote bool) []string {
	ids := []string{colName}
	if w == 0 || w >= 260 {
		ids = append(ids, colSize)
	}
	if w >= 420 {
		ids = append(ids, colModified)
	}
	if w >= 600 && (remote || runtime.GOOS != "windows") {
		ids = append(ids, colPerm)
	}
	return ids
}

// permText is an entry's permissions as its table shows them, the
// owner's and in octal, with the owner's name: "rwx (755/ubuntu)"; and
// in full, with the owner and the group, for its tooltip.
func (a *App) permText(fs *filesState, remote bool, e sftpsvc.Entry, m locales.Messages) (text, detail string) {
	perm := e.Mode.Perm()
	text = perm.String()[1:4] + " (" + fmt.Sprintf("%03o", uint32(perm))
	if !e.IDs {
		return text + ")", e.Mode.String()
	}
	user, group := strconv.FormatUint(uint64(e.UID), 10), strconv.FormatUint(uint64(e.GID), 10)
	switch {
	case !remote:
		user, group = sftpsvc.LocalUser(e.UID), sftpsvc.LocalGroup(e.GID)
	case fs.names != nil:
		if n, ok := fs.names.users[e.UID]; ok {
			user = n
		}
		if n, ok := fs.names.groups[e.GID]; ok {
			group = n
		}
	}
	// The server's names, shown so that they cannot break the line.
	user, group = shownName(user), shownName(group)
	return text + "/" + user + ")", m.PermDetail(e.Mode.String(), user, group)
}

// dirSizeCell is a folder's size in its row: a link that calculates it,
// a spinner while it does, then the size.
func (a *App) dirSizeCell(c *ui.Context, cn *conn, remote bool, name string, on bool, tok theme.Tokens, m locales.Messages) {
	sd := cn.fs.side(remote)
	fg := tok.Accent
	if on {
		fg = tok.AccentText
	}
	switch d := sd.sizes[name]; {
	case d == nil:
		label := m.CalculateSizeHint(shownName(name))
		b := ui.ButtonBase(c.Key("calc:"+name)).Label(label).Tooltip(label).Padding(0, 4).Radius(theme.RadiusSmall).
			Disabled(remote && cn.fs.remote.client == nil)
		if !b.IsDisabled() {
			b.Cursor(ui.CursorPointer)
		}
		if b.Hovered() {
			b.Background(fg.Alpha(0.12))
		}
		b.Children(func() {
			ui.Text(c, m.CalculateSize()).FontSize(theme.Footnote).TextColor(fg).SingleLine()
		})
		if b.Clicked() {
			a.calcSize(cn, remote, name)
		}
	case d.counting:
		ui.Spinner(c).Label(m.CalculatingSize(shownName(name))).TextColor(fg)
	default:
		muted := tok.TextMuted
		if on {
			muted = tok.AccentText
		}
		ui.Text(c, humanSize(d.size)).TextColor(muted).SingleLine()
	}
}

// calcSize calculates the size of the folder name, in a side's folder.
func (a *App) calcSize(cn *conn, remote bool, name string) {
	fs := cn.fs
	sd := fs.side(remote)
	sc := fs.remote.client
	if remote && sc == nil {
		return
	}
	p := sidePath(remote, sd.dir, name)
	ctx, cancel := context.WithCancel(context.Background())
	d := &dirSize{counting: true, cancel: cancel}
	if sd.sizes == nil {
		sd.sizes = map[string]*dirSize{}
	}
	sd.sizes[name] = d
	go func() {
		defer cancel()
		var size int64
		var err error
		if remote {
			size, _, err = sc.DirSize(ctx, p)
		} else {
			size, _, err = sftpsvc.LocalDirSize(ctx, p)
		}
		a.post(func() {
			if sd.sizes[name] != d {
				return // gone from the folder, or the folder left
			}
			if err != nil {
				delete(sd.sizes, name)
				return
			}
			d.size, d.counting = size, false
		})
	}()
}

// fileKeys are the keys of a side's table while it has the focus: Delete
// (Cmd+Backspace on macOS), Cmd+I (Ctrl+I elsewhere) for the entries'
// information, Space to preview an image, F2 to rename, Cmd+C and Cmd+V
// (Ctrl elsewhere) to copy entries and paste them, as a file list does,
// Escape to choose none and Shift+F10 for the menu.
func (a *App) fileKeys(c *ui.Context, cn *conn, remote bool, tb ui.Element, entries func() []menuEntry) {
	fs := cn.fs
	sd := fs.side(remote)
	es := sd.picked()
	switch {
	case len(es) == 0:
	case tb.Shortcut(deleteMods, deleteKey) || deleteKey != ui.KeyDelete && tb.Shortcut(0, ui.KeyDelete):
		a.askDelete(cn, remote, es)
	case tb.Shortcut(toggleMods, ui.KeyI):
		a.openInfo(cn, remote, es)
	case len(es) == 1 && isImage(es[0]) && tb.Shortcut(0, ui.KeySpace):
		a.openPreview(cn, remote, es[0].Name)
	case len(es) == 1 && tb.Shortcut(0, ui.KeyF2):
		fs.ask, fs.askOpen = &nameAsk{kind: askRename, remote: remote, path: sidePath(remote, sd.dir, es[0].Name), value: es[0].Name}, true
	case tb.Shortcut(toggleMods, ui.KeyC):
		a.copyEntries(cn, remote, sd.paths(remote, es))
	case tb.Shortcut(0, ui.KeyEscape):
		sd.chosen.Clear()
	}
	if a.canPaste(cn, remote) && tb.Shortcut(toggleMods, ui.KeyV) {
		a.paste(cn, remote)
	}
	if tb.Shortcut(ui.Shift, ui.KeyF10) {
		b := tb.Bounds()
		a.openContext(b.X+theme.Space(6), b.Y+theme.Space(8), entries)
	}
}

// sideHeader is the title row of one side of a files pane, under key: an
// icon, the side's name and its actions, as many as fit (actionBar). The
// folder shows in the breadcrumbs below.
func (a *App) sideHeader(c *ui.Context, key string, tok theme.Tokens, ic *icons.Icon, title string, acts []barAction, m locales.Messages) {
	row := ui.Row(c.Key(key)).Gap(6).Height(24).Padding(0, 2, 0, 4).AlignItems(ui.Center)
	reserve := 4 + 2 + 18 + 6 + titleRoom(c, title) + 6
	row.Children(func() {
		softTile(c, ic, tok.Accent, 18)
		ui.Text(c, title).FontWeight(600).FontSize(theme.Footnote).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		a.actionBar(c, key, row.Bounds().W, reserve, 6, acts, nil, m)
	})
}

// filesCrumbsMin is the room a files pane's breadcrumbs keep beside the
// remote side's actions.
const filesCrumbsMin = 120

// filesBody is a files pane below its header: the server's files, with
// this computer's beside them (stacked when the pane is narrow) while the
// pane shows both (Settings.FilesDual), then the pane's transfers.
func (a *App) filesBody(c *ui.Context, t *layout.Tab, n *layout.Node, cn *conn, width float32, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	dual := a.settings.FilesDual
	if dual && !fs.local.loaded {
		a.listLocalFirst(cn)
	}
	a.ensureSFTP(cn)
	a.refreshForTransfers(c, cn)
	l, s := &fs.local, &fs.remote
	stacked := width > 0 && width < 620
	body := ui.Column(c.Key("files")).Grow(1).FillWidth().Padding(8).Gap(8)
	body.Children(func() {
		a.unfinishedView(c, cn, tok, m)
		a.noReportView(c, t, cn, tok, m)
		a.planningView(c, cn, tok, m)
		a.workingView(c, cn, tok, m)
		a.skippedView(c, cn, tok, m)
		// Each side keeps clear of the line the sides are resized by.
		gap := func(first bool) (top, right, bottom, left float32) {
			switch {
			case !dual:
				return 0, 0, 0, 0
			case stacked && first:
				return 0, 0, 5, 0
			case stacked:
				return 5, 0, 0, 0
			case first:
				return 0, 5, 0, 0
			}
			return 0, 0, 0, 5
		}
		// sideCard is a side's card: its header, the breadcrumbs of its
		// folder and its rows, kept inside its rounded edge.
		sideCard := func(key string) ui.Element {
			return ui.Column(c.Key(key)).Fill().Gap(2).Padding(4, 6, 6, 6).Radius(theme.RadiusCard-2).Border(1, tok.Line).Clip()
		}
		localPane := func() {
			ui.Box(c.Key("lpad")).Fill().Padding(gap(true)).Children(func() {
				local := sideCard("local")
				local.Children(func() {
					picked := l.picked()
					a.sideHeader(c, "lhead:"+strconv.Itoa(cn.id), tok, icons.LocalDisk, m.Local(), []barAction{
						{Key: "lup", Button: IconButton{Icon: icons.GoUp, Label: m.LocalUp(), Size: Tiny},
							Do: func() { a.listLocal(l, filepath.Dir(l.dir)) }},
						{Key: "lrefresh", Button: IconButton{Icon: icons.Refresh, Label: m.LocalRefresh(), Size: Tiny},
							Do: func() { a.listLocal(l, l.dir) }},
						{Key: "lsearch", Button: IconButton{Icon: icons.Search, Label: m.SearchLocalFiles(), Active: l.search != nil, Toggle: true, Size: Tiny},
							Do: l.toggleSearch},
						{Key: "lmkdir", Button: IconButton{Icon: icons.NewFolder, Label: m.LocalNewFolder(), Size: Tiny},
							Do: func() { fs.ask, fs.askOpen = &nameAsk{kind: askMkdir}, true }},
						{Key: "upload", Button: IconButton{Icon: icons.Upload, Label: m.UploadSelected(), Disabled: s.client == nil || len(picked) == 0, Size: Tiny},
							Do: func() { a.Upload(cn, l.paths(false, picked), s.dir) }},
					}, m)
					a.crumbsBar(c, cn, false, tok, m)
					if l.search != nil {
						a.searchView(c, cn, false, tok, m)
					} else {
						tb, opened := a.fileTable(c, cn, false, m, tok)
						if e, ok := selected(l.sel, l.entries); (tb.Submitted() || opened) && ok {
							a.openEntry(cn, false, e)
						}
					}
					if l.err != "" {
						ui.Text(c, securityText(m, l.err)).TextColor(tok.Danger).FontSize(theme.Footnote)
					}
				})
				// remote rows download here, from this pane or another files
				// pane (on its own connection, in its own queue)
				if d, ok := ui.Drop[filesDrag](local); ok && d.remote {
					if src := a.conns[d.pane]; src != nil && src.fs != nil {
						a.Download(src, d.paths, l.dir)
					}
				}
				if d, ok := ui.DragOver[filesDrag](local); ok && d.remote {
					local.Background(tok.Accent.Alpha(0.08))
				}
			})
		}
		// remoteActions are the remote side's actions: in its header
		// beside the local side, else in one row with its breadcrumbs.
		remoteActions := func() []barAction {
			var acts []barAction
			if s.opening {
				acts = append(acts, barAction{Key: "ropening", Show: func() ui.Element { return ui.Spinner(c) }})
			}
			picked := s.picked()
			off := s.client == nil
			acts = append(acts,
				barAction{Key: "rup", Button: IconButton{Icon: icons.GoUp, Label: m.RemoteUp(), Disabled: off, Size: Tiny}, Do: func() {
					if s.dir != "" {
						a.listRemote(s, sftpsvc.RemoteParent(s.dir))
					}
				}},
				barAction{Key: "rrefresh", Button: IconButton{Icon: icons.Refresh, Label: m.RemoteRefresh(), Disabled: off, Size: Tiny},
					Do: func() { a.listRemote(s, s.dir) }},
				barAction{Key: "rsearch", Button: IconButton{Icon: icons.Search, Label: m.SearchRemoteFiles(), Active: s.search != nil, Toggle: true, Disabled: off, Size: Tiny},
					Do: s.toggleSearch},
				barAction{Key: "rmkdir", Button: IconButton{Icon: icons.NewFolder, Label: m.RemoteNewFolder(), Disabled: off, Size: Tiny},
					Do: func() { fs.ask, fs.askOpen = &nameAsk{kind: askMkdir, remote: true}, true }})
			if !dual {
				acts = append(acts,
					barAction{Key: "rupfiles", Button: IconButton{Icon: icons.Upload, Label: m.UploadFiles(), Disabled: off || a.p.PickFiles == nil, Size: Tiny},
						Do: func() { a.pickUpload(cn, s.dir, false, m) }},
					barAction{Key: "rupfolder", Button: IconButton{Icon: icons.UploadFolder, Label: m.UploadFolder(), Disabled: off || a.p.PickFolder == nil, Size: Tiny},
						Do: func() { a.pickUpload(cn, s.dir, true, m) }})
			}
			label := m.DownloadSelected()
			if !dual {
				label = m.DownloadTo()
			}
			return append(acts, barAction{Key: "download", Button: IconButton{Icon: icons.Download, Label: label, Disabled: off || len(picked) == 0, Size: Tiny},
				Do: func() { a.downloadTo(cn, s.paths(true, picked), m) }})
		}
		remotePane := func() {
			ui.Box(c.Key("rpad")).Fill().Padding(gap(false)).Children(func() {
				remote := sideCard("remote")
				remote.Children(func() {
					if dual {
						a.sideHeader(c, "rhead:"+strconv.Itoa(cn.id), tok, icons.Host, m.Remote(), remoteActions(), m)
					} else {
						// The breadcrumbs keep room for a few folders.
						bar := ui.Row(c.Key("rbar")).Gap(2).MinHeight(24).Shrink(0).FillWidth().AlignItems(ui.Center)
						bar.Children(func() {
							ui.Row(c.Key("rcrumbsbox")).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
								if s.client != nil {
									a.crumbsBar(c, cn, true, tok, m)
								}
							})
							a.actionBar(c, "rbar:"+strconv.Itoa(cn.id), bar.Bounds().W, filesCrumbsMin+2, 2, remoteActions(), nil, m)
						})
					}
					if s.client == nil {
						ui.Column(c).Grow(1).Center().Children(func() {
							if s.opening || cn.s.Status().State != session.Connected {
								ui.Text(c, a.pv(a.statusText(m, cn.host, cn.s.Status(), c.Now()))).TextColor(tok.TextMuted)
							}
							if session.Classify(cn.s.Status().Err) == session.KindHostChanged {
								if (IconButton{Icon: icons.HostKey, Label: m.HostKeyReview()}).Clicked(c, "review-host-key") {
									a.reviewHostKey(cn)
								}
							}
							if s.err != "" {
								ui.Text(c, a.pv(m.ErrGeneric(securityText(m, s.err)))).TextColor(tok.Danger).FontSize(theme.Footnote)
							}
							if s.sudoHeld {
								a.sudoHeldActions(c, cn, m)
							}
						})
						if s.search != nil && s.search.running {
							s.search.stop() // it goes on once connected again
						}
						return
					}
					if dual {
						a.crumbsBar(c, cn, true, tok, m)
					}
					if s.search != nil {
						a.searchView(c, cn, true, tok, m)
					} else {
						tb, opened := a.fileTable(c, cn, true, m, tok)
						if a.focusReq == cn.id && a.page == pageWorkspace {
							tb.Focus()
							a.focusReq = 0
						}
						if e, ok := selected(s.sel, s.entries); (tb.Submitted() || opened) && ok {
							a.openEntry(cn, true, e)
						}
						if len(s.entries) == 0 {
							ui.Text(c, m.Empty()).TextColor(tok.TextMuted).FontSize(theme.Footnote)
						}
					}
					if s.err != "" {
						ui.Text(c, m.ErrGeneric(securityText(m, s.err))).TextColor(tok.Danger).FontSize(theme.Footnote)
					}
				})
				if files := remote.DroppedFiles(); len(files) > 0 {
					a.Upload(cn, files, s.dir)
				}
				// local rows upload here, from this pane or another one: the
				// local disk is the same for all. Remote rows of another pane
				// copy here from their server (Relay); this pane's own rows
				// dropped back on it are left alone.
				if d, ok := ui.Drop[filesDrag](remote); ok {
					if src := a.conns[d.pane]; !d.remote {
						a.Upload(cn, d.paths, s.dir)
					} else if d.pane != cn.id && src != nil && src.fs != nil {
						a.Relay(src, cn, d.paths, s.dir)
					}
				}
				if d, ok := ui.DragOver[filesDrag](remote); remote.FileDragOver() || ok && (!d.remote || d.pane != cn.id) {
					remote.Background(tok.Accent.Alpha(0.08))
				}
			})
		}
		switch {
		case !dual:
			ui.Column(c.Key("single")).Grow(1).FillWidth().MinHeight(0).Children(remotePane)
		case stacked:
			ui.SplitVerticalPercent(c.Key("stacked"), &a.filesSplit, localPane, remotePane).Grow(1).FillWidth()
		default:
			ui.SplitPercent(c.Key("sides"), &a.filesSplit, localPane, remotePane).Grow(1).FillWidth()
		}
	})
	if dual {
		a.saveFilesSplit(c)
	}
	if body.FocusWithin() && t.Focus != n.ID {
		t.Focus = n.ID
	}
	a.askView(c, cn, m)
	a.deleteView(c, cn, m)
	a.conflictView(c, cn, m)
	a.openAskView(c, cn, m)
	a.withView(c, cn, tok, m)
	a.snippetView(c, cn, tok, m)
	a.transfersSheet(c, cn, tok, m)
	a.infoView(c, cn, tok, m)
	a.compressView(c, cn, tok, m)
	a.previewView(c, cn, tok, m)
}

// splitRest is how long the files panes' split rests before it is saved.
const splitRest = 500 * time.Millisecond

// saveFilesSplit saves the files panes' split once it rests where it was
// dragged to, kept between 10 and 90 percent.
func (a *App) saveFilesSplit(c *ui.Context) {
	a.filesSplit = min(max(a.filesSplit, 10), 90)
	if a.filesSplit == a.settings.FilesSplit {
		return
	}
	if a.filesSplit != a.splitSeen {
		a.splitSeen, a.splitAt = a.filesSplit, c.Now()
	}
	if wait := splitRest - c.Now().Sub(a.splitAt); wait > 0 {
		c.After(wait)
		return
	}
	st := a.settings
	st.FilesSplit = a.filesSplit
	a.ApplySettings(st)
}

// toggleDual shows this computer's files in the files panes beside the
// server's, or no longer, for every pane and the next ones.
func (a *App) toggleDual() {
	st := a.settings
	st.FilesDual = !st.FilesDual
	a.ApplySettings(st)
}

// pickUpload asks for files, or a folder, to upload into the remote
// folder dir of a files pane or an editor window.
func (a *App) pickUpload(cn *conn, dir string, folder bool, m locales.Messages) {
	id := cn.id
	title := m.ChooseUploadFiles()
	if folder {
		title = m.ChooseUploadFolder()
	}
	go func() {
		var paths []string
		var err error
		if folder {
			var p string
			if p, err = a.p.PickFolder(title, ""); p != "" {
				paths = []string{p}
			}
		} else {
			paths, err = a.p.PickFiles(title)
		}
		a.post(func() {
			cn := a.conns[id]
			if cn == nil || cn.fs == nil {
				return
			}
			if err != nil {
				a.filesErr(cn, true, err)
				return
			}
			a.Upload(cn, paths, dir)
		})
	}()
}

// downloadTo downloads remote paths of a files pane: into the local
// folder it shows beside them, else into a folder asked for.
func (a *App) downloadTo(cn *conn, paths []string, m locales.Messages) {
	if len(paths) == 0 {
		return
	}
	if a.settings.FilesDual && cn.fs.local.dir != "" {
		a.Download(cn, paths, cn.fs.local.dir)
		return
	}
	if a.p.PickFolder == nil {
		return
	}
	id, title, start := cn.id, m.ChooseDownloadFolder(), a.downloadsDir()
	go func() {
		dir, err := a.p.PickFolder(title, start)
		a.post(func() {
			cn := a.conns[id]
			if cn == nil || cn.fs == nil {
				return
			}
			if err != nil {
				a.filesErr(cn, false, err)
				return
			}
			a.Download(cn, paths, dir)
		})
	}()
}

// localHome is the base for host-relative local paths.
func (a *App) localHome() string {
	if a.p.HomeDir != "" {
		return a.p.HomeDir
	}
	dir, _ := os.UserHomeDir()
	return dir
}

// selected is the entry of row sel of es, if any.
func selected(sel int, es []sftpsvc.Entry) (sftpsvc.Entry, bool) {
	if sel < 0 || sel >= len(es) {
		return sftpsvc.Entry{}, false
	}
	return es[sel], true
}

// refreshForTransfers lists the sides again as transfers finish: once
// all are, else at most every second.
func (a *App) refreshForTransfers(c *ui.Context, cn *conn) {
	fs := cn.fs
	done, active := 0, 0
	for _, t := range fs.transfers.List() {
		switch {
		case t.State == sftpsvc.Done:
			done++
		case t.Active():
			active++
		}
	}
	if done < fs.done {
		fs.done = done // cleared
	}
	if done == fs.done {
		return
	}
	if active == 0 || c.Now().Sub(fs.refreshed) >= time.Second {
		fs.done, fs.refreshed = done, c.Now()
		a.refreshSides(cn)
		return
	}
	c.After(time.Second)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
