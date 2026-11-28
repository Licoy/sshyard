package app

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// knowsHostKey reports whether known_hosts has a key for h.
func (a *App) knowsHostKey(h assets.Host) bool {
	if h.IsRDP() {
		if a.rdpCerts == nil {
			return false
		}
		_, ok := a.rdpCerts.Known(h.Addr())
		return ok
	}
	return a.known != nil && h.SSH() && len(a.known.For(h.Addr())) > 0
}

// ForgetHostKey removes the keys known_hosts has for host id, so its next
// connection asks to trust its key again.
func (a *App) ForgetHostKey(id string) error {
	h, ok := a.store.Get(id)
	if ok && h.IsRDP() {
		if a.rdpCerts == nil {
			return nil
		}
		return a.rdpCerts.Forget(h.Addr())
	}
	if !ok || a.known == nil {
		return nil
	}
	return a.known.Forget(h.Addr())
}

// forgetHostKey is the host menu's Forget Host Key.
func (a *App) forgetHostKey(h assets.Host) {
	err := a.ForgetHostKey(h.ID)
	a.toast(func(m locales.Messages) ui.Toast {
		if err != nil {
			return ui.Toast{Title: knownHostsErrorText(m, err), Type: "error"}
		}
		return ui.Toast{Title: m.HostKeyForgotten(a.pv(h.Label()))}
	})
}

// knownKeyHosts are the labels of the saved hosts each line of
// known_hosts has a key for, by line: hashed lines name them only so.
func (a *App) knownKeyHosts() map[int][]string {
	out := map[int][]string{}
	for _, h := range a.store.List() {
		if !h.SSH() {
			continue
		}
		for _, k := range a.known.For(h.Addr()) {
			out[k.Line] = append(out[k.Line], h.Label())
		}
	}
	return out
}

// knownHostsPage lists the host keys SSHYard trusts, each of which can be
// removed, so a host whose key changed for good can be trusted again.
func (a *App) knownHostsPage(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	if a.known == nil {
		return
	}
	path := a.known.Path()
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		g.row(m.KnownHostsFile(a.homePath(path)), "", func() {
			if a.p.OpenPath != nil && iconButton(c, "kh:folder", icons.OpenFile, m.OpenDataFolder()) {
				if err := a.p.OpenPath(filepath.Dir(path)); err != nil {
					c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
				}
			}
		})
	})
	keys := a.known.List()
	if len(keys) == 0 {
		ui.Text(c, m.KnownHostsEmpty()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
		return
	}
	formTextInput(c.Key("kh:search"), &a.khQuery).Label(m.SearchHosts()).Placeholder(m.SearchHosts()).FillWidth()
	used := a.knownKeyHosts()
	q := strings.ToLower(strings.TrimSpace(a.khQuery))
	shown := 0
	a.settingsSection(c, tok, nil, "", func(g *settingsGroup) {
		for _, k := range keys {
			title := strings.Join(k.Hosts, ", ")
			if k.Hashed() {
				title = m.KnownHostsHashed()
			}
			note := k.Key.Type() + " · " + k.Fingerprint
			if len(used[k.Line]) > 0 {
				note += " · " + m.KnownHostsUsedBy(strings.Join(used[k.Line], ", "))
			}
			if q != "" && !strings.Contains(strings.ToLower(title+" "+note), q) {
				continue
			}
			shown++
			g.row(a.pv(title), a.pv(note), func() {
				if (IconButton{Icon: icons.Delete, Label: m.RemoveHostKey(), Size: Small, Danger: true}).Clicked(c, "kh:rm:"+strconv.Itoa(k.Line)) {
					a.removeKnownKey(k)
				}
			})
		}
		if shown == 0 {
			g.row(m.KnownHostsNoMatch(), "", func() {})
		}
	})
}

// removeKnownKey removes one line of known_hosts, shown on its page.
func (a *App) removeKnownKey(k session.KnownKey) {
	if err := a.known.Remove(k); err != nil {
		a.toast(func(m locales.Messages) ui.Toast {
			return ui.Toast{Title: knownHostsErrorText(m, err), Type: "error"}
		})
	}
}
