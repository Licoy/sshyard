package rdp

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// KnownCertsFile is the RDP certificate pin store in the application data folder.
	KnownCertsFile    = "rdp_known_certs.json"
	maxKnownCertsFile = 1024 * 1024
)

// CertError identifies an unknown or changed RDP leaf certificate.
type CertError struct {
	Addr        string
	Fingerprint string
	Known       string
	Subject     string
	NotAfter    time.Time
}

func (e *CertError) Error() string {
	switch {
	case e.Fingerprint == "":
		return fmt.Sprintf("RDP server %q did not present a leaf certificate", e.Addr)
	case e.Known == "":
		return fmt.Sprintf("RDP certificate for %q is unknown (%s)", e.Addr, e.Fingerprint)
	default:
		return fmt.Sprintf("RDP certificate for %q changed (%s, previously %s)", e.Addr, e.Fingerprint, e.Known)
	}
}

// Fingerprint returns the colon-separated SHA-256 digest of the leaf's DER.
func Fingerprint(cert *x509.Certificate) string {
	if cert == nil || len(cert.Raw) == 0 {
		return ""
	}
	return formatFingerprint(sha256.Sum256(cert.Raw))
}

func formatFingerprint(sum [sha256.Size]byte) string {
	return strings.ReplaceAll(fmt.Sprintf("% X", sum[:]), " ", ":")
}

// KnownCerts keeps explicit trust-on-first-use pins by host and port.
type KnownCerts struct {
	path string
	mu   sync.RWMutex
	pins map[string]string
}

// OpenKnownCerts loads path. A missing file has an empty trust store.
func OpenKnownCerts(path string) (*KnownCerts, error) {
	if path == "" {
		return nil, errors.New("missing RDP certificate store path")
	}
	k := &KnownCerts{path: path, pins: make(map[string]string)}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxKnownCertsFile+1))
	if err != nil {
		return nil, err
	}
	k.pins, err = parseKnownCerts(data)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// Path returns the location of the file.
func (k *KnownCerts) Path() string { return k.path }

// Verify accepts only the currently pinned leaf certificate for addr.
func (k *KnownCerts) Verify(addr string, chain []*x509.Certificate) error {
	key, err := knownCertAddress(addr)
	if err != nil {
		return err
	}
	k.mu.RLock()
	pin := k.pins[key]
	k.mu.RUnlock()

	e := &CertError{Addr: addr, Known: pin}
	if len(chain) > 0 && chain[0] != nil {
		leaf := chain[0]
		e.Fingerprint, e.Subject, e.NotAfter = Fingerprint(leaf), leaf.Subject.CommonName, leaf.NotAfter
	}
	if e.Fingerprint != "" && e.Fingerprint == pin {
		return nil
	}
	return e
}

// Trust adds or replaces a leaf pin after explicit user approval.
func (k *KnownCerts) Trust(addr, fingerprint string) error {
	key, err := knownCertAddress(addr)
	if err != nil {
		return err
	}
	pin, err := canonicalFingerprint(fingerprint)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pins[key] == pin {
		return nil
	}
	pins := maps.Clone(k.pins)
	pins[key] = pin
	return k.save(pins)
}

// Forget removes the pin for addr.
func (k *KnownCerts) Forget(addr string) error {
	key, err := knownCertAddress(addr)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.pins[key]; !exists {
		return nil
	}
	pins := maps.Clone(k.pins)
	delete(pins, key)
	return k.save(pins)
}

// Contents returns a snapshot suitable for backup.
func (k *KnownCerts) Contents() ([]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return json.MarshalIndent(k.pins, "", "  ")
}

// Merge adds imported pins whose addresses are absent from this store.
func (k *KnownCerts) Merge(data []byte) (int, error) {
	imported, err := parseKnownCerts(data)
	if err != nil {
		return 0, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	pins := maps.Clone(k.pins)
	for addr, pin := range imported {
		if _, exists := pins[addr]; !exists {
			pins[addr] = pin
		}
	}
	added := len(pins) - len(k.pins)
	if added == 0 {
		return 0, nil
	}
	if err := k.save(pins); err != nil {
		return 0, err
	}
	return added, nil
}

func (k *KnownCerts) save(pins map[string]string) error {
	data, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxKnownCertsFile {
		return errors.New("RDP certificate store is too large")
	}
	if err := writeKnownCerts(k.path, data); err != nil {
		return err
	}
	k.pins = pins
	return nil
}

func writeKnownCerts(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".tmp-rdp-certs-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func knownCertAddress(addr string) (string, error) {
	host, port, splitErr := net.SplitHostPort(addr)
	n, portErr := strconv.ParseUint(port, 10, 16)
	if splitErr != nil || portErr != nil || n == 0 || host == "" || len(host) > 255 ||
		strings.ContainsAny(host, "[]/@\\") || strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return "", errors.New("invalid RDP certificate address")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else {
		if strings.ContainsAny(host, ":%") {
			return "", errors.New("invalid RDP certificate address")
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if host == "" {
			return "", errors.New("invalid RDP certificate address")
		}
	}
	return net.JoinHostPort(host, strconv.FormatUint(n, 10)), nil
}

func canonicalFingerprint(pin string) (string, error) {
	var sum [sha256.Size]byte
	raw := pin
	if len(pin) == 3*sha256.Size-1 {
		raw = strings.ReplaceAll(pin, ":", "")
	}
	if len(raw) == hex.EncodedLen(len(sum)) {
		if _, err := hex.Decode(sum[:], []byte(raw)); err == nil {
			canonical := formatFingerprint(sum)
			if raw == pin || strings.EqualFold(pin, canonical) {
				return canonical, nil
			}
		}
	}
	return "", errors.New("invalid RDP SHA-256 certificate fingerprint")
}

func parseKnownCerts(data []byte) (map[string]string, error) {
	if len(data) > maxKnownCertsFile {
		return nil, errors.New("RDP certificate store is too large")
	}
	reader := json.NewDecoder(bytes.NewReader(data))
	if token, err := reader.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid RDP certificate store object")
	}
	pins := make(map[string]string)
	for reader.More() {
		token, err := reader.Token()
		addr, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid RDP certificate store key")
		}
		addr, err = knownCertAddress(addr)
		if err != nil {
			return nil, err
		}
		if _, exists := pins[addr]; exists {
			return nil, errors.New("duplicate RDP certificate store address")
		}
		var pin string
		if err := reader.Decode(&pin); err != nil {
			return nil, errors.New("invalid RDP certificate store value")
		}
		pin, err = canonicalFingerprint(pin)
		if err != nil {
			return nil, err
		}
		pins[addr] = pin
	}
	if _, err := reader.Token(); err != nil {
		return nil, errors.New("invalid RDP certificate store terminator")
	}
	if _, err := reader.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing RDP certificate store data")
	}
	return pins, nil
}
