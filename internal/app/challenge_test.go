package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/forward"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/sshtest"
	"github.com/Licoy/sshyard/locales"
	"golang.org/x/crypto/ssh"
)

// otpServer logs in with the password "secret", then the one-time
// password otp, asked by keyboard-interactive questions alone.
func otpServer(t *testing.T, otp string) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.Start("127.0.0.1:0", "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.DirectTCPIP = true
	srv.SetKeyboardInteractive(func(m ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		ans, err := client("", "", []string{"Password: ", "Verification code: "}, []bool{false, false})
		if err != nil || len(ans) != 2 || ans[0] != "secret" || ans[1] != otp {
			return nil, errors.New("refused")
		}
		return nil, nil
	})
	return srv
}

// waitChallenge runs frames until the sheet asks a round of questions.
func waitChallenge(t *testing.T, a *App, tt interface{ Frame() }) *challengeAsk {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(a.challenges) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no login questions asked")
		}
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
	return a.challenges[0]
}

func TestChallengeSheetAnswersOneTimePassword(t *testing.T) {
	const otp = "246810"
	srv := otpServer(t, otp)
	a, tt := newTestApp(t)
	m := locales.In("en")
	h := addHost(t, a, srv, "bastion", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	// The saved password answers its question; only the code is asked.
	r := waitChallenge(t, a, tt)
	if !slices.Equal(r.ch.Questions, []string{"Verification code: "}) || r.ch.Hop != "bastion" || !a.challenging {
		t.Fatalf("asked %+v", r.ch)
	}
	r.answers[0] = otp
	a.answerChallenge(r, false)
	if len(a.challenges) != 0 || a.challenging || r.answers[0] != "" {
		t.Fatal("the sheet stays open, or keeps the code")
	}
	waitConnected(t, a, tt, id)
	// The code is used once: not kept for this run, nor written anywhere.
	a.mu.Lock()
	for k, v := range a.typed {
		if v == otp {
			t.Errorf("the code is kept under %q", k)
		}
	}
	a.mu.Unlock()
	noSecrets(t, a.p.DataDir, otp)

	// Canceled: the pane fails as canceled, asking no password. The
	// closed pane lets go of its connection in the background; until it
	// has, a new pane would share it and be asked nothing.
	a.CloseSession(id)
	waitFor(t, tt, "connection released", func() bool { return a.pool.Shared(a.poolKey(h)) == 0 })
	id, err = a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	r = waitChallenge(t, a, tt)
	a.answerChallenge(r, true)
	c := a.conns[id]
	waitFor(t, tt, "failed", func() bool { return c.s.Status().State == session.Failed })
	st := c.s.Status()
	if !errors.Is(st.Err, session.ErrChallengeCanceled) || a.errText(m, h, st.Err) != m.ErrChallengeCanceled("bastion") {
		t.Fatalf("canceled: %v", st.Err)
	}
	tt.Frame()
	if a.prompt != nil || len(a.challenges) != 0 {
		t.Fatal("canceling asked again")
	}
}

func TestChallengeAskerEnds(t *testing.T) {
	a, tt := newTestApp(t)
	c := &conn{id: 9001, host: assets.Host{ID: "h", Name: "box", Address: "example.com", User: "demo"}}
	a.conns[c.id] = c
	t.Cleanup(func() { delete(a.conns, c.id) })
	ch := session.Challenge{Hop: "box", Questions: []string{"OTP: "}, Echo: []bool{false}}
	type result struct {
		answers []string
		err     error
	}
	ask := func(ctx context.Context) chan result {
		out := make(chan result, 1)
		f := a.challengeAsker(c)
		go func() {
			ans, err := f(ctx, ch)
			out <- result{ans, err}
		}()
		return out
	}
	// await runs frames, which hand the question to the sheet, until the
	// login has its result.
	await := func(got chan result) result {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			select {
			case res := <-got:
				return res
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("the question still waits")
			}
			time.Sleep(10 * time.Millisecond)
			tt.Frame()
		}
	}

	// The login closing (the pane, the handshake) stops the wait and the
	// sheet.
	ctx, cancel := context.WithCancel(context.Background())
	got := ask(ctx)
	waitChallenge(t, a, tt)
	cancel()
	if res := await(got); !errors.Is(res.err, context.Canceled) {
		t.Fatalf("after cancel: %v", res.err)
	}
	waitFor(t, tt, "sheet closed", func() bool { return len(a.challenges) == 0 && !a.challenging })

	// A pane restored at launch, not connected by the user since: fails
	// as unattended, with no sheet.
	c.restoredQuiet = true
	if res := await(ask(context.Background())); !errors.Is(res.err, session.ErrChallengeUnattended) {
		t.Fatalf("restored pane: %v", res.err)
	}
	c.restoredQuiet = false
	// A pane closed before the question came: canceled.
	delete(a.conns, c.id)
	if res := await(ask(context.Background())); !errors.Is(res.err, session.ErrChallengeCanceled) {
		t.Fatalf("closed pane: %v", res.err)
	}
	if len(a.challenges) != 0 {
		t.Fatal("a sheet opened for no one")
	}
	// No one to ask at all.
	if a.challengeAsker(nil) != nil || a.challengeAsker(&conn{unattended: true}) != nil {
		t.Fatal("an unattended connection asks")
	}
}

func TestAutoStartedForwardDoesNotAsk(t *testing.T) {
	srv := otpServer(t, "135790")
	a, tt := newTestApp(t)
	m := locales.In("en")
	h := addHost(t, a, srv, "bastion", "secret")
	a.known.Add(srv.Addr, srv.HostKey.PublicKey())
	fw, err := a.Store().SaveForward(assets.Forward{Name: "db", Kind: assets.ForwardLocal, HostID: h.ID, AutoStart: true,
		ListenPort: freePort(t), TargetAddr: "127.0.0.1", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	a.autoStartForwards()
	waitFor(t, tt, "forward failed", func() bool {
		st, _ := a.ForwardStatus(fw.ID)
		return st.State == forward.Error && errors.Is(st.Err, session.ErrChallengeUnattended)
	})
	if len(a.challenges) != 0 {
		t.Fatal("an automatic forward opened the sheet")
	}
	st, _ := a.ForwardStatus(fw.ID)
	if text := a.errText(m, h, st.Err); !strings.Contains(text, "bastion") || text != m.ErrChallengeUnattended("bastion") {
		t.Fatalf("error text %q", text)
	}
	// Started by the user, it asks.
	a.StopForward(fw.ID)
	if err := a.StartForward(fw.ID); err != nil {
		t.Fatal(err)
	}
	r := waitChallenge(t, a, tt)
	a.answerChallenge(r, true)
}
