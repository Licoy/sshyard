package zmodem

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
	"github.com/Licoy/sshyard/internal/xfer/xfertest"
)

func TestFind(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		at     int
		upload bool
	}{
		{"sz", "rz\r**\x18B00000000000000\r\x8a\x11", 3, false},
		{"rz", "rz waiting to receive.**\x18B0100000023be50\r\x8a\x11", 22, true},
		{"rz without the end", "**\x18B0100000023be50", 0, true},
		{"after a false start", "**\x18Bzz**\x18B00000000000000\r\x8a", 6, false},
		{"bad CRC", "**\x18B0100000023be51\r\x8a\x11", -1, false},
		{"truncated", "**\x18B01000000", -1, false},
		{"another frame type", "**\x18B0900000000" + hex16(zrpos), -1, false},
		{"text", "ls -l **/*.go", -1, false},
	}
	for _, c := range cases {
		at, o := Find([]byte(c.in))
		if at != c.at {
			t.Errorf("%s: at %d, want %d", c.name, at, c.at)
			continue
		}
		if at >= 0 && o.Upload != c.upload {
			t.Errorf("%s: upload %v, want %v", c.name, o.Upload, c.upload)
		}
	}
}

// hex16 is the CRC of a hex header of typ with zero flags, in hex.
func hex16(typ byte) string {
	h := hexHeader(header{typ: typ})
	return string(h[14:18])
}

func TestHexHeaderMatchesLrzsz(t *testing.T) {
	if got := string(hexHeader(flagHeader(zrinit, canfdx|canovio|canfc32))); got != "**\x18B0100000023be50\r\x8a\x11" {
		t.Fatalf("ZRINIT = %q", got)
	}
	if got := string(hexHeader(header{typ: zrqinit})); got != "**\x18B00000000000000\r\x8a\x11" {
		t.Fatalf("ZRQINIT = %q", got)
	}
}

func randomBytes(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func allBytes() []byte {
	var b []byte
	for range 8 {
		for i := range 256 {
			b = append(b, byte(i))
		}
	}
	// What escaping looks at: CR after '@', runs of ZDLE.
	return append(b, "@\r@\x8d\x18\x18\x18\x18\x18\x18*\x18B0"...)
}

type result struct {
	rest []byte
	err  error
}

// loop sends items from one end of a pipe to the other, as sz to us.
func loop(t *testing.T, ctx context.Context, items []xfertest.Item, o Options, sink *xfertest.Sink, setup func(a, b *xfertest.End)) (sent, got result) {
	t.Helper()
	a, b := xfertest.Pipe()
	if setup != nil {
		setup(a, b)
	}
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Second
	}
	// sz starts: what the receiver reads begins at its ZRQINIT.
	a.Write(hexHeader(header{typ: zrqinit}))
	done := make(chan result, 1)
	go func() {
		// rz answers: what the sender reads begins at its ZRINIT.
		rest, err := Send(ctx, a, &xfertest.Source{Items: items}, o)
		done <- result{rest, err}
	}()
	rest, err := Receive(ctx, b, sink, o)
	got = result{rest, err}
	select {
	case sent = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sender did not end")
	}
	return sent, got
}

func files(items ...xfertest.Item) []xfertest.Item { return items }

func file(name string, data []byte) xfertest.Item {
	return xfertest.Item{File: xfer.File{Name: name, Mode: 0o640, ModTime: time.Unix(1700000000, 0)}, Data: data}
}

func checkFiles(t *testing.T, sink *xfertest.Sink, items []xfertest.Item) {
	t.Helper()
	n := 0
	for _, it := range items {
		if it.File.Dir {
			continue
		}
		n++
		got, ok := sink.Files[it.File.Name]
		if !ok {
			t.Errorf("%s: not received", it.File.Name)
			continue
		}
		if !bytes.Equal(got, it.Data) {
			t.Errorf("%s: %d bytes received, want %d, or not the same", it.File.Name, len(got), len(it.Data))
		}
	}
	if len(sink.Files) != n {
		t.Errorf("received %v, want %d files", sink.Committed(), n)
	}
}

func TestSendReceive(t *testing.T) {
	items := files(
		file("empty", nil),
		file("all bytes", allBytes()),
		file("random", randomBytes(1<<20, 1)),
		xfertest.Item{File: xfer.File{Name: "a folder", Dir: true}},
		file("small", []byte("hello\n")),
	)
	for _, ctl := range []bool{false, true} {
		sink := &xfertest.Sink{}
		var wire *xfertest.End
		sent, got := loop(t, context.Background(), items, Options{EscapeControl: ctl}, sink, func(a, b *xfertest.End) { wire = a })
		if sent.err != nil || got.err != nil {
			t.Fatalf("ctl %v: send %v, receive %v", ctl, sent.err, got.err)
		}
		checkFiles(t, sink, items)
		if sink.Aborted != 0 {
			t.Errorf("ctl %v: %d aborted", ctl, sink.Aborted)
		}
		for _, f := range sink.Info {
			if f.Name == "small" && (f.Size != 6 || f.Mode != 0o640 || f.ModTime.Unix() != 1700000000) {
				t.Errorf("ctl %v: info %+v", ctl, f)
			}
		}
		if ctl {
			// Bar the hex headers, which go as they are, and the XON after
			// a ZCRCW, no control character goes raw.
			for i, c := range withoutHex(wire.Wrote.Bytes()) {
				if c&0x60 == 0 && c != zdle && c != xon {
					t.Fatalf("control byte %#x sent raw at %d", c, i)
				}
			}
		}
	}
}

// withoutHex is p without its hex headers.
func withoutHex(p []byte) []byte {
	var out []byte
	for {
		i := bytes.Index(p, []byte{zpad, zpad, zdle, zhex})
		if i < 0 || len(p) < i+18 {
			return append(out, p...)
		}
		out = append(out, p[:i]...)
		p = p[i+18:]
		for k := 0; k < 3 && len(p) > 0 && (p[0] == '\r' || p[0] == 0x8a || p[0] == xon); k++ {
			p = p[1:]
		}
	}
}

func TestRestAfterTransfer(t *testing.T) {
	sink := &xfertest.Sink{}
	sent, got := loop(t, context.Background(), files(file("f", []byte("data"))), Options{}, sink, func(a, b *xfertest.End) {
		a.Tamper = func(p []byte) []byte {
			if string(p) == "OO" {
				return []byte("OO\r\nuser@host:~$ ")
			}
			return p
		}
		fin := hexHeader(posHeader(zfin, 0))
		b.Tamper = func(p []byte) []byte {
			if bytes.Equal(p, fin) {
				return append(p, "rz done\r\n"...)
			}
			return p
		}
	})
	if sent.err != nil || got.err != nil {
		t.Fatalf("send %v, receive %v", sent.err, got.err)
	}
	if string(got.rest) != "\r\nuser@host:~$ " {
		t.Errorf("receiver rest %q", got.rest)
	}
	if string(sent.rest) != "rz done\r\n" {
		t.Errorf("sender rest %q", sent.rest)
	}
}

func TestRecoversFromCorruption(t *testing.T) {
	items := files(file("random", randomBytes(300<<10, 2)), file("second", randomBytes(5000, 3)))
	for _, c32 := range []bool{true, false} {
		sink := &xfertest.Sink{}
		writes, flipped := 0, 0
		sent, got := loop(t, context.Background(), items, Options{}, sink, func(a, b *xfertest.End) {
			a.Tamper = func(p []byte) []byte {
				writes++
				if (writes == 4 || writes == 9 || writes == 15) && len(p) > 200 {
					p[len(p)/2] ^= 0x55
					flipped++
				}
				return p
			}
			if !c32 {
				// A receiver without CRC-32 makes the sender use CRC-16.
				b.Tamper = func(p []byte) []byte {
					return bytes.Replace(p, hexHeader(flagHeader(zrinit, canfdx|canovio|canfc32)), hexHeader(flagHeader(zrinit, canfdx|canovio)), 1)
				}
			}
		})
		if sent.err != nil || got.err != nil {
			t.Fatalf("crc32 %v: send %v, receive %v", c32, sent.err, got.err)
		}
		if flipped == 0 {
			t.Fatalf("crc32 %v: nothing corrupted", c32)
		}
		checkFiles(t, sink, items)
	}
}

func TestSkip(t *testing.T) {
	items := files(file("keep", []byte("1")), file("skip", randomBytes(100<<10, 4)), file("also", []byte("3")))
	sink := &xfertest.Sink{Skip: map[string]bool{"skip": true}}
	sent, got := loop(t, context.Background(), items, Options{}, sink, nil)
	if sent.err != nil || got.err != nil {
		t.Fatalf("send %v, receive %v", sent.err, got.err)
	}
	if names := sink.Committed(); len(names) != 2 || names[0] != "also" || names[1] != "keep" {
		t.Fatalf("committed %v", names)
	}
}

func TestSinkFailureCancels(t *testing.T) {
	sink := &xfertest.Sink{Fail: errors.New("disk full")}
	sent, got := loop(t, context.Background(), files(file("f", []byte("x"))), Options{}, sink, nil)
	if got.err == nil || got.err.Error() != "disk full" {
		t.Fatalf("receive %v", got.err)
	}
	if !errors.Is(sent.err, xfer.ErrRefused) {
		t.Fatalf("send %v, want ErrRefused", sent.err)
	}
}

func TestCancel(t *testing.T) {
	for _, side := range []string{"sender", "receiver"} {
		ctx, cancel := context.WithCancel(context.Background())
		sendCtx, recvCtx := context.Background(), context.Background()
		if side == "sender" {
			sendCtx = ctx
		} else {
			recvCtx = ctx
		}
		sink := &xfertest.Sink{}
		a, b := xfertest.Pipe()
		var once bool
		a.Tamper = func(p []byte) []byte {
			// Cancel once data flows.
			if !once && len(p) > 1000 {
				once = true
				cancel()
			}
			return p
		}
		o := Options{Timeout: 2 * time.Second}
		a.Write(hexHeader(header{typ: zrqinit}))
		done := make(chan result, 1)
		go func() {
			rest, err := Send(sendCtx, a, &xfertest.Source{Items: files(file("big", randomBytes(4<<20, 5)))}, o)
			done <- result{rest, err}
		}()
		_, rerr := Receive(recvCtx, b, sink, o)
		sent := <-done
		cancel()
		if side == "sender" {
			if !errors.Is(sent.err, context.Canceled) || !errors.Is(rerr, xfer.ErrRefused) {
				t.Errorf("sender cancels: send %v, receive %v", sent.err, rerr)
			}
		} else if !errors.Is(rerr, context.Canceled) || !errors.Is(sent.err, xfer.ErrRefused) {
			t.Errorf("receiver cancels: send %v, receive %v", sent.err, rerr)
		}
		if len(sink.Files) != 0 || sink.Aborted != 1 {
			t.Errorf("%s cancels: committed %v, aborted %d", side, sink.Committed(), sink.Aborted)
		}
	}
}

func TestRefuse(t *testing.T) {
	a, b := xfertest.Pipe()
	a.Write([]byte("rz\r**\x18B00000000000000\r\x8a\x11"))
	go func() {
		// sz, cancelled, exits; the shell prints its prompt.
		time.Sleep(50 * time.Millisecond)
		a.Write([]byte("\r\n$ "))
	}()
	rest, err := Refuse(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(a.Wrote.Bytes(), []byte("rz\r")) {
		t.Fatal("setup")
	}
	if out := b.Wrote.Bytes(); !bytes.HasPrefix(out, bytes.Repeat([]byte{zdle}, 5)) {
		t.Errorf("sent %q, want the cancel sequence", out)
	}
	if string(rest) != "\r\n$ " {
		t.Errorf("rest %q", rest)
	}
}

func TestReceiveRefusesCommands(t *testing.T) {
	a, b := xfertest.Pipe()
	var e encoder
	e.binHeader(header{typ: zcommand})
	e.subpacket([]byte("rm -rf ~\x00"), zcrcw, false)
	a.Write(hexHeader(header{typ: zrqinit}))
	a.Write(e.out)
	sink := &xfertest.Sink{}
	_, err := Receive(context.Background(), b, sink, Options{Timeout: time.Second})
	if !errors.Is(err, errCommand) {
		t.Fatalf("err %v, want errCommand", err)
	}
	if !bytes.Contains(b.Wrote.Bytes(), cancelSeq) {
		t.Error("the sender was not cancelled")
	}
}

func TestParseInfo(t *testing.T) {
	f := parseInfo([]byte("dir/name.txt\x0012345 14567543210 100755 0 3 99999\x00"))
	if f.Name != "dir/name.txt" || f.Size != 12345 || f.Mode != 0o755 || f.ModTime.Unix() != 0o14567543210 {
		t.Fatalf("%+v", f)
	}
	f = parseInfo([]byte("bare\x00"))
	if f.Name != "bare" || f.Size != -1 || f.Mode != 0 || !f.ModTime.IsZero() {
		t.Fatalf("%+v", f)
	}
}
