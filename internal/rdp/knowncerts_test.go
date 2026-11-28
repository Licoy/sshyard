package rdp

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRDPKnownCertsTrustAndChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", KnownCertsFile)
	k, err := OpenKnownCerts(p)
	if err != nil || k.Path() != p {
		t.Fatalf("open store = %v, %v", k, err)
	}
	expires := time.Unix(3600, 0)
	leaf := &x509.Certificate{Raw: []byte("abc"), Subject: pkix.Name{CommonName: "test.example"}, NotAfter: expires}
	const expected = "BA:78:16:BF:8F:01:CF:EA:41:41:40:DE:5D:AE:22:23:B0:03:61:A3:96:17:7A:9C:B4:10:FF:61:F2:00:15:AD"
	if fp := Fingerprint(leaf); fp != expected {
		t.Fatalf("SHA-256 fingerprint = %q; want %q", fp, expected)
	}
	const addr = "test.example:3389"
	chain := []*x509.Certificate{leaf}
	var untrusted *CertError
	if err := k.Verify(addr, chain); !errors.As(err, &untrusted) || untrusted.Fingerprint != expected ||
		untrusted.Known != "" || untrusted.Subject != "test.example" || untrusted.NotAfter != expires {
		t.Fatalf("first certificate = %+v (%v)", untrusted, err)
	}
	if err := k.Trust("Test.Example.:03389", strings.ToLower(expected)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(p); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("certificate store mode: %v (%v)", info, err)
	}
	loaded, err := OpenKnownCerts(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify(addr, chain); err != nil {
		t.Fatal(err)
	}
	changed := &x509.Certificate{Raw: []byte("another leaf")}
	if err := loaded.Verify(addr, []*x509.Certificate{changed, leaf}); !errors.As(err, &untrusted) ||
		untrusted.Known != expected || untrusted.Fingerprint != Fingerprint(changed) {
		t.Fatalf("replacement certificate = %+v (%v)", untrusted, err)
	}
	if err := loaded.Verify(addr, []*x509.Certificate{nil, leaf}); !errors.As(err, &untrusted) || untrusted.Fingerprint != "" {
		t.Fatalf("nil leaf matched a non-leaf: %v", err)
	}
	if err := loaded.Verify("test.example:3390", chain); !errors.As(err, &untrusted) || untrusted.Known != "" {
		t.Fatalf("pin matched another port: %v", err)
	}
	if err := loaded.Trust(addr, Fingerprint(changed)); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify(addr, []*x509.Certificate{changed}); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Forget(addr); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify(addr, chain); !errors.As(err, &untrusted) || untrusted.Known != "" {
		t.Fatalf("forgotten pin: %v", err)
	}
}

func TestRDPKnownCertsMergeAndValidation(t *testing.T) {
	dir := t.TempDir()
	current, err := OpenKnownCerts(filepath.Join(dir, "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenKnownCerts(filepath.Join(dir, "backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	leaf := &x509.Certificate{Raw: []byte("current")}
	imported := &x509.Certificate{Raw: []byte("from backup")}
	wg.Go(func() {
		if err := current.Trust("one.test:3389", Fingerprint(leaf)); err != nil {
			t.Error(err)
		}
	})
	wg.Go(func() {
		for _, key := range []string{"one.test:3389", "[2001:0db8::f]:3389"} {
			if err := other.Trust(key, Fingerprint(imported)); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()

	data, err := other.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if added, err := current.Merge(data); added != 1 || err != nil {
		t.Fatalf("merge = %d, %v", added, err)
	}
	if added, err := current.Merge(data); added != 0 || err != nil {
		t.Fatalf("repeat merge = %d, %v", added, err)
	}
	if err := current.Verify("one.test:3389", []*x509.Certificate{leaf}); err != nil {
		t.Fatal(err)
	}
	if err := current.Verify("[2001:db8::f]:3389", []*x509.Certificate{imported}); err != nil {
		t.Fatal(err)
	}
	fingerprint := Fingerprint(leaf)
	for _, data := range []string{
		"null", "{} {}", "{",
		"{\"one.test:3389\":\"" + fingerprint + "\",\"ONE.TEST.:3389\":\"" + fingerprint + "\"}",
		"{\"one.test:3389\":\"" + strings.Repeat("a", 95) + "\"}",
	} {
		if added, err := current.Merge([]byte(data)); added != 0 || err == nil {
			t.Fatalf("malformed merge accepted: %d, %v", added, err)
		}
	}
}

func TestRDPKnownCertsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KnownCertsFile)
	k, err := OpenKnownCerts(path)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := k.Contents(); err != nil || string(b) != "{}" {
		t.Fatalf("missing pin store: %q (%v)", b, err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{Raw: []byte("test certificate")}
	if err := k.Trust("test.example:3389", Fingerprint(leaf)); err == nil {
		t.Fatal("failed rename was reported as success")
	}
	if err := k.Verify("test.example:3389", []*x509.Certificate{leaf}); err == nil {
		t.Fatal("failed disk write changed in-memory trust")
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 1 || files[0].Name() != KnownCertsFile {
		t.Fatalf("temporary file remained: %v (%v)", files, err)
	}
}
