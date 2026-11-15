package theme

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBrandIsFixed(t *testing.T) {
	want := ui.Color{R: 0x19, G: 0x84, B: 0xFC, A: 0xFF}
	if Brand != want {
		t.Fatalf("Brand = %+v, want %+v", Brand, want)
	}
	for _, tk := range []Tokens{LightTokens(), DarkTokens()} {
		if tk.Accent != want {
			t.Errorf("dark=%v accent %+v, want brand", tk.Dark, tk.Accent)
		}
		if tk.Terminal.Cursor != want {
			t.Errorf("dark=%v cursor %+v, want brand", tk.Dark, tk.Terminal.Cursor)
		}
	}
}

func TestModes(t *testing.T) {
	cases := []struct {
		m      Mode
		system bool
		dark   bool
	}{
		{Auto, false, false}, {Auto, true, true},
		{Light, true, false}, {Light, false, false},
		{Dark, false, true}, {Dark, true, true},
	}
	for _, c := range cases {
		if got := c.m.IsDark(c.system); got != c.dark {
			t.Errorf("%s.IsDark(%v) = %v", c.m, c.system, got)
		}
		if got := For(c.m, c.system).Dark; got != c.dark {
			t.Errorf("For(%s,%v).Dark = %v", c.m, c.system, got)
		}
	}
	if Mode("sepia").Valid() || !Auto.Valid() || len(Modes) != 3 {
		t.Error("mode validity")
	}
}

func TestContrast(t *testing.T) {
	for _, tk := range []Tokens{LightTokens(), DarkTokens()} {
		if c := Contrast(tk.Text, tk.Background); c < 7 {
			t.Errorf("dark=%v text contrast %.2f < 7", tk.Dark, c)
		}
		if c := Contrast(tk.TextMuted, tk.Background); c < 4.5 {
			t.Errorf("dark=%v muted contrast %.2f < 4.5", tk.Dark, c)
		}
		if c := Contrast(tk.AccentText, tk.Accent); c < 3 {
			t.Errorf("dark=%v accent text contrast %.2f < 3", tk.Dark, c)
		}
		if c := Contrast(tk.Terminal.Foreground, tk.Terminal.Background); c < 7 {
			t.Errorf("dark=%v terminal contrast %.2f < 7", tk.Dark, c)
		}
		if tk.Dark != (Luminance(tk.Background) < 0.2) {
			t.Errorf("dark=%v background luminance %.2f", tk.Dark, Luminance(tk.Background))
		}
	}
}

func TestApply(t *testing.T) {
	base := ui.Theme{Font: "Inter", Accent: ui.Hex("#ff0000")}
	th := DarkTokens().Apply(base)
	if !th.Dark || th.Accent != Brand || th.InactiveAccent != Brand || th.Font != "Inter" || th.Radius != RadiusControl {
		t.Errorf("Apply: %+v", th)
	}
	if base.Accent == Brand {
		t.Error("Apply changed its base")
	}
}

// TestScales: radii nest (each step holds the one below with room), the
// spacing scale is the 4-DIP grid and the type scale rises.
func TestScales(t *testing.T) {
	radii := []float32{RadiusSmall, RadiusControl, RadiusCard, RadiusSheet}
	for i := 1; i < len(radii); i++ {
		if radii[i] <= radii[i-1] {
			t.Errorf("radius %d (%v) not above %v", i, radii[i], radii[i-1])
		}
	}
	if Capsule < 100 {
		t.Errorf("capsule radius %v rounds no ends", Capsule)
	}
	if Space(1) != 4 || Space(6) != 24 || Space(2.5) != 10 {
		t.Errorf("space scale %v %v %v", Space(1), Space(6), Space(2.5))
	}
	types := []float32{Caption, Footnote, Body, Title3, Title2, Title1}
	for i := 1; i < len(types); i++ {
		if types[i] <= types[i-1] {
			t.Errorf("type size %d (%v) not above %v", i, types[i], types[i-1])
		}
	}
	if d := Dense(*LightTokens().Apply(ui.Theme{})); d.Spacing != DenseUnit || d.FontSize != Footnote || d.Space(8) != 24 || d.Accent != LightTokens().Accent {
		t.Errorf("dense theme %v %v %v", d.Spacing, d.FontSize, d.Space(8))
	}
	for _, tk := range []Tokens{LightTokens(), DarkTokens()} {
		if tk.FontSize != Body || tk.Radius != RadiusControl || tk.Spacing != SpaceUnit {
			t.Errorf("dark=%v metrics %v %v %v", tk.Dark, tk.FontSize, tk.Radius, tk.Spacing)
		}
	}
}

// TestSurfaces: the layers of each appearance stay apart in the right
// order, the translucent ones are translucent and the text reads on every
// opaque layer.
func TestSurfaces(t *testing.T) {
	for _, tk := range []Tokens{LightTokens(), DarkTokens()} {
		for name, c := range map[string]ui.Color{"glass": tk.Glass, "glassedge": tk.GlassEdge, "line": tk.Line,
			"hover": tk.Hover, "press": tk.Press, "fill": tk.Fill} {
			if c.A == 0 || c.A == 0xFF {
				t.Errorf("dark=%v %s alpha %d, want translucent", tk.Dark, name, c.A)
			}
		}
		if tk.Hover.A >= tk.Press.A {
			t.Errorf("dark=%v hover not lighter than press", tk.Dark)
		}
		for name, c := range map[string]ui.Color{"sidebar": tk.Sidebar, "canvas": tk.Canvas, "inset": tk.Inset, "terminal": tk.Terminal.Background} {
			if c.A != 0xFF {
				t.Errorf("dark=%v %s not opaque", tk.Dark, name)
			}
			if r := Contrast(tk.Text, c); r < 7 {
				t.Errorf("dark=%v text on %s %.2f < 7", tk.Dark, name, r)
			}
			if r := Contrast(tk.TextMuted, c); r < 4.5 {
				t.Errorf("dark=%v muted on %s %.2f < 4.5", tk.Dark, name, r)
			}
		}
		// The sidebar and the inset cards stand apart from the canvas.
		if tk.Sidebar == tk.Canvas || tk.Inset == tk.Canvas {
			t.Errorf("dark=%v layers merge", tk.Dark)
		}
		if tk.Lift.Blur <= tk.Card.Blur || tk.Lift.Color.A <= tk.Card.Color.A {
			t.Errorf("dark=%v lift shadow not above card shadow", tk.Dark)
		}
	}
}

func TestTint(t *testing.T) {
	l, d := LightTokens(), DarkTokens()
	if l.Tint("web-01") != l.Tint("web-01") {
		t.Error("tint not stable")
	}
	seen := map[ui.Color]bool{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		seen[l.Tint(n)] = true
		if d.Tint(n) == l.Tint(n) {
			t.Errorf("tint of %q same light and dark", n)
		}
	}
	if len(seen) < 3 {
		t.Errorf("tints spread over %d colors", len(seen))
	}
}

func TestSidebarAndMaterialThemes(t *testing.T) {
	tk := LightTokens()
	base := *tk.Apply(ui.Theme{})
	if th := tk.SidebarTheme(base, false); th.Surface != tk.Sidebar || th.SurfaceHover != tk.Hover || th.Accent != Brand {
		t.Errorf("sidebar theme %+v", th)
	}
	if th := tk.SidebarTheme(base, true); th.Surface != ui.Transparent || th.Background != ui.Transparent {
		t.Error("clear sidebar theme keeps a fill")
	}
	if th := tk.Material(base); th.Background != ui.Transparent || th.Text != tk.Text {
		t.Error("material theme")
	}
}

func TestTintAt(t *testing.T) {
	l, d := LightTokens(), DarkTokens()
	for i := range Tints {
		if l.TintAt(i) != Tints[i][0] || d.TintAt(i) != Tints[i][1] {
			t.Fatalf("TintAt(%d) does not follow the mode", i)
		}
	}
	if l.TintAt(-1) != Tints[len(Tints)-1][0] || l.TintAt(len(Tints)) != Tints[0][0] {
		t.Fatal("TintAt does not wrap")
	}
}

func TestTintNamed(t *testing.T) {
	if len(TintNames) != len(Tints) {
		t.Fatalf("%d names for %d tints", len(TintNames), len(Tints))
	}
	l, d := LightTokens(), DarkTokens()
	for i, n := range TintNames {
		lc, ok1 := l.TintNamed(n)
		dc, ok2 := d.TintNamed(n)
		if !ok1 || !ok2 || lc != Tints[i][0] || dc != Tints[i][1] {
			t.Errorf("%s is not tint %d", n, i)
		}
	}
	for _, n := range []string{"", "Green", "#1984FC"} {
		if _, ok := l.TintNamed(n); ok {
			t.Errorf("%q names a tint", n)
		}
	}
}

func TestSchemes(t *testing.T) {
	if len(Schemes) != 10 {
		t.Errorf("%d built-in schemes", len(Schemes))
	}
	ids := map[string]bool{}
	for _, s := range Schemes {
		if !s.Valid() || s.Custom() || ids[s.ID] {
			t.Errorf("scheme %q: valid %v, custom %v, twice %v", s.ID, s.Valid(), s.Custom(), ids[s.ID])
		}
		ids[s.ID] = true
		if got, ok := FindScheme(s.ID, nil); !ok || got.Name != s.Name {
			t.Errorf("FindScheme(%q)", s.ID)
		}
	}
	d, _ := FindScheme("dracula", nil)
	base := DarkTokens().Terminal
	pal := d.Terminal(base)
	if pal.Background != ui.Hex("#282A36") || pal.ANSI[4] != ui.Hex("#BD93F9") || pal.Selection != ui.Hex("#44475A") ||
		pal.CursorText != pal.Background || pal.Match != base.Match {
		t.Errorf("dracula palette %+v", pal)
	}
	if !d.Dark() {
		t.Error("dracula is not dark")
	}
	if l, _ := FindScheme("solarized-light", nil); l.Dark() {
		t.Error("solarized light is dark")
	}
	custom := []Scheme{{ID: CustomPrefix + "x", Name: "x"}}
	if s, ok := FindScheme(CustomPrefix+"x", custom); !ok || !s.Custom() {
		t.Error("custom scheme not found")
	}
	if _, ok := FindScheme("nope", custom); ok {
		t.Error("found an unknown scheme")
	}
	bad := d
	bad.ANSI[3] = "#12345"
	if bad.Valid() {
		t.Error("a bad color is valid")
	}
}

// plistColor is a color's dict, as iTerm2 writes it.
func plistColor(key string, r, g, b float64) string {
	return fmt.Sprintf(`<key>%s</key><dict><key>Alpha Component</key><real>1</real>
<key>Blue Component</key><real>%g</real><key>Color Space</key><string>sRGB</string>
<key>Green Component</key><real>%g</real><key>Red Component</key><real>%g</real></dict>`, key, b, g, r)
}

func TestParseITermColors(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>`)
	for i := range 16 {
		b.WriteString(plistColor(fmt.Sprintf("Ansi %d Color", i), float64(i)/15, 0, 1))
	}
	b.WriteString(plistColor("Background Color", 0, 0, 0))
	b.WriteString(plistColor("Foreground Color", 1, 1, 1))
	b.WriteString(plistColor("Selection Color", 0.2, 0.4, 0.6))
	b.WriteString(plistColor("Bold Color", 1, 0, 0))
	b.WriteString(`<key>Name</key><string>ignored</string></dict></plist>`)
	s, err := ParseITermColors("My Theme", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != CustomPrefix+"My Theme" || s.Name != "My Theme" || !s.Custom() || !s.Dark() {
		t.Errorf("scheme %+v", s)
	}
	if s.Background != "#000000" || s.Foreground != "#FFFFFF" || s.Selection != "#336699" || s.Cursor != "" ||
		s.ANSI[0] != "#0000FF" || s.ANSI[15] != "#FF00FF" {
		t.Errorf("colors %+v", s)
	}
	// Without the 16 colors, or not a property list at all, it is none.
	short := strings.Replace(b.String(), plistColor("Ansi 7 Color", 7.0/15, 0, 1), "", 1)
	for _, data := range []string{short, "hello", `{"name": "x"}`, ""} {
		if _, err := ParseITermColors("x", []byte(data)); !errors.Is(err, ErrNotITermColors) {
			t.Errorf("%.20q: %v", data, err)
		}
	}
	if _, err := ParseITermColors(" ", []byte(b.String())); !errors.Is(err, ErrNotITermColors) {
		t.Error("a scheme without a name")
	}
}
