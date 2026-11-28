package rdp

import (
	"bytes"
	"encoding/binary"
	"image"
	"testing"
)

func TestRDPPointerMasksAndCache(t *testing.T) {
	var updates []Pointer
	var position image.Point
	g := graphicsState{
		pointer:  func(p Pointer) { updates = append(updates, p) },
		position: func(p image.Point) { position = p },
	}
	if err := g.slowPointer([]byte{1, 0, 0, 0, 0, 0x7f, 0, 0}); err != nil {
		t.Fatal(err)
	}
	wire := []byte{
		8, 0, 0, 0, // TS_POINTER_PDU New Pointer
		1, 0, // 1-bit XOR
		3, 0, 1, 0, 0, 0, // cache #3, hotspot 1,0
		3, 0, 2, 0, // 3x2
		4, 0, 4, 0, // AND and XOR length
		0x40, 0, 0xa0, 0, // XOR: bottom 010, top 101
		0x00, 0, 0x60, 0, // AND: bottom 000, top 011
		0, // optional pad
	}
	if err := g.slowPointer(wire); err != nil {
		t.Fatal(err)
	}
	if err := g.fastOutput([]byte{5, 0, 0, 8, 4, 0, 42, 0, 7, 0, 10, 2, 0, 3, 0}); err != nil {
		t.Fatal(err)
	}
	if position != image.Pt(42, 7) || len(updates) != 4 || !updates[0].Default || !updates[2].Hidden {
		t.Fatalf("pointer updates = %+v, position = %v", updates, position)
	}
	shape, cached := updates[1], updates[3]
	if shape.HotX != 1 || shape.HotY != 0 || shape.Version != 2 || cached.Version != 4 || cached.Image != shape.Image {
		t.Fatalf("pointer shape = %+v, cached = %+v", shape, cached)
	}
	want := []byte{
		255, 255, 255, 255, 0, 0, 0, 0, 128, 128, 128, 255,
		0, 0, 0, 255, 255, 255, 255, 255, 0, 0, 0, 255,
	}
	if !bytes.Equal(shape.Image.Pix, want) {
		t.Fatalf("pointer image = %x; want %x", shape.Image.Pix, want)
	}
	if err := g.cachedPointer([]byte{4, 0}); err == nil {
		t.Fatal("empty pointer cache entry accepted")
	}
}

func TestRDPPointerARGBAndLargeFragment(t *testing.T) {
	var current Pointer
	g := graphicsState{pointer: func(p Pointer) { current = p }}
	// 384x100 BGRA cursor. Its XOR stream needs a 32-bit length and spans frames.
	const width, height = 384, 100
	le := binary.LittleEndian
	header := []byte{32, 0, 1, 0, 0, 0, 0, 0}
	header = le.AppendUint16(header, width)
	header = le.AppendUint16(header, height)
	header = le.AppendUint32(header, 0)
	header = le.AppendUint32(header, width*height*4)
	wire := append(header, bytes.Repeat([]byte{255, 0, 128, 128}, width*height)...)
	for i := 0; i < len(wire); i += 16000 {
		kind := byte(0x3c)
		if i == 0 {
			kind = 0x2c
		}
		end := min(i+16000, len(wire))
		if end == len(wire) {
			kind = 0x1c
		}
		if err := g.fastOutput(rdpTestFastUpdate(kind, wire[i:end])); err != nil {
			t.Fatal(err)
		}
		if end < len(wire) && current.Image != nil {
			t.Fatal("partial pointer update became visible")
		}
	}
	want := bytes.Repeat([]byte{64, 0, 128, 128}, width*height)
	if current.Image == nil || !bytes.Equal(current.Image.Pix, want) {
		t.Fatal("large alpha cursor not decoded")
	}
	for _, offset := range []int{8, 10, 12, 16} {
		bad := bytes.Clone(wire)
		le.PutUint16(bad[offset:], 0xffff)
		if err := g.pointerShape(bad, true, true); err == nil {
			t.Fatalf("malformed pointer field at offset %d accepted", offset)
		}
	}
}

func FuzzRDPPointerShape(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 0, 2, 0, 2, 0, 0x80, 0, 0, 0}, true, false)
	f.Add([]byte{}, false, true)
	f.Fuzz(func(t *testing.T, data []byte, depth, large bool) {
		var g graphicsState
		_ = g.pointerShape(data, depth, large)
	})
}
