package rdp

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

const (
	inputScan byte = iota
	inputMouse
	inputMouseX
	inputSync
	inputUnicode
)

const (
	inputRelease   = 0x01
	inputExtended  = 0x02
	inputExtended1 = 0x04

	pointerMove      = 0x0800
	pointerButton1   = 0x1000
	pointerButton2   = 0x2000
	pointerButton3   = 0x4000
	pointerDown      = 0x8000
	pointerXButton1  = 0x0001
	pointerXButton2  = 0x0002
	pointerWheel     = 0x0200
	pointerHWheel    = 0x0400
	pointerWheelMask = 0x01ff
	maxInputEvents   = 255
)

type wireInput struct {
	kind, flags byte
	data        [3]uint16
}

func scanInput(code uint8, extended, extended1, pressed bool) wireInput {
	event := wireInput{kind: inputScan, data: [3]uint16{uint16(code)}}
	if !pressed {
		event.flags |= inputRelease
	}
	if extended {
		event.flags |= inputExtended
	}
	if extended1 {
		event.flags |= inputExtended1
	}
	return event
}

func unicodeInput(r rune, pressed bool) []wireInput {
	event := wireInput{kind: inputUnicode}
	if !pressed {
		event.flags = inputRelease
	}
	var units [2]uint16
	encoded := utf16.AppendRune(units[:0], r)
	event.data[0] = encoded[0]
	if len(encoded) == 1 {
		return []wireInput{event}
	}
	next := event
	next.data[0] = encoded[1]
	return []wireInput{event, next}
}

func pointerInput(flags, x, y uint16, extended bool) wireInput {
	event := wireInput{kind: inputMouse, data: [3]uint16{flags, x, y}}
	if extended {
		event.kind = inputMouseX
	}
	return event
}

func syncInput(flags uint8) wireInput {
	return wireInput{kind: inputSync, flags: flags & 0x0f}
}

// encodeSlowInput builds TS_INPUT_PDU_DATA (MS-RDPBCGR 2.2.8.1.1.3.1).
// The caller supplies the Share Data and transport headers.
func encodeSlowInput(events []wireInput) ([]byte, error) {
	if len(events) == 0 || len(events) > maxInputEvents {
		return nil, errors.New("RDP input event count is out of range")
	}
	packet := make([]byte, 4+12*len(events))
	le := binary.LittleEndian
	le.PutUint16(packet, uint16(len(events)))
	for i, event := range events {
		record := packet[4+12*i:]
		switch event.kind {
		case inputScan, inputUnicode:
			messageType := uint16(4)
			if event.kind == inputUnicode {
				messageType = 5
			}
			flags := uint16(event.flags&inputRelease)<<15 | uint16(event.flags&(inputExtended|inputExtended1))<<7
			le.PutUint16(record[4:], messageType)
			le.PutUint16(record[6:], flags)
			le.PutUint16(record[8:], event.data[0])
		case inputMouse, inputMouseX:
			le.PutUint16(record[4:], 0x8001+uint16(event.kind-inputMouse))
			for j, word := range event.data {
				le.PutUint16(record[6+2*j:], word)
			}
		case inputSync:
			le.PutUint32(record[8:], uint32(event.flags))
		default:
			return nil, errors.New("unknown RDP input event")
		}
	}
	return packet, nil
}

// encodeFastInput builds a complete TLS-protected TS_FP_INPUT_PDU
// (MS-RDPBCGR 2.2.8.1.2).
func encodeFastInput(events []wireInput) ([]byte, error) {
	if len(events) == 0 || len(events) > maxInputEvents {
		return nil, errors.New("RDP input event count is out of range")
	}
	packet := make([]byte, 3, 3+1+7*len(events))
	if len(events) <= 15 {
		packet[0] = byte(len(events)) << 2
	} else {
		packet = append(packet, byte(len(events)))
	}
	le := binary.LittleEndian
	for _, event := range events {
		packet = append(packet, event.kind<<5|event.flags)
		switch event.kind {
		case inputScan:
			packet = append(packet, byte(event.data[0]))
		case inputUnicode:
			packet = le.AppendUint16(packet, event.data[0])
		case inputMouse, inputMouseX:
			for _, word := range event.data {
				packet = le.AppendUint16(packet, word)
			}
		case inputSync:
		default:
			return nil, errors.New("unknown RDP input event")
		}
	}
	if len(packet)-1 <= 127 {
		copy(packet[2:], packet[3:])
		packet = packet[:len(packet)-1]
		packet[1] = byte(len(packet))
	} else {
		binary.BigEndian.PutUint16(packet[1:], uint16(len(packet))|0x8000)
	}
	return packet, nil
}
