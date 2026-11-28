package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

type extHow struct {
	editor         *settings.ExternalEditor
	system, choose bool
	title          string
}

var (
	errExtLocalChanged = errors.New("external edit: local copy changed during read")
	errExtNoOpenApp    = errors.New("external edit: no app available to open files")
)

func externalErrorText(m locales.Messages, err error) string {
	switch {
	case errors.Is(err, errExtLocalChanged):
		return m.ExternalLocalCopyChanged()
	case errors.Is(err, errExtNoOpenApp):
		return m.ExternalNoOpenApp()
	default:
		return editorErrorText(m, err)
	}
}

// textEditor selects the preference for opening a text file.
func (a *App) textEditor(name string) *extHow {
	switch a.settings.FileEditor {
	case settings.FileEditorSystem:
		return &extHow{system: true}
	case settings.FileEditorExternal:
		if ed, ok := matchEditor(a.settings.ExternalEditors, name); ok {
			return &extHow{editor: &ed}
		}
	}
	return nil
}

func (a *App) editExternally(cn *conn, remote bool, p string, size int64, how extHow) {
	if how.editor == nil && !how.system && !how.choose {
		eds := a.settings.ExternalEditors
		if len(eds) == 0 {
			a.toastIn(cn, func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.ExternalNoEditor(), Type: "error"} })
			return
		}
		ed, ok := matchEditor(eds, sideBase(remote, p))
		if !ok {
			ed = eds[0]
		}
		how.editor = &ed
	}
	a.openExternal(cn, &withAsk{remote: remote, path: p, size: size}, how)
}

func (a *App) openExternal(cn *conn, w *withAsk, how extHow) {
	if w.remote && sftpsvc.Executable(path.Base(w.path)) {
		w.how, w.probe = nil, false
		if how.editor != nil {
			w.how = &how
		}
		a.askOpenWith(cn, w)
		return
	}
	a.openOutside(cn, w, how)
}

func (a *App) externalLauncher(p string, how extHow) (func() error, error) {
	switch {
	case how.editor != nil:
		name, args, err := editorCommand(*how.editor, p, runtime.GOOS)
		start := startEditor
		return func() error { return start(name, args) }, err
	case how.choose && a.p.OpenWith != nil:
		open := a.p.OpenWith
		return func() error { return open(p, how.title) }, nil
	case how.system && a.p.OpenPath != nil:
		open := a.p.OpenPath
		return func() error { return open(p) }, nil
	default:
		return nil, errExtNoOpenApp
	}
}

func (a *App) openOutside(cn *conn, w *withAsk, how extHow) {
	if how.choose && how.title == "" {
		how.title = locales.ChooseAppTitle(shownName(sideBase(w.remote, w.path)))
	}
	if !w.remote {
		a.launchExternal(cn, filepath.Base(w.path), w.path, how)
		return
	}
	if ed := cn.fs.externalEdit(w.path); ed != nil {
		if ed.state != extOpening && !ed.launching {
			launch, err := a.externalLauncher(ed.local, how)
			if err != nil {
				a.openFailed(cn, ed.name, err)
				return
			}
			a.launchExternalCopy(cn, ed, launch, false)
		}
		return
	}
	if w.data != nil {
		a.startExternalEdit(cn, w, how, editorHardReadLimit)
		return
	}
	if !a.confirmOpen(cn, w, func() { a.startExternalEdit(cn, w, how, editorHardReadLimit) }) {
		limit := min(int64(a.settings.OpenConfirmMB)<<20, editorHardReadLimit)
		a.startExternalEdit(cn, w, how, limit)
	}
}

func (a *App) launchExternal(cn *conn, name, p string, how extHow) {
	launch, err := a.externalLauncher(p, how)
	if err != nil {
		a.openFailed(cn, name, err)
		return
	}
	go func() {
		if err := launch(); err != nil && !errors.Is(err, context.Canceled) {
			a.post(func() { a.openFailed(cn, name, err) })
		}
	}()
}

func (a *App) startExternalEdit(cn *conn, w *withAsk, how extHow, limit int64) {
	fs := cn.fs
	if fs.externalEdit(w.path) != nil {
		return
	}
	name := path.Base(w.path)
	if err := sftpsvc.SafeName(name); err != nil {
		a.openFailed(cn, name, err)
		return
	}
	local, err := a.copyPath(&fs.copies, name)
	if err != nil {
		a.openFailed(cn, name, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ed := &extEdit{name: name, remote: w.path, local: local, ctx: ctx, cancel: cancel}
	fs.edits = append(fs.edits, ed)
	launch, launchErr := a.externalLauncher(local, how)
	if launchErr != nil && !w.probe {
		a.extStop(cn, ed)
		a.openFailed(cn, name, launchErr)
		return
	}
	get := fs.transfers.Client
	go func() {
		data, ver := w.data, w.ver
		var err error
		if data == nil {
			data, ver, err = readExternalRemote(ctx, get, ed.remote, limit)
		}
		var sample extSample
		binary := err == nil && w.probe && !sftpsvc.IsText(data)
		if err == nil && !binary {
			if err = launchErr; err == nil {
				sample, err = seedExternalCopy(ctx, local, data)
			}
			ver.Sum = sample.sum
			data = nil
		}
		a.post(func() {
			if a.externalOpened(cn, ed, data, ver, sample, err) {
				a.launchExternalCopy(cn, ed, launch, true)
			}
		})
	}()
}

func readExternalRemote(ctx context.Context, get func(context.Context) (*sftpsvc.Client, error), p string, limit int64) ([]byte, sftpsvc.Version, error) {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	c, err := get(ctx)
	if err != nil {
		return nil, sftpsvc.Version{}, err
	}
	return c.ReadFileContext(ctx, p, limit)
}

func seedExternalCopy(ctx context.Context, p string, data []byte) (extSample, error) {
	if err := ctx.Err(); err != nil {
		return extSample{}, err
	}
	if err := writeLocalCopy(p, data, 0o600); err != nil {
		return extSample{}, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		return extSample{}, err
	}
	info, err := os.Lstat(p)
	if err != nil {
		return extSample{}, err
	}
	return extSample{info: info, sum: sha256.Sum256(data)}, nil
}

// A probe that found a binary keeps the exact bytes for the open-with sheet.
func (a *App) externalOpened(cn *conn, ed *extEdit, data []byte, ver sftpsvc.Version, sample extSample, err error) bool {
	if cn.fs.externalEdit(ed.remote) != ed {
		return false
	}
	switch {
	case err != nil:
		a.extStop(cn, ed)
		a.openFailed(cn, ed.name, err)
	case data != nil:
		a.extStop(cn, ed)
		a.askOpenWith(cn, &withAsk{remote: true, path: ed.remote, size: ver.Size, data: data, ver: ver})
	default:
		ed.state, ed.ver, ed.baseline, ed.observed = extWatching, ver, sample, sample
		a.watchExternalEdits(cn)
		return true
	}
	return false
}

func (a *App) launchExternalCopy(cn *conn, ed *extEdit, launch func() error, first bool) {
	ed.launching = true
	go func() {
		if ed.ctx.Err() != nil {
			return
		}
		err := launch()
		a.post(func() {
			if cn.fs.externalEdit(ed.remote) != ed {
				return
			}
			ed.launching = false
			if err != nil {
				if first && ed.state == extWatching && !ed.copyMayBeUnsynced() {
					a.extStop(cn, ed)
				}
				if !errors.Is(err, context.Canceled) {
					a.openFailed(cn, ed.name, err)
				}
			} else if first {
				a.toastIn(cn, func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.OpenedLocally(shownName(ed.name))} })
			}
		})
	}()
}

func (a *App) extReveal(cn *conn, ed *extEdit) {
	a.launchExternal(cn, ed.name, filepath.Dir(ed.local), extHow{system: true})
}

func (a *App) extStop(cn *conn, ed *extEdit) {
	fs := cn.fs
	i := slices.Index(fs.edits, ed)
	if i == -1 {
		return
	}
	ed.cancel()
	fs.edits = slices.Delete(fs.edits, i, i+1)
	if len(fs.edits) == 0 {
		fs.stopExternalEdits()
	}
	if err := os.RemoveAll(filepath.Dir(ed.local)); err != nil {
		a.openFailed(cn, ed.name, err)
	}
}

func (fs *filesState) stopExternalEdits() {
	if len(fs.edits) == 0 && fs.extWatchCancel == nil {
		return
	}
	for _, ed := range fs.edits {
		ed.cancel()
	}
	fs.edits = nil
	if fs.extWatchCancel != nil {
		fs.extWatchCancel()
		fs.extWatchCancel, fs.extWatchScan = nil, nil
	}
}

func (fs *filesState) externalEdit(remote string) *extEdit {
	for _, ed := range fs.edits {
		if ed.remote == remote {
			return ed
		}
	}
	return nil
}
