package rdp

import "encoding/binary"

const (
	vcFirst        = 0x00000001
	vcLast         = 0x00000002
	vcShowProtocol = 0x00000010
	vcSuspend      = 0x00000020
	vcResume       = 0x00000040
	vcCompression  = 0x00ef0000
	vcMaxChunk     = 16256
)

// virtualChannels owns static channel reassembly and the two negotiated
// endpoints. Session serializes access to it and its transport writes.
type virtualChannels struct {
	dvc, clip   uint16
	send        func(uint16, []byte) error
	chunkSize   int
	suspended   bool
	dvcMessage  channelMessage
	clipMessage channelMessage
	dynamic     dynamicChannel
	clipboard   clipboardChannel
}

func newVirtualChannels(channels map[string]uint16, send func(uint16, []byte) error, onClipboard func(string)) *virtualChannels {
	return &virtualChannels{
		dvc: channels["drdynvc"], clip: channels["cliprdr"], send: send,
		chunkSize: minimumChannelChunk, clipboard: clipboardChannel{onText: onClipboard},
	}
}

func (v *virtualChannels) setChunkSize(size int) {
	v.chunkSize = min(max(size, minimumChannelChunk), vcMaxChunk)
}

func (v *virtualChannels) receive(channel uint16, p []byte) error {
	if channel == 0 || (channel != v.dvc && channel != v.clip) {
		return protocolError("unnegotiated RDP virtual channel")
	}
	if len(p) < 8 {
		return protocolError("short RDP virtual channel header")
	}
	le := binary.LittleEndian
	total, flags := le.Uint32(p), le.Uint32(p[4:])
	p = p[8:]
	if flags&vcCompression != 0 {
		return protocolError("RDP virtual channel compression was not negotiated")
	}
	if flags&(vcSuspend|vcResume) != 0 {
		return v.channelControl(flags, p)
	}
	if v.suspended || len(p) > v.chunkSize {
		return protocolError("invalid RDP virtual channel payload")
	}
	if channel == v.dvc {
		body, err := v.dvcMessage.staticChunk(p, total, minimumChannelChunk, flags)
		if err != nil || body == nil {
			return err
		}
		return v.receiveDynamic(body)
	}
	body, err := v.clipMessage.staticChunk(p, total, maxClipboardPDU, flags)
	if err != nil || body == nil {
		return err
	}
	return v.receiveClipboard(body)
}

func (v *virtualChannels) channelControl(flags uint32, body []byte) error {
	if len(body) != 0 || flags&(vcSuspend|vcResume) == vcSuspend|vcResume {
		return protocolError("invalid RDP virtual channel control PDU")
	}
	v.suspended = flags&vcSuspend != 0
	if !v.suspended {
		return v.announceClipboard()
	}
	return nil
}

// sendChannel wraps payload in CHANNEL_PDU_HEADER (MS-RDPBCGR 2.2.6.1.1).
func (v *virtualChannels) sendChannel(channel uint16, payload []byte) error {
	if channel == 0 || v.suspended {
		return protocolError("RDP virtual channel is unavailable")
	}
	le := binary.LittleEndian
	for offset := 0; ; {
		end := min(len(payload), offset+v.chunkSize)
		flags := uint32(vcShowProtocol)
		if offset == 0 {
			flags |= vcFirst
		}
		if end == len(payload) {
			flags |= vcLast
		}
		packet := make([]byte, 8+end-offset)
		le.PutUint32(packet, uint32(len(payload)))
		le.PutUint32(packet[4:], flags)
		copy(packet[8:], payload[offset:end])
		if err := v.send(channel, packet); err != nil {
			return err
		}
		if end == len(payload) {
			return nil
		}
		offset = end
	}
}

// channelMessage bounds allocation to a validated, per-endpoint advertised
// length. Only one message can be in flight per endpoint.
type channelMessage struct {
	total uint32
	bytes []byte
}

func (m *channelMessage) staticChunk(body []byte, total uint32, limit int, flags uint32) ([]byte, error) {
	if total > uint32(limit) || uint32(len(body)) > total || len(body) == 0 {
		return nil, protocolError("invalid RDP virtual channel message length")
	}
	if flags&vcFirst != 0 {
		if m.bytes != nil || (uint32(len(body)) == total) != (flags&vcLast != 0) {
			return nil, protocolError("invalid RDP virtual channel first fragment")
		}
		if flags&vcLast != 0 {
			return body, nil
		}
		m.start(total, body)
		return nil, nil
	}
	if m.bytes == nil || total != m.total || (len(m.bytes)+len(body) == int(total)) != (flags&vcLast != 0) {
		return nil, protocolError("invalid RDP virtual channel continuation")
	}
	return m.continueWith(body)
}

func (m *channelMessage) start(total uint32, body []byte) {
	m.total = total
	m.bytes = make([]byte, len(body), int(total))
	copy(m.bytes, body)
}

func (m *channelMessage) continueWith(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > int(m.total)-len(m.bytes) {
		return nil, protocolError("RDP virtual channel fragments exceed message length")
	}
	m.bytes = append(m.bytes, body...)
	if len(m.bytes) != int(m.total) {
		return nil, nil
	}
	data := m.bytes
	*m = channelMessage{}
	return data, nil
}
