package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/sftpsvc"
)

const (
	extPollInterval = time.Second
	extSaveDebounce = 500 * time.Millisecond
)

type extState int

const (
	extOpening extState = iota
	extWatching
	extChanged
	extUploading
	extConflict
	extFailed
)

// Session state belongs to the UI thread. Workers use snapshots or the
// immutable paths and cancellation context.
type extEdit struct {
	name, remote, local string
	state               extState
	err                 error
	ver                 sftpsvc.Version
	uploads             int
	lastUpload          time.Time
	ask                 bool

	ctx       context.Context
	cancel    context.CancelFunc
	baseline  extSample
	observed  extSample
	noticed   time.Time
	failed    extSample
	epoch     int
	launching bool
}

type extSample struct {
	info os.FileInfo
	sum  [sha256.Size]byte
}

func sameCopyMetadata(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func externalCopyRoot(p string) (*os.Root, error) {
	dir := filepath.Dir(p)
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, sftpsvc.ErrUnsafePath
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if info, err := root.Stat("."); err != nil || !os.SameFile(before, info) {
		root.Close()
		return nil, sftpsvc.ErrUnsafePath
	}
	return root, nil
}

func readExternalCopy(p string, dst io.Writer) (extSample, error) {
	root, err := externalCopyRoot(p)
	if err != nil {
		return extSample{}, err
	}
	defer root.Close()
	return sampleExternalCopy(root, filepath.Base(p), dst)
}

func sampleExternalCopy(root *os.Root, name string, dst io.Writer) (extSample, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return extSample{}, err
	}
	if !before.Mode().IsRegular() {
		return extSample{}, sftpsvc.ErrUnsafePath
	}
	if before.Size() > editorHardReadLimit {
		return extSample{}, sftpsvc.ErrReadLimit
	}
	f, err := root.Open(name)
	if err != nil {
		return extSample{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return extSample{}, err
	}
	if !sameCopyMetadata(before, opened) {
		return extSample{}, errExtLocalChanged
	}
	sum := sha256.New()
	out := io.Writer(sum)
	if dst != nil {
		out = io.MultiWriter(sum, dst)
	}
	n, err := io.Copy(out, io.LimitReader(f, editorHardReadLimit+1))
	if err != nil {
		return extSample{}, err
	}
	if n > editorHardReadLimit {
		return extSample{}, sftpsvc.ErrReadLimit
	}
	after, err := root.Lstat(name)
	if err != nil {
		return extSample{}, err
	}
	if !sameCopyMetadata(before, after) || n != before.Size() {
		return extSample{}, errExtLocalChanged
	}
	return extSample{info: after, sum: [sha256.Size]byte(sum.Sum(nil))}, nil
}

type extPoll struct {
	ed       *extEdit
	epoch    int
	previous extSample
}

func pollExternalCopy(poll extPoll) (extSample, error) {
	info, err := os.Lstat(poll.ed.local)
	if err != nil {
		return extSample{}, err
	}
	if sameCopyMetadata(poll.previous.info, info) {
		return poll.previous, nil
	}
	return readExternalCopy(poll.ed.local, nil)
}

func (a *App) watchExternalEdits(cn *conn) {
	fs := cn.fs
	if fs.extWatchCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan []extPoll, 1)
	fs.extWatchCancel, fs.extWatchScan = cancel, requests
	go func() {
		tick := time.NewTicker(extPollInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				a.post(func() { fs.requestExternalPoll(requests) })
			case polls := <-requests:
				for _, poll := range polls {
					if ctx.Err() != nil {
						return
					}
					sample, err := pollExternalCopy(poll)
					now := time.Now()
					a.post(func() { a.externalPolled(cn, poll, now, sample, err) })
				}
			}
		}
	}()
}

func (fs *filesState) requestExternalPoll(requests chan []extPoll) {
	if fs.extWatchScan != requests {
		return
	}
	var polls []extPoll
	for _, ed := range fs.edits {
		if ed.state != extOpening && ed.state != extUploading {
			polls = append(polls, extPoll{ed, ed.epoch, ed.observed})
		}
	}
	select {
	case requests <- polls:
	default:
	}
}

func (a *App) externalPolled(cn *conn, poll extPoll, now time.Time, sample extSample, err error) {
	ed := poll.ed
	if cn.fs.externalEdit(ed.remote) != ed || ed.epoch != poll.epoch {
		return
	}
	if err != nil {
		ed.noticed = time.Time{}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errExtLocalChanged) && ed.state != extFailed && ed.state != extConflict {
			a.externalSyncFailed(cn, ed, extSample{}, err)
		}
		return
	}
	if ed.observeCopy(now, sample) {
		ed.ask = a.settings.ExternalUpload == settings.ExternalUploadAsk
		if !ed.ask {
			a.extUpload(cn, ed, false, true)
		}
	}
}

// Observe the same save twice before offering or starting an upload.
func (ed *extEdit) observeCopy(now time.Time, sample extSample) bool {
	if !sameCopyMetadata(ed.observed.info, sample.info) || ed.observed.sum != sample.sum || ed.noticed.IsZero() {
		ed.observed, ed.noticed = sample, now
		ed.ask = false
	}
	if ed.state == extConflict {
		return false
	}
	if ed.failed.info != nil {
		if ed.failed.sum == sample.sum {
			return false
		}
		ed.failed = extSample{}
	}
	if sample.sum == ed.baseline.sum {
		ed.baseline.info = sample.info
		ed.state, ed.err, ed.ask = extWatching, nil, false
		return false
	}
	ed.state, ed.err = extChanged, nil
	return now.Sub(ed.noticed) >= extSaveDebounce
}

func (fs *filesState) extUnsynced() []*extEdit {
	var edits []*extEdit
	for _, ed := range fs.edits {
		switch ed.state {
		case extChanged, extUploading, extConflict, extFailed:
			edits = append(edits, ed)
		case extWatching:
			if ed.copyMayBeUnsynced() {
				edits = append(edits, ed)
			}
		}
	}
	return edits
}

func (ed *extEdit) copyMayBeUnsynced() bool {
	if ed.observed.sum != ed.baseline.sum {
		return true
	}
	info, err := os.Lstat(ed.local)
	return err != nil || !sameCopyMetadata(ed.baseline.info, info)
}
