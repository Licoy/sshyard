package rdp

import (
	"bytes"
	"crypto/hmac"
	"crypto/rc4"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
)

func serveTestCredSSP(conn *tls.Conn, cert *x509.Certificate, cfg Config, password []uint16, version int, protocol uint32, corruptBinding bool) error {
	init, err := readTSRequest(conn)
	negotiate := ntlmNegotiate()
	if err != nil || len(init.Tokens) != 1 || !bytes.Equal(init.Tokens[0].Token, spnegoInit(negotiate)) {
		return fmt.Errorf("bad CredSSP initial message: %v", err)
	}
	challenge := handshakeTestChallenge()
	var token = challenge
	if version >= 5 {
		response := []byte{0xa0, 3, 0x0a, 1, 1}
		response = append(response, spnegoDER(0xa1, ntlmMechanismOID)...)
		response = append(response, spnegoDER(0xa2, spnegoDER(4, challenge))...)
		token = spnegoDER(0xa1, spnegoDER(0x30, response))
	}
	if err := sendTSRequest(conn, tsRequest{Version: version, Tokens: []tsNegoToken{{token}}}); err != nil {
		return err
	}
	req, err := readTSRequest(conn)
	if err != nil || len(req.Tokens) != 1 || len(req.PubKeyAuth) == 0 || req.AuthInfo != nil || (len(req.ClientNonce) == 32) != (version >= 5) {
		return fmt.Errorf("invalid CredSSP authenticate fields: %v", err)
	}
	auth, err := parseSPNEGO(req.Tokens[0].Token)
	if err != nil || len(auth.token) < 88 {
		return fmt.Errorf("invalid CredSSP auth token: %v", err)
	}
	key, err := verifyTestNTLMAuth(cert, cfg, password, negotiate, challenge, auth.token)
	if err != nil {
		return err
	}
	return serveTestCredSSPBinding(conn, cert, cfg, password, version, protocol, req, key, corruptBinding)
}

func handshakeTestChallenge() []byte {
	p := make([]byte, 56)
	copy(p, ntlmSignature)
	le := binary.LittleEndian
	le.PutUint32(p[8:], 2)
	le.PutUint32(p[20:], ntlmClientFlags)
	copy(p[24:32], "NtlmTest")
	copy(p[48:], ntlmClientVersion)
	info := appendNTLMAV(nil, 1, ntlmUTF16("Server"))
	info = appendNTLMAV(info, 2, ntlmUTF16("Domain"))
	info = appendNTLMAV(info, 7, make([]byte, 8))
	info = append(info, 0, 0, 0, 0)
	le.PutUint16(p[40:], uint16(len(info)))
	le.PutUint16(p[42:], uint16(len(info)))
	le.PutUint32(p[44:], uint32(len(p)))
	return append(p, info...)
}

func verifyTestNTLMAuth(cert *x509.Certificate, cfg Config, password []uint16, negotiate, challenge, auth []byte) ([]byte, error) {
	nt, ok := ntlmPayload(auth, 20, 88)
	if !ok || len(nt) < 52 {
		return nil, errors.New("invalid test NTLM proof field")
	}
	hash := ntlmResponseKey(cfg.User, cfg.Domain, password)
	proof := ntlmHMAC(hash, challenge[24:32], nt[16:])
	if !hmac.Equal(proof, nt[:16]) {
		return nil, errors.New("NTLM proof mismatch")
	}
	sealedKey, ok := ntlmPayload(auth, 52, 88)
	if !ok || len(sealedKey) != 16 {
		return nil, errors.New("missing NTLM exported session key")
	}
	cipher, err := rc4.NewCipher(ntlmHMAC(hash, proof))
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	cipher.XORKeyStream(key, sealedKey)
	zeroMIC := bytes.Clone(auth)
	clear(zeroMIC[72:88])
	if !hmac.Equal(auth[72:88], ntlmHMAC(key, negotiate, challenge, zeroMIC)) {
		return nil, errors.New("NTLM authenticate MIC mismatch")
	}
	info, err := parseNTLMAV(nt[44 : len(nt)-4])
	if err != nil {
		return nil, err
	}
	cbt := ntlmChannelBinding(cert)
	for _, pair := range info {
		if pair.id == 10 && bytes.Equal(pair.value, cbt[:]) {
			return key, nil
		}
	}
	return nil, errors.New("NTLM auth omitted channel binding")
}

func serveTestCredSSPBinding(conn *tls.Conn, cert *x509.Certificate, cfg Config, password []uint16, version int, protocol uint32, req tsRequest, key []byte, corrupt bool) error {
	pub, err := credSSPPublicKey(cert)
	if err != nil {
		return err
	}
	in := newNTLMSealState(key, ntlmClientFlags, "client-to-server")
	out := newNTLMSealState(key, ntlmClientFlags, "server-to-client")
	expected, response := credSSPBindings(pub, req.ClientNonce)
	binding, err := in.unseal(req.PubKeyAuth)
	if err != nil || !bytes.Equal(binding, expected) {
		return fmt.Errorf("client public-key binding failed: %v", err)
	}
	if corrupt {
		response[0] ^= 1
	}
	if err := sendTSRequest(conn, tsRequest{Version: version, PubKeyAuth: out.seal(response)}); err != nil {
		return err
	}
	req, err = readTSRequest(conn)
	if corrupt && err != nil {
		return nil
	}
	if err != nil || len(req.AuthInfo) == 0 {
		return fmt.Errorf("missing delegated CredSSP credentials: %v", err)
	}
	if corrupt {
		return errors.New("client delegated credentials despite binding failure")
	}
	if err := verifyTestCredSSPPassword(&in, req.AuthInfo, cfg, password); err != nil {
		return err
	}
	if protocol == protocolHybridEx {
		return writeRDP(conn, []byte{0, 0, 0, 0})
	}
	return nil
}

func verifyTestCredSSPPassword(in *ntlmSealState, auth []byte, cfg Config, password []uint16) error {
	p, err := in.unseal(auth)
	if err != nil {
		return err
	}
	var creds struct {
		Type  int    `asn1:"explicit,tag:0"`
		Value []byte `asn1:"explicit,tag:1"`
	}
	var inner struct {
		Domain   []byte `asn1:"explicit,tag:0"`
		User     []byte `asn1:"explicit,tag:1"`
		Password []byte `asn1:"explicit,tag:2"`
	}
	if !unmarshalRDPDER(p, &creds) || creds.Type != 1 || !unmarshalRDPDER(creds.Value, &inner) ||
		!bytes.Equal(inner.User, ntlmUTF16(cfg.User)) || !bytes.Equal(inner.Domain, ntlmUTF16(cfg.Domain)) || !bytes.Equal(inner.Password, rdpUTF16Bytes(password)) {
		return errors.New("delegated TS password credentials did not match")
	}
	return nil
}
