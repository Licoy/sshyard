package rdp

import "encoding/binary"

func appendMCSBERLength(p []byte, n int) []byte {
	switch {
	case n < 0x80:
		return append(p, byte(n))
	case n <= 0xff:
		return append(p, 0x81, byte(n))
	default:
		return binary.BigEndian.AppendUint16(append(p, 0x82), uint16(n))
	}
}

func mcsBERField(tag byte, p []byte) []byte {
	return append(appendMCSBERLength([]byte{tag}, len(p)), p...)
}

func mcsBERLength(p []byte) ([]byte, int, bool) {
	if len(p) == 0 {
		return nil, 0, false
	}
	if p[0] < 0x80 {
		return p[1:], int(p[0]), true
	}
	width := int(p[0] & 0x7f)
	if width == 0 || width > 2 || len(p) <= width {
		return nil, 0, false
	}
	length := 0
	for _, b := range p[1 : 1+width] {
		length = length<<8 | int(b)
	}
	return p[1+width:], length, true
}

func takeMCSBER(p []byte, tag byte) ([]byte, []byte, bool) {
	if len(p) == 0 || p[0] != tag {
		return nil, nil, false
	}
	p, n, ok := mcsBERLength(p[1:])
	if !ok || n > len(p) {
		return nil, nil, false
	}
	return p[:n], p[n:], true
}

func appendMCSLength(p []byte, n int) []byte {
	if n < 0x80 {
		return append(p, byte(n))
	}
	return binary.BigEndian.AppendUint16(p, uint16(n)|0x8000)
}

func mcsLength(p []byte) ([]byte, int, bool) {
	if len(p) == 0 {
		return nil, 0, false
	}
	if p[0]&0x80 == 0 {
		return p[1:], int(p[0]), true
	}
	if len(p) < 2 || p[0]&0xc0 == 0xc0 {
		return nil, 0, false
	}
	return p[2:], int(binary.BigEndian.Uint16(p[:2]) & 0x3fff), true
}

func appendMCSData(dst, data []byte) []byte {
	// X.691 aligned PER fragments octet strings into multiples of 16K.
	for len(data) >= 16384 {
		factor := min(len(data)/16384, 4)
		dst = append(dst, 0xc0|byte(factor))
		end := factor * 16384
		dst = append(dst, data[:end]...)
		data = data[end:]
	}
	return append(appendMCSLength(dst, len(data)), data...)
}

func takeMCSData(p []byte) ([]byte, bool) {
	var fragments []byte
	for len(p) != 0 && p[0]&0xc0 == 0xc0 {
		factor := int(p[0] & 0x3f)
		end := 1 + factor*16384
		if factor == 0 || factor > 4 || end > len(p) {
			return nil, false
		}
		fragments = append(fragments, p[1:end]...)
		p = p[end:]
	}
	p, n, ok := mcsLength(p)
	if !ok || len(p) != n {
		return nil, false
	}
	if fragments != nil {
		p = append(fragments, p...)
	}
	return p, true
}
