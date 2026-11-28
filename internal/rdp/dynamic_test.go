package rdp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRDPDynamicVersionNegotiation(t *testing.T) {
	// MS-RDPEDYC 4.1.1 and 4.1.2. The sample server sets unused Sp to two.
	v2 := []byte{0x58, 0, 2, 0, 0x33, 0x33, 0x11, 0x11, 0x3d, 0x0a, 0xa7, 0x04}
	for _, check := range []struct {
		wire []byte
		want byte
	}{
		{[]byte{0x50, 0, 1, 0}, 1},
		{v2, 2},
		{append([]byte{0x58, 0, 3, 0}, v2[4:]...), 2},
	} {
		v, sent := testRDPVirtualChannels(nil)
		if err := v.receive(v.dvc, rdpTestVCPacket(0x13, uint32(len(check.wire)), check.wire)); err != nil {
			t.Fatal(err)
		}
		want := []byte{0x50, 0, check.want, 0}
		if len(*sent) != 1 || !bytes.Equal((*sent)[0].body(), want) {
			t.Fatalf("DVC capabilities response = %v; want %x", *sent, want)
		}
		if err := v.receiveDynamic(check.wire); err == nil {
			t.Fatal("duplicate capabilities accepted")
		}
	}
}

func TestRDPDisplayControlFragmentsAndLayout(t *testing.T) {
	v, sent := testRDPVirtualChannels(nil)
	if handled, err := v.resize(1600, 901, 140); handled || err != nil {
		t.Fatalf("resize without Display Control: %v, %v", handled, err)
	}
	if err := v.receiveDynamic([]byte{0x50, 0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	// A larger width than required for the little-endian ID is valid. Reply
	// with the same encoded width for the CREATE and CLOSE exchanges.
	request := append([]byte{0x11, 0x23, 0}, []byte(displayChannelName)...)
	request = append(request, 0)
	if err := v.receiveDynamic(request); err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x11, 0x23, 0, 0, 0, 0, 0}; !bytes.Equal((*sent)[1].body(), want) {
		t.Fatalf("create reply = %x; want %x", (*sent)[1].body(), want)
	}

	caps := []byte{
		5, 0, 0, 0, 20, 0, 0, 0, // display caps, 20 bytes
		2, 0, 0, 0, 0, 4, 0, 0, 0, 3, 0, 0, // 2 * 1024 * 768
	}
	first := append([]byte{0x21, 0x23, 0, 20}, caps[:8]...)
	if err := v.receiveDynamic(first); err != nil {
		t.Fatal(err)
	}
	if handled, err := v.resize(1600, 901, 140); handled || err != nil {
		t.Fatalf("resize after incomplete capabilities: %v, %v", handled, err)
	}
	if err := v.receiveDynamic(append([]byte{0x31, 0x23, 0}, caps[8:]...)); err != nil {
		t.Fatal(err)
	}
	for _, mode := range [][3]int{{1601, 901, 140}, {1920, 1080, 140}, {1600, 901, 501}} {
		if handled, err := v.resize(mode[0], mode[1], mode[2]); handled || err != nil {
			t.Fatalf("unsupported mode %v: %v, %v", mode, handled, err)
		}
	}
	if handled, err := v.resize(1600, 901, 140); !handled || err != nil {
		t.Fatalf("resize after negotiation: %v, %v", handled, err)
	}
	want := []byte{
		0x30, 0x23, // DVC Data for ID 35
		2, 0, 0, 0, 56, 0, 0, 0, 40, 0, 0, 0, 1, 0, 0, 0,
		1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0x40, 6, 0, 0, 0x85, 3, 0, 0,
		46, 1, 0, 0, 170, 0, 0, 0, // about 302x170 mm at effective DPI
		0, 0, 0, 0, 140, 0, 0, 0, 140, 0, 0, 0,
	}
	if got := (*sent)[2].body(); !bytes.Equal(got, want) {
		t.Fatalf("monitor layout = %x; want %x", got, want)
	}

	closeRequest := []byte{0x41, 0x23, 0}
	if err := v.receiveDynamic(closeRequest); err != nil || !bytes.Equal((*sent)[3].body(), closeRequest) {
		t.Fatalf("DVC close = %v, %v", *sent, err)
	}
	if handled, err := v.resize(1600, 901, 140); handled || err != nil {
		t.Fatalf("resize after close: %v, %v", handled, err)
	}
}

func TestRDPDynamicRejectsCapabilitiesAndFragments(t *testing.T) {
	v, sent := testRDPVirtualChannels(nil)
	if err := v.receiveDynamic([]byte{0x50, 0, 3, 0}); err == nil {
		t.Fatal("truncated v3 capabilities accepted")
	}
	v.dynamic.version = 2
	if err := v.receiveDynamic([]byte{0x10, 7, 'x', 0}); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32((*sent)[0].body()[2:]); int32(got) >= 0 || v.dynamic.open {
		t.Fatalf("unsupported channel open result = %#x", got)
	}
	v.dynamic.open, v.dynamic.id = true, 0
	for _, bad := range [][]byte{
		{0x23, 0},                           // invalid cbId
		{0x2c, 0, 0},                        // invalid cbLen
		{0x28, 0, 1, 0, 1, 0, 0},            // >64 KiB
		{0x30, 1, 0},                        // unrelated channel
		{0x20, 0, 2, 1, 2, 3},               // first body exceeds length
		{0x30, 0, 5, 0, 0, 0, 255, 0, 0, 0}, // short CAPS packet
	} {
		if err := v.receiveDynamic(bad); err == nil {
			t.Fatalf("malformed dynamic PDU accepted: %x", bad)
		}
	}
}

func FuzzRDPDynamicChannel(f *testing.F) {
	f.Add([]byte{0x50, 0, 1, 0})
	f.Add([]byte{0x20, 0, 20, 5, 0, 0, 0})
	f.Add([]byte{0x30, 0, 5, 0, 0, 0, 20, 0, 0, 0, 1, 0, 0, 0, 0, 4, 0, 0, 0, 3, 0, 0})
	f.Fuzz(func(t *testing.T, p []byte) {
		v, _ := testRDPVirtualChannels(nil)
		_ = v.receiveDynamic(p)
		v.dynamic.version, v.dynamic.open = 2, true
		_ = v.receiveDynamic(p)
	})
}
