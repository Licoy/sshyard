package sftpsvc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/pkg/sftp"
)

// countingReader counts the READ requests a file is served by.
type countingReader struct {
	io.ReaderAt
	n *atomic.Int32
}

func (r countingReader) ReadAt(b []byte, off int64) (int, error) {
	r.n.Add(1)
	return r.ReaderAt.ReadAt(b, off)
}

type countingGet struct {
	base sftp.FileReader
	n    *atomic.Int32
}

func (g countingGet) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	ra, err := g.base.Fileread(r)
	if err != nil {
		return nil, err
	}
	return countingReader{ra, g.n}, nil
}

// channels records the SFTP channels a server serves: each can be told
// to end at its next request, as a server whose sftp-server was killed.
type channels struct {
	mu   sync.Mutex
	list []*killable
}

type killable struct {
	io.ReadWriteCloser
	kill atomic.Bool
}

func (k *killable) Read(b []byte) (int, error) {
	n, err := k.ReadWriteCloser.Read(b)
	if k.kill.Load() {
		k.ReadWriteCloser.Close()
		return 0, io.EOF
	}
	return n, err
}

func (cs *channels) wrap(rw io.ReadWriteCloser) io.ReadWriteCloser {
	k := &killable{ReadWriteCloser: rw}
	cs.mu.Lock()
	cs.list = append(cs.list, k)
	cs.mu.Unlock()
	return k
}

func (cs *channels) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.list)
}

func (cs *channels) last() *killable {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.list[len(cs.list)-1]
}

// poolServer is a FakeFS server whose READ requests and channels are
// counted, and a client of it.
func poolServer(t *testing.T, files map[string][]byte) (*Client, *channels, *atomic.Int32) {
	t.Helper()
	fake := &sshtest.FakeFS{Files: files, Dirs: map[string][]sshtest.FakeEntry{"/": {}}}
	reads := &atomic.Int32{}
	cs := &channels{}
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	srv.SFTPHandlers = func() sftp.Handlers {
		h := fake.Handlers()
		h.FileGet = countingGet{h.FileGet, reads}
		return h
	}
	srv.SFTPConn = cs.wrap
	t.Cleanup(srv.Close)
	kh, _ := session.OpenKnownHosts(filepath.Join(t.TempDir(), "kh"))
	kh.Add(srv.Addr, srv.HostKey.PublicKey())
	conn, err := session.Dial(context.Background(), session.Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(),
		Credentials: func(context.Context) (session.Credentials, error) {
			return session.Credentials{Password: "secret"}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c, err := New(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, cs, reads
}

func idleCount(c *Client) int {
	c.operations.mu.Lock()
	defer c.operations.mu.Unlock()
	return len(c.operations.idle)
}

func waitIdle(t *testing.T, c *Client, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for idleCount(c) != n {
		if time.Now().After(deadline) {
			t.Fatalf("idle channels: %d, want %d", idleCount(c), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReadFileFewRequests(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 100<<10/16)
	c, _, reads := poolServer(t, map[string][]byte{"/big": data})
	got, ver, err := c.ReadFileContext(t.Context(), "/big", DefaultReadLimit)
	if err != nil || !bytes.Equal(got, data) || ver.Size != int64(len(data)) {
		t.Fatalf("read: %d bytes, %+v, %v", len(got), ver, err)
	}
	// 100 KB in 32 KB requests sent at once: 4, where io.ReadAll's
	// growing reads took 15 one after the other.
	if n := reads.Load(); n > 4 {
		t.Fatalf("%d READ requests for 100 KB", n)
	}
}

func TestReadFileLyingSizeStillBounded(t *testing.T) {
	r, _ := queueServer(t)
	data := bytes.Repeat([]byte("x"), 100<<10)
	fake := &sshtest.FakeFS{Files: map[string][]byte{"/file": data}}
	r.srv.SFTPHandlers = func() sftp.Handlers { h := fake.Handlers(); h.FileList = lyingFileList{h.FileList}; return h }
	c, err := r.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The server says 0 bytes and sends 100 KB.
	if got, _, err := c.ReadFileContext(t.Context(), "/file", 50<<10); !errors.Is(err, ErrReadLimit) || got != nil {
		t.Fatalf("over limit: %d bytes, %v", len(got), err)
	}
	if got, _, err := c.ReadFileContext(t.Context(), "/file", 1<<20); !errors.Is(err, ErrChanged) || got != nil {
		t.Fatalf("size changed: %d bytes, %v", len(got), err)
	}
}

func TestReadUpTo(t *testing.T) {
	src := strings.Repeat("a", 1000)
	for _, tc := range []struct {
		size, n int64
		want    int
	}{
		{1001, 2000, 1000}, // as stat said
		{10, 2000, 1000},   // more than stat said
		{0, 2000, 1000},    // no size
		{5000, 300, 300},   // capped
		{10, 300, 300},
	} {
		got, err := readUpTo(strings.NewReader(src), tc.size, tc.n)
		if err != nil || len(got) != tc.want {
			t.Errorf("size %d n %d: %d bytes, %v", tc.size, tc.n, len(got), err)
		}
	}
	boom := errors.New("boom")
	if _, err := readUpTo(io.MultiReader(strings.NewReader("ab"), errReader{boom}), 10, 100); !errors.Is(err, boom) {
		t.Fatalf("error lost: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// linkDir lists n links in "/", l0 … ln-1, the even ones to folders.
type linkDir struct{ n int }

type linkEntry struct {
	name string
	mode fs.FileMode
}

func (i linkEntry) Name() string       { return i.name }
func (i linkEntry) Size() int64        { return 0 }
func (i linkEntry) Mode() fs.FileMode  { return i.mode }
func (i linkEntry) ModTime() time.Time { return time.Unix(1700000000, 0) }
func (i linkEntry) IsDir() bool        { return i.mode.IsDir() }
func (i linkEntry) Sys() any           { return nil }

type infos []os.FileInfo

func (l infos) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if off+int64(n) >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}

func (d linkDir) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	name := path.Base(r.Filepath)
	switch {
	case r.Filepath == "/":
		if r.Method == "List" {
			var l infos
			for i := range d.n {
				l = append(l, linkEntry{fmt.Sprintf("l%d", i), fs.ModeSymlink | 0o777})
			}
			l = append(l, linkEntry{"plain", 0o644})
			return l, nil
		}
		return infos{linkEntry{"/", fs.ModeDir | 0o755}}, nil
	case r.Method == "Stat" && strings.HasPrefix(name, "l"):
		i, err := strconv.Atoi(name[1:])
		if err != nil || i >= d.n {
			return nil, os.ErrNotExist
		}
		if i%2 == 0 {
			return infos{linkEntry{name, fs.ModeDir | 0o755}}, nil
		}
		return infos{linkEntry{name, 0o644}}, nil
	}
	return nil, os.ErrNotExist
}

// gatedReplies holds the server's replies from its first STAT request
// on until want STAT requests came: a client that waits for each reply
// before the next request never gets there.
type gatedReplies struct {
	io.ReadWriteCloser
	want    int
	mu      sync.Mutex
	buf     []byte
	stats   int
	open    chan struct{}
	release sync.Once
}

func (g *gatedReplies) Read(b []byte) (int, error) {
	n, err := g.ReadWriteCloser.Read(b)
	g.mu.Lock()
	g.buf = append(g.buf, b[:n]...)
	for len(g.buf) >= 5 {
		size := int(binary.BigEndian.Uint32(g.buf))
		if len(g.buf) < 4+size {
			break
		}
		if g.buf[4] == 17 { // SSH_FXP_STAT
			g.stats++
		}
		g.buf = g.buf[4+size:]
	}
	if g.stats >= g.want {
		g.release.Do(func() { close(g.open) })
	}
	g.mu.Unlock()
	return n, err
}

func (g *gatedReplies) Write(b []byte) (int, error) {
	g.mu.Lock()
	held := g.stats > 0
	g.mu.Unlock()
	if held {
		select {
		case <-g.open:
		case <-time.After(2 * time.Second):
			g.release.Do(func() { close(g.open) })
		}
	}
	return g.ReadWriteCloser.Write(b)
}

func TestListFollowsLinksAtOnce(t *testing.T) {
	r, _ := queueServer(t)
	const links = 8
	var gates []*gatedReplies
	var mu sync.Mutex
	r.srv.SFTPHandlers = func() sftp.Handlers {
		h := (&sshtest.FakeFS{}).Handlers()
		h.FileList = linkDir{links}
		return h
	}
	r.srv.SFTPConn = func(rw io.ReadWriteCloser) io.ReadWriteCloser {
		g := &gatedReplies{ReadWriteCloser: rw, want: 4, open: make(chan struct{})}
		mu.Lock()
		gates = append(gates, g)
		mu.Unlock()
		return g
	}
	c, err := r.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	es, err := c.List("/")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatal("links were followed one at a time")
	}
	if len(es) != links+1 {
		t.Fatalf("%d entries: %+v", len(es), es)
	}
	for _, e := range es {
		want := false
		if strings.HasPrefix(e.Name, "l") {
			i, _ := strconv.Atoi(e.Name[1:])
			want = i%2 == 0
		}
		if e.IsDir != want {
			t.Errorf("%s: IsDir %v", e.Name, e.IsDir)
		}
	}
	// Folders first, as before.
	for i, e := range es[:links/2] {
		if !e.IsDir {
			t.Fatalf("entry %d %s is not a folder", i, e.Name)
		}
	}
}

func TestOperationChannelReused(t *testing.T) {
	c, cs, _ := poolServer(t, map[string][]byte{"/a": []byte("hello")})
	base := cs.count()
	for range 3 {
		if data, _, err := c.ReadFileContext(t.Context(), "/a", DefaultReadLimit); err != nil || string(data) != "hello" {
			t.Fatalf("read: %q %v", data, err)
		}
	}
	if _, _, err := c.ReadFileContext(t.Context(), "/missing", DefaultReadLimit); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := c.ListContext(t.Context(), "/"); err != nil {
		t.Fatal(err)
	}
	// A refusal leaves the channel sound: one channel served them all.
	if n := cs.count() - base; n != 1 {
		t.Fatalf("%d channels opened, want 1", n)
	}
	waitIdle(t, c, 1)
}

func TestOperationChannelNotReusedAfterCancel(t *testing.T) {
	fake := &sshtest.FakeFS{Files: map[string][]byte{"/a": []byte("hello")}}
	c := connectFake(t, fake)
	if _, _, err := c.ReadFileContext(t.Context(), "/a", DefaultReadLimit); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, c, 1)
	fake.Stop()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, err := c.ReadFileContext(ctx, "/a", DefaultReadLimit)
	fake.Resume()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// The pooled channel served the canceled read and is closed now.
	waitIdle(t, c, 0)
	if data, _, err := c.ReadFileContext(t.Context(), "/a", DefaultReadLimit); err != nil || string(data) != "hello" {
		t.Fatalf("next read: %q %v", data, err)
	}
}

func TestReleaseClosesBrokenChannel(t *testing.T) {
	c, _, _ := poolServer(t, nil)
	for _, tc := range []struct {
		err  error
		pool bool
	}{
		{nil, true},
		{fs.ErrNotExist, true},
		{&sftp.StatusError{Code: 4}, true},
		{ErrChanged, true},
		{io.EOF, false},
		{io.ErrUnexpectedEOF, false},
		{fmt.Errorf("open: %w", sftp.ErrSSHFxConnectionLost), false},
		{errors.New("something else"), false},
	} {
		sub, err := c.operationClient(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		sub.release(t.Context(), tc.err)
		if pooled := sub.closed.Err() == nil; pooled != tc.pool {
			t.Errorf("%v: pooled %v, want %v", tc.err, pooled, tc.pool)
		}
		if tc.pool {
			if got := c.operations.take(); got != sub {
				t.Errorf("%v: pool gave %p, want %p", tc.err, got, sub)
			}
			sub.Close()
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	sub, err := c.operationClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	sub.release(ctx, nil)
	if sub.closed.Err() == nil || idleCount(c) != 0 {
		t.Fatal("channel of a canceled operation pooled")
	}
}

func TestPoolKeepsFewChannels(t *testing.T) {
	c, _, _ := poolServer(t, nil)
	var subs []*Client
	for range maxIdleOperations + 2 {
		sub, err := c.operationClient(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
	}
	for _, sub := range subs {
		sub.release(t.Context(), nil)
	}
	if n := idleCount(c); n != maxIdleOperations {
		t.Fatalf("%d idle channels", n)
	}
	closed := 0
	for _, sub := range subs {
		if sub.closed.Err() != nil {
			closed++
		}
	}
	if closed != 2 {
		t.Fatalf("%d channels closed, want 2", closed)
	}
}

func TestReadRetriesDeadIdleChannel(t *testing.T) {
	c, cs, _ := poolServer(t, map[string][]byte{"/a": []byte("hello")})
	if _, _, err := c.ReadFileContext(t.Context(), "/a", DefaultReadLimit); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, c, 1)
	opened := cs.count()
	// The resting channel ends at its next request.
	cs.last().kill.Store(true)
	data, _, err := c.ReadFileContext(t.Context(), "/a", DefaultReadLimit)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read after dead channel: %q %v", data, err)
	}
	if n := cs.count() - opened; n != 1 {
		t.Fatalf("%d new channels, want 1", n)
	}
	// A listing too.
	waitIdle(t, c, 1)
	cs.last().kill.Store(true)
	if _, err := c.ListContext(t.Context(), "/"); err != nil {
		t.Fatalf("list after dead channel: %v", err)
	}
}

func TestIdleChannelClosesAfterTimeout(t *testing.T) {
	old := idleOperationTimeout
	idleOperationTimeout = 20 * time.Millisecond
	t.Cleanup(func() { idleOperationTimeout = old })
	c, _, _ := poolServer(t, nil)
	sub, err := c.operationClient(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sub.release(t.Context(), nil)
	waitIdle(t, c, 0)
	if sub.closed.Err() == nil {
		t.Fatal("expired channel left open")
	}
}

func TestIdleChannelLeavesPoolWhenServerEndsIt(t *testing.T) {
	c, cs, _ := poolServer(t, nil)
	sub, err := c.operationClient(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sub.release(t.Context(), nil)
	waitIdle(t, c, 1)
	cs.last().Close()
	waitIdle(t, c, 0)
}

func TestPoolClosesWithConnection(t *testing.T) {
	c, _, _ := poolServer(t, nil)
	sub, err := c.operationClient(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sub.release(t.Context(), nil)
	pool := c.operations
	c.conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for sub.closed.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("idle channel outlived its connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
	pool.mu.Lock()
	ended, idle := pool.ended, len(pool.idle)
	pool.mu.Unlock()
	if !ended || idle != 0 {
		t.Fatalf("pool of an ended connection: ended %v, %d idle", ended, idle)
	}
}

func TestTransferReturnsChannelOnlyWhenDone(t *testing.T) {
	r, root := queueServer(t)
	data := bigFile(t, filepath.Join(root, "source"), 64)
	c, err := r.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, c, 0)
	ts := newQueue(r)
	dst := filepath.Join(t.TempDir(), "target")
	ts.Add(Download, filepath.Join(root, "source"), dst, "target")
	ts.Wait()
	if tr := ts.List()[0]; tr.State != Done {
		t.Fatalf("%v", tr.Err)
	}
	downloaded(t, dst, data)
	waitIdle(t, c, 1)

	// A canceled transfer closes its channel.
	ts2, id, _, _, release := heldDownload(t)
	shared, err := ts2.Client(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ts2.Cancel(id)
	release()
	ts2.Wait()
	waitIdle(t, shared, 0)
}

func TestTransferRetriesDeadIdleChannelAtOnce(t *testing.T) {
	data := bytes.Repeat([]byte("data"), 1<<10)
	c, cs, _ := poolServer(t, map[string][]byte{"/a": data, "/b": data})
	ts := &Transfers{Client: func(context.Context) (*Client, error) { return c, nil }, Logf: func(string, ...any) {},
		Backoff: func(int) time.Duration { return time.Millisecond }}
	dir := t.TempDir()
	ts.Add(Download, "/a", filepath.Join(dir, "a"), "a")
	ts.Wait()
	waitIdle(t, c, 1)
	cs.last().kill.Store(true)
	ts.Add(Download, "/b", filepath.Join(dir, "b"), "b")
	ts.Wait()
	// No backoff waited, no attempt counted.
	for _, tr := range ts.List() {
		if tr.State != Done || tr.Attempt != 0 {
			t.Fatalf("%s: %v attempt %d: %v", tr.Name, tr.State, tr.Attempt, tr.Err)
		}
	}
	downloaded(t, filepath.Join(dir, "b"), data)
}
