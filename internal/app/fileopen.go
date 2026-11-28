package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path"
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

// appFiles are the extensions of files made for other apps (documents,
// media, archives, programs, fonts, databases): a double click asks how to
// open one instead of reading it into the editor.
var appFiles = map[string]bool{}

func init() {
	for _, ext := range strings.Fields(`
		pdf doc docx xls xlsx ppt pptx odt ods odp rtf pages numbers key epub
		mp3 wav flac aac ogg oga m4a wma aif aiff opus mid midi
		mp4 m4v mov avi mkv webm wmv flv mpg mpeg 3gp
		zip tar gz tgz bz2 tbz2 xz txz zst 7z rar lz lzma cab iso dmg img
		exe msi dll so dylib bin o a lib class jar war apk ipa deb rpm pkg app
		ttf otf woff woff2 eot
		db sqlite sqlite3 mdb accdb
		ico icns tif tiff psd ai sketch fig heic heif avif raw cr2 nef
		pyc wasm`) {
		appFiles[ext] = true
	}
}

// appFile says whether the file name is one made for another app.
func appFile(name string) bool {
	return appFiles[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]
}

// sideBase is the name of the entry at p, a path of a side.
func sideBase(remote bool, p string) string {
	if remote {
		return path.Base(p)
	}
	return filepath.Base(p)
}

// openEntry opens an entry of a side, as a double click or Enter does: a
// folder lists, an image shows, a file made for another app asks how to
// open it, and another file opens in a tab of the editor window of its host.
func (a *App) openEntry(cn *conn, remote bool, e sftpsvc.Entry) {
	sd := cn.fs.side(remote)
	p := sidePath(remote, sd.dir, e.Name)
	switch {
	case e.IsDir:
		a.goTo(cn, remote, p)
	case isImage(e):
		a.openPreview(cn, remote, e.Name)
	case appFile(e.Name):
		a.askOpenWith(cn, &withAsk{remote: remote, path: p, size: e.Size})
	default:
		w := &withAsk{remote: remote, path: p, size: e.Size, how: a.textEditor(e.Name)}
		if remote && w.how != nil {
			w.probe = true
			a.openExternal(cn, w, *w.how)
		} else {
			a.editIn(cn, w, false)
		}
	}
}

// openAsk confirms opening a large file in the editor.
type openAsk struct {
	remote bool
	path   string
	size   int64
	run    func()
}

// openTimeout is how long opening or saving waits for the connection.
const openTimeout = 30 * time.Second

// editorHardReadLimit is the most the editor reads, confirmed or not.
const editorHardReadLimit int64 = 256 << 20

// editFile opens a file of a side in the editor: the tab already showing
// it, else after asking about a large one. A file that is not
// text asks how to open it instead, unless asText.
func (a *App) editFile(cn *conn, remote bool, p string, size int64, asText bool) {
	a.editIn(cn, &withAsk{remote: remote, path: p, size: size}, asText)
}

func (a *App) editIn(cn *conn, w *withAsk, asText bool) {
	if w.how == nil {
		if ecn, ed := a.editorTab(cn.host.ID, w.remote, w.path); ed != nil {
			a.showTab(ecn, ed)
			return
		}
	}
	if w.remote && cn.fs.remote.client == nil {
		return
	}
	if !a.confirmOpen(cn, w, func() { a.readForEditor(cn, w, true, asText) }) {
		a.readForEditor(cn, w, false, asText)
	}
}

func (a *App) confirmOpen(cn *conn, w *withAsk, run func()) bool {
	if w.size <= int64(a.settings.OpenConfirmMB)<<20 {
		return false
	}
	cn.fs.open, cn.fs.openOpen = &openAsk{remote: w.remote, path: w.path, size: w.size, run: run}, true
	return true
}

// readForEditor reads a file for the editor, within the size opening
// asks from (or the editor's ceiling once a larger one was confirmed).
func (a *App) readForEditor(cn *conn, w *withAsk, sizeConfirmed, asText bool) {
	remote, p := w.remote, w.path
	limit := min(int64(a.settings.OpenConfirmMB)<<20, editorHardReadLimit)
	if sizeConfirmed {
		limit = editorHardReadLimit
	}
	get := cn.fs.transfers.Client
	go func() {
		var data []byte
		var ver sftpsvc.Version
		var err error
		if remote {
			ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
			defer cancel()
			var c *sftpsvc.Client
			if c, err = get(ctx); err == nil {
				data, ver, err = c.ReadFileContext(ctx, p, limit)
			}
		} else {
			data, ver, err = sftpsvc.ReadLocalFile(p, limit)
		}
		a.post(func() {
			if cn.id != 0 && a.conns[cn.id] != cn {
				return
			}
			switch {
			case err != nil:
				a.openFailed(cn, sideBase(remote, p), err)
			case !asText && !sftpsvc.IsText(data):
				a.askOpenWith(cn, &withAsk{remote: remote, path: p, size: ver.Size, data: data, ver: ver})
			case w.how != nil:
				a.openExternal(cn, &withAsk{remote: remote, path: p, size: ver.Size, data: data, ver: ver}, *w.how)
			default:
				a.openFileEditor(cn, remote, p, data, ver)
			}
		})
	}()
}

// openFailed says, in the window of cn, that the file name did not open.
func (a *App) openFailed(cn *conn, name string, err error) {
	a.toastIn(cn, func(m locales.Messages) ui.Toast {
		return ui.Toast{Title: m.OpenFailed(shownName(name), externalErrorText(m, err)), Type: "error"}
	})
}

// openAskView confirms opening a large file in the editor.
func (a *App) openAskView(c *ui.Context, cn *conn, m locales.Messages) {
	fs := cn.fs
	ask := fs.open
	if ask == nil {
		return
	}
	// The name may be the server's: shown as in the skip notice, so that
	// it cannot break the line or reorder what the question says.
	name := shownName(sideBase(ask.remote, ask.path))
	msg := m.OpenLargeMessage(name, humanSize(ask.size))
	if !ask.remote {
		msg = m.OpenLargeLocalMessage(name, humanSize(ask.size))
	}
	if ui.AlertDialog(c.Key("openask"), &fs.openOpen, m.OpenLargeTitle(), msg, m.Cancel(), m.OpenAnyway()) == 1 {
		ask.run()
	}
	if !fs.openOpen && fs.open == ask {
		fs.open = nil
	}
}

// withAsk asks how to open a file the app does not show: in its default
// app, in an app chosen, as text, or downloaded (a remote one). data is
// the file, when it was read for the editor and is not text.
type withAsk struct {
	remote bool
	path   string
	size   int64
	data   []byte
	ver    sftpsvc.Version
	how    *extHow
	probe  bool
}

func (a *App) askOpenWith(cn *conn, w *withAsk) {
	cn.fs.with, cn.fs.withOpen = w, true
}

// openWith opens a file of a side in another app, its default one or
// one chosen; a remote file a program could be made to look like a
// document asks first.
func (a *App) openWith(cn *conn, remote bool, p string, size int64, choose bool, m locales.Messages) {
	a.editExternally(cn, remote, p, size, extHow{system: !choose, choose: choose, title: m.ChooseAppTitle(shownName(sideBase(remote, p)))})
}

// withView is the sheet asking how to open a file the app does not show.
func (a *App) withView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	w := fs.with
	if w == nil {
		return
	}
	name := sideBase(w.remote, w.path)
	sheet(ui.Modal(c.Key("openwith"), &fs.withOpen, func() {
		ui.Column(c).Gap(14).Width(380).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				fileImage(c, entryIcon(sftpsvc.Entry{Name: name}), 30)
				ui.Text(c, m.OpenWithTitle(shownName(name))).FontSize(theme.Title3).FontWeight(600).Shrink(1).MinWidth(0).SingleLine()
			})
			ui.Text(c, m.OpenWithMessage()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			if w.remote {
				ui.Text(c, m.OpenWithCopyNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
				if sftpsvc.Executable(name) {
					col := tok.Warning
					ui.Row(c).FillWidth().Padding(6, 10).Gap(theme.Space(2)).AlignItems(ui.Start).
						Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
						ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(col).Margin(2, 0, 0, 0)
						ui.Text(c, m.OpenExecMessage(shownName(name))).FontSize(theme.Footnote).Grow(1).Shrink(1)
					})
				}
			}
			ui.Column(c).Gap(8).FillWidth().Margin(4, 0, 0, 0).Children(func() {
				choice := func(label string, primary, disabled bool) bool {
					el, ok := pillEl(c, label, primary, false, disabled)
					el.FillWidth()
					return ok
				}
				switch {
				case w.how == nil:
					if choice(m.OpenWithDefault(), true, a.p.OpenPath == nil) {
						fs.withOpen = false
						a.openOutside(cn, w, extHow{system: true})
					}
					if choice(m.ChooseApp(), false, a.p.OpenWith == nil) {
						fs.withOpen = false
						a.openOutside(cn, w, extHow{choose: true, title: m.ChooseAppTitle(shownName(name))})
					}
				default:
					label := m.OpenWithDefault()
					if w.how.choose {
						label = m.ChooseApp()
					} else if w.how.editor != nil {
						label = m.OpenAnyway()
					}
					if choice(label, true, false) {
						fs.withOpen = false
						a.openOutside(cn, w, *w.how)
					}
				}
				if choice(m.OpenAsText(), false, false) {
					fs.withOpen = false
					if w.data != nil {
						a.openFileEditor(cn, w.remote, w.path, w.data, w.ver)
					} else {
						a.editFile(cn, w.remote, w.path, w.size, true)
					}
				}
				if w.remote && choice(m.DownloadTo(), false, a.p.PickFolder == nil) {
					fs.withOpen = false
					a.downloadTo(cn, []string{w.path}, m)
				}
				if choice(m.Cancel(), false, false) {
					fs.withOpen = false
				}
			})
		})
	}), tok)
	if !fs.withOpen && fs.with == w {
		fs.with = nil
	}
}

// localCopies are the copies of remote files a files pane opened in
// other apps, in the pane's folder of the cache, made on first use and
// deleted with the pane.
type localCopies struct {
	mu  sync.Mutex
	dir string
	// next numbers the copies' folders in dir; never reused, a failed
	// copy's folder (removed) included.
	next int
}

// writeLocalCopy writes a copy (a variable for tests).
var writeLocalCopy = os.WriteFile

// copyPath makes a new folder in the pane's folder of the cache for a
// copy of the remote file name, and is the copy's path there.
func (a *App) copyPath(lc *localCopies, name string) (string, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.dir == "" {
		root, err := a.cacheDir()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		// Named for this process, so that a later start knows when it is
		// gone (cleanCache).
		dir := filepath.Join(root, strconv.Itoa(os.Getpid())+"-"+hex.EncodeToString(b))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
		lc.dir = dir
	}
	sub := filepath.Join(lc.dir, strconv.Itoa(lc.next))
	local, err := sftpsvc.LocalPath(sub, name)
	if err != nil {
		return "", err
	}
	lc.next++
	if err := os.Mkdir(sub, 0o700); err != nil {
		return "", err
	}
	return local, nil
}

// close deletes the copies.
func (lc *localCopies) close() {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.dir != "" {
		os.RemoveAll(lc.dir)
		lc.dir = ""
	}
}

// cacheDir is where files opened in other apps go.
func (a *App) cacheDir() (string, error) {
	if a.p.CacheDir != "" {
		return a.p.CacheDir, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sshyard", "open"), nil
}

// cleanCache deletes the copies of files opened in other apps that a
// process no longer running left (it quit without closing its panes):
// those of a running one, maybe another window of the app, stay, as
// does anything not named as a pane's folder.
func cleanCache(root string) {
	es, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range es {
		pid, rest, ok := strings.Cut(e.Name(), "-")
		n, err := strconv.Atoi(pid)
		if !e.IsDir() || !ok || err != nil || n <= 0 || len(rest) != 12 {
			// Named before pids were: gone once a day old.
			if fi, err := e.Info(); err == nil && e.IsDir() && len(e.Name()) == 12 && isHex(e.Name()) && time.Since(fi.ModTime()) > 24*time.Hour {
				os.RemoveAll(filepath.Join(root, e.Name()))
			}
			continue
		}
		if !isHex(rest) || n == os.Getpid() || processAlive(n) {
			continue
		}
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
