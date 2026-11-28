package rdp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestRDPResizeReconnectCookie(t *testing.T) {
	testSessionARC(t, true)
}

func TestRDPNetworkReconnectCookie(t *testing.T) {
	testSessionARC(t, false)
}

func testSessionARC(t *testing.T, resize bool) {
	t.Helper()
	cert := handshakeTestCert(t)
	serverErrors := make(chan error, 3)
	secondDial := make(chan time.Time, 1)
	secondReady := make(chan struct{})
	var attempts, passwordCalls atomic.Int32
	var drop chan struct{}
	width, height, minDelay := 800, 600, 300*time.Millisecond
	if !resize {
		drop = make(chan struct{})
		width, height, minDelay = 640, 480, time.Second
	}
	cp := arcCookie{id: 73, random: [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}}
	wantARC := handshakeHex(t, "1c0000000100000049000000b639c8731638618b707972aa6e96cf90")
	cfg := Config{Addr: "test.example:3389", User: "operator", Security: SecurityTLS, Width: 640, Height: 480}
	cfg.Password = func(context.Context) (string, error) {
		passwordCalls.Add(1)
		return "fixture-password", nil
	}
	cfg.VerifyCert = func(_ string, chain []*x509.Certificate) error {
		if len(chain) != 1 || !bytes.Equal(chain[0].Raw, cert.Leaf.Raw) {
			return errors.New("unexpected test TLS peer")
		}
		return nil
	}
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		attempt := attempts.Add(1)
		if attempt == 2 {
			secondDial <- time.Now()
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			if attempt == 1 {
				serverErrors <- serveCookieSeed(server, cert, cfg, cp, drop)
				return
			}
			next := cfg
			next.Width, next.Height = width, height
			peer, err := openTestDesktopCheckInfo(server, cert, next, protocolTLS, nil, true, func(info []byte) error {
				if binary.LittleEndian.Uint16(info[16:]) != 0 || binary.LittleEndian.Uint32(info[8:])&8 != 0 ||
					!bytes.Equal(infoARC(info), wantARC) {
					return errors.New("TLS reconnect did not offer the ARC verifier without a password")
				}
				return nil
			})
			if err == nil {
				close(secondReady)
				_, err = peer.readMCS()
				var e *Error
				if errors.As(err, &e) && e.Code == 3 {
					err = nil
				}
			}
			serverErrors <- err
		}()
		return client, nil
	}
	s := New(cfg, Options{AutoReconnect: !resize})
	defer s.Close()
	changes := changesForTest(s)
	s.Start()
	awaitSession(t, s, changes, func(st Status) bool {
		pos, ok := s.PointerPosition()
		return st.State == Connected && ok && pos == image.Pt(40, 50)
	})
	before := time.Now()
	if resize {
		s.Resize(640, 480, 120)
		s.Resize(width+1, height, 120)
	} else {
		close(drop)
		awaitSession(t, s, changes, func(st Status) bool { return st.State == Reconnecting && st.Attempt == 1 })
	}
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Connected && st.Connects == 2 })
	select {
	case start := <-secondDial:
		if start.Sub(before) < minDelay {
			t.Fatalf("reconnect dispatched before its minimum delay: %s", start.Sub(before))
		}
	default:
		t.Fatal("second dial not observed")
	}
	if st := s.Status(); st.Width != width || st.Height != height || attempts.Load() != 2 || passwordCalls.Load() != 2 {
		t.Fatalf("reconnect status %+v, dials=%d password callbacks=%d", st, attempts.Load(), passwordCalls.Load())
	}
	if _, ok := s.PointerPosition(); ok {
		t.Fatal("pointer position survived transport replacement")
	}
	select {
	case <-secondReady:
	case <-time.After(3 * time.Second):
		t.Fatal("resized connection did not send the lock synchronization event")
	}
	s.Close()
	_, version, _ := s.CopyFrame(nil, 0)
	status := s.Status()
	s.Close()
	if _, next, _ := s.CopyFrame(nil, 0); next != version || s.Status() != status {
		t.Fatal("repeated Close changed session state")
	}
	for range 2 {
		select {
		case err := <-serverErrors:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("resized test session did not close")
		}
	}
}

func serveCookieSeed(raw net.Conn, cert tls.Certificate, cfg Config, cp arcCookie, drop <-chan struct{}) error {
	peer, err := openTestDesktopCheckInfo(raw, cert, cfg, protocolTLS, nil, true, func(info []byte) error {
		if binary.LittleEndian.Uint16(info[16:]) == 0 || binary.LittleEndian.Uint32(info[8:])&8 == 0 || len(infoARC(info)) != 0 {
			return errors.New("initial TLS Info did not authenticate with the password")
		}
		return nil
	})
	if err != nil {
		return err
	}
	le := binary.LittleEndian
	p := le.AppendUint32(nil, 3)              // INFOTYPE_LOGON_EXTENDED_INFO
	p = le.AppendUint16(p, 6+4+arcPacketSize) // As in the MS-RDPBCGR 4.3.3 sample, excludes padding.
	p = le.AppendUint32(p, 1)                 // LOGON_EX_AUTORECONNECTCOOKIE
	p = le.AppendUint32(p, arcPacketSize)
	p = le.AppendUint32(p, arcPacketSize)
	p = le.AppendUint32(p, 1)
	p = le.AppendUint32(p, cp.id)
	p = append(p, cp.random[:]...)
	p = append(p, make([]byte, 570)...)
	if err := testServerShare(peer, dataSaveSession, p); err != nil {
		return err
	}
	if err := testServerShare(peer, dataPointer, words(3, 0, 40, 50)); err != nil {
		return err
	}
	if drop != nil {
		select {
		case <-drop:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("test did not drop the original TCP connection")
		}
	}
	if _, err := peer.readMCS(); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("original TLS session did not close for resize: %v", err)
	}
	return nil
}

func infoARC(info []byte) []byte {
	le := binary.LittleEndian
	off := 22
	for i := range 5 {
		off += int(le.Uint16(info[12+2*i:])) + 2
	}
	off += 2 // address family
	for range 2 {
		if off+2 > len(info) {
			return nil
		}
		off += 2 + int(le.Uint16(info[off:]))
	}
	off += 172 + 4 + 4
	if off+2 > len(info) {
		return nil
	}
	n := int(le.Uint16(info[off:]))
	if off+2+n > len(info) {
		return nil
	}
	return info[off+2 : off+2+n]
}

func TestRDPFrameDirtyVersions(t *testing.T) {
	s := New(Config{}, Options{})
	run := &sessionRun{}
	s.run = run
	s.resizeDesktop(run, 200, 200)
	dst, version, full := s.CopyFrame(nil, 0)
	if dst == nil || full != image.Rect(0, 0, 200, 200) {
		t.Fatalf("initial frame bounds %v", full)
	}
	patch := image.NewRGBA(image.Rect(0, 0, 2, 1))
	copy(patch.Pix, []byte{1, 2, 3, 255, 250, 250, 250, 255})
	for _, point := range []image.Point{image.Pt(10, 20), image.Pt(12, 21)} {
		if err := s.bitmapPatch(run, image.Rectangle{Min: point, Max: point.Add(image.Pt(1, 1))}, patch); err != nil {
			t.Fatal(err)
		}
	}
	dst, next, dirty := s.CopyFrame(dst, version)
	if dirty != image.Rect(10, 20, 13, 22) || next != version+2 || dst.Pix[dst.PixOffset(10, 20)] != 1 || dst.Pix[dst.PixOffset(11, 20)] != 0 {
		t.Fatalf("partial frame version=%d, dirty=%v", next, dirty)
	}
	if same, v, rect := s.CopyFrame(dst, next); same != dst || v != next || !rect.Empty() {
		t.Fatalf("unchanged frame version=%d rect=%v", v, rect)
	}
	for i := range dirtyHistory + 1 {
		if err := s.bitmapPatch(run, image.Rect(i, 0, i+1, 1), patch); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, rect := s.CopyFrame(dst, next); rect != full {
		t.Fatalf("stale frame did not receive full copy: %v", rect)
	}
}

func TestRDPSessionInputState(t *testing.T) {
	s := New(Config{}, Options{})
	c := &activeConn{wake: make(chan struct{}, 1), input: inputFlagUnicode | inputFlagMouseX | inputFlagHWheel}
	s.run = &sessionRun{conn: c}
	s.Key(0x1e, false, true)
	if len(c.requests.input) != 0 {
		t.Fatal("idle session queued input")
	}
	s.status.State, s.status.Width, s.status.Height = Connected, 200, 200
	s.MouseMove(10, 10)
	s.MouseMove(-1, 201)
	s.Key(0x1d, true, true)
	s.CtrlAltDel()
	s.Key(pauseScan, false, true)
	s.Key(pauseScan, false, false)
	s.MouseButton(15, 16, ButtonX1, true)
	s.Wheel(20, 21, -120, 240)
	s.ReleaseAll()
	want := []wireInput{
		pointerInput(pointerMove, 0, 199, false), scanInput(0x1d, true, false, true),
		scanInput(0x38, false, false, true), scanInput(0x53, true, false, true), scanInput(0x53, true, false, false), scanInput(0x38, false, false, false),
		scanInput(0x1d, false, true, true), scanInput(0x45, false, false, true), scanInput(0x1d, false, true, false), scanInput(0x45, false, false, false),
		pointerInput(pointerDown|pointerXButton1, 15, 16, true),
		pointerInput(pointerWheel|240, 20, 21, false), pointerInput(pointerHWheel|392, 20, 21, false),
		scanInput(0x1d, true, false, false), pointerInput(pointerXButton1, 20, 21, true),
	}
	if !slices.Equal(c.requests.input, want) {
		t.Fatalf("queued events: %+v", c.requests.input)
	}
	if len(c.held.keys) != 0 || c.held.buttons != 0 {
		t.Fatal("release all retained held input")
	}
}

func TestRDPRetryPolicy(t *testing.T) {
	network := &Error{Kind: KindNetwork, Err: io.EOF}
	for retry, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30, 30, 30, 30} {
		if delay, ok := connectionRetry(network, true, retry); !ok || delay != want*time.Second {
			t.Fatalf("retry %d: %s, %t", retry, delay, ok)
		}
	}
	if _, ok := connectionRetry(network, true, 10); ok {
		t.Fatal("retry limit exceeded")
	}
	for _, err := range []error{
		&Error{Kind: KindNetwork, Err: context.Canceled},
		&Error{Kind: KindAuth, Reason: ReasonLogonFailure},
		&Error{Kind: KindCert},
		&Error{Kind: KindServer, Reason: ReasonLoggedOff},
	} {
		if _, ok := connectionRetry(err, true, 0); ok {
			t.Fatalf("retried %v", err)
		}
	}
}

func TestRDPSessionInitialDialFailure(t *testing.T) {
	var dials atomic.Int32
	cfg := Config{Addr: "test.example:3389"}
	cfg.VerifyCert = func(string, []*x509.Certificate) error { return nil }
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, io.EOF
	}
	s := New(cfg, Options{AutoReconnect: true})
	defer s.Close()
	changes := changesForTest(s)
	s.Start()
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Failed })
	if st := s.Status(); st.Connects != 0 || st.Width != 0 || st.Height != 0 || st.Attempt != 0 || dials.Load() != 1 {
		t.Fatalf("initial failure status %+v, dials=%d", st, dials.Load())
	}
	s.Start()
	if got := s.Status().State; got != Failed {
		t.Fatalf("Start restarted a failed session: %v", got)
	}
	s.Reconnect()
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Failed })
	if st := s.Status(); st.Attempt != 1 || dials.Load() != 2 {
		t.Fatalf("manual retry status %+v, dials=%d", st, dials.Load())
	}
}
