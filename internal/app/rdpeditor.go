package app

import (
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// rdpSecurities are the security choices of an RDP host, in the editor's
// order.
var rdpSecurities = []string{"", "nla", "tls"}

// rdpForm is the editor's fields for an RDP host.
type rdpForm struct {
	rdpDomain string
	// rdpSecurity is the index of rdpSecurities; rdpCustom whether the
	// desktop has a size of its own rather than the pane's.
	rdpSecurity            int
	rdpCustom              bool
	rdpWidth, rdpHeight    string
	rdpAdmin, rdpClipboard bool
}

// loadRDP fills the RDP fields from h, or the defaults for a new host.
func (f *hostForm) loadRDP(h *assets.Host) {
	f.rdpClipboard, f.rdpWidth, f.rdpHeight = true, "1920", "1080"
	if h == nil || h.RDP == nil {
		return
	}
	o := *h.RDP
	f.rdpDomain, f.rdpAdmin, f.rdpClipboard = o.Domain, o.Admin, !o.NoClipboard
	for i, s := range rdpSecurities {
		if s == o.Security {
			f.rdpSecurity = i
		}
	}
	if o.Width > 0 && o.Height > 0 {
		f.rdpCustom = true
		f.rdpWidth, f.rdpHeight = strconv.Itoa(o.Width), strconv.Itoa(o.Height)
	}
}

// applyRDP puts the RDP fields into h; false with the size not a number.
func (f *hostForm) applyRDP(h *assets.Host) bool {
	if !h.IsRDP() {
		h.RDP = nil
		return true
	}
	o := assets.RDPOptions{
		Domain:      strings.TrimSpace(f.rdpDomain),
		Security:    rdpSecurities[min(max(0, f.rdpSecurity), len(rdpSecurities)-1)],
		Admin:       f.rdpAdmin,
		NoClipboard: !f.rdpClipboard,
	}
	if f.rdpCustom {
		w, err1 := strconv.Atoi(strings.TrimSpace(f.rdpWidth))
		ht, err2 := strconv.Atoi(strings.TrimSpace(f.rdpHeight))
		if err1 != nil || err2 != nil {
			return false
		}
		o.Width, o.Height = w, ht
	}
	h.RDP = &o
	return true
}

// rdpFields are an RDP host's domain and desktop settings.
func (a *App) rdpFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.FieldRdpDomain(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			formTextInput(c.Key("f:rdpdomain"), &f.rdpDomain).Placeholder("CORP").Label(m.FieldRdpDomain()).FillWidth()
			ui.Text(c, m.RdpDomainHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
	ui.Field(c, m.FieldRdpSecurity(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			formSegmented(c.Key("f:rdpsec"), &f.rdpSecurity, false, m.RdpSecurityAuto(), "NLA", "TLS").Label(m.FieldRdpSecurity())
			ui.Text(c, m.RdpSecurityHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
	ui.Field(c, m.FieldRdpResolution(), func() {
		ui.Column(c).Gap(6).FillWidth().Children(func() {
			size := 0
			if f.rdpCustom {
				size = 1
			}
			if formSegmented(c.Key("f:rdpsize"), &size, false, m.RdpResolutionFit(), m.RdpResolutionCustom()).Label(m.FieldRdpResolution()).Changed() {
				f.rdpCustom = size == 1
			}
			if f.rdpCustom {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					formTextInput(c.Key("f:rdpw"), &f.rdpWidth).Label(m.RdpWidth()).Width(80)
					ui.Text(c, "×").FontSize(theme.Footnote).TextColor(tok.TextMuted)
					formTextInput(c.Key("f:rdph"), &f.rdpHeight).Label(m.RdpHeight()).Width(80)
				})
			} else {
				ui.Text(c, m.RdpResolutionFitHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
			}
		})
	})
	ui.Field(c, "", func() {
		ui.Column(c).Gap(6).Children(func() {
			formCheckbox(c.Key("f:rdpclip"), &f.rdpClipboard, m.RdpClipboard())
			formCheckbox(c.Key("f:rdpadmin"), &f.rdpAdmin, m.RdpAdminSession())
			ui.Text(c, m.RdpAdminSessionHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}
