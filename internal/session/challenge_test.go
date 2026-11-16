package session

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func TestPasswordPrompt(t *testing.T) {
	for q, want := range map[string]bool{
		"Password: ":                     true,
		"demo@db's password: ":           true,
		"(current) UNIX password: ":      true,
		"密码：":                            true,
		"パスワード:":                         true,
		"Verification code: ":            false,
		"One-time password (OTP): ":      false,
		"OTP Password: ":                 false,
		"New password: ":                 false,
		"Retype new password: ":          false,
		"动态密码：":                          false,
		"短信验证码：":                         false,
		"请输入动态口令：":                       false,
		"Passcode or option (1-3): ":     false,
		"Enter PASSCODE: ":               false,
		"Duo two-factor login for demo:": false,
	} {
		if got := passwordPrompt(q); got != want {
			t.Errorf("passwordPrompt(%q) = %v, want %v", q, got, want)
		}
	}
}

// recorder is an Ask that answers with answers and keeps what it was
// asked.
type recorder struct {
	mu      sync.Mutex
	asked   []Challenge
	answers []string
	err     error
}

func (r *recorder) ask(_ context.Context, ch Challenge) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, ch)
	return r.answers, r.err
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.asked)
}

func TestResponderSavedPasswordAndAsk(t *testing.T) {
	rec := &recorder{answers: []string{"123456"}}
	r := &responder{ctx: context.Background(), password: "secret", declinePassword: true, ask: rec.ask, hop: "bastion", user: "demo"}
	// Nothing asked: no sheet.
	if got, err := r.answer("", "Welcome", nil, nil); err != nil || len(got) != 0 || rec.count() != 0 {
		t.Fatalf("empty round: %q, %v, asked %d", got, err, rec.count())
	}
	// The password is the saved one; the code is asked, alone.
	got, err := r.answer("2FA", "Log in", []string{"Password: ", "Verification code: "}, []bool{false, true})
	if err != nil || !slices.Equal(got, []string{"secret", "123456"}) {
		t.Fatalf("answers %q, %v", got, err)
	}
	if rec.count() != 1 {
		t.Fatalf("asked %d times", rec.count())
	}
	ch := rec.asked[0]
	if ch.Hop != "bastion" || ch.User != "demo" || ch.Name != "2FA" || ch.Instruction != "Log in" ||
		!slices.Equal(ch.Questions, []string{"Verification code: "}) || !slices.Equal(ch.Echo, []bool{true}) {
		t.Fatalf("challenge %+v", ch)
	}
	// Asked for the password again, it was refused: not sent twice, and
	// not asked here, as the caller asks for passwords itself.
	if _, err := r.answer("", "", []string{"Password: "}, []bool{false}); !errors.Is(err, errPasswordWanted) || Classify(err) != KindAuth || rec.count() != 1 {
		t.Fatalf("second password prompt: %v, asked %d", err, rec.count())
	}
}

func TestResponderWithoutPassword(t *testing.T) {
	// A key login with nothing typed for passwords: the server's password
	// question is asked.
	rec := &recorder{answers: []string{"typed"}}
	r := &responder{ctx: context.Background(), ask: rec.ask}
	if got, err := r.answer("", "", []string{"Password: "}, []bool{false}); err != nil || !slices.Equal(got, []string{"typed"}) {
		t.Fatalf("key login password: %q, %v", got, err)
	}
	// A host whose password the caller asks for: declined, for the
	// password prompt and a new attempt.
	r = &responder{ctx: context.Background(), declinePassword: true, ask: rec.ask}
	if _, err := r.answer("", "", []string{"Password: "}, []bool{false}); !errors.Is(err, errPasswordWanted) {
		t.Fatalf("declined password: %v", err)
	}
	// But a one-time password is asked all the same.
	if got, err := r.answer("", "", []string{"OTP: "}, []bool{false}); err != nil || !slices.Equal(got, []string{"typed"}) {
		t.Fatalf("otp without password: %q, %v", got, err)
	}
}

func TestResponderUnattended(t *testing.T) {
	r := &responder{ctx: context.Background(), password: "secret", declinePassword: true}
	if got, err := r.answer("", "", []string{"Password: "}, []bool{false}); err != nil || got[0] != "secret" {
		t.Fatalf("saved password unattended: %q, %v", got, err)
	}
	_, err := r.answer("", "", []string{"Verification code: "}, []bool{false})
	if !errors.Is(err, ErrChallengeUnattended) || Classify(err) != KindChallenge || Classify(err).Retryable() {
		t.Fatalf("otp unattended: %v", err)
	}
}

func TestResponderCanceled(t *testing.T) {
	rec := &recorder{err: ErrChallengeCanceled}
	r := &responder{ctx: context.Background(), ask: rec.ask}
	_, err := r.answer("", "", []string{"Verification code: "}, []bool{false})
	if !errors.Is(err, ErrChallengeCanceled) || !errors.Is(r.err, ErrChallengeCanceled) || Classify(err) != KindChallenge {
		t.Fatalf("canceled: %v", err)
	}
	// Too few answers fail the login rather than sending them.
	rec = &recorder{answers: nil}
	r = &responder{ctx: context.Background(), ask: rec.ask}
	if _, err := r.answer("", "", []string{"Code: "}, []bool{false}); !errors.Is(err, ErrChallengeCanceled) {
		t.Fatalf("no answers: %v", err)
	}
}

func TestResponderContextUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	asked := make(chan struct{})
	r := &responder{ctx: ctx, ask: func(ctx context.Context, ch Challenge) ([]string, error) {
		close(asked)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() {
		_, err := r.answer("", "", []string{"Verification code: "}, []bool{false})
		done <- err
	}()
	<-asked
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question still waits after its context ended")
	}
}

// otpServer asks the password, then a one-time password otp, in two
// keyboard-interactive rounds, and takes no other login.
func otpServer(t *testing.T, otp string) *sshtest.Server {
	t.Helper()
	s, err := sshtest.Start("127.0.0.1:0", "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.SetKeyboardInteractive(func(m ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		if _, err := client("", "Authorized use only.", nil, nil); err != nil {
			return nil, err
		}
		ans, err := client("", "", []string{"Password: "}, []bool{false})
		if err != nil || len(ans) != 1 || ans[0] != "secret" {
			return nil, errors.New("wrong password")
		}
		ans, err = client("Two-factor", "Enter the code from your app.", []string{"Verification code: "}, []bool{false})
		if err != nil || len(ans) != 1 || ans[0] != otp {
			return nil, errors.New("wrong code")
		}
		return nil, nil
	})
	return s
}

func TestDialAsksOneTimePassword(t *testing.T) {
	srv := otpServer(t, "123456")
	var asked []Challenge
	cfg := Config{Name: "db", Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback(),
		// The user takes longer than the handshake may: the deadline waits.
		Timeout: 300 * time.Millisecond,
		Ask: func(ctx context.Context, ch Challenge) ([]string, error) {
			asked = append(asked, ch)
			time.Sleep(600 * time.Millisecond)
			return []string{"123456"}, nil
		}}
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(asked) != 1 || asked[0].Hop != "db" || !slices.Equal(asked[0].Questions, []string{"Verification code: "}) || asked[0].Instruction != "Enter the code from your app." {
		t.Fatalf("asked %+v", asked)
	}

	// Canceled: the login fails as canceled, not as a refused password.
	cfg.Ask = func(context.Context, Challenge) ([]string, error) { return nil, ErrChallengeCanceled }
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrChallengeCanceled) || Classify(err) != KindChallenge {
		t.Fatalf("canceled: %v (%v)", err, Classify(err))
	}
	// No one to ask: it fails at once.
	cfg.Ask = nil
	if _, err := Dial(context.Background(), cfg); !errors.Is(err, ErrChallengeUnattended) {
		t.Fatalf("unattended: %v", err)
	}
	// Closed while asking: the question gives up.
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Ask = func(ctx context.Context, ch Challenge) ([]string, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := Dial(ctx, cfg); err == nil || Classify(err) == KindChallenge {
		t.Fatalf("closed while asking: %v", err)
	}
}

func TestDialAsksAtJumpHost(t *testing.T) {
	b, db := otpServer(t, "654321"), server(t)
	cfg := jumpCfg(knownAll(t, b, db), b, db, "secret")
	var hops []string
	cfg.Ask = func(ctx context.Context, ch Challenge) ([]string, error) {
		hops = append(hops, ch.Hop)
		return []string{"654321"}, nil
	}
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if !slices.Equal(hops, []string{"bastion-1"}) {
		t.Fatalf("asked at %q", hops)
	}
	cfg.Ask = nil
	_, err = Dial(context.Background(), cfg)
	var he *HopError
	if !errors.As(err, &he) || he.Index != 0 || !errors.Is(err, ErrChallengeUnattended) {
		t.Fatalf("unattended jump: %v", err)
	}
}

// TestLocalKeysBeforeQuestions: a host whose password is asked by the
// caller, logging in with this computer's keys, still stops for a key
// file's passphrase, and fails as refused keys, not asking the server's
// password question here.
func TestLocalKeysBeforeQuestions(t *testing.T) {
	srv := otpServer(t, "123456")
	enc := localKey(t, "pw", false)
	srv.AuthorizedKey = enc.Public
	rec := &recorder{answers: []string{"x"}}
	cfg := Config{Addr: srv.Addr, User: "demo", HostKey: trusted(t, srv).Callback(), Ask: rec.ask,
		Credentials: func(context.Context) (Credentials, error) {
			return Credentials{LocalKeys: []LocalKey{enc}, PasswordPrompt: true}, nil
		}}
	_, err := Dial(context.Background(), cfg)
	var le *LocalKeyError
	if !errors.As(err, &le) || Classify(err) != KindLocalKey {
		t.Fatalf("key needing its passphrase: %v", err)
	}
	srv.AuthorizedKey = agentSigner(t).PublicKey()
	_, err = Dial(context.Background(), cfg)
	var kr *KeysRefusedError
	if !errors.As(err, &kr) || Classify(err) != KindAuth {
		t.Fatalf("keys refused: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("asked %+v", rec.asked)
	}
}

// TestUnlistedKbd: after refused keys, the keyboard-interactive login is
// tried once though the server does not list it (a JumpServer before
// v3.7), never ahead of a password left to try or of x/crypto's own pick.
func TestUnlistedKbd(t *testing.T) {
	kbd := ssh.KeyboardInteractive(nil)
	for _, c := range []struct {
		name                    string
		password                bool
		tried, allowed, partial []string
		want                    bool
	}{
		{name: "before the keys", tried: []string{"none"}, allowed: []string{"publickey", "password"}},
		{name: "keys refused", tried: []string{"none", "publickey"}, allowed: []string{"publickey", "password"}, want: true},
		{name: "password to try", password: true, tried: []string{"none", "publickey"}, allowed: []string{"publickey", "password"}},
		{name: "password refused", password: true, tried: []string{"none", "publickey", "password"}, allowed: []string{"publickey", "password"}, want: true},
		{name: "password not allowed", password: true, tried: []string{"none", "publickey"}, allowed: []string{"publickey"}, want: true},
		{name: "listed", tried: []string{"none", "publickey"}, allowed: []string{"publickey", "keyboard-interactive"}},
		{name: "tried", tried: []string{"none", "publickey", "keyboard-interactive"}, allowed: []string{"publickey"}},
		{name: "after partial success", tried: []string{"none"}, partial: []string{"publickey"}, allowed: []string{"keyboard-interactive"}},
	} {
		var picked bool
		got, err := unlistedKbd(kbd, c.password, &picked)(&ssh.ClientAuthContext{TriedMethods: c.tried, AllowedMethods: c.allowed, PartialSuccessMethods: c.partial})
		if err != nil || (got != nil) != c.want || picked != c.want {
			t.Errorf("%s: picked %v %v (%v), want %v", c.name, got != nil, picked, err, c.want)
		}
	}
}

// TestRefusedKeyWithoutQuestions: a server refusing the key that has no
// keyboard-interactive login still fails as a refused login, asking
// nothing.
func TestRefusedKeyWithoutQuestions(t *testing.T) {
	srv := server(t)
	key, _ := keyPEM(t, "")
	_, srv.AuthorizedKey = keyPEM(t, "")
	asked := 0
	_, err := Dial(context.Background(), Config{Addr: srv.Addr, User: "demo", HostKey: trusted(t, srv).Callback(),
		Credentials: func(context.Context) (Credentials, error) { return Credentials{PrivateKey: key}, nil },
		Ask:         func(context.Context, Challenge) ([]string, error) { asked++; return nil, nil }})
	if Classify(err) != KindAuth || asked != 0 {
		t.Fatalf("refused key: %v (%v), asked %d", err, Classify(err), asked)
	}
}
