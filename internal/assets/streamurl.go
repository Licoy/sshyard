package assets

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Licoy/sshyard/internal/serial"
)

// ErrStreamURL is a telnet://, serial:// or rdp:// destination that cannot be
// read.
var ErrStreamURL = errors.New("assets: bad telnet://, serial:// or rdp:// destination")

// ParseStreamURL reads a quick connect destination: telnet://[user@]host[:port],
// rdp://[user@]host[:port], or
// serial://DEVICE[?baud=9600&data=8&parity=none&stop=1], DEVICE being a
// path such as /dev/ttyUSB0 or a port such as COM3. ok is false for any
// other destination, which is left to SSH. The host has no ID.
func ParseStreamURL(s string) (h Host, ok bool, err error) {
	s = strings.TrimSpace(s)
	scheme, rest, found := strings.Cut(s, "://")
	if !found {
		return Host{}, false, nil
	}
	switch strings.ToLower(scheme) {
	case "telnet":
		h, err = parseNetworkURL(ProtoTelnet, rest)
	case "rdp":
		h, err = parseNetworkURL(ProtoRDP, rest)
	case "serial":
		h, err = parseSerialURL(rest)
	default:
		return Host{}, false, nil
	}
	return h, true, err
}

func parseNetworkURL(p Protocol, rest string) (Host, error) {
	u, err := url.Parse(string(p) + "://" + strings.TrimSuffix(rest, "/"))
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return Host{}, ErrStreamURL
	}
	h := Host{Protocol: p, Address: u.Hostname(), Port: p.DefaultPort(), Auth: AuthNone}
	if p == ProtoRDP {
		if u.Fragment != "" || u.ForceQuery {
			return Host{}, ErrStreamURL
		}
		h.Auth = AuthAsk
		h.RDP = &RDPOptions{}
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Host{}, ErrStreamURL
		}
		h.Port = n
	}
	if u.User != nil {
		h.User = u.User.Username()
		if _, has := u.User.Password(); has {
			// A password in a destination ends up in history and logs.
			return Host{}, ErrStreamURL
		}
	}
	h.Name = h.Address
	if ip := net.ParseIP(h.Address); ip != nil && ip.To4() == nil {
		h.Name = "[" + h.Address + "]"
	}
	return h, nil
}

func parseSerialURL(rest string) (Host, error) {
	device, query, _ := strings.Cut(rest, "?")
	device = strings.TrimSpace(device)
	if device == "" {
		return Host{}, ErrStreamURL
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return Host{}, ErrStreamURL
	}
	c := serial.Config{Device: device}
	for key, vals := range q {
		v := strings.ToLower(strings.TrimSpace(vals[len(vals)-1]))
		switch strings.ToLower(key) {
		case "baud":
			if c.Baud, err = strconv.Atoi(v); err != nil {
				return Host{}, ErrStreamURL
			}
		case "data", "databits":
			if c.DataBits, err = strconv.Atoi(v); err != nil {
				return Host{}, ErrStreamURL
			}
		case "parity":
			c.Parity = parityOf(v)
		case "stop", "stopbits":
			c.StopBits = serial.StopBits(v)
		default:
			return Host{}, ErrStreamURL
		}
	}
	c = c.Normalized()
	if c.Validate() != nil {
		return Host{}, ErrStreamURL
	}
	line := serialLine(c)
	return Host{Protocol: ProtoSerial, Name: c.Device, Serial: &line, Auth: AuthNone}, nil
}

// parityOf reads a parity by name or by its letter in 8N1.
func parityOf(v string) serial.Parity {
	for _, p := range []serial.Parity{serial.ParityNone, serial.ParityOdd, serial.ParityEven, serial.ParityMark, serial.ParitySpace} {
		if v == string(p) || v == string(p)[:1] {
			return p
		}
	}
	return serial.Parity(v)
}
