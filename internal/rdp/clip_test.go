package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func rdpTestClipPDU(kind, flags uint16, data []byte) []byte {
	le := binary.LittleEndian
	p := le.AppendUint16(nil, kind)
	p = le.AppendUint16(p, flags)
	p = le.AppendUint32(p, uint32(len(data)))
	return append(p, data...)
}

func TestRDPClipboardNegotiationAndText(t *testing.T) {
	var received []string
	v, sent := testRDPVirtualChannels(func(text string) { received = append(received, text) })
	if err := v.setClipboard("café\n€"); err != nil || len(*sent) != 0 {
		t.Fatalf("local clipboard before ready = %v, %v", *sent, err)
	}
	// MS-RDPECLIP 4.1.1 and 4.1.2 server Clipboard Capabilities and Monitor Ready.
	serverCaps := []byte{
		7, 0, 0, 0, 16, 0, 0, 0, 1, 0, 0, 0,
		1, 0, 12, 0, 2, 0, 0, 0, 14, 0, 0, 0,
	}
	ready := []byte{1, 0, 0, 0, 0, 0, 0, 0}
	for _, p := range [][]byte{serverCaps, ready} {
		if err := v.receive(v.clip, rdpTestVCPacket(0x13, uint32(len(p)), p)); err != nil {
			t.Fatal(err)
		}
	}
	wantCaps := bytes.Clone(serverCaps)
	wantCaps[20] = 2 // long names, text only
	wantList := []byte{2, 0, 0, 0, 12, 0, 0, 0, 13, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0}
	if len(*sent) != 2 || !bytes.Equal((*sent)[0].body(), wantCaps) || !bytes.Equal((*sent)[1].body(), wantList) {
		t.Fatalf("initial clipboard messages = %v", *sent)
	}

	for _, check := range []struct {
		format byte
		want   []byte
	}{
		{1, []byte{'c', 'a', 'f', 0xe9, '\r', '\n', 0x80, 0}}, // Windows-1252
		{13, []byte{'c', 0, 'a', 0, 'f', 0, 0xe9, 0, '\r', 0, '\n', 0, 0xac, 0x20, 0, 0}},
	} {
		if err := v.receiveClipboard(rdpTestClipPDU(4, 0, []byte{check.format, 0, 0, 0})); err != nil {
			t.Fatal(err)
		}
		if got := (*sent)[len(*sent)-1].body(); !bytes.Equal(got, rdpTestClipPDU(5, 1, check.want)) {
			t.Fatalf("clipboard format %d response = %x", check.format, got)
		}
	}
	remoteFormats := []byte{1, 0, 0, 0, 0, 0, 13, 0, 0, 0, 0, 0}
	if err := v.receiveClipboard(rdpTestClipPDU(2, 0, remoteFormats)); err != nil {
		t.Fatal(err)
	}
	if want := rdpTestClipPDU(4, 0, []byte{13, 0, 0, 0}); !bytes.Equal((*sent)[5].body(), want) {
		t.Fatalf("preferred clipboard format = %x; want %x", (*sent)[5].body(), want)
	}
	unicode := []byte{'a', 0, 0x3d, 0xd8, 0x00, 0xde, '\r', 0, '\n', 0, 0, 0}
	p := rdpTestClipPDU(5, 1, unicode)
	if err := v.receive(v.clip, rdpTestVCPacket(0x11, uint32(len(p)), p[:10])); err != nil {
		t.Fatal(err)
	}
	if len(received) != 0 {
		t.Fatal("clipboard callback received partial data")
	}
	if err := v.receive(v.clip, rdpTestVCPacket(0x12, uint32(len(p)), p[10:])); err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 || received[0] != "a😀\n" {
		t.Fatalf("remote clipboard = %q", received)
	}
	if err := v.receiveClipboard(rdpTestClipPDU(4, 0, []byte{13, 0, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if got := (*sent)[len(*sent)-1].body(); !bytes.Equal(got, rdpTestClipPDU(5, 2, nil)) {
		t.Fatalf("superseded local clipboard request got %x", got)
	}
}

func TestRDPClipboardLegacyAndSupersession(t *testing.T) {
	var got []string
	v, sent := testRDPVirtualChannels(func(text string) { got = append(got, text) })
	if err := v.receiveClipboard(rdpTestClipPDU(1, 0, nil)); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 36)
	first[0] = 13 // short predefined format list
	second := make([]byte, 36)
	second[0] = 1
	for _, data := range [][]byte{first, second} {
		if err := v.receiveClipboard(rdpTestClipPDU(2, 4, data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.receiveClipboard(rdpTestClipPDU(5, 1, []byte{'x', 0, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("superseded remote clipboard delivered %q", got)
	}
	if want := rdpTestClipPDU(4, 0, []byte{1, 0, 0, 0}); !bytes.Equal((*sent)[4].body(), want) {
		t.Fatalf("legacy format request = %x, want %x", (*sent)[4].body(), want)
	}
	if err := v.receiveClipboard(rdpTestClipPDU(5, 1, []byte{0x80, 0})); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "€" {
		t.Fatalf("legacy remote clipboard = %q", got)
	}
	if err := v.receiveClipboard(rdpTestClipPDU(8, 0, []byte{0xf7, 0, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if want := rdpTestClipPDU(9, 2, []byte{0xf7, 0, 0, 0}); !bytes.Equal((*sent)[5].body(), want) {
		t.Fatalf("file refusal = %x, want %x", (*sent)[5].body(), want)
	}
}

func TestRDPClipboardBoundsAndValidation(t *testing.T) {
	v, _ := testRDPVirtualChannels(nil)
	bad := []string{"invalid\x00text", "\xff", strings.Repeat("z", clipMaxData/2)}
	for _, text := range bad {
		var rejected *clipboardInputError
		if err := v.setClipboard(text); !errors.As(err, &rejected) {
			t.Fatalf("local clipboard rejection missing, got %v", err)
		}
	}
	wide, ansi, err := encodeClipboardText("😀")
	if err != nil || ansi != nil || !bytes.Equal(wide, []byte{0x3d, 0xd8, 0, 0xde, 0, 0}) {
		t.Fatalf("Unicode-only text = %x, %x, %v", wide, ansi, err)
	}
	if _, err := decodeClipboardText(13, []byte{0, 0xd8, 0, 0}); err == nil {
		t.Fatal("lone clipboard surrogate accepted")
	}
	if _, ok := readClipboardFormats([]byte{13, 0, 0, 0, 1}, true); ok {
		t.Fatal("unterminated clipboard format name accepted")
	}
}

func TestRDPClipboardPadding(t *testing.T) {
	v, sent := testRDPVirtualChannels(nil)
	text := []byte{'x', 0, 0, 0}
	v.clipboard.offered, v.clipboard.unicode = true, text
	p := append(rdpTestClipPDU(clipDataRequest, 0, []byte{clipUnicode, 0, 0, 0}), 0, 0, 0, 0)
	if err := v.receiveClipboard(p); err != nil {
		t.Fatalf("padded clipboard request rejected: %v", err)
	}
	want := rdpTestClipPDU(clipDataResponse, clipOK, text)
	if len(*sent) != 1 || !bytes.Equal((*sent)[0].body(), want) {
		t.Fatalf("padded clipboard request sent %v, want one response %x", *sent, want)
	}

	binary.LittleEndian.PutUint32(p[4:], uint32(len(p)-8+1))
	err := v.receiveClipboard(p)
	if kind, reason := Classify(err); kind != KindServer || reason != ReasonProtocol || len(*sent) != 1 {
		t.Fatalf("oversized clipboard dataLen sent %d additional responses, err=%v", len(*sent)-1, err)
	}
}

func FuzzRDPClipboard(f *testing.F) {
	f.Add(rdpTestClipPDU(2, 0, []byte{13, 0, 0, 0, 0, 0}), true)
	f.Add(rdpTestClipPDU(5, 1, []byte{0x3d, 0xd8, 0, 0xde, 0, 0}), false)
	f.Fuzz(func(t *testing.T, p []byte, longNames bool) {
		v, _ := testRDPVirtualChannels(func(string) {})
		_ = v.receiveClipboard(p)
		v.clipboard.ready, v.clipboard.longNames, v.clipboard.pending = true, longNames, clipUnicode
		_ = v.receiveClipboard(p)
	})
}
