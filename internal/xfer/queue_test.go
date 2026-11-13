package xfer

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestQueueFull: Add waits while the queue holds its most, until a Read
// makes room, and a Close lets it go, taking nothing.
func TestQueueFull(t *testing.T) {
	q := NewQueue(4)
	q.Add([]byte("abcd"))
	added := make(chan bool, 1)
	go func() { added <- q.Add([]byte("ef")) }()
	select {
	case <-added:
		t.Fatal("added to a full queue")
	case <-time.After(50 * time.Millisecond):
	}
	b := make([]byte, 2)
	if n, err := q.Read(b); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if !<-added {
		t.Fatal("refused once there was room")
	}
	go func() { added <- q.Add([]byte("gh")) }()
	time.Sleep(20 * time.Millisecond)
	if rest := string(q.Close()); rest != "cdef" {
		t.Fatalf("left %q", rest)
	}
	if <-added {
		t.Fatal("took bytes once closed")
	}
	if n, err := q.Read(b); n != 0 || err != io.EOF {
		t.Fatal(n, err)
	}
}

// TestQueueDeadline: a Read waiting past its deadline gives up, what is
// waiting is read even past it, and a later deadline waits again.
func TestQueueDeadline(t *testing.T) {
	q := NewQueue(0)
	b := make([]byte, 8)
	q.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	start := time.Now()
	if _, err := q.Read(b); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("gave up early")
	}
	q.Add([]byte("x"))
	if n, err := q.Read(b); n != 1 || err != nil {
		t.Fatal("past the deadline:", n, err)
	}
	q.SetReadDeadline(time.Time{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		q.Add([]byte("y"))
	}()
	if n, err := q.Read(b); n != 1 || err != nil || b[0] != 'y' {
		t.Fatal(n, err)
	}
	// A Close wakes a Read waiting with no deadline.
	go func() {
		time.Sleep(30 * time.Millisecond)
		q.Close()
	}()
	if _, err := q.Read(b); err != io.EOF {
		t.Fatal(err)
	}
}
