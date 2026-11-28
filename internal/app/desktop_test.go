package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/rdp"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
)

// testFingerprint is a well-formed SHA-256 certificate fingerprint.
var testFingerprint = strings.TrimSuffix(strings.Repeat("AB:", 32), ":")

// TestDesktopPaneKinds: an RDP host opens a remote desktop, its
// "terminal", and nothing else; no other host opens one.
func TestDesktopPaneKinds(t *testing.T) {
	a, _ := newTestApp(t)
	h, err := a.store.Add(assets.Host{Name: "win", Protocol: assets.ProtoRDP, Address: "192.0.2.10", User: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	ssh, err := a.store.Add(assets.Host{Name: "web", Address: "web", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.makeConn(h.ID, layout.Terminal, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	if c.kind != layout.Desktop || c.rd == nil || c.s != nil {
		t.Fatalf("an RDP host's terminal is kind %v (desktop %v, session %v)", c.kind, c.rd != nil, c.s != nil)
	}
	if st := c.rd.s.Status().State; st != rdp.Idle {
		t.Fatalf("a desktop not started is %v", st)
	}
	for _, k := range []layout.Kind{layout.Files, layout.Editor, layout.AI, layout.Monitor} {
		if _, err := a.makeConn(h.ID, k, 0); !errors.Is(err, errDesktopOnly) {
			t.Fatalf("%v on an RDP host: %v, want errDesktopOnly", k, err)
		}
	}
	if _, err := a.makeConn(ssh.ID, layout.Desktop, 0); !errors.Is(err, errDesktopOnly) {
		t.Fatalf("a desktop on an SSH host: %v, want errDesktopOnly", err)
	}
}

// TestDesktopConfig: a desktop asks for the pane's size unless its host
// fixes one, signs in as the host's user and domain, and offers the
// clipboard unless the host turns it off.
func TestDesktopConfig(t *testing.T) {
	a, _ := newTestApp(t)
	h := assets.Host{ID: "h", Protocol: assets.ProtoRDP, Address: "192.0.2.10", Port: 3390, User: "admin", Auth: assets.AuthNone}
	c := &conn{id: 1}
	cfg := a.desktopConfig(c, h, 1280, 800, 200)
	if cfg.Addr != "192.0.2.10:3390" || cfg.User != "admin" || cfg.Width != 1280 || cfg.Height != 800 || cfg.Scale != 200 || !cfg.Clipboard || cfg.Admin {
		t.Fatalf("config %+v", cfg)
	}
	if cfg.VerifyCert == nil {
		t.Fatal("certificates are not verified")
	}
	if pw, err := cfg.Password(t.Context()); pw != "" || err != nil {
		t.Fatalf("a manual login gave %q, %v", pw, err)
	}
	h.RDP = &assets.RDPOptions{Domain: "CORP", Security: "tls", Width: 1024, Height: 768, Admin: true, NoClipboard: true}
	cfg = a.desktopConfig(c, h, 1280, 800, 100)
	if cfg.Domain != "CORP" || cfg.Security != rdp.SecurityTLS || cfg.Width != 1024 || cfg.Height != 768 || cfg.Clipboard || !cfg.Admin {
		t.Fatalf("config with options %+v", cfg)
	}
}

// TestRDPFormRoundTrip: the editor keeps an RDP host's options, and
// refuses a fixed size that is not a number.
func TestRDPFormRoundTrip(t *testing.T) {
	in := assets.Host{RDP: &assets.RDPOptions{Domain: "CORP", Security: "nla", Width: 1600, Height: 900, Admin: true, NoClipboard: true}}
	var f hostForm
	f.loadRDP(&in)
	out := assets.Host{Protocol: assets.ProtoRDP}
	if !f.applyRDP(&out) {
		t.Fatal("valid options refused")
	}
	if out.RDP == nil || *out.RDP != *in.RDP {
		t.Fatalf("options %+v, want %+v", out.RDP, in.RDP)
	}
	var fit hostForm
	fit.loadRDP(&assets.Host{})
	out = assets.Host{Protocol: assets.ProtoRDP}
	if !fit.applyRDP(&out) || rdpOptions(out) != (assets.RDPOptions{}) {
		t.Fatalf("default options %+v", out.RDP)
	}
	f.rdpWidth = "wide"
	if f.applyRDP(&out) {
		t.Fatal("a width that is not a number taken")
	}
	ssh := assets.Host{RDP: in.RDP}
	if !f.applyRDP(&ssh) || ssh.RDP != nil {
		t.Fatal("an SSH host kept RDP options")
	}
}

// TestDesktopFailedKinds: what the user can answer of a desktop's
// failures is the password it lacks or that was refused, and its jump
// hosts' failures.
func TestDesktopFailedKinds(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want session.Kind
	}{
		{nil, session.KindOther},
		{session.ErrNoCredentials, session.KindNoCredentials},
		{fmt.Errorf("sign in: %w", session.ErrNoCredentials), session.KindNoCredentials},
		{&rdp.Error{Kind: rdp.KindAuth, Reason: rdp.ReasonLogonFailure}, session.KindAuth},
		{&rdp.Error{Kind: rdp.KindAuth, Reason: rdp.ReasonAccountLocked}, session.KindOther},
		{&rdp.CertError{Addr: "192.0.2.10:3389", Fingerprint: testFingerprint}, session.KindOther},
		{&session.HopError{Jump: true, Err: session.ErrNoCredentials}, session.KindNoCredentials},
	} {
		if got := desktopFailed(tc.err); got != tc.want {
			t.Errorf("desktopFailed(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// TestDesktopState: a desktop's state reads as a session's, for tabs and
// the sidebar.
func TestDesktopState(t *testing.T) {
	for in, want := range map[rdp.State]session.State{
		rdp.Idle: session.Idle, rdp.Connecting: session.Connecting, rdp.Connected: session.Connected,
		rdp.Reconnecting: session.Reconnecting, rdp.Disconnected: session.Disconnected, rdp.Failed: session.Failed,
	} {
		if got := desktopState(in); got != want {
			t.Errorf("desktopState(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestTrustDesktopCert: trusting the certificate asked about pins it for
// the server's address and closes the ask; forgetting the host's key
// drops it.
func TestTrustDesktopCert(t *testing.T) {
	a, _ := newTestApp(t)
	h, err := a.store.Add(assets.Host{Name: "win", Protocol: assets.ProtoRDP, Address: "192.0.2.10", User: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.makeConn(h.ID, layout.Terminal, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	addr := "192.0.2.10:3389"
	a.certAsk = &desktopCertAsk{conn: c.id, err: &rdp.CertError{Addr: addr, Fingerprint: testFingerprint}}
	a.askingCert = true
	if err := a.TrustDesktopCert(); err != nil {
		t.Fatal(err)
	}
	if a.certAsk != nil || a.askingCert {
		t.Fatal("the ask stayed open")
	}
	if pin, ok := a.rdpCerts.Known(addr); !ok || pin != testFingerprint {
		t.Fatalf("pinned %q, %v", pin, ok)
	}
	if !a.knowsHostKey(h) {
		t.Fatal("the host's certificate is not known")
	}
	if b, err := os.ReadFile(filepath.Join(a.p.DataDir, rdp.KnownCertsFile)); err != nil || !strings.Contains(string(b), testFingerprint) {
		t.Fatalf("certificates file %q, %v", b, err)
	}
}

// TestBackupRDPCerts: a backup keeps the trusted desktop certificates and
// a restore merges them in.
func TestBackupRDPCerts(t *testing.T) {
	a, _ := newTestApp(t)
	addr := "192.0.2.10:3389"
	if err := a.rdpCerts.Trust(addr, testFingerprint); err != nil {
		t.Fatal(err)
	}
	p, err := a.collectBackup(false)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := a.packBackup(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unpackBackup(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.RDPCerts, testFingerprint) {
		t.Fatalf("backup certificates %q", got.RDPCerts)
	}
	b, _ := newTestApp(t)
	if err := b.restoreBackup(locales.In("en"), got); err != nil {
		t.Fatal(err)
	}
	if pin, ok := b.rdpCerts.Known(addr); !ok || pin != testFingerprint {
		t.Fatalf("restored pin %q, %v", pin, ok)
	}
}

// TestResetDeletesRDPCerts: a reset of the app lock deletes the trusted
// desktop certificates with the rest of the data.
func TestResetDeletesRDPCerts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, rdp.KnownCertsFile)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, resetFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if done, err := finishReset(dir); !done || err != nil {
		t.Fatalf("finishReset = %v, %v", done, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s kept after the reset: %v", rdp.KnownCertsFile, err)
	}
}
