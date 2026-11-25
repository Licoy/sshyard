//go:build !windows

package session

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/vt"
)

type drainingPTY struct {
	closed, resume chan struct{}
	once           sync.Once
	read           bool
}

func (p *drainingPTY) Read(b []byte) (int, error) {
	if p.read {
		return 0, io.EOF
	}
	<-p.closed
	<-p.resume
	p.read = true
	return copy(b, "\xd6"), nil
}
func (*drainingPTY) Write(p []byte) (int, error) { return len(p), nil }
func (p *drainingPTY) Close() error              { p.once.Do(func() { close(p.closed) }); return nil }
func (*drainingPTY) Wait() error                 { return nil }
func (*drainingPTY) Resize(int, int) error       { return nil }

func TestLocalExitDrainsOutputBeforeNextStream(t *testing.T) {
	p := &drainingPTY{closed: make(chan struct{}), resume: make(chan struct{})}
	previous := startPTY
	t.Cleanup(func() { startPTY = previous })
	startPTY = func(Shell, string, int, int, string) (localPTY, error) { return p, nil }
	defer close(p.resume)
	s := New(Config{}, Options{Local: true}, 80, 24)
	defer s.Close()
	s.Term.SetOutputEncoding(vt.EncodingGBK)
	s.Term.WriteString("old")
	if err := s.connectLocal(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.serveLocal(context.Background(), p) }()
	<-p.closed
	select {
	case err := <-done:
		t.Fatalf("local serve ended before output copy drained: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	p.resume <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local output did not drain")
	}
	s.Term.ResetOutputStream()
	s.Term.WriteString("@\xd6\xd0")
	if text := s.Term.ScreenText(); text != "old�@中" {
		t.Fatalf("old output crossed new stream boundary: %q", text)
	}
}

type encodingPTY struct{ *strings.Reader }

func (*encodingPTY) Write(p []byte) (int, error) { return len(p), nil }
func (*encodingPTY) Close() error                { return nil }
func (*encodingPTY) Wait() error                 { return nil }
func (*encodingPTY) Resize(int, int) error       { return nil }

func TestLocalOutputEncodingStream(t *testing.T) {
	for _, tc := range []struct {
		encoding             vt.OutputEncoding
		fragment, wire, want string
	}{
		{vt.EncodingGBK, "\xd6", "\xd6\xd0", "中"},
		{vt.EncodingGB18030, "\x81\x30\x81", "\x90\x30\x81\x30", "𐀀"},
		{vt.EncodingBig5, "\x88", "\x88\x62", "Ê\u0304"},
		{vt.EncodingShiftJIS, "\x93", "\x93\xfa\xb6", "日ｶ"},
		{vt.EncodingEUCJP, "\x8f\xa2", "\x8f\xa2\xaf", "˘"},
		{vt.EncodingEUCKR, "\xc7", "\xc7\xd1", "한"},
		{vt.EncodingWindows1252, "", "\x80", "€"},
	} {
		t.Run(string(tc.encoding), func(t *testing.T) {
			previous := startPTY
			t.Cleanup(func() { startPTY = previous })
			startPTY = func(Shell, string, int, int, string) (localPTY, error) {
				return &encodingPTY{strings.NewReader("@" + tc.wire)}, nil
			}
			s := New(Config{}, Options{Local: true}, 80, 24)
			defer s.Close()
			if err := s.Term.SetOutputEncoding(tc.encoding); err != nil {
				t.Fatal(err)
			}
			s.Term.WriteString("old" + tc.fragment)
			if err := s.connectLocal(); err != nil {
				t.Fatal(err)
			}
			want := "old"
			if tc.fragment != "" {
				want += "�"
			}
			waitText(t, s, want+"@"+tc.want)
			if s.Term.OutputEncoding() != tc.encoding {
				t.Fatal("local shell changed encoding selection")
			}
		})
	}
}
