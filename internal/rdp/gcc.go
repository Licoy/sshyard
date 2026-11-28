package rdp

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

const clientEarlyCapabilities = 0x0001 | 0x0002 | 0x0020 | 0x0040 | 0x0800 // Error Info, 32 bpp, connection type, monitor layout, skip joins

var gccOID = []byte{0, 5, 0, 0x14, 0x7c, 0, 1}

func clientGCC(cfg Config, selected uint32, names []string) []byte {
	blocks := clientCoreData(cfg, selected)
	if cfg.Admin {
		// Ask for administrative session zero through the cluster session ID.
		const redirectedSessionIDValid = 0x02
		const redirectionVersion4 = 0x03 << 2
		cluster := binary.LittleEndian.AppendUint32(nil, redirectedSessionIDValid|redirectionVersion4)
		blocks = append(blocks, gccBlock(0xc004, append(cluster, 0, 0, 0, 0))...)
	}
	blocks = append(blocks, gccBlock(0xc002, make([]byte, 8))...) // TLS carries transport security.
	network := binary.LittleEndian.AppendUint32(nil, uint32(len(names)))
	for _, name := range names {
		var channel [8]byte
		copy(channel[:], name)
		network = append(network, channel[:]...)
		network = binary.LittleEndian.AppendUint32(network, 0x80000000) // CHANNEL_OPTION_INITIALIZED
	}
	blocks = append(blocks, gccBlock(0xc003, network)...)

	// Conference Create Request with one H.221 user-data set.
	conference := []byte{0, 8, 0, 0x10, 0, 1, 0xc0, 0, 'D', 'u', 'c', 'a'}
	conference = append(appendMCSLength(conference, len(blocks)), blocks...)
	return append(appendMCSLength(append([]byte(nil), gccOID...), len(conference)), conference...)
}

func clientCoreData(cfg Config, selected uint32) []byte {
	le := binary.LittleEndian
	p := make([]byte, 230)
	le.PutUint32(p[0:], 0x00080004) // RDP 5.0 through 8.1
	le.PutUint16(p[4:], uint16(cfg.Width))
	le.PutUint16(p[6:], uint16(cfg.Height))
	le.PutUint16(p[8:], 0xca01)  // RNS_UD_COLOR_8BPP (superseded by highColorDepth)
	le.PutUint16(p[10:], 0xaa03) // RNS_UD_SAS_DEL
	le.PutUint32(p[12:], cfg.KeyboardLayout)
	le.PutUint32(p[16:], 7601) // Windows 7 SP1-era core capabilities
	name, _, _ := strings.Cut(cfg.ClientName, "\x00")
	units := utf16.Encode([]rune(name))
	if len(units) > 15 {
		units = units[:15]
		if units[14] >= 0xd800 && units[14] <= 0xdbff {
			units = units[:14]
		}
	}
	copy(p[20:52], rdpUTF16Bytes(units))
	le.PutUint32(p[52:], 4)  // Enhanced keyboard
	le.PutUint32(p[60:], 12) // Number of function keys
	le.PutUint16(p[128:], 0xca01)
	le.PutUint16(p[130:], 1)
	le.PutUint16(p[136:], 24)  // highColorDepth; WANT_32BPP_SESSION requests 32 bpp
	le.PutUint16(p[138:], 0xf) // supports 15, 16, 24, and 32 bpp
	le.PutUint16(p[140:], clientEarlyCapabilities)
	p[206] = 6 // CONNECTION_TYPE_LAN
	le.PutUint32(p[208:], selected)
	// A virtual display has no measured physical dimensions. Use 96 pixels per inch.
	le.PutUint32(p[212:], uint32(max(10, cfg.Width*254/960)))
	le.PutUint32(p[216:], uint32(max(10, cfg.Height*254/960)))
	le.PutUint32(p[222:], uint32(cfg.Scale))
	le.PutUint32(p[226:], 100)
	return gccBlock(0xc001, p)
}

func gccBlock(kind uint16, p []byte) []byte {
	header := binary.LittleEndian.AppendUint16(nil, kind)
	header = binary.LittleEndian.AppendUint16(header, uint16(len(p)+4))
	return append(header, p...)
}

type serverGCC struct {
	globalID uint16
	ids      []uint16
	skipJoin bool
}

func parseServerGCC(p []byte, requested uint32, maxChannels int) (serverGCC, error) {
	p, err := serverGCCBlocks(p)
	if err != nil {
		return serverGCC{}, err
	}
	var s serverGCC
	seen := make(map[uint16]bool)
	le := binary.LittleEndian
	for len(p) != 0 {
		if len(p) < 4 {
			return s, protocolError("truncated GCC server data header")
		}
		kind, n := le.Uint16(p), int(le.Uint16(p[2:]))
		if n < 4 || n > len(p) || seen[kind] {
			return s, protocolError("invalid or duplicate GCC server data block")
		}
		seen[kind] = true
		b := p[4:n]
		p = p[n:]
		switch kind {
		case 0x0c01: // SC_CORE
			if (len(b) != 4 && len(b) != 8 && len(b) != 12) || le.Uint32(b)>>16 != 8 {
				return s, protocolError("invalid GCC server core data")
			}
			if len(b) >= 8 && le.Uint32(b[4:]) != requested {
				return s, protocolError("GCC server did not echo requested security protocols")
			}
			s.skipJoin = len(b) == 12 && le.Uint32(b[8:])&0x08 != 0
		case 0x0c02: // SC_SECURITY
			if len(b) < 8 {
				return s, protocolError("invalid GCC server security data")
			}
			if method := le.Uint32(b); method != 0 || le.Uint32(b[4:]) != 0 {
				return s, securityError(method, "unexpected standard RDP encryption inside TLS")
			}
		case 0x0c03: // SC_NET
			s.globalID, s.ids, err = gccServerChannels(b, maxChannels)
			if err != nil {
				return s, err
			}
		}
	}
	if !seen[0x0c01] || !seen[0x0c02] || !seen[0x0c03] {
		return s, protocolError("missing GCC server settings")
	}
	return s, nil
}

func serverGCCBlocks(p []byte) ([]byte, error) {
	if !bytes.HasPrefix(p, gccOID) {
		return nil, protocolError("invalid GCC connection identifier")
	}
	// The ConnectData.connectPDU length in the response is ignored per MS-RDPBCGR.
	p, _, ok := mcsLength(p[len(gccOID):])
	if !ok || len(p) < 4 || p[0] != 0x14 {
		return nil, protocolError("invalid GCC conference response")
	}
	// ConferenceCreateResponse has a packed option byte, a two-byte node ID,
	// a length-prefixed tag, a result byte, and a single H.221 user-data set.
	p, n, ok := mcsLength(p[3:])
	if !ok || n == 0 || n > len(p) {
		return nil, protocolError("invalid GCC conference response tag")
	}
	p = p[n:]
	if !bytes.HasPrefix(p, []byte{0, 1, 0xc0, 0, 'M', 'c', 'D', 'n'}) {
		return nil, protocolError("invalid GCC conference result or H.221 key")
	}
	data, ok := takeMCSData(p[8:])
	if !ok {
		return nil, protocolError("invalid GCC user-data length")
	}
	return data, nil
}

func gccServerChannels(p []byte, maxChannels int) (uint16, []uint16, error) {
	if len(p) < 4 {
		return 0, nil, protocolError("invalid GCC server network data")
	}
	le := binary.LittleEndian
	globalID, count := le.Uint16(p), int(le.Uint16(p[2:]))
	if count > maxChannels || len(p) != 4+2*(count+count%2) {
		return 0, nil, protocolError("invalid GCC static virtual channel array")
	}
	ids := make([]uint16, count)
	for i := range ids {
		ids[i] = le.Uint16(p[4+i*2:])
	}
	return globalID, ids, nil
}

func rdpUTF16Bytes(units []uint16) []byte {
	p := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(p[i*2:], unit)
	}
	return p
}
