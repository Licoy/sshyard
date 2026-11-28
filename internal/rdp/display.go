package rdp

import "encoding/binary"

const displayChannelName = "Microsoft::Windows::RDS::DisplayControl"

type displayControl struct {
	maxArea uint64
}

// receive processes DISPLAYCONTROL_CAPS_PDU (MS-RDPEDISP 2.2.2.1).
func (d *displayControl) receive(data []byte) error {
	le := binary.LittleEndian
	if len(data) != 20 || le.Uint32(data) != 5 || le.Uint32(data[4:]) != uint32(len(data)) {
		return protocolError("invalid RDP Display Control capabilities")
	}
	count := uint64(le.Uint32(data[8:]))
	a, b := uint64(le.Uint32(data[12:])), uint64(le.Uint32(data[16:]))
	// Values above the maximum one-monitor size don't affect this client.
	// Limit the factors before multiplying by the monitor count.
	const maxArea = 8192 * 8192
	d.maxArea = min(maxArea, count*min(a*b, maxArea))
	return nil
}

// resize sends a one-monitor layout when Display Control supports the mode.
func (v *virtualChannels) resize(width, height, scale int) (bool, error) {
	d := &v.dynamic
	if !d.open || v.suspended || !desktopBounds(width, height) || width%2 != 0 ||
		scale < 100 || scale > 500 || uint64(width)*uint64(height) > d.display.maxArea {
		return false, nil
	}
	return true, v.sendChannel(v.dvc, append(dynamicHeader(dvcData, d.id), displayLayout(width, height, scale)...))
}

func displayLayout(width, height, scale int) []byte {
	deviceScale := 100
	switch {
	case scale >= 180:
		deviceScale = 180
	case scale >= 140:
		deviceScale = 140
	}
	// Derive an approximate physical size from 96 DPI at 100% desktop scale.
	// This session has no physical-monitor metadata.
	millimeters := func(n int) uint32 { return uint32(n * 2540 / (96 * scale)) }
	words := []uint32{
		2, 56, 40, 1, // packet type, length, monitor layout size, monitor count
		1, 0, 0, // primary monitor at the origin
		uint32(width), uint32(height),
		millimeters(width), millimeters(height),
		0, uint32(scale), uint32(deviceScale), // landscape and scale
	}
	data := make([]byte, 0, 56)
	for _, word := range words {
		data = binary.LittleEndian.AppendUint32(data, word)
	}
	return data
}
