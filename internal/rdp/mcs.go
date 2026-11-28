package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

const mcsUserIDBase = 1001

var x224DataHeader = []byte{2, 0xf0, 0x80}

type rdpTransport struct {
	net.Conn
	protocol uint32
	userID   uint16
	globalID uint16
	channels map[string]uint16
}

func (t *rdpTransport) connectMCS(cfg Config) error {
	names := []string{"drdynvc"}
	if cfg.Clipboard {
		names = append(names, "cliprdr")
	}
	if err := t.writeMCS(mcsConnectInitial(clientGCC(cfg, t.protocol, names))); err != nil {
		return err
	}
	p, err := t.readMCS()
	if err != nil {
		return err
	}
	gcc, err := mcsConnectResponse(p)
	if err != nil {
		return err
	}
	settings, err := parseServerGCC(gcc, requestedProtocols(cfg.Security), len(names))
	if err != nil {
		return err
	}
	if err := t.attachMCS(); err != nil {
		return err
	}
	t.globalID = settings.globalID
	t.channels = make(map[string]uint16, len(settings.ids))
	seen := map[uint16]bool{t.userID: true}
	for _, id := range append([]uint16{settings.globalID}, settings.ids...) {
		if id == 0 || seen[id] {
			return protocolError("invalid or duplicate MCS channel ID")
		}
		seen[id] = true
	}
	for i, id := range settings.ids {
		t.channels[names[i]] = id
	}
	if settings.skipJoin {
		return nil
	}
	for _, id := range append([]uint16{t.userID, t.globalID}, settings.ids...) {
		if err := t.joinMCS(id); err != nil {
			return err
		}
	}
	return nil
}

func (t *rdpTransport) attachMCS() error {
	// Erect Domain Request (choice 1), followed by Attach User Request (choice 10).
	if err := t.writeMCS([]byte{0x04, 1, 0, 1, 0}); err != nil {
		return err
	}
	if err := t.writeMCS([]byte{0x28}); err != nil {
		return err
	}
	p, err := t.readMCS()
	if err != nil {
		return err
	}
	if len(p) < 2 || p[0]>>2 != 11 {
		return protocolError("invalid MCS Attach User Confirm")
	}
	if p[1] != 0 {
		return mcsResultError(p[1], "MCS Attach User Confirm")
	}
	if len(p) != 4 || p[0] != 0x2e || binary.BigEndian.Uint16(p[2:]) > 65535-mcsUserIDBase {
		return protocolError("invalid MCS user channel ID")
	}
	t.userID = mcsUserIDBase + binary.BigEndian.Uint16(p[2:])
	return nil
}

func (t *rdpTransport) joinMCS(channel uint16) error {
	user := t.userID - mcsUserIDBase
	p := binary.BigEndian.AppendUint16([]byte{0x38}, user)
	p = binary.BigEndian.AppendUint16(p, channel)
	if err := t.writeMCS(p); err != nil {
		return err
	}
	p, err := t.readMCS()
	if err != nil {
		return err
	}
	if len(p) < 2 || p[0]>>2 != 15 {
		return protocolError("invalid MCS Channel Join Confirm")
	}
	if p[1] != 0 {
		return mcsResultError(p[1], "MCS Channel Join Confirm")
	}
	if len(p) != 8 || p[0] != 0x3e || binary.BigEndian.Uint16(p[2:]) != user ||
		binary.BigEndian.Uint16(p[4:]) != channel || binary.BigEndian.Uint16(p[6:]) != channel {
		return protocolError("MCS Channel Join Confirm does not match the request")
	}
	return nil
}

func mcsConnectInitial(gcc []byte) []byte {
	// The selectors, upward flag and domain parameters are defined by T.125.
	p := []byte{4, 1, 1, 4, 1, 1, 1, 1, 0xff}
	p = append(p, mcsDomainParameters(34, 2, 0, 65535)...)
	p = append(p, mcsDomainParameters(1, 1, 1, 1056)...)
	p = append(p, mcsDomainParameters(65535, 64535, 65535, 65535)...)
	p = append(p, mcsBERField(4, gcc)...)
	header := appendMCSBERLength([]byte{0x7f, 0x65}, len(p))
	return append(header, p...)
}

func mcsDomainParameters(channels, users, tokens, maxPDU uint16) []byte {
	var p []byte
	for _, value := range []uint16{channels, users, tokens, 1, 0, 1, maxPDU, 2} {
		v := []byte{0, byte(value >> 8), byte(value)}
		for len(v) > 1 && v[0] == 0 && v[1]&0x80 == 0 {
			v = v[1:]
		}
		p = append(p, mcsBERField(2, v)...)
	}
	return mcsBERField(0x30, p)
}

func mcsConnectResponse(p []byte) ([]byte, error) {
	if len(p) < 3 || p[0] != 0x7f || p[1] != 0x66 {
		return nil, protocolError("invalid MCS Connect Response tag")
	}
	p, n, ok := mcsBERLength(p[2:])
	if !ok || n != len(p) {
		return nil, protocolError("invalid MCS Connect Response length")
	}
	result, p, ok := takeMCSBER(p, 0x0a)
	if !ok || len(result) != 1 {
		return nil, protocolError("invalid MCS Connect Response result")
	}
	if result[0] != 0 {
		return nil, mcsResultError(result[0], "MCS Connect Response")
	}
	id, p, ok := takeMCSBER(p, 2)
	if !ok || len(id) == 0 || len(id) > 5 {
		return nil, protocolError("invalid MCS calledConnectId")
	}
	domain, p, ok := takeMCSBER(p, 0x30)
	if !ok || !validMCSDomain(domain) {
		return nil, protocolError("invalid MCS domain parameters")
	}
	gcc, rest, ok := takeMCSBER(p, 4)
	if !ok || len(rest) != 0 {
		return nil, protocolError("invalid MCS user data")
	}
	return gcc, nil
}

func validMCSDomain(p []byte) bool {
	for range 8 {
		value, rest, ok := takeMCSBER(p, 2)
		if !ok || len(value) == 0 || len(value) > 5 {
			return false
		}
		p = rest
	}
	return len(p) == 0
}

func (t *rdpTransport) writeMCS(p []byte) error {
	frame := append(make([]byte, 0, len(x224DataHeader)+len(p)), x224DataHeader...)
	frame = append(frame, p...)
	defer clear(frame)
	if err := writeTPKT(t.Conn, frame); err != nil {
		return transportError(err)
	}
	return nil
}

func (t *rdpTransport) readMCS() ([]byte, error) {
	p, _, err := readRDPFrame(t.Conn, false)
	if err != nil {
		return nil, transportError(err)
	}
	return unwrapMCS(p)
}

func unwrapMCS(p []byte) ([]byte, error) {
	if len(p) < 4 || !bytes.Equal(p[:3], x224DataHeader) {
		return nil, protocolError("invalid X.224 data TPDU")
	}
	p = p[3:]
	if p[0]>>2 == 8 {
		if len(p) != 2 || p[1]&0x7f != 0 {
			return nil, protocolError("invalid MCS Disconnect Provider Ultimatum")
		}
		return nil, &Error{Kind: KindServer, Code: uint32((p[0]&3)<<1 | p[1]>>7), Err: errors.New("RDP server ended the MCS connection")}
	}
	return p, nil
}

func (t *rdpTransport) sendData(channel uint16, p []byte) error {
	if t.userID < mcsUserIDBase || channel == 0 || len(p) > 0x7fff {
		return protocolError("invalid MCS Send Data Request parameters")
	}
	data := binary.BigEndian.AppendUint16([]byte{0x64}, t.userID-mcsUserIDBase)
	data = binary.BigEndian.AppendUint16(data, channel)
	data = append(data, 0x70) // high priority, begin and end segmentation bits
	data = appendMCSData(data, p)
	defer clear(data)
	return t.writeMCS(data)
}

func (t *rdpTransport) receiveData() (uint16, []byte, bool, error) {
	p, fast, err := readRDPFrame(t.Conn, true)
	if err != nil {
		return 0, nil, false, transportError(err)
	}
	if fast {
		return 0, p, true, nil
	}
	p, err = unwrapMCS(p)
	if err != nil {
		return 0, nil, false, err
	}
	if len(p) < 7 || p[0] != 0x68 || p[5]&0x3f != 0x30 {
		return 0, nil, false, protocolError("invalid MCS Send Data Indication")
	}
	channel := binary.BigEndian.Uint16(p[3:5])
	data, ok := takeMCSData(p[6:])
	if !ok || channel == 0 {
		return 0, nil, false, protocolError("invalid MCS Send Data Indication length or channel")
	}
	return channel, data, false, nil
}

func (t *rdpTransport) disconnect() error {
	return t.writeMCS([]byte{0x21, 0x80}) // rn-user-requested
}

func mcsResultError(result byte, name string) error {
	return &Error{Kind: KindServer, Reason: ReasonProtocol, Code: uint32(result), Err: fmt.Errorf("%s failed with result %d", name, result)}
}
