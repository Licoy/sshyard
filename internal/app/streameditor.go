package app

import (
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/serial"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// serialPorts lists the serial ports of this computer; a variable so that
// tests can stand in.
var serialPorts = serial.Ports

// stopBitChoices are the stop-bit settings, as the editor offers them.
var stopBitChoices = []serial.StopBits{serial.Stop1, serial.Stop1Half, serial.Stop2}

// streamForm is the editor's fields for Telnet and serial hosts.
type streamForm struct {
	// proto is the protocol's index in protocols.
	proto int
	// device and the settings are a serial host's line; dataBits is the
	// index of 5..8, stopBits of stopBitChoices.
	device             string
	baud               int
	dataBits, stopBits int
	parity             serial.Parity
	// ports are the serial ports found, portsRead once they were looked
	// for; portPick is the ports select.
	ports     []string
	portsRead bool
	portPick  string
	// newLine is the index of newLineChoices, backspace of
	// backspaceChoices.
	newLine   int
	backspace int
	localEcho bool
}

// protocol is the protocol chosen.
func (f *hostForm) protocol() assets.Protocol {
	if f.proto < 0 || f.proto >= len(protocols) {
		return assets.ProtoSSH
	}
	return protocols[f.proto]
}

// loadStream fills the stream fields from h, or the defaults of a new
// host for nil.
func (f *hostForm) loadStream(h *assets.Host) {
	f.baud, f.dataBits = serial.DefaultBaud, 3
	if h == nil {
		return
	}
	f.proto = max(0, slices.Index(protocols, h.Protocol))
	f.newLine = max(0, slices.Index(newLineChoices, h.NewLine))
	f.backspace = max(0, slices.Index(backspaceChoices, h.Backspace))
	f.localEcho = h.LocalEcho
	if h.Serial != nil {
		sc := h.Serial.Config().Normalized()
		f.device, f.baud, f.parity = sc.Device, sc.Baud, sc.Parity
		f.dataBits = min(3, max(0, sc.DataBits-5))
		f.stopBits = max(0, slices.Index(stopBitChoices, sc.StopBits))
	}
}

// applyStream puts the stream fields into h.
func (f *hostForm) applyStream(h *assets.Host) {
	h.Protocol = f.protocol()
	h.NewLine, h.LocalEcho = newLineChoices[min(max(0, f.newLine), len(newLineChoices)-1)], f.localEcho
	h.Backspace = backspaceChoices[min(max(0, f.backspace), len(backspaceChoices)-1)]
	h.Serial = nil
	if h.Protocol == assets.ProtoSerial {
		h.Serial = &assets.SerialLine{Device: strings.TrimSpace(f.device), Baud: f.baud, DataBits: 5 + f.dataBits,
			Parity: string(f.parity), StopBits: string(stopBitChoices[min(max(0, f.stopBits), len(stopBitChoices)-1)])}
	}
}

// setProtocol switches the editor from protocol index prev to f.proto:
// a login the new protocol has not falls back to its first, and a default
// port follows.
func (a *App) setProtocol(m locales.Messages, f *hostForm, prev int) {
	old := assets.ProtoSSH
	if prev >= 0 && prev < len(protocols) {
		old = protocols[prev]
	}
	p := f.protocol()
	if f.auth == "" {
		f.auth = a.authLabel(m, f.orig)
	}
	choices := a.protoAuthChoices(m, p)
	if !slices.ContainsFunc(choices, func(ch authChoice) bool { return ch.label == f.auth }) {
		f.auth = choices[0].label
		if p == assets.ProtoSSH {
			f.auth = a.askLabel(m)
		}
	}
	if port := strings.TrimSpace(f.port); p != assets.ProtoSerial && (port == "" || port == strconv.Itoa(old.DefaultPort())) {
		f.port = strconv.Itoa(p.DefaultPort())
	}
	if p == assets.ProtoSerial && !f.portsRead {
		f.readPorts()
	}
}

// readPorts looks for the serial ports, and picks the first for a host
// that has none yet.
func (f *hostForm) readPorts() {
	f.ports, _ = serialPorts()
	f.portsRead = true
	if strings.TrimSpace(f.device) == "" && len(f.ports) > 0 {
		f.device = f.ports[0]
	}
}

// protocolField is the editor's protocol switch.
func (a *App) protocolField(c *ui.Context, m locales.Messages, f *hostForm) {
	ui.Field(c, m.FieldProtocol(), func() {
		prev := f.proto
		if formSegmented(c.Key("f:proto"), &f.proto, false, m.ProtocolSSH(), m.ProtocolTelnet(), m.ProtocolSerial()).Label(m.FieldProtocol()).Changed() {
			a.setProtocol(m, f, prev)
		}
	})
}

// serialFields are a serial host's port and line settings.
func (a *App) serialFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	if !f.portsRead {
		f.readPorts()
	}
	ui.Field(c, m.FieldDevice(), func() {
		ui.Column(c).Gap(6).FillWidth().Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
				formTextInput(c.Key("f:device"), &f.device).Placeholder(devicePlaceholder()).Label(m.FieldDevice()).Grow(1).Shrink(1)
				if (IconButton{Icon: icons.Refresh, Label: m.RefreshPorts()}).Clicked(c, "f:ports") {
					f.readPorts()
				}
			})
			if len(f.ports) == 0 {
				ui.Text(c, m.NoSerialPorts()).FontSize(theme.Caption).TextColor(tok.TextMuted)
				return
			}
			f.portPick = m.FieldDevice()
			if slices.Contains(f.ports, strings.TrimSpace(f.device)) {
				f.portPick = strings.TrimSpace(f.device)
			}
			all := append([]string{m.FieldDevice()}, f.ports...)
			if formSelect(c.Key("f:portpick"), &f.portPick, all).Label(m.FieldDevice()).FillWidth().Changed() && f.portPick != m.FieldDevice() {
				f.device = f.portPick
			}
		})
	})
	ui.Field(c, m.FieldBaud(), func() {
		bauds := make([]string, 0, len(serial.Bauds)+1)
		for _, b := range serial.Bauds {
			bauds = append(bauds, strconv.Itoa(b))
		}
		baud := strconv.Itoa(f.baud)
		if !slices.Contains(bauds, baud) {
			bauds = append(bauds, baud)
		}
		if formSelect(c.Key("f:baud"), &baud, bauds).Label(m.FieldBaud()).Width(140).Changed() {
			if b, err := strconv.Atoi(baud); err == nil {
				f.baud = b
			}
		}
	})
	ui.Field(c, m.FieldDataBits(), func() {
		formSegmented(c.Key("f:databits"), &f.dataBits, false, "5", "6", "7", "8").Label(m.FieldDataBits())
	})
	ui.Field(c, m.FieldParity(), func() {
		labels, codes := parityChoices(m)
		lbl := labels[max(0, slices.Index(codes, f.parity))]
		if formSelect(c.Key("f:parity"), &lbl, labels).Label(m.FieldParity()).Width(140).Changed() {
			f.parity = codes[max(0, slices.Index(labels, lbl))]
		}
	})
	ui.Field(c, m.FieldStopBits(), func() {
		formSegmented(c.Key("f:stopbits"), &f.stopBits, false, "1", "1.5", "2").Label(m.FieldStopBits())
	})
}

// streamTermFields are what Return and Backspace send and the local echo
// of a Telnet or serial host.
func (a *App) streamTermFields(c *ui.Context, m locales.Messages, tok theme.Tokens, f *hostForm) {
	ui.Field(c, m.FieldNewLine(), func() {
		formSegmented(c.Key("f:newline"), &f.newLine, false, newLineLabels...).Label(m.FieldNewLine())
	})
	ui.Field(c, m.FieldBackspace(), func() {
		formSegmented(c.Key("f:backspace"), &f.backspace, false, backspaceLabels...).Label(m.FieldBackspace())
	})
	ui.Field(c, "", func() {
		ui.Column(c).Gap(6).Children(func() {
			formCheckbox(c.Key("f:echo"), &f.localEcho, m.LocalEcho())
			ui.Text(c, m.LocalEchoHint()).FontSize(theme.Caption).TextColor(tok.TextMuted)
		})
	})
}

// devicePlaceholder is how a serial port is named on this system.
func devicePlaceholder() string {
	switch runtime.GOOS {
	case "windows":
		return "COM3"
	case "darwin":
		return "/dev/cu.usbserial-0001"
	}
	return "/dev/ttyUSB0"
}
