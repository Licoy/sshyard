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
	// ProtoRDP is a Remote Desktop Protocol server.
	ProtoRDP Protocol = "rdp"
)

// DefaultPort is the port a host of p listens on unless told otherwise;
// 0 for a serial host.
func (p Protocol) DefaultPort() int {
	switch p {
	case ProtoTelnet:
		return 23
	case ProtoSerial:
		return 0
	case ProtoRDP:
		return 3389
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

// Backspace is what the Backspace key sends to a Telnet or serial host.
type Backspace string

const (
	// BackspaceDEL sends DEL (^?), as xterm does.
	BackspaceDEL Backspace = ""
	// BackspaceBS sends BS (^H), the erase character of many network
	// and embedded devices; Ctrl+Backspace then sends DEL.
	BackspaceBS Backspace = "bs"
)

// FileProtocol is how an SSH host's Files panes reach its files.
type FileProtocol string

const (
	// FileAuto speaks SFTP, and the shell and scp where the server has
	// no SFTP.
	FileAuto FileProtocol = ""
	// FileSFTP speaks SFTP only.
	FileSFTP FileProtocol = "sftp"
	// FileSCP moves files with the shell and scp, whether or not the
	// server has SFTP.
	FileSCP FileProtocol = "scp"
)

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

// SSH reports whether the host supports SSH features such as files,
// metrics, port forwards and acting as a jump host.
func (h Host) SSH() bool { return h.Protocol == ProtoSSH }

// IsRDP reports whether the host opens a desktop session.
func (h Host) IsRDP() bool { return h.Protocol == ProtoRDP }

// LogsIn reports whether a Telnet or serial host answers its login
// prompts with its user and password.
func (h Host) LogsIn() bool {
	return (h.Protocol == ProtoTelnet || h.Protocol == ProtoSerial) && h.Auth != AuthNone
}

// streamTarget is Target for a non-SSH host.
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
		if h.IsRDP() && h.RDP != nil && h.RDP.Domain != "" {
			s = h.RDP.Domain + `\` + s
		}
	}
	if h.Port != 0 && h.Port != h.Protocol.DefaultPort() {
		s += ":" + strconv.Itoa(h.Port)
	}
	return string(h.Protocol) + "://" + s
}

// normalizeStream removes options the host's protocol does not use.
func (h *Host) normalizeStream() {
	if h.Auth == "" {
		h.Auth = AuthNone
	}
	h.AskPassphrase, h.Certificate = false, ""
	h.CwdFollow = false
	h.TerminalDir, h.RemoteDir, h.LocalDir, h.Tmux = "", "", "", ""
	h.Startup = false
	h.SFTPSudo, h.FileProtocol = false, FileAuto
	h.Metrics = MetricsFollow
	h.AIPolicy = AIPolicy{}
	if h.IsRDP() {
		h.normalizeRDP()
		return
	}
	h.JumpChain = nil
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

// validateStream is Validate for a normalized non-SSH host.
func (h Host) validateStream() error {
	switch h.Protocol {
	case ProtoTelnet, ProtoRDP:
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
	if h.IsRDP() {
		return h.RDP.validate()
	}
	switch h.NewLine {
	case NewLineCR, NewLineLF, NewLineCRLF:
	default:
		return &FieldError{"newline", "unknown"}
	}
	switch h.Backspace {
	case BackspaceDEL, BackspaceBS:
	default:
		return &FieldError{"backspace", "unknown"}
	}
	return nil
}
