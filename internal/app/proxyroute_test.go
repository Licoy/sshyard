package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
)

// dialRecorder replaces the app's dialer: it notes each address and
// connects nowhere.
type dialRecorder struct {
	mu    sync.Mutex
	addrs []string
}

var errNoDial = errors.New("test: not dialed")

func (d *dialRecorder) dial(_ context.Context, _, addr string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addrs = append(d.addrs, addr)
	return nil, errNoDial
}

func (d *dialRecorder) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	addrs := d.addrs
	d.addrs = nil
	return addrs
}

func addTestProxy(t *testing.T, a *App, name string) assets.Proxy {
	t.Helper()
	px, err := a.store.AddProxy(assets.Proxy{ID: assets.NewID(), Name: name, Kind: assets.ProxySOCKS5, Address: "10.9.9.9", Port: 1080})
	if err != nil {
		t.Fatal(err)
	}
	return px
}

func TestProxyRoute(t *testing.T) {
	a, _ := newTestApp(t)
	d := &dialRecorder{}
	a.p.Dial = d.dial
	px := addTestProxy(t, a, "office")
	ctx := context.Background()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1", nil)

	for _, tc := range []struct {
		name, id string
		want     string
	}{
		{"system, loopback", "", "api.example.com:443"},
		{"direct", settings.ProxyDirect, "api.example.com:443"},
		{"saved proxy", px.ID, "10.9.9.9:1080"},
	} {
		r := a.proxyRoute(func() string { return tc.id })
		if u, err := r.Proxy(req); u != nil || err != nil {
			t.Errorf("%s: HTTP proxy %v %v", tc.name, u, err)
		}
		r.Dial(ctx, "tcp", "api.example.com:443")
		if got := d.take(); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("%s: dialed %v, want %s", tc.name, got, tc.want)
		}
	}

	// A deleted proxy goes nowhere rather than around it.
	r := a.proxyRoute(func() string { return "gone" })
	if _, err := r.Dial(ctx, "tcp", "api.example.com:443"); !errors.Is(err, errProxyMissing) {
		t.Errorf("missing proxy: %v", err)
	}
	if got := d.take(); len(got) != 0 {
		t.Errorf("missing proxy dialed %v", got)
	}
}

func TestAIProviderProxy(t *testing.T) {
	a, _ := newTestApp(t)
	m := locales.In("en")
	d := &dialRecorder{}
	a.p.Dial = d.dial
	px := addTestProxy(t, a, "office")

	a.openProvider(nil)
	f := a.prov
	f.name, f.base, f.model, f.key, f.proxy = "Local", "http://127.0.0.1:9/v1", "m", "k", px.ID
	if !a.saveProvider(m) {
		t.Fatal(f.err)
	}
	p := a.Settings().AIProviders[0]
	if p.Proxy != px.ID {
		t.Fatalf("saved proxy %q, want %q", p.Proxy, px.ID)
	}
	a.openProvider(&p)
	if a.prov.proxy != px.ID {
		t.Fatalf("reopened proxy %q", a.prov.proxy)
	}

	ask := func(p settings.AIProvider) []string {
		t.Helper()
		cl, err := a.providerClient(&p)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cl.Test(ctx); err == nil {
			t.Fatal("test request reached nothing yet passed")
		}
		return d.take()
	}
	if got := ask(p); !slices.Equal(got, []string{"10.9.9.9:1080"}) {
		t.Errorf("through the proxy: dialed %v", got)
	}
	p.Proxy = settings.ProxyDirect
	if got := ask(p); !slices.Equal(got, []string{"127.0.0.1:9"}) {
		t.Errorf("direct: dialed %v", got)
	}
}

func TestUpdateProxyRoute(t *testing.T) {
	upd := &fakeUpdates{enabled: true}
	a, tt := newUpdatesApp(t, upd)
	m := locales.In("en")
	d := &dialRecorder{}
	a.p.Dial = d.dial
	px := addTestProxy(t, a, "office")
	ctx := context.Background()

	if upd.route == nil {
		t.Fatal("no route for the updates at launch")
	}
	s := a.Settings()
	s.UpdateProxy = px.ID
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	upd.route.Dial(ctx, "tcp", "github.com:443")
	if got := d.take(); !slices.Equal(got, []string{"10.9.9.9:1080"}) {
		t.Errorf("updates through the proxy: dialed %v", got)
	}

	// "New proxy…" on the General page saves the new one as the choice.
	a.openSettings(prefGeneral)
	tt.Frame()
	a.updProxy = proxyNew
	tt.Frame()
	if a.proxyForm == nil || a.updProxy != px.ID {
		t.Fatalf("New proxy…: sheet %v, choice %q", a.proxyForm != nil, a.updProxy)
	}
	a.proxyForm.name, a.proxyForm.address = "home", "10.8.8.8"
	if !a.SaveProxyForm(m) {
		t.Fatal(a.proxyForm.err)
	}
	home, ok := a.store.Proxy(a.Settings().UpdateProxy)
	if !ok || home.Name != "home" {
		t.Fatalf("update proxy %q", a.Settings().UpdateProxy)
	}

	// Deleted, it fails closed.
	if err := a.DeleteProxy(home.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := upd.route.Dial(ctx, "tcp", "github.com:443"); !errors.Is(err, errProxyMissing) {
		t.Errorf("deleted update proxy: %v", err)
	}
}

func TestProxySelectNewComesBack(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")

	// The AI provider sheet: canceled, the choice before stays.
	a.openProvider(nil)
	f := a.prov
	f.proxy = settings.ProxyDirect
	tt.Frame()
	f.proxy = proxyNew
	tt.Frame()
	if a.proxyForm == nil || a.provOpen || f.proxy != settings.ProxyDirect {
		t.Fatalf("New proxy…: sheet %v, provider open %v, proxy %q", a.proxyForm != nil, a.provOpen, f.proxy)
	}
	tt.Frame()
	if a.prov != f {
		t.Fatal("the provider sheet was dropped behind the proxy sheet")
	}
	a.proxyEditing = false
	tt.Frame()
	if a.proxyForm != nil || !a.provOpen || a.prov != f || f.proxy != settings.ProxyDirect {
		t.Fatalf("canceled: provider open %v, proxy %q", a.provOpen, f.proxy)
	}
	// Saved, the new one is chosen.
	f.proxy = proxyNew
	tt.Frame()
	a.proxyForm.name, a.proxyForm.address = "office", "10.9.9.9"
	if !a.SaveProxyForm(m) {
		t.Fatal(a.proxyForm.err)
	}
	if px, ok := a.store.Proxy(f.proxy); !a.provOpen || a.prov != f || !ok || px.Name != "office" {
		t.Fatalf("saved: provider open %v, proxy %q", a.provOpen, f.proxy)
	}
	a.provOpen = false
	tt.Frame()

	// The backup location sheet.
	a.openTargetForm(nil)
	g := a.target
	g.kind = slices.Index(targetKinds, settings.BackupWebDAV)
	tt.Frame()
	g.proxy = proxyNew
	tt.Frame()
	if a.proxyForm == nil || a.targetEditing || g.proxy != "" {
		t.Fatalf("New proxy…: sheet %v, location open %v, proxy %q", a.proxyForm != nil, a.targetEditing, g.proxy)
	}
	tt.Frame()
	a.proxyForm.name, a.proxyForm.address = "lab", "10.7.7.7"
	if !a.SaveProxyForm(m) {
		t.Fatal(a.proxyForm.err)
	}
	if px, ok := a.store.Proxy(g.proxy); !a.targetEditing || a.target != g || !ok || px.Name != "lab" {
		t.Fatalf("saved: location open %v, proxy %q", a.targetEditing, g.proxy)
	}
}
