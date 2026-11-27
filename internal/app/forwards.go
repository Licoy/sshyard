package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/forward"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/netproxy"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// fwdRun is a port forward started this run. Its connection is in
// a.conns, so that the prompts for a password or a host key serve it as
// they serve the panes, but it is shown in no pane.
type fwdRun struct {
	f  assets.Forward
	fw *forward.Forward
	c  *conn
	// err is why it could not start at all (its password is missing)
	err error
}

// errFwdNoPassword is a forward asking its clients for a password the
// keychain no longer holds.
var errFwdNoPassword = errors.New("forward: password missing from the keychain")

// fwdRefresh is how often the connections listed are redrawn, for their
// duration and bytes.
const fwdRefresh = 2 * time.Second

func fwdKind(k assets.ForwardKind) forward.Kind {
	switch k {
	case assets.ForwardRemote:
		return forward.Remote
	case assets.ForwardDynamic:
		return forward.Dynamic
	}
	return forward.Local
}

// promptConns are the connections whose failures may ask for a secret:
// the panes', then the port forwards'.
func (a *App) promptConns() []int {
	var ids []int
	for _, it := range a.allItems() {
		ids = append(ids, it.ID)
	}
	var fw []int
	for _, r := range a.fwds {
		if r.c != nil {
			fw = append(fw, r.c.id)
		}
	}
	slices.Sort(fw)
	return append(ids, fw...)
}

// StartForward starts a saved port forward on its host's connection,
// opening one without a shell when no pane has it. A local port in use
// is returned at once; the forward shows the error until stopped.
func (a *App) StartForward(id string) error { return a.startForward(id, false) }

// startForward is StartForward; auto starts it with no one watching, so
// that a login asking questions fails rather than asking them.
func (a *App) startForward(id string, auto bool) error {
	if !a.ready() {
		return keychain.ErrLocked
	}
	if a.fwds[id] != nil {
		return nil
	}
	f, ok := a.store.Forward(id)
	if !ok {
		return assets.ErrNotFound
	}
	h, ok := a.store.Get(f.HostID)
	if !ok {
		return assets.ErrNotFound
	}
	if !a.unlocked() && (a.connectionNeedsSecrets(h) || (f.Kind == assets.ForwardDynamic && f.User != "")) {
		return keychain.ErrLocked
	}
	run := &fwdRun{f: f}
	a.fwds[id] = run
	r := forward.Rule{Kind: fwdKind(f.Kind), Listen: f.Listen(), Target: f.Target(), Protocol: netproxy.Protocol(f.Protocol)}
	if f.Kind == assets.ForwardDynamic && f.User != "" {
		pw, err := a.kc.Get(keychain.ForwardPassword(id))
		if err != nil {
			run.err = errFwdNoPassword
			return run.err
		}
		r.User, r.Password = f.User, string(pw)
	}
	a.nextConn++
	c := &conn{id: a.nextConn, host: h, unattended: auto}
	cfg := a.hostConfig(h, c, func(context.Context) (session.Credentials, error) { return a.credentials(c, h) })
	fw := forward.New(r, cfg, session.Options{KeepAlive: time.Duration(a.settings.KeepAliveSeconds) * time.Second})
	c.s, c.fw = fw.Session(), fw
	fw.SetOnChange(a.invalidate)
	run.fw, run.c = fw, c
	a.conns[c.id] = c
	return fw.Start()
}

// StopForward stops a port forward and the connections it relays.
func (a *App) StopForward(id string) {
	run := a.fwds[id]
	if run == nil {
		return
	}
	delete(a.fwds, id)
	if run.c != nil {
		delete(a.conns, run.c.id)
		run.c.close()
	}
	a.invalidate()
}

// ToggleForward starts a stopped forward and stops a started one.
func (a *App) ToggleForward(id string) error {
	if a.fwds[id] != nil {
		a.StopForward(id)
		return nil
	}
	return a.StartForward(id)
}

// ForwardStatus is a forward's status, and whether it was started.
func (a *App) ForwardStatus(id string) (forward.Status, bool) {
	run := a.fwds[id]
	switch {
	case run == nil:
		return forward.Status{State: forward.Stopped}, false
	case run.err != nil:
		return forward.Status{State: forward.Error, Err: run.err}, true
	}
	st := run.fw.Status()
	// Its connection failed for a password or host key the user is being
	// asked for: it is still on its way.
	if st.State == forward.Error && st.Session.State == session.Failed && st.Session.Err != nil &&
		st.Session.Err == run.c.handled && (a.prompt != nil || a.hostKey != nil) {
		st.State, st.Err = forward.Connecting, nil
	}
	return st, true
}

// forwardConns are the connections a forward relays now, oldest first.
func (a *App) forwardConns(id string) []*netproxy.Conn {
	run := a.fwds[id]
	if run == nil || run.fw == nil {
		return nil
	}
	cs := run.fw.Conns.List()
	slices.SortFunc(cs, func(x, y *netproxy.Conn) int { return x.Start.Compare(y.Start) })
	return cs
}

// DisconnectForwardConn closes one connection of a forward.
func (a *App) DisconnectForwardConn(id string, conn uint64) {
	if run := a.fwds[id]; run != nil && run.fw != nil {
		run.fw.Conns.Close(conn)
	}
}

// runningForwards counts the forwards running through host id.
func (a *App) runningForwards(hostID string) int {
	n := 0
	for id, r := range a.fwds {
		if r.f.HostID == hostID {
			if st, _ := a.ForwardStatus(id); st.State == forward.Running {
				n++
			}
		}
	}
	return n
}

// autoStartForwards starts the forwards marked to start on launch.
func (a *App) autoStartForwards() {
	for _, f := range a.store.Forwards() {
		if f.AutoStart {
			a.startForward(f.ID, true)
		}
	}
}

// fwdStateText says what a forward is doing, and whether that is an error.
func (a *App) fwdStateText(m locales.Messages, f assets.Forward, st forward.Status) (string, bool) {
	switch st.State {
	case forward.Connecting:
		return m.FwdStateConnecting(), false
	case forward.Running:
		return m.FwdStateRunning(), false
	case forward.Reconnecting:
		return m.FwdStateReconnecting(), false
	case forward.Error:
		var rr *forward.RemoteRefusedError
		switch {
		case errors.Is(st.Err, forward.ErrPortInUse):
			return m.FwdPortInUse(), true
		case errors.As(st.Err, &rr):
			return m.FwdRemoteRefused(rr.Addr), true
		case errors.Is(st.Err, errFwdNoPassword):
			return m.FwdPasswordMissing(), true
		case st.Err == nil:
			return m.FwdError(m.StateDisconnected()), true
		}
		h, _ := a.store.Get(f.HostID)
		if r := a.fwds[f.ID]; r != nil && r.c != nil {
			h = a.failedHost(r.c.host, st.Err)
		}
		return m.FwdError(errText(m, h, st.Err)), true
	}
	return m.FwdStateStopped(), false
}

func fwdColor(tok theme.Tokens, s forward.State) ui.Color {
	switch s {
	case forward.Running:
		return tok.Success
	case forward.Connecting, forward.Reconnecting:
		return tok.Warning
	case forward.Error:
		return tok.Danger
	}
	return tok.TextMuted
}

// fwdSummary is a forward in ssh's terms: "-L 127.0.0.1:8080 → db:5432".
func fwdSummary(f assets.Forward) string {
	switch f.Kind {
	case assets.ForwardDynamic:
		return "-D " + f.Listen()
	case assets.ForwardRemote:
		return "-R " + f.Listen() + " → " + f.Target()
	}
	return "-L " + f.Listen() + " → " + f.Target()
}

func fwdDuration(d time.Duration) string {
	s := int(d / time.Second)
	if s < 3600 {
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

// forwardsSection is the sidebar's group of port forwards, below the
// hosts: a row each, with its state, connections and switch; a row
// clicked lists its connections.
func (a *App) forwardsSection(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	fs := a.store.Forwards()
	if len(fs) == 0 {
		return
	}
	ui.SidebarSection(c.Key("s:fwd"), m.PortForwards(), a.section("fwd"), func() {
		for _, f := range fs {
			a.forwardItem(c, m, tok, f)
		}
	})
}

func (a *App) forwardItem(c *ui.Context, m locales.Messages, tok theme.Tokens, f assets.Forward) {
	st, on := a.ForwardStatus(f.ID)
	open := a.fwdOpen[f.ID]
	h, _ := a.store.Get(f.HostID)
	label := f.Label()
	row := ui.Row(c.Key("fwd:"+f.ID)).FillWidth().Shrink(0).Gap(8).Padding(3, 4, 3, 10).Radius(theme.RadiusControl).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(m.FwdRowLabel(label)).Tooltip(a.pv(fwdSummary(f) + " · " + h.Label()))
	if open {
		row.Background(tok.Fill)
	} else if row.Hovered() {
		row.Background(tok.Hover)
	}
	toggled := false
	row.Children(func() {
		dot(c, fwdColor(tok, st.State))
		ui.Text(c, label).SingleLine().Grow(1).Shrink(1)
		if on && st.State != forward.Error {
			ui.Badge(c, strconv.Itoa(st.Conns))
		}
		tip := m.FwdStart()
		if on {
			tip = m.FwdStop()
		}
		if (IconButton{Icon: icons.Run, On: icons.RunOn, Label: tip, Active: on, Toggle: true, Size: Small}).Clicked(c, "toggle") {
			toggled = true
			a.runSecretAction(func() error { return a.ToggleForward(f.ID) }, func(err error) { a.toastError(m.ErrGeneric(secretStoreErrorText(m, err))) })
		}
	})
	a.contextMenu(row, func() []menuEntry {
		tip, ic := m.FwdStart(), icons.Run
		if on {
			tip, ic = m.FwdStop(), icons.Stop
		}
		return []menuEntry{
			{Icon: ic, Label: tip, Do: func() {
				a.runSecretAction(func() error { return a.ToggleForward(f.ID) }, func(err error) { a.toastError(m.ErrGeneric(secretStoreErrorText(m, err))) })
			}},
			{Icon: icons.Edit, Label: m.Edit(), Do: func() {
				ff := f
				a.openFwdForm(m, &ff, "")
			}},
			menuSep,
			{Icon: icons.Delete, Label: m.Delete(), Danger: true, Do: func() { a.fwdDeleteID, a.fwdDeleting = f.ID, true }},
		}
	})
	if row.Clicked() && !toggled {
		a.fwdOpen[f.ID] = !open
		open = !open
	}
	if !open {
		return
	}
	ui.Column(c.Key("fwdconns:"+f.ID)).FillWidth().Shrink(0).Gap(4).Padding(2, 4, 6, 25).Children(func() {
		text, bad := a.fwdStateText(m, f, st)
		col := tok.TextMuted
		if bad {
			col = tok.Danger
		}
		ui.Row(c).Gap(4).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Column(c).Gap(1).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, text).FontSize(theme.Caption).TextColor(col)
				ui.Text(c, a.pv(fwdSummary(f)+" · "+h.Label())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			})
			if (IconButton{Icon: icons.Edit, Label: m.EditForward(), Size: Small}).Clicked(c, "fwdedit:"+f.ID) {
				ff := f
				a.openFwdForm(m, &ff, "")
			}
		})
		cs := a.forwardConns(f.ID)
		if on && len(cs) == 0 && st.State != forward.Error {
			ui.Text(c, m.FwdNoConns()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		}
		now := c.Now()
		for _, cn := range cs {
			cn := cn
			up, down := cn.Bytes()
			ui.Row(c.Key("fc:"+strconv.FormatUint(cn.ID, 10))).FillWidth().Gap(4).Padding(4, 2, 4, 8).Radius(theme.RadiusControl).
				Background(tok.Fill).AlignItems(ui.Center).Tooltip(cn.Source + " → " + cn.Target).Children(func() {
				ui.Column(c).Gap(1).Grow(1).Shrink(1).Children(func() {
					// The target is what people look for, so it gets
					// the first line; the narrow sidebar would
					// otherwise cut it off behind the source.
					ui.Text(c, "→ "+cn.Target).FontSize(theme.Caption).SingleLine()
					src := m.FwdConnFrom(cn.Source)
					if cn.Protocol != "" {
						src = cn.Protocol + " · " + src
					}
					ui.Text(c, src).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
					info := fwdDuration(now.Sub(cn.Start)) + " · ↑" + humanSize(up) + " ↓" + humanSize(down)
					ui.Text(c, info).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
				})
				if (IconButton{Icon: icons.Disconnect, Label: m.FwdDisconnect(), Size: Small, Danger: true}).Clicked(c, "x") {
					a.DisconnectForwardConn(f.ID, cn.ID)
				}
			})
		}
		if on {
			c.After(fwdRefresh)
		}
	})
}

// paneForwards marks a terminal pane's header with the forwards running
// through its host.
func (a *App) paneForwards(c *ui.Context, m locales.Messages, tok theme.Tokens, hostID string) {
	n := a.runningForwards(hostID)
	if n == 0 {
		return
	}
	tip := m.FwdRunningOnHost(n)
	ui.Row(c.Key("fwds")).Shrink(0).Gap(3).Padding(1, 6).Radius(theme.Capsule).Background(tok.Success.Alpha(0.14)).
		AlignItems(ui.Center).Label(tip).Tooltip(tip).Children(func() {
		ui.Icon(c, icons.Forward.SVG).Size(12, 12).TextColor(tok.Success)
		ui.Text(c, strconv.Itoa(n)).FontSize(theme.Caption).FontWeight(500).TextColor(tok.Success).SingleLine()
	})
}

// fwdForm is the port forward editor's fields. Its password lives here
// only while the sheet is open; saving moves it into the keychain.
type fwdForm struct {
	isNew bool
	orig  assets.Forward
	name  string
	// kind is the segmented control's index: 0 -L, 1 -R, 2 -D
	kind                   int
	host                   string // the host select's label
	listenAddr, listenPort string
	targetAddr, targetPort string
	proto                  string // the protocol select's label
	user, password, note   string
	autoStart              bool
	err                    string
}

var fwdKinds = []assets.ForwardKind{assets.ForwardLocal, assets.ForwardRemote, assets.ForwardDynamic}
var fwdProtos = []assets.ForwardProtocol{assets.ProtoAuto, assets.ProtoHTTP, assets.ProtoSOCKS4, assets.ProtoSOCKS5}

func fwdProtoLabels(m locales.Messages) []string {
	return []string{m.FwdProtoAuto(), "HTTP", "SOCKS4/4a", "SOCKS5"}
}

// hostChoices are the hosts a forward can go through, by label.
func (a *App) hostChoices() (labels, ids []string) {
	for _, h := range a.store.List() {
		if h.SSH() {
			labels, ids = append(labels, h.Label()), append(ids, h.ID)
		}
	}
	return
}

// openFwdForm opens the forward sheet on f, or on a new forward through
// hostID.
func (a *App) openFwdForm(m locales.Messages, f *assets.Forward, hostID string) {
	ff := &fwdForm{isNew: f == nil, listenAddr: "127.0.0.1", targetAddr: "127.0.0.1"}
	ff.proto = fwdProtoLabels(m)[0]
	if f != nil {
		ff.orig = *f
		ff.name, ff.listenAddr, ff.listenPort = f.Name, f.ListenAddr, strconv.Itoa(f.ListenPort)
		ff.targetAddr, ff.user, ff.note, ff.autoStart = f.TargetAddr, f.User, f.Note, f.AutoStart
		if f.TargetPort != 0 {
			ff.targetPort = strconv.Itoa(f.TargetPort)
		}
		ff.kind = max(slices.Index(fwdKinds, f.Kind), 0)
		ff.proto = fwdProtoLabels(m)[max(slices.Index(fwdProtos, f.Protocol), 0)]
		hostID = f.HostID
	}
	labels, ids := a.hostChoices()
	if i := slices.Index(ids, hostID); i >= 0 {
		ff.host = labels[i]
	} else if len(labels) > 0 {
		ff.host = labels[0]
	}
	a.fwdForm, a.fwdEditing = ff, true
}

// forward is the sheet's forward, as it would be saved.
func (f *fwdForm) forward(a *App, m locales.Messages) (assets.Forward, string) {
	out := f.orig
	out.Name, out.Kind, out.ListenAddr, out.TargetAddr = f.name, fwdKinds[f.kind], f.listenAddr, f.targetAddr
	out.User, out.Note, out.AutoStart = f.user, f.note, f.autoStart
	labels, ids := a.hostChoices()
	out.HostID = ""
	if i := slices.Index(labels, f.host); i >= 0 {
		out.HostID = ids[i]
	}
	out.Protocol = fwdProtos[max(slices.Index(fwdProtoLabels(m), f.proto), 0)]
	port := func(s string) (int, bool) {
		p, err := strconv.Atoi(strings.TrimSpace(s))
		return p, err == nil && p >= 1 && p <= 65535
	}
	var ok bool
	if out.ListenPort, ok = port(f.listenPort); !ok {
		return out, m.InvalidPort()
	}
	if out.Kind != assets.ForwardDynamic {
		if out.TargetPort, ok = port(f.targetPort); !ok {
			return out, m.InvalidPort()
		}
	}
	out.Normalize()
	return out, ""
}

// SaveFwdForm validates the sheet, saves the forward and puts its
// password in the keychain; a forward running starts again with it.
func (a *App) SaveFwdForm(m locales.Messages) bool {
	f := a.fwdForm
	if f == nil {
		return false
	}
	f.err = ""
	out, msg := f.forward(a, m)
	if msg != "" {
		f.err = msg
		return false
	}
	if err := out.Validate(); err != nil {
		f.err = fwdFieldError(m, err)
		return false
	}
	auth := out.Kind == assets.ForwardDynamic && out.User != ""
	if auth && f.password == "" && (f.isNew || f.orig.User == "" || f.orig.Kind != assets.ForwardDynamic) {
		f.err = m.RequiredField(m.FieldPassword())
		return false
	}
	if f.isNew {
		out.ID = assets.NewID()
	}
	tx := &secretTx{kc: a.kc}
	fail := func(msg string) bool {
		if rerr := tx.rollback(); rerr != nil {
			msg = m.ErrKeychainRestore(msg, secretStoreErrorText(m, rerr))
		}
		f.err = msg
		return false
	}
	if !a.unlocked() && (f.password != "" || (!f.isNew && f.orig.Kind == assets.ForwardDynamic && f.orig.User != "" && !auth)) {
		draft := a.fwdForm
		a.withSecrets(func() {
			if a.fwdForm == draft {
				a.SaveFwdForm(m)
			}
		})
		return false
	}
	key := keychain.ForwardPassword(out.ID)
	var err error
	switch {
	case !auth && !f.isNew && f.orig.Kind == assets.ForwardDynamic && f.orig.User != "":
		err = tx.delete(key)
	case f.password != "":
		err = tx.set(key, []byte(f.password))
	}
	if err != nil {
		return fail(m.ErrKeychainSave(secretStoreErrorText(m, err)))
	}
	var saved assets.Forward
	if f.isNew {
		saved, err = a.store.AddForward(out)
	} else {
		saved, err = a.store.SaveForward(out)
	}
	if err != nil {
		return fail(fwdFieldError(m, err))
	}
	a.fwdForm, a.fwdEditing = nil, false
	if a.fwds[saved.ID] != nil {
		a.StopForward(saved.ID)
		a.StartForward(saved.ID)
	}
	return true
}

func fwdFieldError(m locales.Messages, err error) string {
	var fe *assets.FieldError
	if errors.As(err, &fe) && fe.Reason != "not ssh" {
		switch fe.Field {
		case "host":
			return m.RequiredField(m.FwdHost())
		case "target":
			return m.RequiredField(m.FwdTarget())
		}
	}
	return fieldError(m, err)
}

// DeleteForward stops a port forward, then removes it and its password;
// the password goes first.
func (a *App) DeleteForward(id string) error {
	f, ok := a.store.Forward(id)
	if !ok {
		return assets.ErrNotFound
	}
	if f.Kind == assets.ForwardDynamic && f.User != "" {
		if err := a.kc.Delete(keychain.ForwardPassword(id)); err != nil {
			return &keychainDeleteError{err}
		}
	}
	if err := a.store.DeleteForward(id); err != nil {
		return err
	}
	a.StopForward(id)
	delete(a.fwdOpen, id)
	return nil
}

func (a *App) fwdView(c *ui.Context, m locales.Messages) {
	f := a.fwdForm
	if f == nil {
		return
	}
	tok := currentTokens(c)
	sheet(ui.Modal(c, &a.fwdEditing, func() {
		ui.Column(c).Gap(18).Width(500).Children(func() {
			title := m.EditForward()
			if f.isNew {
				title = m.NewForward()
			}
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tint := theme.Brand
				if !f.isNew {
					tint = tok.Tint(f.orig.ID)
				}
				tile(c, icons.Forward, tint, 36)
				ui.Column(c).Gap(1).Shrink(1).Children(func() {
					sheetTitle(c, title)
					if out, msg := f.forward(a, m); msg == "" {
						ui.Text(c, fwdSummary(out)).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
					}
				})
			})
			dyn := fwdKinds[f.kind] == assets.ForwardDynamic
			ui.Form(c, func() {
				ui.Field(c, m.FieldName(), func() {
					in := formTextInput(c.Key("ff:name"), &f.name).Placeholder(m.FwdNamePlaceholder()).AutoFocus()
					a.fwdHold.apply(c, f, in)
				})
				ui.Field(c, m.FieldType(), func() {
					formSegmented(c.Key("ff:kind"), &f.kind, false, m.FwdKindLocal(), m.FwdKindRemote(), m.FwdKindDynamic()).Label(m.FieldType())
				})
				ui.Field(c, m.FwdHost(), func() {
					labels, _ := a.hostChoices()
					formSelect(c.Key("ff:host"), &f.host, labels).Label(m.FwdHost())
				})
				listen := m.FwdListenLocal()
				if fwdKinds[f.kind] == assets.ForwardRemote {
					listen = m.FwdListenRemote()
				}
				ui.Field(c, listen, func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						formTextInput(c.Key("ff:laddr"), &f.listenAddr).Placeholder("127.0.0.1").Label(listen).Grow(1)
						ui.Text(c, ":").TextColor(tok.TextMuted)
						formTextInput(c.Key("ff:lport"), &f.listenPort).Placeholder("8080").Label(m.FieldPort()).Width(80)
					})
				})
				if !dyn {
					target := m.FwdTargetRemote()
					if fwdKinds[f.kind] == assets.ForwardRemote {
						target = m.FwdTargetLocal()
					}
					ui.Field(c, target, func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							formTextInput(c.Key("ff:taddr"), &f.targetAddr).Placeholder("127.0.0.1").Label(target).Grow(1)
							ui.Text(c, ":").TextColor(tok.TextMuted)
							formTextInput(c.Key("ff:tport"), &f.targetPort).Placeholder("80").Label(m.FieldPort()).Width(80)
						})
					})
				} else {
					ui.Field(c, m.FwdProtocol(), func() {
						formSelect(c.Key("ff:proto"), &f.proto, fwdProtoLabels(m)).Label(m.FwdProtocol())
					})
					ui.Field(c, m.FieldUser(), func() {
						formTextInput(c.Key("ff:user"), &f.user).Placeholder(m.FwdUserPlaceholder())
					})
					if strings.TrimSpace(f.user) != "" {
						ui.Field(c, m.FieldPassword(), func() {
							ph := ""
							if !f.isNew && f.orig.User != "" {
								ph = m.PasswordUnchanged()
							}
							formTextInput(c.Key("ff:pw"), &f.password).Password().Placeholder(ph)
						})
					}
				}
				ui.Field(c, m.FwdAutoStart(), func() {
					formSwitch(c.Key("ff:auto"), &f.autoStart).Label(m.FwdAutoStart())
				})
				ui.Field(c, m.FieldNote(), func() { formTextInput(c.Key("ff:note"), &f.note) })
			})
			ui.Text(c, fwdHint(m, fwdKinds[f.kind])).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			probe := assets.Forward{ListenAddr: strings.TrimSpace(f.listenAddr)}
			if probe.ListenAddr == "" {
				probe.ListenAddr = "127.0.0.1"
			}
			if fwdKinds[f.kind] != assets.ForwardRemote && probe.Exposed() {
				warn := m.FwdExposed(probe.ListenAddr)
				if dyn && strings.TrimSpace(f.user) == "" {
					warn += " " + m.FwdExposedAuth()
				}
				ui.Row(c.Key("ff:warn")).Padding(8, 10).Gap(8).Radius(theme.RadiusControl).Background(tok.Warning.Alpha(0.12)).
					Border(1, tok.Warning.Alpha(0.35)).Children(func() {
					ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(tok.Warning)
					ui.Text(c, warn).FontSize(theme.Footnote).Shrink(1)
				})
			}
			if f.err != "" {
				ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				if !f.isNew && pill(c, m.Delete(), false, true, false) {
					// The sheet closes; the confirmation asks on its own.
					a.fwdDeleteID, a.fwdDeleting = f.orig.ID, true
					a.fwdEditing = false
				}
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.fwdEditing = false
				}
				if pill(c, m.Save(), true, false, false) {
					a.SaveFwdForm(m)
				}
			})
		})
	}), tok)
	if !a.fwdEditing {
		a.fwdForm = nil
	}
}

// fwdDeleteView confirms deleting a forward, from its row's menu or its
// sheet; a keychain error is reported and the rule is kept.
func (a *App) fwdDeleteView(c *ui.Context, m locales.Messages) {
	if !a.fwdDeleting {
		return
	}
	name := a.fwdDeleteID
	if f, ok := a.store.Forward(a.fwdDeleteID); ok {
		name = f.Label()
	}
	if ui.AlertDialog(c, &a.fwdDeleting, m.DeleteForward(), m.DeleteForwardConfirm(name), m.Cancel(), m.Delete()) == 1 {
		id := a.fwdDeleteID
		a.runSecretAction(func() error { return a.DeleteForward(id) }, func(err error) { a.toastError(a.inUseText(m, err)) })
	}
}

func fwdHint(m locales.Messages, k assets.ForwardKind) string {
	switch k {
	case assets.ForwardRemote:
		return m.FwdHintRemote()
	case assets.ForwardDynamic:
		return m.FwdHintDynamic()
	}
	return m.FwdHintLocal()
}
