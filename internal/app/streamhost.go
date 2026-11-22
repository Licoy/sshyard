package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/serial"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// Telnet and serial hosts have a terminal only: there is no SSH
// connection under them, so no files, metrics, port forwards or jump
// hosts. Their assistant types into the terminal. A serial port opens
// once, so a host on it has one terminal pane.

// errTerminalOnly is opening anything but a terminal, or an assistant
// that types into it, on a Telnet or serial host.
var errTerminalOnly = errors.New("app: this host opens terminals only")

// serialInUseError is opening a serial port a pane has open already.
type serialInUseError struct{ pane *conn }

func (e *serialInUseError) Error() string {
	return "app: " + e.pane.host.Serial.Device + " is open in another pane"
}

// hasSSH reports whether h is reached over SSH: not this computer, nor a
// Telnet or serial host.
func hasSSH(h assets.Host) bool { return !isLocal(h.ID) && h.SSH() }

// isStream reports whether h is a Telnet or serial host.
func isStream(h assets.Host) bool { return !isLocal(h.ID) && !h.SSH() }

// serialPane is the terminal pane that has h's serial port open, nil for
// none.
func (a *App) serialPane(h assets.Host) *conn {
	if h.Protocol != assets.ProtoSerial || h.Serial == nil {
		return nil
	}
	for _, c := range a.conns {
		if c.kind == layout.Terminal && c.s != nil && c.host.Protocol == assets.ProtoSerial && c.host.Serial != nil &&
			strings.EqualFold(c.host.Serial.Device, h.Serial.Device) {
			return c
		}
	}
	return nil
}

// streamSession is the configuration and options of a Telnet or serial
// host's terminal, logging in with what is saved or typed for c.
func (a *App) streamSession(h assets.Host, c *conn, opts *session.Options) session.Config {
	cfg := session.Config{
		Name: h.Label(), ID: h.ID, Addr: h.Addr(), User: h.User, Dial: a.p.Dial,
		Credentials: func(context.Context) (session.Credentials, error) { return a.credentials(c, h) },
	}
	if h.Protocol == assets.ProtoSerial {
		opts.Open = session.Serial(h.Serial.Config())
	} else {
		if px, ok := a.store.Proxy(h.ProxyID); ok {
			cfg.Proxy = a.proxyDialer(px)
		}
		opts.Open = session.Telnet(cfg)
	}
	opts.Login, opts.NewLine, opts.LocalEcho = h.LogsIn(), h.NewLine.Bytes(), h.LocalEcho
	opts.BackspaceBS = h.Backspace == assets.BackspaceBS
	return cfg
}

// serialErrText explains a serial port that would not open, "" for
// another error.
func serialErrText(m locales.Messages, h assets.Host, err error) string {
	device := ""
	if h.Serial != nil {
		device = h.Serial.Device
	}
	switch {
	case errors.Is(err, serial.ErrNotFound):
		return m.ErrSerialNotFound(device)
	case errors.Is(err, serial.ErrBusy):
		return m.ErrSerialBusy(device)
	case errors.Is(err, serial.ErrPermission):
		return m.ErrSerialPermission(device)
	case errors.Is(err, serial.ErrDisconnected):
		return m.ErrSerialDisconnected(device)
	case errors.Is(err, serial.ErrInvalidConfig):
		return m.ErrSerialSettings(device)
	}
	return ""
}

// canBreak reports whether the terminal of cn can send a Break now.
func canBreak(cn *conn) bool {
	return cn != nil && cn.kind == layout.Terminal && cn.s != nil && cn.s.CanBreak()
}

// sendBreak sends a Break on the terminal of cn; a serial port holds the
// line for a moment, so off the UI thread.
func (a *App) sendBreak(cn *conn) {
	if !canBreak(cn) {
		return
	}
	s := cn.s
	go s.Break()
}

// protocolName names how a host of p is reached.
func protocolName(m locales.Messages, p assets.Protocol) string {
	switch p {
	case assets.ProtoTelnet:
		return m.ProtocolTelnet()
	case assets.ProtoSerial:
		return m.ProtocolSerial()
	}
	return m.ProtocolSSH()
}

// protocols are the editor's protocol choices, in its order.
var protocols = []assets.Protocol{assets.ProtoSSH, assets.ProtoTelnet, assets.ProtoSerial}

// parityChoices are the parity settings and their labels, in order.
func parityChoices(m locales.Messages) ([]string, []serial.Parity) {
	return []string{m.ParityNone(), m.ParityOdd(), m.ParityEven(), m.ParityMark(), m.ParitySpace()},
		[]serial.Parity{serial.ParityNone, serial.ParityOdd, serial.ParityEven, serial.ParityMark, serial.ParitySpace}
}

// newLineChoices are what Return can send, as the editor offers them.
var newLineChoices = []assets.NewLine{assets.NewLineCR, assets.NewLineLF, assets.NewLineCRLF}

// newLineLabels name newLineChoices: control characters, the same in
// every language.
var newLineLabels = []string{"CR", "LF", "CR LF"}

// backspaceChoices are what Backspace can send, as the editor offers
// them.
var backspaceChoices = []assets.Backspace{assets.BackspaceDEL, assets.BackspaceBS}

// backspaceLabels name backspaceChoices: control characters, the same in
// every language.
var backspaceLabels = []string{"DEL (^?)", "BS (^H)"}

// streamFacts are the connection card of a Telnet or serial host; or
// stands in a dash for an empty value.
func (a *App) streamFacts(c *ui.Context, m locales.Messages, tok theme.Tokens, h assets.Host, proxy string, panes int, or func(string) string) {
	nl := newLineLabels[max(0, slices.Index(newLineChoices, h.NewLine))]
	if h.Protocol == assets.ProtoSerial && h.Serial != nil {
		sc := h.Serial.Config().Normalized()
		facts(c, tok, "",
			m.FieldProtocol(), protocolName(m, h.Protocol), m.FieldDevice(), sc.Device,
			m.FieldBaud(), strconv.Itoa(sc.Baud), m.SerialSettings(), sc.Frame(),
			m.FieldNewLine(), nl, m.FieldGroup(), or(h.Group),
			m.OpenPanes(), strconv.Itoa(panes))
	} else {
		facts(c, tok, "",
			m.FieldProtocol(), protocolName(m, h.Protocol), m.SysAddress(), a.pv(h.Address),
			m.FieldPort(), strconv.Itoa(h.Port), m.FieldUser(), or(h.User),
			m.FieldProxy(), proxy, m.FieldNewLine(), nl,
			m.FieldGroup(), or(h.Group), m.OpenPanes(), strconv.Itoa(panes))
	}
	ui.Column(c).Gap(2).FillWidth().Children(func() {
		ui.Text(c, m.FieldAuth()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		ui.Text(c, a.authText(m, h)).FontSize(theme.Footnote)
	})
}

// streamAuthChoices are the logins of a Telnet or serial host: by hand,
// a saved or asked password, or a password credential.
func (a *App) streamAuthChoices(m locales.Messages) []authChoice {
	out := []authChoice{{m.AuthManual(), assets.AuthNone, ""}, {m.CredKindPassword(), assets.AuthPassword, ""}, {m.AuthAsk(), assets.AuthAsk, ""}}
	for _, cr := range a.store.Credentials() {
		if cr.Kind == assets.CredPassword {
			out = append(out, authChoice{cr.Name + " · " + m.CredKindPassword(), assets.AuthCredential, cr.ID})
		}
	}
	return out
}

// protoAuthChoices are the logins the editor offers a host of p.
func (a *App) protoAuthChoices(m locales.Messages, p assets.Protocol) []authChoice {
	if p == assets.ProtoSSH {
		return a.hostAuthChoices(m)
	}
	return a.streamAuthChoices(m)
}
