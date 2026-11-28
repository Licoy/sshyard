package rdp

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/md4"
)

const (
	ntlmSignature              = "NTLMSSP\x00"
	ntlmUnicode         uint32 = 0x00000001
	ntlmRequestTarget   uint32 = 0x00000004
	ntlmSign            uint32 = 0x00000010
	ntlmSeal            uint32 = 0x00000020
	ntlmNegotiateNTLM   uint32 = 0x00000200
	ntlmAlwaysSign      uint32 = 0x00008000
	ntlmExtendedSession uint32 = 0x00080000
	ntlmTargetInfo      uint32 = 0x00800000
	ntlmVersion         uint32 = 0x02000000
	ntlm128             uint32 = 0x20000000
	ntlmKeyExchange     uint32 = 0x40000000
	ntlmRequired               = ntlmUnicode | ntlmSign | ntlmSeal | ntlmNegotiateNTLM | ntlmExtendedSession | ntlmTargetInfo | ntlm128
	ntlmClientFlags            = ntlmRequired | ntlmRequestTarget | ntlmAlwaysSign | ntlmVersion | ntlmKeyExchange
)

var ntlmClientVersion = []byte{6, 1, 0xb1, 0x1d, 0, 0, 0, 15}

func ntlmNegotiate() []byte {
	p := binary.LittleEndian.AppendUint32([]byte(ntlmSignature), 1)
	p = binary.LittleEndian.AppendUint32(p, ntlmClientFlags)
	return append(append(p, make([]byte, 16)...), ntlmClientVersion...)
}

type ntlmChallenge struct {
	flags     uint32
	nonce     []byte
	av        []ntlmAV
	timestamp []byte
}

func parseNTLMChallenge(p []byte) (ntlmChallenge, error) {
	if len(p) < 48 || !bytes.HasPrefix(p, []byte(ntlmSignature)) || binary.LittleEndian.Uint32(p[8:]) != 2 {
		return ntlmChallenge{}, protocolError("invalid NTLM Challenge header")
	}
	flags := binary.LittleEndian.Uint32(p[20:])
	if flags&ntlmRequired != ntlmRequired {
		return ntlmChallenge{}, securityError(flags, "server did not negotiate NTLMv2 signing and 128-bit sealing")
	}
	payloadOffset := 48
	if flags&ntlmVersion != 0 {
		payloadOffset += 8
	}
	if len(p) < payloadOffset {
		return ntlmChallenge{}, protocolError("truncated NTLM Challenge version")
	}
	if _, ok := ntlmPayload(p, 12, payloadOffset); !ok {
		return ntlmChallenge{}, protocolError("invalid NTLM Challenge target name")
	}
	info, ok := ntlmPayload(p, 40, payloadOffset)
	if !ok {
		return ntlmChallenge{}, protocolError("invalid NTLM Challenge target info")
	}
	av, err := parseNTLMAV(info)
	if err != nil {
		return ntlmChallenge{}, err
	}
	var computer, domain bool
	var timestamp []byte
	for _, pair := range av {
		switch pair.id {
		case 1:
			computer = len(pair.value) > 0
		case 2:
			domain = len(pair.value) > 0
		case 7:
			timestamp = pair.value
		}
	}
	if !computer || !domain {
		return ntlmChallenge{}, &Error{Kind: KindAuth, Reason: ReasonLogonFailure, Err: protocolError("NTLM Challenge omitted NetBIOS computer or domain name")}
	}
	return ntlmChallenge{flags: flags & ntlmClientFlags, nonce: p[24:32], av: av, timestamp: timestamp}, nil
}

func ntlmPayload(p []byte, field, minimumOffset int) ([]byte, bool) {
	d := p[field : field+8]
	n := int(binary.LittleEndian.Uint16(d))
	if n == 0 {
		return nil, true
	}
	off := uint64(binary.LittleEndian.Uint32(d[4:]))
	if off < uint64(minimumOffset) || off+uint64(n) > uint64(len(p)) {
		return nil, false
	}
	return p[off : off+uint64(n)], true
}

type ntlmAV struct {
	id    uint16
	value []byte
}

func parseNTLMAV(p []byte) ([]ntlmAV, error) {
	var av []ntlmAV
	seen := make(map[uint16]bool)
	for len(p) >= 4 {
		id := binary.LittleEndian.Uint16(p)
		n := int(binary.LittleEndian.Uint16(p[2:]))
		p = p[4:]
		if id == 0 && n == 0 && len(p) == 0 {
			return av, nil
		}
		if id == 0 || n > len(p) || seen[id] {
			break
		}
		switch id {
		case 1, 2, 3, 4, 5, 9:
			if n%2 != 0 {
				return nil, protocolError("misaligned NTLM target-info Unicode value")
			}
		case 6, 7, 10:
			expected := [...]int{6: 4, 7: 8, 10: 16}
			if n != expected[id] {
				return nil, protocolError("invalid NTLM target-info value length")
			}
		}
		seen[id] = true
		av = append(av, ntlmAV{id, p[:n]})
		p = p[n:]
	}
	return nil, protocolError("invalid NTLM target-info list")
}

func buildNTLMTargetInfo(av []ntlmAV, cbt []byte, target string) []byte {
	var p []byte
	flags := uint32(0x02) // MIC provided
	for _, pair := range av {
		switch pair.id {
		case 6:
			flags |= binary.LittleEndian.Uint32(pair.value)
		case 9, 10:
			// These values originate on the client.
		default:
			p = appendNTLMAV(p, pair.id, pair.value)
		}
	}
	p = appendNTLMAV(p, 6, binary.LittleEndian.AppendUint32(nil, flags))
	p = appendNTLMAV(p, 9, ntlmUTF16(target))
	p = appendNTLMAV(p, 10, cbt)
	return append(p, 0, 0, 0, 0)
}

func appendNTLMAV(p []byte, id uint16, value []byte) []byte {
	p = binary.LittleEndian.AppendUint16(p, id)
	p = binary.LittleEndian.AppendUint16(p, uint16(len(value)))
	return append(p, value...)
}

func ntlmUTF16(s string) []byte {
	return rdpUTF16Bytes(utf16.Encode([]rune(s)))
}

func ntlmResponseKey(user, domain string, password []uint16) []byte {
	h := md4.New()
	p := rdpUTF16Bytes(password)
	_, _ = h.Write(p)
	clear(p)
	ntHash := h.Sum(nil)
	h.Reset()
	defer clear(ntHash)
	return ntlmHMAC(ntHash, ntlmUTF16(strings.ToUpper(user)+domain))
}

func ntlmV2Responses(key, serverNonce, clientNonce, timestamp, targetInfo []byte) (nt, lm, sessionKey []byte) {
	blob := []byte{1, 1, 0, 0, 0, 0, 0, 0}
	blob = append(blob, timestamp...)
	blob = append(blob, clientNonce...)
	blob = append(blob, 0, 0, 0, 0)
	blob = append(blob, targetInfo...)
	blob = append(blob, 0, 0, 0, 0)
	proof := ntlmHMAC(key, serverNonce, blob)
	nt = append(append([]byte(nil), proof...), blob...)
	lm = append(ntlmHMAC(key, serverNonce, clientNonce), clientNonce...)
	sessionKey = ntlmHMAC(key, proof)
	return nt, lm, sessionKey
}

func ntlmAuthenticate(cfg Config, password []uint16, challenge, negotiate []byte, cbt []byte, target string) ([]byte, []byte, uint32, error) {
	c, err := parseNTLMChallenge(challenge)
	if err != nil {
		return nil, nil, 0, err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, 0, err
	}
	timestamp := c.timestamp
	if timestamp == nil {
		const unixToFileTime = 116444736000000000
		timestamp = binary.LittleEndian.AppendUint64(nil, uint64(time.Now().UnixNano()/100+unixToFileTime))
	}
	key := ntlmResponseKey(cfg.User, cfg.Domain, password)
	defer clear(key)
	info := buildNTLMTargetInfo(c.av, cbt, target)
	nt, lm, exported := ntlmV2Responses(key, c.nonce, nonce[:], timestamp, info)
	if c.timestamp != nil {
		clear(lm)
	}
	var encryptedKey []byte
	if c.flags&ntlmKeyExchange != 0 {
		cipher, err := rc4.NewCipher(exported)
		clear(exported)
		if err != nil {
			return nil, nil, 0, err
		}
		defer cipher.Reset()
		if _, err := rand.Read(exported); err != nil {
			return nil, nil, 0, err
		}
		encryptedKey = make([]byte, len(exported))
		cipher.XORKeyStream(encryptedKey, exported)
	}
	p, err := marshalNTLMAuthenticate(c.flags, negotiate, challenge, exported, lm, nt, ntlmUTF16(cfg.Domain), ntlmUTF16(cfg.User), ntlmUTF16(cfg.ClientName), encryptedKey)
	if err != nil {
		clear(exported)
		return nil, nil, 0, err
	}
	return p, exported, c.flags, nil
}

func marshalNTLMAuthenticate(flags uint32, negotiate, challenge, key []byte, fields ...[]byte) ([]byte, error) {
	p := make([]byte, 64)
	copy(p, ntlmSignature)
	le := binary.LittleEndian
	le.PutUint32(p[8:], 3)
	le.PutUint32(p[60:], flags)
	if flags&ntlmVersion != 0 {
		p = append(p, ntlmClientVersion...)
	}
	micOffset := len(p)
	p = append(p, make([]byte, md5.Size)...)
	for i, field := range fields {
		if len(field) > 65535 {
			return nil, protocolError("NTLM Authenticate field exceeds 65535 bytes")
		}
		d := p[12+8*i:]
		le.PutUint16(d, uint16(len(field)))
		le.PutUint16(d[2:], uint16(len(field)))
		le.PutUint32(d[4:], uint32(len(p)))
		p = append(p, field...)
	}
	copy(p[micOffset:], ntlmHMAC(key, negotiate, challenge, p))
	return p, nil
}

func ntlmHMAC(key []byte, parts ...[]byte) []byte {
	h := hmac.New(md5.New, key)
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	return h.Sum(nil)
}

// ntlmChannelBinding hashes the RFC 5929 tls-server-end-point GSS binding.
func ntlmChannelBinding(cert *x509.Certificate) [16]byte {
	var digest []byte
	switch cert.SignatureAlgorithm {
	case x509.SHA384WithRSA, x509.SHA384WithRSAPSS, x509.ECDSAWithSHA384:
		hash := sha512.Sum384(cert.Raw)
		digest = hash[:]
	case x509.SHA512WithRSA, x509.SHA512WithRSAPSS, x509.ECDSAWithSHA512:
		hash := sha512.Sum512(cert.Raw)
		digest = hash[:]
	default:
		hash := sha256.Sum256(cert.Raw)
		digest = hash[:]
	}
	const label = "tls-server-end-point:"
	p := binary.LittleEndian.AppendUint32(make([]byte, 16), uint32(len(label)+len(digest)))
	return md5.Sum(append(append(p, label...), digest...))
}
