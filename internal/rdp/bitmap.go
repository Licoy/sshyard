package rdp

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

const (
	bitmapCompressed = 0x0001
	bitmapNoHeader   = 0x0400
	maxBitmapPixels  = 16 * 1024 * 1024
)

type bitmapFormat struct {
	width, height, depth int
}

func (f bitmapFormat) pixelCount() (int, error) {
	if f.width < 1 || f.height < 1 || f.width > maxBitmapPixels/f.height {
		return 0, fmt.Errorf("invalid RDP bitmap dimensions %dx%d", f.width, f.height)
	}
	return f.width * f.height, nil
}

func (g *graphicsState) bitmapUpdate(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("short RDP bitmap update")
	}
	count := int(binary.LittleEndian.Uint16(data))
	data = data[2:]
	budget := maxBitmapPixels
	for i := 0; i < count; i++ {
		rest, err := g.bitmapRectangle(data, &budget)
		if err != nil {
			return fmt.Errorf("RDP bitmap rectangle %d: %w", i, err)
		}
		data = rest
	}
	if len(data) != 0 {
		return fmt.Errorf("trailing RDP bitmap update data")
	}
	return nil
}

func (g *graphicsState) bitmapRectangle(data []byte, budget *int) ([]byte, error) {
	if len(data) < 18 {
		return nil, fmt.Errorf("short bitmap header")
	}
	le := binary.LittleEndian
	left, top := int(le.Uint16(data)), int(le.Uint16(data[2:]))
	right, bottom := int(le.Uint16(data[4:]))+1, int(le.Uint16(data[6:]))+1
	f := bitmapFormat{int(le.Uint16(data[8:])), int(le.Uint16(data[10:])), int(le.Uint16(data[12:]))}
	flags, size := le.Uint16(data[14:]), int(le.Uint16(data[16:]))
	count, err := f.pixelCount()
	if err != nil {
		return nil, err
	}
	if right <= left || bottom <= top || right-left > f.width || bottom-top > f.height {
		return nil, fmt.Errorf("invalid bitmap destination")
	}
	if count > *budget || size > len(data)-18 {
		return nil, fmt.Errorf("bitmap exceeds update bounds")
	}
	*budget -= count
	data = data[18:]
	body := data[:size]
	compressed := flags&bitmapCompressed != 0
	if compressed && flags&bitmapNoHeader == 0 {
		if len(body) < 8 || le.Uint16(body) != 0 || int(le.Uint16(body[2:])) != len(body)-8 {
			return nil, fmt.Errorf("invalid bitmap compression header")
		}
		body = body[8:]
	}
	img, err := g.decodeBitmap(body, f, compressed)
	if err != nil {
		return nil, err
	}
	if g.bitmap != nil {
		if err := g.bitmap(image.Rect(left, top, right, bottom), img); err != nil {
			return nil, err
		}
	}
	return data[size:], nil
}

func (g *graphicsState) decodeBitmap(data []byte, f bitmapFormat, compressed bool) (*image.RGBA, error) {
	switch f.depth {
	case 8:
		if !g.hasPalette {
			return nil, fmt.Errorf("RDP indexed bitmap received before palette")
		}
	case 15, 16, 24, 32:
	default:
		return nil, fmt.Errorf("unsupported RDP bitmap depth %d", f.depth)
	}
	if compressed {
		if f.depth == 32 {
			return decodePlanarBitmap(data, f)
		}
		return g.decodeInterleavedBitmap(data, f)
	}
	return g.decodeRawBitmap(data, f)
}

func (g *graphicsState) decodeRawBitmap(data []byte, f bitmapFormat) (*image.RGBA, error) {
	if _, err := f.pixelCount(); err != nil {
		return nil, err
	}
	pixelSize := (f.depth + 7) / 8
	stride := (f.width*pixelSize + 3) &^ 3
	if len(data) != stride*f.height {
		return nil, fmt.Errorf("invalid uncompressed RDP bitmap size")
	}
	img := image.NewRGBA(image.Rect(0, 0, f.width, f.height))
	for y := range f.height {
		row := data[(f.height-1-y)*stride:]
		pixels := img.Pix[y*img.Stride:]
		for x := range f.width {
			raw := bitmapPixel(row[x*pixelSize : x*pixelSize+pixelSize])
			putBitmapColor(pixels[x*4:], g.bitmapColor(raw, f.depth))
		}
	}
	return img, nil
}

// bitmapPixel preserves the wire pixel so RLE can XOR palette indexes and
// packed RGB values before color conversion.
func bitmapPixel(data []byte) uint32 {
	var value uint32
	for i, b := range data {
		value |= uint32(b) << (8 * i)
	}
	return value
}

func (g *graphicsState) bitmapColor(v uint32, bpp int) color.RGBA {
	switch bpp {
	case 1:
		if v != 0 {
			return color.RGBA{255, 255, 255, 255}
		}
		return color.RGBA{A: 255}
	case 2, 4, 8:
		return g.palette[byte(v)]
	case 15, 16:
		greenBits := bpp - 10
		r := uint8(v>>(greenBits+5)) & 31
		gr := uint8(v>>5) & uint8((1<<greenBits)-1)
		b := uint8(v) & 31
		return color.RGBA{r<<3 | r>>2, gr<<(8-greenBits) | gr>>(2*greenBits-8), b<<3 | b>>2, 255}
	default:
		return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
	}
}

func putBitmapColor(dst []byte, c color.RGBA) {
	dst[0], dst[1], dst[2], dst[3] = c.R, c.G, c.B, c.A
}
