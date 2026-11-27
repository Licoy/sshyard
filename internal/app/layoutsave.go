package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// Version 1 keeps sidebar sections and optional workspace structure, never
// session content or directories.
// The "panels" and "stackPct" an older build saved are ignored: those panels
// are tool panes now, saved as panes on no host.
type layoutSnap struct {
	Version  int             `json:"version"`
	Active   int             `json:"active,omitempty"`
	Tabs     []tabSnap       `json:"tabs,omitempty"`
	Sections map[string]bool `json:"sections,omitempty"`
	// Widths are the railWidths, by kind name.
	Widths map[string]float32 `json:"widths,omitempty"`
	// Windows are the workspace windows but the main one, whose tabs are
	// Tabs. A build before them reads the main window alone.
	Windows []windowSnap `json:"windows,omitempty"`
}

// windowSnap is a workspace window but the main one: its tabs, and the
// key its place and size are kept under.
type windowSnap struct {
	Key    string    `json:"key"`
	Active int       `json:"active,omitempty"`
	Tabs   []tabSnap `json:"tabs"`
}
type tabSnap struct {
	Title string    `json:"title,omitempty"`
	Color string    `json:"color,omitempty"`
	Focus int       `json:"focus"`
	Root  *nodeSnap `json:"root"`
}
type nodeSnap struct {
	ID    int       `json:"id,omitempty"`
	Split string    `json:"split,omitempty"`
	Ratio float32   `json:"ratio,omitempty"`
	A     *nodeSnap `json:"a,omitempty"`
	B     *nodeSnap `json:"b,omitempty"`
	Kind  string    `json:"kind,omitempty"`
	Host  string    `json:"host,omitempty"`
	// Path is accepted only to identify legacy files; it is never populated.
	Path     string `json:"path,omitempty"`
	Link     int    `json:"link,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

func (a *App) layoutPath() string {
	if a.p.DataDir == "" {
		return ""
	}
	return filepath.Join(a.p.DataDir, "layout.json")
}
func (a *App) layoutSnapshot() layoutSnap {
	snap := a.groupLayoutSnapshot()
	for _, key := range a.sidebarBuiltinSectionKeys() {
		if p := a.sections[key]; p != nil {
			snap.Sections[key] = *p
		}
	}
	for k, w := range a.railWidths {
		if snap.Widths == nil {
			snap.Widths = map[string]float32{}
		}
		snap.Widths[k.String()] = w
	}
	snap.Tabs, snap.Active = a.snapTabs(a.main.ws)
	for _, w := range a.wins[1:] {
		if tabs, active := a.snapTabs(w.ws); len(tabs) > 0 {
			snap.Windows = append(snap.Windows, windowSnap{Key: w.key, Active: active, Tabs: tabs})
		}
	}
	return snap
}

// snapTabs are the tabs of ws as saved, and the place of its active one
// among them.
func (a *App) snapTabs(ws *layout.Workspace) (tabs []tabSnap, active int) {
	for i, t := range ws.Tabs {
		if t == nil {
			continue
		}
		root := a.snapNode(t.Root)
		if root == nil {
			continue
		}
		if i == ws.Active {
			active = len(tabs)
		}
		tabs = append(tabs, tabSnap{Title: t.Title, Color: t.Color, Focus: t.Focus, Root: root})
	}
	return tabs, active
}

func (a *App) groupLayoutSnapshot() layoutSnap {
	snap := layoutSnap{Version: 1, Sections: map[string]bool{}}
	for _, group := range a.store.Groups() {
		key := "g:" + group
		if open := a.sections[key]; open != nil && !*open {
			snap.Sections[key] = false
		}
	}
	return snap
}
func (a *App) snapNode(n *layout.Node) *nodeSnap {
	if n == nil {
		return nil
	}
	if !n.Leaf() {
		left, right := a.snapNode(n.A), a.snapNode(n.B)
		if left == nil {
			return right
		}
		if right == nil {
			return left
		}
		split := "row"
		if n.Axis == layout.Column {
			split = "column"
		}
		return &nodeSnap{Split: split, Ratio: n.Ratio, A: left, B: right}
	}
	c := a.conns[n.Item.ID]
	if c == nil || isTemp(c.host.ID) || c.kind == layout.Editor {
		return nil
	}
	if isTool(c.kind) {
		return &nodeSnap{ID: n.ID, Kind: c.kind.String()}
	}
	host := c.host.ID
	if host == localID {
		host = localID + a.shell(host).ID
	}
	s := &nodeSnap{ID: n.ID, Kind: c.kind.String(), Host: host}
	if c.ai != nil {
		s.Agent, s.Mode, s.Provider, s.Model, s.Effort = c.ai.agent, c.ai.mode, c.ai.provider, c.ai.model, c.ai.effort
		if s.Mode == "full" {
			s.Mode = ""
		}
		if w := a.winOf(c.ai.link); w != nil {
			_, p := w.ws.Locate(c.ai.link)
			s.Link = p.ID
		}
	}
	return s
}
func (a *App) layoutBytes(rememberWorkspace bool) ([]byte, error) {
	var snap layoutSnap
	if rememberWorkspace {
		snap = a.layoutSnapshot()
	} else {
		snap = a.groupLayoutSnapshot()
		if len(snap.Sections) == 0 {
			return nil, nil
		}
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	return append(b, '\n'), err
}

// writeLayout removes the snapshot when every saved group is open and
// workspace memory is disabled.
func writeLayout(path string, data []byte) error {
	if data != nil {
		return settings.WriteFileAtomic(path, data, 0o600)
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (a *App) saveLayout() {
	if a.settings.RememberLayout {
		a.flushLayout()
	}
}

func (a *App) flushLayout() {
	path := a.layoutPath()
	if path == "" || a.restoring || !a.ready() || a.layoutBlocked {
		return
	}
	b, err := a.layoutBytes(a.settings.RememberLayout)
	if err == nil {
		err = writeLayout(path, b)
	}
	if err != nil {
		if !a.layoutSaveFailed {
			a.layoutNotice(false)
		}
		a.layoutSaveFailed = true
		log.Printf("sshyard: workspace layout save failed")
		return
	}
	a.layoutSaveFailed = false
	a.layoutObserved = append(a.layoutObserved[:0], b...)
	a.layoutDue = time.Time{}
}

// Observing the safe snapshot captures focus, split drags and all shared controls.
func (a *App) observeLayout(c *ui.Context) {
	if a.layoutPath() == "" || a.layoutBlocked || a.restoring {
		return
	}
	b, err := a.layoutBytes(a.settings.RememberLayout)
	if err != nil {
		if !a.layoutSaveFailed {
			a.layoutNotice(false)
		}
		a.layoutSaveFailed = true
		return
	}
	if !bytes.Equal(b, a.layoutObserved) {
		a.pruneGroupSections()
		a.layoutObserved = b
		a.layoutDue = c.Now().Add(300 * time.Millisecond)
	}
	if a.layoutDue.IsZero() {
		return
	}
	if c.Now().Before(a.layoutDue) {
		c.After(a.layoutDue.Sub(c.Now()))
		return
	}
	a.layoutDue = time.Time{}
	a.flushLayout()
}
func (a *App) layoutNotice(read bool) {
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
		text := m.ErrLayoutSave()
		if read {
			text = m.ErrLayoutRead()
		}
		return ui.Toast{Title: text, Type: "error"}
	})
}

// ReportLayoutError makes native geometry failures observable without exposing paths.
func (a *App) ReportLayoutError(err error) {
	if err != nil {
		log.Printf("sshyard: native layout persistence failed")
		a.post(func() { a.layoutNotice(false) })
	}
}

// saveSettingsLayout keeps the preference, native state and workspace in step.
// Failed transactions restore the previous workspace bytes before returning.
func (a *App) saveSettingsLayout(s, old settings.Settings) error {
	path := a.layoutPath()
	if s.RememberLayout == old.RememberLayout {
		return settings.Save(filepath.Join(a.p.DataDir, "settings.json"), s)
	}
	previous, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	var undo func() error
	if a.p.SetRememberLayout != nil {
		var err error
		undo, err = a.p.SetRememberLayout(s.RememberLayout)
		if err != nil {
			return err
		}
	}
	rollback := func(err error) error {
		if readErr == nil {
			err = errors.Join(err, settings.WriteFileAtomic(path, previous, 0o600))
		} else if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
		if undo != nil {
			err = errors.Join(err, undo())
		}
		return err
	}
	b, err := a.layoutBytes(s.RememberLayout)
	if err != nil {
		return rollback(err)
	}
	if err := writeLayout(path, b); err != nil {
		return rollback(err)
	}
	if err := settings.Save(filepath.Join(a.p.DataDir, "settings.json"), s); err != nil {
		return rollback(err)
	}
	a.layoutBlocked = false
	a.layoutSaveFailed = false
	a.layoutObserved = b
	a.layoutDue = time.Time{}
	return nil
}
