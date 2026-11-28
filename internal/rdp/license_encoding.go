package rdp

import (
	"bytes"
	"encoding/binary"
	"slices"
)

type licenseProduct struct {
	version uint32
	company []byte
	product []byte
}

type licenseRequest struct {
	licenseProduct
	cert         []byte
	serverRandom []byte
	scopes       [][]byte
}

type rdpClientLicense struct {
	licenseProduct
	scope []byte
	value []byte
}

func (l *rdpClientLicense) matches(r licenseRequest) bool {
	return l.version >= r.version && bytes.Equal(l.company, r.company) && bytes.Equal(l.product, r.product) &&
		slices.ContainsFunc(r.scopes, func(scope []byte) bool { return bytes.Equal(scope, l.scope) })
}

func parseLicenseRequest(p []byte) (licenseRequest, error) {
	var r licenseRequest
	if len(p) < 36 {
		return r, malformedLicense("truncated RDP server license request")
	}
	r.serverRandom = p[:32]
	r.version = binary.LittleEndian.Uint32(p[32:])
	company, p, ok := readLicenseField(p[36:])
	if !ok || !licenseUnicode(company) {
		return r, malformedLicense("invalid RDP licensing company name")
	}
	r.company = company
	product, p, ok := readLicenseField(p)
	if !ok || !licenseUnicode(product) {
		return r, malformedLicense("invalid RDP licensing product ID")
	}
	r.product = product
	kind, key, p, ok := readLicenseBlob(p)
	if !ok || kind != 0x0d || len(key) != 4 || binary.LittleEndian.Uint32(key) != 1 {
		return r, malformedLicense("unsupported RDP license key exchange")
	}
	kind, cert, p, ok := readLicenseBlob(p)
	if !ok || kind != 3 {
		return r, malformedLicense("invalid RDP licensing certificate blob")
	}
	r.cert = cert
	scopes, err := readLicenseScopes(p)
	r.scopes = scopes
	return r, err
}

func readLicenseScopes(p []byte) ([][]byte, error) {
	if len(p) < 4 {
		return nil, malformedLicense("truncated RDP licensing scope list")
	}
	n := binary.LittleEndian.Uint32(p)
	p = p[4:]
	if uint64(n) > uint64(len(p)/5) {
		return nil, malformedLicense("invalid RDP licensing scope count")
	}
	scopes := make([][]byte, 0, n)
	for range n {
		kind, scope, rest, ok := readLicenseBlob(p)
		if !ok || kind != 0x0e || len(scope) == 0 || scope[len(scope)-1] != 0 {
			return nil, malformedLicense("invalid RDP licensing scope")
		}
		scopes, p = append(scopes, scope), rest
	}
	if len(p) != 0 {
		return nil, malformedLicense("unexpected trailing RDP licensing scope data")
	}
	return scopes, nil
}

func parseIssuedLicense(p []byte) (*rdpClientLicense, error) {
	if len(p) < 4 {
		return nil, malformedLicense("truncated RDP issued license")
	}
	l := &rdpClientLicense{licenseProduct: licenseProduct{version: binary.LittleEndian.Uint32(p)}}
	scope, p, ok := readLicenseField(p[4:])
	if !ok || len(scope) == 0 || scope[len(scope)-1] != 0 {
		return nil, malformedLicense("invalid RDP issued license scope")
	}
	company, p, ok := readLicenseField(p)
	if !ok || !licenseUnicode(company) {
		return nil, malformedLicense("invalid RDP issued license company name")
	}
	product, p, ok := readLicenseField(p)
	if !ok || !licenseUnicode(product) {
		return nil, malformedLicense("invalid RDP issued license product ID")
	}
	value, rest, ok := readLicenseField(p)
	if !ok || len(value) == 0 || len(rest) != 0 {
		return nil, malformedLicense("invalid RDP issued license content")
	}
	l.scope, l.company, l.product, l.value = scope, company, product, value
	return l, nil
}

func readLicenseField(p []byte) (value, rest []byte, ok bool) {
	if len(p) < 4 {
		return nil, nil, false
	}
	n := uint64(binary.LittleEndian.Uint32(p))
	p = p[4:]
	if n > uint64(len(p)) {
		return nil, nil, false
	}
	return p[:n], p[n:], true
}

func readLicenseBlob(p []byte) (kind uint16, value, rest []byte, ok bool) {
	if len(p) < 4 {
		return 0, nil, nil, false
	}
	kind, n := binary.LittleEndian.Uint16(p), int(binary.LittleEndian.Uint16(p[2:]))
	p = p[4:]
	if n > len(p) {
		return 0, nil, nil, false
	}
	return kind, p[:n], p[n:], true
}

func appendLicenseBlob(p []byte, kind uint16, payload []byte) []byte {
	p = binary.LittleEndian.AppendUint16(p, kind)
	p = binary.LittleEndian.AppendUint16(p, uint16(len(payload)))
	return append(p, payload...)
}

func licenseUnicode(p []byte) bool {
	return len(p) >= 2 && len(p)%2 == 0 && binary.LittleEndian.Uint16(p[len(p)-2:]) == 0
}
