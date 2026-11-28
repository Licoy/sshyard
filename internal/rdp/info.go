package rdp

import (
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxInfoStringUnits = 255
	arcPacketSize      = 28
)

func infoUnits(s string) ([]uint16, error) {
	if len(s) > maxInfoStringUnits*4 || strings.IndexByte(s, 0) >= 0 || !utf8.ValidString(s) {
		return nil, errors.New("invalid RDP logon field")
	}
	var units []uint16
	for _, r := range s {
		units = utf16.AppendRune(units, r)
	}
	if len(units) > maxInfoStringUnits {
		clear(units)
		return nil, errors.New("RDP logon field exceeds 255 UTF-16 code units")
	}
	return units, nil
}

// clientInfo builds the SEC_INFO_PKT and TS_INFO_PACKET (MS-RDPBCGR 2.2.1.11).
// The caller clears the packet immediately after sending it.
func clientInfo(cfg Config, local net.Addr, protocol uint32, password []uint16, arc []byte) ([]byte, error) {
	domain, err := infoUnits(cfg.Domain)
	if err != nil {
		return nil, err
	}
	user, err := infoUnits(cfg.User)
	if err != nil {
		return nil, err
	}
	const infoFlags = 0x00000001 | 0x00000002 | 0x00000010 | 0x00000020 | 0x00000040 | 0x00000100 | 0x00010000 | 0x00020000 | 0x00080000
	flags := uint32(infoFlags) // Mouse, no Ctrl+Alt+Del screen, Unicode, maximized shell (which xrdp requires), logon notifications, Windows keys, logon errors, wheel, no audio playback.
	if protocol == protocolTLS && len(password) > 0 && len(arc) == 0 {
		flags |= 0x0008 // INFO_AUTOLOGON
	} else {
		password = nil // CredSSP carries its own credentials.
	}
	p := make([]byte, 22, 22+2*(len(domain)+len(user)+len(password))+340)
	le := binary.LittleEndian
	le.PutUint16(p, 0x0040) // SEC_INFO_PKT with the Basic Security Header
	le.PutUint32(p[4:], cfg.KeyboardLayout&0xffff)
	le.PutUint32(p[8:], flags)
	for i, field := range [][]uint16{domain, user, password, nil, nil} {
		le.PutUint16(p[12+i*2:], uint16(len(field)*2))
		for _, unit := range field {
			p = le.AppendUint16(p, unit)
		}
		p = le.AppendUint16(p, 0)
	}
	return appendExtendedInfo(p, local, arc), nil
}

func appendExtendedInfo(p []byte, local net.Addr, arc []byte) []byte {
	family, addr := uint16(2), "0.0.0.0"
	if tcp, ok := local.(*net.TCPAddr); ok && tcp.IP != nil {
		addr = tcp.IP.String()
		if tcp.IP.To4() == nil {
			family = 0x17
		}
	}
	le := binary.LittleEndian
	p = le.AppendUint16(p, family)
	p = le.AppendUint16(p, uint16(2*(len(addr)+1)))
	for i := range len(addr) {
		p = le.AppendUint16(p, uint16(addr[i]))
	}
	p = append(p, 0, 0, 2, 0, 0, 0) // Address terminator and empty client directory.
	p = append(p, make([]byte, 172+4+4)...)
	p = le.AppendUint16(p, uint16(len(arc)))
	p = append(p, arc...)
	return append(p, 0, 0, 0, 0) // TS_EXTENDED_INFO_PACKET reserved fields.
}

// arcCookie is the in-memory server part of MS-RDPBCGR 5.5.
type arcCookie struct {
	id     uint32
	random [16]byte
}

func (c arcCookie) clientPacket() []byte {
	p := make([]byte, 12, arcPacketSize)
	le := binary.LittleEndian
	le.PutUint32(p, arcPacketSize)
	le.PutUint32(p[4:], 1)
	le.PutUint32(p[8:], c.id)
	m := hmac.New(md5.New, c.random[:])
	var clientRandom [32]byte // Enhanced RDP Security sends no client random.
	_, _ = m.Write(clientRandom[:])
	return m.Sum(p)
}

func sessionInfo(data []byte) (*arcCookie, error) {
	le := binary.LittleEndian
	if len(data) < 4 {
		return nil, protocolError("invalid RDP Save Session Info PDU")
	}
	infoType := le.Uint32(data)
	if infoType != 3 { // INFOTYPE_LOGON_EXTENDED_INFO
		return nil, validateLogonInfo(infoType, data[4:])
	}
	p := data[4:]
	const padding = 570
	if len(p) < 6+padding {
		return nil, protocolError("invalid RDP extended logon information length")
	}
	n := int(le.Uint16(p))
	if n != len(p) && n != len(p)-padding {
		return nil, protocolError("invalid RDP extended logon information length")
	}
	fields := le.Uint32(p[2:])
	p = p[6 : len(p)-padding]
	var cookie *arcCookie
	for _, flag := range []uint32{1, 2} {
		if fields&flag == 0 {
			continue
		}
		var field []byte
		var err error
		field, p, err = logonField(p)
		if err != nil {
			return nil, err
		}
		if flag == 2 {
			return cookie, logonNotification(field)
		}
		if len(field) != arcPacketSize || le.Uint32(field) != arcPacketSize || le.Uint32(field[4:]) != 1 {
			return nil, protocolError("invalid RDP Server Auto-Reconnect Packet")
		}
		cookie = &arcCookie{id: le.Uint32(field[8:]), random: [16]byte(field[12:])}
	}
	return cookie, nil
}

func validateLogonInfo(infoType uint32, p []byte) error {
	le := binary.LittleEndian
	valid := true
	switch infoType {
	case 0: // TS_LOGON_INFO, with an optional session ID.
		valid = (len(p) == 572 || len(p) == 576) &&
			terminatedLogonString(p[4:56], le.Uint32(p)) && terminatedLogonString(p[60:572], le.Uint32(p[56:]))
	case 1: // TS_LOGON_INFO_VERSION_2, with two strings after its fixed part.
		const fixed = 576
		// The sample in MS-RDPBCGR 4.3.1 counts the header without padding.
		valid = len(p) >= fixed && le.Uint16(p) == 1 && (le.Uint32(p[2:]) == 18 || le.Uint32(p[2:]) == fixed)
		if valid {
			domain, user := le.Uint32(p[10:]), le.Uint32(p[14:])
			valid = uint64(domain)+uint64(user) == uint64(len(p)-fixed) &&
				terminatedLogonString(p[fixed:fixed+int(domain)], domain) && terminatedLogonString(p[fixed+int(domain):], user)
		}
	case 2: // TS_PLAIN_NOTIFY
		valid = len(p) == 576
	}
	if !valid {
		return protocolError("invalid RDP logon information")
	}
	return nil
}

func terminatedLogonString(p []byte, n uint32) bool {
	return n >= 2 && n%2 == 0 && n <= uint32(len(p)) && n <= 512 && binary.LittleEndian.Uint16(p[n-2:n]) == 0
}

func logonField(p []byte) ([]byte, []byte, error) {
	if len(p) < 4 {
		return nil, nil, protocolError("truncated RDP logon information field")
	}
	n := binary.LittleEndian.Uint32(p)
	if uint64(n) > uint64(len(p)-4) {
		return nil, nil, protocolError("invalid RDP logon information field length")
	}
	return p[4 : 4+int(n)], p[4+int(n):], nil
}

func logonNotification(p []byte) error {
	if len(p) != 8 {
		return protocolError("invalid RDP logon error notification")
	}
	code := binary.LittleEndian.Uint32(p)
	detail := binary.LittleEndian.Uint32(p[4:])
	kind, reason := KindAuth, ReasonNone
	switch code {
	case 0xfffffff8, 0xfffffff9, 0xfffffffb, 0xfffffffc, 0xfffffffe:
		return nil // A server dialog or logon progress.
	case 0xfffffffa, 5:
		reason = ReasonServerDenied
	case 0xfffffffd:
		kind, reason = KindServer, ReasonLoggedOff
	default:
		_, reason = Classify(credSSPStatus(code))
		if reason == ReasonNone {
			switch detail {
			case 0:
				reason = ReasonLogonFailure
			case 1:
				reason = ReasonPasswordMustChange
			case 3:
				return nil // The server displays a non-fatal warning.
			}
		}
	}
	return &Error{Kind: kind, Reason: reason, Code: code, Err: fmt.Errorf("RDP logon notification (0x%08x)", code)}
}
