package sftpsvc

import (
	"context"
	"slices"
	"sync"
	"time"
)

const maxPendingOperations = 4

// A reservation remains pending until SSH negotiation really returns. Live
// callers may wait for capacity; four abandoned opens reject new work instead
// of accumulating more goroutines behind uncancellable SSH channel opens.
type operationPool struct {
	mu        sync.Mutex
	pending   int
	abandoned int
	changed   chan struct{}
	// idle are finished channels kept for the next operation; ended is
	// set once the connection is gone.
	idle  []idleOperation
	ended bool
}

type operationReservation struct {
	pool      *operationPool
	done      bool
	abandoned bool
}

func (p *operationPool) reserve(ctx context.Context) (*operationReservation, error) {
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if p.pending < maxPendingOperations {
			p.pending++
			p.mu.Unlock()
			return &operationReservation{pool: p}, nil
		}
		if p.abandoned == p.pending {
			p.mu.Unlock()
			return nil, ErrOperationsBusy
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (p *operationPool) wake() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (r *operationReservation) abandon() {
	p := r.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.done || r.abandoned {
		return
	}
	r.abandoned = true
	p.abandoned++
	p.wake()
}

func (r *operationReservation) release() {
	p := r.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	p.pending--
	if r.abandoned {
		p.abandoned--
	}
	p.wake()
}

// maxIdleOperations is how many finished SFTP channels a connection
// keeps for the next operation (few: each counts against the server's
// MaxSessions, as terminals do), idleOperationTimeout how long one waits.
const maxIdleOperations = 2

var idleOperationTimeout = 60 * time.Second

// idleOperation is a channel at rest in the pool, closed when its timer
// fires.
type idleOperation struct {
	c     *Client
	timer *time.Timer
}

// take is the channel that rested last, nil when none does. It needs no
// reservation: it is open already.
func (p *operationPool) take() *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.idle) > 0 {
		op := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		op.timer.Stop()
		if op.c.closed.Err() == nil {
			op.c.reused = true
			return op.c
		}
	}
	return nil
}

// put keeps sub for the next operation, or says it cannot (the pool is
// full or its connection ended): the caller then closes it.
func (p *operationPool) put(sub *Client) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || len(p.idle) >= maxIdleOperations {
		return false
	}
	p.idle = append(p.idle, idleOperation{sub, time.AfterFunc(idleOperationTimeout, func() { p.drop(sub) })})
	if !sub.watched {
		// A channel the server ends while it rests leaves the pool then,
		// not when the next operation finds it dead.
		sub.watched = true
		go func() {
			sub.c.Wait()
			p.drop(sub)
		}()
	}
	return true
}

// drop closes sub if it rests in the pool.
func (p *operationPool) drop(sub *Client) {
	p.mu.Lock()
	i := slices.IndexFunc(p.idle, func(op idleOperation) bool { return op.c == sub })
	if i < 0 {
		p.mu.Unlock()
		return
	}
	p.idle[i].timer.Stop()
	p.idle = slices.Delete(p.idle, i, i+1)
	p.mu.Unlock()
	sub.Close()
}

// end closes the resting channels of a connection that ended, and every
// channel offered afterwards.
func (p *operationPool) end() {
	p.mu.Lock()
	idle := p.idle
	p.idle, p.ended = nil, true
	p.mu.Unlock()
	for _, op := range idle {
		op.timer.Stop()
		op.c.Close()
	}
}
