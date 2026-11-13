package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Licoy/sshyard/internal/highlight"
	"github.com/Licoy/sshyard/internal/theme"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "settings.json")
	s, err := Load(p)
	if err != nil || !reflect.DeepEqual(s, Default()) {
		t.Fatalf("missing file: %+v %v", s, err)
	}
	s.Theme, s.Language, s.TerminalFontSize, s.LocalShell = theme.Dark, "zh-Hans", 15, "wsl:Ubuntu"
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v %v, want %+v", got, err, s)
	}
}

func TestNormalize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"theme":"sepia","language":"fr","terminalFontSize":99,"terminalLineHeight":500,"transferConcurrency":0,"openConfirmMB":5000,"filesSplit":95,"editorTreeWidth":20}`), 0o600)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Theme != theme.Auto || s.Language != "" || s.TerminalFontSize != 13 || s.TerminalLineHeight != 100 || s.TerminalCharWidth != 100 || s.TransferConcurrency != 3 || s.OpenConfirmMB != 10 || s.FilesSplit != 50 || s.FilesDual || s.EditorTreeWidth != 240 {
		t.Errorf("not normalized: %+v", s)
	}
}

func TestFontsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := Default()
	if s.UIFont != "" || s.TerminalFont != "" || !s.MonospacedOnly {
		t.Fatalf("default fonts %+v", s)
	}
	s.UIFont, s.TerminalFont, s.TerminalFontSize, s.MonospacedOnly = "Noto Sans", "JetBrains Mono", 15, false
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v %v, want %+v", got, err, s)
	}
	// A file of before the font settings keeps the monospaced filter on.
	os.WriteFile(path, []byte(`{"theme":"dark","terminalFontSize":14}`), 0o600)
	if got, _ := Load(path); !got.MonospacedOnly || got.TerminalFont != "" {
		t.Errorf("old file %+v", got)
	}
}

func TestOSC52Policy(t *testing.T) {
	if g, h := ResolveOSC52Write(OSC52Ask, ""), ResolveOSC52Write(OSC52Deny, OSC52Allow); g != OSC52Ask || h != OSC52Allow {
		t.Fatalf("resolve %s %s", g, h)
	}
	if ResolveOSC52Write(OSC52Deny, "") != OSC52Deny {
		t.Fatal("empty host follows the global policy")
	}
	if ResolveOSC52Write("nope", "nope") != OSC52Ask {
		t.Fatal("invalid policy is ask")
	}
	s := Default()
	if s.OSC52Write != OSC52Ask || s.OSC52MaxBytes != OSC52MaxDefault {
		t.Fatalf("default %+v", s)
	}
	s.OSC52Write, s.OSC52MaxBytes = "nope", 10
	s.Normalize()
	if s.OSC52Write != OSC52Ask || s.OSC52MaxBytes != OSC52MaxDefault {
		t.Fatalf("normalized %+v", s)
	}
	s.OSC52Write, s.OSC52MaxBytes = OSC52Ask, OSC52MaxMin
	s.Normalize()
	if s.OSC52Write != OSC52Ask || s.OSC52MaxBytes != OSC52MaxMin {
		t.Fatalf("kept %+v", s)
	}
	s.OSC52MaxBytes = OSC52MaxMax + 1
	s.Normalize()
	if s.OSC52MaxBytes != OSC52MaxDefault {
		t.Fatalf("ceiling %+v", s)
	}
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"theme":"dark"}`), 0o600)
	got, err := Load(p)
	if err != nil || got.OSC52Write != OSC52Ask || got.OSC52MaxBytes != OSC52MaxDefault || !got.OSC52Notify {
		t.Fatalf("old file %+v %v", got, err)
	}
	os.WriteFile(p, []byte(`{"osc52Notify":false}`), 0o600)
	if got, err := Load(p); err != nil || got.OSC52Notify {
		t.Fatalf("notify off %+v %v", got, err)
	}
}

func TestMetricsDefaults(t *testing.T) {
	d := Default()
	if !d.MetricsEnabled || d.MetricsInterval != 2 || d.MetricsMount != "" {
		t.Fatalf("defaults %+v", d)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	// A file from before metrics keeps the bar on, at 2 seconds, with no extra mount.
	os.WriteFile(path, []byte(`{"theme":"dark","terminalFontSize":14}`), 0o600)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MetricsEnabled || got.MetricsInterval != 2 || got.MetricsMount != "" || got.TerminalFontSize != 14 {
		t.Fatalf("old file %+v", got)
	}
	os.WriteFile(path, []byte(`{"metricsEnabled":false,"metricsInterval":10,"metricsMount":"/mnt/my disk"}`), 0o600)
	got, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.MetricsEnabled || got.MetricsInterval != 10 || got.MetricsMount != "/mnt/my disk" {
		t.Fatalf("kept %+v", got)
	}
	os.WriteFile(path, []byte(`{"metricsEnabled":false,"metricsInterval":7,"metricsMount":"/data/$(reboot)"}`), 0o600)
	got, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.MetricsEnabled || got.MetricsInterval != 2 || got.MetricsMount != "" {
		t.Fatalf("normalized %+v", got)
	}
}

func TestNormalizeFonts(t *testing.T) {
	for in, want := range map[string]string{
		"  Fira Code ":           "Fira Code",
		"Menlo, monospace":       "",
		"a\nb":                   "",
		strings.Repeat("x", 200): "",
		"思源黑体":                   "思源黑体",
	} {
		s := Default()
		s.UIFont, s.TerminalFont, s.TerminalFallbackFont = in, in, in
		s.Normalize()
		if s.UIFont != want || s.TerminalFont != want || s.TerminalFallbackFont != want {
			t.Errorf("%q: %q %q %q, want %q", in, s.UIFont, s.TerminalFont, s.TerminalFallbackFont, want)
		}
	}
}

// TestMaterials: the sidebar is translucent and the content opaque by
// default, as before the setting, and each keeps its own choice.
func TestMaterials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"theme":"dark","contentMaterial":"frosted"}`), 0o600)
	s, err := Load(path)
	if err != nil || s.ChromeMaterial != MaterialTranslucent || s.ContentMaterial != MaterialOpaque {
		t.Fatalf("old file %+v %v", s, err)
	}
	s.ChromeMaterial, s.ContentMaterial = MaterialOpaque, MaterialTranslucent
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(path); got.ChromeMaterial != MaterialOpaque || got.ContentMaterial != MaterialTranslucent {
		t.Errorf("got %+v", got)
	}
}

func TestOSC52ExplicitPoliciesPersist(t *testing.T) {
	for _, policy := range []string{OSC52Allow, OSC52Ask, OSC52Deny} {
		s := Default()
		s.OSC52Write = policy
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := Save(path, s); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.OSC52Write != policy {
			t.Fatalf("policy %s became %s: %v", policy, got.OSC52Write, err)
		}
		if ResolveOSC52Write(policy, "") != policy {
			t.Fatalf("global %s changed", policy)
		}
		if ResolveOSC52Write("invalid", policy) != policy {
			t.Fatalf("host %s changed", policy)
		}
	}
}

func TestUpdateInterval(t *testing.T) {
	if d := Default(); d.UpdateIntervalHours != 1 || !d.CheckUpdates {
		t.Fatalf("default %d %v", d.UpdateIntervalHours, d.CheckUpdates)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	for in, want := range map[string]int{`{}`: 1, `{"updateIntervalHours":6}`: 6, `{"updateIntervalHours":24}`: 24, `{"updateIntervalHours":3}`: 1, `{"updateIntervalHours":0}`: 1} {
		os.WriteFile(path, []byte(in), 0o600)
		if s, err := Load(path); err != nil || s.UpdateIntervalHours != want {
			t.Errorf("%s: %d %v, want %d", in, s.UpdateIntervalHours, err, want)
		}
	}
}

func TestBackupTargets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"backupTargets":[
		{"id":" nas ","name":" NAS ","kind":"webdav","url":" https://dav.example.com ","keep":5000,"autoHours":24},
		{"id":"nas","name":"twice","kind":"s3"},
		{"id":"ftp","name":"FTP","kind":"ftp"},
		{"id":"a/b","name":"bad ID","kind":"gist"},
		{"id":"s3","name":"S3","kind":"s3","bucket":"b","keep":-1,"autoHours":3,"omitSecrets":true}
	]}`), 0o600)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	ts := s.BackupTargets
	if len(ts) != 2 {
		t.Fatalf("kept %+v", ts)
	}
	if n := ts[0]; n.ID != "nas" || n.Name != "NAS" || n.URL != "https://dav.example.com" || n.Keep != BackupKeepMax || n.AutoHours != 24 || n.OmitSecrets {
		t.Errorf("first %+v", n)
	}
	if b := ts[1]; b.ID != "s3" || b.Keep != 0 || b.AutoHours != 0 || !b.OmitSecrets {
		t.Errorf("second %+v", b)
	}
	if got, ok := s.BackupTarget("s3"); !ok || got.Bucket != "b" {
		t.Errorf("BackupTarget(s3) = %+v, %v", got, ok)
	}
	if _, ok := s.BackupTarget("ftp"); ok {
		t.Error("a target of no kind was kept")
	}
}

func TestLanguagesRoundTrip(t *testing.T) {
	for _, lang := range append([]string{""}, Languages...) {
		t.Run(lang, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			s := Default()
			s.Language = lang
			if err := Save(path, s); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Language != lang {
				t.Errorf("language = %q, want %q", got.Language, lang)
			}
		})
	}
}

func TestTerminalSchemes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	s := Default()
	s.TerminalScheme = "nord"
	sc := theme.Schemes[0]
	sc.ID, sc.Name = theme.CustomPrefix+"Mine", "Mine"
	s.AddTerminalScheme(sc)
	if s.TerminalScheme != sc.ID || len(s.TerminalSchemes) != 1 {
		t.Fatalf("import not chosen: %+v", s.TerminalSchemes)
	}
	// Again, under the same name: replaced, not added.
	sc.Background = "#000000"
	s.AddTerminalScheme(sc)
	if len(s.TerminalSchemes) != 1 || s.TerminalSchemes[0].Background != "#000000" {
		t.Fatalf("reimport %+v", s.TerminalSchemes)
	}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip %+v %v", got.TerminalSchemes, err)
	}
	s.RemoveTerminalScheme(sc.ID)
	if s.TerminalScheme != "" || len(s.TerminalSchemes) != 0 {
		t.Errorf("removed scheme still chosen: %q", s.TerminalScheme)
	}
	// A broken or built-in looking entry is dropped, an unknown choice
	// cleared.
	os.WriteFile(p, []byte(`{"terminalScheme":"gone","terminalSchemes":[{"id":"dracula","name":"x"},{"id":"custom:bad","name":"bad","foreground":"red"}]}`), 0o600)
	if got, _ := Load(p); got.TerminalScheme != "" || got.TerminalSchemes != nil {
		t.Errorf("not normalized: %q %+v", got.TerminalScheme, got.TerminalSchemes)
	}
	s = Default()
	for i := range MaxTerminalSchemes + 1 {
		sc.ID = theme.CustomPrefix + strings.Repeat("x", i+1)
		s.AddTerminalScheme(sc)
	}
	if len(s.TerminalSchemes) != MaxTerminalSchemes || s.TerminalScheme != sc.ID || s.TerminalSchemes[0].ID != theme.CustomPrefix+"xx" {
		t.Errorf("%d schemes, chosen %q", len(s.TerminalSchemes), s.TerminalScheme)
	}
}

func TestShortcutsNormalize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"shortcuts":{"newTab":"super+j","find":"super+enter","":"super+k","x":"nonsense"}}`), 0o600)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Shortcuts) != 1 || s.Shortcuts["newTab"] != "super+j" {
		t.Errorf("shortcuts %v", s.Shortcuts)
	}
	os.WriteFile(p, []byte(`{"shortcuts":{"find":"bad"}}`), 0o600)
	if s, _ := Load(p); s.Shortcuts != nil {
		t.Errorf("shortcuts %v", s.Shortcuts)
	}
}

// TestUIZoom: the interface zoom follows the system by default, and
// keeps only a zoom the user can choose.
func TestUIZoom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for in, want := range map[string]int{`{}`: 0, `{"uiZoom":150}`: 150, `{"uiZoom":80}`: 80, `{"uiZoom":133}`: 0, `{"uiZoom":-5}`: 0, `{"uiZoom":500}`: 0} {
		os.WriteFile(path, []byte(in), 0o600)
		if s, err := Load(path); err != nil || s.UIZoom != want {
			t.Errorf("%s: %d %v, want %d", in, s.UIZoom, err, want)
		}
	}
}

func TestHighlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{}`), 0o600)
	s, err := Load(path)
	if err != nil || !s.Highlight.On || len(s.Highlight.PresetsOn()) != len(highlight.Presets) {
		t.Fatalf("defaults: %+v %v", s.Highlight, err)
	}
	os.WriteFile(path, []byte(`{"highlight":{"on":false,"presetsOff":["errors","gone"],"rules":[{"pattern":"x","color":40},{"pattern":""},{"pattern":"y","regex":true,"color":3,"bold":true}]}}`), 0o600)
	s, err = Load(path)
	h := s.Highlight
	if err != nil || h.On || !slices.Equal(h.PresetsOff, []string{"errors"}) || slices.Contains(h.PresetsOn(), "errors") {
		t.Fatalf("loaded: %+v %v", h, err)
	}
	if len(h.Rules) != 2 || h.Rules[0].Color != 1 || h.Rules[1].Pattern != "y" || !h.Rules[1].Bold || !h.Rules[1].Regex {
		t.Errorf("rules %+v", h.Rules)
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if again, _ := Load(path); !reflect.DeepEqual(again.Highlight, h) {
		t.Errorf("saved %+v, loaded %+v", h, again.Highlight)
	}
}

// TestFilesEdge: a files pane opens on the right by default and for any
// value but below, which is kept.
func TestFilesEdge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"theme":"dark","filesEdge":"left"}`), 0o600)
	s, err := Load(path)
	if err != nil || s.FilesEdge != FilesRight || s.OpenLocalOnStart {
		t.Fatalf("old file %+v %v", s, err)
	}
	s.FilesEdge, s.OpenLocalOnStart = FilesBelow, true
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(path); got.FilesEdge != FilesBelow || !got.OpenLocalOnStart {
		t.Errorf("saved %+v", got)
	}
}

// TestToolPanes: the rail replaces its pane by default and for any value
// but side by side, which is kept.
func TestToolPanes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"theme":"dark","toolPanes":"tabs"}`), 0o600)
	s, err := Load(path)
	if err != nil || s.ToolPanes != "" {
		t.Fatalf("old file %+v %v", s, err)
	}
	s.ToolPanes = ToolPanesSplit
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(path); got.ToolPanes != ToolPanesSplit {
		t.Errorf("saved %+v", got)
	}
}

func TestRenewalNotifyDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	// A file from before the setting keeps the notifications on.
	os.WriteFile(p, []byte(`{"theme":"dark"}`), 0o600)
	s, err := Load(p)
	if err != nil || !s.RenewalNotify || s.RenewalNotified != nil {
		t.Fatalf("loaded %v %v %v", s.RenewalNotify, s.RenewalNotified, err)
	}
	s.RenewalNotify, s.RenewalNotified = false, map[string]string{"h1": "2026-11-01"}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.RenewalNotify || got.RenewalNotified["h1"] != "2026-11-01" {
		t.Fatalf("reloaded %v %v %v", got.RenewalNotify, got.RenewalNotified, err)
	}
}

func TestSessionLogDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	dir := filepath.Join(t.TempDir(), "logs")
	for in, want := range map[string]string{"": "", "  " + dir + " ": dir, "relative/logs": "", "~/logs": ""} {
		s := Default()
		s.SessionLog.Dir = in
		if err := Save(p, s); err != nil {
			t.Fatal(err)
		}
		got, err := Load(p)
		if err != nil || got.SessionLog.Dir != want {
			t.Errorf("dir %q loaded as %q, want %q (%v)", in, got.SessionLog.Dir, want, err)
		}
	}
}

func TestNormalizeTerminalType(t *testing.T) {
	for in, want := range map[string]string{"": "", " linux ": "linux", "xterm-256color": "", "vt 100": "", "-x": ""} {
		s := Default()
		s.TerminalType = in
		s.Normalize()
		if s.TerminalType != want {
			t.Errorf("TerminalType %q normalized to %q, want %q", in, s.TerminalType, want)
		}
	}
}
