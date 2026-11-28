package rdp

import (
	"bytes"
	"crypto/hmac"
	"encoding/asn1"
)

// RFC 4178 wraps the optimistic NTLM token in a GSS InitialContextToken.
var (
	spnegoMechanismOID = []byte{6, 6, 0x2b, 6, 1, 5, 5, 2}
	ntlmMechanismOID   = []byte{6, 10, 0x2b, 6, 1, 4, 1, 0x82, 0x37, 2, 2, 10}
	spnegoNTLMList     = spnegoDER(0x30, ntlmMechanismOID)
)

func spnegoInit(ntlm []byte) []byte {
	p := spnegoDER(0xa0, spnegoNTLMList)
	p = append(p, spnegoDER(0xa2, spnegoDER(4, ntlm))...)
	p = spnegoDER(0xa0, spnegoDER(0x30, p))
	return spnegoDER(0x60, append(append([]byte(nil), spnegoMechanismOID...), p...))
}

func spnegoAuth(ntlm, mic []byte) []byte {
	p := spnegoDER(0xa2, spnegoDER(4, ntlm))
	if mic != nil {
		p = append(p, spnegoDER(0xa3, spnegoDER(4, mic))...)
	}
	return spnegoDER(0xa1, spnegoDER(0x30, p))
}

func spnegoDER(tag byte, p []byte) []byte {
	h := []byte{tag}
	if len(p) > 65535 {
		h = append(h, 0x83, byte(len(p)>>16), byte(len(p)>>8), byte(len(p)))
	} else {
		h = appendMCSBERLength(h, len(p))
	}
	return append(h, p...)
}

type spnegoResponse struct {
	state int
	token []byte
	mic   []byte
	raw   bool
}

func parseSPNEGO(p []byte) (spnegoResponse, error) {
	s := spnegoResponse{state: -1}
	if bytes.HasPrefix(p, []byte(ntlmSignature)) {
		s.token, s.raw = p, true
		return s, nil
	}
	var wrapper, seq asn1.RawValue
	if !unmarshalRDPDER(p, &wrapper) || wrapper.Class != asn1.ClassContextSpecific || wrapper.Tag != 1 || !wrapper.IsCompound ||
		!unmarshalRDPDER(wrapper.Bytes, &seq) || seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence || !seq.IsCompound {
		return s, protocolError("invalid SPNEGO NegTokenResp")
	}
	lastTag := -1
	for data := seq.Bytes; len(data) != 0; {
		var item asn1.RawValue
		rest, err := asn1.Unmarshal(data, &item)
		if err != nil || item.Class != asn1.ClassContextSpecific || !item.IsCompound || item.Tag <= lastTag {
			return s, protocolError("invalid SPNEGO response field")
		}
		data, lastTag = rest, item.Tag
		switch item.Tag {
		case 0:
			var state asn1.Enumerated
			if !unmarshalRDPDER(item.Bytes, &state) || state < 0 || state > 3 {
				return s, protocolError("invalid SPNEGO negotiation state")
			}
			if state == 2 {
				return s, spnegoRejected()
			}
			s.state = int(state)
		case 1:
			if !bytes.Equal(item.Bytes, ntlmMechanismOID) {
				return s, securityError(0, "server selected an unsupported SPNEGO mechanism")
			}
		case 2:
			if !unmarshalRDPDER(item.Bytes, &s.token) {
				return s, protocolError("invalid SPNEGO response token")
			}
		case 3:
			if !unmarshalRDPDER(item.Bytes, &s.mic) {
				return s, protocolError("invalid SPNEGO mechanism MIC")
			}
		}
	}
	return s, nil
}

func spnegoNTLMMIC(key []byte, flags uint32, direction string) []byte {
	// SPNEGO's NTLM signing is independent of CredSSP's sealing state.
	state := newNTLMSealState(key, flags, direction)
	defer state.clear()
	sig := state.signature(spnegoNTLMList)
	return sig[:]
}

func verifyFinalSPNEGO(tokens []tsNegoToken, key []byte, flags uint32, wantMIC bool) error {
	if len(tokens) == 0 && !wantMIC {
		return nil
	}
	if len(tokens) != 1 {
		return protocolError("invalid CredSSP final SPNEGO token count")
	}
	response, err := parseSPNEGO(tokens[0].Token)
	if err != nil {
		return err
	}
	if (response.state != 0 && response.state != -1) || len(response.token) != 0 {
		return protocolError("CredSSP SPNEGO authentication did not complete")
	}
	if (wantMIC || response.mic != nil) && !hmac.Equal(response.mic, spnegoNTLMMIC(key, flags, "server-to-client")) {
		return securityError(0, "invalid SPNEGO server mechanism MIC")
	}
	return nil
}
