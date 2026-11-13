package app

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/keys"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/vt"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// hostForm keeps the editor fields and unsaved host-owned secrets in memory.
type hostForm struct {
	isNew               bool
	orig                assets.Host
	name, address, port string
	user, group         string
	newGroup            string
	addingGroup         bool
	// icon and color are the host's look as chosen (Host.Icon and
	// Host.Color), "" for those picked from the address; emoji is the
	// icon field's text
	icon, color, emoji string
	// auth is the label chosen in the auth select (see authChoices),
	// "" until the editor first shows
	secret    credForm
	auth      string
	favorite  bool
	cwdFollow bool
	// metricsIdx is 0 follow, 1 on, 2 off.
	metricsIdx int
	// proxy is the ID chosen in the proxy select ("" none); jumps the
	// jump chain's host IDs, first hop first; jumpPick the add select
	proxy    string
	jumps    []string
	jumpPick string
	jumpList ui.ListState
	netOpen  bool
	// termDir, remoteDir and localDir are the host's default folders
	// (Host.TerminalDir, RemoteDir, LocalDir); dirsOpen shows them.
	termDir, remoteDir, localDir string
	dirsOpen                     bool
	// tmux is the tmux session the host's terminals attach to (Host.Tmux)
	tmux string
	// encoding is the output encoding of the host's new terminals
	// (Host.Encoding)
	encoding vt.OutputEncoding
	// term is the TERM of the host's new terminals (Host.Term), ""
	// the setting's; termCustom edits it as text rather than a preset.
	term       string
	termCustom bool
	// sessionLog logs the host's terminals as they connect
	// (Host.SessionLog)
	sessionLog bool
	// sftpSudo runs the host's SFTP through sudo (Host.SFTPSudo)
	sftpSudo bool
	// transfers lets the host's terminals take ZMODEM and trzsz
	// transfers (not Host.TransfersOff)
	transfers bool
	// osc52 is the override's label once Advanced is open; osc52Code is
	// the saved policy ("", allow, ask, deny), so a save before that
	// keeps it. advOpen shows Advanced.
	osc52, osc52Code string
	advOpen          bool
	err              string
	// AI permission override. aiIdx 0 inherits. fullOK is set once the
	// full-access sheet has been confirmed for this edit.
	aiIdx, aiPrev int
	roots, sens   string
	aiOpen        bool
	fullOK        bool
	// promote is the temporary host being saved, whose panes then show
	// the new host
	promote string
	streamForm
	renewalForm
}

// authChoice is one option of the editor's auth select.
type authChoice struct {
	label  string
	method assets.AuthMethod
	cred   string
}

// askLabel names the ask login: it tries the keys of this computer first
// when the setting allows.
func (a *App) askLabel(m locales.Messages) string {
	if a.lkOn.Load() {
		return m.AuthAuto()
	}
	return m.AuthAsk()
}

// authChoices are the editor's ways to log in: asking each time, the
// SSH agent, then every credential.
func (a *App) authChoices(m locales.Messages) []authChoice {
	out := []authChoice{{a.askLabel(m), assets.AuthAsk, ""}, {m.AuthAgent(), assets.AuthAgent, ""}}
	for _, cr := range a.store.Credentials() {
		kind := m.CredKindPassword()
		if cr.Kind == assets.CredKey {
			kind = m.CredKindKey()
		}
		out = append(out, authChoice{cr.Name + " · " + kind, assets.AuthCredential, cr.ID})
	}
	return out
}

func (a *App) authLabel(m locales.Messages, h assets.Host) string {
	choices := a.protoAuthChoices(m, h.Protocol)
	for _, ch := range choices {
		if ch.method == h.Auth && ch.cred == h.CredentialID {
			return ch.label
		}
	}
	if !h.SSH() {
		return choices[0].label
	}
	return a.askLabel(m)
}

func (a *App) openEditor(h *assets.Host) {
	if h != nil && h.Auth == assets.AuthKey && !a.unlocked() {
		a.withSecrets(func() { a.openEditor(h) })
		return
	}

	f := &hostForm{isNew: h == nil, port: "22", cwdFollow: true, encoding: vt.EncodingUTF8, transfers: true}
	f.loadStream(h)
	f.loadRenewal(h, time.Now())
	if h != nil {
		f.orig = *h
		f.transfers = !h.TransfersOff
		f.name, f.address, f.port = h.Name, h.Address, strconv.Itoa(h.Port)
		f.user, f.group, f.favorite, f.cwdFollow = h.User, h.Group, h.Favorite, h.CwdFollow
		f.icon, f.color = h.Icon, h.Color
		if !strings.HasPrefix(h.Icon, assets.IconPrefix) {
			f.emoji = h.Icon
		}
		f.metricsIdx = metricsIndex(h.Metrics)
		f.jumps = slices.Clone(h.JumpChain)
		f.proxy = h.ProxyID
		f.netOpen = f.proxy != "" || len(f.jumps) > 0
		f.termDir, f.remoteDir, f.localDir = h.TerminalDir, h.RemoteDir, h.LocalDir
		f.tmux = h.Tmux
		f.sessionLog = h.SessionLog
		f.sftpSudo = h.SFTPSudo
		if e := vt.OutputEncoding(h.Encoding); slices.Contains(vt.OutputEncodings(), e) {
			f.encoding = e
		}
		f.term = h.Term
		f.termCustom = h.Term != "" && !slices.Contains(termPresets, h.Term)
		f.dirsOpen = f.termDir != "" || f.remoteDir != "" || f.localDir != ""
		f.osc52Code = h.OSC52
		f.advOpen = h.OSC52 != ""
		f.aiIdx = hostModeIndex(h.AIPolicy.Mode)
		f.aiPrev = f.aiIdx
		f.roots = strings.Join(h.AIPolicy.WritableRoots, "\n")
		f.sens = strings.Join(h.AIPolicy.Sensitive, "\n")
		f.aiOpen = h.AIPolicy.Mode != "" || f.roots != "" || f.sens != ""
	}
	f.secret.isNew = true
	if h != nil {
		f.secret.isNew = h.Auth != assets.AuthPassword && h.Auth != assets.AuthKey
		f.secret.askPass = h.AskPassphrase
		if h.Auth == assets.AuthKey {
			f.secret.loadCert(h.Certificate)
			if data, err := a.kc.Get(keychain.Host(h.ID, keychain.PrivateKey)); err == nil {
				k, kerr := keys.Import(data, nil)
				if kerr != nil && !errors.Is(kerr, keys.ErrPassphraseNeeded) {
					f.err = keyError(locales.In(a.settings.Language), kerr)
				}
				f.secret.orig = assets.Credential{Kind: assets.CredKey, KeyType: k.Type(), Fingerprint: k.Fingerprint(), Encrypted: k.Encrypted}
			} else if !errors.Is(err, keychain.ErrNotFound) {
				f.err = secretStoreErrorText(locales.In(a.settings.Language), err)
			}
		}
	}
	a.form, a.editing = f, true
}

func osc52Choices(m locales.Messages) (labels, codes []string) {
	return []string{m.Osc52Follow(), m.Osc52Allow(), m.Osc52Ask(), m.Osc52Deny()},
		[]string{"", settings.OSC52Allow, settings.OSC52Ask, settings.OSC52Deny}
}

// syncOSC52Label makes the select's label match the saved code, and
// remembers a label the user already picked so a language change keeps it.
func syncOSC52Label(m locales.Messages, f *hostForm) []string {
	labels, codes := osc52Choices(m)
	if i := slices.Index(labels, f.osc52); i >= 0 {
		f.osc52Code = codes[i]
		return labels
	}
	f.osc52 = labels[0]
	for i, code := range codes {
		if code == f.osc52Code {
			f.osc52 = labels[i]
		}
	}
	return labels
}

// osc52CodeFrom is the policy to save: the label when Advanced has shown
// it, otherwise the code the host already had.
func osc52CodeFrom(m locales.Messages, f *hostForm) string {
	labels, codes := osc52Choices(m)
	if i := slices.Index(labels, f.osc52); i >= 0 {
		return codes[i]
	}
	return f.osc52Code
}

// fieldError is a host field's problem in the user's language.
func fieldError(m locales.Messages, err error) string {
	var fe *assets.FieldError
	if !errors.As(err, &fe) {
		return m.ErrGeneric(secretStoreErrorText(m, err))
	}
	label := map[string]string{"name": m.FieldName(), "address": m.FieldHost(), "port": m.FieldPort(),
		"user": m.FieldUser(), "auth": m.FieldAuth(), "credential": m.FieldAuth(), "device": m.FieldDevice()}[fe.Field]
	switch {
	case fe.Field == "port":
		return m.InvalidPort()
	case fe.Field == "serial":
		return m.InvalidSerialSettings()
	case fe.Field == "term":
		return m.TermTypeInvalid()
	case fe.Field == "jump" && fe.Reason == "not ssh":
		return m.JumpNotSSH()
	case fe.Field == "host" && fe.Reason == "not ssh":
		return m.ForwardNotSSH()
	case fe.Field == "credential" && fe.Reason == "password only":
		return m.CredentialPasswordOnly()
	case fe.Field == "kind" && fe.Reason == "password only":
		return m.CredentialKeepPassword()
	}
	if fe.Reason == "taken" {
		return m.NameTaken()
	}
	return m.RequiredField(label)
}

// SaveForm validates the editor and saves the host.
func (a *App) SaveForm(m locales.Messages) bool {
	f := a.form
	if f == nil {
		return false
	}
	if f.addingGroup && !a.addHostGroup(m, f) {
		return false
	}
	// A serial host has no port: its line is its address.
	port, err := 0, error(nil)
	if f.protocol() != assets.ProtoSerial {
		port, err = strconv.Atoi(strings.TrimSpace(f.port))
		if err != nil || port < 1 || port > 65535 {
			f.err = m.InvalidPort()
			return false
		}
	}
	h := f.orig
	h.Name, h.Address, h.Port, h.User, h.Group = f.name, f.address, port, f.user, f.group
	h.Favorite, h.CwdFollow, h.Metrics = f.favorite, f.cwdFollow, metricsMode(f.metricsIdx)
	h.Icon, h.Color = f.icon, f.color
	f.applyStream(&h)
	f.applyRenewal(&h)
	if f.auth == "" {
		f.auth = a.authLabel(m, h)
	}
	h.Auth, h.CredentialID = assets.AuthAsk, ""
	if !h.SSH() {
		h.Auth = assets.AuthNone
	}
	for _, ch := range a.protoAuthChoices(m, h.Protocol) {
		if ch.label == f.auth {
			h.Auth, h.CredentialID = ch.method, ch.cred
		}
	}
	h.ProxyID = f.proxy
	h.JumpChain = slices.Clone(f.jumps)
	h.TerminalDir, h.RemoteDir, h.LocalDir = f.termDir, f.remoteDir, f.localDir
	h.Tmux = strings.TrimSpace(f.tmux)
	h.Encoding = string(f.encoding)
	h.Term = f.term
	h.SessionLog = f.sessionLog
	h.SFTPSudo = f.sftpSudo
	h.TransfersOff = !f.transfers
	if h.SSH() && h.Tmux != "" && !session.TmuxNameOK(h.Tmux) {
		f.err = m.TmuxNameInvalid()
		return false
	}
	h.OSC52 = osc52CodeFrom(m, f)
	mode := ""
	if f.aiIdx >= 0 && f.aiIdx < 4 {
		mode = []string{"", "ask", "review", "full"}[f.aiIdx]
	}
	if mode == "full" && f.orig.AIPolicy.Mode != "full" && !f.fullOK {
		a.openFull("host", 0)
		return false
	}
	h.AIPolicy.Mode = mode
	h.AIPolicy.WritableRoots = linesOf(f.roots)
	h.AIPolicy.Sensitive = linesOf(f.sens)
	h.Certificate = f.secret.cert
	if h.Auth == assets.AuthKey && f.secret.certProblem() != nil {
		f.err = m.ErrCertMismatch()
		return false
	}
	h.Normalize()
	if err := h.Validate(); err != nil {
		f.err = fieldError(m, err)
		return false
	}
	if !a.unlocked() && hostFormNeedsSecrets(f, h) {
		draft := a.form
		a.withSecrets(func() {
			if a.form == draft {
				a.SaveForm(m)
			}
		})
		return false
	}
	h, err = a.saveHostForm(m, f, h)
	if errors.Is(err, assets.ErrJumpLoop) {
		f.err, f.netOpen = m.ErrJumpLoop(), true
		return false
	}
	if errors.Is(err, errCostAsked) {
		return false
	}
	if err != nil {
		f.err = fieldError(m, err)
		return false
	}
	for _, c := range a.conns {
		if c.host.ID == h.ID {
			a.invalidateAI(c)
			a.refreshMode(c)
		}
	}
	if f.promote != "" {
		a.promote(f.promote, h)
	}
	a.selected = h.ID
	a.form, a.editing = nil, false
	return true
}

// DeleteHost removes a host, unless other hosts jump through it or
// forwards use it (an *assets.InUseError naming them).
func (a *App) DeleteHost(id string) error {
	if err := a.deleteHostSecrets(id); err != nil {
		return err
	}
	if err := a.deleteHostChats(id); err != nil {
		return err
	}
	delete(a.chatList, id)
	a.mu.Lock()
	delete(a.typed, "ask/"+id)
	a.mu.Unlock()
	a.lk.ForgetHost(id)
	if a.selectedHost() == "" {
		a.selected = ""
	}
	return nil
}

// inUseText says what still uses an entry that cannot be deleted.
func (a *App) inUseText(m locales.Messages, err error) string {
	var iu *assets.InUseError
	var kd *keychainDeleteError
	if errors.As(err, &kd) {
		return m.ErrKeychainDelete(secretStoreErrorText(m, kd.err))
	}
	if !errors.As(err, &iu) {
		return m.ErrGeneric(err.Error())
	}
	var names []string
	for _, id := range iu.Hosts {
		if h, ok := a.store.Get(id); ok {
			id = h.Label()
		}
		names = append(names, id)
	}
	for _, id := range iu.Forwards {
		if f, ok := a.store.Forward(id); ok {
			id = f.Label()
		}
		names = append(names, id)
	}
	return m.InUse(strings.Join(names, ", "))
}

func (a *App) editorView(c *ui.Context, m locales.Messages) {
	if a.form != nil {
		f := a.form
		tok := currentTokens(c)
		sheet(ui.Modal(c, &a.editing, func() {
			// The form scrolls between the title and the buttons when the
			// window is too short for it.
			ui.Column(c).Gap(18).Width(480).Shrink(1).MinHeight(0).Children(func() {
				title := m.EditHost()
				switch {
				case f.promote != "":
					title = m.SaveAsHost()
				case f.isNew:
					title = m.NewHost()
				}
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					name := f.name
					if name == "" {
						name = f.address
					}
					hostAvatar(c, tok, f.look(), 36)
					ui.Column(c).Gap(1).Shrink(1).Children(func() {
						sheetTitle(c, title)
						if name != "" {
							ui.Text(c, name).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
						}
					})
				})
				sheetScroll(ui.Scroll(c.Key("f:scroll")).Shrink(1).MinHeight(0)).Children(func() {
					ui.Form(c, func() {
						ui.Field(c, m.FieldName(), func() {
							in := formTextInput(c.Key("f:name"), &f.name).Placeholder("web-01").AutoFocus()
							if !f.addingGroup {
								a.editorHold.apply(c, f, in)
							}
						})
						a.protocolField(c, m, f)
						proto := f.protocol()
						if proto == assets.ProtoTelnet {
							ui.Field(c, "", func() { notice(c, "f:plain", icons.Warning, tok.Warning, m.TelnetPlaintext()) })
						}
						if proto == assets.ProtoSerial {
							a.serialFields(c, m, tok, f)
						} else {
							ui.Field(c, m.FieldHost(), func() { formTextInput(c.Key("f:addr"), &f.address).Placeholder("example.com") })
							ui.Field(c, m.FieldPort(), func() { formTextInput(c.Key("f:port"), &f.port).Width(90) })
						}
						ui.Field(c, m.FieldUser(), func() { formTextInput(c.Key("f:user"), &f.user).Placeholder("root") })
						ui.Field(c, m.FieldGroup(), func() { a.hostGroupField(c, m, f) })
						ui.Field(c, m.FieldAppearance(), func() { a.lookFields(c, m, tok, &f.icon, &f.emoji, &f.color, m.AutoIcon(), m.AutoColor()) })
						ui.Field(c, m.FieldAuth(), func() {
							if f.auth == "" {
								f.auth = a.authLabel(m, f.orig)
							}
							var labels []string
							for _, ch := range a.protoAuthChoices(m, proto) {
								labels = append(labels, ch.label)
							}
							labels = append(labels, m.NewCredentialChoice())
							prev := f.auth
							if formSelect(c.Key("f:auth"), &f.auth, labels).Label(m.FieldAuth()).Changed() && f.auth == m.NewCredentialChoice() {
								// The credential sheet replaces the editor, then
								// comes back with the new credential chosen.
								f.auth = prev
								a.openCredForm(nil, f)
								a.editing = false
							}
						})
						a.hostSecretFields(c, m, tok, f)
						if proto != assets.ProtoSSH && f.auth != m.AuthManual() {
							ui.Field(c, "", func() { ui.Text(c, m.AutoLoginHint()).FontSize(theme.Caption).TextColor(tok.TextMuted) })
						}
						if proto != assets.ProtoSerial {
							ui.Field(c, "", func() {
								ui.Collapsible(c.Key("f:net"), m.Network(), &f.netOpen, func() {
									a.networkFields(c, m, tok, f)
								})
							})
						}
						if proto == assets.ProtoSSH {
							ui.Field(c, "", func() {
								ui.Collapsible(c.Key("f:dirs"), m.DefaultDirs(), &f.dirsOpen, func() {
									a.dirFields(c, m, tok, f)
								})
							})
							sftpSudoField(c, m, tok, f)
							ui.Field(c, m.TmuxAutoAttach(), func() {
								ui.Column(c).Gap(4).FillWidth().Children(func() {
									formTextInput(c.Key("f:tmux"), &f.tmux).Placeholder(m.TmuxAutoAttachPlaceholder()).Label(m.TmuxAutoAttach()).FillWidth()
									ui.Text(c, m.TmuxAutoAttachHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
								})
							})
							ui.Field(c, m.MetricsBar(), func() {
								ui.Column(c).Gap(4).Children(func() {
									formSegmented(c.Key("f:met"), &f.metricsIdx, !a.settings.MetricsEnabled, m.MetricsFollow(), m.MetricsHostOn(), m.MetricsHostOff()).Label(m.MetricsBar())
									if !a.settings.MetricsEnabled {
										ui.Text(c, m.MetricsDisabledGlobally()).FontSize(theme.Caption).TextColor(tok.TextMuted)
									}
								})
							})
						} else {
							a.streamTermFields(c, m, tok, f)
						}
						encodingField(c, m, tok, f)
						if f.protocol() != assets.ProtoSerial {
							a.termTypeField(c, m, tok, f)
						}
						sessionLogField(c, m, tok, f)
						a.transfersField(c, m, tok, f)
						ui.Field(c, "", func() {
							ui.Collapsible(c.Key("f:renewal"), m.RenewalSection(), &f.renewOpen, func() {
								a.renewalFields(c, m, tok, f)
							})
						})
						ui.Field(c, "", func() {
							ui.Collapsible(c.Key("f:adv"), m.Advanced(), &f.advOpen, func() {
								labels := syncOSC52Label(m, f)
								ui.Column(c).Gap(6).FillWidth().Margin(8, 0, 0, 0).Children(func() {
									ui.Text(c, m.Osc52Title()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
									formSelect(c.Key("f:osc52"), &f.osc52, labels).Label(m.Osc52Title()).FillWidth()
								})
							})
						})
						ui.Field(c, "", func() {
							a.aiHostFields(c, m, tok, f, proto == assets.ProtoSSH)
						})
						ui.Field(c, "", func() {
							ui.Column(c).Gap(6).Children(func() {
								if proto == assets.ProtoSSH {
									formCheckbox(c.Key("f:cwd"), &f.cwdFollow, m.CwdFollow())
									ui.Text(c, m.HostCwdFollowHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
								}
								formCheckbox(c.Key("f:fav"), &f.favorite, m.Favorite())
							})
						})
					})
				})
				if f.err != "" {
					ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
				}
				ui.Box(c).Height(1).FillWidth().Background(tok.Line)
				ui.Row(c).Gap(8).Children(func() {
					if !f.isNew && pill(c, m.Delete(), false, true, false) {
						a.deleteID, a.deleting = f.orig.ID, true
						a.editing = false
					}
					ui.Spacer(c)
					if pill(c, m.Cancel(), false, false, false) {
						a.editing = false
					}
					if pill(c, m.Save(), true, false, false) {
						a.SaveForm(m)
					}
				})
			})
		}), tok)
		if !a.editing && (a.credForm == nil || a.credForm.back != f) && !a.proxyKeeps(f) {
			a.form = nil
		}
	}
	if a.deleting {
		name := a.deleteID
		if h, ok := a.store.Get(a.deleteID); ok {
			name = h.Label()
		}
		switch ui.AlertDialog(c, &a.deleting, m.DeleteHost(), m.DeleteHostConfirm(name), m.Cancel(), m.Delete()) {
		case 1:
			id := a.deleteID
			a.runSecretAction(func() error { return a.DeleteHost(id) }, func(err error) { a.toastError(a.inUseText(m, err)) })
		}
	}
}

// networkFields are the editor's network section: the proxy of the first
// hop, and the jump hosts dialed through in order, which drag reorders.
func (a *App) networkFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Column(c).Gap(10).FillWidth().Margin(8, 0, 0, 0).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.FieldProxy()).FontSize(theme.Footnote).TextColor(tok.TextMuted).Width(92)
			a.proxySelect(c.Key("f:proxy"), m, &f.proxy, proxyPick{owner: f,
				leave: func() { a.editing = false },
				back: func(id string) {
					a.form, a.editing = f, true
					if id != "" {
						f.proxy = id
					}
				}}).Grow(1)
		})
		// Telnet goes through a proxy only: jump hosts are SSH.
		if f.protocol() != assets.ProtoSSH {
			return
		}
		ui.Text(c, m.FieldJumpHosts()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
		// Row actions replace f.jumps while List still uses this build's
		// count, so all row callbacks must read the same stable slice.
		jumps := f.jumps
		if len(jumps) == 0 {
			ui.Text(c, m.NoJumpHosts()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
		} else {
			f.jumpList.Key = func(i int) any { return jumps[i] }
			f.jumpList.Label = func(i int) string { return a.hostLabel(jumps[i]) }
			f.jumpList.Reorder = func(rows []int, to int) { f.jumps = moveRows(jumps, rows, to) }
			// More than five scroll, the remove buttons clear of the bar.
			end := float32(4)
			if len(jumps) > 5 {
				end += scrollGutter
			}
			ui.List(c.Key("f:jumps"), &f.jumpList, len(jumps), func(i int) {
				id := jumps[i]
				ui.Row(c.Key("jump:"+id)).Height(34).Gap(8).Padding(0, end, 0, 8).AlignItems(ui.Center).FillWidth().Children(func() {
					ui.Text(c, strconv.Itoa(i+1)).FontSize(theme.Caption).TextColor(tok.TextMuted).Width(14)
					tile(c, icons.JumpHost, tok.Tint(id), 20)
					ui.Text(c, a.hostLabel(id)).FontSize(theme.Body).SingleLine().Grow(1).Shrink(1)
					if iconButton(c, "jump:rm:"+id, icons.Close, m.RemoveJumpHost()) {
						f.jumps = slices.DeleteFunc(slices.Clone(f.jumps), func(s string) bool { return s == id })
					}
				})
			}).Height(float32(34*min(len(jumps), 5))).FillWidth().Radius(theme.RadiusControl).Border(1, tok.Line).Background(tok.Fill)
		}
		var pick []string
		var pickIDs []string
		for _, h := range a.store.List() {
			if h.ID != f.orig.ID && h.SSH() && !slices.Contains(f.jumps, h.ID) {
				pick, pickIDs = append(pick, h.Label()), append(pickIDs, h.ID)
			}
		}
		if len(pick) > 0 {
			f.jumpPick = m.AddJumpHost()
			if formSelect(c.Key("f:jumpadd"), &f.jumpPick, append([]string{m.AddJumpHost()}, pick...)).Label(m.AddJumpHost()).FillWidth().Changed() {
				if i := slices.Index(pick, f.jumpPick); i >= 0 {
					f.jumps = append(slices.Clone(f.jumps), pickIDs[i])
				}
				f.jumpPick = m.AddJumpHost()
			}
		}
	})
}

// dirFields are the editor's default folders: where the host's terminal
// and the two sides of its Files panes start.
func (a *App) dirFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Column(c).Gap(10).FillWidth().Margin(8, 0, 0, 0).Children(func() {
		for _, d := range []struct {
			key, label, hint string
			value            *string
		}{
			{"f:tdir", m.DefaultDirTerminal(), "~", &f.termDir},
			{"f:rdir", m.DefaultDirRemote(), "~", &f.remoteDir},
			{"f:ldir", m.DefaultDirLocal(), a.localHome(), &f.localDir},
		} {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, d.label).FontSize(theme.Footnote).TextColor(tok.TextMuted).Width(92)
				formTextInput(c.Key(d.key), d.value).Placeholder(d.hint).Label(d.label).Grow(1)
			})
		}
		ui.Text(c, m.DefaultDirsHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
	})
}

// encodingField is the output encoding the host's new terminals start
// with; each open terminal still switches its own.
func encodingField(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.TerminalEncoding(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			encodings := vt.OutputEncodings()
			labels := make([]string, len(encodings))
			for i, e := range encodings {
				labels[i] = outputEncodingLabel(m, e)
			}
			label := outputEncodingLabel(m, f.encoding)
			if formSelect(c.Key("f:enc"), &label, labels).Label(m.TerminalEncoding()).FillWidth().Changed() {
				if i := slices.Index(labels, label); i >= 0 {
					f.encoding = encodings[i]
				}
			}
			ui.Text(c, m.TerminalEncodingHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}

// sftpSudoField is whether the host's files panes open the files as
// root, through sudo.
func sftpSudoField(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.SftpSudo(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			formCheckbox(c.Key("f:sudo"), &f.sftpSudo, m.SftpSudoOn())
			ui.Text(c, m.SftpSudoHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}

// sessionLogField is whether the host's terminals are logged as they
// connect.
func sessionLogField(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.SessionLogs(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			formCheckbox(c.Key("f:log"), &f.sessionLog, m.LogAuto())
			ui.Text(c, m.LogAutoHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}

// transfersField lets the host's terminals take ZMODEM and trzsz
// transfers, which the global setting turned off turns off for all.
func (a *App) transfersField(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.TermXferSection(), func() {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			formCheckbox(c.Key("f:xfer"), &f.transfers, m.TermXferHost())
			hint := m.TermXferHostHint()
			if !a.settings.TerminalTransfers {
				hint = m.TermXferDisabledGlobally()
			}
			ui.Text(c, hint).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}

// hostLabel is the label of the host of id, or id when it is gone.
func (a *App) hostLabel(id string) string {
	if h, ok := a.store.Get(id); ok {
		return h.Label()
	}
	return id
}

// moveRows moves the rows of s (in order) to before index to.
func moveRows[T any](s []T, rows []int, to int) []T {
	var moved, rest []T
	before := 0
	for i, v := range s {
		if slices.Contains(rows, i) {
			moved = append(moved, v)
			if i < to {
				before++
			}
		} else {
			rest = append(rest, v)
		}
	}
	to -= before
	return slices.Concat(rest[:to], moved, rest[to:])
}
