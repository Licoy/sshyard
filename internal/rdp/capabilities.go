package rdp

import "encoding/binary"

const (
	capGeneral          = 1
	capBitmap           = 2
	capOrder            = 3
	capPointer          = 8
	capShare            = 9
	capColorCache       = 10
	capSound            = 12
	capInput            = 13
	capFont             = 14
	capBrush            = 15
	capGlyphCache       = 16
	capOffscreen        = 17
	capBitmapCacheRev2  = 19
	capVirtualChannel   = 20
	capMultiFragment    = 26
	capLargePointer     = 27
	capBitmapCodecs     = 29
	inputFlagScancode   = 0x0001
	inputFlagMouseX     = 0x0004
	inputFlagFast       = 0x0008
	inputFlagUnicode    = 0x0010
	inputFlagFast2      = 0x0020
	inputFlagHWheel     = 0x0100
	minimumChannelChunk = 1600
)

type serverCapabilities struct {
	shareID                uint32
	width, height, bpp     int
	input                  uint16
	refresh, suppress, arc bool
	chunk                  int
}

// demandActive reads TS_DEMAND_ACTIVE_PDU (MS-RDPBCGR 2.2.1.13.1.1).
func demandActive(p []byte) (serverCapabilities, error) {
	var c serverCapabilities
	if len(p) < 16 {
		return c, protocolError("truncated RDP Demand Active PDU")
	}
	le := binary.LittleEndian
	c.shareID = le.Uint32(p)
	source, combined := int(le.Uint16(p[4:])), int(le.Uint16(p[6:]))
	if combined < 4 || 8+source+combined+4 != len(p) {
		return c, protocolError("invalid RDP Demand Active capabilities length")
	}
	p = p[8+source : len(p)-4] // sessionId is ignored.
	count := int(le.Uint16(p))
	p = p[4:]
	c.chunk = minimumChannelChunk
	seen := map[uint16]bool{}
	for range count {
		if len(p) < 4 {
			return c, protocolError("truncated RDP capability set header")
		}
		kind, size := le.Uint16(p), int(le.Uint16(p[2:]))
		if size < 4 || size > len(p) || seen[kind] {
			return c, protocolError("invalid or duplicate RDP capability set")
		}
		seen[kind] = true
		if err := c.readCapability(kind, p[4:size]); err != nil {
			return c, err
		}
		p = p[size:]
	}
	if len(p) != 0 || !seen[capGeneral] || !seen[capBitmap] {
		return c, protocolError("invalid RDP server capabilities")
	}
	return c, nil
}

func (c *serverCapabilities) readCapability(kind uint16, p []byte) error {
	le := binary.LittleEndian
	switch kind {
	case capGeneral:
		if len(p) < 20 || le.Uint16(p[4:]) != 0x0200 {
			return protocolError("invalid RDP General capability")
		}
		c.arc = le.Uint16(p[10:])&0x0008 != 0
		c.refresh, c.suppress = p[18] != 0, p[19] != 0
	case capBitmap:
		if len(p) < 24 {
			return protocolError("truncated RDP Bitmap capability")
		}
		c.bpp = int(le.Uint16(p))
		c.width, c.height = int(le.Uint16(p[8:])), int(le.Uint16(p[10:]))
		if c.bpp != 8 && c.bpp != 15 && c.bpp != 16 && c.bpp != 24 && c.bpp != 32 || !desktopBounds(c.width, c.height) {
			return protocolError("unsupported RDP desktop size or color depth")
		}
	case capInput:
		if len(p) < 84 {
			return protocolError("truncated RDP Input capability")
		}
		c.input = le.Uint16(p)
	case capVirtualChannel:
		if len(p) != 4 && len(p) != 8 {
			return protocolError("invalid RDP Virtual Channel capability")
		}
		if len(p) == 8 {
			n := le.Uint32(p[4:])
			if n < minimumChannelChunk || n > 16256 {
				return protocolError("RDP virtual channel chunk size is out of bounds")
			}
			c.chunk = int(n)
		}
	}
	return nil
}

func confirmActive(userID uint16, cfg Config, caps serverCapabilities) []byte {
	sets, count := encodeCapabilities(cfg, caps)
	le := binary.LittleEndian
	p := le.AppendUint32(nil, caps.shareID)
	p = le.AppendUint16(p, serverChannelID)
	p = le.AppendUint16(p, 4)
	p = le.AppendUint16(p, uint16(len(sets)+4))
	p = append(p, 'R', 'D', 'P', 0)
	p = le.AppendUint16(p, count)
	p = append(p, 0, 0)
	return controlPDU(userID, shareConfirmActive, append(p, sets...))
}

// encodeCapabilities advertises bitmap updates and positional input (MS-RDPBCGR 2.2.7).
func encodeCapabilities(cfg Config, caps serverCapabilities) ([]byte, uint16) {
	le := binary.LittleEndian
	general := words(0, 0, 0x0200, 0, 0, 0x040d, 0, 0, 0, 0) // fast output, long credentials, ARC, compact bitmap headers
	bitmap := words(uint16(caps.bpp), 1, 1, 1, uint16(caps.width), uint16(caps.height), 0, 1, 1, 0, 1, 0)
	order := make([]byte, 84)
	le.PutUint16(order[20:], 1)      // X save granularity
	le.PutUint16(order[22:], 20)     // Y save granularity
	le.PutUint16(order[26:], 1)      // maximumOrderLevel is ignored, defaults to ORD_LEVEL_1_ORDERS
	le.PutUint16(order[30:], 0x000a) // NEGOTIATEORDERSUPPORT | ZEROBOUNDSDELTASSUPPORT
	input := make([]byte, 84)
	le.PutUint16(input, inputFlagScancode|inputFlagMouseX|inputFlagFast|inputFlagUnicode|inputFlagFast2|inputFlagHWheel)
	le.PutUint32(input[4:], cfg.KeyboardLayout)
	le.PutUint32(input[8:], 4) // Enhanced keyboard
	le.PutUint32(input[16:], 12)
	sets := []struct {
		kind uint16
		data []byte
	}{
		{capGeneral, general}, {capBitmap, bitmap}, {capOrder, order},
		{capBitmapCacheRev2, make([]byte, 36)}, {capPointer, words(1, pointerCacheEntries, pointerCacheEntries)},
		{capInput, input}, {capBrush, make([]byte, 4)}, {capGlyphCache, make([]byte, 48)},
		{capOffscreen, make([]byte, 8)}, {capVirtualChannel, words(0, 0, minimumChannelChunk, 0)}, {capSound, words(0, 0)},
		{capShare, words(0, 0)}, {capFont, words(1, 0)}, {capColorCache, words(6, 0)},
		{capMultiFragment, le.AppendUint32(nil, maxFastUpdateBytes)}, {capLargePointer, words(3)}, {capBitmapCodecs, []byte{0}},
	}
	var p []byte
	for _, cap := range sets {
		p = le.AppendUint16(p, cap.kind)
		p = le.AppendUint16(p, uint16(len(cap.data)+4))
		p = append(p, cap.data...)
	}
	return p, uint16(len(sets))
}

func words(v ...uint16) []byte {
	p := make([]byte, 0, 2*len(v))
	for _, n := range v {
		p = binary.LittleEndian.AppendUint16(p, n)
	}
	return p
}
