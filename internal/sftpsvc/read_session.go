package sftpsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/Licoy/sshyard/internal/scpfs"
	"github.com/pkg/sftp"
)

var ErrOperationsBusy = errors.New("sftp: too many pending operations")

var operationPools = struct {
	sync.Mutex
	slots map[*ssh.Client]*operationPool
}{slots: make(map[*ssh.Client]*operationPool)}

func operationSlots(conn *ssh.Client) *operationPool {
	operationPools.Lock()
	defer operationPools.Unlock()
	if slots := operationPools.slots[conn]; slots != nil {
		return slots
	}
	slots := &operationPool{changed: make(chan struct{})}
	operationPools.slots[conn] = slots
	go func() {
		conn.Wait()
		operationPools.Lock()
		delete(operationPools.slots, conn)
		operationPools.Unlock()
		slots.end()
	}()
	return slots
}

// operationClient is an SFTP channel for one operation: one at rest in
// the pool when there is one, else a new one (newOperation). The caller
// gives it back with release.
func (c *Client) operationClient(ctx context.Context) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.sudo == nil {
		if sub := c.operations.take(); sub != nil {
			return sub, nil
		}
	}
	return c.newOperation(ctx)
}

// newOperation bounds pending SSH channel opens, which SSH cannot cancel.
// Once a channel exists, cancellation closes it before subsystem negotiation.
func (c *Client) newOperation(ctx context.Context) (*Client, error) {
	reservation, err := c.operations.reserve(ctx)
	if err != nil {
		return nil, err
	}
	type result struct {
		c   *Client
		err error
	}
	ready := make(chan result)
	go func() {
		sub, err := c.negotiateOperation(ctx)
		// Negotiation no longer occupies a pending-open slot once it finishes.
		// Release before handing the client to a caller that may open again.
		// A cancelled NewSession keeps its slot until negotiation really ends.
		reservation.release()
		select {
		case ready <- result{sub, err}:
		case <-ctx.Done():
			if sub != nil {
				sub.Close()
			}
		}
	}()
	select {
	case got := <-ready:
		if err := ctx.Err(); err != nil {
			if got.c != nil {
				got.c.Close()
			}
			return nil, err
		}
		return got.c, got.err
	case <-ctx.Done():
		reservation.abandon()
		return nil, ctx.Err()
	}
}

func (c *Client) negotiateOperation(ctx context.Context) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.sudo != nil {
		return c.sudoOperation(ctx)
	}
	if withoutSFTP(c.conn) {
		return c.scpOperation()
	}
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	complete := false
	defer func() {
		if !complete {
			session.Close()
		}
	}()
	rd, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	wr, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := session.RequestSubsystem("sftp"); err != nil {
		if err.Error() != errSubsystem {
			return nil, err
		}
		// The server has no SFTP: its shell and scp stand in.
		session.Close()
		markWithoutSFTP(c.conn)
		return c.scpOperation()
	}
	sc, err := sftp.NewClientPipe(rd, wr)
	if err != nil {
		if ctx.Err() == nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			// The server took the subsystem but ended it unanswered, as
			// dropbear or OpenSSH does whose sftp-server is missing: its
			// shell and scp stand in, if it runs commands.
			session.Close()
			sub, scpErr := c.scpOperation()
			if scpErr != nil {
				return nil, fmt.Errorf("%w (%w)", scpErr, err)
			}
			markWithoutSFTP(c.conn)
			return sub, nil
		}
		return nil, err
	}
	complete = true
	return opened(&Client{c: sc, conn: c.conn, operation: session, operations: c.operations}), nil
}

// errSubsystem is what x/crypto/ssh says when the server refuses a
// subsystem.
const errSubsystem = "ssh: subsystem request failed"

// noSFTP are the connections whose server refused the sftp subsystem.
var noSFTP = struct {
	sync.Mutex
	m map[*ssh.Client]bool
}{m: map[*ssh.Client]bool{}}

func withoutSFTP(conn *ssh.Client) bool {
	noSFTP.Lock()
	defer noSFTP.Unlock()
	return noSFTP.m[conn]
}

func markWithoutSFTP(conn *ssh.Client) {
	noSFTP.Lock()
	defer noSFTP.Unlock()
	if noSFTP.m[conn] {
		return
	}
	noSFTP.m[conn] = true
	go func() {
		conn.Wait()
		noSFTP.Lock()
		delete(noSFTP.m, conn)
		noSFTP.Unlock()
	}()
}

// release ends an operation on sub, which ended with err: a plain SFTP
// channel that is still sound goes back to the pool, any other closes.
// The caller must have stopped what would close sub (its AfterFuncs),
// left no request running and no file open; with ctx done, sub closes.
func (sub *Client) release(ctx context.Context, err error) {
	if ctx.Err() != nil || !sub.poolable() || !reusable(err) || !sub.operations.put(sub) {
		sub.Close()
	}
}

// poolable says sub is a plain SFTP channel of its own, still open: not
// sudo's (its server runs as root) nor scp's.
func (sub *Client) poolable() bool {
	return sub.operation != nil && sub.sudo == nil && !sub.scp && sub.operations != nil &&
		sub.closed != nil && sub.closed.Err() == nil
}

// reusable says an operation that ended with err left its channel sound:
// it ended, or the server answered with a refusal (a missing file, a
// permission) or what it sent failed a check of ours.
func reusable(err error) bool {
	var status *sftp.StatusError
	switch {
	case err == nil:
		return true
	case channelLost(err):
		return false
	}
	return errors.As(err, &status) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, ErrReadLimit) || errors.Is(err, ErrChanged) || errors.Is(err, ErrUnsafePath) || errors.Is(err, errNotRegular)
}

// channelLost says err is the end of the channel rather than a server's
// answer.
func channelLost(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, sftp.ErrSSHFxConnectionLost) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
}

// withOperation runs f on an SFTP channel of its own, which ending ctx
// closes, then gives the channel back (release). An idempotent f may
// take a channel at rest in the pool, and runs again once on a new one
// when that channel turns out dead; any other f gets a new channel.
func (c *Client) withOperation(ctx context.Context, idempotent bool, f func(sub *Client) error) error {
	open := c.operationClient
	if !idempotent {
		open = c.newOperation
	}
	for {
		sub, err := open(ctx)
		if err != nil {
			return err
		}
		stop := context.AfterFunc(ctx, func() { sub.Close() })
		err = f(sub)
		if !stop() || ctx.Err() != nil {
			sub.Close()
			return ctx.Err()
		}
		if idempotent && sub.reused && channelLost(err) {
			// It died while it rested: once more, on a new channel.
			sub.Close()
			open, idempotent = c.newOperation, false
			continue
		}
		sub.release(ctx, err)
		return err
	}
}

// scpOperation is an SFTP client of a server without SFTP (scpfs).
func (c *Client) scpOperation() (*Client, error) {
	sc, err := scpfs.NewClient(c.conn)
	if err != nil {
		return nil, fmt.Errorf("sftp: no SFTP, and %w", err)
	}
	return opened(&Client{c: sc, conn: c.conn, operations: c.operations, scp: true}), nil
}
