package rdp

import "encoding/binary"

const (
	clipMonitorReady = 1
	clipFormatList   = 2
	clipListResponse = 3
	clipDataRequest  = 4
	clipDataResponse = 5
	clipCaps         = 7
	clipFileRequest  = 8
	clipFileResponse = 9

	clipOK        = 0x01
	clipFail      = 0x02
	clipLongNames = 0x02
	clipText      = 1
	clipUnicode   = 13
	clipMaxData   = 1024 * 1024

	maxClipboardPDU = 8 + clipMaxData
)

type clipboardChannel struct {
	onText func(string)

	gotCaps    bool
	longNames  bool
	ready      bool
	needsOffer bool
	offered    bool
	unicode    []byte
	ansi       []byte

	pending    uint32
	next       uint32
	superseded bool
}

type clipboardInputError struct{ message string }

func (e *clipboardInputError) Error() string { return e.message }

// receiveClipboard processes MS-RDPECLIP control and data PDUs.
func (v *virtualChannels) receiveClipboard(p []byte) error {
	le := binary.LittleEndian
	if len(p) < 8 || len(p) > maxClipboardPDU || le.Uint32(p[4:]) != uint32(len(p)-8) {
		return protocolError("invalid RDP clipboard header")
	}
	kind, flags, data := le.Uint16(p), le.Uint16(p[2:]), p[8:]
	c := &v.clipboard
	switch kind {
	case clipCaps:
		if c.ready || c.gotCaps || flags != 0 {
			return protocolError("unexpected RDP clipboard capabilities")
		}
		longNames, err := clipboardCaps(data)
		if err != nil {
			return err
		}
		c.gotCaps, c.longNames = true, longNames
	case clipMonitorReady:
		if c.ready || len(data) != 0 || flags != 0 {
			return protocolError("invalid RDP clipboard Monitor Ready")
		}
		return v.clipboardReady()
	case clipFormatList:
		if !c.ready {
			return protocolError("RDP clipboard format list before Monitor Ready")
		}
		return v.clipboardFormats(data)
	case clipListResponse:
		if len(data) != 0 || (flags != clipOK && flags != clipFail) {
			return protocolError("invalid RDP clipboard list response")
		}
		if flags == clipFail {
			c.offered = false
		}
	case clipDataRequest:
		if len(data) != 4 || flags != 0 {
			return protocolError("invalid RDP clipboard data request")
		}
		return v.serveClipboard(le.Uint32(data))
	case clipDataResponse:
		return v.receiveClipboardData(flags, data)
	case clipFileRequest:
		// File transfers are disabled, including for unexpected requests.
		if len(data) < 4 {
			return protocolError("short RDP clipboard file request")
		}
		return v.sendClipboard(clipFileResponse, clipFail, data[:4])
	}
	return nil
}

func clipboardCaps(p []byte) (bool, error) {
	le := binary.LittleEndian
	if len(p) < 4 {
		return false, protocolError("short RDP clipboard capabilities")
	}
	count := int(le.Uint16(p))
	p = p[4:]
	longNames, general := false, false
	for range count {
		if len(p) < 4 {
			return false, protocolError("short RDP clipboard capability set")
		}
		size := int(le.Uint16(p[2:]))
		if size < 4 || size > len(p) {
			return false, protocolError("invalid RDP clipboard capability set length")
		}
		if le.Uint16(p) == 1 {
			if general || size != 12 {
				return false, protocolError("invalid RDP clipboard general capability")
			}
			general = true
			longNames = le.Uint32(p[4:]) >= 2 && le.Uint32(p[8:])&clipLongNames != 0
		}
		p = p[size:]
	}
	if len(p) != 0 {
		return false, protocolError("trailing RDP clipboard capabilities")
	}
	return longNames, nil
}

func (v *virtualChannels) clipboardReady() error {
	c := &v.clipboard
	if c.gotCaps {
		caps := []byte{
			1, 0, 0, 0, // one capability set, padding
			1, 0, 12, 0, // general capability, 12 bytes
			2, 0, 0, 0, // capability version
			0, 0, 0, 0, // no file transfer, file paths, or file locking
		}
		if c.longNames {
			caps[12] = clipLongNames
		}
		if err := v.sendClipboard(clipCaps, 0, caps); err != nil {
			return err
		}
	}
	c.ready, c.needsOffer = true, true
	return v.announceClipboard()
}

func (v *virtualChannels) setClipboard(text string) error {
	if v.clip == 0 {
		return &clipboardInputError{"RDP clipboard channel is unavailable"}
	}
	unicode, ansi, err := encodeClipboardText(text)
	if err != nil {
		return err
	}
	c := &v.clipboard
	c.unicode, c.ansi, c.needsOffer = unicode, ansi, true
	if c.pending != 0 {
		c.superseded, c.next = true, 0
	}
	return v.announceClipboard()
}

func (v *virtualChannels) announceClipboard() error {
	c := &v.clipboard
	if !c.ready || !c.needsOffer || v.suspended {
		return nil
	}
	var p []byte
	if c.unicode != nil {
		p = appendClipboardFormat(p, clipUnicode, c.longNames)
	}
	if c.ansi != nil {
		p = appendClipboardFormat(p, clipText, c.longNames)
	}
	if err := v.sendClipboard(clipFormatList, 0, p); err != nil {
		return err
	}
	c.needsOffer, c.offered = false, c.unicode != nil
	return nil
}

func appendClipboardFormat(p []byte, id uint32, longNames bool) []byte {
	p = binary.LittleEndian.AppendUint32(p, id)
	if longNames {
		return append(p, 0, 0)
	}
	return append(p, make([]byte, 32)...)
}

func (v *virtualChannels) clipboardFormats(p []byte) error {
	c := &v.clipboard
	format, valid := readClipboardFormats(p, c.longNames)
	if !valid {
		return v.sendClipboard(clipListResponse, clipFail, nil)
	}
	if err := v.sendClipboard(clipListResponse, clipOK, nil); err != nil {
		return err
	}
	// Remote ownership supersedes the last local offer; refuse stale requests.
	c.offered, c.needsOffer = false, false
	c.unicode, c.ansi = nil, nil
	if c.pending != 0 {
		c.superseded, c.next = true, format
		return nil
	}
	return v.requestClipboard(format)
}

func readClipboardFormats(p []byte, longNames bool) (uint32, bool) {
	var format uint32
	for len(p) > 0 {
		if len(p) < 4 {
			return 0, false
		}
		id := binary.LittleEndian.Uint32(p)
		p = p[4:]
		if longNames {
			size := clipboardWideEnd(p)
			if size < 0 {
				return 0, false
			}
			p = p[size+2:]
		} else {
			if len(p) < 32 {
				return 0, false
			}
			p = p[32:]
		}
		if id == clipUnicode || (id == clipText && format == 0) {
			format = id
		}
	}
	return format, true
}

func (v *virtualChannels) requestClipboard(format uint32) error {
	if format == 0 || v.clipboard.onText == nil {
		return nil
	}
	data := binary.LittleEndian.AppendUint32(nil, format)
	if err := v.sendClipboard(clipDataRequest, 0, data); err != nil {
		return err
	}
	v.clipboard.pending = format
	return nil
}

func (v *virtualChannels) serveClipboard(format uint32) error {
	c := &v.clipboard
	if c.offered {
		switch format {
		case clipUnicode:
			return v.sendClipboard(clipDataResponse, clipOK, c.unicode)
		case clipText:
			if c.ansi != nil {
				return v.sendClipboard(clipDataResponse, clipOK, c.ansi)
			}
		}
	}
	return v.sendClipboard(clipDataResponse, clipFail, nil)
}

func (v *virtualChannels) receiveClipboardData(flags uint16, p []byte) error {
	c := &v.clipboard
	if c.pending == 0 || (flags != clipOK && flags != clipFail) {
		return protocolError("unexpected RDP clipboard data response")
	}
	format, next, superseded := c.pending, c.next, c.superseded
	c.pending, c.next, c.superseded = 0, 0, false
	if flags == clipOK && !superseded {
		text, err := decodeClipboardText(format, p)
		if err != nil {
			return err
		}
		c.onText(text)
	}
	return v.requestClipboard(next)
}

func (v *virtualChannels) sendClipboard(kind, flags uint16, data []byte) error {
	le := binary.LittleEndian
	p := make([]byte, 8+len(data))
	le.PutUint16(p, kind)
	le.PutUint16(p[2:], flags)
	le.PutUint32(p[4:], uint32(len(data)))
	copy(p[8:], data)
	return v.sendChannel(v.clip, p)
}
