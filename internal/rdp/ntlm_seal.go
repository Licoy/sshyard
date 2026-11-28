package rdp

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
)

// NTLM Extended Session Security maintains independent keys, RC4 state and
// sequence numbers for each direction (MS-NLMP 3.4).
type ntlmSealState struct {
	signingKey [16]byte
	cipher     *rc4.Cipher
	keyExch    bool
	seq        uint32
}

func newNTLMSealState(key []byte, flags uint32, direction string) ntlmSealState {
	signing := md5.Sum(append(append([]byte(nil), key...), "session key to "+direction+" signing key magic constant\x00"...))
	sealing := md5.Sum(append(append([]byte(nil), key...), "session key to "+direction+" sealing key magic constant\x00"...))
	cipher, _ := rc4.NewCipher(sealing[:]) // Exactly 16 bytes, which is always valid.
	clear(sealing[:])
	return ntlmSealState{signingKey: signing, cipher: cipher, keyExch: flags&ntlmKeyExchange != 0}
}

func (s *ntlmSealState) clear() {
	clear(s.signingKey[:])
	s.cipher.Reset()
}

func (s *ntlmSealState) signature(p []byte) [16]byte {
	var sig [16]byte
	sig[0] = 1
	seq := sig[12:16]
	binary.LittleEndian.PutUint32(seq, s.seq)
	copy(sig[4:12], ntlmHMAC(s.signingKey[:], seq, p)[:8])
	if s.keyExch {
		s.cipher.XORKeyStream(sig[4:12], sig[4:12])
	}
	s.seq++
	return sig
}

func (s *ntlmSealState) seal(p []byte) []byte {
	out := make([]byte, 16+len(p))
	s.cipher.XORKeyStream(out[16:], p)
	sig := s.signature(p)
	copy(out[:16], sig[:])
	return out
}

func (s *ntlmSealState) unseal(p []byte) ([]byte, error) {
	if len(p) < 16 {
		return nil, protocolError("truncated NTLM sealed message")
	}
	out := make([]byte, len(p)-16)
	s.cipher.XORKeyStream(out, p[16:])
	sig := s.signature(out)
	if !hmac.Equal(sig[:], p[:16]) {
		clear(out)
		return nil, securityError(0, "invalid NTLM sealed message signature")
	}
	return out, nil
}
