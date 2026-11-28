package rdp

import (
	"bytes"
	"encoding/binary"
)

const (
	dvcCreate      = 1
	dvcDataFirst   = 2
	dvcData        = 3
	dvcClose       = 4
	dvcCaps        = 5
	dvcMaxVersion  = 2
	dvcMessageSize = 64 * 1024
)

type dynamicChannel struct {
	version uint16
	id      uint32
	open    bool
	message channelMessage
	display displayControl
}

// receiveDynamic processes one MS-RDPEDYC PDU. Display Control is the sole
// listener, so one in-flight dynamic message suffices.
func (v *virtualChannels) receiveDynamic(data []byte) error {
	if len(data) == 0 || len(data) > minimumChannelChunk {
		return protocolError("invalid RDP dynamic channel packet length")
	}
	header := data[0]
	if header>>4 == dvcCaps {
		return v.dynamicCapabilities(data)
	}
	if v.dynamic.version == 0 {
		return protocolError("RDP dynamic channel traffic before capabilities")
	}
	p, ok := readDynamicHeader(data)
	if !ok {
		return protocolError("invalid RDP dynamic channel header")
	}
	switch p.cmd {
	case dvcCreate:
		return v.createDynamic(p)
	case dvcClose:
		return v.closeDynamic(p)
	case dvcDataFirst, dvcData:
		return v.dynamicData(p)
	default:
		return protocolError("unsupported RDP dynamic channel command")
	}
}

func (v *virtualChannels) dynamicCapabilities(p []byte) error {
	if len(p) < 4 || p[0]&3 != 0 || p[1] != 0 || v.dynamic.version != 0 {
		return protocolError("invalid RDP dynamic channel capabilities")
	}
	version := binary.LittleEndian.Uint16(p[2:])
	switch version {
	case 1:
		if len(p) != 4 {
			return protocolError("invalid RDP dynamic channel version 1 capabilities")
		}
	case 2, 3:
		if len(p) != 12 {
			return protocolError("invalid RDP dynamic channel priority capabilities")
		}
	default:
		return protocolError("unknown RDP dynamic channel version")
	}
	// Version 2 prioritization is implicit with one listener. Version 3
	// introduces compressed dynamic messages, so negotiate version 2 or lower.
	version = min(version, dvcMaxVersion)
	response := binary.LittleEndian.AppendUint16([]byte{dvcCaps << 4, 0}, version)
	if err := v.sendChannel(v.dvc, response); err != nil {
		return err
	}
	v.dynamic.version = version
	return nil
}

type dynamicPDU struct {
	cmd, extra byte
	idSize     byte
	id         uint32
	idBytes    []byte
	body       []byte
}

func readDynamicHeader(data []byte) (dynamicPDU, bool) {
	head := data[0]
	id, rest, ok := dynamicNumber(data[1:], head&3)
	if !ok {
		return dynamicPDU{}, false
	}
	return dynamicPDU{
		cmd: head >> 4, extra: head >> 2 & 3, idSize: head & 3, id: id,
		idBytes: data[1 : len(data)-len(rest)], body: rest,
	}, true
}

func (p dynamicPDU) replyHeader() []byte {
	return append([]byte{p.cmd<<4 | p.idSize}, p.idBytes...)
}

func dynamicNumber(data []byte, n byte) (uint32, []byte, bool) {
	size := 1 << n
	if n > 2 || len(data) < size {
		return 0, nil, false
	}
	switch size {
	case 1:
		return uint32(data[0]), data[1:], true
	case 2:
		return uint32(binary.LittleEndian.Uint16(data)), data[2:], true
	default:
		return binary.LittleEndian.Uint32(data), data[4:], true
	}
}

func dynamicHeader(cmd byte, id uint32) []byte {
	le := binary.LittleEndian
	switch {
	case id <= 0xff:
		return []byte{cmd << 4, byte(id)}
	case id <= 0xffff:
		return le.AppendUint16([]byte{cmd<<4 | 1}, uint16(id))
	default:
		return le.AppendUint32([]byte{cmd<<4 | 2}, id)
	}
}

func (v *virtualChannels) createDynamic(p dynamicPDU) error {
	name, rest, found := bytes.Cut(p.body, []byte{0})
	if !found || len(name) == 0 || len(rest) != 0 {
		return protocolError("invalid RDP dynamic channel name")
	}
	status := uint32(0x80070490) // HRESULT_FROM_WIN32(ERROR_NOT_FOUND).
	if string(name) == displayChannelName && !v.dynamic.open {
		status = 0
	}
	response := binary.LittleEndian.AppendUint32(p.replyHeader(), status)
	if err := v.sendChannel(v.dvc, response); err != nil {
		return err
	}
	if status == 0 {
		v.dynamic.id, v.dynamic.open = p.id, true
	}
	return nil
}

func (v *virtualChannels) closeDynamic(p dynamicPDU) error {
	if len(p.body) != 0 {
		return protocolError("invalid RDP dynamic channel close")
	}
	if !v.dynamic.open || p.id != v.dynamic.id {
		return nil
	}
	v.dynamic = dynamicChannel{version: v.dynamic.version}
	return v.sendChannel(v.dvc, p.replyHeader())
}

func (v *virtualChannels) dynamicData(p dynamicPDU) error {
	d := &v.dynamic
	if !d.open || p.id != d.id {
		return protocolError("RDP data for unopened dynamic channel")
	}
	if p.cmd == dvcDataFirst {
		total, body, ok := dynamicNumber(p.body, p.extra)
		if !ok || total > dvcMessageSize || len(body) == 0 || uint32(len(body)) > total || d.message.bytes != nil {
			return protocolError("invalid RDP first dynamic channel fragment")
		}
		if uint32(len(body)) == total {
			return d.display.receive(body)
		}
		d.message.start(total, body)
		return nil
	}
	if d.message.bytes != nil {
		body, err := d.message.continueWith(p.body)
		if err != nil || body == nil {
			return err
		}
		return d.display.receive(body)
	}
	return d.display.receive(p.body)
}
