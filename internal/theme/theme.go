// Package theme holds SSHYard's design tokens: the brand color, the light
// and dark palettes of the interface and of the terminal, and how a mode
// (auto, light, dark) chooses between them.
package theme

import (
	"math"
	"slices"

	"github.com/egoist/mygo/ui"
)

// BrandHex is SSHYard's fixed brand color.
const BrandHex = "#1984FC"

// Brand is the brand color.
var Brand = ui.Hex(BrandHex)

// Mode is the appearance the user chose.
type Mode string

const (
	Auto  Mode = "auto"  // follow the system
	Light Mode = "light" // always light
	Dark  Mode = "dark"  // always dark
)

// Modes lists the modes in the order the settings show them.
var Modes = []Mode{Auto, Light, Dark}

// Valid reports whether m is one of the known modes.
func (m Mode) Valid() bool { return m == Auto || m == Light || m == Dark }

// IsDark resolves the mode against the system's appearance.
func (m Mode) IsDark(systemDark bool) bool {
	switch m {
	case Light:
		return false
	case Dark:
		return true
	}
	return systemDark
}

// Terminal is the palette of the terminal view.
type Terminal struct {
	Foreground, Background, Cursor, CursorText, Selection, Match, MatchCurrent ui.Color
	// ANSI are the 16 colors of SGR 30–37 and 90–97.
	ANSI [16]ui.Color
}

// Radii are the corner radii, small to large. Shapes nest concentrically:
// a shape inside another with padding p has the outer radius minus p.
const (
	RadiusSmall   float32 = 6   // icon buttons, menu items, chips
	RadiusControl float32 = 8   // inputs, list rows, selects, banners
	RadiusCard    float32 = 12  // panes, settings groups, popovers
	RadiusSheet   float32 = 16  // dialogs and sheets
	Capsule       float32 = 999 // pill buttons, segmented tracks
)

// SpaceUnit is the step of the spacing scale: Space(n) is n steps.
const SpaceUnit float32 = 4

// Space is n steps of the spacing scale (4, 8, 12, 16, 20, 24 …).
func Space(n float32) float32 { return n * SpaceUnit }

// DenseUnit is MyGo's spacing step in dense lists, as a files table: its
// rows and header are 24 DIPs, not 32.
const DenseUnit float32 = 3

// Dense is a copy of base for dense lists, for ui.Themed: MyGo's widgets
// inside take the DenseUnit step and the Footnote size.
func Dense(base ui.Theme) *ui.Theme {
	th := base
	th.Spacing, th.FontSize = DenseUnit, Footnote
	return &th
}

// The type scale, in DIPs, after Apple's text styles on the Mac.
const (
	Caption  float32 = 11 // details, counts, secondary lines
	Footnote float32 = 12 // pane headers, banners
	Body     float32 = 13 // everything else
	Title3   float32 = 15 // the sidebar's app name, page headers
	Title2   float32 = 17 // dialog titles
	Title1   float32 = 22 // the settings page title, the welcome
)

// Shadow is one soft drop shadow.
type Shadow struct {
	X, Y, Blur, Spread float32
	Color              ui.Color
}

// On gives e the shadow.
func (s Shadow) On(e ui.Element) ui.Element { return e.Shadow(s.X, s.Y, s.Blur, s.Spread, s.Color) }

// Tokens are the colors and metrics of one appearance.
type Tokens struct {
	Dark                                      bool
	Background, Surface, SurfaceHover         ui.Color
	SurfacePressed, Border, Text, TextMuted   ui.Color
	Accent, AccentHover, AccentPressed        ui.Color
	AccentText, Danger, Warning, Success      ui.Color
	Selection, Focus, Sidebar, TabActive      ui.Color
	Inverse, InverseText, Scrollbar           ui.Color
	Radius, Spacing, FontSize, ScrollbarWidth float32

	// Canvas is the content area behind the panes, beside the sidebar.
	Canvas ui.Color
	// Inset is the fill of what sits on the canvas: panes, cards, the
	// selected tab, menus.
	Inset ui.Color
	// Glass is the translucent fill of the toolbar and of the controls
	// floating on content; GlassEdge is its hairline.
	Glass, GlassEdge ui.Color
	// Line is a hairline separator; Hover and Press tint a control under
	// the pointer and pressed, over any fill.
	Line, Hover, Press ui.Color
	// Fill is the quiet fill of capsule controls at rest.
	Fill ui.Color
	// Card is the shadow of a card on the canvas, Lift that of a sheet
	// or popover floating over the window.
	Card, Lift Shadow
	Terminal   Terminal
}

// LightTokens are the tokens of the light appearance. They and
// DarkTokens are made once: views ask for them many times a frame.
func LightTokens() Tokens { return lightTokens }

var lightTokens, darkTokens = makeLightTokens(), makeDarkTokens()

func makeLightTokens() Tokens {
	ink := ui.Hex("#1D1D1F")
	return Tokens{
		Background:     ui.Hex("#FFFFFF"),
		Surface:        ui.Hex("#FFFFFF"),
		SurfaceHover:   ui.Hex("#F2F2F4"),
		SurfacePressed: ui.Hex("#E8E8EB"),
		Border:         ui.Hex("#DCDCE0"),
		Text:           ink,
		TextMuted:      ui.Hex("#67676C"),
		Accent:         Brand,
		AccentHover:    ui.Hex("#2A8DFC"),
		AccentPressed:  ui.Hex("#1570D6"),
		AccentText:     ui.Hex("#FFFFFF"),
		Danger:         ui.Hex("#E5342A"),
		Warning:        ui.Hex("#D97E00"),
		Success:        ui.Hex("#24A148"),
		Selection:      Brand.Alpha(0.25),
		Focus:          Brand.Alpha(0.5),
		Sidebar:        ui.Hex("#EDEDF0"),
		TabActive:      ui.Hex("#FFFFFF"),
		Inverse:        ink,
		InverseText:    ui.Hex("#FFFFFF"),
		Scrollbar:      ink.Alpha(0.28),
		Radius:         RadiusControl,
		Spacing:        SpaceUnit,
		FontSize:       Body,
		ScrollbarWidth: 8,
		Canvas:         ui.Hex("#F5F5F7"),
		Inset:          ui.Hex("#FFFFFF"),
		Glass:          ui.RGBA(0xFF, 0xFF, 0xFF, 0.72),
		GlassEdge:      ui.RGBA(0, 0, 0, 0.08),
		Line:           ui.RGBA(0, 0, 0, 0.07),
		Hover:          ui.RGBA(0, 0, 0, 0.05),
		Press:          ui.RGBA(0, 0, 0, 0.10),
		Fill:           ui.RGBA(0, 0, 0, 0.06),
		Card:           Shadow{0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.08)},
		Lift:           Shadow{0, 12, 36, 0, ui.RGBA(0, 0, 0, 0.20)},
		Terminal: Terminal{
			Foreground:   ui.Hex("#1F2328"),
			Background:   ui.Hex("#FFFFFF"),
			Cursor:       Brand,
			CursorText:   ui.Hex("#FFFFFF"),
			Selection:    Brand.Alpha(0.25),
			Match:        ui.RGBA(0xF5, 0xB0, 0x00, 0.35),
			MatchCurrent: ui.RGBA(0xF5, 0x8A, 0x00, 0.70),
			ANSI: [16]ui.Color{
				ui.Hex("#24292F"), ui.Hex("#CF222E"), ui.Hex("#116329"), ui.Hex("#9A6700"),
				ui.Hex("#0B63D6"), ui.Hex("#8250DF"), ui.Hex("#1B7C83"), ui.Hex("#6E7781"),
				ui.Hex("#57606A"), ui.Hex("#E0383E"), ui.Hex("#1A7F37"), ui.Hex("#B88700"),
				Brand, ui.Hex("#A475F9"), ui.Hex("#3192AA"), ui.Hex("#8C959F"),
			},
		},
	}
}

// DarkTokens are the tokens of the dark appearance.
func DarkTokens() Tokens { return darkTokens }

func makeDarkTokens() Tokens {
	ink := ui.Hex("#F5F5F7")
	return Tokens{
		Dark:           true,
		Background:     ui.Hex("#18181A"),
		Surface:        ui.Hex("#333336"),
		SurfaceHover:   ui.Hex("#3C3C3F"),
		SurfacePressed: ui.Hex("#47474A"),
		Border:         ui.Hex("#3A3A3D"),
		Text:           ink,
		TextMuted:      ui.Hex("#98989D"),
		Accent:         Brand,
		AccentHover:    ui.Hex("#2A8DFC"),
		AccentPressed:  ui.Hex("#1570D6"),
		AccentText:     ui.Hex("#FFFFFF"),
		Danger:         ui.Hex("#FF453A"),
		Warning:        ui.Hex("#FF9F0A"),
		Success:        ui.Hex("#30D158"),
		Selection:      Brand.Alpha(0.4),
		Focus:          Brand.Alpha(0.6),
		Sidebar:        ui.Hex("#28282A"),
		TabActive:      ui.Hex("#3A3A3D"),
		Inverse:        ink,
		InverseText:    ui.Hex("#1D1D1F"),
		Scrollbar:      ink.Alpha(0.25),
		Radius:         RadiusControl,
		Spacing:        SpaceUnit,
		FontSize:       Body,
		ScrollbarWidth: 8,
		Canvas:         ui.Hex("#1E1E20"),
		Inset:          ui.Hex("#2A2A2D"),
		Glass:          ui.RGBA(0x2C, 0x2C, 0x2E, 0.72),
		GlassEdge:      ui.RGBA(0xFF, 0xFF, 0xFF, 0.09),
		Line:           ui.RGBA(0xFF, 0xFF, 0xFF, 0.08),
		Hover:          ui.RGBA(0xFF, 0xFF, 0xFF, 0.06),
		Press:          ui.RGBA(0xFF, 0xFF, 0xFF, 0.12),
		Fill:           ui.RGBA(0xFF, 0xFF, 0xFF, 0.07),
		Card:           Shadow{0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.35)},
		Lift:           Shadow{0, 14, 40, 0, ui.RGBA(0, 0, 0, 0.50)},
		Terminal: Terminal{
			Foreground:   ui.Hex("#D8DEE9"),
			Background:   ui.Hex("#161618"),
			Cursor:       Brand,
			CursorText:   ui.Hex("#FFFFFF"),
			Selection:    Brand.Alpha(0.38),
			Match:        ui.RGBA(0xF5, 0xB0, 0x00, 0.35),
			MatchCurrent: ui.RGBA(0xF5, 0x8A, 0x00, 0.75),
			ANSI: [16]ui.Color{
				ui.Hex("#2E3440"), ui.Hex("#F0626A"), ui.Hex("#7FCF8A"), ui.Hex("#E9C46A"),
				Brand, ui.Hex("#C08CF0"), ui.Hex("#5CC8D7"), ui.Hex("#C9D1DC"),
				ui.Hex("#5C6675"), ui.Hex("#FF7B83"), ui.Hex("#9BE3A5"), ui.Hex("#F4D58A"),
				ui.Hex("#5BA8FD"), ui.Hex("#D4A9F7"), ui.Hex("#86DCE6"), ui.Hex("#F2F5F9"),
			},
		},
	}
}

// Tints are Apple's system colors, light then dark, for the tiles that
// tell hosts apart; the brand blue stays the accent.
var Tints = [][2]ui.Color{
	{ui.Hex("#28B14C"), ui.Hex("#30D158")}, // green
	{ui.Hex("#F59100"), ui.Hex("#FF9F0A")}, // orange
	{ui.Hex("#14A3BE"), ui.Hex("#40C8E0")}, // teal
	{ui.Hex("#AF52DE"), ui.Hex("#BF5AF2")}, // purple
	{ui.Hex("#5856D6"), ui.Hex("#5E5CE6")}, // indigo
	{ui.Hex("#FF2D55"), ui.Hex("#FF375F")}, // pink
	{ui.Hex("#FF3B30"), ui.Hex("#FF453A")}, // red
	{ui.Hex("#007AFF"), ui.Hex("#0A84FF")}, // blue
}

// TintNames name the Tints, in order: what a host saves as its color.
var TintNames = []string{"green", "orange", "teal", "purple", "indigo", "pink", "red", "blue"}

// TintNamed is the tint of a name of TintNames for the mode; ok is false
// for any other name, "" too.
func (t Tokens) TintNamed(name string) (c ui.Color, ok bool) {
	i := slices.Index(TintNames, name)
	if i < 0 {
		return c, false
	}
	return t.TintAt(i), true
}

// Tint is the system color picked for name, the same for every run.
func (t Tokens) Tint(name string) ui.Color {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h = (h ^ uint32(name[i])) * 16777619
	}
	p := Tints[h%uint32(len(Tints))]
	if t.Dark {
		return p[1]
	}
	return p[0]
}

// Material is the theme for content drawn on the window's material
// (vibrancy), whose own fill must stay clear for the material to show.
func (t Tokens) Material(base ui.Theme) *ui.Theme {
	th := t.Apply(base)
	th.Background = ui.Transparent
	return th
}

// SidebarTheme returns the theme of the sidebar: rounded rows,
// a quiet hover and an unfocused selection in gray, on the sidebar's
// own fill (or the clear material, when clear).
func (t Tokens) SidebarTheme(base ui.Theme, clear bool) *ui.Theme {
	th := t.Apply(base)
	th.Surface = t.Sidebar
	if clear {
		th.Surface, th.Background = ui.Transparent, ui.Transparent
	}
	th.SurfaceHover = t.Hover
	th.SurfacePressed = t.Press
	return th
}

// For returns the tokens of the mode under the system's appearance.
func For(m Mode, systemDark bool) Tokens {
	if m.IsDark(systemDark) {
		return DarkTokens()
	}
	return LightTokens()
}

// Apply writes the tokens over a copy of base, the theme MyGo made, and
// returns it, for Context.SetTheme.
func (t Tokens) Apply(base ui.Theme) *ui.Theme {
	th := base
	th.Dark = t.Dark
	th.Background, th.Surface, th.SurfaceHover, th.SurfacePressed = t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed
	th.Border, th.Text, th.TextMuted = t.Border, t.Text, t.TextMuted
	th.Accent, th.AccentHover, th.AccentPressed, th.AccentText = t.Accent, t.AccentHover, t.AccentPressed, t.AccentText
	// The brand accent stays in windows behind another, where MyGo would
	// fade it to gray.
	th.InactiveAccent = t.Accent
	th.Danger, th.Warning, th.Success = t.Danger, t.Warning, t.Success
	th.Selection, th.Focus = t.Selection, t.Focus
	th.Inverse, th.InverseText = t.Inverse, t.InverseText
	th.Scrollbar, th.ScrollbarWidth = t.Scrollbar, t.ScrollbarWidth
	th.Radius, th.Spacing, th.FontSize = t.Radius, t.Spacing, t.FontSize
	return &th
}

// Luminance is the relative luminance of c (WCAG 2), ignoring alpha.
func Luminance(c ui.Color) float64 {
	ch := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// Contrast is the WCAG contrast ratio of two opaque colors.
func Contrast(a, b ui.Color) float64 {
	la, lb := Luminance(a), Luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TintAt returns the i-th system tint for the mode: 0 green, 1 orange,
// 2 teal, 3 purple, 4 indigo, 5 pink, 6 red, 7 blue.
func (t Tokens) TintAt(i int) ui.Color {
	p := Tints[((i%len(Tints))+len(Tints))%len(Tints)]
	if t.Dark {
		return p[1]
	}
	return p[0]
}
