package rdp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
)

const credSSPVersion = 6

func credSSP(conn *tls.Conn, cfg Config, password []uint16) error {
	cert := conn.ConnectionState().PeerCertificates[0]
	pub, err := credSSPPublicKey(cert)
	if err != nil {
		return err
	}
	negotiate := ntlmNegotiate()
	if err := sendTSRequest(conn, tsRequest{Version: credSSPVersion, Tokens: []tsNegoToken{{spnegoInit(negotiate)}}}); err != nil {
		return err
	}
	request, err := readTSRequest(conn)
	if err != nil {
		return err
	}
	if len(request.Tokens) != 1 || request.AuthInfo != nil || request.PubKeyAuth != nil || request.ClientNonce != nil {
		return protocolError("invalid CredSSP challenge fields")
	}
	challenge, err := parseSPNEGO(request.Tokens[0].Token)
	if err != nil {
		return err
	}
	if challenge.state == 0 || len(challenge.token) == 0 || challenge.mic != nil {
		return protocolError("invalid CredSSP SPNEGO challenge")
	}
	cbt := ntlmChannelBinding(cert)
	auth, key, flags, err := ntlmAuthenticate(cfg, password, challenge.token, negotiate, cbt[:], "TERMSRV/"+rdpHostname(cfg.Addr))
	if err != nil {
		return err
	}
	defer clear(key)
	out := newNTLMSealState(key, flags, "client-to-server")
	defer out.clear()
	in := newNTLMSealState(key, flags, "server-to-client")
	defer in.clear()
	version := min(request.Version, credSSPVersion)
	if err := credSSPVerifyServer(conn, version, pub, auth, &challenge, key, flags, &out, &in); err != nil {
		return err
	}
	return sendCredSSPPassword(conn, cfg, password, &out)
}

func credSSPVerifyServer(conn io.ReadWriter, version int, pub, auth []byte, challenge *spnegoResponse, key []byte, flags uint32, out, in *ntlmSealState) error {
	var nonce []byte
	if version >= 5 {
		nonce = make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
	}
	ownBinding, peerBinding := credSSPBindings(pub, nonce)
	if !challenge.raw {
		var mic []byte
		if challenge.state == 3 {
			mic = spnegoNTLMMIC(key, flags, "client-to-server")
		}
		auth = spnegoAuth(auth, mic)
	}
	req := tsRequest{Version: credSSPVersion, Tokens: []tsNegoToken{{auth}}, PubKeyAuth: out.seal(ownBinding), ClientNonce: nonce}
	if err := sendTSRequest(conn, req); err != nil {
		return err
	}
	response, err := readTSRequest(conn)
	if err != nil {
		return err
	}
	if min(response.Version, credSSPVersion) != version || response.AuthInfo != nil || response.ClientNonce != nil || response.PubKeyAuth == nil {
		return protocolError("invalid CredSSP public-key response")
	}
	if err := verifyFinalSPNEGO(response.Tokens, key, flags, challenge.state == 3); err != nil {
		return err
	}
	peer, err := in.unseal(response.PubKeyAuth)
	if err != nil {
		return err
	}
	if !hmac.Equal(peerBinding, peer) {
		return securityError(0, "CredSSP server certificate binding mismatch")
	}
	return nil
}

func credSSPPublicKey(cert *x509.Certificate) ([]byte, error) {
	var spki struct {
		Algorithm asn1.RawValue
		Key       asn1.BitString
	}
	rest, err := asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &spki)
	if err != nil || len(rest) != 0 || len(spki.Key.Bytes) == 0 || spki.Key.BitLength != len(spki.Key.Bytes)*8 {
		return nil, securityError(0, "invalid CredSSP certificate subject public key")
	}
	return spki.Key.Bytes, nil
}

func credSSPBindings(key, nonce []byte) (client, server []byte) {
	if nonce == nil {
		server = append([]byte(nil), key...)
		server[0]++
		return key, server
	}
	return credSSPHash("Client-To-Server", nonce, key), credSSPHash("Server-To-Client", nonce, key)
}

func credSSPHash(direction string, nonce, key []byte) []byte {
	h := sha256.New()
	_, _ = io.WriteString(h, "CredSSP "+direction+" Binding Hash\x00")
	_, _ = h.Write(nonce)
	_, _ = h.Write(key)
	return h.Sum(nil)
}

func sendCredSSPPassword(conn io.Writer, cfg Config, password []uint16, out *ntlmSealState) error {
	p := rdpUTF16Bytes(password)
	defer clear(p)
	inner, err := asn1.Marshal(struct {
		Domain   []byte `asn1:"explicit,tag:0"`
		User     []byte `asn1:"explicit,tag:1"`
		Password []byte `asn1:"explicit,tag:2"`
	}{ntlmUTF16(cfg.Domain), ntlmUTF16(cfg.User), p})
	if err != nil {
		return err
	}
	defer clear(inner)
	creds, err := asn1.Marshal(struct {
		Type  int    `asn1:"explicit,tag:0"`
		Value []byte `asn1:"explicit,tag:1"`
	}{1, inner})
	if err != nil {
		return err
	}
	defer clear(creds)
	return sendTSRequest(conn, tsRequest{Version: credSSPVersion, AuthInfo: out.seal(creds)})
}

type tsNegoToken struct {
	Token []byte `asn1:"explicit,tag:0"`
}

type tsRequest struct {
	Version     int           `asn1:"explicit,tag:0"`
	Tokens      []tsNegoToken `asn1:"optional,explicit,tag:1"`
	AuthInfo    []byte        `asn1:"optional,explicit,tag:2"`
	PubKeyAuth  []byte        `asn1:"optional,explicit,tag:3"`
	ErrorCode   asn1.RawValue `asn1:"optional,explicit,tag:4"`
	ClientNonce []byte        `asn1:"optional,explicit,tag:5"`
}

func sendTSRequest(w io.Writer, r tsRequest) error {
	p, err := asn1.Marshal(r)
	if err != nil {
		return err
	}
	if err := writeRDP(w, p); err != nil {
		return transportError(err)
	}
	return nil
}

func readTSRequest(r io.Reader) (tsRequest, error) {
	p, err := readCredSSPDER(r)
	if err != nil {
		return tsRequest{}, err
	}
	return parseTSRequest(p)
}

func parseTSRequest(p []byte) (tsRequest, error) {
	var req tsRequest
	if !validTSRequestFields(p) || !unmarshalRDPDER(p, &req) || req.Version < 2 || (req.ClientNonce != nil && len(req.ClientNonce) != 32) {
		return tsRequest{}, protocolError("invalid CredSSP TSRequest")
	}
	if len(req.ErrorCode.FullBytes) != 0 {
		var code int64
		if !unmarshalRDPDER(req.ErrorCode.Bytes, &code) || code < -1<<31 || code > 0xffffffff {
			return tsRequest{}, protocolError("invalid CredSSP NTSTATUS")
		}
		return tsRequest{}, credSSPStatus(uint32(code))
	}
	return req, nil
}

// encoding/asn1 permits extra elements after unmarshaling a SEQUENCE into a
// struct. Check ordering and repetition before using any credential fields.
func validTSRequestFields(p []byte) bool {
	var sequence asn1.RawValue
	if !unmarshalRDPDER(p, &sequence) || sequence.Class != asn1.ClassUniversal || sequence.Tag != asn1.TagSequence || !sequence.IsCompound {
		return false
	}
	lastTag := -1
	for fields := sequence.Bytes; len(fields) != 0; {
		var field asn1.RawValue
		rest, err := asn1.Unmarshal(fields, &field)
		if err != nil || field.Class != asn1.ClassContextSpecific || !field.IsCompound || field.Tag <= lastTag || field.Tag > 5 {
			return false
		}
		lastTag, fields = field.Tag, rest
	}
	return lastTag >= 0
}

func readCredSSPDER(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:2]); err != nil {
		return nil, transportError(err)
	}
	if header[0] != 0x30 {
		return nil, protocolError("invalid CredSSP DER tag")
	}
	count, length := 2, int(header[1])
	if length&0x80 != 0 {
		n := length & 0x7f
		if n == 0 || n > 3 {
			return nil, protocolError("invalid CredSSP DER length")
		}
		count += n
		if _, err := io.ReadFull(r, header[2:count]); err != nil {
			return nil, transportError(err)
		}
		length = 0
		for _, b := range header[2:count] {
			length = length<<8 | int(b)
		}
	}
	if length < 5 || length > 64*1024 {
		return nil, protocolError("CredSSP message length is out of bounds")
	}
	p := make([]byte, count+length)
	copy(p, header[:count])
	if _, err := io.ReadFull(r, p[count:]); err != nil {
		return nil, transportError(err)
	}
	return p, nil
}

func unmarshalRDPDER(p []byte, value any) bool {
	rest, err := asn1.Unmarshal(p, value)
	return err == nil && len(rest) == 0
}

func credSSPStatus(status uint32) error {
	var reason Reason
	switch status {
	case 0xc0000064, 0xc000006a, 0xc000006d, 0x8009030c: // No such user, wrong password, failed logon.
		reason = ReasonLogonFailure
	case 0xc0000234:
		reason = ReasonAccountLocked
	case 0xc0000072:
		reason = ReasonAccountDisabled
	case 0xc000006e, 0xc000006f, 0xc0000070:
		reason = ReasonAccountRestricted
	case 0xc0000193:
		reason = ReasonAccountExpired
	case 0xc0000071:
		reason = ReasonPasswordExpired
	case 0xc0000224:
		reason = ReasonPasswordMustChange
	case 0xc0000155, 0xc000015b:
		reason = ReasonLogonTypeNotGranted
	case 0xc0000022:
		reason = ReasonServerDenied
	case 0xc00000bb:
		return securityError(status, "server does not support the CredSSP version")
	default:
		reason = ReasonNone
	}
	return &Error{Kind: KindAuth, Reason: reason, Code: status, Err: fmt.Errorf("CredSSP authentication failed (NTSTATUS 0x%08x)", status)}
}

func spnegoRejected() error {
	return &Error{Kind: KindAuth, Reason: ReasonLogonFailure, Err: errors.New("server rejected CredSSP SPNEGO authentication")}
}
