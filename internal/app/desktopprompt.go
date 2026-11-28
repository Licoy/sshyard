package app

import (
	"errors"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/rdp"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// desktopCertAsk is a remote desktop's certificate, unknown or changed,
// the user is asked to trust.
type desktopCertAsk struct {
	conn int
	err  *rdp.CertError
}

// promptable reports whether a typed secret answers a failure of kind k.
func promptable(k session.Kind) bool {
	switch k {
	case session.KindNoCredentials, session.KindAuth, session.KindPassphrase, session.KindLocalKey:
		return true
	}
	return false
}

// checkDesktopFailure opens a prompt for remote desktop c, failed for
// want of what the user can give: a password (its own or a jump host's),
// trust in its certificate or in a jump host's key. It reports whether it
// opened one.
func (a *App) checkDesktopFailure(c *conn, m locales.Messages) bool {
	st := c.rd.s.Status()
	if st.State != rdp.Failed || st.Err == nil || st.Err == c.handled {
		return false
	}
	c.handled = st.Err
	var ce *rdp.CertError
	if errors.As(st.Err, &ce) {
		a.certAsk, a.askingCert = &desktopCertAsk{conn: c.id, err: ce}, true
		return true
	}
	kind := desktopFailed(st.Err)
	if kind == session.KindHostUnknown {
		var u *session.UnknownHostError
		if errors.As(st.Err, &u) {
			a.hostKey, a.askingKey = &hostKeyAsk{conn: c.id, err: u, err0: st.Err}, true
			return true
		}
		return false
	}
	if !promptable(kind) {
		return false
	}
	h := a.failedHost(c.host, st.Err)
	key, pass := a.promptKeyFor(h, kind, st.Err)
	if key == "" {
		return false
	}
	p := &credPrompt{conn: c.id, host: h, key: key, passphrase: pass}
	a.mu.Lock()
	if gen, ok := c.tried[key]; ok && gen < a.typedGen[key] && a.typed[key] != "" {
		// It failed with what was typed before the latest answer: try
		// that one instead of asking again.
		a.mu.Unlock()
		a.reconnectDesktop(c)
		return false
	}
	if kind == session.KindAuth {
		p.err = a.desktopErrText(m, c.host, st.Err)
	}
	delete(a.typed, key)
	a.mu.Unlock()
	for _, o := range a.sameSecretFailures(c, key) {
		if o.rd != nil {
			o.handled = o.rd.s.Status().Err
		} else if o.s != nil {
			o.handled = o.s.Status().Err
		}
	}
	a.prompt, a.prompting = p, true
	return true
}

// TrustDesktopCert trusts the certificate asked about and connects again.
func (a *App) TrustDesktopCert() error {
	k := a.certAsk
	a.certAsk, a.askingCert = nil, false
	if k == nil || a.rdpCerts == nil {
		return nil
	}
	if err := a.rdpCerts.Trust(k.err.Addr, k.err.Fingerprint); err != nil {
		return err
	}
	if c := a.conns[k.conn]; c != nil && c.rd != nil {
		a.focusReq = c.id
		a.reconnectDesktop(c)
	}
	return nil
}

// desktopCertView is the sheet asking to trust a remote desktop's
// certificate: one never seen, or one that is not the one trusted before.
func (a *App) desktopCertView(c *ui.Context, m locales.Messages) {
	k := a.certAsk
	if k == nil {
		return
	}
	name := k.err.Addr
	if cn := a.conns[k.conn]; cn != nil {
		name = cn.host.Label()
	}
	changed := k.err.Known != ""
	tok := currentTokens(c)
	sheet(ui.Modal(c, &a.askingCert, func() {
		ui.Column(c).Gap(14).Width(460).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				col := tok.Warning
				title := m.RdpCertUnknownTitle()
				if changed {
					col, title = tok.Danger, m.RdpCertChangedTitle()
				}
				tile(c, icons.HostKey, col, 36)
				sheetTitle(c, title)
			})
			text := m.RdpCertUnknown(a.pv(name))
			if changed {
				text = m.RdpCertChanged(a.pv(name))
			}
			ui.Text(c, text).FontSize(theme.Footnote)
			ui.Column(c).Gap(4).Padding(10, 12).Radius(theme.RadiusControl).Background(tok.Fill).FillWidth().Children(func() {
				if k.err.Subject != "" {
					ui.Text(c, m.RdpCertSubject(k.err.Subject)).FontSize(theme.Caption).TextColor(tok.TextMuted)
				}
				if !k.err.NotAfter.IsZero() {
					ui.Text(c, m.RdpCertExpires(k.err.NotAfter.Local().Format("2006-01-02"))).FontSize(theme.Caption).TextColor(tok.TextMuted)
				}
				if changed {
					ui.Text(c, m.RdpCertSaved(k.err.Known)).FontSize(theme.Caption).TextColor(tok.TextMuted).Selectable()
				}
				ui.Text(c, m.RdpCertOffered(k.err.Fingerprint)).FontSize(theme.Caption).FontWeight(600).Selectable()
			})
			ui.Text(c, m.RdpCertVerifyHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
			ui.Row(c).Gap(8).Margin(4, 0, 0, 0).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.askingCert = false
				}
				label := m.TrustAndConnect()
				if changed {
					label = m.TrustNewKeyAndConnect()
				}
				if pill(c, label, !changed, changed, false) {
					if err := a.TrustDesktopCert(); err != nil {
						c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
					}
				}
			})
		})
	}), tok)
	if !a.askingCert {
		a.certAsk = nil
	}
}
