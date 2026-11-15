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

// Version 1 keeps workspace structure only, never session content or directories.
// The "panels" and "stackPct" an older build saved are ignored: those panels
// are tool panes now, saved as panes on no host.
type layoutSnap struct {
	Version  int             `json:"version"`
	Active   int             `json:"active"`
	Tabs     []tabSnap       `json:"tabs"`
	Sections map[string]bool `json:"sections,omitempty"`
	// Widths are the railWidths, by kind name.
	Widths map[string]float32 `json:"widths,omitempty"`
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
	snap := layoutSnap{Version: 1, Active: 0, Sections: map[string]bool{}}
	for _, key := range a.sidebarSectionKeys() {
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
	for i, t := range a.ws.Tabs {
		if t == nil {
			continue
		}
		root := a.snapNode(t.Root)
		if root == nil {
			continue
		}
		if i == a.ws.Active {
			snap.Active = len(snap.Tabs)
		}
		snap.Tabs = append(snap.Tabs, tabSnap{Title: t.Title, Color: t.Color, Focus: t.Focus, Root: root})
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
		if _, p := a.ws.Locate(c.ai.link); p != nil {
			s.Link = p.ID
		}
	}
	return s
}
func (a *App) layoutBytes() ([]byte, error) {
	b, err := json.MarshalIndent(a.layoutSnapshot(), "", "  ")
	return append(b, '\n'), err
}
func (a *App) saveLayout() {
	if a.layoutPath() == "" || a.restoring || !a.ready() || !a.settings.RememberLayout || a.layoutBlocked {
		return
	}
	b, err := a.layoutBytes()
	if err == nil {
		err = settings.WriteFileAtomic(a.layoutPath(), b, 0o600)
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
	if !a.settings.RememberLayout || a.layoutBlocked || a.restoring {
		return
	}
	b, err := a.layoutBytes()
	if err != nil {
		if !a.layoutSaveFailed {
			a.layoutNotice(false)
		}
		a.layoutSaveFailed = true
		return
	}
	if !bytes.Equal(b, a.layoutObserved) {
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
	a.saveLayout()
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
	if s.RememberLayout {
		b, err := a.layoutBytes()
		if err != nil {
			return rollback(err)
		}
		if err = settings.WriteFileAtomic(path, b, 0o600); err != nil {
			return rollback(err)
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return rollback(err)
	}
	if err := settings.Save(filepath.Join(a.p.DataDir, "settings.json"), s); err != nil {
		return rollback(err)
	}
	a.layoutBlocked = false
	a.layoutSaveFailed = false
	a.layoutObserved = nil
	a.layoutDue = time.Time{}
	return nil
}
