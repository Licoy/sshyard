package app

import (
	"hash/fnv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// lookSeed is what picks the icon and the tint of a host that has none of
// its own: its address, so that the same server looks the same under any
// name, and in every run.
func lookSeed(h assets.Host) string {
	if a := strings.ToLower(strings.TrimSpace(h.Address)); a != "" {
		return a
	}
	if h.Serial != nil && h.Serial.Device != "" {
		return strings.ToLower(h.Serial.Device)
	}
	return h.ID
}

// hostTint is the host's color: its own, else one picked from its address.
func hostTint(tok theme.Tokens, h assets.Host) ui.Color {
	if isLocal(h.ID) {
		return tok.Accent
	}
	if c, ok := tok.TintNamed(h.Color); ok {
		return c
	}
	return tok.Tint(lookSeed(h))
}

// hostIcon is the host's icon, or the text (an emoji) it shows instead.
// A host without its own gets one of the plain ones, from its address.
func hostIcon(h assets.Host) (*icons.Icon, string) {
	if isLocal(h.ID) {
		return icons.LocalShell, ""
	}
	if key, ok := strings.CutPrefix(h.Icon, assets.IconPrefix); ok {
		if ic, ok := icons.HostIconOf(key); ok {
			return ic, ""
		}
	} else if h.Icon != "" {
		return nil, h.Icon
	}
	switch h.Protocol {
	case assets.ProtoTelnet:
		return icons.TelnetHost, ""
	case assets.ProtoSerial:
		return icons.SerialHost, ""
	case assets.ProtoRDP:
		return icons.RDPHost, ""
	}
	f := fnv.New32a()
	f.Write([]byte(lookSeed(h)))
	return icons.HostIcons[f.Sum32()%icons.AutoHostIcons].Icon, ""
}

// hostAvatar is the host's tile: its icon in white on its tint, or its
// emoji on a light wash of it.
func hostAvatar(c *ui.Context, tok theme.Tokens, h assets.Host, size float32) ui.Element {
	ic, text := hostIcon(h)
	tint := hostTint(tok, h)
	if ic != nil {
		return tile(c, ic, tint, size)
	}
	return ui.Box(c).Size(size, size).Shrink(0).Radius(size * 0.28).Background(tint.Alpha(0.2)).Center().Children(func() {
		ui.Text(c, text).FontSize(size * 0.58).SingleLine().TextAlign(ui.Center)
	})
}

// groupGlyph is a group's icon: its own icon or emoji, else the folder,
// in its color or else muted.
func groupGlyph(c *ui.Context, tok theme.Tokens, look assets.GroupLook, size float32) ui.Element {
	color, tinted := tok.TintNamed(look.Color)
	if !tinted {
		color = tok.TextMuted
	}
	ic := icons.Folder
	if key, ok := strings.CutPrefix(look.Icon, assets.IconPrefix); ok {
		if hi, ok := icons.HostIconOf(key); ok {
			ic = hi
		}
	} else if look.Icon != "" {
		if !tinted {
			color = tok.Text
		}
		return ui.Text(c, look.Icon).FontSize(size * 0.85).TextColor(color).SingleLine().Shrink(0)
	}
	return ui.Icon(c, ic.SVG).Size(size, size).Shrink(0).TextColor(color)
}

// look is the host as the editor shows it so far: where it is and how
// it looks, for its tile.
func (f *hostForm) look() assets.Host {
	h := f.orig
	h.Address, h.Icon, h.Color = f.address, f.icon, f.color
	h.Protocol, h.Serial = f.protocol(), nil
	if h.Protocol == assets.ProtoSerial {
		h.Address, h.Serial = "", &assets.SerialLine{Device: strings.TrimSpace(f.device)}
	}
	if f.isNew && lookSeed(h) == "" {
		h.ID = "new"
	}
	return h
}

// iconName is the name of a host icon, for its button's label.
func iconName(m locales.Messages, key string) string {
	switch key {
	case "server":
		return m.IconServer()
	case "database":
		return m.IconDatabase()
	case "cloud":
		return m.IconCloud()
	case "computer":
		return m.IconComputer()
	case "laptop":
		return m.IconLaptop()
	case "router":
		return m.IconRouter()
	case "storage":
		return m.IconStorage()
	case "container":
		return m.IconContainer()
	case "code":
		return m.IconCode()
	case "rocket":
		return m.IconRocket()
	case "planet":
		return m.IconPlanet()
	case "test":
		return m.IconTest()
	case "git":
		return m.IconGit()
	case "home":
		return m.IconHome()
	case "office":
		return m.IconOffice()
	case "secure":
		return m.IconSecure()
	case "game":
		return m.IconGame()
	case "ubuntu":
		return m.IconUbuntu()
	case "centos":
		return m.IconCentOS()
	case "apple":
		return m.IconApple()
	case "windows":
		return m.IconWindows()
	case "android":
		return m.IconAndroid()
	}
	return key
}

// colorNames are the names of theme.TintNames, in order.
func colorNames(m locales.Messages) []string {
	return []string{m.ColorGreen(), m.ColorOrange(), m.ColorTeal(), m.ColorPurple(), m.ColorIndigo(), m.ColorPink(), m.ColorRed(), m.ColorBlue()}
}

// lookFields are an appearance in a form: one of the app's icons or an
// emoji, and a color, each back to the default with the pill autoIcon or
// autoColor names (a host's are picked from its address).
func (a *App) lookFields(c *ui.Context, m locales.Messages, tok theme.Tokens, icon, emoji, color *string, autoIcon, autoColor string) {
	ui.Column(c).Gap(10).FillWidth().Children(func() {
		ui.Row(c.Key("f:icons")).Wrap().Gap(2).FillWidth().Children(func() {
			for _, hi := range icons.HostIcons {
				on := *icon == assets.IconPrefix+hi.Key
				if (IconButton{Icon: hi.Icon, Label: iconName(m, hi.Key), Active: on, Toggle: true, Size: Small}).Clicked(c, "f:icon:"+hi.Key) {
					*icon, *emoji = assets.IconPrefix+hi.Key, ""
				}
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			in := formTextInput(c.Key("f:emoji"), emoji).Placeholder(m.EmojiPlaceholder()).Label(m.HostEmoji()).Width(200)
			if in.Changed() {
				*icon = assets.IconText(*emoji)
			}
			ui.Spacer(c)
			if pill(c, autoIcon, *icon == "", false, false) {
				*icon, *emoji = "", ""
			}
		})
		ui.Row(c.Key("f:colors")).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
			names := colorNames(m)
			for i, key := range theme.TintNames {
				on := *color == key
				sw := ui.ButtonBase(c.Key("f:color:"+key)).Label(names[i]).Tooltip(names[i]).Size(22, 22).Radius(theme.Capsule).
					Background(tok.TintAt(i)).Cursor(ui.CursorPointer).Checked(on)
				if on {
					sw.Border(2, tok.Text.Alpha(0.75))
				} else if sw.Hovered() {
					sw.Border(2, tok.Text.Alpha(0.25))
				}
				if sw.Clicked() {
					*color = key
				}
			}
			ui.Spacer(c)
			if pill(c, autoColor, *color == "", false, false) {
				*color = ""
			}
		})
	})
}
