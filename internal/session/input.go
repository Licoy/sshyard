package session

import (
	"errors"
	"io"
	"sync"
)

// ErrInputBusy means another whole input write is still in progress. Input is
// refused rather than queued, so the UI never waits for a blocked writer's lock.
var ErrInputBusy = errors.New("session: input write in progress")

// CaptureInput binds input to the current connection. A later reconnect never
// redirects these writes to a different shell. Writes may block on remote flow
// control, so callers on the UI thread must write in a background goroutine.
func (s *Session) CaptureInput() (io.Writer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != Connected || s.stdin == nil {
		return nil, errors.New("session: not connected")
	}
	return s.stdin, nil
}

type serializedInput struct {
	io.WriteCloser
	// reply, when set, takes what the terminal and the login answer
	// instead of WriteCloser, which treats input as typed.
	reply io.Writer
	// mu is held for the whole of each write to the shell.
	mu sync.Mutex
	// queued is the input Send has taken that a goroutine, holding mu
	// while draining, has yet to write.
	qmu      sync.Mutex
	queued   []byte
	draining bool
}

// send queues p behind the input still being written, in order, and
// returns at once, so a stalled connection never holds up the UI thread.
// It refuses input while a whole write (Write) is in progress.
func (w *serializedInput) send(p []byte) error {
	w.qmu.Lock()
	defer w.qmu.Unlock()
	if !w.draining {
		if !w.mu.TryLock() {
			return ErrInputBusy
		}
		w.draining = true
		go w.drain()
	}
	w.queued = append(w.queued, p...)
	return nil
}

// drain writes what send queued until there is no more, then lets go of
// mu, which it holds from send's TryLock.
func (w *serializedInput) drain() {
	var p []byte
	for {
		w.qmu.Lock()
		if len(w.queued) == 0 {
			w.draining = false
			w.mu.Unlock()
			w.qmu.Unlock()
			return
		}
		// The two buffers swap, so a steady stream of keys allocates none;
		// a paste's is let go.
		if cap(p) > 64<<10 {
			p = nil
		}
		p, w.queued = w.queued, p[:0]
		w.qmu.Unlock()
		w.WriteCloser.Write(p)
	}
}

func (w *serializedInput) Write(p []byte) (int, error) {
	if !w.mu.TryLock() {
		return 0, ErrInputBusy
	}
	defer w.mu.Unlock()
	return w.WriteCloser.Write(p)
}

// raw is where bytes go to the shell as they are, never as typed: a
// transfer that took the stream over writes there.
func (w *serializedInput) raw() io.Writer {
	if w.reply != nil {
		return w.reply
	}
	return w.WriteCloser
}

// Terminal protocol replies already run off the UI thread and must wait for a
// whole paste to finish. Capture before launching so reconnect cannot redirect
// a reply intended for the old shell into the new one.
func (s *Session) replyInput(p []byte) {
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()
	if w == nil {
		return
	}
	w.answer(p)
}

// answer writes p, not as typed, after the input being written, in a
// goroutine of its own.
func (w *serializedInput) answer(p []byte) {
	p = append([]byte(nil), p...)
	go func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.reply != nil {
			w.reply.Write(p)
			return
		}
		w.WriteCloser.Write(p)
	}()
}
