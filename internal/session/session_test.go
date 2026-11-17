package session

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func server(t *testing.T) *sshtest.Server {
	t.Helper()
	s, err := sshtest.Start("127.0.0.1:0", "demo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func trusted(t *testing.T, s *sshtest.Server) *KnownHosts {
	t.Helper()
	kh, err := OpenKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := kh.Add(s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	return kh
}

func password(pw string) func(context.Context) (Credentials, error) {
	return func(context.Context) (Credentials, error) { return Credentials{Password: pw}, nil }
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// logins is how many logins srv counted once it counted want (or gave
// up waiting): the server counts one only after the client is in.
func logins(t *testing.T, srv *sshtest.Server, want int) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for srv.Logins() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // and no more come
	return srv.Logins()
}

func waitState(t *testing.T, s *Session, want State) Status {
	t.Helper()
	waitFor(t, "state "+want.String(), func() bool { return s.Status().State == want })
	return s.Status()
}

func waitText(t *testing.T, s *Session, text string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%q on screen", text), func() bool { return strings.Contains(s.Term.ScreenText(), text) })
}

func TestTrustOnFirstUseThenShell(t *testing.T) {
	srv := server(t)
	kh, err := OpenKnownHosts(filepath.Join(t.TempDir(), "sub", "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: kh.Callback()}, Options{}, 80, 24)
	defer s.Close()
	s.Start()
	st := waitState(t, s, Failed)
	var unknown *UnknownHostError
	if !errors.As(st.Err, &unknown) || Classify(st.Err) != KindHostUnknown {
		t.Fatalf("first connect: %v", st.Err)
	}
	if unknown.Fingerprint != ssh.FingerprintSHA256(srv.HostKey.PublicKey()) {
		t.Error("fingerprint")
	}
	if srv.Logins() != 0 {
		t.Error("logged in to an untrusted host")
	}
	if err := kh.Add(unknown.Host, unknown.Key); err != nil {
		t.Fatal(err)
	}
	s.Reconnect()
	waitState(t, s, Connected)
	waitText(t, s, "Welcome")
	if p := srv.LastPTY(); p.Term != "xterm-256color" || p.Cols != 80 || p.Rows != 24 {
		t.Errorf("pty %+v", p)
	}
	s.Write([]byte("hello\r"))
	waitText(t, s, "you said: hello")
	s.Write([]byte("color\r"))
	waitText(t, s, "red brand")

	s.Resize(100, 30)
	select {
	case p := <-srv.Resizes():
		if p.Cols != 100 || p.Rows != 30 {
			t.Errorf("window change %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no window change")
	}
	if c, r := s.Term.Size(); c != 100 || r != 30 {
		t.Error("terminal not resized")
	}

	s.Write([]byte("exit\r"))
	waitState(t, s, Exited)
	if s.Client() != nil {
		t.Error("client after exit")
	}
}

func TestWrongPasswordIsNotRetried(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("nope"), HostKey: trusted(t, srv).Callback()}, Options{AutoReconnect: true}, 80, 24)
	defer s.Close()
	s.Start()
	st := waitState(t, s, Failed)
	if Classify(st.Err) != KindAuth {
		t.Fatalf("%v → %v", st.Err, Classify(st.Err))
	}
	time.Sleep(50 * time.Millisecond)
	if s.Status().State != Failed {
		t.Error("auth failure retried")
	}
}

func TestHostKeyChanged(t *testing.T) {
	srv := server(t)
	kh, _ := OpenKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	kh.Add(srv.Addr, sshtest.NewHostKey().PublicKey()) // some other key
	_, err := Dial(context.Background(), Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: kh.Callback()})
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) || Classify(err) != KindHostChanged || changed.Line != 1 || len(changed.Old) != 1 {
		t.Fatalf("%v", err)
	}
	if Classify(err).Retryable() {
		t.Error("a changed host key is retryable")
	}
}

func keyPEM(t *testing.T, passphrase string) ([]byte, ssh.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(pub)
	return pem.EncodeToMemory(block), sp
}

func TestKeyAuth(t *testing.T) {
	srv := server(t)
	kh := trusted(t, srv)
	key, pub := keyPEM(t, "")
	srv.AuthorizedKey = pub
	c, err := Dial(context.Background(), Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(),
		Credentials: func(context.Context) (Credentials, error) { return Credentials{PrivateKey: key}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	enc, pub2 := keyPEM(t, "pass phrase")
	srv.AuthorizedKey = pub2
	cfg := Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(),
		Credentials: func(context.Context) (Credentials, error) { return Credentials{PrivateKey: enc}, nil }}
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrPassphraseNeeded) || Classify(err) != KindPassphrase {
		t.Fatalf("no passphrase: %v", err)
	}
	cfg.Credentials = func(context.Context) (Credentials, error) {
		return Credentials{PrivateKey: enc, Passphrase: []byte("pass phrase")}, nil
	}
	c, err = Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	cfg.Credentials = func(context.Context) (Credentials, error) { return Credentials{}, nil }
	if _, err := Dial(context.Background(), cfg); Classify(err) != KindNoCredentials {
		t.Errorf("no credentials: %v", err)
	}
}

func TestAutoReconnectKeepsScrollback(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{AutoReconnect: true, Backoff: 20 * time.Millisecond}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	s.Write([]byte("before\r"))
	waitText(t, s, "you said: before")

	srv.Drop()
	waitFor(t, "a second connection", func() bool { st := s.Status(); return st.State == Connected && st.Connects == 2 })
	if !strings.Contains(s.Term.ScreenText(), "you said: before") {
		t.Error("output before the drop is gone")
	}
	waitText(t, s, "Welcome")
	s.Write([]byte("after\r"))
	waitText(t, s, "you said: after")
	if logins(t, srv, 2) != 2 {
		t.Errorf("%d logins", srv.Logins())
	}
}

func TestReconnectBackoffAndOneClick(t *testing.T) {
	srv := server(t)
	addr := srv.Addr
	kh := trusted(t, srv)
	s := New(Config{Addr: addr, User: "demo", Credentials: password("secret"), HostKey: kh.Callback()},
		Options{AutoReconnect: true, Backoff: time.Hour, MaxBackoff: time.Hour, MaxAttempts: 2}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	// The server goes away entirely; the session waits an hour to retry.
	srv.Close()
	st := waitState(t, s, Reconnecting)
	waitFor(t, "first retry scheduled", func() bool { return s.Status().Attempt == 1 && !s.Status().Retry.IsZero() })
	if time.Until(s.Status().Retry) < 30*time.Minute {
		t.Errorf("retry at %v", st.Retry)
	}
	// It comes back on the same port, and the user clicks Reconnect.
	srv2 := restart(t, addr, srv)
	s.Reconnect()
	waitState(t, s, Connected)
	if srv2.Logins() != 1 {
		t.Error("not reconnected to the new server")
	}
}

// restart starts a server on addr with the old one's host key.
func restart(t *testing.T, addr string, old *sshtest.Server) *sshtest.Server {
	t.Helper()
	var s *sshtest.Server
	var err error
	for range 50 {
		if s, err = sshtest.StartWithKey(addr, "demo", "secret", old.HostKey); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Skip("cannot listen on the old port again:", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{AutoReconnect: true, Backoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, MaxAttempts: 3}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	srv.Close()
	st := waitState(t, s, Disconnected)
	if Classify(st.Err) != KindNetwork {
		t.Errorf("%v → %v", st.Err, Classify(st.Err))
	}
}

func TestProbeMeasuresLatencyWhenAsked(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{KeepAlive: time.Hour}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	time.Sleep(50 * time.Millisecond)
	if _, ok := s.Latency(); ok {
		t.Fatal("probed before a metrics bar asked")
	}
	if s.probeEvery() != time.Hour {
		t.Fatalf("every %s", s.probeEvery())
	}
	s.SetProbe(5 * time.Second)
	if s.probeEvery() != 5*time.Second {
		t.Fatalf("every %s", s.probeEvery())
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := s.Latency(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no latency within 1s of showing the bar")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.SetProbe(0)
	if s.probeEvery() != time.Hour {
		t.Fatalf("hidden every %s", s.probeEvery())
	}
	if _, ok := s.Latency(); !ok {
		t.Fatal("hiding the bar dropped the measurement")
	}
}

func TestKeepAliveTimeout(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{KeepAlive: 20 * time.Millisecond, KeepAliveMax: 2}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	time.Sleep(80 * time.Millisecond)
	if s.Status().State != Connected {
		t.Fatal("answered keepalives dropped the connection")
	}
	srv.Mute.Store(true)
	st := waitState(t, s, Disconnected)
	if !errors.Is(st.Err, ErrKeepAliveTimeout) {
		t.Errorf("%v", st.Err)
	}
	// One click reconnects.
	srv.Mute.Store(false)
	s.Reconnect()
	waitState(t, s, Connected)
}

func TestCloseStopsEverything(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()}, Options{AutoReconnect: true}, 80, 24)
	var changes atomic.Int32
	s.OnChange = func() { changes.Add(1) }
	s.Start()
	waitState(t, s, Connected)
	s.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop still running")
	}
	if s.Status().State != Closed || changes.Load() == 0 {
		t.Error("status")
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Error("write after close")
	}
}

func TestDialTimeoutAndClassify(t *testing.T) {
	// A listener that never speaks SSH.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	kh, _ := OpenKnownHosts(filepath.Join(t.TempDir(), "kh"))
	_, err := Dial(context.Background(), Config{Addr: ln.Addr().String(), User: "u", Credentials: password("x"), HostKey: kh.Callback(), Timeout: 100 * time.Millisecond})
	if Classify(err) != KindNoBanner || !errors.Is(err, context.DeadlineExceeded) || !Classify(err).Retryable() {
		t.Errorf("silent server: %v → %v", err, Classify(err))
	}
	// A server that greets and then stalls times out, but is heard.
	l3, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l3.Close()
	go func() {
		for {
			c, err := l3.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			c.Write([]byte("SSH-2.0-Stall\r\n"))
		}
	}()
	_, err = Dial(context.Background(), Config{Addr: l3.Addr().String(), User: "u", Credentials: password("x"), HostKey: kh.Callback(), Timeout: 100 * time.Millisecond})
	if Classify(err) != KindTimeout {
		t.Errorf("stalled server: %v → %v", err, Classify(err))
	}
	// A port nobody listens on.
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l2.Addr().String()
	l2.Close()
	_, err = Dial(context.Background(), Config{Addr: addr, User: "u", Credentials: password("x"), HostKey: kh.Callback()})
	if Classify(err) != KindNetwork {
		t.Errorf("refused: %v → %v", err, Classify(err))
	}
	if Classify(nil) != KindNone || Classify(errors.New("x")) != KindOther {
		t.Error("classify")
	}
	_ = os.Remove
}

func TestNoShellKeepsAConnection(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{NoShell: true, AutoReconnect: true, Backoff: 20 * time.Millisecond}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	if s.Client() == nil {
		t.Fatal("no client")
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Error("wrote to a session without a shell")
	}
	srv.Drop()
	waitFor(t, "a second connection", func() bool { st := s.Status(); return st.State == Connected && st.Connects == 2 })
	s.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
	if s.Status().State != Closed {
		t.Errorf("state %v", s.Status().State)
	}
}

// userCert signs pub for principal with a new CA; it returns the
// certificate line and the CA's public key.
func userCert(t *testing.T, pub ssh.PublicKey, principal string) ([]byte, ssh.PublicKey) {
	t.Helper()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: pub, CertType: ssh.UserCert, KeyId: "test", ValidPrincipals: []string{principal},
		ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Hour).Unix())}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(cert), ca.PublicKey()
}

func TestCertAuth(t *testing.T) {
	srv := server(t)
	kh := trusted(t, srv)
	key, pub := keyPEM(t, "")
	cert, ca := userCert(t, pub, "demo")
	srv.TrustedCA = ca
	cr := Credentials{PrivateKey: key, Certificate: cert}
	cfg := Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(),
		Credentials: func(context.Context) (Credentials, error) { return cr, nil }}
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	// The bare key is not authorized: only its certificate is.
	cr = Credentials{PrivateKey: key}
	if _, err := Dial(context.Background(), cfg); Classify(err) != KindAuth {
		t.Errorf("bare key: %v", err)
	}
	other, _ := keyPEM(t, "")
	cr = Credentials{PrivateKey: other, Certificate: cert}
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrCertificate) || Classify(err) != KindCertificate || Classify(err).Retryable() {
		t.Errorf("another key's certificate: %v", err)
	}
	cr = Credentials{PrivateKey: key, Certificate: []byte("ssh-ed25519 AAAA")}
	if _, err := Dial(context.Background(), cfg); Classify(err) != KindCertificate {
		t.Errorf("no certificate: %v", err)
	}
}

func TestShellEntersDirAfterPrompt(t *testing.T) {
	srv := server(t)
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{Dir: "/srv/it's here", AutoReconnect: true, Backoff: 20 * time.Millisecond}, 80, 24)
	defer s.Close()
	s.Start()
	waitText(t, s, `you said: cd '/srv/it'\''s here'`)
	if text := s.Term.ScreenText(); strings.Index(text, "Welcome") > strings.Index(text, "cd '") {
		t.Errorf("cd typed before the prompt:\n%s", text)
	}

	srv.Drop()
	waitFor(t, "a second connection", func() bool { st := s.Status(); return st.State == Connected && st.Connects == 2 })
	waitFor(t, "the cd again", func() bool { return strings.Count(s.Term.ScreenText(), "you said: cd") == 2 })
}

// TestShellStartCommand: each time the shell starts, the command Start
// gives is typed after the cd, Start asked again on a reconnect.
func TestShellStartCommand(t *testing.T) {
	srv := server(t)
	var asked atomic.Int32
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()},
		Options{Dir: "/srv", AutoReconnect: true, Backoff: 20 * time.Millisecond, Start: func(ctx context.Context, client *ssh.Client) string {
			if client == nil {
				return "no client"
			}
			if asked.Add(1) == 1 {
				return "first"
			}
			return ""
		}}, 80, 24)
	defer s.Close()
	s.Start()
	waitText(t, s, "you said: first")
	if text := s.Term.ScreenText(); strings.Index(text, "cd '/srv'") > strings.Index(text, "you said: first") {
		t.Errorf("the command typed before the cd:\n%s", text)
	}
	srv.Drop()
	waitFor(t, "the cd again", func() bool { return strings.Count(s.Term.ScreenText(), "you said: cd") == 2 })
	if n := asked.Load(); n != 2 || strings.Count(s.Term.ScreenText(), "you said: first") != 1 {
		t.Errorf("asked %d times:\n%s", n, s.Term.ScreenText())
	}
}

// TestShellStartsWhenEnvUnanswered: a server that leaves the env request
// unanswered (warpgate) gets the shell on another channel, untagged, and
// is not asked again.
func TestShellStartsWhenEnvUnanswered(t *testing.T) {
	srv := server(t)
	srv.IgnoreEnv = true
	old := envWait
	envWait = 50 * time.Millisecond
	t.Cleanup(func() { envWait = old; noEnv.Delete(srv.Addr) })
	cfg := Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()}
	s := New(cfg, Options{}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	waitText(t, s, "Welcome")
	s.Write([]byte("hello\r"))
	waitText(t, s, "you said: hello")
	if _, ok := s.Tag(); ok {
		t.Error("tagged without the server's answer")
	}
	if n := srv.Envs(); n != 1 {
		t.Fatalf("%d env requests", n)
	}

	s2 := New(cfg, Options{}, 80, 24)
	defer s2.Close()
	s2.Start()
	waitState(t, s2, Connected)
	if n := srv.Envs(); n != 1 {
		t.Errorf("asked again: %d env requests", n)
	}
}

// TestShellUnansweredFails: a server that never starts the shell fails
// the session with ErrNoShell instead of leaving it connecting.
func TestShellUnansweredFails(t *testing.T) {
	srv := server(t)
	srv.IgnoreShell = true
	old := shellWait
	shellWait = 100 * time.Millisecond
	t.Cleanup(func() { shellWait = old })
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()}, Options{}, 80, 24)
	defer s.Close()
	s.Start()
	st := waitState(t, s, Failed)
	if !errors.Is(st.Err, ErrNoShell) || Classify(st.Err) != KindNoShell {
		t.Fatalf("err %v", st.Err)
	}
}

func TestCdLine(t *testing.T) {
	for dir, want := range map[string]string{
		"/var/www":  " cd '/var/www'\r",
		"~":         " cd ~\r",
		"~/a b":     " cd ~/'a b'\r",
		"/it's":     ` cd '/it'\''s'` + "\r",
		"rel/$HOME": " cd 'rel/$HOME'\r",
	} {
		if got := cdLine(dir); got != want {
			t.Errorf("cdLine(%q) = %q, want %q", dir, got, want)
		}
	}
}
