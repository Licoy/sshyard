package app

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func startSidebarTestApp(t *testing.T, dir string) *App {
	t.Helper()
	a, err := New(Platform{DataDir: dir, HomeDir: dir, CacheDir: filepath.Join(dir, "cache"), Secrets: keychain.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a.ready() {
			a.Close()
		}
	})
	a.Start()
	return a
}

func addSidebarTestGroup(t *testing.T, a *App, path string) {
	t.Helper()
	if parent := assets.GroupParent(path); parent != "" {
		a.openSubgroupForm(parent)
	} else {
		a.openGroupForm("", "")
	}
	saveSidebarTestGroup(t, a, assets.GroupLeaf(path))
}

func saveSidebarTestGroup(t *testing.T, a *App, name string) {
	t.Helper()
	a.group.name = name
	if !a.SaveGroup(locales.In("en")) {
		t.Fatalf("save %q: %s", name, a.group.err)
	}
}

func checkSidebarSections(t *testing.T, a *App, want map[string]bool) {
	t.Helper()
	for key, open := range want {
		if got := *a.section(key); got != open {
			t.Errorf("section %q: open=%v, want %v", key, got, open)
		}
	}
}

func readSidebarLayout(t *testing.T, a *App) layoutSnap {
	t.Helper()
	data, err := os.ReadFile(a.layoutPath())
	if err != nil {
		t.Fatal(err)
	}
	var snap layoutSnap
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestSidebarGroupPersistenceWithoutLayout(t *testing.T) {
	dir := t.TempDir()
	a := startSidebarTestApp(t, dir)
	if a.Settings().RememberLayout {
		t.Fatal("workspace layout memory should default to off")
	}
	addSidebarTestGroup(t, a, "Prod")
	checkSidebarSections(t, a, map[string]bool{"g:Prod": true})
	*a.section("g:Prod") = false
	a.Close()

	a = startSidebarTestApp(t, dir)
	checkSidebarSections(t, a, map[string]bool{"g:Prod": false})
	*a.section("g:Prod") = true
	a.Close()

	a = startSidebarTestApp(t, dir)
	checkSidebarSections(t, a, map[string]bool{"g:Prod": true})
}

func TestSidebarGroupPersistenceDebouncesWithoutLayout(t *testing.T) {
	a := startSidebarTestApp(t, t.TempDir())
	addSidebarTestGroup(t, a, "Prod")
	addSidebarTestGroup(t, a, "Lab")
	tt := ui.NewTester(a.observeLayout, 100, 100)
	tt.Frame()

	*a.section("g:Prod") = false
	*a.section("g:Lab") = false
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	if _, err := os.Stat(a.layoutPath()); !os.IsNotExist(err) {
		t.Fatalf("collapse saved before debounce: %v", err)
	}
	tt.Advance(200 * time.Millisecond)
	collapsed := map[string]bool{"g:Prod": false, "g:Lab": false}
	if got := readSidebarLayout(t, a).Sections; !maps.Equal(got, collapsed) {
		t.Fatalf("saved sections after collapse: %v", got)
	}

	*a.section("g:Prod") = true
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	if got := readSidebarLayout(t, a).Sections; !maps.Equal(got, collapsed) {
		t.Fatalf("expand saved before debounce: %v", got)
	}
	tt.Advance(200 * time.Millisecond)
	if got := readSidebarLayout(t, a).Sections; !maps.Equal(got, map[string]bool{"g:Lab": false}) {
		t.Fatalf("saved sections after expand: %v", got)
	}
}

func TestSidebarGroupPersistenceRenamesAndMovesSubgroups(t *testing.T) {
	dir := t.TempDir()
	a := startSidebarTestApp(t, dir)
	addSidebarTestGroup(t, a, "Prod")
	addSidebarTestGroup(t, a, "Prod/Cache")
	addSidebarTestGroup(t, a, "Lab")
	checkSidebarSections(t, a, map[string]bool{"g:Prod/Cache": true})
	*a.section("g:Prod") = false
	*a.section("g:Prod/Cache") = false
	*a.section("g:Lab") = false
	a.Close()

	a = startSidebarTestApp(t, dir)
	a.openGroupForm("Prod", "")
	saveSidebarTestGroup(t, a, "Live")
	a.Close()
	if got := readSidebarLayout(t, a).Sections; !maps.Equal(got, map[string]bool{"g:Live": false, "g:Live/Cache": false, "g:Lab": false}) {
		t.Fatalf("saved sections after rename: %v", got)
	}

	a = startSidebarTestApp(t, dir)
	if err := a.dropGroup("Live", "Lab", dropInto); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if got := readSidebarLayout(t, a).Sections; !maps.Equal(got, map[string]bool{"g:Lab/Live": false, "g:Lab/Live/Cache": false}) {
		t.Fatalf("saved sections after move: %v", got)
	}

	a = startSidebarTestApp(t, dir)
	if got := a.store.Groups(); !slices.Equal(got, []string{"Lab", "Lab/Live", "Lab/Live/Cache"}) {
		t.Fatalf("groups after restart: %q", got)
	}
	checkSidebarSections(t, a, map[string]bool{"g:Lab": true, "g:Lab/Live": false, "g:Lab/Live/Cache": false})
}

func TestSidebarGroupPersistenceDeletesSubgroups(t *testing.T) {
	dir := t.TempDir()
	a := startSidebarTestApp(t, dir)
	addSidebarTestGroup(t, a, "Prod")
	addSidebarTestGroup(t, a, "Prod/Cache")
	addSidebarTestGroup(t, a, "Production")
	*a.section("g:Prod") = false
	*a.section("g:Prod/Cache") = false
	*a.section("g:Production") = false
	a.Close()

	a = startSidebarTestApp(t, dir)
	if err := a.deleteGroup("Prod"); err != nil {
		t.Fatal(err)
	}
	if got := a.store.Groups(); !slices.Equal(got, []string{"Production"}) {
		t.Fatalf("groups after deletion: %q", got)
	}
	addSidebarTestGroup(t, a, "Prod")
	addSidebarTestGroup(t, a, "Prod/Cache")
	want := map[string]bool{"g:Prod": true, "g:Prod/Cache": true, "g:Production": false}
	checkSidebarSections(t, a, want)
	a.Close()
	if got := readSidebarLayout(t, a).Sections; !maps.Equal(got, map[string]bool{"g:Production": false}) {
		t.Fatalf("saved sections after deletion and recreation: %v", got)
	}

	a = startSidebarTestApp(t, dir)
	checkSidebarSections(t, a, want)
}

func TestSidebarGroupPersistenceWithLayout(t *testing.T) {
	dir := t.TempDir()
	a := startSidebarTestApp(t, dir)
	addSidebarTestGroup(t, a, "Prod")
	h := addGroupedHost(t, a, "Favorite", "")
	if err := a.store.SetFavorite(h.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddForward(assets.Forward{Name: "forward", HostID: h.ID, Kind: assets.ForwardLocal, ListenPort: 32123, TargetAddr: "localhost", TargetPort: 22}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	rememberLayoutIn(t, dir)
	const legacy = `{
		"version":1,
		"tabs":[{"title":"Saved tools","focus":1,"root":{"id":1,"kind":"tmux"}}],
		"sections":{"g:Prod":false,"g:Retired":false,"fav":false,"none":false,"fwd":false},
		"widths":{"files":240}
	}`
	if err := os.WriteFile(a.layoutPath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	a = startSidebarTestApp(t, dir)
	if len(a.ws.Tabs) != 1 || a.ws.ActiveTab().Title != "Saved tools" || a.railWidths[layout.Files] != 240 {
		t.Fatal("legacy workspace layout was not restored")
	}
	want := map[string]bool{"g:Prod": false, "g:Retired": true, "fav": false, "none": false, "fwd": false}
	addSidebarTestGroup(t, a, "Retired")
	checkSidebarSections(t, a, want)
	a.Close()

	a = startSidebarTestApp(t, dir)
	checkSidebarSections(t, a, want)
	s := a.Settings()
	s.RememberLayout = false
	if err := a.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	checkSidebarSections(t, a, want)
	snap := readSidebarLayout(t, a)
	if len(snap.Tabs) != 0 || len(snap.Widths) != 0 || !maps.Equal(snap.Sections, map[string]bool{"g:Prod": false}) {
		t.Fatalf("saved layout after disabling workspace memory: %+v", snap)
	}
	a.Close()

	a = startSidebarTestApp(t, dir)
	if len(a.ws.Tabs) != 0 || len(a.railWidths) != 0 {
		t.Fatal("disabled workspace layout was restored")
	}
	checkSidebarSections(t, a, map[string]bool{"g:Prod": false, "g:Retired": true, "fav": true, "none": true, "fwd": true})
}
