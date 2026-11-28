package app

import (
	"context"
	"errors"
	"image"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/rdp"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// An RDP host opens one kind of pane, its remote desktop: no terminal,
// files, metrics or assistant. It is reached directly, through its
// proxy, or through its SSH jump hosts, the last of which opens a
// direct-tcpip channel to it.

// errDesktopOnly is opening anything but a remote desktop on an RDP host,
// or a remote desktop on another host.
var errDesktopOnly = errors.New("app: an RDP host opens a remote desktop only")

// desktop is a remote desktop pane's session and what draws it.
type desktop struct {
	s *rdp.Session
	h assets.Host

	// started is whether the session was asked to start; begun whether
	// it did, once the pane knew its size (an RDP desktop connects at the
	// size it is shown at).
	started, begun bool

	// frames are two buffers the desktop is copied into in turn, vers
	// what version of it each holds: Bitmap.Update keeps the one it was
	// last given until the next.
	frames [2]*image.RGBA
	vers   [2]uint64
	cur    int
	bm     *ui.Bitmap
	shown  uint64

	ptr   rdp.Pointer
	ptrBm *ui.Bitmap

	// view is where the desktop is drawn in the pane's body (DIPs), and
	// scale the window's pixels a DIP, as the body last drew.
	view  ui.Rect
	scale float32
	// asked is the size last asked of the server: width, height, scale.
	asked [3]int

	// at is the pointer over the desktop, in its pixels; over whether it
	// is over it.
	at   image.Point
	over bool
	// held are the buttons held down on the desktop.
	held map[rdp.Button]bool
	// focused is whether the desktop had the keyboard last frame;
	// composing whether an input method is composing text.
	focused, composing bool
	// clip is the local clipboard text last offered to the remote side.
	clip string
	// composing is whether an input method composes text, composed
	// whether what it commits next goes as text.
	composed bool
	// winPending is the Command key held on macOS, not yet sent as the
	// Windows key; winDown whether it was.
	winPending, winDown bool
	// wheelX and wheelY are the wheel units scrolled not yet sent.
	wheelX, wheelY float32
	// hidden is whether the server was told the desktop is not seen.
	hidden bool
	// cfg is the session's configuration for a desktop of w×h pixels at
	// scale percent.
	cfg func(w, h, scale int) rdp.Config
}

// newDesktop makes the remote desktop of pane c on h; it connects once
// started and shown.
func (a *App) newDesktop(c *conn, h assets.Host) *desktop {
	d := &desktop{h: h, held: map[rdp.Button]bool{}}
	d.cfg = func(w, ht, scale int) rdp.Config { return a.desktopConfig(c, d.h, w, ht, scale) }
	d.s = rdp.New(d.cfg(0, 0, 0), rdp.Options{AutoReconnect: a.settings.AutoReconnect})
	d.s.OnChange = func() { c.wake.ask(func() { a.invalidateWin(int(c.wid.Load())) }, afterFunc) }
	d.s.OnClipboard = func(text string) {
		a.post(func() {
			if a.conns[c.id] != c {
				return
			}
			d.clip = text
			a.svc.WriteClipboard(text)
		})
	}
	return d
}

// rdpOptions are h's remote desktop settings, the defaults when none.
func rdpOptions(h assets.Host) assets.RDPOptions {
	if h.RDP == nil {
		return assets.RDPOptions{}
	}
	return *h.RDP
}

// desktopConfig is how pane c reaches h's desktop, asking for a desktop
// of w×h pixels at scale percent.
func (a *App) desktopConfig(c *conn, h assets.Host, w, ht, scale int) rdp.Config {
	o := rdpOptions(h)
	if o.Width > 0 && o.Height > 0 {
		w, ht = o.Width, o.Height
	}
	cfg := rdp.Config{
		Addr:       h.Addr(),
		Dial:       a.desktopDial(h, c),
		Domain:     o.Domain,
		User:       a.loginUser(h),
		Security:   rdp.Security(o.Security),
		Width:      w,
		Height:     ht,
		Scale:      scale,
		Admin:      o.Admin,
		Clipboard:  !o.NoClipboard,
		ClientName: clientName(),
		Password: func(context.Context) (string, error) {
			if h.Auth == assets.AuthNone {
				return "", nil
			}
			cr, err := a.credentials(c, h)
			if err != nil {
				return "", err
			}
			return cr.Password, nil
		},
	}
	if certs := a.rdpCerts; certs != nil {
		cfg.VerifyCert = certs.Verify
	}
	return cfg
}

// clientName is this computer's name, as the server lists the session's
// client: its first label, as NetBIOS names are short.
func clientName() string {
	name, _ := os.Hostname()
	name, _, _ = strings.Cut(name, ".")
	return name
}

// desktopDial dials h for pane c: through its proxy, or through its jump
// hosts, on the shared SSH connection of the last of them.
func (a *App) desktopDial(h assets.Host, c *conn) func(ctx context.Context, network, addr string) (net.Conn, error) {
	direct := a.p.Dial
	if direct == nil {
		direct = (&net.Dialer{}).DialContext
	}
	if len(h.JumpChain) == 0 {
		if px, ok := a.store.Proxy(h.ProxyID); ok {
			d := a.proxyDialer(px)
			d.Forward = direct
			return d.DialContext
		}
		return direct
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		cfg := a.hostConfig(h, c, nil)
		n := len(cfg.Jumps)
		if n == 0 {
			// A bad chain: hostConfig's Dial says why.
			return cfg.Dial(ctx, network, addr)
		}
		last := cfg.Jumps[n-1]
		cfg.Name, cfg.ID, cfg.Addr, cfg.User = last.Name, last.ID, last.Addr, last.User
		cfg.Credentials, cfg.HostKey = last.Credentials, last.HostKey
		cfg.Jumps = cfg.Jumps[:n-1]
		cfg.Key = "rdp|" + a.poolKey(h)
		client, release, err := a.pool.Acquire(ctx, cfg.Key, cfg)
		if err != nil {
			// Each hop is a jump host of the desktop's.
			var he *session.HopError
			if errors.As(err, &he) {
				he.Jump = true
				return nil, err
			}
			return nil, &session.HopError{Index: n - 1, ID: last.ID, Name: last.Name, Jump: true, Err: err}
		}
		conn, err := client.DialContext(ctx, network, addr)
		if err != nil {
			release()
			return nil, err
		}
		return &releasedConn{Conn: conn, release: release}, nil
	}
}

// releasedConn is a channel through a shared SSH connection, which it
// lets go when closed.
type releasedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *releasedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// start connects the desktop, at once when the pane has drawn its size,
// else as soon as it has.
func (d *desktop) start() {
	d.started = true
	if d.asked[0] > 0 && !d.begun {
		d.begun = true
		d.s.Start()
	}
}

// reconnect drops the connection and connects anew, with the host's
// latest settings.
func (a *App) reconnectDesktop(c *conn) {
	d := c.rd
	if d == nil {
		return
	}
	c.handled = nil
	if h, ok := a.host(c.host.ID); ok && isDesktop(h) {
		c.host, d.h = h, h
	}
	d.s.SetConfig(d.cfg(d.asked[0], d.asked[1], d.asked[2]))
	if !d.begun {
		d.start()
		return
	}
	d.s.Reconnect()
}

func (d *desktop) close() { d.s.Close() }

// desktopState is remote desktop state st as a session's, for what shows
// any pane's: tabs, the status bar, the sidebar.
func desktopState(st rdp.State) session.State {
	switch st {
	case rdp.Connecting:
		return session.Connecting
	case rdp.Connected:
		return session.Connected
	case rdp.Reconnecting:
		return session.Reconnecting
	case rdp.Disconnected:
		return session.Disconnected
	case rdp.Failed:
		return session.Failed
	}
	return session.Idle
}

// paneStatus is the state of pane cn's connection and what says it: its
// session's or its remote desktop's; ok is false for a pane with neither.
func (a *App) paneStatus(m locales.Messages, cn *conn, now time.Time) (session.State, string, bool) {
	switch {
	case cn == nil:
	case cn.s != nil:
		st := cn.s.Status()
		return st.State, a.statusText(m, a.liveHost(cn.host), st, now), true
	case cn.rd != nil:
		st := cn.rd.s.Status()
		return desktopState(st.State), a.desktopStatusText(m, cn.host, st), true
	}
	return session.Idle, "", false
}

// desktopFailed reports a remote desktop's failure the user can answer,
// as a session kind: a missing or refused password, or one of its jump
// hosts' failures.
func desktopFailed(err error) session.Kind {
	if err == nil {
		return session.KindOther
	}
	var he *session.HopError
	if errors.As(err, &he) {
		return session.Classify(err)
	}
	if errors.Is(err, session.ErrNoCredentials) {
		return session.KindNoCredentials
	}
	if k, r := rdp.Classify(err); k == rdp.KindAuth && r == rdp.ReasonLogonFailure {
		return session.KindAuth
	}
	return session.KindOther
}

// desktopStatusText says how remote desktop st is doing.
func (a *App) desktopStatusText(m locales.Messages, h assets.Host, st rdp.Status) string {
	switch st.State {
	case rdp.Idle, rdp.Connecting:
		switch st.Step {
		case rdp.StepTLS:
			return m.RdpStepSecuring()
		case rdp.StepAuth:
			return m.RdpStepAuth()
		case rdp.StepLicense:
			return m.RdpStepLicense()
		case rdp.StepCapabilities, rdp.StepReady:
			return m.RdpStepDesktop()
		}
		return m.StateConnecting()
	case rdp.Connected:
		return m.StateConnected()
	case rdp.Reconnecting:
		return m.RdpReconnecting(strconv.Itoa(st.Attempt))
	}
	if st.Err != nil {
		return a.desktopErrText(m, h, st.Err)
	}
	return m.StateDisconnected()
}

// desktopErrText explains why a remote desktop failed or ended.
func (a *App) desktopErrText(m locales.Messages, h assets.Host, err error) string {
	var he *session.HopError
	if errors.As(err, &he) {
		return a.errText(m, h, err)
	}
	if errors.Is(err, session.ErrNoCredentials) {
		return m.RdpPasswordNeeded()
	}
	var ce *rdp.CertError
	if errors.As(err, &ce) {
		return m.RdpCertRefused()
	}
	kind, reason := rdp.Classify(err)
	switch reason {
	case rdp.ReasonLogonFailure:
		return m.RdpLogonFailure()
	case rdp.ReasonAccountLocked:
		return m.RdpAccountLocked()
	case rdp.ReasonAccountDisabled:
		return m.RdpAccountDisabled()
	case rdp.ReasonAccountRestricted:
		return m.RdpAccountRestricted()
	case rdp.ReasonAccountExpired:
		return m.RdpAccountExpired()
	case rdp.ReasonPasswordExpired:
		return m.RdpPasswordExpired()
	case rdp.ReasonPasswordMustChange:
		return m.RdpPasswordMustChange()
	case rdp.ReasonLogonTypeNotGranted:
		return m.RdpNotAllowed()
	case rdp.ReasonLoggedOff:
		return m.RdpLoggedOff()
	case rdp.ReasonIdleTimeout:
		return m.RdpIdleTimeout()
	case rdp.ReasonLogonTimeout:
		return m.RdpLogonTimeout()
	case rdp.ReasonReplaced:
		return m.RdpReplaced()
	case rdp.ReasonAdminEnded:
		return m.RdpAdminEnded()
	case rdp.ReasonServerDenied:
		return m.RdpServerDenied()
	case rdp.ReasonProtocol:
		return m.RdpProtocolError()
	}
	switch kind {
	case rdp.KindAuth:
		return m.RdpLogonFailure()
	case rdp.KindSecurity:
		return m.RdpSecurityFailed()
	case rdp.KindLicense:
		return m.RdpLicenseFailed()
	case rdp.KindServer:
		return m.RdpServerEnded()
	}
	return a.errText(m, h, err)
}
