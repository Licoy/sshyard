package app

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
)

// TestRailRows: the rail shows every item by default; hiding items drops
// them, a group left empty takes its separator with it, at the start, in
// the middle or at the end, and an ID naming no item changes nothing.
func TestRailRows(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range railItems {
		if it.id == "" || it.id == railSep || seen[it.id] {
			t.Fatalf("item ID %q", it.id)
		}
		seen[it.id] = true
	}
	all := []string{"monitor", "usage", "host", railSep, "files", "docker", "tmux", "ai", "forward", railSep, "snippets", "history", "broadcast", "logs"}
	for _, tc := range []struct {
		name        string
		hidden      []string
		top, bottom []string
	}{
		{"none", nil, all, []string{"quick", "import"}},
		{"unknown", []string{"later", ""}, all, []string{"quick", "import"}},
		{"some", []string{"usage", "tmux", "import"},
			[]string{"monitor", "host", railSep, "files", "docker", "ai", "forward", railSep, "snippets", "history", "broadcast", "logs"}, []string{"quick"}},
		{"first group", []string{"monitor", "usage", "host"},
			[]string{"files", "docker", "tmux", "ai", "forward", railSep, "snippets", "history", "broadcast", "logs"}, []string{"quick", "import"}},
		{"middle group", []string{"files", "docker", "tmux", "ai", "forward"},
			[]string{"monitor", "usage", "host", railSep, "snippets", "history", "broadcast", "logs"}, []string{"quick", "import"}},
		{"last group", []string{"snippets", "history", "broadcast", "logs", "quick", "import"},
			[]string{"monitor", "usage", "host", railSep, "files", "docker", "tmux", "ai", "forward"}, nil},
		{"one left", []string{"monitor", "usage", "host", "files", "docker", "tmux", "ai", "snippets", "history", "broadcast", "logs"},
			[]string{"forward"}, []string{"quick", "import"}},
	} {
		top, bottom := railRows(tc.hidden)
		if !slices.Equal(top, tc.top) || !slices.Equal(bottom, tc.bottom) {
			t.Errorf("%s: top %q bottom %q", tc.name, top, bottom)
		}
	}
}

// TestRailHide: hiding an item from its menu saves the settings and
// leaves the pane it opened open; the tool still opens and closes the
// other ways, and showing it again takes it off the list.
func TestRailHide(t *testing.T) {
	a, _ := newTestApp(t)
	m := locales.In("en")
	h := savedHost(t, a, "saved", "127.0.0.1")
	term, err := a.makeConn(h.ID, layout.Terminal, 0)
	if err != nil {
		t.Fatal(err)
	}
	a.ws.Open(term.item())
	a.ToggleTool(layout.Docker)
	entries := a.railEntries(m, "docker")
	if got := menuLabels(entries); !slices.Equal(got, []string{m.RailHide(), "-", m.RailCustomize()}) {
		t.Fatalf("menu %q", got)
	}
	entries[0].Do()
	if a.Settings().RailShown("docker") || !a.toolHere(layout.Docker) {
		t.Fatalf("hidden %q, docker open %v", a.Settings().RailHidden, a.toolHere(layout.Docker))
	}
	saved, err := settings.Load(filepath.Join(a.p.DataDir, "settings.json"))
	if err != nil || !slices.Equal(saved.RailHidden, []string{"docker"}) {
		t.Fatalf("saved %q %v", saved.RailHidden, err)
	}
	a.ToggleTool(layout.Docker)
	if a.toolHere(layout.Docker) {
		t.Fatal("a hidden tool no longer closes")
	}
	a.ToggleTool(layout.Docker)
	if !a.toolHere(layout.Docker) {
		t.Fatal("a hidden tool no longer opens")
	}
	a.setRailShown("docker", true)
	if a.Settings().RailHidden != nil {
		t.Fatalf("shown again %q", a.Settings().RailHidden)
	}
	// The rail's own menu only opens the settings, at the items.
	entries = a.railEntries(m, "")
	if got := menuLabels(entries); !slices.Equal(got, []string{m.RailCustomize()}) {
		t.Fatalf("rail menu %q", got)
	}
	entries[0].Do()
	if a.prefNow() != prefGeneral || a.prefSearch.row != m.RailSection() {
		t.Fatalf("opened %v at %q", a.prefNow(), a.prefSearch.row)
	}
}
