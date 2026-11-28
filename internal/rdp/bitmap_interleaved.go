package rdp

import (
	"encoding/binary"
	"fmt"
	"image"
)

const (
	rleBackground byte = iota
	rleForeground
	rleFgBg
	rleSolid
	rleLiteral
	rleSetForeground
	rleSetFgBg
	rleDither
	rleSpecialFgBg1
	rleSpecialFgBg2
	rleWhite
	rleBlack
)

type interleavedOrder struct {
	code byte
	size int
}

func readInterleavedOrder(data []byte) (order interleavedOrder, rest []byte, err error) {
	head := data[0]
	data = data[1:]
	switch {
	case head < 0xc0:
		order = interleavedOrder{head >> 5, int(head & 31)}
	case head < 0xf0:
		order = interleavedOrder{rleSetForeground + (head>>4 - 0xc), int(head & 15)}
	default:
		return readExtendedInterleaved(head, data)
	}

	fgBg := order.code == rleFgBg || order.code == rleSetFgBg
	if order.size == 0 {
		if len(data) == 0 {
			return order, nil, fmt.Errorf("short RDP bitmap RLE run")
		}
		order.size = int(data[0]) + 1
		if !fgBg {
			order.size = int(data[0]) + 32
			if head >= 0xc0 {
				order.size = int(data[0]) + 16
			}
		}
		data = data[1:]
	} else if fgBg {
		order.size *= 8
	}
	return order, data, nil
}

func readExtendedInterleaved(head byte, data []byte) (interleavedOrder, []byte, error) {
	switch head {
	case 0xf9, 0xfa:
		return interleavedOrder{rleSpecialFgBg1 + head - 0xf9, 8}, data, nil
	case 0xfd, 0xfe:
		return interleavedOrder{rleWhite + head - 0xfd, 1}, data, nil
	case 0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf6, 0xf7, 0xf8:
		if len(data) >= 2 {
			code := head - 0xf0
			if head >= 0xf6 {
				code--
			}
			return interleavedOrder{code, int(binary.LittleEndian.Uint16(data))}, data[2:], nil
		}
	}
	return interleavedOrder{}, nil, fmt.Errorf("invalid RDP bitmap RLE order %#x", head)
}

type interleavedBitmap struct {
	pixels             []uint32
	width, limit, size int
	foreground, white  uint32
	previousBackground bool
	previousFirstRow   bool
}

func (g *graphicsState) decodeInterleavedBitmap(data []byte, f bitmapFormat) (*image.RGBA, error) {
	count, err := f.pixelCount()
	if err != nil {
		return nil, err
	}
	b := interleavedBitmap{
		pixels: make([]uint32, 0, min(count, 4096)),
		width:  f.width, limit: count, size: (f.depth + 7) / 8,
		white: (1 << f.depth) - 1,
	}
	b.foreground = b.white
	for len(data) > 0 {
		order, rest, err := readInterleavedOrder(data)
		if err != nil {
			return nil, err
		}
		data, err = b.decodeOrder(order, rest)
		if err != nil {
			return nil, err
		}
	}
	if len(b.pixels) != count {
		return nil, fmt.Errorf("RDP bitmap RLE produced %d of %d pixels", len(b.pixels), count)
	}
	img := image.NewRGBA(image.Rect(0, 0, f.width, f.height))
	for y := range f.height {
		row := b.pixels[(f.height-1-y)*f.width:]
		pixels := img.Pix[y*img.Stride:]
		for x := range f.width {
			putBitmapColor(pixels[x*4:], g.bitmapColor(row[x], f.depth))
		}
	}
	return img, nil
}

func (b *interleavedBitmap) decodeOrder(order interleavedOrder, data []byte) ([]byte, error) {
	n := order.size
	if order.code == rleDither {
		n *= 2
	}
	if n == 0 || n > b.limit-len(b.pixels) {
		return nil, fmt.Errorf("RDP bitmap RLE run exceeds bitmap dimensions")
	}
	if needed := len(b.pixels) + n; needed > cap(b.pixels) {
		pixels := make([]uint32, len(b.pixels), min(b.limit, max(needed, 2*cap(b.pixels))))
		copy(pixels, b.pixels)
		b.pixels = pixels
	}
	firstRow := len(b.pixels) < b.width
	insertForeground := b.previousBackground && firstRow == b.previousFirstRow
	b.previousBackground, b.previousFirstRow = order.code == rleBackground, firstRow

	// Set orders prefix the usual run data with a single foreground color.
	switch order.code {
	case rleSetForeground, rleSetFgBg:
		if len(data) < b.size {
			return nil, fmt.Errorf("short RDP bitmap RLE foreground color")
		}
		b.foreground = bitmapPixel(data[:b.size])
		data = data[b.size:]
	}
	switch order.code {
	case rleBackground:
		for i := range n {
			b.appendRelative(insertForeground && i == 0)
		}
	case rleForeground, rleSetForeground:
		for range n {
			b.appendRelative(true)
		}
	case rleFgBg, rleSetFgBg:
		masks := (n + 7) / 8
		if len(data) < masks {
			return nil, fmt.Errorf("short RDP bitmap RLE image mask")
		}
		b.appendMask(data[:masks], n)
		data = data[masks:]
	case rleSpecialFgBg1:
		b.appendMask([]byte{0x03}, n)
	case rleSpecialFgBg2:
		b.appendMask([]byte{0x05}, n)
	case rleWhite:
		b.pixels = append(b.pixels, b.white)
	case rleBlack:
		b.pixels = append(b.pixels, 0)
	case rleSolid, rleLiteral, rleDither:
		return b.appendColors(order.code, n, data)
	default:
		return nil, fmt.Errorf("invalid RDP bitmap RLE order")
	}
	return data, nil
}

func (b *interleavedBitmap) appendRelative(foreground bool) {
	var value uint32
	if i := len(b.pixels); i >= b.width {
		value = b.pixels[i-b.width]
	}
	if foreground {
		value ^= b.foreground
	}
	b.pixels = append(b.pixels, value)
}

func (b *interleavedBitmap) appendMask(mask []byte, n int) {
	for i := range n {
		b.appendRelative(mask[i/8]&(1<<(i%8)) != 0)
	}
}

func (b *interleavedBitmap) appendColors(code byte, n int, data []byte) ([]byte, error) {
	colors := n
	switch code {
	case rleSolid:
		colors = 1
	case rleDither:
		colors = 2
	}
	length := colors * b.size
	if len(data) < length {
		return nil, fmt.Errorf("short RDP bitmap RLE color run")
	}
	switch code {
	case rleSolid:
		value := bitmapPixel(data[:b.size])
		for range n {
			b.pixels = append(b.pixels, value)
		}
	case rleDither:
		a, c := bitmapPixel(data[:b.size]), bitmapPixel(data[b.size:length])
		for range n / 2 {
			b.pixels = append(b.pixels, a, c)
		}
	default:
		for i := 0; i < length; i += b.size {
			b.pixels = append(b.pixels, bitmapPixel(data[i:i+b.size]))
		}
	}
	return data[length:], nil
}
