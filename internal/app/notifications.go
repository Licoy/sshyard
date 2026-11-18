package app

import (
	"context"
	"errors"
	"log"

	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/i18n"
)

// Notification is a desktop notification. An empty ID makes it unique;
// reusing an ID replaces the earlier notification.
type Notification struct {
	ID, Title, Body string
	OnClick         func()
}

// ErrNotificationsDenied means the system has denied notification permission.
var ErrNotificationsDenied = errors.New("system notifications are denied")

// NotifyPendingApprovals runs on the UI thread when the main window leaves
// the foreground. Each pending request gets at most one notice.
func (a *App) NotifyPendingApprovals() {
	for _, c := range a.conns {
		if c.ai != nil && c.ai.pending != nil {
			a.notifyApproval(c)
		}
	}
}

func (a *App) notifyApproval(c *conn) {
	pending, notify := c.ai.pending, a.p.Notify
	if notify == nil || pending.ctx == nil || pending.ctx.Err() != nil || pending.cancelNotification != nil {
		return
	}
	ctx, cancel := context.WithCancel(pending.ctx)
	pending.cancelNotification = cancel
	m := locales.In(i18n.Locale())
	note := Notification{Title: m.AiApprovalNotificationTitle(), Body: m.AiApprovalNotificationBody(), OnClick: func() {
		a.post(func() {
			if ctx.Err() != nil || a.conns[c.id] != c || c.ai.pending != pending {
				return
			}
			a.showPane(c)
			a.activateMainWindow()
		})
	}}
	go func() {
		defer cancel()
		withdraw, err := notify(ctx, note)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("show AI approval notification: %v", err)
			}
			return
		}
		<-ctx.Done()
		withdraw()
	}()
}

func (a *App) activateMainWindow() {
	if activate := a.p.ActivateMainWindow; activate != nil {
		activate()
	}
}

func (a *App) testNotification(m locales.Messages) {
	notify := a.p.Notify
	if notify == nil {
		a.toastError(m.NotificationTestFailed())
		return
	}
	note := Notification{Title: m.NotificationTestTitle(), Body: m.NotificationTestBody()}
	go func() {
		_, err := notify(context.Background(), note)
		if err == nil {
			return
		}
		message := m.NotificationTestFailed()
		if errors.Is(err, ErrNotificationsDenied) {
			message = m.NotificationTestDenied()
		} else {
			log.Printf("show test notification: %v", err)
		}
		a.post(func() { a.toastError(message) })
	}()
}
