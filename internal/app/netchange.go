package app

import (
	"context"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/session"
)

// When the network changes under the SSH connections (another Wi-Fi
// network, a VPN turned on or off, a wake from sleep; see netwatch), a
// connection on the old path may hang until TCP gives up. Told of the
// change (Platform.WatchNetwork), the app checks each connection at once
// (session.Pool.Probe): those that do not answer are dropped, and their
// panes and port forwards go through the reconnect they would have gone
// through later, or show the connection lost when automatic reconnection
// is off. Those that answer are left alone. Telnet, serial and local
// terminals are not touched.

// watchNetwork starts telling the app of network changes, once.
func (a *App) watchNetwork() {
	if a.p.WatchNetwork == nil || a.netStop != nil {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	a.netStop = stop
	go a.p.WatchNetwork(ctx, a.networkChanged)
}

// networkChanged is told that the network changed, on any goroutine.
func (a *App) networkChanged() {
	a.post(a.retryOnNetwork)
	if a.probing.Swap(true) {
		return // a check is under way
	}
	go func() {
		defer a.probing.Store(false)
		a.pool.Probe(session.DefaultProbeTimeout)
	}()
}

// retryOnNetwork has the SSH sessions waiting to reconnect try now: the
// network they lost may be back.
func (a *App) retryOnNetwork() {
	for _, c := range a.conns {
		if c.s != nil && retryNow(c.host, c.s.Status().State) {
			c.s.Reconnect()
		}
	}
}

// retryNow says whether a session on h in state st tries to
// reconnect at once after the network changed: an SSH one waiting for
// its next attempt.
func retryNow(h assets.Host, st session.State) bool {
	return hasSSH(h) && st == session.Reconnecting
}
