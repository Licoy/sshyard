package app

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func (a *App) loadLayout() {
	file := a.layoutPath()
	if file == "" || len(a.allTabs()) > 0 {
		return
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var snap layoutSnap
	if err == nil {
		err = json.Unmarshal(b, &snap)
	}
	if err == nil && legacyLayout(b) {
		// Legacy AI-only files are not sessions; start recording the current workspace.
		a.rememberLayoutBaseline()
		return
	}
	if err != nil || !validLayout(snap) {
		a.layoutBlocked = true
		a.layoutNotice(true)
		log.Printf("sshyard: workspace layout read failed or unsupported version")
		return
	}
	a.restoreGroupSections(snap.Sections)
	if !a.settings.RememberLayout {
		a.rememberLayoutBaseline()
		return
	}
	a.restoring = true
	defer func() { a.restoring = false }()
	mapping := map[int]*layout.Node{}
	links := map[int]int{}
	skipped := false
	a.restoreTabs(a.main, snap.Tabs, snap.Active, mapping, links, &skipped)
	var opened []*winState
	for _, ws := range snap.Windows {
		a.nextWin++
		w := newWinState(a.nextWin, a.main.ws.Sibling())
		if id, ok := windowID(ws.Key); ok && !slices.ContainsFunc(a.wins, func(x *winState) bool { return x.id == id }) {
			w = newWinState(id, w.ws)
			a.nextWin = max(a.nextWin, id)
		}
		if a.restoreTabs(w, ws.Tabs, ws.Active, mapping, links, &skipped) {
			a.wins = append(a.wins, w)
			opened = append(opened, w)
		}
	}
	for id, link := range links {
		ai := a.conns[id]
		if p := mapping[link]; p != nil {
			term := a.conns[p.Item.ID]
			if term != nil && term.kind == layout.Terminal && term.host.ID == ai.host.ID {
				ai.ai.link = term.id
			}
		}
	}
	for _, key := range []string{"fav", "none", "fwd"} {
		if open, ok := snap.Sections[key]; ok {
			a.sections[key] = &open
		}
	}
	for name, w := range snap.Widths {
		if k, ok := railKind(name); ok && w > 0 && w < 1e5 {
			if a.railWidths == nil {
				a.railWidths = map[layout.Kind]float32{}
			}
			a.railWidths[k] = w
		}
	}
	if skipped {
		a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.LayoutRestoreSkipped(), Type: "warning"} })
	}
	// All tabs and references exist before any fresh session starts.
	for _, c := range a.conns {
		if c.s == nil {
			continue
		}
		c.restoredQuiet = true
		if a.restoredConnectionNeedsSecrets(c.host) {
			c.waitingSecrets = true
			c.restorePending = true
			continue
		}
		c.s.Start()
	}
	for _, w := range opened {
		a.openNative(w, 0, 0, false)
	}
	// Establish the restored baseline, without rewriting the file on launch.
	a.rememberLayoutBaseline()
}

// restoreTabs opens the saved tabs in window w, and reports whether it
// opened any.
func (a *App) restoreTabs(w *winState, tabs []tabSnap, active int, mapping map[int]*layout.Node, links map[int]int, skipped *bool) bool {
	shown := 0
	for i, t := range tabs {
		seen := map[string]bool{}
		root := a.restoreLayoutNode(t.Root, mapping, links, seen, skipped)
		if root == nil {
			continue
		}
		focus := 0
		if p := mapping[t.Focus]; p != nil {
			focus = p.ID
		}
		if i == active {
			shown = len(w.ws.Tabs)
		}
		w.ws.AddTab(root, focus)
		w.ws.ActiveTab().Title = t.Title
		if slices.Contains(theme.TintNames, t.Color) {
			w.ws.ActiveTab().Color = t.Color
		}
	}
	if len(w.ws.Tabs) == 0 {
		return false
	}
	w.ws.Active = shown
	w.focusReq = w.ws.ActiveTab().Focused().ID
	return true
}

// windowID is the number a window's saved key gives it, for a key of
// another window than the main one.
func windowID(key string) (int, bool) {
	n, ok := strings.CutPrefix(key, "window-")
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(n)
	return id, err == nil && id > 1 && id < 1<<16
}

func (a *App) restoreGroupSections(sections map[string]bool) {
	for _, group := range a.store.Groups() {
		key := "g:" + group
		if open, ok := sections[key]; ok {
			a.sections[key] = &open
		}
	}
}

// Establish a baseline only when serialization succeeds, retaining diagnostics on failure.
func (a *App) rememberLayoutBaseline() {
	b, err := a.layoutBytes(a.settings.RememberLayout)
	if err != nil {
		if !a.layoutSaveFailed {
			a.layoutNotice(false)
		}
		a.layoutSaveFailed = true
		log.Printf("sshyard: workspace layout baseline serialization failed")
		return
	}
	a.layoutObserved = b
}

// Only the known unversioned workspace envelope is treated as a legacy file.
func legacyLayout(b []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return false
	}
	if _, versioned := fields["version"]; versioned {
		return false
	}
	tabs, ok := fields["tabs"]
	if !ok {
		return false
	}
	var old []json.RawMessage
	return json.Unmarshal(tabs, &old) == nil
}

// Validate the complete tree before allocating sessions; unknown versions remain untouched.
func validLayout(s layoutSnap) bool {
	tabs := slices.Clone(s.Tabs)
	keys := map[string]bool{}
	for _, w := range s.Windows {
		if _, ok := windowID(w.Key); !ok || keys[w.Key] {
			return false
		}
		keys[w.Key] = true
		tabs = append(tabs, w.Tabs...)
	}
	if s.Version != 1 || len(tabs) > 256 || len(s.Windows) > 32 {
		return false
	}
	ids := map[int]bool{}
	count := 0
	var valid func(*nodeSnap, int) bool
	valid = func(n *nodeSnap, depth int) bool {
		count++
		if n == nil || depth > 64 || count > 4096 {
			return false
		}
		if n.Split != "" {
			return (n.Split == "row" || n.Split == "column") && n.Kind == "" && n.Ratio > 0 && n.Ratio < 1 && valid(n.A, depth+1) && valid(n.B, depth+1)
		}
		if n.A != nil || n.B != nil || n.ID <= 0 || ids[n.ID] {
			return false
		}
		ids[n.ID] = true
		if _, ok := toolKind(n.Kind); ok {
			return n.Host == ""
		}
		return n.Host != "" && (n.Kind == "terminal" || n.Kind == "files" || n.Kind == "ai" || n.Kind == "editor" || n.Kind == "desktop")
	}
	for _, t := range tabs {
		if !valid(t.Root, 0) {
			return false
		}
	}
	return true
}
func (a *App) restoreLayoutNode(n *nodeSnap, mapping map[int]*layout.Node, links map[int]int, seen map[string]bool, skipped *bool) *layout.Node {
	if n.Split != "" {
		left := a.restoreLayoutNode(n.A, mapping, links, seen, skipped)
		right := a.restoreLayoutNode(n.B, mapping, links, seen, skipped)
		if left == nil {
			return right
		}
		if right == nil {
			return left
		}
		axis := layout.Row
		if n.Split == "column" {
			axis = layout.Column
		}
		return &layout.Node{ID: a.ws.FreshID(), Axis: axis, Ratio: n.Ratio, A: left, B: right}
	}
	if k, ok := toolKind(n.Kind); ok {
		// One of each in the whole workspace, as the rail opens them.
		if a.toolPane(k) != nil {
			*skipped = true
			return nil
		}
		if k == layout.Logs {
			a.listLogs()
		}
		a.nextConn++
		c := &conn{id: a.nextConn, kind: k}
		a.conns[c.id] = c
		p := &layout.Node{ID: a.ws.FreshID(), Item: c.item()}
		mapping[n.ID] = p
		return p
	}
	if n.Kind == "editor" || isTemp(n.Host) || isLocal(n.Host) && !a.layoutShellExists(n.Host) || (n.Kind == "ai" || n.Kind == "files") && seen[n.Kind] {
		*skipped = true
		return nil
	}
	kind := layout.Terminal
	switch n.Kind {
	case "ai":
		kind = layout.AI
	case "files":
		kind = layout.Files
	}
	c, err := a.makeConn(n.Host, kind, 0)
	if err != nil {
		*skipped = true
		return nil
	}
	seen[n.Kind] = true
	if c.ai != nil {
		if n.Agent != "" {
			c.ai.agent = n.Agent
		}
		if n.Mode != "full" {
			c.ai.mode = n.Mode
		}
		c.ai.provider, c.ai.model, c.ai.effort = n.Provider, n.Model, n.Effort
		links[c.id] = n.Link
	}
	p := &layout.Node{ID: a.ws.FreshID(), Item: c.item()}
	mapping[n.ID] = p
	return p
}

// toolKind is the tool pane kind saved as s.
func toolKind(s string) (layout.Kind, bool) {
	for k := layout.Broadcast; k <= layout.Tmux; k++ {
		if isTool(k) && k.String() == s {
			return k, true
		}
	}
	return 0, false
}

// railKind is the kind of a pane the rail opens, saved as s.
func railKind(s string) (layout.Kind, bool) {
	switch s {
	case "files":
		return layout.Files, true
	case "ai":
		return layout.AI, true
	}
	return toolKind(s)
}

func (a *App) layoutShellExists(id string) bool {
	for _, sh := range a.shells {
		if localID+sh.ID == id {
			return true
		}
	}
	return false
}
func (a *App) startRestoredConnections() {
	for _, c := range a.conns {
		if c.restorePending && c.s != nil {
			c.restorePending = false
			c.waitingSecrets = false
			c.s.Start()
		}
	}
}
func (a *App) ReportLayoutReadError(err error) {
	if err != nil {
		log.Printf("sshyard: native layout read failed")
		a.post(func() { a.layoutNotice(true) })
	}
}

// Remembered local-key metadata covers automatic AuthAsk credentials without
// discovering private key files or requesting authorization during restoration.
func (a *App) restoredConnectionNeedsSecrets(h assets.Host) bool {
	if a.connectionNeedsSecrets(h) {
		return true
	}
	if a.unlocked() || !a.unlock.stored || !a.lkOn.Load() || isLocal(h.ID) {
		return false
	}
	path, _ := a.store.JumpPath(h.ID)
	path = append(path, h)
	for _, hop := range path {
		if hop.Auth == assets.AuthAsk && a.lk.IsRemembered(a.lk.Last(hop.ID)) {
			return true
		}
	}
	return false
}
