package rdp

import (
	"bytes"
	"encoding/binary"
	"image"
	"testing"
)

func TestRDPRawBitmapFormats(t *testing.T) {
	var g graphicsState
	palette := make([]byte, 8+256*3)
	palette[0], palette[5] = 2, 1
	copy(palette[8+7*3:], []byte{0, 0, 255})
	copy(palette[8+8*3:], []byte{255, 0, 0})
	if err := g.slowUpdate(palette); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		depth int
		wire  []byte // bottom blue, top red, rows padded to 4 bytes
	}{
		{8, []byte{7, 0xa5, 0xa5, 0xa5, 8, 0xa5, 0xa5, 0xa5}},
		{15, []byte{31, 0, 0xa5, 0xa5, 0, 0x7c, 0xa5, 0xa5}},
		{16, []byte{31, 0, 0xa5, 0xa5, 0, 0xf8, 0xa5, 0xa5}},
		{24, []byte{255, 0, 0, 0xa5, 0, 0, 255, 0xa5}},
		{32, []byte{255, 0, 0, 0, 0, 0, 255, 0}},
	} {
		img, err := g.decodeBitmap(check.wire, bitmapFormat{1, 2, check.depth}, false)
		want := []byte{255, 0, 0, 255, 0, 0, 255, 255}
		if err != nil || !bytes.Equal(img.Pix, want) {
			t.Fatalf("depth %d got %v (%v), want %x", check.depth, img, err, want)
		}
	}
	if err := g.slowUpdate(palette[:len(palette)-1]); err == nil {
		t.Fatal("truncated palette accepted")
	}
}

func TestRDPInterleavedBitmapOrders(t *testing.T) {
	var g graphicsState
	for _, check := range []struct {
		name   string
		format bitmapFormat
		wire   []byte
		red    []byte
	}{
		{"regular-with-previous-row", bitmapFormat{4, 2, 24},
			[]byte{0x64, 0, 0, 195, 0x01, 0x03}, []byte{195, 60, 195, 195, 195, 195, 195, 195}},
		{"background-first-row-boundary", bitmapFormat{2, 2, 16},
			[]byte{0x02, 0x02}, []byte{0, 0, 0, 0}},
		{"first-row-background-insert-foreground", bitmapFormat{4, 1, 15},
			[]byte{0x02, 0x02}, []byte{0, 0, 255, 0}},
		{"lite-dither", bitmapFormat{4, 1, 24},
			[]byte{0xe2, 0, 0, 33, 0, 0, 71}, []byte{33, 71, 33, 71}},
		{"mega-mega-literal", bitmapFormat{2, 1, 15},
			[]byte{0xf4, 2, 0, 0, 0x7c, 0, 0x40}, []byte{255, 132}},
		{"short-fg-bg-mask", bitmapFormat{3, 1, 24},
			[]byte{0xd0, 2, 0, 0, 70, 0b101}, []byte{70, 0, 70}},
		{"special-fg-bg", bitmapFormat{8, 1, 24},
			[]byte{0xfa}, []byte{255, 0, 255, 0, 0, 0, 0, 0}},
	} {
		t.Run(check.name, func(t *testing.T) {
			img, err := g.decodeBitmap(check.wire, check.format, true)
			if err != nil {
				t.Fatal(err)
			}
			var red []byte
			for i := 0; i < len(img.Pix); i += 4 {
				red = append(red, img.Pix[i])
			}
			if !bytes.Equal(red, check.red) {
				t.Fatalf("red samples = %v; want %v", red, check.red)
			}
		})
	}
	for _, wire := range [][]byte{nil, {0xc1}, {0xe1, 0, 0, 0}, {0xf0, 2, 0}, {0xf5}, {0xfd, 0xfe}} {
		if _, err := g.decodeBitmap(wire, bitmapFormat{1, 1, 24}, true); err == nil {
			t.Fatalf("invalid RLE %x accepted", wire)
		}
	}
}

func TestRDPPlanarBitmapDelta(t *testing.T) {
	// MS-RDPEGDI 3.1.9.2.3 decoded plane example, with red only and no alpha.
	wire := []byte{
		0x30,
		0x13, 0xff, 0x20, 0xfe, 0xfd,
		0x60, 0x01, 0x7d, 0xf5, 0xc2, 0x9a, 0x38,
		0x60, 0x01, 0x67, 0x8b, 0xa3, 0x78, 0xaf,
		0x06, 0x06, 0x06, // green rows
		0x06, 0x06, 0x06, // blue rows
	}
	img, err := decodePlanarBitmap(wire, bitmapFormat{6, 3, 32})
	if err != nil {
		t.Fatal(err)
	}
	wantRed := []byte{253, 140, 62, 14, 135, 193, 254, 192, 132, 96, 75, 25, 255, 255, 255, 255, 254, 253}
	for i, r := range wantRed {
		if got := img.Pix[4*i : 4*i+4]; !bytes.Equal(got, []byte{r, 0, 0, 255}) {
			t.Fatalf("pixel %d = %x", i, got)
		}
	}
	for _, invalid := range [][]byte{wire[:len(wire)-1], append(bytes.Clone(wire), 0), {0x30, 0, 0, 0}} {
		if _, err := decodePlanarBitmap(invalid, bitmapFormat{6, 3, 32}); err == nil {
			t.Fatalf("invalid planar %x accepted", invalid)
		}
	}
}

func TestRDPPlanarBitmapRawAndSubsampled(t *testing.T) {
	wire := []byte{0x00, 128, 255, 0, 255, 0}
	img, err := decodePlanarBitmap(wire, bitmapFormat{1, 1, 32})
	if err != nil || !bytes.Equal(img.Pix, []byte{255, 0, 255, 255}) {
		t.Fatalf("ARGB = %v (%v)", img, err)
	}
	wire = []byte{0x29,
		100, 100, 100, 100, 100, 100, 100, 100, 100,
		10, 20, 30, 0xf6, // bottom-left, bottom-right, top-left, top-right Co/2
		0, 0, 0, 0, // Cg/2
	}
	img, err = decodePlanarBitmap(wire, bitmapFormat{3, 3, 32})
	if err != nil {
		t.Fatal(err)
	}
	wantRed := []byte{130, 130, 90, 130, 130, 90, 110, 110, 120}
	for i, r := range wantRed {
		if img.Pix[4*i] != r {
			t.Fatalf("chroma sample %d = %d, want %d", i, img.Pix[4*i], r)
		}
	}
}

func TestRDPBitmapRectangleUpdate(t *testing.T) {
	// A 2x1 destination backed by a 4x1 padded decoded bitmap, with TS_CD_HEADER.
	update := []byte{
		1, 0, 1, 0,
		10, 0, 20, 0, 11, 0, 20, 0,
		4, 0, 1, 0, 16, 0, 1, 0, 11, 0,
		0, 0, 3, 0, 4, 0, 8, 0,
		0x64, 0xe0, 0x07,
	}
	calls := 0
	g := graphicsState{bitmap: func(dst image.Rectangle, patch *image.RGBA) error {
		calls++
		if dst != image.Rect(10, 20, 12, 21) || patch.Bounds() != image.Rect(0, 0, 4, 1) {
			t.Fatalf("destination %v for bitmap %v", dst, patch.Bounds())
		}
		if !bytes.Equal(patch.Pix, bytes.Repeat([]byte{0, 255, 0, 255}, 4)) {
			t.Fatalf("bitmap contents: %x", patch.Pix)
		}
		return nil
	}}
	if err := g.slowUpdate(update); err != nil || calls != 1 {
		t.Fatalf("bitmap update calls = %d, err = %v", calls, err)
	}
	if err := g.slowUpdate(update[:len(update)-1]); err == nil {
		t.Fatal("truncated bitmap update accepted")
	}
	binary.LittleEndian.PutUint16(update[12:], 2)
	binary.LittleEndian.PutUint16(update[8:], 20)
	if err := g.slowUpdate(update); err == nil {
		t.Fatal("bitmap destination wider than pixels accepted")
	}
}

func FuzzRDPBitmapInterleaved(f *testing.F) {
	f.Add([]byte{0x64, 0, 0, 20, 0x04}, uint8(4), uint8(2), true)
	f.Add([]byte{0xf4, 2, 0, 0, 0x7c, 0, 0x40}, uint8(2), uint8(1), false)
	f.Fuzz(func(t *testing.T, wire []byte, width, height uint8, color24 bool) {
		var g graphicsState
		depth := 16
		if color24 {
			depth = 24
		}
		_, _ = g.decodeBitmap(wire, bitmapFormat{int(width), int(height), depth}, true)
	})
}

func FuzzRDPBitmapPlanar(f *testing.F) {
	f.Add([]byte{0x20, 255, 0, 255}, uint8(1), uint8(1))
	f.Add([]byte{0x30, 0x04, 0x04, 0x04}, uint8(4), uint8(1))
	f.Fuzz(func(t *testing.T, wire []byte, width, height uint8) {
		_, _ = decodePlanarBitmap(wire, bitmapFormat{int(width), int(height), 32})
	})
}
