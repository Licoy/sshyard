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
	"testing"
	"time"
)

func TestRDPSessionTLSDesktop(t *testing.T) {
	cert := handshakeTestCert(t)
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprintf("fast_input_%t", fast), func(t *testing.T) {
			serverErr := make(chan error, 1)
			inputSeen := make(chan struct{})
			visibilitySeen := make(chan bool, 2)
			cfg := Config{Addr: "test.example:3389", User: "operator", Security: SecurityTLS, Width: 640, Height: 480}
			cfg.VerifyCert = func(addr string, chain []*x509.Certificate) error {
				if addr != cfg.Addr || len(chain) != 1 || !bytes.Equal(chain[0].Raw, cert.Leaf.Raw) {
					return errors.New("unexpected TLS peer")
				}
				return nil
			}
			cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					serverErr <- serveTestDesktop(server, cert, cfg, fast, inputSeen, visibilitySeen)
				}()
				return client, nil
			}
			s := New(cfg, Options{AutoReconnect: true})
			defer s.Close()
			changes := changesForTest(s)
			s.Start()
			awaitSession(t, s, changes, func(st Status) bool { return st.State == Connected })
			if st := s.Status(); st.Step != StepReady || st.Connects != 1 || st.Attempt != 0 || st.Width != 640 || st.Height != 480 {
				t.Fatalf("connected status %+v", st)
			}
			awaitSession(t, s, changes, func(Status) bool {
				position, ok := s.PointerPosition()
				return ok && position == image.Pt(33, 44)
			})
			frame, version, full := s.CopyFrame(nil, 0)
			if frame == nil || version == 0 || full != image.Rect(0, 0, 640, 480) || frame.Pix[3] != 255 ||
				!bytes.Equal(frame.Pix[frame.PixOffset(10, 15):][:4], []byte{255, 0, 0, 255}) {
				t.Fatalf("desktop version=%d rect=%v", version, full)
			}
			for _, visible := range []bool{false, true} {
				s.SetVisible(visible)
				select {
				case got := <-visibilitySeen:
					if got != visible {
						t.Fatalf("server saw visibility %t, expected %t", got, visible)
					}
				case err := <-serverErr:
					t.Fatalf("server before visibility update: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("server did not receive a visibility change")
				}
			}
			s.Key(0x1e, false, true)
			select {
			case <-inputSeen:
			case err := <-serverErr:
				t.Fatalf("server before input: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("server did not receive a key event")
			}
			awaitSession(t, s, changes, func(st Status) bool { return st.State == Disconnected })
			if kind, reason := Classify(s.Status().Err); kind != KindServer || reason != ReasonLoggedOff || s.Status().Attempt != 0 {
				t.Fatalf("final status %+v, classified %d %d", s.Status(), kind, reason)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

const testDesktopShareID = 0x103ea

func serveTestDesktop(raw net.Conn, cert tls.Certificate, cfg Config, fast bool, inputSeen chan<- struct{}, visibilitySeen chan<- bool) error {
	t, err := openTestDesktop(raw, cert, cfg, protocolTLS, nil, fast)
	if err != nil {
		return err
	}
	// An aligned two-pixel bitmap for a one-pixel destination, with opaque red output.
	bitmap := append(words(1, 1, 10, 15, 10, 15, 2, 1, 32, 0, 8), 0, 0, 255, 0, 0, 255, 0, 0)
	if err := testServerShare(t, dataUpdate, bitmap); err != nil {
		return err
	}
	if err := testServerShare(t, dataPointer, words(3, 0, 33, 44)); err != nil {
		return err
	}
	for _, visible := range []bool{false, true} {
		if err := testExpectClientShare(t, dataSuppress, desktopRectPDU(visible, cfg.Width, cfg.Height)); err != nil {
			return err
		}
		if visible {
			if err := testExpectClientShare(t, dataRefreshRect, desktopRectPDU(true, cfg.Width, cfg.Height)); err != nil {
				return err
			}
		}
		visibilitySeen <- visible
	}
	if err := testExpectInput(t, fast, []wireInput{scanInput(0x1e, false, false, true)}); err != nil {
		return err
	}
	close(inputSeen)
	return testServerShare(t, dataErrorInfo, []byte{0x0c, 0, 0, 0})
}

func openTestDesktop(raw net.Conn, cert tls.Certificate, cfg Config, protocol uint32, password []uint16, fast bool) (*rdpTransport, error) {
	return openTestDesktopCheckInfo(raw, cert, cfg, protocol, password, fast, nil)
}

func openTestDesktopCheckInfo(raw net.Conn, cert tls.Certificate, cfg Config, protocol uint32, password []uint16, fast bool, infoCheck func([]byte) error) (*rdpTransport, error) {
	if err := raw.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	req, _, err := readRDPFrame(raw, false)
	if err != nil || len(req) != 15 || req[1] != 0xe0 || req[7] != 1 || binary.LittleEndian.Uint32(req[11:]) != requestedProtocols(cfg.Security) {
		return nil, fmt.Errorf("invalid server-side X.224 negotiation request: %v", err)
	}
	confirm := []byte{14, 0xd0, 0, 0, 0x12, 0x34, 0, 2, 0, 8, 0}
	if err := writeTPKT(raw, binary.LittleEndian.AppendUint32(confirm, protocol)); err != nil {
		return nil, err
	}
	secure := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err := secure.Handshake(); err != nil {
		return nil, err
	}
	if protocol != protocolTLS {
		if err := serveTestCredSSP(secure, cert.Leaf, cfg, password, 6, protocol, false); err != nil {
			return nil, err
		}
	}
	t := &rdpTransport{Conn: secure, userID: 1006, globalID: 1003, protocol: protocol}
	if err := testDesktopMCS(t, cfg); err != nil {
		return nil, err
	}
	if err := testDesktopActivation(t, cfg, fast, infoCheck); err != nil {
		return nil, err
	}
	return t, nil
}

func testDesktopMCS(t *rdpTransport, cfg Config) error {
	p, err := t.readMCS()
	if err != nil || len(p) < 3 || !bytes.Equal(p[:2], []byte{0x7f, 0x65}) || !bytes.Contains(p, []byte("Duca")) {
		return fmt.Errorf("missing server-side MCS Connect Initial: %v", err)
	}
	core := bytes.Index(p, []byte{1, 0xc0, 0xea, 0})
	if core < 0 || len(p)-core < 12 || int(binary.LittleEndian.Uint16(p[core+8:])) != cfg.Width || int(binary.LittleEndian.Uint16(p[core+10:])) != cfg.Height {
		return errors.New("unexpected desktop size in Client Core Data")
	}
	ids := []uint16{1004}
	if cfg.Clipboard {
		ids = append(ids, 1005)
	}
	if err := t.writeMCS(testGCCResponse(requestedProtocols(cfg.Security), true, ids)); err != nil {
		return err
	}
	for _, want := range [][]byte{{4, 1, 0, 1, 0}, {0x28}} {
		if p, err := t.readMCS(); err != nil || !bytes.Equal(p, want) {
			return fmt.Errorf("unexpected MCS setup packet %x: %v", p, err)
		}
	}
	return t.writeMCS([]byte{0x2e, 0, 0, 5})
}

func testDesktopActivation(t *rdpTransport, cfg Config, fast bool, infoCheck func([]byte) error) error {
	_, info, err := testClientData(t)
	if err != nil || len(info) < 22 || binary.LittleEndian.Uint16(info) != 0x40 {
		return fmt.Errorf("missing server-side Client Info PDU: %v", err)
	}
	if t.protocol != protocolTLS && binary.LittleEndian.Uint16(info[16:]) != 0 {
		return errors.New("client repeated CredSSP password in Client Info")
	}
	if infoCheck != nil {
		if err := infoCheck(info); err != nil {
			return err
		}
	}
	validClient := testLicensePacket(0xff, []byte{7, 0, 0, 0, 2, 0, 0, 0, 4, 0, 0, 0})
	if err := testServerData(t, t.globalID, validClient); err != nil {
		return err
	}
	if err := testServerData(t, t.globalID, testDemandActive(cfg.Width, cfg.Height, fast)); err != nil {
		return err
	}
	_, p, err := testClientData(t)
	if err != nil || len(p) < 24 || p[2] != 0x13 || binary.LittleEndian.Uint32(p[6:]) != testDesktopShareID {
		return fmt.Errorf("invalid Confirm Active PDU: %v", err)
	}
	for _, want := range []struct {
		kind byte
		body []byte
	}{{dataSynchronize, words(1, 1002)}, {dataControl, words(4, 0, 0, 0)}, {dataControl, words(1, 0, 0, 0)}, {dataFontList, words(0, 0, 3, 50)}} {
		if err := testExpectClientShare(t, want.kind, want.body); err != nil {
			return err
		}
	}
	for _, p := range [][]byte{
		dataPDU(serverChannelID, testDesktopShareID, dataSynchronize, words(1, t.userID)),
		dataPDU(serverChannelID, testDesktopShareID, dataControl, words(4, 0, 0, 0)),
		dataPDU(serverChannelID, testDesktopShareID, dataControl, words(2, t.userID, 0, 0)),
		dataPDU(serverChannelID, testDesktopShareID, dataFontMap, words(0, 0, 3, 4)),
	} {
		if err := testServerData(t, t.globalID, p); err != nil {
			return err
		}
	}
	return testExpectInput(t, fast, []wireInput{syncInput(0)})
}

func testDemandActive(width, height int, fast bool) []byte {
	input := uint16(inputFlagScancode | inputFlagUnicode | inputFlagMouseX | inputFlagHWheel)
	if fast {
		input |= inputFlagFast2
	}
	general := words(capGeneral, 24, 0, 0, 0x0200, 0, 0, 9, 0, 0, 0, 0x0101)
	bitmap := words(capBitmap, 28, 32, 1, 1, 1, uint16(width), uint16(height), 0, 1, 1, 0, 1, 0)
	inputSet := append(words(capInput, 88, input), make([]byte, 82)...)
	p := binary.LittleEndian.AppendUint32(nil, testDesktopShareID)
	p = append(p, words(4, uint16(4+len(general)+len(bitmap)+len(inputSet)))...)
	p = append(p, 'R', 'D', 'P', 0, 3, 0, 0, 0)
	p = append(append(append(p, general...), bitmap...), inputSet...)
	return controlPDU(serverChannelID, shareDemandActive, append(p, 0, 0, 0, 0))
}

func testClientData(t *rdpTransport) (uint16, []byte, error) {
	p, err := t.readMCS()
	if err != nil {
		return 0, nil, err
	}
	if len(p) < 7 || p[0] != 0x64 {
		return 0, nil, errors.New("missing client MCS Send Data Request")
	}
	data, ok := takeMCSData(p[6:])
	if !ok {
		return 0, nil, errors.New("invalid test MCS client payload")
	}
	return binary.BigEndian.Uint16(p[3:]), data, nil
}

func testServerData(t *rdpTransport, channel uint16, data []byte) error {
	p := binary.BigEndian.AppendUint16([]byte{0x68, 0, 1}, channel)
	p = appendMCSLength(append(p, 0x70), len(data))
	return t.writeMCS(append(p, data...))
}

func testServerShare(t *rdpTransport, kind byte, data []byte) error {
	return testServerData(t, t.globalID, dataPDU(serverChannelID, testDesktopShareID, kind, data))
}

func testExpectClientShare(t *rdpTransport, kind byte, data []byte) error {
	channel, p, err := testClientData(t)
	if err != nil {
		return err
	}
	if channel != t.globalID || len(p) != 18+len(data) || p[2] != 0x17 || p[14] != kind || !bytes.Equal(p[18:], data) {
		return fmt.Errorf("invalid client Share Data PDU, expected type %#x", kind)
	}
	return nil
}

func testExpectInput(t *rdpTransport, fast bool, events []wireInput) error {
	if fast {
		want, err := encodeFastInput(events)
		if err != nil {
			return err
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(t, got); err != nil || !bytes.Equal(got, want) {
			return fmt.Errorf("unexpected fast-path input %x: %v", got, err)
		}
		return nil
	}
	want, err := encodeSlowInput(events)
	if err != nil {
		return err
	}
	return testExpectClientShare(t, dataInput, want)
}

func changesForTest(s *Session) chan struct{} {
	changes := make(chan struct{}, 1)
	s.OnChange = func() {
		_ = s.Status() // The subscriber may read state without a mutex held by Session.
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	return changes
}

func awaitSession(t *testing.T, s *Session, changes <-chan struct{}, predicate func(Status) bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		status := s.Status()
		if predicate(status) {
			return
		}
		if status.State == Failed {
			t.Fatalf("session failed: %v", status.Err)
		}
		select {
		case <-changes:
		case <-timer.C:
			t.Fatalf("session state did not arrive, last status %+v", s.Status())
		}
	}
}
