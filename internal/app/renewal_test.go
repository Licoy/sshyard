package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/locales"
)

// soonRenewal is a yearly subscription renewing in about two days from
// now, within its reminder.
func soonRenewal(now time.Time) assets.Renewal {
	return assets.Renewal{Start: now.AddDate(-1, 0, 2).Format(assets.DateLayout), Months: 12, Remind: 30}
}

// TestHostSearchProviderAndNote: the sidebar's search finds a host by
// its provider and its note.
func TestHostSearchProviderAndNote(t *testing.T) {
	a, _ := newTestApp(t)
	h, err := a.Store().Add(assets.Host{Name: "web", Address: "10.0.0.5", User: "root", Provider: "Vultr", Note: "blog at 10.0.0.9", Renewal: soonRenewal(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]bool{"vultr": true, "blog": true, "nothing-like-it": false} {
		_, _, byGroup := hostsBy(a.Store(), q)
		found := false
		for _, got := range byGroup[""] {
			found = found || got.ID == h.ID
		}
		if found != want {
			t.Errorf("search %q: found %v, want %v", q, found, want)
		}
	}
}

func TestHostEditorRenewal(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	h, err := a.Store().Add(assets.Host{Name: "web", Address: "10.0.0.5", User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	a.openEditor(&h)
	tt.Frame()
	f := a.form
	if f.renew || f.renewOpen || f.renewMonths != 1 || f.renewRemind != assets.RenewalRemindDefault {
		t.Fatalf("new subscription fields: %+v", f.renewalForm)
	}
	f.provider, f.note = "Vultr", "blog\nproxy"
	f.renew, f.renewStart, f.renewMonths, f.renewRemind = true, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), 12, 7
	if !a.SaveForm(m) {
		t.Fatal(f.err)
	}
	got, _ := a.Store().Get(h.ID)
	if got.Provider != "Vultr" || got.Note != "blog\nproxy" || got.Renewal != (assets.Renewal{Start: "2026-10-01", Months: 12, Remind: 7}) {
		t.Fatalf("saved %q %q %+v", got.Provider, got.Note, got.Renewal)
	}
	a.openEditor(&got)
	tt.Frame()
	if !a.form.renewOpen || !a.form.renew || a.form.renewStart.Format(assets.DateLayout) != "2026-10-01" {
		t.Fatalf("reopened fields: %+v", a.form.renewalForm)
	}
	a.form.renew = false
	if !a.SaveForm(m) {
		t.Fatal(a.form.err)
	}
	if got, _ := a.Store().Get(h.ID); got.Renewal.On() || got.Provider != "Vultr" {
		t.Fatalf("subscription kept after turning it off: %+v", got)
	}
}

func TestRemindRenewals(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	type note struct{ id, title, body string }
	sent := make(chan note, 8)
	a.p.Notify = func(_ context.Context, n Notification) (func(), error) {
		n.OnClick()
		sent <- note{n.ID, n.Title, n.Body}
		return func() {}, nil
	}
	next := func() (note, bool) {
		select {
		case n := <-sent:
			return n, true
		case <-time.After(200 * time.Millisecond):
			return note{}, false
		}
	}
	web, err := a.Store().Add(assets.Host{Name: "web", Address: "10.0.0.5", User: "root", Provider: "Vultr", Renewal: assets.Renewal{Start: "2026-10-01", Months: 1, Remind: 3}})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []assets.Host{
		{Name: "quiet", Address: "10.0.0.6", User: "root", Renewal: assets.Renewal{Start: "2026-10-01", Months: 1}},
		{Name: "later", Address: "10.0.0.7", User: "root", Renewal: assets.Renewal{Start: "2026-10-01", Months: 1, Remind: 1}},
		{Name: "none", Address: "10.0.0.8", User: "root"},
	} {
		if _, err := a.Store().Add(h); err != nil {
			t.Fatal(err)
		}
	}
	at := func(s string) time.Time {
		d, _ := time.ParseInLocation(assets.DateLayout, s, time.Local)
		return d.Add(10 * time.Hour)
	}
	a.remindRenewals(at("2026-10-28"))
	if n, ok := next(); ok {
		t.Fatalf("notified before the reminder: %+v", n)
	}
	a.remindRenewals(at("2026-10-29"))
	n, ok := next()
	if !ok || n.id != "renewal:"+web.ID || n.title != m.RenewalNotifyTitle("web", m.RenewInDays(3)) || n.body != "2026-11-01 · "+m.RenewMonthly()+" · Vultr" {
		t.Fatalf("notification %+v %v", n, ok)
	}
	if n, ok := next(); ok {
		t.Fatalf("notified twice: %+v", n)
	}
	if got := a.settings.RenewalNotified; len(got) != 1 || got[web.ID] != "2026-11-01" {
		t.Fatalf("notified %v", got)
	}
	// Clicking it shows the host.
	tt.Frame()
	if a.selected != web.ID || !a.toolHere(layout.HostInfo) {
		t.Fatalf("click showed %q, host pane %v", a.selected, a.toolHere(layout.HostInfo))
	}
	// Each renewal notifies once, as many days before as its host says.
	a.remindRenewals(at("2026-10-31"))
	if n, ok := next(); !ok || n.title != m.RenewalNotifyTitle("later", m.RenewInDays(1)) {
		t.Fatalf("notification of the later host %+v %v", n, ok)
	}
	if n, ok := next(); ok {
		t.Fatalf("notified again of the same renewal: %+v", n)
	}
	a.remindRenewals(at("2026-11-30"))
	titles := map[string]bool{}
	for range 2 {
		if n, ok := next(); ok && strings.HasPrefix(n.body, "2026-12-01 · ") {
			titles[n.title] = true
		}
	}
	if !titles[m.RenewalNotifyTitle("web", m.RenewInDays(1))] || !titles[m.RenewalNotifyTitle("later", m.RenewInDays(1))] {
		t.Fatalf("next renewal notifications %v", titles)
	}
	a.remindRenewals(at("2026-12-05"))
	if a.settings.RenewalNotified != nil {
		t.Fatalf("past renewals kept: %v", a.settings.RenewalNotified)
	}
	// Off, nothing is notified.
	s := a.settings
	s.RenewalNotify = false
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a.remindRenewals(at("2026-12-30"))
	if n, ok := next(); ok {
		t.Fatalf("notified while off: %+v", n)
	}
}

func TestMCPHostRenewal(t *testing.T) {
	r := assets.Renewal{Start: "2026-10-01", Months: 3, Remind: 7}
	info := mcpHostOf(assets.Host{ID: "h1", Name: "web", Address: "10.0.0.5", Port: 22, Provider: "Vultr", Note: "blog", Renewal: r})
	now := time.Now()
	next, _ := r.Next(now)
	days, _ := r.DaysLeft(now)
	if info.Provider != "Vultr" || info.Note != "blog" || info.Renewal == nil ||
		*info.Renewal != (mcpRenewal{Start: "2026-10-01", EveryMonths: 3, RemindDays: 7, Next: next.Format(assets.DateLayout), DaysLeft: days}) {
		t.Fatalf("info %+v %+v", info, info.Renewal)
	}
	b, _ := json.Marshal(mcpHostOf(assets.Host{ID: "h2", Name: "db", Address: "10.0.0.6", Port: 22}))
	for _, absent := range []string{"renewal", "provider", "note"} {
		if strings.Contains(string(b), absent) {
			t.Errorf("empty %q listed: %s", absent, b)
		}
	}
}
