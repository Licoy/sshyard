package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/i18n"
)

func receiveNoticeTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("notification callback did not complete")
		var zero T
		return zero
	}
}

func newNotificationTestApp(t *testing.T, p Platform) (*App, *dispatchPump) {
	t.Helper()
	pump := newDispatchPump()
	p.Post, p.DataDir, p.Secrets = pump.post, t.TempDir(), keychain.NewMemory()
	a, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, pump
}

func askNotificationTest(t *testing.T, ctx context.Context, a *App, cn *conn, pump *dispatchPump) <-chan policy.Answer {
	t.Helper()
	result := make(chan policy.Answer, 1)
	pr := policy.Prompt{Action: policy.Action{Command: "private command", Path: "/private/path"}}
	pro := aiPrompter{a: a, conn: cn, state: cn.ai}
	go func() { result <- pro.Ask(ctx, pr) }()
	pump.next(t)
	if cn.ai.pending == nil {
		t.Fatal("approval did not become pending")
	}
	return result
}

func TestAIApprovalNotificationRoutesToPendingPane(t *testing.T) {
	for _, initiallyBackground := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially_background=%t", initiallyBackground), func(t *testing.T) {
			background, activations := initiallyBackground, 0
			sent, withdrawn := make(chan Notification, 4), make(chan struct{}, 4)
			a, pump := newNotificationTestApp(t, Platform{
				MainWindowBackground: func() bool { return background },
				ActivateMainWindow:   func() { activations++ },
				Notify: func(_ context.Context, n Notification) (func(), error) {
					sent <- n
					return func() { withdrawn <- struct{}{} }, nil
				},
			})
			host := savedHost(t, a, "private host", "10.0.0.8")
			target := a.conns[openAIPane(t, a, host.ID)]
			other := openAIPane(t, a, host.ID)
			answers := askNotificationTest(t, t.Context(), a, target, pump)
			pending := target.ai.pending
			if !initiallyBackground {
				if pending.cancelNotification != nil {
					t.Fatal("a foreground request scheduled a system notification")
				}
				background = true
				a.NotifyPendingApprovals()
			}
			note := receiveNoticeTest(t, sent)
			m := locales.In(i18n.Locale())
			if note.ID != "" || note.Title != m.AiApprovalNotificationTitle() || note.Body != m.AiApprovalNotificationBody() {
				t.Fatalf("unexpected approval notification %+v", note)
			}
			a.NotifyPendingApprovals()
			a.NotifyPendingApprovals()
			note.OnClick()
			pump.next(t)
			if !a.paneShown(target.id) || a.focusReq != target.id || activations != 1 || target.ai.pending != pending || len(answers) != 0 {
				t.Fatal("notification click did not focus the waiting pane without answering")
			}
			a.answerAI(target, policy.AllowOnce)
			if ans := receiveNoticeTest(t, answers); ans != policy.AllowOnce {
				t.Fatalf("approval answered %v", ans)
			}
			receiveNoticeTest(t, withdrawn)
			a.showPane(a.conns[other])
			note.OnClick()
			pump.next(t)
			if activations != 1 || a.focusReq != other || len(sent) != 0 {
				t.Fatal("duplicate notification or stale click changed focus")
			}
		})
	}
}

func TestAIApprovalNotificationWithdrawsWhenRequestEnds(t *testing.T) {
	for _, end := range []string{"cancelled", "timed out", "pane closed", "replaced"} {
		t.Run(end, func(t *testing.T) {
			sent, withdrawn := make(chan Notification, 2), make(chan struct{}, 2)
			a, pump := newNotificationTestApp(t, Platform{
				MainWindowBackground: func() bool { return true },
				ActivateMainWindow:   func() { t.Error("stale click activated the window") },
				Notify: func(ctx context.Context, n Notification) (func(), error) {
					sent <- n
					if end == "cancelled" {
						// Keep delivery waiting for the system permission answer.
						<-ctx.Done()
						withdrawn <- struct{}{}
						return nil, ctx.Err()
					}
					return func() { withdrawn <- struct{}{} }, nil
				},
			})
			host := savedHost(t, a, "host", "10.0.0.8")
			target := a.conns[openAIPane(t, a, host.ID)]
			other := openAIPane(t, a, host.ID)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if end == "timed out" {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, time.Second)
				defer timeoutCancel()
			}
			answers := askNotificationTest(t, ctx, a, target, pump)
			note := receiveNoticeTest(t, sent)
			switch end {
			case "cancelled":
				cancel()
			case "pane closed":
				a.CloseSession(target.id)
			case "replaced":
				later := askNotificationTest(t, t.Context(), a, target, pump)
				receiveNoticeTest(t, sent)
				receiveNoticeTest(t, withdrawn)
				note.OnClick()
				pump.next(t)
				if a.focusReq != other || target.ai.pending == nil || len(later) != 0 {
					t.Fatal("old notification interfered with the replacement")
				}
				cancel()
				receiveNoticeTest(t, answers)
				pump.next(t)
				a.answerAI(target, policy.Reject)
				receiveNoticeTest(t, later)
				receiveNoticeTest(t, withdrawn)
				return
			}
			if ans := receiveNoticeTest(t, answers); ans != policy.Cancelled {
				t.Fatalf("ended approval answered %v", ans)
			}
			receiveNoticeTest(t, withdrawn)
			note.OnClick()
			pump.next(t)
			if a.focusReq != other || target.ai.pending != nil {
				t.Fatal("ended approval changed focus or stayed pending")
			}
		})
	}
}

func TestSendNotificationTest(t *testing.T) {
	m := locales.In("en")
	for _, tt := range []struct {
		name, message string
		err           error
	}{
		{name: "sent"},
		{name: "denied", message: m.NotificationTestDenied(), err: fmt.Errorf("system: %w", ErrNotificationsDenied)},
		{name: "failed", message: m.NotificationTestFailed(), err: errors.New("delivery failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sent := make(chan Notification, 1)
			a, pump := newNotificationTestApp(t, Platform{
				MainWindowBackground: func() bool { return false },
				Notify: func(_ context.Context, n Notification) (func(), error) {
					sent <- n
					return func() {}, tt.err
				},
			})
			a.settings.RenewalNotify = false
			a.testNotification(m)
			n := receiveNoticeTest(t, sent)
			if n.ID != "" || n.Title != m.NotificationTestTitle() || n.Body != m.NotificationTestBody() || n.OnClick != nil {
				t.Fatalf("unexpected test notification %+v", n)
			}
			if tt.err == nil {
				return
			}
			pump.next(t)
			if len(a.toasts) != 1 {
				t.Fatalf("notification errors reported %d times", len(a.toasts))
			}
			if toast := a.toasts[0](m); toast.Type != "error" || toast.Title != tt.message {
				t.Fatalf("unexpected error feedback %+v", toast)
			}
		})
	}
}
