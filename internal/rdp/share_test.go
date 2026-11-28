package rdp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRDPShareDataCompressionFlags(t *testing.T) {
	const shareID = 0x12345678
	body := []byte{1, 0, 0xea, 3} // Synchronize message type and target user.
	p := append([]byte{0x78, 0x56, 0x34, 0x12, 0, 1, 8, 0, dataSynchronize, 0, 0, 0}, body...)
	for _, flags := range []byte{0x80, 0x40, 0xc0} { // PACKET_FLUSHED, PACKET_AT_FRONT, or both.
		p[9] = flags
		id, kind, data, err := readDataPDU(p)
		if err != nil || id != shareID || kind != dataSynchronize || !bytes.Equal(data, body) {
			t.Errorf("share data flags %#x gave share=%#x kind=%#x data=%x err=%v", flags, id, kind, data, err)
		}
	}
	// Without PACKET_COMPRESSED, the enclosing PDU bounds even a nonzero compressedLength.
	binary.LittleEndian.PutUint16(p[10:], 24)
	if _, _, data, err := readDataPDU(p); err != nil || !bytes.Equal(data, body) {
		t.Errorf("uncompressed share data with compressedLength gave data=%x err=%v", data, err)
	}

	p[9] |= 0x20 // PACKET_COMPRESSED
	_, _, data, err := readDataPDU(p)
	if kind, reason := Classify(err); kind != KindServer || reason != ReasonProtocol || data != nil {
		t.Fatalf("compressed share data gave data=%x err=%v", data, err)
	}
}
