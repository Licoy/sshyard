package sftpsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Direction is which way a transfer goes.
type Direction int

const (
	Upload Direction = iota
	Download
	// Relay copies a file from another server (Transfer.From) to this
	// queue's: what one SFTP channel reads goes straight to the other,
	// through this process's memory only, never through a local file, and
	// neither server is given the other's address or any credential.
	Relay
)

// remoteDst says a transfer of direction d writes on the server: all but
// a download.
func remoteDst(d Direction) bool { return d != Download }

// TransferState is where a transfer is.
type TransferState int

const (
	// Queued waits for a free slot (or for its retry).
	Queued TransferState = iota
	Running
	// Paused keeps what was copied; Resume goes on from there.
	Paused
	Done
	Failed
	// Canceled removed what was copied.
	Canceled
)

// MaxRetries is how many times a failed transfer is tried again by itself.
const MaxRetries = 3

// DefaultConcurrency is how many transfers run at once unless set.
const DefaultConcurrency = 3

var (
	errPaused   = errors.New("sftp: transfer paused")
	errCanceled = errors.New("sftp: transfer canceled")
	// ErrSizeMismatch is a copy whose size differs from its source's.
	ErrSizeMismatch = errors.New("sftp: the copy's size differs from the source's")
)

// Transfer is one file being copied, from Src to Dst (both full paths).
type Transfer struct {
	ID    int
	Name  string // what the list shows, as a path under the folder dropped
	Dir   Direction
	Src   string
	Dst   string
	Bytes int64
	Total int64
	State TransferState
	Err   error
	// Destination is the path written, once Done.
	Destination string
	// Attempt is how many times it failed and was tried again by itself.
	Attempt int
	// Offset is where the last start resumed from, 0 from the beginning.
	Offset int64
	// Speed is the recent rate in bytes per second while it runs.
	Speed float64
	// From names the server a Relay copies from, as the list shows it.
	From string

	// from is a Relay's source server, as Transfers.Client is the
	// destination's.
	from   func(ctx context.Context) (*Client, error)
	cancel context.CancelCauseFunc
	// stopping is set while a transfer asked to pause or cancel is still
	// running: State already says what was last asked (a resume or retry
	// meanwhile sets it back to Running) and the run settles to that
	// when it ends, whatever stopped it
	stopping bool
	// removing is set while a canceled transfer's part file is being
	// removed: a retry, or another transfer to the same file, waits for
	// it, so as not to lose its new part
	removing  bool
	part      string
	partOwned bool
	partReady bool
	partInfo  fs.FileInfo
	srcSize   int64
	srcMod    time.Time
	mark      time.Time
	markB     int64
}

// Percent is how far the transfer is, 0 to 100.
func (t Transfer) Percent() int {
	if t.Total <= 0 {
		if t.State == Done {
			return 100
		}
		return 0
	}
	return int(t.Bytes * 100 / t.Total)
}

// ETA is the time left at the current speed, 0 when unknown.
func (t Transfer) ETA() time.Duration {
	if t.State != Running || t.Speed <= 0 || t.Total <= t.Bytes {
		return 0
	}
	return time.Duration(float64(t.Total-t.Bytes) / t.Speed * float64(time.Second))
}

// Active is a transfer that has not ended.
func (t Transfer) Active() bool {
	return t.State == Queued || t.State == Running || t.State == Paused
}

// Transfers is a queue of file transfers: a few run at once
// (Concurrency), the others wait; each can be paused, resumed, retried
// and canceled. A transfer writes a part file next to its destination
// (".name.sshyard-part") and renames it when complete, so that a paused
// or failed one resumes from the part's size, and a canceled one leaves
// nothing behind.
type Transfers struct {
	// OnChange is called from any goroutine when a transfer changes.
	OnChange func()
	// Client returns the SFTP client to use, waiting until there is one
	// (the connection may be down), or ctx's error.
	Client func(ctx context.Context) (*Client, error)
	// Logf logs resumes and retries; log.Printf when nil.
	Logf func(format string, args ...any)
	// Backoff is the wait before retry n (1-based); 1s, 2s, 4s when nil.
	Backoff func(n int) time.Duration

	// onWrite, for tests, is called after each write of a transfer.
	onWrite func(t *Transfer, done int64)

	mu    sync.Mutex
	list  []*Transfer
	next  int
	limit int
}

func (ts *Transfers) changed() {
	if ts.OnChange != nil {
		ts.OnChange()
	}
}

func (ts *Transfers) logf(format string, args ...any) {
	if ts.Logf != nil {
		ts.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (ts *Transfers) backoff(n int) time.Duration {
	if ts.Backoff != nil {
		return ts.Backoff(n)
	}
	return time.Second << (n - 1)
}

// Concurrency is how many transfers run at once.
func (ts *Transfers) Concurrency() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.concurrency()
}

func (ts *Transfers) concurrency() int {
	if ts.limit <= 0 {
		return DefaultConcurrency
	}
	return ts.limit
}

// SetConcurrency sets how many transfers run at once (1 or more). Those
// running go on; more start when there is room.
func (ts *Transfers) SetConcurrency(n int) {
	ts.mu.Lock()
	ts.limit = max(n, 1)
	ts.pump()
	ts.mu.Unlock()
	ts.changed()
}

// List returns copies of the transfers, in the order they were added.
func (ts *Transfers) List() []Transfer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]Transfer, len(ts.list))
	for i, t := range ts.list {
		out[i] = *t
	}
	return out
}

// Add queues copying src to dst (full paths: local to remote for an
// Upload, remote to local for a Download). name is what the list shows.
func (ts *Transfers) Add(dir Direction, src, dst, name string) int {
	return ts.add(&Transfer{Name: name, Dir: dir, Src: src, Dst: dst})
}

// AddRelay queues a Relay: copying the file src of the server from
// returns (waiting for its connection, as Client does) to dst on this
// queue's server. fromName is what the list says it comes from.
func (ts *Transfers) AddRelay(from func(ctx context.Context) (*Client, error), fromName, src, dst, name string) int {
	return ts.add(&Transfer{Name: name, Dir: Relay, Src: src, Dst: dst, From: fromName, from: from})
}

func (ts *Transfers) add(t *Transfer) int {
	ts.mu.Lock()
	ts.next++
	t.ID = ts.next
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(err)
	}
	t.part = PartName(t.Dst, remoteDst(t.Dir)) + "-" + hex.EncodeToString(token[:])
	ts.list = append(ts.list, t)
	ts.pump()
	id := t.ID
	ts.mu.Unlock()
	ts.changed()
	return id
}

// pump starts queued transfers while there is room. Under ts.mu.
func (ts *Transfers) pump() {
	running := 0
	for _, t := range ts.list {
		if t.State == Running || t.stopping {
			running++
		}
	}
	for _, t := range ts.list {
		if running >= ts.concurrency() {
			return
		}
		if t.State == Queued && !t.removing && !ts.partBusy(t) {
			ts.run(t)
			running++
		}
	}
}

// partBusy says whether another transfer's part file at t's is still
// being removed: t would write it meanwhile. Under ts.mu.
func (ts *Transfers) partBusy(t *Transfer) bool {
	return slices.ContainsFunc(ts.list, func(u *Transfer) bool {
		return u != t && (u.removing || u.State == Running || u.stopping) && remoteDst(u.Dir) == remoteDst(t.Dir) && u.Dst == t.Dst
	})
}

func (ts *Transfers) find(id int) *Transfer {
	for _, t := range ts.list {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// run starts t. Under ts.mu.
func (ts *Transfers) run(t *Transfer) {
	ctx, cancel := context.WithCancelCause(context.Background())
	t.State, t.Err, t.cancel, t.Speed = Running, nil, cancel, 0
	go func() {
		err := ts.attempts(ctx, t)
		ts.mu.Lock()
		// Settle on what was last asked, not on why ctx ended: a pause
		// followed by a resume runs on, by a cancel cancels.
		canceled := false
		switch {
		case err == nil:
			t.State, t.Destination, t.Bytes = Done, t.Dst, t.Total
		case t.stopping && t.State == Paused:
		case t.stopping && t.State == Canceled:
			t.Bytes, t.removing, canceled = 0, true, true
		case t.stopping:
			t.State = Queued // resumed or retried while stopping
		default:
			t.State, t.Err = Failed, err
		}
		t.Speed, t.stopping = 0, false
		cancel(nil)
		ts.pump()
		ts.mu.Unlock()
		if canceled {
			ts.dropPart(t)
		}
		ts.changed()
	}()
}

// attempts copies t, trying again after a failure up to MaxRetries times.
// Waiting for the connection is no attempt.
func (ts *Transfers) attempts(ctx context.Context, t *Transfer) error {
	for {
		err := ts.copy(ctx, t)
		if err == nil {
			return nil // done, even if stopped as it finished
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ts.mu.Lock()
		if t.Attempt >= MaxRetries || permanent(err) {
			ts.mu.Unlock()
			return err
		}
		t.Attempt++
		n := t.Attempt
		t.Err, t.Speed = err, 0
		ts.mu.Unlock()
		ts.changed()
		d := ts.backoff(n)
		ts.logf("sftp: %s failed (%v); retry %d/%d in %v", t.Name, err, n, MaxRetries, d)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}

// permanent reports a failure that trying again cannot change: a missing
// file, a refusal, or a path or folder the safety checks reject.
func permanent(err error) bool {
	for _, e := range []error{fs.ErrNotExist, fs.ErrPermission, ErrUnsafeName, ErrUnsafeRemoteName, ErrDstIsDir, ErrUnsafePath, ErrUnsafeDestination, ErrPartChanged} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// ErrDstIsDir is a transfer to where a folder is.
var ErrDstIsDir = errors.New("a folder is already there")

// PartName is the prefix for a transfer's random, exclusively created partial file.
func PartName(dst string, remote bool) string {
	if remote {
		return path.Join(path.Dir(dst), "."+path.Base(dst)+".sshyard-part")
	}
	return filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".sshyard-part")
}

func (ts *Transfers) client(ctx context.Context) (*Client, error) {
	if ts.Client == nil {
		return nil, errors.New("sftp: no connection")
	}
	return ts.Client(ctx)
}

// channel opens an attempt's own SFTP channel on the connection: ending
// ctx (a pause or a cancel) or closing the shared client closes it, so
// that a request a stalled server never answers returns at once, while
// the shared client, the terminals and the other transfers go on. When
// the server opens no more channels, the attempt uses the shared client.
func (ts *Transfers) channel(ctx context.Context, t *Transfer) (*Client, func(), error) {
	return ts.channelOn(ctx, ts.client, t)
}

// source is channel on the server a Relay copies from.
func (ts *Transfers) source(ctx context.Context, t *Transfer) (*Client, func(), error) {
	if t.from == nil {
		return nil, nil, errors.New("sftp: no connection")
	}
	return ts.channelOn(ctx, t.from, t)
}

// channelOn is channel on the connection of the client get returns.
func (ts *Transfers) channelOn(ctx context.Context, get func(context.Context) (*Client, error), t *Transfer) (*Client, func(), error) {
	shared, err := get(ctx)
	if err != nil {
		return nil, nil, err
	}
	c, err := shared.operationClient(ctx)
	var refused *ssh.OpenChannelError
	if err != nil && ctx.Err() == nil && (errors.As(err, &refused) || errors.Is(err, ErrOperationsBusy)) {
		ts.logf("sftp: %s: no channel of its own (%v); using the shared one", t.Name, err)
		return shared, func() {}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	stopShared := func() bool { return false }
	if shared.closed != nil {
		stopShared = context.AfterFunc(shared.closed, func() { c.Close() })
	}
	return c, func() {
		stop()
		stopShared()
		c.Close()
	}, nil
}

// copy makes one attempt at t, resuming from its part file.
func (ts *Transfers) copy(ctx context.Context, t *Transfer) error {
	c, done, err := ts.channel(ctx, t)
	if err != nil {
		return err
	}
	defer done()
	remote := remoteFS{c, ctx}
	var src, dst fileSide
	switch t.Dir {
	case Upload:
		if err := remote.checkParents(path.Dir(t.Dst)); err != nil {
			return err
		}
		src, dst = localFS{}, remote
	case Relay:
		// Plans check the names the source server sent; one queued to an
		// unsafe name anyway must not reach a part file in another folder.
		if err := RemoteName(path.Base(t.Dst)); err != nil {
			return err
		}
		if err := remote.checkParents(path.Dir(t.Dst)); err != nil {
			return err
		}
		// The source's own channel, which ending ctx closes as it does
		// the destination's. What it reads is written to the other as it
		// comes (io.Copy below): the bytes held at once are bounded by the
		// SFTP reads in flight, never the file's size.
		from, fromDone, err := ts.source(ctx, t)
		if err != nil {
			return err
		}
		defer fromDone()
		src, dst = remoteFS{from, ctx}, remote
	default:
		local, err := destinationRoot(t.Dst)
		if err != nil {
			return err
		}
		defer local.root.Close()
		src, dst = remote, local
		// Plans check the names; a download whose own name is unsafe
		// (a server's "../" makes it "..") must not reach a part file
		// in some other folder either.
		if err := SafeName(filepath.Base(t.Dst)); err != nil {
			return fmt.Errorf("sftp: %w", err)
		}
	}
	// A folder where the file goes (a server's "../" makes the file the
	// folder chosen) fails at once: its part file would sit beside it.
	if di, err := dst.lstat(t.Dst); err == nil && di.IsDir() {
		return fmt.Errorf("sftp: %s: %w", t.Dst, ErrDstIsDir)
	}
	fi, err := src.stat(t.Src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("sftp: %s is a folder", t.Src)
	}
	size, mod := fi.Size(), fi.ModTime()
	part := t.part
	var off int64
	ts.mu.Lock()
	same := t.srcSize == size && t.srcMod.Equal(mod)
	t.srcSize, t.srcMod, t.Total = size, mod, size
	ts.mu.Unlock()
	partExists := false
	if pi, err := dst.lstat(part); err == nil && t.partOwned && same && pi.Mode().IsRegular() && pi.Size() <= size {
		partExists = true
		off = pi.Size()
	}
	if t.partReady && partExists && off == size && same {
		// A completed part needs only its failed rename retried. Reopening a
		// read-only source mode for writing would unnecessarily prevent retry.
		if local, ok := dst.(localDestination); ok {
			f, err := local.root.Open(local.relative(part))
			if err != nil {
				return err
			}
			info, err := f.Stat()
			f.Close()
			if err != nil {
				return err
			}
			if t.partInfo == nil || !os.SameFile(t.partInfo, info) {
				return ErrPartChanged
			}
		} else if err := remote.resolve(part); err != nil {
			return err
		}
		return dst.rename(part, t.Dst)
	}
	if off == 0 {
		ts.mu.Lock()
		t.partReady = false
		ts.mu.Unlock()
		if err := dst.mkdirAll(dirOf(t.Dst, remoteDst(t.Dir))); err != nil {
			return err
		}
	}
	r, err := src.open(t.Src)
	if err != nil {
		return err
	}
	defer r.Close()
	if t.partOwned && off == 0 {
		if err := dst.remove(part); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		ts.mu.Lock()
		t.partOwned = false
		ts.mu.Unlock()
	}
	w, err := dst.create(part, off > 0)
	if err != nil {
		return err
	}
	if f, ok := w.(*os.File); ok {
		info, statErr := f.Stat()
		if statErr != nil {
			w.Close()
			return statErr
		}
		if off > 0 && (t.partInfo == nil || !os.SameFile(t.partInfo, info)) {
			w.Close()
			return ErrPartChanged
		}
		ts.mu.Lock()
		t.partInfo = info
		ts.mu.Unlock()
	}
	ts.mu.Lock()
	t.partOwned = true
	ts.mu.Unlock()
	if off > 0 {
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			w.Close()
			return err
		}
		if _, err := w.Seek(off, io.SeekStart); err != nil {
			w.Close()
			return err
		}
		ts.logf("sftp: resuming %s at offset %d of %d", t.Name, off, size)
	}
	now := time.Now()
	ts.mu.Lock()
	t.Offset, t.Bytes, t.mark, t.markB = off, off, now, off
	ts.mu.Unlock()
	ts.changed()
	pw := &progressWriter{ctx: ctx, w: w, done: off, total: size, fn: func(done, total int64) { ts.progress(t, done) }}
	if ts.onWrite != nil {
		pw.step = func(done int64) { ts.onWrite(t, done) }
	}
	_, err = io.Copy(pw, r)
	ts.mu.Lock()
	t.Bytes = pw.done
	ts.mu.Unlock()
	if err == nil {
		if f, ok := w.(*os.File); ok {
			err = f.Chmod(LocalMode(fi.Mode().Perm()))
			if err == nil {
				err = setFileTimes(f, mod)
			}
		} else if f, ok := w.(interface{ Chmod(fs.FileMode) error }); ok {
			// A server that refuses it leaves the part's 0600: safe, so
			// the upload still completes.
			if cerr := f.Chmod(fi.Mode().Perm()); cerr != nil {
				ts.logf("sftp: %s: keeping mode 0600: %v", t.Name, cerr)
			}
		}
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if r, ok := dst.(remoteFS); ok {
		// *sftp.File cannot set times, so the closed part is set by path.
		if cerr := r.chtimes(part, mod); cerr != nil {
			ts.logf("sftp: %s: keeping upload time: %v", t.Name, cerr)
		}
	}
	pi, err := dst.lstat(part)
	if err != nil {
		return err
	}
	if pi.Size() != size {
		dst.remove(part)
		ts.mu.Lock()
		t.srcSize = -1 // start over
		ts.mu.Unlock()
		return ErrSizeMismatch
	}
	ts.mu.Lock()
	t.partReady = true
	ts.mu.Unlock()
	if err := dst.rename(part, t.Dst); err != nil {
		return err
	}
	return nil
}

func dirOf(p string, remote bool) string {
	if remote {
		return path.Dir(p)
	}
	return filepath.Dir(p)
}

func (ts *Transfers) progress(t *Transfer, done int64) {
	ts.mu.Lock()
	defer ts.changed()
	defer ts.mu.Unlock()
	if t.State == Canceled {
		return // stopping
	}
	t.Bytes = done
	if t.State != Running {
		return
	}
	if dt := time.Since(t.mark); dt >= 400*time.Millisecond {
		rate := float64(done-t.markB) / dt.Seconds()
		if t.Speed == 0 {
			t.Speed = rate
		} else {
			t.Speed = 0.5*t.Speed + 0.5*rate
		}
		t.mark, t.markB = time.Now(), done
	}
}

// dropPart removes t's part file (t.removing set under ts.mu), then lets
// t start again if it was retried meanwhile.
func (ts *Transfers) dropPart(t *Transfer) {
	ts.removePart(t)
	ts.mu.Lock()
	t.removing = false
	ts.pump()
	ts.mu.Unlock()
	ts.changed()
}

// partWait is how long removePart waits for the connection before it
// logs that it still waits.
var partWait = time.Minute

// removePart deletes a canceled transfer's part file; an upload's waits
// for the connection for as long as it may come back (Client waits), so
// that a long outage does not leave the part on the server.
func (ts *Transfers) removePart(t *Transfer) {
	part := t.part
	if t.Dir == Download {
		local, err := destinationRoot(t.Dst)
		if err == nil {
			defer local.root.Close()
			local.remove(part)
		}
		return
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), partWait)
		c, err := ts.client(ctx)
		cancel()
		if err == nil {
			c.c.Remove(part)
			return
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			ts.logf("sftp: could not remove %s: %v", part, err)
			return
		}
		ts.logf("sftp: still waiting for the connection to remove %s", part)
	}
}

// Pause stops a queued or running transfer, keeping what it copied.
func (ts *Transfers) Pause(id int) {
	ts.mu.Lock()
	if t := ts.find(id); t != nil {
		ts.pause(t)
	}
	ts.mu.Unlock()
	ts.changed()
}

func (ts *Transfers) pause(t *Transfer) {
	switch t.State {
	case Running:
		ts.stop(t, errPaused)
		t.State, t.Speed = Paused, 0
	case Queued:
		t.State = Paused
	}
}

// stop ends t's run, if not already ending. Under ts.mu.
func (ts *Transfers) stop(t *Transfer, cause error) {
	if !t.stopping {
		t.stopping = true
		t.cancel(cause)
	}
}

// resume queues t again if paused; one still stopping runs on.
func (ts *Transfers) resume(t *Transfer) {
	if t.State != Paused {
		return
	}
	if t.stopping {
		t.State = Running
	} else {
		t.State = Queued
	}
}

// PauseAll pauses every queued and running transfer.
func (ts *Transfers) PauseAll() {
	ts.mu.Lock()
	for _, t := range ts.list {
		ts.pause(t)
	}
	ts.mu.Unlock()
	ts.changed()
}

// Resume queues a paused transfer again; it goes on from where it was.
func (ts *Transfers) Resume(id int) {
	ts.mu.Lock()
	if t := ts.find(id); t != nil {
		ts.resume(t)
		ts.pump()
	}
	ts.mu.Unlock()
	ts.changed()
}

// ResumeAll resumes every paused transfer.
func (ts *Transfers) ResumeAll() {
	ts.mu.Lock()
	for _, t := range ts.list {
		ts.resume(t)
	}
	ts.pump()
	ts.mu.Unlock()
	ts.changed()
}

// Retry queues a failed or canceled transfer again, with its retries
// counted afresh; a failed one resumes from its part file.
func (ts *Transfers) Retry(id int) {
	ts.mu.Lock()
	if t := ts.find(id); t != nil {
		switch {
		case t.State == Canceled && t.stopping:
			// canceled and retried before it stopped: it starts again
			// once stopped, its part kept
			t.State, t.Err, t.Attempt = Running, nil, 0
		case t.State == Failed || t.State == Canceled:
			// a canceled one waits for its part file to go (removing)
			t.State, t.Err, t.Attempt = Queued, nil, 0
		}
		ts.pump()
	}
	ts.mu.Unlock()
	ts.changed()
}

// Cancel stops a transfer that has not ended and removes its part file.
func (ts *Transfers) Cancel(id int) {
	ts.mu.Lock()
	t := ts.find(id)
	idle := t != nil && (t.State == Queued || t.State == Paused) && !t.stopping
	if t != nil {
		ts.cancel(t)
	}
	ts.mu.Unlock()
	if idle {
		go ts.dropPart(t)
	}
	ts.changed()
}

func (ts *Transfers) cancel(t *Transfer) {
	switch {
	case t.State == Running || t.stopping && t.State == Paused:
		// the run removes the part once it has stopped
		ts.stop(t, errCanceled)
		t.State, t.Bytes, t.Speed = Canceled, 0, 0
	case t.State == Queued || t.State == Paused:
		t.State, t.Bytes, t.removing = Canceled, 0, true
	}
}

// CancelAll cancels every transfer that has not ended.
func (ts *Transfers) CancelAll() {
	ts.mu.Lock()
	var idle []*Transfer
	for _, t := range ts.list {
		if (t.State == Queued || t.State == Paused) && !t.stopping {
			idle = append(idle, t)
		}
		ts.cancel(t)
	}
	ts.mu.Unlock()
	for _, t := range idle {
		go ts.dropPart(t)
	}
	ts.changed()
}

// ClearFinished drops the transfers that ended (a canceled one whose
// part file is still being removed stays until it is).
func (ts *Transfers) ClearFinished() {
	ts.mu.Lock()
	ts.list = slices.DeleteFunc(ts.list, func(t *Transfer) bool { return !t.Active() && !t.removing })
	ts.mu.Unlock()
	ts.changed()
}

// Wait blocks until no transfer is queued or running and no canceled
// one's part file is still being removed, for tests.
func (ts *Transfers) Wait() {
	for {
		ts.mu.Lock()
		busy := slices.ContainsFunc(ts.list, func(t *Transfer) bool { return t.State == Queued || t.State == Running || t.stopping || t.removing })
		ts.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fileSide is one end of a transfer: the local disk or the remote one.
type fileSide interface {
	stat(p string) (fs.FileInfo, error)
	lstat(p string) (fs.FileInfo, error)
	open(p string) (io.ReadSeekCloser, error)
	create(p string, keep bool) (writeSeekCloser, error)
	mkdirAll(p string) error
	remove(p string) error
	rename(from, to string) error
}

type writeSeekCloser interface {
	io.Writer
	io.Seeker
	io.Closer
}

var _ writeSeekCloser = (*os.File)(nil)

type localFS struct{}

func (localFS) lstat(p string) (fs.FileInfo, error)      { return os.Lstat(p) }
func (localFS) stat(p string) (fs.FileInfo, error)       { return os.Stat(p) }
func (localFS) open(p string) (io.ReadSeekCloser, error) { return os.Open(p) }
func (localFS) create(p string, keep bool) (writeSeekCloser, error) {
	flag := os.O_WRONLY
	if !keep {
		flag |= os.O_CREATE | os.O_EXCL
	}
	return os.OpenFile(p, flag, 0o600)
}
func (localFS) mkdirAll(p string) error      { return os.MkdirAll(p, 0o755) }
func (localFS) remove(p string) error        { return os.Remove(p) }
func (localFS) rename(from, to string) error { return os.Rename(from, to) }

// remoteFS is a transfer's remote side, on the channel Transfers.channel
// gave it; its path checks use that channel too, which ctx ending closes.
type remoteFS struct {
	c   *Client
	ctx context.Context
}

func (r remoteFS) resolve(p string) error {
	_, err := r.c.resolvePath(r.ctx, p)
	return err
}

func (r remoteFS) checkParents(p string) error { return checkParents(p, r.resolve) }

func (r remoteFS) lstat(p string) (fs.FileInfo, error)      { return r.c.c.Lstat(p) }
func (r remoteFS) stat(p string) (fs.FileInfo, error)       { return r.c.c.Stat(p) }
func (r remoteFS) open(p string) (io.ReadSeekCloser, error) { return r.c.c.Open(p) }
func (r remoteFS) create(p string, keep bool) (writeSeekCloser, error) {
	flag := os.O_WRONLY
	if !keep {
		flag |= os.O_CREATE | os.O_EXCL
	}
	parent, err := r.c.c.Stat(path.Dir(p))
	if err != nil {
		return nil, err
	}
	// Only a folder anyone may write to without the sticky bit lets
	// strangers replace the part file; group-writable folders (umask 002,
	// shared team folders) and /tmp are fine.
	if parent.Mode().Perm()&0o002 != 0 && parent.Mode()&fs.ModeSticky == 0 {
		return nil, ErrUnsafeDestination
	}
	if err := r.resolve(p); err != nil {
		return nil, err
	}
	f, err := r.c.c.OpenFile(p, flag)
	if err == nil {
		f.Chmod(0o600)
	}
	return f, err
}
func (r remoteFS) mkdirAll(p string) error {
	if err := r.checkParents(p); err != nil {
		return err
	}
	return r.c.c.MkdirAll(p)
}
func (r remoteFS) remove(p string) error { return r.c.c.Remove(p) }
func (r remoteFS) rename(from, to string) error {
	if _, ok := r.c.c.HasExtension("posix-rename@openssh.com"); ok {
		return r.c.c.PosixRename(from, to)
	}
	// SFTP v3 rename must refuse an existing destination; never delete it.
	return r.c.c.Rename(from, to)
}
func (r remoteFS) chtimes(p string, t time.Time) error { return r.c.c.Chtimes(p, t, t) }
