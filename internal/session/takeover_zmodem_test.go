package session

import (
	"bytes"
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
	"github.com/Licoy/sshyard/internal/xfer/xfertest"
	"github.com/Licoy/sshyard/internal/zmodem"
)

// pipeStream is a shell whose other end a test drives as a server.
type pipeStream struct{ e, server *xfertest.End }

func (p pipeStream) Read(b []byte) (int, error)  { return p.e.Read(b) }
func (p pipeStream) Write(b []byte) (int, error) { return p.e.Write(b) }
func (p pipeStream) Close() error                { p.e.Close(); p.server.Close(); return nil }
func (p pipeStream) Resize(int, int) error       { return nil }

// TestTakeoverZmodem: sz on the server sends a file through the
// session, which the terminal shows nothing of, then the prompt shows.
func TestTakeoverZmodem(t *testing.T) {
	e, server := xfertest.Pipe()
	s := New(Config{}, Options{Open: opener(pipeStream{e, server})}, 80, 24)
	defer s.Close()
	sink := &xfertest.Sink{}
	done := make(chan error, 1)
	s.SetTakeover(func(p []byte) (int, func(context.Context, xfer.Conn) []byte) {
		at, o := zmodem.Find(p)
		if o == nil || o.Upload {
			return -1, nil
		}
		return at, func(ctx context.Context, c xfer.Conn) []byte {
			rest, err := zmodem.Receive(ctx, c, sink, zmodem.Options{})
			done <- err
			return rest
		}
	})
	s.Start()
	waitState(t, s, Connected)

	data := make([]byte, 300<<10)
	rand.New(rand.NewSource(1)).Read(data)
	src := &xfertest.Source{Items: []xfertest.Item{{File: xfer.File{Name: "f.bin"}, Data: data}}}
	server.Write([]byte("$ sz f.bin\r\n**\x18B00000000000000\r\x8a\x11"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := zmodem.Send(ctx, server, src, zmodem.Options{}); err != nil {
		t.Fatal(err)
	}
	server.Write([]byte("\r\n$ done"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the receiver did not end")
	}
	waitText(t, s, "$ done")
	// The receiver ended: nothing writes the sink now.
	if got := sink.Files["f.bin"]; !bytes.Equal(got, data) {
		t.Fatalf("received %d bytes", len(got))
	}
	screen := s.Term.ScreenText()
	if !strings.Contains(screen, "$ sz f.bin") || strings.Contains(screen, "B0000") || strings.Contains(screen, "OO") {
		t.Fatalf("screen %q", screen)
	}
}
