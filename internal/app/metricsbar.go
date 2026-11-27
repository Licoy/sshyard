package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// metricsCompact is the width of the metrics bar under which its items
// show their icons without their words, which their tips still name.
const metricsCompact = 760

// metricsProbe is how often a visible bar times the keepalive.
const metricsProbe = 5 * time.Second

// metricsWanted reports whether this host shows a bar. The global switch
// hides every host. Off hides one host while the global switch is on.
func (a *App) metricsWanted(h assets.Host) bool {
	if !a.settings.MetricsEnabled || h.Metrics == assets.MetricsOff || !hasSSH(h) {
		return false
	}
	return true
}

// commitMetricsMount saves the extra mount after the monitoring settings
// close (or another settings page shows) on a field that still had the
// caret. On the page itself the field saves on Enter and when focus
// leaves it.
func (a *App) commitMetricsMount() {
	if (a.page == pageSettings && a.prefNow() == prefMonitoring) || !a.metMountFocus {
		return
	}
	a.metMountFocus = false
	if a.metMount == a.settings.MetricsMount {
		return
	}
	s := a.settings
	s.MetricsMount = a.metMount
	if err := a.ApplySettings(s); err != nil {
		a.toast(func(m locales.Messages) ui.Toast {
			return ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"}
		})
	}
}

func (a *App) minimizedNow() bool {
	a.metMu.Lock()
	defer a.metMu.Unlock()
	return a.minimized
}

// SetMinimized pauses every collector while the window is minimised.
// Stop only cancels, so a server that never closes the channel cannot
// stall this caller. Probes reset on the next frame, because sessions
// live on the UI thread.
func (a *App) SetMinimized(min bool) {
	a.metMu.Lock()
	a.minimized = min
	var cols []*session.Collector
	if min {
		for _, c := range a.metrics {
			cols = append(cols, c)
		}
	}
	a.metMu.Unlock()
	if min {
		for _, c := range cols {
			c.Stop()
		}
	}
	a.post(func() { a.syncMetrics() })
}

// stopCollectors ends every collector. It does not touch sessions.
// Each cancel is immediate. The wait is bounded so a server that never
// closes the channel cannot hold the window open.
func (a *App) stopCollectors() {
	a.metMu.Lock()
	cols := a.metrics
	a.metrics = map[string]*session.Collector{}
	a.metMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, c := range cols {
		c.Stop()
	}
	for _, c := range cols {
		c.Wait(ctx)
	}
}

// syncMetrics starts one collector per host that has a visible terminal
// and stops it when none remain visible. The collector does not take a
// pool reference. Call it on the UI thread.
func (a *App) syncMetrics() {
	type want struct {
		client   *ssh.Client
		interval int
		mount    string
		show     bool
	}
	byKey := map[string]*want{}
	var order []string

	active := map[int]bool{}
	// The active tab of each window showing its sessions; the main one
	// shows none while minimized.
	for _, w := range a.wins {
		if w.page != pageWorkspace || w.gone || w == a.main && a.minimizedNow() {
			continue
		}
		if t := w.ws.ActiveTab(); t != nil {
			for _, p := range t.Panes() {
				active[p.Item.ID] = true
			}
		}
	}
	for _, cn := range a.conns {
		if cn == nil || cn.s == nil || cn.fw != nil || cn.kind != layout.Terminal {
			continue
		}
		h := cn.host
		if cur, ok := a.host(cn.host.ID); ok {
			h = cur
		}
		visible := a.metricsWanted(h) && active[cn.id] && cn.s.Status().State == session.Connected
		if visible {
			cn.s.SetProbe(metricsProbe)
		} else {
			cn.s.SetProbe(0)
		}
		key := cn.s.Config().Key
		w := byKey[key]
		if w == nil {
			w = &want{}
			byKey[key] = w
			order = append(order, key)
		}
		if visible {
			if cl := cn.s.Client(); cl != nil {
				w.show = true
				w.client = cl
				w.interval = a.settings.MetricsInterval
				w.mount = a.settings.MetricsMount
			}
		}
	}

	a.metMu.Lock()
	if a.metrics == nil {
		a.metrics = map[string]*session.Collector{}
	}
	var stop []*session.Collector
	type job struct {
		c *session.Collector
		w *want
	}
	var jobs []job
	for k, c := range a.metrics {
		if byKey[k] == nil {
			stop = append(stop, c)
			delete(a.metrics, k)
		}
	}
	for _, k := range order {
		w := byKey[k]
		c := a.metrics[k]
		if !w.show || w.client == nil {
			if c != nil {
				stop = append(stop, c)
			}
			continue
		}
		if c == nil {
			c = session.NewCollector()
			a.metrics[k] = c
		}
		jobs = append(jobs, job{c, w})
	}
	a.metMu.Unlock()

	for _, c := range stop {
		c.Stop()
	}
	for _, j := range jobs {
		// A minimize between the scan and here must not start a command.
		if a.minimizedNow() {
			j.c.Stop()
			continue
		}
		j.c.Start(j.w.client, j.w.interval, j.w.mount)
	}
}

func (a *App) metricsCollector(cn *conn) *session.Collector {
	if cn == nil || cn.s == nil {
		return nil
	}
	a.metMu.Lock()
	defer a.metMu.Unlock()
	return a.metrics[cn.s.Config().Key]
}

// hideMetrics turns the bar off for one host and keeps that choice.
// A failed save leaves the host unchanged and says so.
func (a *App) hideMetrics(id string) {
	h, ok := a.host(id)
	if !ok {
		return
	}
	h.Metrics = assets.MetricsOff
	if isTemp(id) {
		a.temps[id] = h
	} else if _, err := a.store.Update(h); err != nil {
		a.toast(func(m locales.Messages) ui.Toast {
			return ui.Toast{Title: m.MetricsHideFailed(err.Error()), Type: "error"}
		})
		return
	}
	for _, cn := range a.conns {
		if cn.host.ID == id {
			cn.host.Metrics = assets.MetricsOff
		}
	}
}

func metricsIndex(m assets.MetricsMode) int {
	switch m {
	case assets.MetricsOn:
		return 1
	case assets.MetricsOff:
		return 2
	default:
		return 0
	}
}

func metricsMode(i int) assets.MetricsMode {
	switch i {
	case 1:
		return assets.MetricsOn
	case 2:
		return assets.MetricsOff
	default:
		return assets.MetricsFollow
	}
}

func intervalIndex(n int) int {
	switch session.MetricsInterval(n) {
	case 1:
		return 0
	case 5:
		return 2
	case 10:
		return 3
	default:
		return 1
	}
}

// meterColor is the color of a usage at pct: success below 60%, warning
// below 80% and danger from 80%; muted when it is not known.
func meterColor(tok theme.Tokens, pct float64, ok bool) ui.Color {
	switch {
	case !ok:
		return tok.TextMuted
	case pct >= 80:
		return tok.Danger
	case pct >= 60:
		return tok.Warning
	}
	return tok.Success
}

// latencyColor is success below 100 ms, warning below 300 ms and danger
// from 300 ms; muted when it is not known.
func latencyColor(tok theme.Tokens, d time.Duration, ok bool) ui.Color {
	switch {
	case !ok:
		return tok.TextMuted
	case d >= 300*time.Millisecond:
		return tok.Danger
	case d >= 100*time.Millisecond:
		return tok.Warning
	}
	return tok.Success
}

// loadPct is the load over the last minute as a share of the CPUs, the
// percent a usage is colored by, when the cores are known.
func loadPct(rd session.Reading) (float64, bool) {
	if !rd.LoadOK || len(rd.Cores) == 0 {
		return 0, false
	}
	return rd.Load1 / float64(len(rd.Cores)) * 100, true
}

func fmtKB(kb uint64) string {
	const m = 1024
	const g = 1024 * 1024
	if kb >= g {
		return fmt.Sprintf("%.1fG", float64(kb)/float64(g))
	}
	if kb >= m {
		return fmt.Sprintf("%.1fM", float64(kb)/float64(m))
	}
	return fmt.Sprintf("%dK", kb)
}

func fmtPct(p float64) string { return fmt.Sprintf("%.0f%%", p) }

func fmtLoad(v float64) string { return fmt.Sprintf("%.2f", v) }

func fmtRate(b float64) string {
	if b < 0 {
		b = 0
	}
	const k = 1024.0
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1fG/s", b/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1fM/s", b/(k*k))
	case b >= k:
		return fmt.Sprintf("%.1fK/s", b/k)
	default:
		return fmt.Sprintf("%.0fB/s", b)
	}
}

func metricsReason(m locales.Messages, code string) string {
	switch code {
	case session.ReasonExec:
		return m.MetricsReasonExec()
	case session.ReasonShell:
		return m.MetricsReasonShell()
	case session.ReasonParse:
		return m.MetricsReasonParse()
	default:
		return ""
	}
}

func memoryText(m locales.Messages, rd session.Reading) string {
	if !rd.MemOK {
		d := m.MetricsDash()
		return m.MetricsMemory(d, d, d, d)
	}
	return m.MetricsMemory(fmtKB(rd.MemUsedKB), fmtKB(rd.MemTotalKB), fmtKB(rd.SwapUsedKB), fmtKB(rd.SwapTotalKB))
}

func loadText(m locales.Messages, rd session.Reading) string {
	if !rd.LoadOK {
		d := m.MetricsDash()
		return m.MetricsLoad(d, d, d)
	}
	return m.MetricsLoad(fmtLoad(rd.Load1), fmtLoad(rd.Load5), fmtLoad(rd.Load15))
}

func latencyText(m locales.Messages, d time.Duration, ok bool) string {
	return m.MetricsLatencyLabel(latencyValue(m, d, ok))
}

// latencyValue is the latency alone, as "12 ms", or a dash.
func latencyValue(m locales.Messages, d time.Duration, ok bool) string {
	if !ok {
		return m.MetricsDash()
	}
	num := "<1"
	if d >= time.Millisecond {
		num = strconv.FormatInt(d.Milliseconds(), 10)
	}
	return m.MetricsLatency(num)
}

func rootVol(vols []session.VolUse) (session.VolUse, bool) {
	for _, v := range vols {
		if v.Mount == "/" {
			return v, true
		}
	}
	if len(vols) > 0 {
		return vols[0], true
	}
	return session.VolUse{}, false
}

func maxGPU(gs []session.GPU) (float64, bool) {
	var pct float64
	ok := false
	for _, g := range gs {
		if !g.UtilOK {
			continue
		}
		u := float64(g.Util)
		if !ok || u > pct {
			pct, ok = u, true
		}
	}
	return pct, ok
}

func (a *App) metricsItemMenu(el ui.Element, cn *conn, m locales.Messages) {
	a.contextMenu(el, func() []menuEntry {
		return []menuEntry{
			{Icon: icons.Privacy, Label: m.MetricsHide(), Do: func() { a.hideMetrics(cn.host.ID) }},
			{Icon: icons.Settings, Label: m.OpenSettings(), Do: func() { a.openSettings(prefMonitoring) }},
		}
	})
}

// metricPart is a figure of the metrics bar: its icon, its word (muted)
// and its value, the icon and the value in the color of its level.
type metricPart struct {
	icon        *icons.Icon
	word, value string
	col         ui.Color
}

// metric is an item of the metrics bar: its figures, its whole text for
// assistive technology and the tip, and, with open, the popover it opens.
type metric struct {
	key, label, tip string
	parts           []metricPart
	open            *bool
	pop             func()
}

// metricItem shows it, its menu that of the bar; one that opens a popover
// lights in a rounded shape under the pointer and while open. It keeps
// the width the item took in the last frame.
func (a *App) metricItem(c *ui.Context, cn *conn, m locales.Messages, tok theme.Tokens, fam string, it metric) {
	var el ui.Element
	if it.open != nil {
		el = ui.ButtonBase(c.Key(it.key)).Cursor(ui.CursorPointer)
		switch {
		case *it.open || el.Pressed():
			el.Background(tok.Press)
		case el.Hovered():
			el.Background(tok.Hover)
		}
		if el.Clicked() {
			was := *it.open
			closeMetricPops(cn)
			*it.open = !was
		}
	} else {
		el = ui.Row(c.Key(it.key))
	}
	el.Label(it.label).Tooltip(it.tip).Height(18).Padding(0, 5).Gap(8).AlignItems(ui.Center).Radius(theme.RadiusSmall).Shrink(0)
	a.metricsItemMenu(el, cn, m)
	el.Children(func() { metricParts(c, tok, fam, it.parts, cn.metCompact) })
	if it.open != nil {
		ui.Popover(c, el, it.open, func() {
			ui.Column(c).Padding(8, 10).Gap(6).Children(it.pop)
		})
	}
	if w := el.Bounds().W; w > 0 && cn.metShown == cn.metCompact && w != cn.metW[it.key] {
		if cn.metW == nil {
			cn.metW = map[string]float32{}
		}
		cn.metW[it.key] = w
		c.Invalidate()
	}
}

func closeMetricPops(cn *conn) {
	cn.metCPU, cn.metDisk, cn.metNet, cn.metGPU, cn.metLoad, cn.metMore = false, false, false, false, false, false
}

// metricParts shows the figures of an item; compact drops the words of
// those with an icon.
func metricParts(c *ui.Context, tok theme.Tokens, fam string, parts []metricPart, compact bool) {
	for _, p := range parts {
		ui.Row(c).Gap(4).AlignItems(ui.Center).PassThrough().Children(func() {
			if p.icon != nil {
				ui.Icon(c, p.icon.SVG).Size(12, 12).Shrink(0).TextColor(p.col)
			}
			if p.word != "" && (!compact || p.icon == nil) {
				ui.Text(c, p.word).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine()
			}
			ui.Text(c, p.value).Font(fam).FontSize(theme.Caption).FontWeight(500).TextColor(p.col).SingleLine()
		})
	}
}

// metricsMoreW is the width of the more button of the metrics bar.
const metricsMoreW = 20

// metricsFit is how many items of widths ws, in order, the bar shows in
// avail: all of them when they fit, otherwise those that fit beside the
// more button. An item not measured yet (0) is shown, to be measured.
func metricsFit(ws []float32, avail, gap float32) int {
	var total float32
	for i, w := range ws {
		if i > 0 {
			total += gap
		}
		total += w
	}
	if total <= avail {
		return len(ws)
	}
	room := avail - metricsMoreW - gap
	var used float32
	for i, w := range ws {
		if i > 0 {
			used += gap
		}
		used += w
		if used > room {
			return i
		}
	}
	return len(ws)
}

// metricsMore is the more button of the metrics bar, its popover the
// items the bar has no room for, with their words, each tip its own. One
// with details opens them under it, one at a time, as its own popover
// does on the bar.
func (a *App) metricsMore(c *ui.Context, cn *conn, m locales.Messages, tok theme.Tokens, fam string, hidden []metric) {
	el := IconButton{Icon: icons.More, Label: m.MetricsMore(), Size: Tiny, Active: cn.metMore}.Show(c, "mmore")
	el.Shrink(0)
	a.metricsItemMenu(el, cn, m)
	if el.Clicked() {
		was := cn.metMore
		closeMetricPops(cn)
		cn.metMore = !was
		cn.metMoreItem = ""
	}
	ui.Popover(c, el, &cn.metMore, func() {
		ui.Column(c).Padding(8, 10).Gap(6).Children(func() {
			for _, it := range hidden {
				open := it.pop != nil && cn.metMoreItem == it.key
				var row ui.Element
				if it.pop != nil {
					row = ui.ButtonBase(c.Key("mm:" + it.key)).Cursor(ui.CursorPointer).Expanded(open)
					switch {
					case open || row.Pressed():
						row.Background(tok.Press)
					case row.Hovered():
						row.Background(tok.Hover)
					}
					if row.Clicked() {
						open = !open
						cn.metMoreItem = ""
						if open {
							cn.metMoreItem = it.key
						}
					}
				} else {
					row = ui.Row(c.Key("mm:" + it.key))
				}
				row.Label(it.label).Tooltip(it.tip).Height(18).Padding(0, 5).Gap(8).AlignItems(ui.Center).Radius(theme.RadiusSmall)
				row.Children(func() { metricParts(c, tok, fam, it.parts, false) })
				if open {
					ui.Column(c.Key("mmd:"+it.key)).Padding(2, 5, 4, 5).Gap(6).Children(it.pop)
				}
			}
		})
	})
}

func (a *App) metricsBar(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	var rd session.Reading
	reason := ""
	if col := a.metricsCollector(cn); col != nil {
		rd, reason = col.View()
	}
	fam := a.termFamily()
	ui.Box(c).Height(1).Shrink(0).FillWidth().Background(tok.Line)
	row := ui.Row(c.Key("metrics")).MinHeight(24).Shrink(0).FillWidth().Padding(2, 6, 2, 6).Gap(2).AlignItems(ui.Center).
		FontSize(theme.Caption).Label(m.MetricsBar()).ClipX()
	if w := row.Bounds().W; w > 0 && (w < metricsCompact) != cn.metCompact {
		// Measured anew: lay the items out again with or without words,
		// and measure them again.
		cn.metCompact = w < metricsCompact
		cn.metW = nil
		c.Invalidate()
	}
	a.metricsItemMenu(row, cn, m)
	defer func() { cn.metShown = cn.metCompact }()
	row.Children(func() {
		if reason != "" {
			why := metricsReason(m, reason)
			label := m.MetricsUnavailable()
			tip := label
			if why != "" {
				label = m.MetricsUnavailableReason(m.MetricsUnavailable(), why)
				tip = why
			}
			ui.Icon(c, icons.Warning.SVG).Size(12, 12).Shrink(0).TextColor(tok.Warning).Margin(0, 4, 0, 4)
			tx := ui.Text(c, m.MetricsUnavailable()).FontSize(theme.Caption).TextColor(tok.TextMuted).SingleLine().Grow(1).Shrink(1).Label(label).Tooltip(tip)
			a.metricsItemMenu(tx, cn, m)
			a.latencyItem(c, cn, tok, m, fam)
			return
		}
		dash := m.MetricsDash()
		cpuPct := dash
		if rd.CPUOK {
			cpuPct = fmtPct(rd.CPU)
		}
		cpu := m.MetricsCPU(cpuPct)
		var items []metric
		items = append(items, metric{key: "mcpu", label: cpu, tip: cpu, open: &cn.metCPU,
			parts: []metricPart{{icons.CPU, m.BarCPU(), cpuPct, meterColor(tok, rd.CPU, rd.CPUOK)}},
			pop:   func() { a.cpuPop(c, m, tok, fam, rd) }})

		mem := memoryText(m, rd)
		used, swap := dash, ""
		if rd.MemOK {
			used = fmtKB(rd.MemUsedKB) + "/" + fmtKB(rd.MemTotalKB)
			if rd.SwapTotalKB > 0 {
				swap = fmtKB(rd.SwapUsedKB) + "/" + fmtKB(rd.SwapTotalKB)
			}
		}
		parts := []metricPart{{icons.Memory, m.BarMemory(), used, meterColor(tok, rd.MemPct, rd.MemOK)}}
		if swap != "" {
			parts = append(parts, metricPart{nil, m.BarSwap(), swap, meterColor(tok, rd.SwapPct, true)})
		}
		items = append(items, metric{key: "mmem", label: mem, tip: mem, parts: parts})

		diskPct := dash
		diskOK := false
		var diskHot float64
		if v, ok := rootVol(rd.Vols); ok {
			diskPct = fmtPct(v.Pct)
			diskHot, diskOK = v.Pct, true
		}
		disk := m.MetricsDisk(diskPct)
		items = append(items, metric{key: "mdisk", label: disk, tip: disk, open: &cn.metDisk,
			parts: []metricPart{{icons.Disk, m.BarDisk(), diskPct, meterColor(tok, diskHot, diskOK)}},
			pop:   func() { a.diskPop(c, m, fam, rd) }})

		up, down := dash, dash
		netCol := tok.TextMuted
		if rd.NetOK {
			up, down, netCol = fmtRate(rd.NetUp), fmtRate(rd.NetDown), tok.Accent
		}
		netLabel := m.MetricsNetLabel(up, down)
		items = append(items, metric{key: "mnet", label: netLabel, tip: a.netTip(m, rd, netLabel), open: &cn.metNet,
			parts: []metricPart{{icons.Sent, "", up, netCol}, {icons.Received, "", down, netCol}},
			pop:   func() { a.netPop(c, m, fam, rd) }})

		// The load as a share of the CPUs; its three averages in the
		// popover, and as the figure itself where the CPUs are unknown.
		load := loadText(m, rd)
		lp, lok := loadPct(rd)
		loads := dash
		loadCol := meterColor(tok, lp, lok)
		switch {
		case lok:
			loads = fmtPct(lp)
		case rd.LoadOK:
			loads, loadCol = fmtLoad(rd.Load1), tok.Text
		}
		items = append(items, metric{key: "mload", label: load, tip: load, open: &cn.metLoad,
			parts: []metricPart{{icons.Load, m.BarLoad(), loads, loadCol}},
			pop:   func() { a.loadPop(c, m, fam, rd) }})

		if len(rd.GPUs) > 0 {
			gp := dash
			gok := false
			var gpct float64
			if p, ok := maxGPU(rd.GPUs); ok {
				gp = fmtPct(p)
				gpct, gok = p, true
			}
			gpu := m.MetricsGPU(gp)
			items = append(items, metric{key: "mgpu", label: gpu, tip: gpu, open: &cn.metGPU,
				parts: []metricPart{{icons.GPU, m.BarGPU(), gp, meterColor(tok, gpct, gok)}},
				pop:   func() { a.gpuPop(c, m, fam, rd) }})
		}

		// As many items as the bar has room for, beside the latency at
		// its end and its padding; the rest in the more popover.
		const gap = 2
		n := len(items)
		if w := row.Bounds().W; w > 0 {
			ws := make([]float32, len(items))
			for i, it := range items {
				ws[i] = cn.metW[it.key]
			}
			n = metricsFit(ws, w-12-cn.metW["mlat"]-2*gap, gap)
		}
		for _, it := range items[:n] {
			a.metricItem(c, cn, m, tok, fam, it)
		}
		hidden := items[n:]
		for _, it := range hidden {
			if it.open != nil {
				*it.open = false
			}
		}
		if len(hidden) > 0 {
			a.metricsMore(c, cn, m, tok, fam, hidden)
		} else {
			cn.metMore = false
		}
		ui.Spacer(c)
		a.latencyItem(c, cn, tok, m, fam)
	})
}

func (a *App) latencyItem(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages, fam string) {
	d, ok := cn.s.Latency()
	text := latencyText(m, d, ok)
	a.metricItem(c, cn, m, tok, fam, metric{key: "mlat", label: text, tip: text,
		parts: []metricPart{{icons.Monitor, "", latencyValue(m, d, ok), latencyColor(tok, d, ok)}}})
}

func (a *App) cpuPop(c *ui.Context, m locales.Messages, tok theme.Tokens, fam string, rd session.Reading) {
	ui.Text(c, m.MetricsCores()).Font(fam).FontSize(theme.Caption).FontWeight(600)
	if len(rd.Cores) == 0 {
		ui.Text(c, m.MetricsNoCores()).Font(fam).FontSize(theme.Caption).Label(m.MetricsNoCores())
		return
	}
	for i, core := range rd.Cores {
		pct := m.MetricsDash()
		col := meterColor(tok, 0, false)
		if core.OK {
			pct = fmtPct(core.Pct)
			col = meterColor(tok, core.Pct, true)
		}
		label := m.MetricsCore(strconv.Itoa(i), pct)
		w := float32(4)
		if core.OK && core.Pct > 0 {
			p := core.Pct
			if p > 100 {
				p = 100
			}
			w = 4 + 76*float32(p)/100
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).Font(fam).FontSize(theme.Caption).TextColor(col).Label(label)
			ui.Box(c).Width(w).Height(6).Radius(3).Background(col)
		})
	}
}

func (a *App) diskPop(c *ui.Context, m locales.Messages, fam string, rd session.Reading) {
	ui.Text(c, m.MetricsDiskTitle()).Font(fam).FontSize(theme.Caption).FontWeight(600)
	if len(rd.Vols) == 0 {
		ui.Text(c, m.MetricsDash()).Font(fam).FontSize(theme.Caption)
		return
	}
	for _, v := range rd.Vols {
		line := m.MetricsVol(v.Mount, fmtKB(v.UsedKB), fmtKB(v.TotalKB), fmtPct(v.Pct))
		ui.Text(c, line).Font(fam).FontSize(theme.Caption).Label(line)
	}
}

// loadPop lists the load averages over 1, 5 and 15 minutes, and how many
// CPUs share them.
func (a *App) loadPop(c *ui.Context, m locales.Messages, fam string, rd session.Reading) {
	ui.Text(c, m.MetricsLoadTitle()).Font(fam).FontSize(theme.Caption).FontWeight(600)
	if !rd.LoadOK {
		ui.Text(c, m.MetricsDash()).Font(fam).FontSize(theme.Caption)
		return
	}
	for _, l := range []struct {
		min int
		v   float64
	}{{1, rd.Load1}, {5, rd.Load5}, {15, rd.Load15}} {
		line := m.MetricsLoadLine(l.min, fmtLoad(l.v))
		ui.Text(c, line).Font(fam).FontSize(theme.Caption).Label(line)
	}
	if n := len(rd.Cores); n > 0 {
		line := m.MetricsLoadCPUs(n)
		ui.Text(c, line).Font(fam).FontSize(theme.Caption).TextColor(currentTokens(c).TextMuted).Label(line)
	}
}

func (a *App) netTip(m locales.Messages, rd session.Reading, label string) string {
	if len(rd.NICs) == 0 {
		return label
	}
	tip := ""
	for i, n := range rd.NICs {
		if i > 0 {
			tip += "\n"
		}
		tip += nicLine(m, n)
	}
	return tip
}

func nicLine(m locales.Messages, n session.NICRate) string {
	up, down := m.MetricsDash(), m.MetricsDash()
	if n.OK {
		up, down = fmtRate(n.Up), fmtRate(n.Down)
	}
	return m.MetricsNIC(n.Name, up, down)
}

func (a *App) netPop(c *ui.Context, m locales.Messages, fam string, rd session.Reading) {
	ui.Text(c, m.MetricsNetTitle()).Font(fam).FontSize(theme.Caption).FontWeight(600)
	if len(rd.NICs) == 0 {
		ui.Text(c, m.MetricsDash()).Font(fam).FontSize(theme.Caption)
		return
	}
	for _, n := range rd.NICs {
		line := nicLine(m, n)
		ui.Text(c, line).Font(fam).FontSize(theme.Caption).Label(line)
	}
}

func gpuBits(m locales.Messages, g session.GPU) (util, mem, temp string) {
	util, mem, temp = m.MetricsDash(), m.MetricsDash(), m.MetricsDash()
	if g.UtilOK {
		util = fmtPct(float64(g.Util))
	}
	if g.MemOK {
		mem = strconv.FormatInt(g.MemUsed, 10) + "/" + strconv.FormatInt(g.MemTotal, 10) + " MiB"
	}
	if g.TempOK {
		temp = strconv.FormatInt(g.Temp, 10) + "°C"
	}
	return util, mem, temp
}

func (a *App) gpuPop(c *ui.Context, m locales.Messages, fam string, rd session.Reading) {
	ui.Text(c, m.MetricsGPUTitle()).Font(fam).FontSize(theme.Caption).FontWeight(600)
	for _, g := range rd.GPUs {
		util, mem, temp := gpuBits(m, g)
		line := m.MetricsGPULine(util, mem, temp)
		ui.Text(c, line).Font(fam).FontSize(theme.Caption).Label(line)
	}
}
