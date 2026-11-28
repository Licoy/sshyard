package rdp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

func TestRDPSessionCertificateTrustReconnect(t *testing.T) {
	cert := handshakeTestCert(t)
	known, err := OpenKnownCerts(filepath.Join(t.TempDir(), KnownCertsFile))
	if err != nil {
		t.Fatal(err)
	}
	cfg, password := sessionSecurityTestConfig(known.Verify)
	finish := make(chan struct{})
	defer close(finish)
	results := make(chan error, 2)
	var dials atomic.Int32
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		attempt := dials.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			transport, err := openTestDesktop(server, cert, cfg, protocolHybridEx, password, false)
			if attempt == 1 {
				var handshakeErr *net.OpError
				if !errors.As(err, &handshakeErr) || handshakeErr.Op != "remote error" {
					results <- fmt.Errorf("untrusted TLS connection was not rejected by the client: %v", err)
					return
				}
				results <- nil
				return
			}
			if err != nil {
				results <- err
				return
			}
			results <- nil
			<-finish
			_ = transport.Close()
		}()
		return client, nil
	}
	s := New(cfg, Options{AutoReconnect: true})
	defer s.Close()
	changes := changesForTest(s)
	s.Start()
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Failed })
	failed := s.Status()
	var pin *CertError
	if !errors.As(failed.Err, &pin) || pin.Fingerprint != Fingerprint(cert.Leaf) || pin.Addr != cfg.Addr || pin.Known != "" ||
		failed.Step != StepTLS || Retryable(failed.Err) || dials.Load() != 1 {
		t.Fatalf("unexpected trust denial: status=%+v dials=%d", failed, dials.Load())
	}
	awaitSecurityTestServer(t, results)
	if err := known.Trust(pin.Addr, pin.Fingerprint); err != nil {
		t.Fatal(err)
	}
	s.Reconnect()
	awaitSession(t, s, changes, func(st Status) bool { return st.State == Connected })
	awaitSecurityTestServer(t, results)
	if st := s.Status(); st.Step != StepReady || st.Connects != 1 || dials.Load() != 2 {
		t.Fatalf("unexpected status after trusting the same certificate: status=%+v dials=%d", st, dials.Load())
	}
}

func TestRDPSessionNLAAuthReasons(t *testing.T) {
	cert := handshakeTestCert(t)
	for _, tc := range []struct {
		name   string
		code   uint32
		reason Reason
		early  bool
	}{
		{"denied logon", 0xc000006d, ReasonLogonFailure, false},
		{"locked account", 0xc0000234, ReasonAccountLocked, false},
		{"expired password", 0xc0000071, ReasonPasswordExpired, false},
		{"password must change", 0xc0000224, ReasonPasswordMustChange, false},
		{"logon type forbidden", 0xc000015b, ReasonLogonTypeNotGranted, false},
		{"early authorization denied", 5, ReasonServerDenied, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, password := sessionSecurityTestConfig(func(addr string, chain []*x509.Certificate) error {
				if addr != "test.example:3389" || len(chain) != 1 || !bytes.Equal(chain[0].Raw, cert.Leaf.Raw) {
					return errors.New("unexpected TLS identity in NLA test")
				}
				return nil
			})
			results := make(chan error, 1)
			var dials atomic.Int32
			cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					results <- serveSecurityTestAuthFailure(server, cert, cfg, password, tc.code, tc.early)
				}()
				return client, nil
			}
			s := New(cfg, Options{AutoReconnect: true})
			defer s.Close()
			changes := changesForTest(s)
			s.Start()
			awaitSession(t, s, changes, func(st Status) bool { return st.State == Failed })
			st := s.Status()
			var rdpErr *Error
			if !errors.As(st.Err, &rdpErr) || rdpErr.Kind != KindAuth || rdpErr.Reason != tc.reason || rdpErr.Code != tc.code ||
				st.Step != StepAuth || st.Connects != 0 || Retryable(st.Err) || dials.Load() != 1 {
				t.Fatalf("wrong NLA failure: status=%+v dials=%d", st, dials.Load())
			}
			awaitSecurityTestServer(t, results)
		})
	}
}

func sessionSecurityTestConfig(verify func(string, []*x509.Certificate) error) (Config, []uint16) {
	const password = "Password" // MS-NLMP 4.2 public test value.
	return Config{
		Addr: "test.example:3389", User: "User", Domain: "Domain", ClientName: "SSHYard", Security: SecurityNLA,
		Width: 640, Height: 480, VerifyCert: verify, Timeout: 3 * time.Second,
		Password: func(context.Context) (string, error) { return password, nil },
	}, utf16.Encode([]rune(password))
}

func serveSecurityTestAuthFailure(conn net.Conn, cert tls.Certificate, cfg Config, password []uint16, code uint32, early bool) error {
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	req, _, err := readRDPFrame(conn, false)
	if err != nil || len(req) != 15 || req[1] != 0xe0 || binary.LittleEndian.Uint32(req[11:]) != 0xb {
		return fmt.Errorf("invalid NLA negotiation request: %v", err)
	}
	if err := writeTPKT(conn, []byte{14, 0xd0, 0, 0, 0, 0, 0, 2, 0, 8, 0, 8, 0, 0, 0}); err != nil {
		return err
	}
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err := secure.Handshake(); err != nil {
		return err
	}
	if early {
		// Run CredSSP without the helper's authorization success result.
		if err := serveTestCredSSP(secure, cert.Leaf, cfg, password, 6, protocolHybrid, false); err != nil {
			return err
		}
		err = writeRDP(secure, binary.LittleEndian.AppendUint32(nil, code))
	} else {
		err = rejectSecurityTestCredSSP(secure, code)
	}
	if err != nil {
		return err
	}
	var extra [1]byte
	if n, err := secure.Read(extra[:]); n != 0 || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, net.ErrClosed)) {
		return fmt.Errorf("client continued after NLA rejection: %v", err)
	}
	return nil
}

func rejectSecurityTestCredSSP(conn *tls.Conn, code uint32) error {
	msg, err := readTSRequest(conn)
	if err != nil || len(msg.Tokens) != 1 || msg.AuthInfo != nil {
		return fmt.Errorf("invalid initial CredSSP request: %v", err)
	}
	if err := sendTSRequest(conn, tsRequest{Version: 6, Tokens: []tsNegoToken{{handshakeTestChallenge()}}}); err != nil {
		return err
	}
	msg, err = readTSRequest(conn)
	if err != nil || len(msg.Tokens) != 1 || msg.PubKeyAuth == nil || msg.AuthInfo != nil {
		return fmt.Errorf("invalid CredSSP authenticate request: %v", err)
	}
	status, err := asn1.Marshal(int64(code))
	if err != nil {
		return err
	}
	return writeRDP(conn, spnegoDER(0x30, append([]byte{0xa0, 3, 2, 1, 6}, spnegoDER(0xa4, status)...)))
}

func awaitSecurityTestServer(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("security test server did not finish")
	}
}
