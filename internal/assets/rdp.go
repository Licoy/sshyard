package assets

import (
	"slices"
	"strings"
)

// RDPOptions are the saved settings of an RDP desktop session.
type RDPOptions struct {
	Domain      string `json:"domain,omitempty"`
	Security    string `json:"security,omitempty"` // Empty negotiates automatically.
	Width       int    `json:"width,omitempty"`    // Both dimensions zero use the pane size.
	Height      int    `json:"height,omitempty"`
	Admin       bool   `json:"admin,omitempty"`
	NoClipboard bool   `json:"noClipboard,omitempty"`
}

func (h Host) copyRDP() Host {
	if h.RDP != nil {
		options := *h.RDP
		h.RDP = &options
	}
	return h
}

func cloneHosts(hosts []Host) []Host {
	out := slices.Clone(hosts)
	for i := range out {
		out[i] = out[i].copyRDP()
	}
	return out
}

func (h *Host) normalizeRDP() {
	h.Serial, h.NewLine, h.LocalEcho, h.Backspace = nil, "", false, ""
	h.Encoding, h.Term, h.OSC52 = "", "", ""
	h.TransfersOff, h.SessionLog = false, false
	var options RDPOptions
	if h.RDP != nil {
		options = *h.RDP
		options.Domain = strings.TrimSpace(options.Domain)
		options.Security = strings.TrimSpace(options.Security)
	}
	h.RDP = &options
}

func (o *RDPOptions) validate() error {
	if o == nil {
		return &FieldError{"rdp", "required"}
	}
	switch o.Security {
	case "", "nla", "tls":
	default:
		return &FieldError{"security", "unknown"}
	}
	if o.Width == 0 && o.Height == 0 {
		return nil
	}
	if o.Width < 200 || o.Width > 8192 {
		return &FieldError{"width", "out of range"}
	}
	if o.Height < 200 || o.Height > 8192 {
		return &FieldError{"height", "out of range"}
	}
	return nil
}
