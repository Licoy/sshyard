package assets

import (
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/serial"
)

// Protocol is how a host is reached.
type Protocol string

const (
	// ProtoSSH, the zero value, is an SSH server.
	ProtoSSH Protocol = ""
	// ProtoTelnet is a Telnet server: a terminal over plain TCP.
	ProtoTelnet Protocol = "telnet"
	// ProtoSerial is a device on a serial port of this computer.
	ProtoSerial Protocol = "serial"
)

// DefaultPort is the port a host of p listens on unless told otherwise;
// 0 for a serial host.
func (p Protocol) DefaultPort() int {
	switch p {
	case ProtoTelnet:
		return 23
	case ProtoSerial:
		return 0
	}
	return 22
}

// NewLine is what Enter sends to a Telnet or serial host.
type NewLine string

const (
	NewLineCR   NewLine = ""
	NewLineLF   NewLine = "lf"
	NewLineCRLF NewLine = "crlf"
)

// Bytes are the characters sent.
func (n NewLine) Bytes() string {
	switch n {
	case NewLineLF:
		return "\n"
	case NewLineCRLF:
		return "\r\n"
	}
	return "\r"
}

// SerialLine is a serial host's port and its settings.
type SerialLine struct {
	Device   string `json:"device"`
	Baud     int    `json:"baud"`
	DataBits int    `json:"dataBits"`
	Parity   string `json:"parity"`
	StopBits string `json:"stopBits"`
}

// Config is the line as the serial package opens it.
func (l SerialLine) Config() serial.Config {
	return serial.Config{Device: l.Device, Baud: l.Baud, DataBits: l.DataBits, Parity: serial.Parity(l.Parity), StopBits: serial.StopBits(l.StopBits)}
}

func serialLine(c serial.Config) SerialLine {
	return SerialLine{Device: c.Device, Baud: c.Baud, DataBits: c.DataBits, Parity: string(c.Parity), StopBits: string(c.StopBits)}
}

// SSH reports whether the host is an SSH server, with files, AI, metrics,
// port forwards and jump hosts; Telnet and serial hosts have a terminal
// only.
func (h Host) SSH() bool { return h.Protocol == ProtoSSH }

// LogsIn reports whether a Telnet or serial host answers its login
// prompts with its user and password.
func (h Host) LogsIn() bool { return !h.SSH() && h.Auth != AuthNone }

// streamTarget is Target for a Telnet or serial host.
func (h Host) streamTarget() string {
	if h.Protocol == ProtoSerial {
		if h.Serial == nil {
			return ""
		}
		c := h.Serial.Config().Normalized()
		return c.Device + " · " + strconv.Itoa(c.Baud) + " " + c.Frame()
	}
	s := h.Address
	if strings.Contains(s, ":") && !strings.HasPrefix(s, "[") {
		s = "[" + s + "]"
	}
	if h.User != "" {
		s = h.User + "@" + s
	}
	if h.Port != 0 && h.Port != ProtoTelnet.DefaultPort() {
		s += ":" + strconv.Itoa(h.Port)
	}
	return "telnet://" + s
}

// normalizeStream is Normalize for a Telnet or serial host: what only
// SSH has is dropped.
func (h *Host) normalizeStream() {
	if h.Auth == "" {
		h.Auth = AuthNone
	}
	h.AskPassphrase, h.Certificate = false, ""
	h.JumpChain = nil
	h.CwdFollow = false
	h.TerminalDir, h.RemoteDir, h.LocalDir, h.Tmux = "", "", "", ""
	h.Startup = false
	h.SFTPSudo = false
	h.Metrics = MetricsFollow
	h.AIPolicy = AIPolicy{}
	if h.Protocol == ProtoSerial {
		h.Address, h.Port, h.ProxyID = "", 0, ""
		if h.Serial == nil {
			h.Serial = &SerialLine{}
		}
		line := serialLine(h.Serial.Config().Normalized())
		h.Serial = &line
	} else {
		h.Serial = nil
	}
}

// validateStream is Validate for a normalized Telnet or serial host.
func (h Host) validateStream() error {
	switch h.Protocol {
	case ProtoTelnet:
		switch {
		case h.Address == "":
			return &FieldError{"address", "required"}
		case strings.ContainsAny(h.Address, " \t/@"):
			return &FieldError{"address", "invalid"}
		case h.Port < 1 || h.Port > 65535:
			return &FieldError{"port", "out of range"}
		}
	case ProtoSerial:
		if h.Serial.Device == "" {
			return &FieldError{"device", "required"}
		}
		if err := h.Serial.Config().Validate(); err != nil {
			return &FieldError{"serial", err.Error()}
		}
	default:
		return &FieldError{"protocol", "unknown"}
	}
	switch h.Auth {
	case AuthNone, AuthPassword, AuthAsk:
	case AuthCredential:
		if h.CredentialID == "" {
			return &FieldError{"credential", "required"}
		}
	default:
		return &FieldError{"auth", "unknown method"}
	}
	switch h.NewLine {
	case NewLineCR, NewLineLF, NewLineCRLF:
	default:
		return &FieldError{"newline", "unknown"}
	}
	return nil
}
