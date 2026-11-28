package rdp

import (
	"encoding/binary"
	"image"
	"testing"
)

func rdpTestFastUpdate(head byte, data []byte) []byte {
	packet := binary.LittleEndian.AppendUint16([]byte{head}, uint16(len(data)))
	return append(packet, data...)
}

func TestRDPFastOutputCompressionFlags(t *testing.T) {
	for _, flags := range []byte{0x80, 0x40, 0xc0} { // PACKET_FLUSHED, PACKET_AT_FRONT, or both.
		var updates []Pointer
		g := graphicsState{pointer: func(p Pointer) { updates = append(updates, p) }}
		// A system pointer update with compression flags and an empty body.
		if err := g.fastOutput([]byte{0x86, flags, 0, 0}); err != nil {
			t.Errorf("fast-path flags %#x rejected: %v", flags, err)
		} else if len(updates) != 1 || !updates[0].Default {
			t.Errorf("fast-path flags %#x produced pointer updates %+v", flags, updates)
		}
		updates = nil
		if err := g.fastOutput([]byte{0x86, flags | 0x20, 0, 0}); err == nil || len(updates) != 0 {
			t.Errorf("compressed fast-path flags %#x produced pointer updates %+v, err=%v", flags|0x20, updates, err)
		}
	}
}

func TestRDPFastOutputFragmentSequence(t *testing.T) {
	var position image.Point
	g := graphicsState{position: func(p image.Point) { position = p }}
	for _, packet := range [][]byte{
		rdpTestFastUpdate(0x28, []byte{17}),
		rdpTestFastUpdate(0x38, []byte{1}),
		append(rdpTestFastUpdate(0x18, []byte{10, 0}), rdpTestFastUpdate(3, nil)...),
	} {
		if err := g.fastOutput(packet); err != nil {
			t.Fatal(err)
		}
	}
	if position != image.Pt(273, 10) || g.fastFragment != nil {
		t.Fatalf("fragmented pointer position = %v", position)
	}

	for _, packet := range [][]byte{
		{0x85},                   // missing compression flags
		{0x85, 0x20, 0, 0},       // unnegotiated bulk compression
		{0x45, 0, 0},             // invalid compression indicator
		{5, 1, 0},                // truncated body
		{5, 1, 0, 0},             // invalid system-pointer payload
		{0x18, 0, 0},             // LAST without FIRST
		{0x28, 0, 0, 0x1a, 0, 0}, // LAST differs in type
		{0x28, 0, 0, 0x28, 0, 0}, // FIRST interrupts FIRST
	} {
		var bad graphicsState
		if err := bad.fastOutput(packet); err == nil {
			t.Fatalf("invalid fast-path output %x accepted", packet)
		}
	}

	part := make([]byte, 16*1024-4)
	for g.fastFragment == nil || len(g.fastFragment)+len(part) <= maxFastUpdateBytes {
		header := byte(0x38)
		if g.fastFragment == nil {
			header = 0x28
		}
		if err := g.fastUpdateFragment(header, part); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.fastUpdateFragment(0x38, part); err == nil || cap(g.fastFragment) > maxFastUpdateBytes {
		t.Fatal("oversized fast-path fragment series accepted")
	}
}

func FuzzRDPFastOutput(f *testing.F) {
	f.Add([]byte{5, 0, 0, 8, 4, 0, 0, 0, 0, 0})
	f.Add([]byte{0x28, 2, 0, 0, 0, 0x18, 2, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		var g graphicsState
		_ = g.fastOutput(data)
	})
}
