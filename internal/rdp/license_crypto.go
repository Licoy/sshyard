package rdp

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
	"slices"
)

func (l *licenseExchange) licensePublicKey(cert []byte) (*rsa.PublicKey, error) {
	if len(cert) != 0 {
		return parseLicenseCertificate(cert)
	}
	// MS-RDPELE permits omitting the certificate when transport encryption
	// protects the licensing exchange. Here the verified TLS key is available.
	conn, ok := l.transport.Conn.(*tls.Conn)
	if ok {
		state := conn.ConnectionState()
		if state.HandshakeComplete && len(state.PeerCertificates) != 0 {
			if key, ok := state.PeerCertificates[0].PublicKey.(*rsa.PublicKey); ok {
				return key, nil
			}
		}
	}
	return nil, malformedLicense("RDP licensing requires an RSA server certificate")
}

func parseLicenseCertificate(p []byte) (*rsa.PublicKey, error) {
	if len(p) < 4 {
		return nil, malformedLicense("truncated RDP licensing certificate")
	}
	switch binary.LittleEndian.Uint32(p) & 0x7fffffff {
	case 1:
		return parseProprietaryLicenseCert(p)
	case 2:
		return parseX509LicenseCert(p[4:])
	default:
		return nil, malformedLicense("unsupported RDP licensing certificate version")
	}
}

func parseProprietaryLicenseCert(p []byte) (*rsa.PublicKey, error) {
	if len(p) < 12 || binary.LittleEndian.Uint32(p[4:]) != 1 || binary.LittleEndian.Uint32(p[8:]) != 1 {
		return nil, malformedLicense("invalid proprietary RDP licensing certificate algorithms")
	}
	kind, blob, sig, ok := readLicenseBlob(p[12:])
	if !ok || kind != 6 || len(blob) < 20 || !bytes.HasPrefix(blob, []byte("RSA1")) {
		return nil, malformedLicense("invalid proprietary RDP licensing RSA blob")
	}
	le := binary.LittleEndian
	n := uint64(le.Uint32(blob[4:]))
	bits := uint64(le.Uint32(blob[8:]))
	exponent := uint64(le.Uint32(blob[16:]))
	if bits < 512 || bits > 8192 || bits%8 != 0 || n != bits/8+8 || uint64(len(blob)) != n+20 ||
		uint64(le.Uint32(blob[12:])) != bits/8-1 || exponent < 3 || exponent%2 != 1 || exponent > 0x7fffffff || !licenseZero(blob[len(blob)-8:]) {
		return nil, malformedLicense("invalid proprietary RDP licensing RSA modulus")
	}
	kind, signature, rest, ok := readLicenseBlob(sig)
	if !ok || kind != 8 || len(rest) != 0 || !verifyProprietaryLicenseSig(p[:16+len(blob)], signature) {
		return nil, malformedLicense("invalid proprietary RDP licensing certificate signature")
	}
	return &rsa.PublicKey{N: licenseLittleInt(blob[20 : len(blob)-8]), E: int(exponent)}, nil
}

// MS-RDPBCGR 5.3.3.1.1 defines this public modulus and exponent for checking
// proprietary certificate consistency. TLS already verifies the RDP identity.
var licenseSigningModulus = [...]byte{
	0x3d, 0x3a, 0x5e, 0xbd, 0x72, 0x43, 0x3e, 0xc9, 0x4d, 0xbb, 0xc1, 0x1e, 0x4a, 0xba, 0x5f, 0xcb,
	0x3e, 0x88, 0x20, 0x87, 0xef, 0xf5, 0xc1, 0xe2, 0xd7, 0xb7, 0x6b, 0x9a, 0xf2, 0x52, 0x45, 0x95,
	0xce, 0x63, 0x65, 0x6b, 0x58, 0x3a, 0xfe, 0xef, 0x7c, 0xe7, 0xbf, 0xfe, 0x3d, 0xf6, 0x5c, 0x7d,
	0x6c, 0x5e, 0x06, 0x09, 0x1a, 0xf5, 0x61, 0xbb, 0x20, 0x93, 0x09, 0x5f, 0x05, 0x6d, 0xea, 0x87,
}

func verifyProprietaryLicenseSig(data, sig []byte) bool {
	if len(sig) != 72 || !licenseZero(sig[64:]) {
		return false
	}
	decoded := new(big.Int).Exp(licenseLittleInt(sig[:64]), big.NewInt(0xc0887b5b), licenseLittleInt(licenseSigningModulus[:])).Bytes()
	slices.Reverse(decoded)
	digest := md5.Sum(data)
	return len(decoded) == 63 && decoded[16] == 0 && decoded[62] == 1 &&
		bytes.Count(decoded[17:62], []byte{0xff}) == 45 && bytes.Equal(digest[:], decoded[:16])
}

func parseX509LicenseCert(p []byte) (*rsa.PublicKey, error) {
	if len(p) < 4 {
		return nil, malformedLicense("truncated RDP licensing certificate chain")
	}
	count := int(binary.LittleEndian.Uint32(p))
	if count < 2 || count > 200 {
		return nil, malformedLicense("invalid RDP licensing certificate chain length")
	}
	p = p[4:]
	var issuer *x509.Certificate
	for range count {
		der, rest, ok := readLicenseField(p)
		if !ok {
			return nil, malformedLicense("truncated DER certificate in RDP licensing chain")
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, malformedLicense("invalid DER certificate in RDP licensing chain")
		}
		if issuer != nil && issuer.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
			return nil, malformedLicense("invalid signature in RDP licensing chain")
		}
		issuer, p = cert, rest
	}
	if len(p) != 8+4*count {
		return nil, malformedLicense("invalid RDP licensing certificate padding")
	}
	key, ok := issuer.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, malformedLicense("RDP licensing certificate does not contain an RSA key")
	}
	return key, nil
}

func licenseLittleInt(p []byte) *big.Int {
	bigEndian := bytes.Clone(p)
	slices.Reverse(bigEndian)
	value := new(big.Int).SetBytes(bigEndian)
	clear(bigEndian)
	return value
}

// MS-RDPELE 5.1.1.1 references the raw little-endian RSA operation in
// MS-RDPBCGR 5.3.4.1. The operation is inside authenticated TLS.
func encryptLicensePremaster(key *rsa.PublicKey, secret []byte) ([]byte, error) {
	if key.N == nil || key.N.Sign() <= 0 || key.E < 3 || key.Size() < 64 || key.Size() > 1024 {
		return nil, malformedLicense("invalid RDP licensing RSA key")
	}
	message := licenseLittleInt(secret)
	if message.Cmp(key.N) >= 0 {
		return nil, malformedLicense("RDP licensing premaster exceeds RSA modulus")
	}
	ciphertext := new(big.Int).Exp(message, big.NewInt(int64(key.E)), key.N)
	message.SetInt64(0)
	p := make([]byte, key.Size()+8)
	ciphertext.FillBytes(p[:key.Size()])
	slices.Reverse(p[:key.Size()])
	return p, nil
}

func deriveLicenseKeys(secret, clientRandom, serverRandom []byte) (mac, enc [16]byte) {
	master := licenseSaltedHash(secret, clientRandom, serverRandom)
	defer clear(master[:])
	session := licenseSaltedHash(master[:], serverRandom, clientRandom)
	defer clear(session[:])
	copy(mac[:], session[:16])
	final := make([]byte, 0, 16+len(clientRandom)+len(serverRandom))
	final = append(append(append(final, session[16:32]...), clientRandom...), serverRandom...)
	defer clear(final)
	return mac, md5.Sum(final)
}

func licenseSaltedHash(secret, firstRandom, secondRandom []byte) [48]byte {
	var out [48]byte
	for i, prefix := range []string{"A", "BB", "CCC"} {
		sha := sha1.New()
		_, _ = io.WriteString(sha, prefix)
		_, _ = sha.Write(secret)
		_, _ = sha.Write(firstRandom)
		_, _ = sha.Write(secondRandom)
		md := md5.New()
		_, _ = md.Write(secret)
		_, _ = md.Write(sha.Sum(nil))
		copy(out[i*16:], md.Sum(nil))
	}
	return out
}

func licenseMAC(key [16]byte, p []byte) []byte {
	sha := sha1.New()
	_, _ = sha.Write(key[:])
	_, _ = sha.Write(bytes.Repeat([]byte{0x36}, 40))
	_, _ = sha.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(p))))
	_, _ = sha.Write(p)
	md := md5.New()
	_, _ = md.Write(key[:])
	_, _ = md.Write(bytes.Repeat([]byte{0x5c}, 48))
	_, _ = md.Write(sha.Sum(nil))
	return md.Sum(nil)
}

func cryptLicense(key [16]byte, p []byte) []byte {
	cipher, _ := rc4.NewCipher(key[:]) // The license key is exactly 16 bytes.
	defer cipher.Reset()
	out := make([]byte, len(p))
	cipher.XORKeyStream(out, p)
	return out
}
