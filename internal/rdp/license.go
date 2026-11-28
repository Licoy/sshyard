package rdp

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	licenseFlag        = 0x0080
	licenseEncryptFlag = 0x0200
	licensePlatformID  = 0x04000000
	licensePreamble    = 0x03
)

type licenseState uint8

const (
	licenseStart licenseState = iota
	licenseAwaitChallenge
	licenseAwaitIssue
	licenseComplete
)

type licenseExchange struct {
	cfg       Config
	transport *rdpTransport
	state     licenseState
	macKey    [16]byte
	cipherKey [16]byte
	hwid      [20]byte
	cached    *rdpClientLicense
	issued    *rdpClientLicense
	last      []byte
}

func newLicenseExchange(cfg Config, transport *rdpTransport) *licenseExchange {
	return &licenseExchange{cfg: cfg, transport: transport}
}

func (l *licenseExchange) clearKeys() {
	clear(l.macKey[:])
	clear(l.cipherKey[:])
	clear(l.hwid[:])
	clear(l.last)
	l.last = nil
}

func (l *licenseExchange) receive(payload []byte) (handled, complete bool, err error) {
	if len(payload) < 2 {
		return false, false, nil
	}
	flags := binary.LittleEndian.Uint16(payload)
	if flags != licenseFlag && flags != licenseFlag|licenseEncryptFlag {
		return false, false, nil
	}
	// A Share Control totalLength can coincide with the Security Header flags.
	// With a nonzero flagsHi, require a matching licensing preamble as well.
	p := payload[min(len(payload), 4):]
	if len(payload) >= 4 && binary.LittleEndian.Uint16(payload[2:]) != 0 && !validLicensePreamble(p) {
		return false, false, nil
	}
	if !validLicensePreamble(p) {
		return true, false, malformedLicense("invalid RDP license preamble")
	}
	if l.state == licenseComplete {
		return true, false, malformedLicense("unexpected RDP license packet after licensing completed")
	}
	if l.cfg.Log != nil {
		l.cfg.Log(licensePacketName(p[0]))
	}
	switch p[0] {
	case 1:
		err = l.request(p[4:])
	case 2:
		err = l.challenge(p[4:])
	case 3, 4:
		err = l.acceptLicense(p[0], p[4:])
	case 0xff:
		err = l.alert(p[4:])
	default:
		err = malformedLicense("unsupported RDP license packet type")
	}
	return true, l.state == licenseComplete, err
}

func validLicensePreamble(p []byte) bool {
	if len(p) < 4 || int(binary.LittleEndian.Uint16(p[2:])) != len(p) {
		return false
	}
	return p[1]&0x0f == 2 || p[1]&0x0f == 3
}

func (l *licenseExchange) request(p []byte) error {
	if l.state != licenseStart {
		return malformedLicense("out-of-sequence RDP Server License Request")
	}
	req, err := parseLicenseRequest(p)
	if err != nil {
		return err
	}
	publicKey, err := l.licensePublicKey(req.cert)
	if err != nil {
		alert := binary.LittleEndian.AppendUint32(nil, 1)  // ERR_INVALID_SERVER_CERTIFICATE
		alert = binary.LittleEndian.AppendUint32(alert, 1) // ST_TOTAL_ABORT
		alert = appendLicenseBlob(alert, 4, nil)           // BB_ERROR_BLOB
		return errors.Join(err, l.send(0xff, alert))
	}
	var clientRandom [32]byte
	var premaster [48]byte
	defer clear(premaster[:])
	if _, err := rand.Read(clientRandom[:]); err != nil {
		return err
	}
	if _, err := rand.Read(premaster[:]); err != nil {
		return err
	}
	ciphertext, err := encryptLicensePremaster(publicKey, premaster[:])
	if err != nil {
		return err
	}
	l.macKey, l.cipherKey = deriveLicenseKeys(premaster[:], clientRandom[:], req.serverRandom)
	l.hwid, err = licenseHardwareID(l.cfg.ClientName)
	if err != nil {
		return err
	}
	body := binary.LittleEndian.AppendUint32(nil, 1) // KEY_EXCHANGE_ALG_RSA
	body = binary.LittleEndian.AppendUint32(body, licensePlatformID)
	body = append(body, clientRandom[:]...)
	body = appendLicenseBlob(body, 2, ciphertext)
	l.state = licenseAwaitChallenge
	if l.cached != nil && l.cached.hwid == l.hwid && l.cached.matches(req) {
		body = appendLicenseBlob(body, 1, l.cached.value)
		body = appendLicenseBlob(body, 9, cryptLicense(l.cipherKey, l.hwid[:]))
		body = append(body, licenseMAC(l.macKey, l.hwid[:])...)
		return l.send(0x12, body)
	}
	body = appendLicenseBlob(body, 0x0f, licenseName(l.cfg.User))
	body = appendLicenseBlob(body, 0x10, licenseName(l.cfg.ClientName))
	return l.send(0x13, body)
}

func (l *licenseExchange) challenge(p []byte) error {
	if l.state != licenseAwaitChallenge || len(p) < 4 {
		return malformedLicense("out-of-sequence or truncated RDP platform challenge")
	}
	// The platform challenge blob type is unused by Microsoft servers.
	_, encrypted, mac, ok := readLicenseBlob(p[4:])
	if !ok || len(encrypted) == 0 || len(mac) != 16 {
		return malformedLicense("invalid RDP platform challenge data")
	}
	challenge := cryptLicense(l.cipherKey, encrypted)
	if !hmac.Equal(mac, licenseMAC(l.macKey, challenge)) {
		return &Error{Kind: KindLicense, Reason: ReasonProtocol, Code: 3, Err: errors.New("invalid RDP platform challenge MAC")}
	}
	response := []byte{0, 1, 0, 0xff, 3, 0} // v0x0100, OTHER_PLATFORMCHALLENGE_TYPE, LICENSE_DETAIL_DETAIL
	response = binary.LittleEndian.AppendUint16(response, uint16(len(challenge)))
	response = append(response, challenge...)
	body := appendLicenseBlob(nil, 9, cryptLicense(l.cipherKey, response))
	body = appendLicenseBlob(body, 9, cryptLicense(l.cipherKey, l.hwid[:]))
	body = append(body, licenseMAC(l.macKey, append(response, l.hwid[:]...))...)
	l.state = licenseAwaitIssue
	return l.send(0x15, body)
}

func (l *licenseExchange) acceptLicense(kind byte, p []byte) error {
	if l.state != licenseAwaitIssue && l.state != licenseAwaitChallenge {
		return malformedLicense("out-of-sequence RDP server license")
	}
	blobType, encrypted, mac, ok := readLicenseBlob(p)
	// Older licensing servers did not define a blob type in upgraded licenses.
	if !ok || (blobType != 9 && kind != 4) || len(encrypted) == 0 || len(mac) != 16 {
		return malformedLicense("invalid RDP server license data")
	}
	plain := cryptLicense(l.cipherKey, encrypted)
	if !hmac.Equal(mac, licenseMAC(l.macKey, plain)) {
		return &Error{Kind: KindLicense, Reason: ReasonProtocol, Code: 3, Err: errors.New("invalid RDP server license MAC")}
	}
	license, err := parseIssuedLicense(plain)
	if err != nil {
		return err
	}
	license.hwid = l.hwid
	l.issued = license
	l.state = licenseComplete
	l.clearKeys()
	return nil
}

func (l *licenseExchange) alert(p []byte) error {
	if len(p) < 12 {
		return malformedLicense("truncated RDP licensing error message")
	}
	code, state := binary.LittleEndian.Uint32(p), binary.LittleEndian.Uint32(p[4:])
	kind, details, rest, ok := readLicenseBlob(p[8:])
	// An empty blob's type means nothing: some servers send any.
	if !ok || (kind != 4 && len(details) != 0) || len(rest) != 0 {
		return malformedLicense("invalid RDP licensing error details")
	}
	switch state {
	case 2:
		if code == 7 { // STATUS_VALID_CLIENT, ST_NO_TRANSITION
			l.state = licenseComplete
			l.clearKeys()
			return nil
		}
	case 3: // ST_RESET_PHASE_TO_START
		l.state = licenseStart
		l.cached = nil
		l.clearKeys()
		return nil
	case 4: // ST_RESEND_LAST_MESSAGE
		if l.last != nil {
			return l.transport.sendData(l.transport.globalID, l.last)
		}
	}
	return &Error{Kind: KindLicense, Code: code, Err: fmt.Errorf("RDP licensing failed (error 0x%08x, transition %d)", code, state)}
}

func (l *licenseExchange) send(kind byte, body []byte) error {
	if len(body) > 0x7fff-8 {
		return malformedLicense("RDP license message is too large")
	}
	p := binary.LittleEndian.AppendUint16([]byte{licenseFlag, 0, 0, 0, kind, licensePreamble}, uint16(len(body)+4))
	p = append(p, body...)
	if err := l.transport.sendData(l.transport.globalID, p); err != nil {
		return err
	}
	clear(l.last)
	l.last = p
	return nil
}

func licensePacketName(kind byte) string {
	switch kind {
	case 1:
		return "LICENSE_REQUEST"
	case 2:
		return "PLATFORM_CHALLENGE"
	case 3:
		return "NEW_LICENSE"
	case 4:
		return "UPGRADE_LICENSE"
	case 0xff:
		return "LICENSE_ERROR_ALERT"
	default:
		return "LICENSING_PDU"
	}
}

func licenseHardwareID(name string) ([20]byte, error) {
	// Hash the client and host names into a stable 16-byte identifier.
	var id [20]byte
	host, err := os.Hostname()
	if err != nil {
		return id, &Error{Kind: KindLicense, Err: fmt.Errorf("read RDP licensing host name: %w", err)}
	}
	digest := sha256.Sum256([]byte(name + "\x00" + host))
	binary.LittleEndian.PutUint32(id[:], licensePlatformID)
	copy(id[4:], digest[:])
	return id, nil
}

func licenseName(s string) []byte {
	s, _, _ = strings.Cut(s, "\x00")
	p := make([]byte, 0, min(len(s)+1, 256))
	for _, r := range s {
		if r > 255 {
			r = '?'
		}
		if len(p) == 255 {
			break
		}
		p = append(p, byte(r))
	}
	return append(p, 0)
}

func malformedLicense(message string) error {
	return &Error{Kind: KindLicense, Reason: ReasonProtocol, Err: errors.New(message)}
}

func licenseZero(p []byte) bool {
	return bytes.Count(p, []byte{0}) == len(p)
}
