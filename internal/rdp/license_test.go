package rdp

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"math/big"
	"net"
	"slices"
	"testing"
	"time"
)

func TestLicenseValidClient(t *testing.T) {
	// Decrypted licensing PDU from MS-RDPBCGR 4.1.11 under enhanced security.
	p := handshakeHex(t, "80000000ff031000070000000200000004000000")
	license := newLicenseExchange(Config{}, nil)
	handled, complete, err := license.receive(p)
	if err != nil || !handled || !complete {
		t.Fatalf("valid-client alert handled=%v complete=%v err=%v", handled, complete, err)
	}
	p[8], p[12] = 6, 1
	license = newLicenseExchange(Config{}, nil)
	handled, complete, err = license.receive(p)
	var rdpErr *Error
	if !handled || complete || !errors.As(err, &rdpErr) || rdpErr.Kind != KindLicense || rdpErr.Code != 6 {
		t.Fatalf("licensing failure handled=%v complete=%v err=%v", handled, complete, err)
	}
	// A 128-byte share PDU begins with the same two bytes as SEC_LICENSE_PKT.
	share := append([]byte{0x80, 0, 0x11, 0}, make([]byte, 124)...)
	if handled, _, err := license.receive(share); handled || err != nil {
		t.Fatalf("Demand Active was consumed as a license: %v", err)
	}
}

func TestRDPLicenseXrdpValidClientAlert(t *testing.T) {
	// xrdp sends STATUS_VALID_CLIENT with an empty error blob of type 0x1428.
	p := handshakeHex(t, "ff021000070000000200000028140000")[4:] // Skip the licensing preamble.
	license := newLicenseExchange(Config{}, nil)
	if err := license.alert(p); err != nil || license.state != licenseComplete {
		t.Fatalf("xrdp alert state=%v err=%v", license.state, err)
	}

	p = append(p, 0)
	binary.LittleEndian.PutUint16(p[10:], 1) // Error blob size.
	license = newLicenseExchange(Config{}, nil)
	err := license.alert(p)
	if kind, reason := Classify(err); kind != KindLicense || reason != ReasonProtocol || license.state != licenseStart {
		t.Fatalf("nonempty unknown error blob state=%v err=%v", license.state, err)
	}
}

func TestLicenseKeyDerivation(t *testing.T) {
	// MS-RDPELE 4.1 and 4.2 sample randoms/premaster, with the expected
	// outputs calculated independently from the hash formulas in 5.1.2/5.1.5.
	server := handshakeHex(t, "84efae20b1d59e36491ae82e0a9989ac49a6474f339b5ab99503a6c6c23c3f61")
	client := handshakeHex(t, "dc73a0c869256b18af0b947aa9a520af8bbc0dcca395b7b9eb815dbe0a109cd8")
	premaster := handshakeHex(t, "cf7adbcbfb0e1523871c8481ba9d4e15bbd256bdd8f7f316cc353be1934278dd929ae47ae299d473b1aa6f55943bc9bc")
	mac, key := deriveLicenseKeys(premaster, client, server)
	if !bytes.Equal(mac[:], handshakeHex(t, "74cca2098e38f01d3eddf0b495c56968")) || !bytes.Equal(key[:], handshakeHex(t, "6e1891432db6e14ce25985fba5c6f36d")) {
		t.Fatalf("invalid derived licensing keys: mac=%x enc=%x", mac, key)
	}
	if got := licenseMAC(mac, ntlmUTF16("TEST\x00")); !bytes.Equal(got, handshakeHex(t, "e452a73830b396466bda2a6dcada4558")) {
		t.Fatalf("invalid licensing MAC: %x", got)
	}
}

func TestLicenseExchange(t *testing.T) {
	key, chain := licenseTestCertificate(t)
	conn := &licenseRecordingConn{}
	cfg := Config{User: "sample", ClientName: "test-client"}
	transport := &rdpTransport{Conn: conn, userID: 1006, globalID: 1003}
	license := newLicenseExchange(cfg, transport)
	defer license.clearKeys()
	serverRandom := bytes.Repeat([]byte{0x94}, 32)
	company, product, scope := ntlmUTF16("Sample\x00"), ntlmUTF16("A02\x00"), []byte("example.test\x00")
	req := append(bytes.Clone(serverRandom), 0, 0, 6, 0)
	req = testLicenseField(testLicenseField(req, company), product)
	req = appendLicenseBlob(req, 0x0d, []byte{1, 0, 0, 0})
	req = appendLicenseBlob(req, 3, chain)
	req = appendLicenseBlob(append(req, 1, 0, 0, 0), 0x0e, scope)
	request := testLicensePacket(1, req)
	if handled, complete, err := license.receive(request); !handled || complete || err != nil {
		t.Fatalf("new license request handled=%v complete=%v err=%v", handled, complete, err)
	}
	newRequest := readLicenseClientPDU(t, conn, 0x13)
	if len(newRequest) < 40 || binary.LittleEndian.Uint32(newRequest) != 1 {
		t.Fatalf("invalid Client New License Request: %x", newRequest)
	}
	kind, cipher, names, ok := readLicenseBlob(newRequest[40:])
	if !ok || kind != 2 || len(cipher) != key.Size()+8 || !licenseZero(cipher[key.Size():]) {
		t.Fatal("invalid RSA premaster blob")
	}
	_, user, names, ok := readLicenseBlob(names)
	_, machine, rest, namesOK := readLicenseBlob(names)
	if !ok || !namesOK || len(rest) != 0 || string(user) != "sample\x00" || string(machine) != "test-client\x00" {
		t.Fatal("invalid licensing user/machine names")
	}
	enc := licenseLittleInt(cipher[:key.Size()])
	premaster := new(big.Int).Exp(enc, key.D, key.N).FillBytes(make([]byte, 48))
	slices.Reverse(premaster)
	mac, rc4Key := deriveLicenseKeys(premaster, newRequest[8:40], serverRandom)
	if mac != license.macKey || rc4Key != license.cipherKey {
		t.Fatal("server and client derived different licensing keys")
	}
	challenge := ntlmUTF16("TEST\x00")
	body := appendLicenseBlob(make([]byte, 4), 0xf750, cryptLicense(rc4Key, challenge))
	body = append(body, licenseMAC(mac, challenge)...)
	bad := testLicensePacket(2, body)
	bad[len(bad)-1] ^= 1
	if _, _, err := license.receive(bad); err == nil || conn.writes.Len() != 0 {
		t.Fatalf("tampered challenge wrote a response: %v", err)
	}
	if _, _, err := license.receive(testLicensePacket(2, body)); err != nil {
		t.Fatal(err)
	}
	verifyLicenseChallengeResponse(t, readLicenseClientPDU(t, conn, 0x15), challenge, mac, rc4Key)

	licenseValue := []byte{0x30, 3, 0x02, 1, 1}
	issued := []byte{0, 0, 6, 0}
	for _, field := range [][]byte{scope, company, product, licenseValue} {
		issued = testLicenseField(issued, field)
	}
	body = appendLicenseBlob(nil, 9, cryptLicense(rc4Key, issued))
	body = append(body, licenseMAC(mac, issued)...)
	if handled, complete, err := license.receive(testLicensePacket(3, body)); !handled || !complete || err != nil || !bytes.Equal(license.issued.value, licenseValue) {
		t.Fatalf("new license handled=%v complete=%v err=%v", handled, complete, err)
	}
	if license.macKey != [16]byte{} || license.cipherKey != [16]byte{} {
		t.Fatal("temporary licensing key state was retained")
	}
	resumed := newLicenseExchange(cfg, transport)
	defer resumed.clearKeys()
	resumed.cached = license.issued
	if handled, complete, err := resumed.receive(request); !handled || complete || err != nil {
		t.Fatalf("cached license handled=%v complete=%v err=%v", handled, complete, err)
	}
	cached := readLicenseClientPDU(t, conn, 0x12)
	_, _, next, ok := readLicenseBlob(cached[40:])
	kind, value, _, calOK := readLicenseBlob(next)
	if !ok || !calOK || kind != 1 || !bytes.Equal(value, licenseValue) {
		t.Fatal("cached license did not reuse issued CAL")
	}
}

func verifyLicenseChallengeResponse(t *testing.T, p, challenge []byte, mac, key [16]byte) {
	t.Helper()
	_, encryptedChallenge, p, ok := readLicenseBlob(p)
	_, encryptedID, signature, idOK := readLicenseBlob(p)
	if !ok || !idOK {
		t.Fatal("invalid client licensing challenge response")
	}
	plain := cryptLicense(key, encryptedChallenge)
	hwid := cryptLicense(key, encryptedID)
	if len(plain) != len(challenge)+8 || !bytes.Equal(plain[8:], challenge) || len(hwid) != 20 ||
		!bytes.Equal(signature, licenseMAC(mac, append(plain, hwid...))) {
		t.Fatal("client licensing challenge or HWID did not verify")
	}
}

func licenseTestCertificate(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	rootDER, err := x509.CreateCertificate(rand.Reader, issuer, issuer, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: issuer.NotBefore, NotAfter: issuer.NotAfter}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, issuer, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	chain := testLicenseField(testLicenseField([]byte{2, 0, 0, 0, 2, 0, 0, 0}, rootDER), leafDER)
	return key, append(chain, make([]byte, 16)...)
}

type licenseRecordingConn struct {
	net.Conn
	writes bytes.Buffer
}

func (c *licenseRecordingConn) Write(p []byte) (int, error) { return c.writes.Write(p) }

func testLicensePacket(kind byte, p []byte) []byte {
	return append(binary.LittleEndian.AppendUint16([]byte{0x80, 0, 0, 0, kind, 3}, uint16(len(p)+4)), p...)
}

func testLicenseField(p, field []byte) []byte {
	return append(binary.LittleEndian.AppendUint32(p, uint32(len(field))), field...)
}

func readLicenseClientPDU(t *testing.T, conn *licenseRecordingConn, kind byte) []byte {
	t.Helper()
	p, _, err := readRDPFrame(&conn.writes, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err = unwrapMCS(p)
	if err != nil || len(p) < 7 || p[0] != 0x64 || binary.BigEndian.Uint16(p[3:]) != 1003 {
		t.Fatalf("invalid MCS client licensing frame: %v", err)
	}
	p, ok := takeMCSData(p[6:])
	if !ok || len(p) < 8 || p[0] != 0x80 || p[4] != kind || !validLicensePreamble(p[4:]) {
		t.Fatalf("incorrect client license PDU: %x", p)
	}
	return p[8:]
}
