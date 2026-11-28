package rdp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func TestRDPCapabilitiesDemandActiveExample(t *testing.T) {
	pdu, rest, err := readSharePDU(demandActiveExample(t))
	if err != nil || len(rest) != 0 || pdu.kind != shareDemandActive || pdu.source != 1002 {
		t.Fatalf("MS-RDPBCGR 4.1.12 Share Control Header: %+v, rest=%d, %v", pdu, len(rest), err)
	}
	caps, err := demandActive(pdu.data)
	if err != nil {
		t.Fatal(err)
	}
	want := serverCapabilities{
		shareID: 0x103ea, width: 1280, height: 1024, bpp: 24, input: 0x35,
		refresh: true, suppress: true, arc: true, chunk: 1600,
	}
	if caps != want {
		t.Fatalf("Demand Active decoded to %+v, want %+v", caps, want)
	}
}

func TestRDPCapabilitiesConfirmActive(t *testing.T) {
	le := binary.LittleEndian
	cfg := Config{KeyboardLayout: 0x0409}
	caps := serverCapabilities{shareID: 0x103ea, width: 1280, height: 1024, bpp: 32}
	p := confirmActive(1006, cfg, caps)
	share, rest, err := readSharePDU(p)
	if err != nil || len(rest) != 0 || share.kind != shareConfirmActive || share.source != 1006 {
		t.Fatalf("invalid Confirm Active Share Control Header: %v", err)
	}
	p = share.data
	if len(p) < 18 || le.Uint32(p) != caps.shareID || le.Uint16(p[4:]) != 1002 {
		t.Fatal("Confirm Active wrote the wrong share or originator")
	}
	sourceLen, combined := int(le.Uint16(p[6:])), int(le.Uint16(p[8:]))
	if sourceLen != 4 || combined < 4 || combined != len(p)-10-sourceLen {
		t.Fatal("invalid Confirm Active capabilities length")
	}
	sets := readTestCapabilitySets(t, p[10+sourceLen:])
	assertClientTestCapabilities(t, caps, sets)
}

func readTestCapabilitySets(t *testing.T, p []byte) map[uint16][]byte {
	t.Helper()
	le := binary.LittleEndian
	count := int(le.Uint16(p))
	p = p[4:]
	sets := make(map[uint16][]byte, count)
	for range count {
		if len(p) < 4 {
			t.Fatal("truncated advertised capability header")
		}
		kind, size := le.Uint16(p), int(le.Uint16(p[2:]))
		if size < 4 || size > len(p) || sets[kind] != nil {
			t.Fatalf("invalid advertised capability %d size %d", kind, size)
		}
		sets[kind], p = p[4:size], p[size:]
	}
	if len(p) != 0 {
		t.Fatal("trailing Confirm Active capabilities bytes")
	}
	return sets
}

func assertClientTestCapabilities(t *testing.T, caps serverCapabilities, sets map[uint16][]byte) {
	t.Helper()
	le := binary.LittleEndian
	general, bitmap, order := sets[1], sets[2], sets[3]
	if len(general) != 20 || le.Uint16(general[4:]) != 0x0200 || le.Uint16(general[10:]) != 0x040d || le.Uint16(general[8:]) != 0 {
		t.Fatal("incorrect General capability flags")
	}
	if len(bitmap) != 24 || int(le.Uint16(bitmap)) != caps.bpp || int(le.Uint16(bitmap[8:])) != caps.width || int(le.Uint16(bitmap[10:])) != caps.height {
		t.Fatal("incorrect Bitmap capability depth or dimensions")
	}
	if len(order) != 84 || !bytes.Equal(order[32:64], make([]byte, 32)) {
		t.Fatal("drawing orders were advertised")
	}
	input, vc, fragment, codecs := sets[13], sets[20], sets[26], sets[29]
	if len(input) != 84 || le.Uint16(input) != 0x013d || le.Uint32(input[4:]) != 0x0409 {
		t.Fatal("incorrect input flags or keyboard layout")
	}
	if len(vc) != 8 || le.Uint32(vc) != 0 || le.Uint32(vc[4:]) != 1600 {
		t.Fatal("incorrect virtual channel compression or chunk size")
	}
	if len(fragment) != 4 || le.Uint32(fragment) == 0 || len(codecs) != 1 || codecs[0] != 0 {
		t.Fatal("incorrect fast-path fragmentation or bitmap codec advertisement")
	}
}

func FuzzRDPCapabilities(f *testing.F) {
	sample := demandActiveExample(f)
	f.Add(sample)
	badLength := bytes.Clone(sample)
	badLength[24], badLength[25] = 3, 0 // First capability length is smaller than its header.
	f.Add(badLength)
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, _, err := readSharePDU(data); err == nil && p.kind == shareDemandActive {
			_, _ = demandActive(p.data)
		}
	})
}

func demandActiveExample(t testing.TB) []byte {
	t.Helper()
	// MS-RDPBCGR 4.1.12, decrypted TS_DEMAND_ACTIVE_PDU including Share Control Header.
	// https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpbcgr/084026ea-8264-4315-ac66-c77dea02b0c1
	p, err := hex.DecodeString(strings.Join(strings.Fields(`
67011100ea03ea030100040051015244
50000d00000009000800ea03dce20100
1800010003000002000000001d040000
00000000010114000800020000001600
28000000000070f613f3010000000100
0000180000009cf613f361a682800000
0000005091bf0e00040002001c001800
01000100010000050004000001000100
00000100000003005800000000000000
0000000000000000000040420f000100
14000000010000002200010101010100
00010101010100000001010101010101
01000101010100000000a10600004042
0f0040420f0001000000000000000a00
08000600000012000800010000000800
0a000100190019000d00580035000000
a106000040420f000cf613f3935a37f3
009030e1341c38f340f613f304000000
4c54dce20850dce2010000000850dce2
0000000038f613f32e0538f30850dce2
2cf613f30000000008000a0001001900
170008000000000018000b0000000000
00000000000000`), ""))
	if err != nil || len(p) != 359 {
		t.Fatalf("could not decode published Demand Active fixture: %v", err)
	}
	return p
}
