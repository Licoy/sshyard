package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func (a *App) extUploadNow(cn *conn, ed *extEdit) {
	a.extUpload(cn, ed, false, false)
}

func (a *App) extOverwrite(cn *conn, ed *extEdit) {
	a.extUpload(cn, ed, true, false)
}

type extUploadWork struct {
	was      sftpsvc.Version
	expected [sha256.Size]byte
	settled  bool
}

type extUploadResult struct {
	sample    extSample
	ver       sftpsvc.Version
	err       error
	unsettled bool
	uploaded  bool
}

func (a *App) extUpload(cn *conn, ed *extEdit, force, automatic bool) {
	fs := cn.fs
	if fs.externalEdit(ed.remote) != ed || ed.state == extOpening || ed.state == extUploading || force != (ed.state == extConflict) {
		return
	}
	work := extUploadWork{was: ed.ver, expected: ed.observed.sum, settled: automatic}
	if force {
		work.was = sftpsvc.Version{}
	}
	ed.state, ed.err, ed.ask = extUploading, nil, false
	ed.epoch++
	get := fs.transfers.Client
	go func() {
		result := runExternalUpload(ed, get, work)
		if automatic && (errors.Is(result.err, errExtLocalChanged) || errors.Is(result.err, os.ErrNotExist) && result.sample.info == nil) {
			result.err, result.unsettled = nil, true
		}
		a.post(func() { a.externalUploadDone(cn, ed, result) })
	}()
}

func runExternalUpload(ed *extEdit, get func(context.Context) (*sftpsvc.Client, error), work extUploadWork) (result extUploadResult) {
	var data bytes.Buffer
	result.sample, result.err = readExternalCopy(ed.local, &data)
	if result.err != nil {
		return result
	}
	if work.settled && result.sample.sum != work.expected {
		result.unsettled = true
		return result
	}
	if !work.was.ModTime.IsZero() && result.sample.sum == work.was.Sum {
		return result
	}
	ctx, cancel := context.WithTimeout(ed.ctx, openTimeout)
	defer cancel()
	c, err := get(ctx)
	if err != nil {
		result.err = err
		return result
	}
	result.ver, result.err = c.WriteFileContext(ctx, ed.remote, data.Bytes(), work.was)
	if result.err == nil {
		result.ver.Sum, result.uploaded = result.sample.sum, true
	}
	return result
}

func (a *App) externalUploadDone(cn *conn, ed *extEdit, result extUploadResult) {
	if cn.fs.externalEdit(ed.remote) != ed {
		return
	}
	switch {
	case result.unsettled:
		ed.state, ed.observed, ed.noticed = extWatching, result.sample, time.Time{}
	case result.err != nil:
		a.externalSyncFailed(cn, ed, result.sample, result.err)
	default:
		ed.acceptCopy(result.sample)
		if result.uploaded {
			ed.ver, ed.uploads, ed.lastUpload = result.ver, ed.uploads+1, time.Now()
			if a.settings.ExternalNotify {
				a.toastIn(cn, func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.ExternalUploaded(shownName(ed.name))} })
			}
		}
	}
}

func (a *App) externalSyncFailed(cn *conn, ed *extEdit, sample extSample, err error) {
	ed.err, ed.failed, ed.ask = err, sample, false
	if errors.Is(err, sftpsvc.ErrChanged) {
		ed.state = extConflict
		a.toastIn(cn, func(m locales.Messages) ui.Toast {
			return ui.Toast{Title: m.ExternalConflict(shownName(ed.name)), Type: "error"}
		})
		return
	}
	ed.state = extFailed
	a.toastIn(cn, func(m locales.Messages) ui.Toast {
		return ui.Toast{Title: m.ExternalUploadFailed(shownName(ed.name), externalErrorText(m, err)), Type: "error"}
	})
}

func (ed *extEdit) acceptCopy(sample extSample) {
	ed.baseline, ed.observed, ed.failed = sample, sample, extSample{}
	ed.state, ed.err, ed.ask, ed.noticed = extWatching, nil, false, time.Time{}
	ed.epoch++
}

func (a *App) extSkip(cn *conn, ed *extEdit) {
	if cn.fs.externalEdit(ed.remote) != ed || ed.state != extChanged {
		return
	}
	sample, err := pollExternalCopy(extPoll{ed: ed, previous: ed.observed})
	if err != nil {
		a.externalSyncFailed(cn, ed, extSample{}, err)
		return
	}
	ed.acceptCopy(sample)
}

func (a *App) extTakeServer(cn *conn, ed *extEdit) {
	fs := cn.fs
	if fs.externalEdit(ed.remote) != ed || ed.state != extConflict {
		return
	}
	ed.state, ed.err, ed.ask = extUploading, nil, false
	ed.epoch++
	get := fs.transfers.Client
	go func() {
		sample, ver, err := takeExternalServer(ed, get)
		a.post(func() {
			if fs.externalEdit(ed.remote) != ed {
				return
			}
			if err != nil {
				ed.state, ed.err = extConflict, err
				a.toastIn(cn, func(m locales.Messages) ui.Toast {
					return ui.Toast{Title: m.ExternalTakeServerFailed(shownName(ed.name), externalErrorText(m, err)), Type: "error"}
				})
				return
			}
			ed.ver = ver
			ed.acceptCopy(sample)
		})
	}()
}

func takeExternalServer(ed *extEdit, get func(context.Context) (*sftpsvc.Client, error)) (extSample, sftpsvc.Version, error) {
	root, err := externalCopyRoot(ed.local)
	if err != nil {
		return extSample{}, sftpsvc.Version{}, err
	}
	defer root.Close()
	name := filepath.Base(ed.local)
	before, err := sampleExternalCopy(root, name, nil)
	if err != nil {
		return extSample{}, sftpsvc.Version{}, err
	}
	data, ver, err := readExternalRemote(ed.ctx, get, ed.remote, editorHardReadLimit)
	if err != nil {
		return extSample{}, sftpsvc.Version{}, err
	}
	ver.Sum = sha256.Sum256(data)
	info, err := replaceExternalCopy(ed.ctx, root, name, data, before)
	return extSample{info: info, sum: ver.Sum}, ver, err
}

func replaceExternalCopy(ctx context.Context, root *os.Root, name string, data []byte, before extSample) (os.FileInfo, error) {
	tmp := ".edit-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer root.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	_, writeErr := f.Write(data)
	info, statErr := f.Stat()
	if err := errors.Join(writeErr, statErr, f.Close()); err != nil {
		return nil, err
	}
	current, err := sampleExternalCopy(root, name, nil)
	if err != nil {
		return nil, err
	}
	if !sameCopyMetadata(before.info, current.info) || before.sum != current.sum {
		return nil, errExtLocalChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := root.Rename(tmp, name); err != nil {
		return nil, err
	}
	return info, nil
}
