package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// isImage reports whether e is a file the preview shows.
func isImage(e sftpsvc.Entry) bool {
	if e.IsDir {
		return false
	}
	switch strings.ToLower(path.Ext(e.Name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// fileWork is a compression or an extraction under way on a side, one at
// a time per files pane.
type fileWork struct {
	remote, extract bool
	name            string
	cancel          context.CancelFunc
}

// workingView shows the compression or extraction under way, with a way
// to cancel it.
func (a *App) workingView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	w := cn.fs.working
	if w == nil {
		return
	}
	text, ic := m.Compressing(shownName(w.name)), icons.Compress
	if w.extract {
		text, ic = m.Extracting(shownName(w.name)), icons.Extract
	}
	col := tok.Accent
	ui.Row(c.Key("working")).FillWidth().Padding(6, 8, 6, 12).Gap(8).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(col.Alpha(0.12)).Border(1, col.Alpha(0.22)).Children(func() {
		ui.Icon(c, ic.SVG).Size(14, 14).Shrink(0).TextColor(col)
		ui.Text(c, text).FontSize(theme.Footnote).Grow(1).Shrink(1)
		if pill(c, m.Cancel(), false, false, false) {
			w.cancel()
			cn.fs.working = nil
		}
	})
}

// startWork runs job, a compression or an extraction on a side, off the
// UI thread. Once it is done, the side's folder dir lists again, with
// made (what it made there) chosen; a failure shows on the side.
func (a *App) startWork(cn *conn, w fileWork, dir, made string, job func(context.Context) error) {
	fs := cn.fs
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	run := &w
	fs.working = run
	a.filesErr(cn, w.remote, nil)
	go func() {
		err := job(ctx)
		cancel()
		a.post(func() {
			if fs.working == run {
				fs.working = nil
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				a.filesErr(cn, w.remote, err)
			}
			if sd := fs.side(w.remote); sd.dir == dir {
				if err == nil {
					sd.reveal = made
				}
				a.listAgain(cn, w.remote)
			}
		})
	}()
}

// errOutput keeps the start of what a command writes to its errors.
type errOutput struct{ b bytes.Buffer }

func (o *errOutput) Write(p []byte) (int, error) {
	if room := 4096 - o.b.Len(); room > 0 {
		o.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// runRemote runs the shell command cmd on the server, ended when ctx is.
// A failure is the last line the command wrote to its errors, if any.
func runRemote(ctx context.Context, client *ssh.Client, cmd string) error {
	if client == nil {
		return errNotConnected
	}
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	var out errOutput
	sess.Stderr = &out
	stop := context.AfterFunc(ctx, func() {
		sess.Signal(ssh.SIGTERM)
		sess.Close()
	})
	defer stop()
	err = sess.Run(cmd)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		lines := strings.Split(strings.TrimSpace(out.b.String()), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return errors.New(shownName(last))
		}
	}
	return err
}

// compressAsk is the dialog naming the archive of entries of a side.
type compressAsk struct {
	remote bool
	dir    string
	names  []string
	name   string
	format int // the index of an sftpsvc.ArchiveFormat
	err    string
}

// askCompress asks for the name of an archive of es, entries of a side.
func (a *App) askCompress(cn *conn, remote bool, es []sftpsvc.Entry, m locales.Messages) {
	fs := cn.fs
	if len(es) == 0 || fs.working != nil {
		return
	}
	// The server has tar, not always zip; this computer opens a zip.
	f := sftpsvc.Zip
	if remote {
		f = sftpsvc.TarGz
	}
	name := m.ArchiveDefaultName()
	if len(es) == 1 {
		name = es[0].Name
	}
	names := make([]string, len(es))
	for i, e := range es {
		names[i] = e.Name
	}
	fs.compress = &compressAsk{remote: remote, dir: fs.side(remote).dir, names: names, name: name + f.Ext(), format: int(f)}
	fs.compressOpen = true
}

func (a *App) compressView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	ask := fs.compress
	if ask == nil {
		return
	}
	sheet(ui.Modal(c.Key("compress"), &fs.compressOpen, func() {
		ui.Column(c).Gap(14).Width(380).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				softTile(c, icons.Compress, tok.Accent, 30)
				ui.Text(c, m.CompressTitle()).FontSize(theme.Title3).FontWeight(600).Shrink(1)
			})
			in := formTextInput(c.Key("archname"), &ask.name).AutoFocus().Label(m.ArchiveName()).Placeholder(m.ArchiveName()).FillWidth()
			a.compressHold.apply(c, ask, in)
			was := sftpsvc.ArchiveFormat(ask.format)
			if formSegmented(c.Key("archfmt"), &ask.format, false, "zip", "tar.gz").Label(m.ArchiveFormat()).Changed() {
				if base, ok := strings.CutSuffix(ask.name, was.Ext()); ok {
					ask.name = base + sftpsvc.ArchiveFormat(ask.format).Ext()
				}
			}
			if ask.err != "" {
				ui.Text(c, m.ErrGeneric(securityText(m, ask.err))).TextColor(tok.Danger).FontSize(theme.Footnote)
			}
			submit := in.Submitted()
			ui.Row(c).Gap(8).Margin(4, 0, 0, 0).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					fs.compressOpen = false
				}
				if pill(c, m.CompressTitle(), true, false, false) {
					submit = true
				}
			})
			if submit {
				if err := a.compress(cn, ask, m); err != nil {
					ask.err = err.Error()
				} else {
					fs.compressOpen = false
				}
			}
		})
	}), tok)
	if !fs.compressOpen {
		fs.compress = nil
	}
}

// compress starts making the archive ask names, or says why it cannot.
func (a *App) compress(cn *conn, ask *compressAsk, m locales.Messages) error {
	fs := cn.fs
	name := strings.TrimSpace(ask.name)
	if err := sftpsvc.SafeName(name); err != nil {
		return err
	}
	if fs.working != nil {
		return nil
	}
	sd := fs.side(ask.remote)
	exists := errors.New(m.ErrNameExists(name))
	if slices.ContainsFunc(sd.listed, func(e sftpsvc.Entry) bool { return e.Name == name }) && sd.dir == ask.dir {
		return exists
	}
	dir, names, f := ask.dir, ask.names, sftpsvc.ArchiveFormat(ask.format)
	dst := sidePath(ask.remote, dir, name)
	w := fileWork{remote: ask.remote, name: name}
	if !ask.remote {
		a.startWork(cn, w, dir, name, func(ctx context.Context) error {
			if sftpsvc.LocalExists(dst) {
				return exists
			}
			return sftpsvc.CompressLocal(ctx, dir, names, dst, f)
		})
		return nil
	}
	sc, client := fs.remote.client, cn.s.Client()
	if sc == nil || client == nil {
		return errNotConnected
	}
	a.startWork(cn, w, dir, name, func(ctx context.Context) error {
		if sc.Exists(dst) {
			return exists
		}
		err := runRemote(ctx, client, sftpsvc.CompressCommand(dir, names, dst, f))
		if err != nil {
			sc.Remove(dst) // what the command made of it
		}
		return err
	})
	return nil
}

// extract extracts the archive name of a side into a new folder next to
// it, named as the archive without its extension.
func (a *App) extract(cn *conn, remote bool, name string) {
	fs := cn.fs
	if fs.working != nil {
		return
	}
	sd := fs.side(remote)
	dir := sd.dir
	archive := sidePath(remote, dir, name)
	taken := make(map[string]bool, len(sd.listed))
	for _, e := range sd.listed {
		taken[e.Name] = true
	}
	dst := sftpsvc.UniqueName(sidePath(remote, dir, sftpsvc.ArchiveBase(name)), remote, func(p string) bool {
		if remote {
			return taken[path.Base(p)]
		}
		return taken[filepath.Base(p)]
	})
	made := path.Base(filepath.ToSlash(dst))
	w := fileWork{remote: remote, extract: true, name: name}
	if !remote {
		a.startWork(cn, w, dir, made, func(ctx context.Context) error {
			if err := os.Mkdir(dst, 0o755); err != nil {
				return err
			}
			err := sftpsvc.ExtractLocal(ctx, archive, dst)
			if err != nil {
				os.RemoveAll(dst)
			}
			return err
		})
		return
	}
	sc, client := fs.remote.client, cn.s.Client()
	if sc == nil || client == nil {
		a.filesErr(cn, true, errNotConnected)
		return
	}
	a.startWork(cn, w, dir, made, func(ctx context.Context) error {
		cmd, err := sftpsvc.ExtractCommand(archive, dst)
		if err != nil {
			return err
		}
		if _, err := sc.Mkdir(dir, made); err != nil {
			return err
		}
		if err = runRemote(ctx, client, cmd); err != nil {
			sc.Remove(dst)
		}
		return err
	})
}

// infoAsk is the information shown about entries of a side: the size of
// the folders among them is counted as it shows.
type infoAsk struct {
	remote   bool
	dir      string
	es       []sftpsvc.Entry
	size     int64
	files    int
	counting bool
	cancel   context.CancelFunc
}

// openInfo shows the information of es, entries of a side.
func (a *App) openInfo(cn *conn, remote bool, es []sftpsvc.Entry) {
	fs := cn.fs
	if len(es) == 0 {
		return
	}
	if fs.info != nil && fs.info.cancel != nil {
		fs.info.cancel()
	}
	sd := fs.side(remote)
	info := &infoAsk{remote: remote, dir: sd.dir, es: es}
	fs.info, fs.infoOpen = info, true
	var dirs []string
	for _, e := range es {
		if e.IsDir {
			dirs = append(dirs, sidePath(remote, sd.dir, e.Name))
		} else {
			info.size += e.Size
			info.files++
		}
	}
	if len(dirs) == 0 {
		return
	}
	sc := fs.remote.client
	if remote && sc == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	info.counting, info.cancel = true, cancel
	go func() {
		defer cancel()
		var size int64
		var files int
		for _, d := range dirs {
			var s int64
			var n int
			var err error
			if remote {
				s, n, err = sc.DirSize(ctx, d)
			} else {
				s, n, err = sftpsvc.LocalDirSize(ctx, d)
			}
			if err != nil {
				return
			}
			size, files = size+s, files+n
		}
		a.post(func() {
			info.size, info.files, info.counting = info.size+size, info.files+files, false
		})
	}()
}

func (a *App) infoView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	info := fs.info
	if info == nil {
		return
	}
	es, one := info.es, len(info.es) == 1
	title := m.ItemsCount(len(es))
	if one {
		title = shownName(es[0].Name)
	}
	sheet(ui.Modal(c.Key("info"), &fs.infoOpen, func() {
		ui.Column(c).Gap(14).Width(400).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				if one {
					fileImage(c, entryIcon(es[0]), 30)
				} else {
					softTile(c, icons.Info, tok.Accent, 30)
				}
				ui.Text(c, title).FontSize(theme.Title3).FontWeight(600).Shrink(1).MinWidth(0).SingleLine()
			})
			ui.Column(c).Gap(8).FillWidth().Children(func() {
				row := func(label, value string) {
					ui.Row(c).Gap(12).AlignItems(ui.Start).FillWidth().Children(func() {
						ui.Text(c, label).FontSize(theme.Footnote).TextColor(tok.TextMuted).Width(96).Shrink(0)
						ui.Text(c, value).FontSize(theme.Footnote).Grow(1).Shrink(1).MinWidth(0).Selectable()
					})
				}
				if one {
					e := es[0]
					kind := m.KindFile()
					switch {
					case e.IsDir:
						kind = m.KindFolder()
					case e.Mode&os.ModeSymlink != 0:
						kind = m.KindLink()
					}
					row(m.InfoKind(), kind)
				}
				row(m.InfoWhere(), shownName(info.dir))
				var size string
				switch {
				case info.counting:
					size = m.InfoCounting()
				case one && !es[0].IsDir:
					size = m.InfoBytes(humanSize(info.size), strconv.FormatInt(info.size, 10))
				default:
					size = m.InfoFolderSize(humanSize(info.size), info.files)
				}
				row(m.ColSize(), size)
				if one {
					e := es[0]
					row(m.ColModified(), e.ModTime.Format("2006-01-02 15:04:05"))
					row(m.InfoPermissions(), e.Mode.String()+" ("+strconv.FormatUint(uint64(e.Mode.Perm()), 8)+")")
				}
			})
			ui.Row(c).Gap(8).Margin(4, 0, 0, 0).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Close(), true, false, false) {
					fs.infoOpen = false
				}
			})
		})
	}), tok)
	if !fs.infoOpen {
		if info.cancel != nil {
			info.cancel()
		}
		fs.info = nil
	}
}

// previewLimit is the largest image the preview reads, previewPixels
// the most pixels it decodes.
const (
	previewLimit  = 32 << 20
	previewPixels = 64 << 20
)

// previewState is the image preview of a side: the images of its folder,
// the one shown and its picture once read.
type previewState struct {
	remote  bool
	dir     string
	images  []sftpsvc.Entry
	at      int
	bmp     *ui.Bitmap
	err     string
	big     bool // too large to show
	loading bool
	cancel  context.CancelFunc
	loads   int // the reads started, the last of which shows
}

// openPreview shows the image name of a side, among the other images of
// its folder.
func (a *App) openPreview(cn *conn, remote bool, name string) {
	fs := cn.fs
	sd := fs.side(remote)
	var images []sftpsvc.Entry
	at := 0
	for _, e := range sd.entries {
		if isImage(e) {
			if e.Name == name {
				at = len(images)
			}
			images = append(images, e)
		}
	}
	if len(images) == 0 {
		return
	}
	fs.closePreview()
	fs.preview = &previewState{remote: remote, dir: sd.dir, images: images, at: at}
	fs.previewOpen = true
	a.loadPreview(cn)
}

// closePreview stops reading the image shown.
func (fs *filesState) closePreview() {
	if p := fs.preview; p != nil && p.cancel != nil {
		p.cancel()
	}
	fs.preview = nil
}

// loadPreview reads and decodes the preview's image off the UI thread.
func (a *App) loadPreview(cn *conn) {
	p := cn.fs.preview
	if p.cancel != nil {
		p.cancel()
	}
	e := p.images[p.at]
	p.bmp, p.err, p.big, p.loading, p.cancel = nil, "", false, false, nil
	if e.Size > previewLimit {
		p.big = true
		return
	}
	sc := cn.fs.remote.client
	if p.remote && sc == nil {
		p.err = errNotConnected.Error()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.loading, p.cancel = true, cancel
	p.loads++
	full, load := sidePath(p.remote, p.dir, e.Name), p.loads
	go func() {
		defer cancel()
		var data []byte
		var err error
		if p.remote {
			data, _, err = sc.ReadFileContext(ctx, full, previewLimit)
		} else {
			data, err = readLocalLimit(full, previewLimit)
		}
		var bmp *ui.Bitmap
		if err == nil {
			bmp, err = decodePreview(data)
		}
		a.post(func() {
			if cn.fs.preview != p || p.loads != load {
				return // closed, or another image shows
			}
			p.loading = false
			switch {
			case errors.Is(err, sftpsvc.ErrReadLimit):
				p.big = true
			case err != nil:
				p.err = err.Error()
			default:
				p.bmp = bmp
			}
		})
	}()
}

// readLocalLimit reads the local file p, of at most limit bytes.
func readLocalLimit(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = sftpsvc.ErrReadLimit
	}
	return data, err
}

// errTooManyPixels is an image larger than the preview decodes.
var errTooManyPixels = errors.New("image too large")

// decodePreview decodes an image for the preview, refusing one of more
// than previewPixels pixels before decoding it.
func decodePreview(data []byte) (*ui.Bitmap, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > previewPixels {
		return nil, errTooManyPixels
	}
	return ui.DecodeBitmap(data)
}

// previewStep shows the image d after the one shown (d -1 before),
// around the ends.
func (a *App) previewStep(cn *conn, d int) {
	p := cn.fs.preview
	if p == nil || len(p.images) < 2 {
		return
	}
	p.at = (p.at + d + len(p.images)) % len(p.images)
	a.loadPreview(cn)
}

func (a *App) previewView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	p := fs.preview
	if p == nil {
		return
	}
	e := p.images[p.at]
	ww, wh := c.Size()
	w := min(max(ww-160, 320), 1100)
	h := min(max(wh-240, 200), 760)
	many := len(p.images) > 1
	panel := sheet(ui.Modal(c.Key("preview"), &fs.previewOpen, func() {
		ui.Column(c).Gap(12).Width(w).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
				softTile(c, icons.Image, tok.Accent, 26)
				ui.Text(c, shownName(e.Name)).FontSize(theme.Body).FontWeight(600).SingleLine().Shrink(1).MinWidth(0)
				if many {
					ui.Text(c, m.PreviewOf(strconv.Itoa(p.at+1), strconv.Itoa(len(p.images)))).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine().Shrink(0)
				}
				ui.Spacer(c)
				if many {
					if (IconButton{Icon: icons.Previous, Label: m.PreviousImage()}).Clicked(c, "previmg") {
						a.previewStep(cn, -1)
					}
					if (IconButton{Icon: icons.Next, Label: m.NextImage()}).Clicked(c, "nextimg") {
						a.previewStep(cn, 1)
					}
				}
				if (IconButton{Icon: icons.Close, Label: m.ClosePreview()}).Clicked(c, "closeimg") {
					fs.previewOpen = false
				}
			})
			ui.Box(c.Key("imgarea")).Width(w).Height(h).Radius(theme.RadiusControl).Background(tok.Canvas).Border(1, tok.Line).
				Center().Clip().Children(func() {
				switch {
				case p.loading:
					ui.Spinner(c)
				case p.big || p.err != "":
					msg := m.ImageTooLarge(humanSize(e.Size))
					if !p.big {
						msg = m.ImageFailed(securityText(m, p.err))
					}
					ui.Column(c).Gap(8).AlignItems(ui.Center).Padding(theme.Space(4)).Children(func() {
						ui.Icon(c, icons.Warning.SVG).Size(22, 22).TextColor(tok.Warning)
						ui.Text(c, msg).FontSize(theme.Footnote).TextColor(tok.TextMuted)
					})
				case p.bmp != nil:
					ui.Image(c, p.bmp).Fill().Fit(ui.ScaleDown).Label(shownName(e.Name))
				}
			})
			if p.bmp != nil {
				bw, bh := p.bmp.Size()
				ui.Text(c, m.ImageDims(strconv.Itoa(bw), strconv.Itoa(bh), humanSize(e.Size))).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			}
		})
	}), tok)
	if many {
		switch {
		case panel.OverlayShortcut(0, ui.KeyLeft):
			a.previewStep(cn, -1)
		case panel.OverlayShortcut(0, ui.KeyRight):
			a.previewStep(cn, 1)
		}
	}
	if !fs.previewOpen {
		fs.closePreview()
	}
}
