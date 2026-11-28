package rdp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestHandshake(t *testing.T) {
	cert := handshakeTestCert(t)
	cases := []struct {
		name      string
		protocol  uint32
		version   int
		tlsMax    uint16
		skipJoin  bool
		badCert   bool
		badBind   bool
		wantKind  Kind
		wantDials int
	}{
		{"TLS Channel Join", protocolTLS, 0, tls.VersionTLS13, false, false, false, KindOther, 1},
		{"TLS 1.0 fallback", protocolTLS, 0, tls.VersionTLS10, true, false, false, KindOther, 2},
		{"TLS 1.1 fallback", protocolTLS, 0, tls.VersionTLS11, true, false, false, KindOther, 2},
		{"TLS certificate rejection", protocolTLS, 0, tls.VersionTLS13, false, true, false, KindCert, 1},
		{"CredSSP v2", protocolHybrid, 2, tls.VersionTLS12, false, false, false, KindOther, 1},
		{"CredSSP v3", protocolHybrid, 3, tls.VersionTLS12, false, false, false, KindOther, 1},
		{"CredSSP v4", protocolHybrid, 4, tls.VersionTLS12, false, false, false, KindOther, 1},
		{"CredSSP v5", protocolHybrid, 5, tls.VersionTLS12, false, false, false, KindOther, 1},
		{"CredSSP v6 HYBRID_EX", protocolHybridEx, 6, tls.VersionTLS13, true, false, false, KindOther, 1},
		{"CredSSP server binding rejection", protocolHybridEx, 6, tls.VersionTLS13, false, false, true, KindSecurity, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			dials, verifies := 0, 0
			serve := make(chan error, tc.wantDials)
			cfg := Config{Addr: "test.example:3389", Domain: "Domain", User: "User", ClientName: "SSHYard", Width: 1280, Height: 768, Scale: 120, Clipboard: true}
			password := utf16.Encode([]rune("Password")) // MS-NLMP 4.2 test vector credential.
			cfg.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials++
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					serve <- serveHandshake(server, cert, cfg, password, tc.protocol, tc.version, tc.tlsMax, tc.skipJoin, tc.badBind)
				}()
				return client, nil
			}
			cfg.VerifyCert = func(addr string, chain []*x509.Certificate) error {
				verifies++
				if addr != cfg.Addr || len(chain) != 1 || !bytes.Equal(chain[0].Raw, cert.Certificate[0]) {
					return errors.New("unexpected peer certificate or address")
				}
				if tc.badCert {
					return errors.New("test certificate policy denied")
				}
				return nil
			}
			rdp, err := openTransport(ctx, cfg, password, func(Step) {})
			if dials != tc.wantDials || verifies != 1 {
				t.Fatalf("dials=%d certificate checks=%d error=%v", dials, verifies, err)
			}
			if tc.wantKind != KindOther {
				if kind, _ := Classify(err); kind != tc.wantKind {
					t.Fatalf("expected failure kind %v, got %v: %v", tc.wantKind, kind, err)
				}
				if tc.badBind {
					if err := <-serve; err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer rdp.Close()
			if rdp.userID != 1006 || rdp.globalID != 1003 || rdp.channels["drdynvc"] != 1004 || rdp.channels["cliprdr"] != 1005 {
				t.Fatalf("bad channel assignment: user=%d global=%d channels=%v", rdp.userID, rdp.globalID, rdp.channels)
			}
			if err := rdp.disconnect(); err != nil {
				t.Fatal(err)
			}
			versionMismatches := 0
			for i := 0; i < tc.wantDials; i++ {
				if err := <-serve; err != nil {
					if !strings.HasPrefix(err.Error(), "tls: client offered only unsupported versions:") {
						t.Fatal(err)
					}
					versionMismatches++
				}
			}
			if versionMismatches != tc.wantDials-1 {
				t.Fatalf("expected %d rejected TLS versions, got %d", tc.wantDials-1, versionMismatches)
			}
		})
	}
}

func TestHandshakeMandatoryCertificate(t *testing.T) {
	_, err := openTransport(context.Background(), Config{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("unexpected dial without certificate callback")
			return nil, nil
		},
	}, nil, func(Step) {})
	if kind, _ := Classify(err); kind != KindCert {
		t.Fatalf("certificate callback check: %v", err)
	}
}

func handshakeTestCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"test.example"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func serveHandshake(conn net.Conn, cert tls.Certificate, cfg Config, password []uint16, protocol uint32, version int, tlsMax uint16, skipJoin, corruptBinding bool) error {
	req, _, err := readRDPFrame(conn, false)
	if err != nil || len(req) != 15 || req[1] != 0xe0 || req[7] != 1 || binary.LittleEndian.Uint32(req[11:]) != 0xb {
		return fmt.Errorf("invalid X.224 negotiation request: %x, %v", req, err)
	}
	confirm := []byte{14, 0xd0, 0, 0, 0x12, 0x34, 0, 2, 0, 8, 0}
	if err := writeTPKT(conn, binary.LittleEndian.AppendUint32(confirm, protocol)); err != nil {
		return err
	}
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS10, MaxVersion: tlsMax})
	if err := secure.Handshake(); err != nil {
		return err
	}
	if protocol != protocolTLS {
		if err := serveTestCredSSP(secure, cert.Leaf, cfg, password, version, protocol, corruptBinding); err != nil || corruptBinding {
			return err
		}
	}
	return serveTestMCS(secure, skipJoin)
}

func serveTestMCS(conn net.Conn, skip bool) error {
	t := &rdpTransport{Conn: conn}
	p, err := t.readMCS()
	if err != nil || len(p) < 3 || !bytes.Equal(p[:2], []byte{0x7f, 0x65}) || !bytes.Contains(p, []byte("Duca")) {
		return fmt.Errorf("missing MCS Connect Initial: %v", err)
	}
	if err := t.writeMCS(testGCCResponse(0xb, skip, []uint16{1004, 1005})); err != nil {
		return err
	}
	if p, err := t.readMCS(); err != nil || !bytes.Equal(p, []byte{4, 1, 0, 1, 0}) {
		return fmt.Errorf("bad Erect Domain Request: %x %v", p, err)
	}
	if p, err := t.readMCS(); err != nil || !bytes.Equal(p, []byte{0x28}) {
		return fmt.Errorf("bad Attach User Request: %x %v", p, err)
	}
	if err := t.writeMCS([]byte{0x2e, 0, 0, 5}); err != nil {
		return err
	}
	if !skip {
		for _, channel := range []uint16{1006, 1003, 1004, 1005} {
			p, err := t.readMCS()
			if err != nil || !bytes.Equal(p, binary.BigEndian.AppendUint16([]byte{0x38, 0, 5}, channel)) {
				return fmt.Errorf("bad Channel Join Request: %x %v", p, err)
			}
			if err := t.writeMCS(binary.BigEndian.AppendUint16(append([]byte{0x3e, 0}, p[1:]...), channel)); err != nil {
				return err
			}
		}
	}
	_, err = t.readMCS()
	var protocolErr *Error
	if !errors.As(err, &protocolErr) || protocolErr.Kind != KindServer || protocolErr.Code != 3 {
		return fmt.Errorf("missing Disconnect Provider Ultimatum: %v", err)
	}
	return nil
}

func testGCCResponse(requested uint32, skip bool, ids []uint16) []byte {
	core := binary.LittleEndian.AppendUint32([]byte{4, 0, 8, 0}, requested)
	caps := uint32(0)
	if skip {
		caps = 8
	}
	blocks := gccBlock(0x0c01, binary.LittleEndian.AppendUint32(core, caps))
	blocks = append(blocks, gccBlock(0x0c02, make([]byte, 8))...)
	network := binary.LittleEndian.AppendUint16([]byte{0xeb, 3}, uint16(len(ids)))
	for _, id := range ids {
		network = binary.LittleEndian.AppendUint16(network, id)
	}
	if len(ids)%2 == 1 {
		network = append(network, 0, 0)
	}
	blocks = append(blocks, gccBlock(0x0c03, network)...)
	conference := []byte{0, 5, 0, 0x14, 0x7c, 0, 1, 0x2a, 0x14, 0x76, 0x0a, 1, 1, 0, 1, 0xc0, 0, 'M', 'c', 'D', 'n'}
	conference = append(appendMCSLength(conference, len(blocks)), blocks...)
	p := append([]byte{0x0a, 1, 0, 2, 1, 0}, mcsDomainParameters(34, 3, 0, 65528)...)
	p = append(p, mcsBERField(4, conference)...)
	return append(appendMCSBERLength([]byte{0x7f, 0x66}, len(p)), p...)
}

func handshakeHex(t *testing.T, s string) []byte {
	t.Helper()
	p, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNTLM(t *testing.T) {
	key := ntlmResponseKey("User", "Domain", utf16.Encode([]rune("Password")))
	if !bytes.Equal(key, handshakeHex(t, "0c868a403bfd7a93a3001ef22ef02e3f")) {
		t.Fatalf("MS-NLMP 4.2.4 NTOWFv2 = %x", key)
	}
	nt, lm, session := ntlmV2Responses(key, handshakeHex(t, "0123456789abcdef"), bytes.Repeat([]byte{0xaa}, 8), make([]byte, 8),
		handshakeHex(t, "02000c0044006f006d00610069006e0001000c0053006500720076006500720000000000"))
	for name, vector := range map[string]struct {
		got  []byte
		want string
	}{
		"NT proof":         {nt[:16], "68cd0ab851e51c96aabc927bebef6a1c"},
		"LM response":      {lm, "86c35097ac9cec102554764a57cccc19aaaaaaaaaaaaaaaa"},
		"session base key": {session, "8de40ccadbc14a82f15cb0ad0de95ca3"},
	} {
		if !bytes.Equal(vector.got, handshakeHex(t, vector.want)) {
			t.Errorf("MS-NLMP 4.2.4 %s = %x", name, vector.got)
		}
	}
}

func TestTSRequest(t *testing.T) {
	for code, reason := range map[int64]Reason{0xc0000234: ReasonAccountLocked, 0xc0000071: ReasonPasswordExpired, 0xc000015b: ReasonLogonTypeNotGranted, -1073741715: ReasonLogonFailure} {
		p, err := asn1.Marshal(code)
		if err != nil {
			t.Fatal(err)
		}
		p = spnegoDER(0x30, append([]byte{0xa0, 3, 2, 1, 6}, spnegoDER(0xa4, p)...))
		_, err = readTSRequest(bytes.NewReader(p))
		var rdpError *Error
		if !errors.As(err, &rdpError) || rdpError.Kind != KindAuth || rdpError.Reason != reason || rdpError.Code != uint32(code) {
			t.Errorf("NTSTATUS %#x: %v", uint32(code), err)
		}
	}
}
