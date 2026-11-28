package rdp

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

// maxFastUpdateBytes is also the advertised Multifragment Update capability.
const maxFastUpdateBytes = 4 * 1024 * 1024

type graphicsState struct {
	bitmap   func(image.Rectangle, *image.RGBA) error
	pointer  func(Pointer)
	position func(image.Point)

	palette        [256]color.RGBA
	hasPalette     bool
	pointerCache   [pointerCacheEntries]Pointer
	pointerVersion uint64

	fastFragment     []byte
	fastFragmentType byte
}

func (g *graphicsState) slowUpdate(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("short RDP graphics update")
	}
	switch binary.LittleEndian.Uint16(data) {
	case 0:
		return fmt.Errorf("RDP drawing orders were not negotiated")
	case 1:
		return g.bitmapUpdate(data[2:])
	case 2:
		return g.paletteUpdate(data[2:])
	default:
		// Synchronize and unrecognized update types can be ignored (MS-RDPBCGR 3.2.5.9.1).
		return nil
	}
}

func (g *graphicsState) paletteUpdate(data []byte) error {
	if len(data) != 6+3*len(g.palette) || binary.LittleEndian.Uint32(data[2:]) != uint32(len(g.palette)) {
		return fmt.Errorf("invalid RDP palette update")
	}
	for i := range g.palette {
		offset := 6 + 3*i
		g.palette[i] = color.RGBA{data[offset], data[offset+1], data[offset+2], 255}
	}
	g.hasPalette = true
	return nil
}

// fastOutput processes the update sequence after the fast-path frame header.
func (g *graphicsState) fastOutput(data []byte) error {
	for len(data) > 0 {
		head := data[0]
		data = data[1:]
		switch head >> 6 {
		case 0:
		case 2:
			if len(data) == 0 {
				return fmt.Errorf("short RDP fast-path compression flags")
			}
			// Only PACKET_COMPRESSED says the data is: a server may flag a
			// flush (or "at front") with no compression negotiated.
			if data[0]&0x20 != 0 {
				return fmt.Errorf("RDP bulk compression was not negotiated")
			}
			data = data[1:]
		default:
			return fmt.Errorf("invalid RDP fast-path compression indicator")
		}
		if len(data) < 2 {
			return fmt.Errorf("short RDP fast-path update size")
		}
		size := int(binary.LittleEndian.Uint16(data))
		data = data[2:]
		if size > len(data) {
			return fmt.Errorf("short RDP fast-path update")
		}
		if err := g.fastUpdateFragment(head, data[:size]); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}

func (g *graphicsState) fastUpdateFragment(head byte, data []byte) error {
	kind, fragment := head&0x0f, head>>4&3
	if len(data) > maxFastUpdateBytes-len(g.fastFragment) {
		return fmt.Errorf("RDP fast-path fragment sequence exceeds %d bytes", maxFastUpdateBytes)
	}
	switch fragment {
	case 0, 2: // SINGLE, FIRST
		if g.fastFragment != nil {
			return fmt.Errorf("interrupted RDP fast-path fragment sequence")
		}
		if fragment == 0 {
			return g.fastUpdate(kind, data)
		}
		g.fastFragment = make([]byte, 0, len(data))
		g.fastFragmentType = head &^ 0x30
	case 1, 3: // LAST, NEXT
		if g.fastFragment == nil || g.fastFragmentType != head&^0x30 {
			return fmt.Errorf("invalid RDP fast-path fragment sequence")
		}
	}
	needed := len(g.fastFragment) + len(data)
	if needed > cap(g.fastFragment) {
		buf := make([]byte, len(g.fastFragment), min(maxFastUpdateBytes, max(needed, 2*cap(g.fastFragment))))
		copy(buf, g.fastFragment)
		g.fastFragment = buf
	}
	g.fastFragment = append(g.fastFragment, data...)
	if fragment == 1 {
		data, g.fastFragment = g.fastFragment, nil
		return g.fastUpdate(kind, data)
	}
	return nil
}

func (g *graphicsState) fastUpdate(kind byte, data []byte) error {
	switch kind {
	case 0:
		return fmt.Errorf("RDP drawing orders were not negotiated")
	case 1, 2:
		if len(data) < 2 || binary.LittleEndian.Uint16(data) != uint16(kind) {
			return fmt.Errorf("invalid RDP fast-path graphics update type")
		}
		return g.slowUpdate(data)
	case 3:
		return nil
	case 4:
		return fmt.Errorf("RDP surface commands were not negotiated")
	case 5, 6:
		if len(data) != 0 {
			return fmt.Errorf("invalid RDP fast-path system pointer")
		}
		g.publishPointer(Pointer{Hidden: kind == 5, Default: kind == 6})
		return nil
	case 8:
		return g.pointerPosition(data)
	case 9:
		return g.pointerShape(data, false, false)
	case 10:
		return g.cachedPointer(data)
	case 11, 12:
		return g.pointerShape(data, true, kind == 12)
	default:
		return nil
	}
}
