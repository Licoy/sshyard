package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// The right edge of the workspace: a rail of IconButtons opening the tool
// panes (the system monitor, the host's details, the snippets and the
// others; see toolpane.go) and actions on the focused session.

// railWidth is the rail's width, the status bar's height.
const railWidth = statusBarHeight

// railView is the rail: the panels' toggles, then actions on the focused
// session, then ways to reach a new host at the bottom.
func (a *App) railView(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	cn := a.focused()
	ui.Column(c.Key("rail")).Width(railWidth).Shrink(0).FillHeight().Padding(theme.Space(2), 0).Gap(theme.Space(1)).
		AlignItems(ui.Center).Background(a.chromeFill(tok.Glass)).Children(func() {
		// A tool pane's toggle, lit while it is in this tab.
		toggle := func(k layout.Kind, ic, on *icons.Icon, label string) {
			if (IconButton{Icon: ic, On: on, Label: label, Active: a.toolHere(k), Toggle: true, Size: Small}).Clicked(c, "tool:"+k.String()) {
				a.ToggleTool(k)
			}
		}
		toggle(layout.Monitor, icons.Monitor, icons.MonitorOn, m.SystemMonitor())
		toggle(layout.Usage, icons.Usage, icons.UsageOn, m.ProcessUsage())
		toggle(layout.HostInfo, icons.Info, icons.InfoOn, m.HostDetails())
		ui.Box(c.Key("sep")).Width(14).Height(1).Shrink(0).MarginY(theme.Space(1)).Background(tok.Line)
		// The tab's files pane and AI pane, one each: lit while it has
		// one, which a click closes.
		t := a.ws.ActiveTab()
		files, ai := a.tabPane(t, layout.Files) != nil, a.tabPane(t, layout.AI) != nil
		label := openFilesLabel(m, a.filesEdge())
		if files {
			label = m.CloseFilesPane()
		}
		if (IconButton{Icon: icons.Folder, On: icons.FolderOn, Label: label, Active: files, Toggle: true, Disabled: !files && !remoteOnly(cn), Size: Small}).Clicked(c, "files") {
			a.ToggleFiles()
		}
		toggle(layout.Docker, icons.Docker, icons.DockerOn, m.DockerManager())
		toggle(layout.Tmux, icons.Tmux, icons.TmuxOn, m.TmuxManager())
		label = m.OpenAIRight()
		if ai {
			label = m.CloseAIPane()
		}
		if (IconButton{Icon: icons.Robot, On: icons.RobotOn, Label: label, Active: ai, Toggle: true, Disabled: !ai && !canAI(cn), Size: Small}).Clicked(c, "ai") {
			a.ToggleAI()
		}
		if (IconButton{Icon: icons.Forward, Label: m.NewForwardMenu(), Disabled: a.store.Len() == 0, Size: Small}).Clicked(c, "fwd") {
			id := a.selectedHost()
			if cn != nil && !isTemp(cn.host.ID) {
				id = cn.host.ID
			}
			a.openFwdForm(m, nil, id)
		}
		// The tool panes, working on the sessions: lit while in this
		// tab.
		ui.Box(c.Key("sep2")).Width(14).Height(1).Shrink(0).MarginY(theme.Space(1)).Background(tok.Line)
		toggle(layout.Snippets, icons.API, nil, m.Snippets())
		toggle(layout.History, icons.History, nil, m.CommandHistory())
		synced := ""
		if n := len(a.bc.synced); n > 0 {
			synced = strconv.Itoa(n)
		}
		if (IconButton{Icon: icons.Broadcast, On: icons.BroadcastOn, Label: m.Broadcast(), Active: a.toolHere(layout.Broadcast), Toggle: true,
			Prominent: synced != "", Badge: synced, Size: Small}).Clicked(c, "broadcast") {
			a.ToggleTool(layout.Broadcast)
		}
		logging, failed := a.logCount()
		badge := ""
		if logging > 0 {
			badge = strconv.Itoa(logging)
		}
		if (IconButton{Icon: icons.FileText, On: icons.FileTextOn, Label: m.SessionLogs(), Active: a.toolHere(layout.Logs), Toggle: true,
			Prominent: logging > 0, Badge: badge, BadgeDanger: failed, Size: Small}).Clicked(c, "logs") {
			a.ToggleTool(layout.Logs)
		}
		ui.Spacer(c)
		if (IconButton{Icon: icons.Quick, Label: m.QuickConnect(), Size: Small}).Clicked(c, "quick") {
			a.OpenQuickConnect()
		}
		if (IconButton{Icon: icons.Import, Label: m.ImportSSHConfig(), Size: Small}).Clicked(c, "import") {
			a.OpenImport(m, "")
		}
	})
}

// hostToolPanel is the body of tool pane k on the focused session's host:
// the monitor and the host's details as cards scrolling, the usage,
// Docker and tmux as tables (the tmux list) that scroll themselves.
func (a *App) hostToolPanel(c *ui.Context, m locales.Messages, tok theme.Tokens, k layout.Kind) {
	switch k {
	case layout.Usage, layout.Docker, layout.Tmux:
		ui.Column(c.Key("panel")).Fill().MinHeight(0).Padding(theme.Space(3)).Gap(theme.Space(2.5)).Children(func() {
			switch k {
			case layout.Usage:
				a.usagePanel(c, m, tok)
			case layout.Tmux:
				a.tmuxPanel(c, m, tok)
			default:
				a.dockerPanel(c, m, tok)
			}
		})
		return
	}
	ui.Scroll(c.Key("panel")).Fill().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(theme.Space(3)).Gap(theme.Space(2.5)).Children(func() {
			if k == layout.Monitor {
				a.monitorPanel(c, m, tok)
			} else {
				a.hostPanel(c, m, tok)
			}
		})
	})
}

// panelCard is a card of a panel: a tile, a title and a muted note on
// the right, over what fn builds.
func (a *App) panelCard(c *ui.Context, tok theme.Tokens, key string, ic *icons.Icon, tint ui.Color, title, note string, fn func()) {
	a.contentCard(c.Key(key), tok).FillWidth().Padding(theme.Space(3)).Gap(theme.Space(2.5)).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			softTile(c, ic, tint, 22)
			ui.Text(c, title).FontSize(theme.Body).FontWeight(600).Grow(1).Shrink(1).SingleLine()
			if note != "" {
				ui.Text(c, note).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			}
		})
		fn()
	})
}

// panelNote is a quiet line in place of a panel's content.
func panelNote(c *ui.Context, tok theme.Tokens, s string) {
	ui.Text(c, s).FontSize(theme.Footnote).TextColor(tok.TextMuted)
}

// barColor is the fill of a meter at pct: the accent, warning above 80%
// and danger above 95%.
func barColor(tok theme.Tokens, pct float64) ui.Color {
	switch {
	case pct > 95:
		return tok.Danger
	case pct > 80:
		return tok.Warning
	}
	return tok.Accent
}

// meterBar is a capsule track filled to pct percent.
func meterBar(c *ui.Context, tok theme.Tokens, pct float64) {
	p := float32(min(max(pct, 0), 100))
	ui.Row(c).FillWidth().Height(6).Shrink(0).Radius(3).Background(tok.Fill).Clip().Children(func() {
		if p > 0 {
			ui.Box(c).WidthPercent(p).MinWidth(6).FillHeight().Radius(3).Background(barColor(tok, pct))
		}
	})
}

// fact is a muted label over its value, a cell of a panel's grid.
func fact(c *ui.Context, tok theme.Tokens, label, value, fam string) {
	ui.Column(c).Gap(2).Grow(1).Basis(0).Shrink(1).MinWidth(0).Children(func() {
		ui.Text(c, label).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		v := ui.Text(c, value).FontSize(theme.Footnote).SingleLine()
		if fam != "" {
			v.Font(fam)
		}
	})
}

// facts lays out label, value pairs two to a row; an odd last pair
// takes the whole row, for a long value.
func facts(c *ui.Context, tok theme.Tokens, fam string, kv ...string) {
	ui.Column(c).Gap(theme.Space(2.5)).FillWidth().Children(func() {
		for i := 0; i+1 < len(kv); i += 4 {
			ui.Row(c).Gap(theme.Space(3)).FillWidth().Children(func() {
				fact(c, tok, kv[i], kv[i+1], fam)
				if i+3 < len(kv) {
					fact(c, tok, kv[i+2], kv[i+3], fam)
				}
			})
		}
	})
}

// fmtDur is a duration as the status bar and the monitor show it: the
// two largest units.
func fmtDur(m locales.Messages, d time.Duration) string {
	if d < 0 {
		d = 0
	}
	n := func(v time.Duration) string { return strconv.FormatInt(int64(v), 10) }
	switch {
	case d < time.Minute:
		return m.DurSeconds(n(d / time.Second))
	case d < time.Hour:
		return m.DurMinutes(n(d / time.Minute))
	case d < 24*time.Hour:
		return m.DurHours(n(d/time.Hour), n(d%time.Hour/time.Minute))
	}
	return m.DurDays(n(d/(24*time.Hour)), n(d%(24*time.Hour)/time.Hour))
}

// tickDur asks for the next frame when a duration since then shown by
// fmtDur next changes.
func tickDur(c *ui.Context, d time.Duration) {
	step := time.Minute
	if d < time.Minute {
		step = time.Second
	}
	c.After(step - d%step + 50*time.Millisecond)
}

// liveHost is h as saved now, or as the pane opened it when it is gone.
func (a *App) liveHost(h assets.Host) assets.Host {
	if cur, ok := a.host(h.ID); ok {
		return cur
	}
	return h
}

// monitorConn is the terminal whose host the monitor reads: the focused
// pane when it is a terminal, else a terminal of its tab on its host.
func (a *App) monitorConn(cn *conn) *conn {
	if cn == nil {
		return nil
	}
	if cn.kind == layout.Terminal && cn.s != nil {
		return cn
	}
	if t := a.ws.ActiveTab(); t != nil {
		for _, p := range t.Panes() {
			if o := a.conns[p.Item.ID]; o != nil && o.kind == layout.Terminal && o.s != nil && o.host.ID == cn.host.ID {
				return o
			}
		}
	}
	return nil
}

// monitorPanel is the focused session's host as its metrics read: the
// system, then a card each for CPU, memory, network, disks, GPUs and
// who is logged in.
func (a *App) monitorPanel(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	cn := a.focused()
	if cn == nil {
		panelNote(c, tok, m.InspectorEmpty())
		return
	}
	h := a.liveHost(cn.host)
	fam := a.termFamily()
	dash := m.MetricsDash()
	tc := a.monitorConn(cn)
	var rd session.Reading
	reason := ""
	connected := false
	if tc != nil {
		connected = tc.s.Status().State == session.Connected
		if col := a.metricsCollector(tc); col != nil {
			rd, reason = col.View()
		}
	}
	if isLocal(h.ID) {
		panelNote(c, tok, m.MonitorLocal())
		return
	}
	if !h.SSH() {
		panelNote(c, tok, m.MonitorStream())
		return
	}
	a.panelCard(c, tok, "sys", icons.Host, hostTint(tok, h), m.SysSection(), "", func() {
		osName, lat, up := dash, dash, dash
		if rd.OSName != "" {
			osName = rd.OSName
		} else if rd.OS != "" {
			osName = rd.OS
		}
		if tc != nil && connected {
			d, ok := tc.s.Latency()
			lat = latencyValue(m, d, ok)
			if !tc.up.IsZero() {
				d := c.Now().Sub(tc.up)
				up = fmtDur(m, d)
				tickDur(c, d)
			}
		}
		kv := []string{m.FieldHost(), a.pv(h.Label()), m.SysOS(), osName, m.SysLatency(), lat, m.SysConnectedFor(), up}
		// A BSD release already names its kernel.
		if rd.Kernel != "" && !strings.Contains(osName, rd.Kernel) {
			kv = append(kv, m.SysKernel(), rd.Kernel)
		}
		facts(c, tok, fam, append(kv, m.SysAddress(), a.pv(h.Target()))...)
	})
	switch {
	case tc == nil:
		panelNote(c, tok, m.MonitorNoTerminal())
		return
	case !a.metricsWanted(h):
		ui.Column(c).Gap(8).Children(func() {
			panelNote(c, tok, m.MonitorOff())
			if pill(c, m.OpenSettings(), false, false, false) {
				a.openSettings(prefMonitoring)
			}
		})
		return
	case !connected:
		panelNote(c, tok, m.MonitorNotConnected())
		return
	case reason != "":
		why := metricsReason(m, reason)
		if why == "" {
			why = m.MetricsUnavailable()
		}
		panelNote(c, tok, why)
		return
	case !rd.CPUOK && !rd.MemOK && !rd.NetOK && len(rd.Vols) == 0:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			panelNote(c, tok, m.MonitorWaiting())
		})
		return
	}
	a.cpuCard(c, m, tok, fam, rd)
	a.memCard(c, m, tok, fam, rd)
	a.netCard(c, m, tok, fam, rd)
	a.diskCard(c, m, tok, fam, rd)
	if len(rd.GPUs) > 0 {
		a.gpuCard(c, m, tok, fam, rd)
	}
	if rd.LoginsOK {
		a.loginCard(c, m, tok, fam, rd)
	}
}

func (a *App) cpuCard(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	note := ""
	if len(rd.Cores) > 0 {
		note = m.CpuCores(len(rd.Cores))
	}
	a.panelCard(c, tok, "cpu", icons.CPU, tok.TintAt(0), "CPU", note, func() {
		pct := m.MetricsDash()
		if rd.CPUOK {
			pct = fmtPct(rd.CPU)
		}
		ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
			big := ui.Text(c, pct).Font(fam).FontSize(theme.Title1).FontWeight(600).SingleLine().Shrink(0)
			if rd.CPUOK {
				big.TextColor(barColor(tok, rd.CPU))
			}
			ui.Column(c).Gap(6).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, m.CpuUsage()).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
				meterBar(c, tok, rd.CPU)
			})
		})
		if len(rd.Cores) > 0 {
			// One bar per core, filled from the bottom.
			ui.Row(c.Key("cores")).Height(32).FillWidth().Gap(2).Children(func() {
				for i, core := range rd.Cores {
					p := m.MetricsDash()
					if core.OK {
						p = fmtPct(core.Pct)
					}
					tip := m.MetricsCore(strconv.Itoa(i), p)
					ui.Column(c).Grow(1).Basis(0).MinWidth(2).FillHeight().Radius(2).Background(tok.Fill).Clip().Justify(ui.End).
						Label(tip).Tooltip(tip).Children(func() {
						if core.OK && core.Pct > 0 {
							ui.Box(c).FillWidth().HeightPercent(float32(min(core.Pct, 100))).Background(barColor(tok, core.Pct))
						}
					})
				}
			})
		}
		load := m.MetricsDash()
		if rd.LoadOK {
			load = fmtLoad(rd.Load1) + "  " + fmtLoad(rd.Load5) + "  " + fmtLoad(rd.Load15)
		}
		ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LoadAverage()).FontSize(theme.Caption).TextColor(tok.TextMuted).Grow(1).Shrink(1).SingleLine()
			ui.Text(c, load).Font(fam).FontSize(theme.Footnote).SingleLine()
		})
	})
}

// usageRow is a meter with its label, its percent and what is used of
// how much under it.
func usageRow(c *ui.Context, tok theme.Tokens, fam, label string, pct float64, ok bool, detail string) {
	ui.Column(c).Gap(5).FillWidth().Children(func() {
		ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).Font(fam).FontSize(theme.Footnote).FontWeight(500).Grow(1).Shrink(1).SingleLine()
			p := "—"
			col := tok.TextMuted
			if ok {
				p, col = fmtPct(pct), barColor(tok, pct)
			}
			ui.Text(c, p).Font(fam).FontSize(theme.Footnote).FontWeight(600).TextColor(col).SingleLine()
		})
		meterBar(c, tok, pct)
		if detail != "" {
			ui.Text(c, detail).Font(fam).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
		}
	})
}

func (a *App) memCard(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	a.panelCard(c, tok, "mem", icons.Memory, tok.TintAt(3), m.MemorySection(), "", func() {
		if !rd.MemOK {
			panelNote(c, tok, m.NoData())
			return
		}
		usageRow(c, tok, fam, m.MemRAM(), rd.MemPct, true, fmtKB(rd.MemUsedKB)+" / "+fmtKB(rd.MemTotalKB))
		if rd.SwapTotalKB > 0 {
			usageRow(c, tok, fam, m.MemSwap(), rd.SwapPct, true, fmtKB(rd.SwapUsedKB)+" / "+fmtKB(rd.SwapTotalKB))
		}
	})
}

// rates is a sent and a received rate, each after its arrow.
func rates(c *ui.Context, tok theme.Tokens, fam string, up, down string) {
	ui.Row(c).Gap(10).Shrink(0).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Sent.SVG).Size(12, 12).TextColor(tok.Success)
			ui.Text(c, up).Font(fam).FontSize(theme.Footnote).TextColor(tok.Success).SingleLine()
		})
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Received.SVG).Size(12, 12).TextColor(tok.Accent)
			ui.Text(c, down).Font(fam).FontSize(theme.Footnote).TextColor(tok.Accent).SingleLine()
		})
	})
}

func (a *App) netCard(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	a.panelCard(c, tok, "net", icons.Network, tok.TintAt(2), m.NetworkSection(), "", func() {
		if len(rd.NICs) == 0 {
			panelNote(c, tok, m.NoData())
			return
		}
		ui.Column(c).Gap(2).FillWidth().Children(func() {
			for _, n := range rd.NICs {
				up, down := m.MetricsDash(), m.MetricsDash()
				if n.OK {
					up, down = fmtRate(n.Up), fmtRate(n.Down)
				}
				line := nicLine(m, n)
				ui.Row(c).Gap(8).FillWidth().Padding(5, 8).Radius(theme.RadiusSmall).Background(tok.Fill).AlignItems(ui.Center).Label(line).Children(func() {
					ui.Text(c, n.Name).Font(fam).FontSize(theme.Footnote).Grow(1).Shrink(1).SingleLine()
					rates(c, tok, fam, up, down)
				})
			}
		})
	})
}

func (a *App) diskCard(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	a.panelCard(c, tok, "disk", icons.Disk, tok.TintAt(1), m.DisksSection(), "", func() {
		if len(rd.Vols) == 0 {
			panelNote(c, tok, m.NoData())
			return
		}
		for _, v := range rd.Vols {
			usageRow(c, tok, fam, v.Mount, v.Pct, true, fmtKB(v.UsedKB)+" / "+fmtKB(v.TotalKB))
		}
	})
}

func (a *App) gpuCard(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	a.panelCard(c, tok, "gpu", icons.GPU, tok.TintAt(4), m.GpuSection(), "", func() {
		for i, g := range rd.GPUs {
			_, mem, temp := gpuBits(m, g)
			usageRow(c, tok, fam, "GPU "+strconv.Itoa(i), float64(g.Util), g.UtilOK, mem+" · "+temp)
		}
		if rd.GPUProcsOK {
			gpuProcs(c, m, tok, fam, rd)
		}
	})
}

// gpuProcs is the processes holding GPU memory: each with its GPU, its
// pid and how much it holds.
func gpuProcs(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	if rd.GPUProcCount == 0 {
		panelNote(c, tok, m.GpuProcsNone())
		return
	}
	ui.Text(c, m.GpuProcesses(rd.GPUProcCount)).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
	ui.Column(c).Gap(2).FillWidth().Children(func() {
		for _, p := range rd.GPUProcs {
			name, mem := m.MetricsDash(), m.MetricsDash()
			if p.Name != "" {
				name = p.Name
			}
			if p.MemOK {
				mem = fmtKB(uint64(p.MemMiB) * 1024)
			}
			meta := m.GpuProcPid(strconv.FormatUint(p.PID, 10))
			if p.GPU >= 0 {
				meta = "GPU " + strconv.Itoa(p.GPU) + " · " + meta
			}
			ui.Row(c).Gap(8).FillWidth().Padding(5, 8).Radius(theme.RadiusSmall).Background(tok.Fill).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(1).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, name).Font(fam).FontSize(theme.Footnote).SingleLine()
					ui.Text(c, meta).Font(fam).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
				})
				ui.Text(c, mem).Font(fam).FontSize(theme.Footnote).FontWeight(600).SingleLine().Shrink(0)
			})
		}
	})
}

// loginCard is who is logged in on the host: each session's user and
// line, where it came from and since when, as the server says.
func (a *App) loginCard(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	a.panelCard(c, tok, "logins", icons.Logins, tok.TintAt(5), m.LoginSection(), m.LoginCount(rd.LoginCount), func() {
		if len(rd.Logins) == 0 {
			panelNote(c, tok, m.LoginsNone())
			return
		}
		ui.Column(c).Gap(2).FillWidth().Children(func() {
			for _, l := range rd.Logins {
				var detail []string
				if l.From != "" {
					detail = append(detail, a.pv(l.From))
				}
				if l.When != "" {
					detail = append(detail, l.When)
				}
				ui.Row(c).Gap(8).FillWidth().Padding(5, 8).Radius(theme.RadiusSmall).Background(tok.Fill).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Gap(1).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						ui.Text(c, a.pv(l.User)).FontSize(theme.Footnote).FontWeight(500).SingleLine()
						if len(detail) > 0 {
							ui.Text(c, strings.Join(detail, " · ")).Font(fam).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
						}
					})
					ui.Text(c, l.Line).Font(fam).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Shrink(0)
				})
			}
		})
	})
}

// authText says how h logs in.
func (a *App) authText(m locales.Messages, h assets.Host) string {
	switch h.Auth {
	case assets.AuthPassword:
		return m.CredKindPassword()
	case assets.AuthKey:
		return m.CredKindKey()
	case assets.AuthAgent:
		return m.AuthAgent()
	case assets.AuthAsk:
		if !h.SSH() {
			return m.AuthAsk()
		}
		return a.askLabel(m)
	case assets.AuthCredential:
		if cr, ok := a.store.Credential(h.CredentialID); ok {
			return m.AuthLabelCredential(cr.Name)
		}
	case assets.AuthNone:
		return m.AuthManual()
	}
	return m.MetricsDash()
}

// hostPanel is the host of the focused session (or the one chosen in the
// sidebar): who it is, how it is reached, and what to do with it.
func (a *App) hostPanel(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	var h assets.Host
	ok := false
	if cn := a.focused(); cn != nil {
		h, ok = a.liveHost(cn.host), true
	} else if id := a.selectedHost(); id != "" {
		h, ok = a.store.Get(id)
	}
	if !ok {
		panelNote(c, tok, m.InspectorEmpty())
		return
	}
	temp, local := isTemp(h.ID), isLocal(h.ID)
	a.contentCard(c.Key("hosthero"), tok).FillWidth().Padding(theme.Space(3)).Gap(theme.Space(3)).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
			hostAvatar(c, tok, h, 36)
			ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, a.pv(h.Label())).FontSize(theme.Title3).FontWeight(600).SingleLine().Shrink(1)
					if temp {
						tempBadge(c, m, tok)
					}
				})
				ui.Text(c, a.pv(h.Target())).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			})
		})
		ui.Row(c).Gap(2).AlignItems(ui.Center).FillWidth().Children(func() {
			if pill(c, m.Connect(), true, false, false) {
				a.Connect(h.ID, openAt{})
			}
			ui.Spacer(c)
			if (IconButton{Icon: icons.Folder, Label: m.OpenFiles(), Disabled: local || !h.SSH()}).Clicked(c, "hfiles") {
				a.OpenFiles(h.ID, openAt{}, nil)
			}
			fav := m.AddFavorite()
			if h.Favorite {
				fav = m.RemoveFavorite()
			}
			if (IconButton{Icon: icons.Star, On: icons.StarOn, Label: fav, Active: h.Favorite, Toggle: true, Disabled: temp || local}).Clicked(c, "hfav") {
				a.store.SetFavorite(h.ID, !h.Favorite)
			}
			if (IconButton{Icon: icons.Edit, Label: m.EditHost(), Disabled: temp || local}).Clicked(c, "hedit") {
				hh := h
				a.openEditor(&hh)
			}
		})
	})
	if local {
		panelNote(c, tok, m.LocalTerminalNote())
		return
	}
	dash := m.MetricsDash()
	or := func(s string) string {
		if s == "" {
			return dash
		}
		return s
	}
	proxy := m.ProxyNone()
	if px, ok := a.proxyOf(h); ok {
		proxy = px.Name
	}
	var jumps []string
	path, _ := a.store.JumpPath(h.ID)
	for _, j := range path {
		jumps = append(jumps, a.pv(j.Label()))
	}
	panes := 0
	for _, o := range a.conns {
		if o.fw == nil && o.host.ID == h.ID {
			panes++
		}
	}
	a.panelCard(c, tok, "hostinfo", icons.Proxy, tok.TintAt(7), m.ConnectionSection(), "", func() {
		if !h.SSH() {
			a.streamFacts(c, m, tok, h, proxy, panes, or)
			return
		}
		facts(c, tok, "",
			m.SysAddress(), a.pv(h.Address), m.FieldPort(), strconv.Itoa(h.Port),
			m.FieldUser(), or(h.User), m.FieldGroup(), or(h.Group),
			m.FieldProxy(), proxy, m.OpenPanes(), strconv.Itoa(panes))
		ui.Column(c).Gap(2).FillWidth().Children(func() {
			ui.Text(c, m.FieldAuth()).FontSize(theme.Caption).TextColor(tok.TextMuted)
			ui.Text(c, a.authText(m, h)).FontSize(theme.Footnote)
		})
		ui.Column(c).Gap(2).FillWidth().Children(func() {
			ui.Text(c, m.JumpHosts()).FontSize(theme.Caption).TextColor(tok.TextMuted)
			ui.Text(c, or(strings.Join(jumps, " → "))).FontSize(theme.Footnote)
		})
	})
	a.renewalPanel(c, m, tok, h)
}

// renewalPanel is the card of the host's provider, subscription and note,
// shown while it keeps any of them.
func (a *App) renewalPanel(c *ui.Context, m locales.Messages, tok theme.Tokens, h assets.Host) {
	if h.Provider == "" && !h.Renewal.On() && h.Note == "" {
		return
	}
	a.panelCard(c, tok, "hostrenewal", icons.Renewal, tok.TintAt(2), m.RenewalSection(), "", func() {
		item := func(label, value string, col ui.Color) {
			ui.Column(c).Gap(2).FillWidth().Children(func() {
				ui.Text(c, label).FontSize(theme.Caption).TextColor(tok.TextMuted)
				t := ui.Text(c, value).FontSize(theme.Footnote)
				if col != (ui.Color{}) {
					t.TextColor(col)
				}
			})
		}
		if h.Provider != "" {
			item(m.FieldProvider(), h.Provider, ui.Color{})
		}
		if line, col, ok := renewalLine(m, tok, h.Renewal, c.Now()); ok {
			item(m.RenewalNext(), line, col)
			item(m.RenewalRemind(), remindLabel(m, h.Renewal.Remind), ui.Color{})
		}
		if h.Note != "" {
			item(m.FieldNote(), a.pv(h.Note), ui.Color{})
		}
	})
}
