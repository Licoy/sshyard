package rdp

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

const (
	pointerCacheEntries = 20
	pointerMaxSize      = 96
	largePointerMaxSize = 384
)

// Pointer is the current remote cursor shape. Image is immutable and may be
// shared with earlier snapshots. HotX and HotY are relative to Image.
type Pointer struct {
	Image           *image.RGBA
	HotX, HotY      int
	Hidden, Default bool
	Version         uint64
}

func (g *graphicsState) slowPointer(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("short RDP pointer update")
	}
	message := binary.LittleEndian.Uint16(data)
	data = data[4:]
	switch message {
	case 1:
		if len(data) != 4 {
			return fmt.Errorf("invalid RDP system pointer")
		}
		switch binary.LittleEndian.Uint32(data) {
		case 0:
			g.publishPointer(Pointer{Hidden: true})
		case 0x7f00:
			g.publishPointer(Pointer{Default: true})
		default:
			return fmt.Errorf("unknown RDP system pointer")
		}
		return nil
	case 3:
		return g.pointerPosition(data)
	case 6:
		return g.pointerShape(data, false, false)
	case 7:
		return g.cachedPointer(data)
	case 8:
		return g.pointerShape(data, true, false)
	default:
		return nil
	}
}

func (g *graphicsState) pointerPosition(data []byte) error {
	if len(data) != 4 {
		return fmt.Errorf("invalid RDP pointer position")
	}
	if g.position != nil {
		le := binary.LittleEndian
		g.position(image.Pt(int(le.Uint16(data)), int(le.Uint16(data[2:]))))
	}
	return nil
}

func (g *graphicsState) publishPointer(p Pointer) {
	g.pointerVersion++
	p.Version = g.pointerVersion
	if g.pointer != nil {
		g.pointer(p)
	}
}

func (g *graphicsState) cachedPointer(data []byte) error {
	if len(data) != 2 {
		return fmt.Errorf("invalid RDP cached pointer index")
	}
	index := int(binary.LittleEndian.Uint16(data))
	if index >= len(g.pointerCache) || g.pointerCache[index].Image == nil {
		return fmt.Errorf("RDP pointer cache entry %d is unavailable", index)
	}
	g.publishPointer(g.pointerCache[index])
	return nil
}

// pointerShape reads the shared MS-RDPBCGR color/new/large pointer fields.
func (g *graphicsState) pointerShape(data []byte, hasDepth, large bool) error {
	depth := 24
	le := binary.LittleEndian
	if hasDepth {
		if len(data) < 2 {
			return fmt.Errorf("short RDP pointer color depth")
		}
		depth, data = int(le.Uint16(data)), data[2:]
	}
	maxSize, headerSize := pointerMaxSize, 14
	if large {
		maxSize, headerSize = largePointerMaxSize, 18
	}
	if len(data) < headerSize {
		return fmt.Errorf("short RDP pointer shape header")
	}
	index := int(le.Uint16(data))
	hotX, hotY := int(le.Uint16(data[2:])), int(le.Uint16(data[4:]))
	width, height := int(le.Uint16(data[6:])), int(le.Uint16(data[8:]))
	if index >= len(g.pointerCache) || width > maxSize || height > maxSize || hotX >= width || hotY >= height {
		return fmt.Errorf("invalid RDP pointer cache index, dimensions or hotspot")
	}
	andSize, xorSize := uint32(le.Uint16(data[10:])), uint32(le.Uint16(data[12:]))
	if large {
		andSize, xorSize = le.Uint32(data[10:]), le.Uint32(data[14:])
	}
	masks := pointerMasks{bitmapFormat{width, height, depth}, andSize, xorSize}
	img, err := g.decodePointerMasks(data[headerSize:], masks)
	if err != nil {
		return err
	}
	p := Pointer{Image: img, HotX: hotX, HotY: hotY}
	g.pointerCache[index] = p
	g.publishPointer(p)
	return nil
}

type pointerMasks struct {
	format  bitmapFormat
	andSize uint32
	xorSize uint32
}

func (g *graphicsState) decodePointerMasks(data []byte, masks pointerMasks) (*image.RGBA, error) {
	f := masks.format
	switch f.depth {
	case 1, 15, 16, 24, 32:
	case 2, 4, 8:
		if !g.hasPalette {
			return nil, fmt.Errorf("RDP indexed pointer received before palette")
		}
	default:
		return nil, fmt.Errorf("unsupported RDP pointer color depth %d", f.depth)
	}
	bits := f.depth
	if bits == 15 {
		bits = 16
	}
	xorStride := ((f.width*bits + 15) / 16) * 2
	andStride := ((f.width + 15) / 16) * 2
	if masks.xorSize != uint32(xorStride*f.height) ||
		(masks.andSize != uint32(andStride*f.height) && !(f.depth == 32 && masks.andSize == 0)) {
		return nil, fmt.Errorf("invalid RDP pointer mask lengths")
	}
	length := int(masks.xorSize + masks.andSize)
	if len(data) != length && len(data) != length+1 {
		return nil, fmt.Errorf("invalid RDP pointer mask data")
	}
	xorData, andData := data[:masks.xorSize], data[masks.xorSize:length]
	img := image.NewRGBA(image.Rect(0, 0, f.width, f.height))
	alpha := f.depth == 32 && pointerUsesAlpha(xorData, andData)
	for y := range f.height {
		wireRow := f.height - 1 - y
		xorRow := xorData[wireRow*xorStride:]
		var andRow []byte
		if len(andData) > 0 {
			andRow = andData[wireRow*andStride:]
		}
		for x := range f.width {
			c := g.pointerColor(xorRow, andRow, x, f.depth, alpha)
			putBitmapColor(img.Pix[y*img.Stride+x*4:], c)
		}
	}
	return img, nil
}

func pointerUsesAlpha(xorData, andData []byte) bool {
	if len(andData) == 0 {
		return true
	}
	for i := 3; i < len(xorData); i += 4 {
		if xorData[i] != 0 {
			return true
		}
	}
	return false
}

func (g *graphicsState) pointerColor(xorRow, andRow []byte, x, depth int, alpha bool) color.RGBA {
	var raw uint32
	if depth < 8 {
		offset := x * depth
		raw = uint32(xorRow[offset/8]>>(8-depth-offset%8)) & ((1 << depth) - 1)
	} else {
		size := (depth + 7) / 8
		raw = bitmapPixel(xorRow[x*size : (x+1)*size])
	}
	c := g.bitmapColor(raw, depth)
	if alpha {
		a := uint16(byte(raw >> 24))
		return color.RGBA{
			R: uint8(uint16(c.R) * a / 255),
			G: uint8(uint16(c.G) * a / 255),
			B: uint8(uint16(c.B) * a / 255),
			A: byte(a),
		}
	}
	if len(andRow) == 0 || andRow[x/8]&(0x80>>(x%8)) == 0 {
		return c
	}
	if c.R|c.G|c.B == 0 {
		return color.RGBA{}
	}
	// An RGBA cursor cannot XOR the pixels underneath it. Neutral grey
	// keeps the common invert-mask pixels visible over black and white.
	return color.RGBA{128, 128, 128, 255}
}
