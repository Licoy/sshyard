package rdp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

type rdpVCTestPacket struct {
	channel uint16
	wire    []byte
}

func testRDPVirtualChannels(onText func(string)) (*virtualChannels, *[]rdpVCTestPacket) {
	var sent []rdpVCTestPacket
	v := newVirtualChannels(map[string]uint16{"drdynvc": 1004, "cliprdr": 1005}, func(id uint16, wire []byte) error {
		sent = append(sent, rdpVCTestPacket{id, bytes.Clone(wire)})
		return nil
	}, onText)
	return v, &sent
}

func rdpTestVCPacket(flags, total uint32, body []byte) []byte {
	le := binary.LittleEndian
	header := le.AppendUint32(nil, total)
	header = le.AppendUint32(header, flags)
	return append(header, body...)
}

func (p rdpVCTestPacket) body() []byte { return p.wire[8:] }

func TestRDPStaticChannelFragmentation(t *testing.T) {
	v, sent := testRDPVirtualChannels(nil)
	v.setChunkSize(2000)
	wire := bytes.Repeat([]byte{0xfe}, 4001)
	if err := v.sendChannel(v.clip, wire); err != nil {
		t.Fatal(err)
	}
	flags := []uint32{0x11, 0x10, 0x12}
	var assembled channelMessage
	var got []byte
	for i, p := range *sent {
		le := binary.LittleEndian
		total, flag := le.Uint32(p.wire), le.Uint32(p.wire[4:])
		if p.channel != v.clip || flag != flags[i] || total != 4001 {
			t.Fatalf("fragment %d sent on %d: %x", i, p.channel, p.wire[:8])
		}
		var err error
		got, err = assembled.staticChunk(p.body(), total, maxClipboardPDU, flag)
		if err != nil {
			t.Fatal(err)
		}
		if i < len(flags)-1 && got != nil {
			t.Fatal("partial SVC message published")
		}
	}
	if len(*sent) != 3 || !bytes.Equal(got, wire) {
		t.Fatalf("fragmented payload size = %d, %d packets", len(got), len(*sent))
	}

	for _, p := range [][]byte{
		rdpTestVCPacket(0x12, 3, []byte{1, 2, 3}),
		rdpTestVCPacket(0x11, maxClipboardPDU+1, []byte{1}),
		rdpTestVCPacket(0x13, 1, []byte{1, 2}),
		rdpTestVCPacket(0x13|0x200000, 1, []byte{1}),
	} {
		if err := v.receive(v.clip, p); err == nil {
			t.Fatalf("invalid static fragment accepted: %x", p[:8])
		}
	}
}

func FuzzRDPStaticChannel(f *testing.F) {
	f.Add(rdpTestVCPacket(0x13, 4, []byte{0x50, 0, 1, 0}), true)
	f.Add(rdpTestVCPacket(0x13, 8, []byte{1, 0, 0, 0, 0, 0, 0, 0}), false)
	f.Fuzz(func(t *testing.T, p []byte, dynamic bool) {
		v, _ := testRDPVirtualChannels(nil)
		channel := v.clip
		if dynamic {
			channel = v.dvc
		}
		_ = v.receive(channel, p)
	})
}
