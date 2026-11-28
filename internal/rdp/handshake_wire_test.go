package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestHandshakeWire(t *testing.T) {
	// MS-RDPBCGR 4.1.2, with each returned protocol and the failure op tested.
	sample := handshakeHex(t, "030000130ed000001234000200080000000000")
	for _, tc := range []struct {
		mode Security
		prot uint32
		want Kind
	}{
		{SecurityAuto, 0, KindSecurity}, {SecurityAuto, 1, KindOther}, {SecurityAuto, 2, KindOther},
		{SecurityAuto, 8, KindOther}, {SecurityAuto, 0x10, KindSecurity}, {SecurityAuto, 3, KindSecurity},
		{SecurityTLS, 1, KindOther}, {SecurityTLS, 2, KindSecurity}, {SecurityNLA, 1, KindSecurity},
		{SecurityNLA, 2, KindOther}, {SecurityNLA, 8, KindOther},
	} {
		binary.LittleEndian.PutUint32(sample[15:], tc.prot)
		p, _, err := readRDPFrame(bytes.NewReader(sample), false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseRDPSelection(p, tc.mode, requestedProtocols(tc.mode))
		if kind, _ := Classify(err); kind != tc.want || (err == nil && got != tc.prot) {
			t.Errorf("mode %q protocol %#x got %#x, %v", tc.mode, tc.prot, got, err)
		}
	}
	p := []byte{14, 0xd0, 0, 0, 0, 0, 0, 3, 0, 8, 0, 5, 0, 0, 0}
	_, err := parseRDPSelection(p, SecurityTLS, protocolTLS)
	var rdpErr *Error
	if !errors.As(err, &rdpErr) || rdpErr.Kind != KindSecurity || rdpErr.Code != 5 {
		t.Fatalf("negotiation failure lost reason code: %v", err)
	}
	if err := readEarlyAuthorization(bytes.NewReader([]byte{5, 0, 0, 0})); !errors.As(err, &rdpErr) || rdpErr.Reason != ReasonServerDenied {
		t.Fatalf("Early User Authorization denied: %v", err)
	}
}

func TestHandshakeGCC(t *testing.T) {
	// MS-RDPBCGR 4.1.3 request framing and field offsets.
	p := clientGCC(Config{Width: 1280, Height: 900, Scale: 140, Admin: true, KeyboardLayout: 0x409}, protocolHybridEx, []string{"drdynvc"})
	if len(p) < 9 || !bytes.Equal(p[:7], handshakeHex(t, "000500147c0001")) {
		t.Fatalf("GCC conference header %x", p)
	}
	p, n, ok := mcsLength(p[7:])
	if !ok || len(p) != n || !bytes.HasPrefix(p, handshakeHex(t, "000800100001c00044756361")) {
		t.Fatalf("invalid GCC conference request length or Duca key")
	}
	blocks, ok := takeMCSData(p[12:])
	if !ok || len(blocks) < 270 {
		t.Fatalf("invalid GCC client blocks length %d", len(blocks))
	}
	le := binary.LittleEndian
	if le.Uint16(blocks[2:]) != 234 || le.Uint16(blocks[8:]) != 1280 || le.Uint16(blocks[10:]) != 900 ||
		le.Uint16(blocks[144:]) != 0x0863 || blocks[210] != 6 || le.Uint32(blocks[212:]) != 8 ||
		le.Uint32(blocks[216:]) != 338 || le.Uint32(blocks[226:]) != 140 || le.Uint32(blocks[230:]) != 100 {
		t.Fatal("GCC core omitted desktop settings or client capabilities")
	}
	if !bytes.Equal(blocks[234:246], handshakeHex(t, "04c00c000e00000000000000")) {
		t.Fatal("GCC did not request administrative session zero")
	}
	if !bytes.Equal(blocks[246:258], handshakeHex(t, "02c00c000000000000000000")) {
		t.Fatal("GCC did not disable standard security for TLS")
	}

	for _, skip := range []bool{false, true} {
		packet := testGCCResponse(1, skip, []uint16{1004})
		gcc, err := mcsConnectResponse(packet)
		if err != nil {
			t.Fatal(err)
		}
		settings, err := parseServerGCC(gcc, 1, 1)
		if err != nil || settings.skipJoin != skip || settings.globalID != 1003 || len(settings.ids) != 1 || settings.ids[0] != 1004 {
			t.Fatalf("GCC response = %+v, %v", settings, err)
		}
	}
}

func TestRDPClientInfoXrdpLogonFlags(t *testing.T) {
	p, err := clientInfo(Config{User: "sample"}, nil, protocolTLS, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) < 12 {
		t.Fatalf("short Client Info PDU: %d bytes", len(p))
	}
	const logonNormal = 0x33 // xrdp's RDP_LOGON_NORMAL mask.
	if flags := binary.LittleEndian.Uint32(p[8:12]); flags&logonNormal != logonNormal {
		t.Fatalf("Client Info flags %#08x omit bits from RDP_LOGON_NORMAL (%#x)", flags, logonNormal)
	}
}

func TestHandshakeFastPath(t *testing.T) {
	for _, pkt := range [][]byte{{0, 4, 3, 9}, {0, 0x80, 5, 3, 9}} {
		p, fast, err := readRDPFrame(bytes.NewReader(pkt), true)
		if err != nil || !fast || !bytes.Equal(p, []byte{0, 3, 9}) {
			t.Fatalf("fast path = %x %v %v", p, fast, err)
		}
	}
}

func FuzzRDPFraming(f *testing.F) {
	f.Add([]byte{3, 0, 0, 9, 2, 0xf0, 0x80, 0x21, 0x80})
	f.Add([]byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0x12, 0x34, 0, 2, 0, 8, 0, 0, 0, 0, 0})
	f.Add([]byte{0, 0x80, 5, 3, 9})
	f.Fuzz(func(t *testing.T, data []byte) {
		p, fast, err := readRDPFrame(bytes.NewReader(data), true)
		if err == nil && !fast {
			_, _ = unwrapMCS(p)
			_, _ = parseRDPSelection(p, SecurityAuto, 0xb)
		}
	})
}

func FuzzMCSGCC(f *testing.F) {
	f.Add(testGCCResponse(0xb, true, []uint16{1004}))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := mcsConnectResponse(data)
		if err == nil {
			_, _ = parseServerGCC(p, 0xb, 2)
		}
	})
}

func FuzzNTLMChallenge(f *testing.F) {
	f.Add(handshakeTestChallenge())
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = parseNTLMChallenge(data) })
}

func FuzzTSRequest(f *testing.F) {
	f.Add([]byte{0x30, 5, 0xa0, 3, 2, 1, 6})
	f.Add([]byte{0x30, 0x81, 0xff, 0xa0, 3, 2, 1, 6})
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = readTSRequest(bytes.NewReader(data)) })
}

func TestHandshakeMCSFragmentedLength(t *testing.T) {
	for _, size := range []int{0, 127, 128, 16383, 16384, 16385, 32767} {
		p := bytes.Repeat([]byte{0x47}, size)
		encoded := appendMCSData(nil, p)
		if size >= 16384 && encoded[0] != 0xc1 {
			t.Fatalf("expected fragmented PER length, got %#x", encoded[0])
		}
		decoded, ok := takeMCSData(encoded)
		if !ok || !bytes.Equal(decoded, p) {
			t.Errorf("%d-byte MCS data failed to round trip", size)
		}
		if _, ok := takeMCSData(encoded[:len(encoded)-1]); ok {
			t.Errorf("accepted truncated %d-byte MCS data", size)
		}
	}
}

func TestNormalizeRDPClientName(t *testing.T) {
	for input, want := range map[string]string{
		"":                  "SSHYard",
		"\x00hidden":        "SSHYard",
		"laptop\x00hidden":  "laptop",
		"abcdefghijklm🌎end": "abcdefghijklm🌎",
		"abcdefghijklmn🌎":   "abcdefghijklmn",
	} {
		if got := normalizeRDPClientName(input); got != want {
			t.Errorf("client name %q = %q, want %q", input, got, want)
		}
	}
}
