// Package rdp implements the client side of the Remote Desktop Protocol.
package rdp

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

// Security selects the external security protocol used by an RDP connection.
type Security string

const (
	SecurityAuto Security = ""
	SecurityNLA  Security = "nla"
	SecurityTLS  Security = "tls"
)

// Config is a snapshot of the settings used by one connection attempt.
type Config struct {
	Addr, Domain, User, ClientName string
	Dial                           func(context.Context, string, string) (net.Conn, error)
	Password                       func(context.Context) (string, error)
	VerifyCert                     func(addr string, chain []*x509.Certificate) error
	Security                       Security
	Width, Height, Scale           int
	Admin, Clipboard               bool
	KeyboardLayout                 uint32
	Timeout                        time.Duration
	Log                            func(string)
}

// State is the state of a desktop session.
type State int

const (
	Idle State = iota
	Connecting
	Connected
	Reconnecting
	Disconnected
	Failed
)

// Step describes the latest connection progress.
type Step int

const (
	StepDial Step = iota
	StepTLS
	StepAuth
	StepLicense
	StepCapabilities
	StepReady
)

// Status is the current state and progress of a desktop session.
type Status struct {
	State             State
	Step              Step
	Err               error
	Attempt, Connects int
	Width, Height     int
	Since             time.Time
}

// Options controls session behavior across individual attempts.
type Options struct {
	AutoReconnect bool
}

// Button names the mouse buttons carried in RDP input events.
type Button int

const (
	ButtonLeft Button = iota
	ButtonRight
	ButtonMiddle
	ButtonX1
	ButtonX2
)

func desktopBounds(width, height int) bool {
	return width >= 200 && width <= 8192 && height >= 200 && height <= 8192
}

func normalizeConfig(cfg Config) Config {
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.Width == 0 && cfg.Height == 0 {
		cfg.Width, cfg.Height = 1280, 720
	}
	cfg.Width &^= 1
	if cfg.Scale == 0 {
		cfg.Scale = 100
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "SSHYard"
	}
	if cfg.KeyboardLayout == 0 {
		cfg.KeyboardLayout = 0x0409 // en-US physical keyboard layout
	}
	return cfg
}

func (cfg Config) validate() error {
	if cfg.Dial == nil {
		return &Error{Kind: KindOther, Err: errors.New("RDP requires a connection dialer")}
	}
	if cfg.VerifyCert == nil {
		return &Error{Kind: KindCert, Err: errors.New("RDP certificate verification is required")}
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host == "" || strings.ContainsAny(host, "\x00 \t\r\n/@") || n < 1 || n > 65535 {
		return &Error{Kind: KindOther, Err: errors.New("invalid RDP address or port")}
	}
	if !desktopBounds(cfg.Width, cfg.Height) || cfg.Scale < 100 || cfg.Scale > 500 || cfg.Timeout < 0 {
		return &Error{Kind: KindOther, Err: errors.New("invalid RDP dimensions, scale or timeout")}
	}
	switch cfg.Security {
	case SecurityAuto, SecurityNLA, SecurityTLS:
	default:
		return &Error{Kind: KindSecurity, Reason: ReasonProtocol, Err: errors.New("unknown RDP security mode")}
	}
	return nil
}
