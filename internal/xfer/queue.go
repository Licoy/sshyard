package xfer

import (
	"io"
	"os"
	"sync"
	"time"
)

// Queue holds bytes one goroutine adds for another to read, as a Conn
// reads them: with read deadlines, what is waiting read even past one.
// It is how the shell's output reaches a transfer.
type Queue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      []byte
	max      int
	closed   bool
	deadline time.Time
	timer    *time.Timer
}

// NewQueue returns a queue that holds up to max bytes before Add waits
// for them to be read; 0 holds any amount.
func NewQueue(max int) *Queue {
	q := &Queue{max: max}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Add queues p, waiting while the queue is full. It reports false, and
// takes nothing, once the queue is closed.
func (q *Queue) Add(p []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for !q.closed && q.max > 0 && len(q.buf) >= q.max {
		q.cond.Wait()
	}
	if q.closed {
		return false
	}
	q.buf = append(q.buf, p...)
	q.cond.Broadcast()
	return true
}

// Read reads what is queued, waiting for some until the deadline; io.EOF
// once the queue is closed and empty.
func (q *Queue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.buf) == 0 {
		switch {
		case q.closed:
			return 0, io.EOF
		case !q.deadline.IsZero() && !time.Now().Before(q.deadline):
			return 0, os.ErrDeadlineExceeded
		}
		q.cond.Wait()
	}
	n := copy(p, q.buf)
	q.buf = q.buf[n:]
	if len(q.buf) == 0 {
		q.buf = nil // let a large backlog go
	}
	q.cond.Broadcast()
	return n, nil
}

// SetReadDeadline sets when a waiting Read gives up; zero waits for ever.
func (q *Queue) SetReadDeadline(t time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deadline = t
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	if !t.IsZero() {
		if d := time.Until(t); d > 0 {
			q.timer = time.AfterFunc(d, func() {
				q.mu.Lock()
				q.cond.Broadcast()
				q.mu.Unlock()
			})
		}
	}
	q.cond.Broadcast()
	return nil
}

// Close ends the queue and returns what was left unread. Add then takes
// nothing, and Read returns io.EOF.
func (q *Queue) Close() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	rest := q.buf
	q.buf, q.closed = nil, true
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.cond.Broadcast()
	return rest
}
