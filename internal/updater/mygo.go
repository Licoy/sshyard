package updater

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
)

// Plugin is MyGo's updater plugin drawn in native UI, which reads the
// manifest `mygo build` publishes to the latest release of the repository
// in mygo.json (updates.github = Licoy/sshyard) and verifies signatures.
// It checks again as the user installs, and as Check is called while it
// offers a version, so a release published since the offer is the one
// installed.
// It checks soon after each launch and every interval after while
// automatic is on. The interval is read once: a change applies at the
// next launch.
func Plugin(interval time.Duration, automatic bool) mygo.Plugin {
	routeRequests()
	return native.New(updater.Options{Interval: interval, DisableAutomaticChecks: !automatic})
}

// route is the Route of SetRoute, nil before: the system's proxy.
var route atomic.Pointer[Route]

// routeRequests sends the updater's requests through route. MyGo's
// updater has no option for its client: it uses netproxy's shared
// transport, whose Proxy and DialContext are set here once, before any
// request, and read route at each one.
func routeRequests() {
	t := netproxy.Transport()
	dial := t.DialContext
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		if r := route.Load(); r != nil {
			return r.Proxy(req)
		}
		return netproxy.Proxy(req)
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if r := route.Load(); r != nil {
			return r.Dial(ctx, network, addr)
		}
		return dial(ctx, network, addr)
	}
}

// MygoBackend is the Backend of the app, over the plugin Plugin returns,
// which must be used.
type MygoBackend struct{}

func (MygoBackend) Enabled() bool        { return mygo.Updater.Enabled() }
func (MygoBackend) Check()               { updater.CheckForUpdates() }
func (MygoBackend) SetAutomatic(on bool) { updater.SetAutomaticChecks(on) }
func (MygoBackend) SetRoute(r Route) {
	// Connections kept open went the old way.
	route.Store(&r)
	netproxy.Transport().CloseIdleConnections()
}
func (MygoBackend) Available() string               { return updater.AvailableVersion() }
func (MygoBackend) OnChange(fn func()) (off func()) { return updater.OnChange(fn) }
