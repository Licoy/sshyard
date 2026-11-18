package app

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

// The subscriptions of hosts, as rented servers bill: the editor's
// fields, how a renewal reads, and the system notification as one comes
// within its reminder.

const (
	// renewalFirst is how soon after launch the renewals are checked,
	// renewalEvery how often after, so that a new day is seen within it.
	renewalFirst = 5 * time.Second
	renewalEvery = time.Hour
)

// renewCycleLabel names a subscription's period of months.
func renewCycleLabel(m locales.Messages, months int) string {
	switch months {
	case 1:
		return m.RenewMonthly()
	case 3:
		return m.RenewQuarterly()
	case 6:
		return m.RenewHalfYearly()
	case 12:
		return m.RenewYearly()
	case 24:
		return m.RenewTwoYears()
	case 36:
		return m.RenewThreeYears()
	}
	return m.RenewEveryMonths(months)
}

// remindLabel names a reminder of days before a renewal.
func remindLabel(m locales.Messages, days int) string {
	if days == 0 {
		return m.RemindNever()
	}
	return m.RemindDaysBefore(days)
}

// renewWhen is how far a renewal days away is: today, or in so many days.
func renewWhen(m locales.Messages, days int) string {
	if days == 0 {
		return m.RenewToday()
	}
	return m.RenewInDays(days)
}

// renewalLine is how the next renewal of r reads at now: its day, how far
// it is and the period; and its color, the warning within its reminder
// and the danger on its day, else none.
func renewalLine(m locales.Messages, tok theme.Tokens, r assets.Renewal, now time.Time) (string, ui.Color, bool) {
	next, ok := r.Next(now)
	if !ok {
		return "", ui.Color{}, false
	}
	days, _ := r.DaysLeft(now)
	line := next.Format(assets.DateLayout) + " · " + renewWhen(m, days) + " · " + renewCycleLabel(m, r.Months)
	return line, renewalColor(tok, r, now), true
}

// renewalColor is the color of r's next renewal at now: the danger on
// its day, the warning within its reminder, else none.
func renewalColor(tok theme.Tokens, r assets.Renewal, now time.Time) ui.Color {
	days, soon := r.Reminding(now)
	switch {
	case soon && days == 0:
		return tok.Danger
	case soon:
		return tok.Warning
	}
	return ui.Color{}
}

// renewalForm is the subscription fields of the host editor: provider
// and note are Host.Provider and Host.Note; renew keeps a subscription,
// begun on renewStart (in the local time zone), of renewMonths months,
// reminded renewRemind days before; renewOpen shows them.
type renewalForm struct {
	provider, note string
	renew          bool
	renewStart     time.Time
	renewMonths    int
	renewRemind    int
	renewOpen      bool
}

// loadRenewal fills the fields from h, nil for a new host, whose
// subscription would begin today.
func (f *renewalForm) loadRenewal(h *assets.Host, now time.Time) {
	f.renewStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	f.renewMonths, f.renewRemind = 1, assets.RenewalRemindDefault
	if h == nil {
		return
	}
	f.provider, f.note = h.Provider, h.Note
	if r := h.Renewal; r.On() {
		if d, err := time.ParseInLocation(assets.DateLayout, r.Start, time.Local); err == nil {
			f.renewStart = d
		}
		f.renew, f.renewMonths, f.renewRemind = true, r.Months, r.Remind
	}
	f.renewOpen = f.renew || f.provider != "" || f.note != ""
}

// renewal is the subscription the fields keep, none while renew is off.
func (f *renewalForm) renewal() assets.Renewal {
	if !f.renew {
		return assets.Renewal{}
	}
	return assets.Renewal{Start: f.renewStart.Format(assets.DateLayout), Months: f.renewMonths, Remind: f.renewRemind}
}

// applyRenewal puts the fields in h.
func (f *renewalForm) applyRenewal(h *assets.Host) {
	h.Provider, h.Note, h.Renewal = f.provider, f.note, f.renewal()
}

// renewalFields are the editor's subscription section: the provider, the
// subscription with its next renewal, and the note.
func (a *App) renewalFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	row := func(label string, fn func()) {
		ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, label).FontSize(theme.Footnote).TextColor(tok.TextMuted).Width(92).Shrink(0)
			fn()
		})
	}
	// choose is a select of the values, as label names them, keeping a
	// value the editor does not offer.
	choose := func(key, label string, value *int, values []int, name func(int) string) {
		if !slices.Contains(values, *value) {
			values = append(slices.Clone(values), *value)
		}
		names := make([]string, len(values))
		for i, v := range values {
			names[i] = name(v)
		}
		cur := name(*value)
		if formSelect(c.Key(key), &cur, names).Label(label).Grow(1).Changed() {
			if i := slices.Index(names, cur); i >= 0 {
				*value = values[i]
			}
		}
	}
	ui.Column(c).Gap(10).FillWidth().Margin(8, 0, 0, 0).Children(func() {
		row(m.FieldProvider(), func() {
			formTextInput(c.Key("f:provider"), &f.provider).Placeholder("Vultr").Label(m.FieldProvider()).Grow(1)
		})
		formCheckbox(c.Key("f:renew"), &f.renew, m.TrackRenewal())
		if f.renew {
			row(m.RenewalStart(), func() {
				ui.DateInput(c.Key("f:rstart"), &f.renewStart).Label(m.RenewalStart()).Grow(1)
			})
			row(m.RenewalCycle(), func() {
				choose("f:rcycle", m.RenewalCycle(), &f.renewMonths, assets.RenewalMonths, func(n int) string { return renewCycleLabel(m, n) })
			})
			row(m.RenewalRemind(), func() {
				choose("f:rremind", m.RenewalRemind(), &f.renewRemind, assets.RenewalReminds, func(n int) string { return remindLabel(m, n) })
			})
			if line, col, ok := renewalLine(m, tok, f.renewal(), c.Now()); ok {
				t := ui.Text(c, m.RenewalNextOn(line)).FontSize(theme.Caption).TextColor(tok.TextMuted)
				if col != (ui.Color{}) {
					t.TextColor(col)
				}
			}
		}
		ui.Column(c).Gap(6).FillWidth().Children(func() {
			ui.Text(c, m.FieldNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			formTextArea(c.Key("f:note"), &f.note).Label(m.FieldNote()).FillWidth().Height(72)
			ui.Text(c, m.NoteHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}

// startRenewalReminders checks the hosts' renewals until the app closes.
func (a *App) startRenewalReminders() {
	ctx := a.cloudCtx
	go func() {
		tm := time.NewTimer(renewalFirst)
		defer tm.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tm.C:
				a.post(func() { a.remindRenewals(time.Now()) })
				tm.Reset(renewalEvery)
			}
		}
	}()
}

// remindRenewals shows a system notification for each renewal within its
// reminder at now, once for each, while the setting allows. What it
// notified of stays in the settings, for the renewals still to come.
func (a *App) remindRenewals(now time.Time) {
	if a.p.Notify == nil || !a.settings.RenewalNotify || !a.ready() {
		return
	}
	m := locales.In(i18n.Locale())
	notified := map[string]string{}
	for _, h := range a.store.List() {
		days, soon := h.Renewal.Reminding(now)
		if !soon {
			continue
		}
		next, _ := h.Renewal.Next(now)
		day := next.Format(assets.DateLayout)
		notified[h.ID] = day
		if a.settings.RenewalNotified[h.ID] == day {
			continue
		}
		parts := []string{day, renewCycleLabel(m, h.Renewal.Months)}
		if h.Provider != "" {
			parts = append(parts, h.Provider)
		}
		title, body, id := m.RenewalNotifyTitle(a.pv(h.Label()), renewWhen(m, days)), strings.Join(parts, " · "), h.ID
		notify := a.p.Notify
		// It may wait for the user to allow notifications.
		go func() {
			_, err := notify(context.Background(), Notification{ID: "renewal:" + id, Title: title, Body: body, OnClick: func() {
				a.post(func() {
					a.activateMainWindow()
					a.revealHost(id)
				})
			}})
			if err != nil {
				log.Printf("show renewal notification: %v", err)
			}
		}()
	}
	if maps.Equal(notified, a.settings.RenewalNotified) {
		return
	}
	s := a.settings
	s.RenewalNotified = nil
	if len(notified) > 0 {
		s.RenewalNotified = notified
	}
	if err := a.ApplySettings(s); err != nil {
		a.toastError(locales.In(i18n.Locale()).ErrGeneric(err.Error()))
	}
}

// revealHost shows host id: chosen in the sidebar, with its details.
func (a *App) revealHost(id string) {
	if _, ok := a.store.Get(id); !ok {
		return
	}
	a.selected, a.page = id, pageWorkspace
	a.ShowTool(layout.HostInfo)
	a.invalidate()
}

// aiRenewal is how the assistant reads a subscription at now, "" for none.
func aiRenewal(r assets.Renewal, now time.Time) string {
	next, ok := r.Next(now)
	if !ok {
		return ""
	}
	days, _ := r.DaysLeft(now)
	return fmt.Sprintf("renews every %d months since %s; next renewal %s, in %d days", r.Months, r.Start, next.Format(assets.DateLayout), days)
}
