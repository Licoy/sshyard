package app

import (
	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
	"strings"
	"time"
)

// addEntries are the entries of the sidebar's "+": a new host or group,
// quick connect, a local terminal, import.
func (a *App) addEntries(m locales.Messages) []menuEntry {
	return []menuEntry{
		{Icon: icons.Host, Label: m.NewHost(), Do: func() { a.openEditor(nil) }},
		{Icon: icons.NewFolder, Label: m.NewGroup(), Do: func() { a.openGroupForm("", "") }},
		a.bound(actQuickConnect, menuEntry{Icon: icons.Quick, Label: m.QuickConnectMenu(), Do: a.OpenQuickConnect}),
		{Icon: icons.Forward, Label: m.NewForwardMenu(), Disabled: a.store.Len() == 0, Do: func() { a.openFwdForm(m, nil, a.selectedHost()) }},
		a.localEntry(m),
		menuSep,
		{Icon: icons.Import, Label: m.ImportSSHConfigMenu(), Do: func() { a.OpenImport(m, "") }},
	}
}

// dragHost is a host dragged from the sidebar onto a pane.
type dragHost struct{ ID string }

func (a *App) section(key string) *bool {
	if b, ok := a.sections[key]; ok {
		return b
	}
	b := true
	a.sections[key] = &b
	return &b
}

func (a *App) sidebarView(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ui.Column(c).Fill().Children(func() {
		// Choosing several and the search, each opening into its own bar
		// across the row, then measuring the hosts, folding the groups and
		// adding: a new host or group, a host typed on the spot, or the
		// hosts of ~/.ssh/config.
		ui.Row(c.Key("sbtools")).Padding(8, 8, 6, 8).Gap(4).AlignItems(ui.Center).Children(func() {
			// as high as the field, so that the row keeps its size
			ui.Box(c).Size(0, 30).Shrink(0)
			switch {
			case a.multi.on:
				a.multiSelectBar(c, m, tok)
				return
			case a.searchOpen || a.search != "":
				a.searchBox(c, m, tok)
				return
			}
			if (IconButton{Icon: icons.MultiSelect, Label: m.MultiSelect(), Disabled: a.store.Len()+len(a.store.Groups()) == 0}).Clicked(c, "multi") {
				a.OpenMultiSelect()
			}
			search := IconButton{Icon: icons.Search, Label: m.SearchHosts()}.Show(c, "search")
			if a.searchFocus {
				search.Focus()
				a.searchFocus = false
			}
			if search.Clicked() {
				a.OpenSearch()
			}
			ui.Spacer(c)
			if (IconButton{Icon: icons.TestLatency, Label: m.TestLatency(), Disabled: a.store.Len() == 0 || a.pinging()}).Clicked(c, "ping") {
				a.PingAll()
			}
			a.sidebarSectionsButton(c, m)
			a.menuButton(c, "newhost", IconButton{Icon: icons.Add, Label: m.AddMenu()}, func() []menuEntry {
				return a.addEntries(m)
			})
		})
		if a.store.Len() == 0 && len(a.store.Groups()) == 0 {
			ui.Column(c).Padding(16).Gap(6).Children(func() {
				ui.Text(c, m.NoHosts()).FontWeight(600)
				ui.Text(c, m.NoHostsHint()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
			})
			ui.Spacer(c)
		} else {
			favs, groups, byGroup := hostsBy(a.store, a.search)
			sb := ui.Sidebar(c.Key("hosts"), &a.selected, func() {
				if len(favs) > 0 {
					ui.SidebarSection(c.Key("s:fav"), m.Favorites(), a.section("fav"), func() {
						for _, h := range favs {
							a.hostItem(c, m, h, "fav:")
						}
					})
				}
				gap := len(favs) > 0
				for _, g := range groups {
					if assets.GroupParent(g) == "" {
						a.groupTree(c, m, tok, g, groups, byGroup, gap)
						gap = false
					}
				}
				if hs := byGroup[""]; len(hs) > 0 {
					title := m.Ungrouped()
					if len(groups) == 0 {
						title = m.Hosts()
					}
					sec := ui.SidebarSection(c.Key("s:none"), title, a.section("none"), func() {
						for _, h := range hs {
							a.hostItem(c, m, h, "")
						}
					})
					a.ungroupedDrops(c, m, sec, tok)
				}
				a.forwardsSection(c, m, tok)
			}).Grow(1).FillWidth()
			if a.hasBackground() {
				// The sidebar region already owns its image tint; MyGo's
				// sidebar scroll must not cover it with Theme.Surface.
				sb.Background(ui.Transparent)
			}
		}

		// The foot of the sidebar: host count and live SSH connections.
		ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
		ui.Row(c.Key("sbfoot")).Height(statusBarHeight).Shrink(0).Padding(0, 6, 0, 12).Gap(theme.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.HostCount(a.store.Len())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(1)
			ui.Spacer(c)
			n := a.connectedCount()
			color := tok.TextMuted
			if n > 0 {
				color = tok.Success
			}
			ui.Row(c.Key("sbconnections")).Shrink(0).Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
				dot(c, color)
				ui.Text(c, m.ConnectionCount(n)).FontSize(theme.Caption).TextColor(color).SingleLine()
			})
			if (IconButton{Icon: icons.HideSidebar, Label: m.HideSidebar(), Size: Small}).Clicked(c, "hidesidebar") {
				a.setSidebarHidden(true)
			}
		})
	})
}

// connectedCount counts shared SSH clients once, excluding local shells.
func (a *App) connectedCount() int {
	clients := make(map[*ssh.Client]struct{})
	for _, cn := range a.conns {
		if cn.s != nil {
			if client := cn.s.Client(); client != nil {
				clients[client] = struct{}{}
			}
		}
	}
	return len(clients)
}

// ungroupedDrops takes the hosts and groups dropped on the ungrouped
// section: a host leaves its group, a group goes to the top level.
func (a *App) ungroupedDrops(c *ui.Context, m locales.Messages, sec ui.Element, tok theme.Tokens) {
	movable := func(v sidebarDrop) bool {
		switch d := v.(type) {
		case dragHost:
			h, ok := a.store.Get(d.ID)
			return ok && h.Group != ""
		case dragGroup:
			return a.search == "" && assets.GroupParent(d.Name) != ""
		}
		return false
	}
	if v, ok := ui.Drop[sidebarDrop](sec); ok && movable(v) {
		var err error
		switch d := v.(type) {
		case dragHost:
			err = a.store.SetGroup(d.ID, "")
		case dragGroup:
			err = a.store.NestGroup(d.Name, "")
			if err == nil {
				a.moveSections(d.Name, assets.GroupLeaf(d.Name))
			}
		}
		if err != nil {
			c.AddToast(ui.Toast{Title: groupMoveError(m, err), Type: "error"})
		}
	}
	if v, ok := ui.DragOver[sidebarDrop](sec); ok && movable(v) {
		sec.Background(tok.Accent.Alpha(0.08))
	}
}

// searchBox is the hosts' search, open across the toolbar: the text, then
// an IconButton giving the field the keyboard and one closing the search
// (as Escape does), which empties it. MyGo's
// SearchField draws its clear button's cross in the theme's background,
// which the sidebar's clear material makes invisible: hence a field of
// the app's own.
func (a *App) searchBox(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	row := ui.Row(c.Key("hostsearch")).Grow(1).Shrink(1).MinWidth(0).Height(30).Padding(0, 3, 0, 10).Gap(6).AlignItems(ui.Center).
		Radius(theme.RadiusControl).Background(tok.Fill)
	row.Children(func() {
		in := ui.TextInputBase(c.Key("q"), &a.search).Placeholder(m.SearchHosts()).Label(m.SearchHosts()).Grow(1).Shrink(1).MinWidth(0)
		if a.searchFocus {
			in.Focus()
			a.searchFocus = false
		}
		if (IconButton{Icon: icons.Search, Label: m.SearchHosts(), Size: Small}).Clicked(c, "find") {
			a.searchFocus = true
		}
		if (IconButton{Icon: icons.Close, Label: m.CloseSearch(), Size: Small}).Clicked(c, "close") || in.Shortcut(0, ui.KeyEscape) {
			a.CloseSearch()
		}
	})
}

// OpenSearch opens the hosts' search and focuses its field.
func (a *App) OpenSearch() {
	a.searchOpen, a.searchFocus = true, true
}

// CloseSearch empties the hosts' search and closes it, giving its button
// the keyboard.
func (a *App) CloseSearch() {
	a.search, a.searchOpen, a.searchFocus = "", false, true
}

// selectedHost is the ID of the host chosen in the sidebar, if it still is.
func (a *App) selectedHost() string {
	id := a.selected
	if len(id) > 4 && id[:4] == "fav:" {
		id = id[4:]
	}
	if _, ok := a.store.Get(id); !ok {
		return ""
	}
	return id
}

// hostActionsWidth is the room the actions of a sidebar host take at the
// end of its row: two small IconButtons and the gap between them.
const hostActionsWidth = 2*24 + 2 + 4

// renewalMarkWidth is the room the mark of a renewal soon takes at the
// end of a sidebar host's row, beside its latency, in place of its
// actions.
var renewalMarkWidth = 14 + theme.Space(1)

// hostItem is a host of the sidebar: its tile, then its name, and while
// the pointer is over it or it is chosen, what to do with it at the end
// (files, and the rest in its menu, as on a right click; a double click
// connects). A dot on the tile's corner tells how its sessions are, a
// mark at the end that its subscription renews soon. Resting the pointer
// on it shows its card; its latency shows at the end while its actions
// do not. The item lays its children out reversed, so that
// the tile it is given (after its label, as a badge) comes first.
func (a *App) hostItem(c *ui.Context, m locales.Messages, h assets.Host, prefix string) {
	tok := currentTokens(c)
	it := ui.SidebarItem(c.Key(prefix+h.ID), prefix+h.ID, nil, a.pv(h.Label())).Reverse().Description(a.pv(a.hostTip(m, h))).Drag(dragHost{h.ID})
	chosen := !a.multi.on && a.selected == prefix+h.ID
	if chosen {
		// Gray even while the sidebar has the focus, as the actions on
		// it are drawn for the sidebar's ground, not the accent.
		it.Background(tok.Press).TextColor(tok.Text)
	}
	if prefix == "" {
		a.locateMark(c, tok, it, h.ID)
	}
	more := "more:" + prefix + h.ID
	shown := chosen || !a.multi.on && it.Hovered() || a.menu.key == more
	days, soon := h.Renewal.Reminding(c.Now())
	lat, measured := a.pingOf(h)
	var marks float32
	if soon {
		marks += renewalMarkWidth
	}
	if measured {
		marks += pingWidth
	}
	switch {
	case shown:
		it.Padding(theme.Space(1.5), hostActionsWidth, theme.Space(1.5), theme.Space(2))
	case marks > 0:
		it.Padding(theme.Space(1.5), marks+theme.Space(3), theme.Space(1.5), theme.Space(2))
	}
	it.Children(func() {
		// Laid out reversed: the check box comes before the tile.
		ui.Box(c.Key("avatar")).Shrink(0).Children(func() {
			hostAvatar(c, tok, h, 20)
			if st, ok := a.hostState(h.ID); ok {
				dot(c, stateColor(tok, st)).Absolute().Top(-2).Right(-2).Border(1.5, tok.Sidebar).Size(9, 9).Radius(4.5).
					Label(a.statusText(m, h, session.Status{State: st}, c.Now()))
			}
		})
		if a.multi.on {
			state := pickNone
			if a.multi.hosts[h.ID] {
				state = pickFull
			}
			pickBox(c, "pick", a.pv(h.Label()), state, func(on bool) { a.pickHost(h.ID, on) })
		}
		if marks > 0 && !shown {
			ui.Row(c.Key("marks")).Absolute().Top(0).Bottom(0).Right(theme.Space(2)).Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
				if soon {
					ui.Box(c.Key("renewal")).Shrink(0).Label(m.RenewalSoon(renewWhen(m, days))).Children(func() {
						ui.Icon(c, icons.Renewal.SVG).Size(14, 14).TextColor(renewalColor(tok, h.Renewal, c.Now()))
					})
				}
				if measured {
					pingText(c, m, tok, lat)
				}
			})
		}
		// Shown while its menu is open, which outlives the hover.
		acts := ui.Row(c.Key("acts")).Absolute().Top(0).Bottom(0).Right(4).Gap(2).AlignItems(ui.Center)
		if !shown {
			acts.Invisible()
		}
		acts.Children(func() {
			if (IconButton{Icon: icons.Folder, Label: m.OpenFiles(), Size: Small, Disabled: !h.SSH()}).Clicked(c, "files") {
				a.OpenFiles(h.ID, openAt{}, nil)
			}
			a.menuButton(c, more, IconButton{Icon: icons.More, Label: m.MoreActions(), Size: Small}, func() []menuEntry {
				return a.hostEntries(m, h)
			})
		})
	})
	a.hostCardOn(c, m, tok, it, prefix+h.ID, h)
	switch {
	case a.multi.on && it.Clicked():
		a.pickHost(h.ID, !a.multi.hosts[h.ID])
		c.Invalidate() // the groups above and the toolbar, built already
	case !a.multi.on && it.DoubleClicked():
		a.Connect(h.ID, openAt{})
	}
	a.contextMenu(it, func() []menuEntry { return a.hostEntries(m, h) })
}

// hostEntries are what can be done with a sidebar host: its context
// menu, and the menu of its "more" button.
func (a *App) hostEntries(m locales.Messages, h assets.Host) []menuEntry {
	noTab := a.ws.ActiveTab() == nil
	// Telnet and serial hosts have a terminal only.
	noSSH := !h.SSH()
	beside := func(open func(at openAt)) func() {
		return func() {
			if t := a.ws.ActiveTab(); t != nil {
				open(openAt{pane: t.Focus, edge: layout.Right})
			}
		}
	}
	groups := []menuEntry{
		{Icon: icons.Folder, Label: m.NoGroup(), Checked: h.Group == "", Do: func() {
			if h.Group != "" {
				a.store.SetGroup(h.ID, "")
			}
		}},
	}
	groupNames := a.store.Groups()
	if len(groupNames) > 0 {
		groups = append(groups, menuSep)
	}
	for _, g := range groupNames {
		groups = append(groups, menuEntry{Icon: icons.Folder, Label: g, Checked: h.Group == g, Do: func() {
			if h.Group != g {
				a.store.SetGroup(h.ID, g)
			}
		}})
	}
	groups = append(groups, menuSep, menuEntry{Icon: icons.NewFolder, Label: m.NewGroupEllipsis(), Do: func() { a.openGroupForm("", h.ID) }})
	connect := []menuEntry{{Icon: icons.Run, Label: m.Connect(), Do: func() { a.Connect(h.ID, openAt{}) }}}
	if h.SSH() && h.Tmux != "" {
		// A way in should the host's tmux session not start.
		connect = append(connect, menuEntry{Icon: icons.Terminal, Label: m.ConnectWithoutTmux(), Do: func() { a.ConnectWithoutTmux(h.ID) }})
	}
	return append(connect, []menuEntry{
		{Icon: icons.SplitRight, Label: m.SplitRight(), Disabled: noTab, Do: beside(func(at openAt) { a.Connect(h.ID, at) })},
		{Icon: icons.Folder, Label: m.OpenFiles(), Disabled: noSSH, Do: func() { a.OpenFiles(h.ID, openAt{}, nil) }},
		{Icon: icons.Robot, Label: m.OpenAI(), Do: func() { a.OpenAIFor(h.ID) }},
		{Icon: icons.OpenFile, Label: openFilesLabel(m, a.filesEdge()), Disabled: noTab || noSSH, Do: beside(func(at openAt) {
			at.edge = a.filesEdge()
			a.OpenFiles(h.ID, at, nil)
		})},
		menuSep,
		{Icon: icons.Star, Label: m.Favorite(), Checked: h.Favorite, Do: func() { a.store.SetFavorite(h.ID, !h.Favorite) }},
		{Icon: icons.MoveTo, Label: m.MoveToGroup(), Sub: groups},
		{Icon: icons.Edit, Label: m.Edit(), Do: func() {
			hh := h
			a.openEditor(&hh)
		}},
		{Icon: icons.InstallKey, Label: m.InstallPublicKey(), Disabled: noSSH, Do: func() { a.openInstall(h.ID) }},
		{Icon: icons.HostKey, Label: m.ForgetHostKey(), Disabled: !a.knowsHostKey(h), Do: func() { a.forgetHostKey(h) }},
		{Icon: icons.Forward, Label: m.NewForwardMenu(), Disabled: noSSH, Do: func() { a.openFwdForm(m, nil, h.ID) }},
		{Icon: icons.Delete, Label: m.Delete(), Danger: true, Do: func() { a.deleteID, a.deleting = h.ID, true }},
	}...)
}

// hostState is how the sessions open on host id are, the best of them
// (connected, then on the way, then failed), and false with none.
func (a *App) hostState(id string) (session.State, bool) {
	rank := func(st session.State) int {
		switch st {
		case session.Connected:
			return 3
		case session.Connecting, session.Reconnecting:
			return 2
		case session.Failed, session.Disconnected:
			return 1
		}
		return 0
	}
	best, ok := session.State(0), false
	for _, cn := range a.conns {
		if cn.host.ID != id || cn.s == nil || cn.fw != nil {
			continue
		}
		st := cn.s.Status().State
		if rank(st) > 0 && (!ok || rank(st) > rank(best)) {
			best, ok = st, true
		}
	}
	return best, ok
}

// hostTip is a sidebar host's tooltip: where it logs in, and the proxy
// and jump hosts it is reached through ("via A → B").
func (a *App) hostTip(m locales.Messages, h assets.Host) string {
	var chain []string
	if px, ok := a.proxyOf(h); ok {
		chain = append(chain, px.Name)
	}
	path, _ := a.store.JumpPath(h.ID)
	for _, j := range path {
		chain = append(chain, j.Label())
	}
	if len(chain) == 0 {
		return h.Target()
	}
	return m.ViaJumps(h.Target(), strings.Join(chain, " → "))
}

// locateFlashes and locateBlink: a host revealed in the sidebar blinks
// three times, lit, then not, for locateBlink each.
const (
	locateFlashes = 3
	locateBlink   = 250 * time.Millisecond
)

// hostLocate is the host revealed in the sidebar: to scroll to the next
// time it is drawn, then blinking from at.
type hostLocate struct {
	id     string
	scroll bool
	at     time.Time
}

// locateHost reveals saved host id in the sidebar: shows the sidebar,
// empties the search, unfolds the host's groups, chooses it and has it
// scrolled to and blink.
func (a *App) locateHost(id string) {
	h, ok := a.store.Get(id)
	if !ok {
		return
	}
	if a.settings.SidebarHidden {
		a.setSidebarHidden(false)
	}
	a.search, a.searchOpen = "", false
	if h.Group == "" {
		*a.section("none") = true
	}
	for g := h.Group; g != ""; g = assets.GroupParent(g) {
		*a.section("g:" + g) = true
	}
	a.selected, a.page = id, pageWorkspace
	a.locate = hostLocate{id: id, scroll: true}
	a.invalidate()
}

// locateMark scrolls to it, the sidebar item of host id, when the host
// was just revealed, and lights it while it blinks.
func (a *App) locateMark(c *ui.Context, tok theme.Tokens, it ui.Element, id string) {
	l := &a.locate
	if l.id == "" || l.id != id {
		return
	}
	now := c.Now()
	if l.scroll {
		it.ScrollIntoView()
		l.scroll, l.at = false, now
	}
	gone := now.Sub(l.at)
	if gone >= 2*locateFlashes*locateBlink {
		*l = hostLocate{}
		return
	}
	if gone/locateBlink%2 == 0 {
		it.Background(tok.Accent.Alpha(0.24))
	}
	c.After(locateBlink - gone%locateBlink)
}
