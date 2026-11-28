package rdp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	shareDemandActive  = 1
	shareConfirmActive = 3
	shareDeactivate    = 6
	shareData          = 7
	shareRedirect      = 10
	shareVersion       = 0x10
	serverChannelID    = 1002
)

const (
	dataUpdate      = 0x02
	dataControl     = 0x14
	dataPointer     = 0x1b
	dataInput       = 0x1c
	dataSynchronize = 0x1f
	dataRefreshRect = 0x21
	dataSuppress    = 0x23
	dataSaveSession = 0x26
	dataFontList    = 0x27
	dataFontMap     = 0x28
	dataErrorInfo   = 0x2f
	dataMonitor     = 0x37
)

// Share Control and Share Data headers, MS-RDPBCGR 2.2.8.1.1.1.
func controlPDU(userID, kind uint16, body []byte) []byte {
	le := binary.LittleEndian
	p := make([]byte, 6, 6+len(body))
	le.PutUint16(p, uint16(len(body)+6))
	le.PutUint16(p[2:], shareVersion|kind)
	le.PutUint16(p[4:], userID)
	return append(p, body...)
}

func dataPDU(userID uint16, shareID uint32, kind byte, body []byte) []byte {
	le := binary.LittleEndian
	p := make([]byte, 12, 12+len(body))
	le.PutUint32(p, shareID)
	p[5] = 1 // STREAM_LOW
	// The client finalization examples in MS-RDPBCGR 4.1.14–4.1.16 use payload plus four.
	le.PutUint16(p[6:], uint16(len(body)+4))
	p[8] = kind
	return controlPDU(userID, shareData, append(p, body...))
}

type sharePDU struct {
	kind   uint16
	source uint16
	data   []byte
}

func readSharePDU(p []byte) (sharePDU, []byte, error) {
	if len(p) < 4 {
		return sharePDU{}, nil, protocolError("truncated RDP share control header")
	}
	le := binary.LittleEndian
	n, flags := int(le.Uint16(p)), le.Uint16(p[2:])
	kind := flags & 0x0f
	if n == 4 && kind == shareDeactivate && flags&0xfff0 == shareVersion {
		// Older servers omit the source and payload in Deactivate All (MS-RDPBCGR 2.2.3.1.1).
		return sharePDU{kind: kind}, p[4:], nil
	}
	if n < 6 || n > len(p) || flags&0xfff0 != shareVersion {
		return sharePDU{}, nil, protocolError("invalid RDP share control header")
	}
	return sharePDU{kind: kind, source: le.Uint16(p[4:]), data: p[6:n]}, p[n:], nil
}

func readDataPDU(p []byte) (uint32, byte, []byte, error) {
	if len(p) < 12 {
		return 0, 0, nil, protocolError("truncated RDP share data header")
	}
	// Windows versions have left uncompressedLength uninitialized. The enclosing
	// share control length bounds uncompressed data (MS-RDPBCGR 4.1.18).
	// Only PACKET_COMPRESSED says the data is (MS-RDPBCGR 2.2.8.1.1.1.2).
	if p[9]&0x20 != 0 {
		return 0, 0, nil, protocolError("RDP bulk compression was not negotiated")
	}
	return binary.LittleEndian.Uint32(p), p[8], p[12:], nil
}

func errorInfo(p []byte) error {
	if len(p) != 4 {
		return protocolError("invalid RDP Error Info PDU")
	}
	code := binary.LittleEndian.Uint32(p)
	if code == 0 {
		return nil
	}
	kind, reason := KindServer, ReasonNone
	switch code {
	case 1, 2, 0x0b:
		reason = ReasonAdminEnded
	case 3:
		reason = ReasonIdleTimeout
	case 4:
		reason = ReasonLogonTimeout
	case 5:
		reason = ReasonReplaced
	case 6, 7:
		reason = ReasonServerDenied
	case 9:
		kind, reason = KindAuth, ReasonLogonTypeNotGranted
	case 0x0a:
		kind, reason = KindAuth, ReasonLogonFailure
	case 0x0c:
		reason = ReasonLoggedOff
	}
	if code >= 0x0100 && code <= 0x010a {
		kind = KindLicense
	}
	if code >= 0x1000 && code < 0x2000 {
		reason = ReasonProtocol
	}
	return &Error{Kind: kind, Reason: reason, Code: code, Err: fmt.Errorf("RDP server ended the connection (0x%08x)", code)}
}

func graphicsError(err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	return &Error{Kind: KindServer, Reason: ReasonProtocol, Err: err}
}

func desktopRectPDU(allow bool, width, height int) []byte {
	p := make([]byte, 4, 12)
	if !allow {
		return p
	}
	p[0] = 1 // allow output, or refresh one rectangle
	p = append(p, 0, 0, 0, 0)
	p = binary.LittleEndian.AppendUint16(p, uint16(width-1))
	return binary.LittleEndian.AppendUint16(p, uint16(height-1))
}
