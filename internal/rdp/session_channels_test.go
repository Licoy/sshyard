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
	"image/color"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestRDPSessionClipboardAndDisplayReactivation(t *testing.T) {
	cert := handshakeTestCert(t)
	serverErr := make(chan error, 1)
	incoming := make(chan string, 1)
	localSeen := make(chan struct{})
	reactivate := make(chan struct{})
	finished, finish := context.WithCancel(context.Background())
	defer finish()
	var dials atomic.Int32
	cfg := Config{
		Addr: "test.example:3389", User: "operator", Security: SecurityTLS,
		Width: 640, Height: 480, Clipboard: true,
	}
	cfg.VerifyCert = func(addr string, chain []*x509.Certificate) error {
		if addr != cfg.Addr || len(chain) != 1 || !bytes.Equal(chain[0].Raw, cert.Leaf.Raw) {
			return errors.New("unexpected TLS peer")
		}
		return nil
	}
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		if dials.Add(1) != 1 {
			return nil, errors.New("display resize opened a new connection")
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			serverErr <- testChannelDesktop(server, cert, cfg, localSeen, reactivate, finished.Done())
		}()
		return client, nil
	}

	s := New(cfg, Options{})
	defer s.Close()
	s.OnClipboard = func(text string) { incoming <- text }
	changes := changesForTest(s)
	s.Start()
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Connected })
	before, version, _ := s.CopyFrame(nil, 0)
	if before == nil || before.Bounds() != image.Rect(0, 0, 640, 480) {
		t.Fatalf("initial frame = %v", before)
	}
	select {
	case text := <-incoming:
		if text != "remote\n世界" {
			t.Fatalf("remote clipboard = %q", text)
		}
	case err := <-serverErr:
		t.Fatalf("server before incoming clipboard: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server clipboard did not arrive")
	}

	s.SetClipboard("local 😀")
	select {
	case <-localSeen:
	case err := <-serverErr:
		t.Fatalf("server before outgoing clipboard: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive local clipboard")
	}
	s.Resize(960, 540, 140)
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Connecting && st.Step == StepCapabilities })
	close(reactivate)

	awaitSession(t, s, changes, func(st Status) bool {
		return st.State == Connected && st.Width == 960 && st.Height == 540
	})
	wantFrame := image.Rect(0, 0, 960, 540)
	awaitSession(t, s, changes, func(Status) bool {
		frame, newVersion, dirty := s.CopyFrame(before, version)
		return frame != nil && dirty == wantFrame && frame.Bounds() == wantFrame && newVersion > version &&
			frame.RGBAAt(900, 500) == (color.RGBA{G: 255, A: 255})
	})
	if status := s.Status(); status.Connects != 1 || dials.Load() != 1 {
		t.Fatalf("in-place resize opened a new session: %+v", status)
	}
	finish()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func testChannelDesktop(raw net.Conn, cert tls.Certificate, cfg Config, localSeen chan<- struct{}, reactivate, finished <-chan struct{}) error {
	t, err := openTestDesktop(raw, cert, cfg, protocolTLS, nil, false)
	if err != nil {
		return err
	}
	if err := testDisplayChannelOpen(t); err != nil {
		return err
	}
	if err := testClipboardBothDirections(t); err != nil {
		return err
	}
	close(localSeen)
	// DVC Data, DisplayControl Monitor Layout, one primary 960x540 at 140%.
	const dvc = 1004
	want := []byte{0x30, 0x27, 2, 0, 0, 0, 56, 0, 0, 0, 40, 0, 0, 0, 1, 0, 0, 0,
		1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xc0, 3, 0, 0, 0x1c, 2, 0, 0,
		181, 0, 0, 0, 102, 0, 0, 0, 0, 0, 0, 0, 140, 0, 0, 0, 140, 0, 0, 0}
	if err := testExpectChannelPayload(t, dvc, want); err != nil {
		return fmt.Errorf("resize: %w", err)
	}
	deactivate := binary.LittleEndian.AppendUint32(nil, testDesktopShareID)
	deactivate = append(deactivate, 4, 0, 'R', 'D', 'P', 0)
	if err := testServerData(t, t.globalID, controlPDU(serverChannelID, shareDeactivate, deactivate)); err != nil {
		return err
	}
	select {
	case <-reactivate:
	case <-finished:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("client did not report deactivation")
	}
	if err := testReactivateDesktop(t, 960, 540); err != nil {
		return err
	}
	if err := testServerPlanarUpdate(t); err != nil {
		return err
	}
	<-finished
	return nil
}

func testDisplayChannelOpen(t *rdpTransport) error {
	const dvc = 1004
	if err := testSendChannelPayload(t, dvc, []byte{0x50, 0, 1, 0}); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, dvc, []byte{0x50, 0, 1, 0}); err != nil {
		return err
	}
	create := append([]byte{0x10, 0x27}, []byte("Microsoft::Windows::RDS::DisplayControl\x00")...)
	if err := testSendChannelPayload(t, dvc, create); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, dvc, []byte{0x10, 0x27, 0, 0, 0, 0}); err != nil {
		return err
	}
	caps := []byte{0x30, 0x27, 5, 0, 0, 0, 20, 0, 0, 0, 1, 0, 0, 0, 0, 8, 0, 0, 0, 8, 0, 0}
	return testSendChannelPayload(t, dvc, caps)
}

func testClipboardBothDirections(t *rdpTransport) error {
	const clip = 1005
	caps := []byte{7, 0, 0, 0, 16, 0, 0, 0, 1, 0, 0, 0, 1, 0, 12, 0, 2, 0, 0, 0, 2, 0, 0, 0}
	if err := testSendChannelPayload(t, clip, caps); err != nil {
		return err
	}
	if err := testSendChannelPayload(t, clip, []byte{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, clip, caps); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, clip, []byte{2, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	offer := []byte{2, 0, 0, 0, 6, 0, 0, 0, 13, 0, 0, 0, 0, 0}
	ack := []byte{3, 0, 1, 0, 0, 0, 0, 0}
	request := []byte{4, 0, 0, 0, 4, 0, 0, 0, 13, 0, 0, 0}
	if err := testSendChannelPayload(t, clip, offer); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, clip, ack); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, clip, request); err != nil {
		return err
	}
	remote := []byte{5, 0, 1, 0, 22, 0, 0, 0, 'r', 0, 'e', 0, 'm', 0, 'o', 0, 't', 0, 'e', 0,
		'\r', 0, '\n', 0, 0x16, 0x4e, 0x4c, 0x75, 0, 0}
	if err := testSendChannelPayload(t, clip, remote); err != nil {
		return err
	}
	if err := testExpectChannelPayload(t, clip, offer); err != nil {
		return err
	}
	if err := testSendChannelPayload(t, clip, ack); err != nil {
		return err
	}
	if err := testSendChannelPayload(t, clip, request); err != nil {
		return err
	}
	local := []byte{5, 0, 1, 0, 18, 0, 0, 0, 'l', 0, 'o', 0, 'c', 0, 'a', 0, 'l', 0,
		' ', 0, 0x3d, 0xd8, 0, 0xde, 0, 0}
	return testExpectChannelPayload(t, clip, local)
}

func testReactivateDesktop(t *rdpTransport, width, height int) error {
	if err := testServerData(t, t.globalID, testDemandActive(width, height, false)); err != nil {
		return err
	}
	channel, p, err := testClientData(t)
	if err != nil || channel != t.globalID || len(p) < 24 || p[2] != 0x13 || binary.LittleEndian.Uint32(p[6:]) != testDesktopShareID {
		return fmt.Errorf("invalid reactivation Confirm Active PDU: %v", err)
	}
	for _, want := range []struct {
		kind byte
		body []byte
	}{
		{dataSynchronize, words(1, 1002)}, {dataControl, words(4, 0, 0, 0)},
		{dataControl, words(1, 0, 0, 0)}, {dataFontList, words(0, 0, 3, 50)},
	} {
		if err := testExpectClientShare(t, want.kind, want.body); err != nil {
			return err
		}
	}
	for _, send := range []struct {
		kind byte
		body []byte
	}{
		{dataSynchronize, words(1, t.userID)}, {dataControl, words(4, 0, 0, 0)},
		{dataControl, words(2, t.userID, 0, 0)}, {dataFontMap, words(0, 0, 3, 4)},
	} {
		if err := testServerShare(t, send.kind, send.body); err != nil {
			return err
		}
	}
	return testExpectInput(t, false, []wireInput{syncInput(0)})
}

func testServerPlanarUpdate(t *rdpTransport) error {
	// Four green pixels in an RDP 6.0 planar stream, clipped to one
	// pixel at a coordinate that was outside the old desktop.
	bitmap := words(1, 1, 900, 500, 900, 500, 4, 1, 32, 0x0401, 13)
	bitmap = append(bitmap, 0x20, 0, 0, 0, 0, 255, 255, 255, 255, 0, 0, 0, 0)
	fast := rdpTestFastUpdate(1, bitmap)
	return writeRDP(t, append([]byte{0, byte(len(fast) + 2)}, fast...))
}

func testSendChannelPayload(t *rdpTransport, channel uint16, data []byte) error {
	return testServerData(t, channel, rdpTestVCPacket(0x13, uint32(len(data)), data))
}

func testExpectChannelPayload(t *rdpTransport, channel uint16, want []byte) error {
	gotChannel, got, err := testClientData(t)
	expected := rdpTestVCPacket(0x13, uint32(len(want)), want)
	if err != nil || gotChannel != channel || !bytes.Equal(got, expected) {
		return fmt.Errorf("channel %d PDU = %x (%v), want channel %d PDU %x", gotChannel, got, err, channel, expected)
	}
	return nil
}
