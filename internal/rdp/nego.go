package rdp

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const (
	protocolTLS      uint32 = 1
	protocolHybrid   uint32 = 2
	protocolHybridEx uint32 = 8
)

// openTransport completes MS-RDPBCGR connection initiation, external security,
// and channel connection. Only a TLS protocol-version mismatch permits a retry.
func openTransport(ctx context.Context, cfg Config, password []uint16, report func(Step)) (*rdpTransport, error) {
	if cfg.VerifyCert == nil {
		return nil, &Error{Kind: KindCert, Err: errors.New("RDP certificate verification is required")}
	}
	if cfg.Dial == nil {
		return nil, &Error{Kind: KindOther, Err: errors.New("RDP dial callback is required")}
	}
	transport, retry, err := connectTransport(ctx, cfg, password, report, tls.VersionTLS12)
	if retry && ctx.Err() == nil {
		transport, _, err = connectTransport(ctx, cfg, password, report, tls.VersionTLS10)
	}
	return transport, err
}

func connectTransport(ctx context.Context, cfg Config, password []uint16, report func(Step), minTLS uint16) (*rdpTransport, bool, error) {
	report(StepDial)
	raw, err := cfg.Dial(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, false, &Error{Kind: KindNetwork, Err: err}
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()

	protocol, err := negotiateRDP(raw, cfg.Security)
	if err != nil {
		return nil, false, err
	}
	report(StepTLS)
	conn, retry, err := secureRDP(ctx, raw, cfg, minTLS)
	if err != nil {
		return nil, retry, err
	}
	if protocol != protocolTLS {
		report(StepAuth)
		if err = credSSP(conn, cfg, password); err != nil {
			return nil, false, err
		}
		if protocol == protocolHybridEx {
			if err = readEarlyAuthorization(conn); err != nil {
				return nil, false, err
			}
		}
	}
	transport := &rdpTransport{Conn: conn, protocol: protocol}
	if err = transport.connectMCS(cfg); err != nil {
		return nil, false, err
	}
	if !stop() || ctx.Err() != nil {
		return nil, false, &Error{Kind: KindNetwork, Err: ctx.Err()}
	}
	ok = true
	return transport, false, nil
}

func secureRDP(ctx context.Context, raw net.Conn, cfg Config, minVersion uint16) (*tls.Conn, bool, error) {
	var certErr error
	conn := tls.Client(raw, &tls.Config{
		ServerName: rdpHostname(cfg.Addr),
		MinVersion: minVersion,
		// The mandatory callback implements the caller's trust policy, including pins.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				certErr = errors.New("RDP server did not present a certificate")
			} else {
				certErr = cfg.VerifyCert(cfg.Addr, state.PeerCertificates)
			}
			return certErr
		},
	})
	if err := conn.HandshakeContext(ctx); err != nil {
		if certErr != nil {
			return nil, false, &Error{Kind: KindCert, Err: certErr}
		}
		retry := legacyTLSVersionError(err)
		if !retry && tlsHandshakeNetworkError(err) {
			return nil, false, transportError(err)
		}
		return nil, retry, &Error{Kind: KindSecurity, Reason: ReasonProtocol, Err: err}
	}
	return conn, false, nil
}

func legacyTLSVersionError(err error) bool {
	var remote *net.OpError
	// crypto/tls uses its private alert type on the TLS record layer.
	return (errors.As(err, &remote) && remote.Op == "remote error" && remote.Err.Error() == "tls: protocol version not supported") ||
		err.Error() == "tls: server selected unsupported protocol version 301" ||
		err.Error() == "tls: server selected unsupported protocol version 302"
}

func tlsHandshakeNetworkError(err error) bool {
	var op *net.OpError
	var netErr net.Error
	if errors.As(err, &op) && (op.Op == "local error" || op.Op == "remote error") {
		return false // TLS alerts describe protocol failures.
	}
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func rdpHostname(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	host, _, _ := strings.Cut(addr, "%")
	return host
}

func requestedProtocols(mode Security) uint32 {
	if mode == SecurityTLS {
		return protocolTLS
	}
	return protocolTLS | protocolHybrid | protocolHybridEx
}

func negotiateRDP(conn io.ReadWriter, mode Security) (uint32, error) {
	requested := requestedProtocols(mode)
	request := []byte{14, 0xe0, 0, 0, 0, 0, 0, 1, 0, 8, 0}
	request = binary.LittleEndian.AppendUint32(request, requested)
	if err := writeTPKT(conn, request); err != nil {
		return 0, transportError(err)
	}
	response, _, err := readRDPFrame(conn, false)
	if err != nil {
		return 0, transportError(err)
	}
	return parseRDPSelection(response, mode, requested)
}

func parseRDPSelection(p []byte, mode Security, requested uint32) (uint32, error) {
	if len(p) < 7 || int(p[0])+1 != len(p) || p[1] != 0xd0 || p[6] != 0 {
		return 0, protocolError("invalid X.224 connection confirm")
	}
	if len(p) == 7 {
		return 0, securityError(0, "standard RDP security is unsupported")
	}
	if len(p) != 15 || binary.LittleEndian.Uint16(p[9:11]) != 8 {
		return 0, protocolError("invalid RDP negotiation response length")
	}
	value := binary.LittleEndian.Uint32(p[11:15])
	switch p[7] {
	case 2:
		if (value == protocolTLS || value == protocolHybrid || value == protocolHybridEx) &&
			requested&value != 0 && (mode != SecurityNLA || value != protocolTLS) {
			return value, nil
		}
		return 0, securityError(value, "server selected a disallowed RDP security protocol")
	case 3:
		if p[8] != 0 {
			return 0, protocolError("invalid RDP negotiation failure flags")
		}
		return 0, rdpNegotiationFailure(value)
	default:
		return 0, protocolError("unexpected RDP negotiation data")
	}
}

func rdpNegotiationFailure(code uint32) error {
	message := "server rejected RDP security negotiation"
	switch code {
	case 1:
		message = "server requires enhanced RDP security"
	case 2:
		message = "server permits only standard RDP security"
	case 3:
		message = "server has no valid RDP authentication certificate"
	case 4:
		message = "server rejected inconsistent RDP security protocols"
	case 5:
		message = "server requires RDP Network Level Authentication"
	case 6:
		message = "server requires TLS client authentication with a certificate"
	case 7:
		message = "server requires Microsoft Entra ID authentication"
	}
	return securityError(code, message)
}

func readEarlyAuthorization(r io.Reader) error {
	var p [4]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return transportError(err)
	}
	if result := binary.LittleEndian.Uint32(p[:]); result != 0 {
		return &Error{Kind: KindAuth, Reason: ReasonServerDenied, Code: result, Err: errors.New("server denied RDP user authorization")}
	}
	return nil
}

// readRDPFrame strips TPKT framing or the fast-path length field. The fast-path
// header remains at payload[0] for the output decoder.
func readRDPFrame(r io.Reader, allowFast bool) ([]byte, bool, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:2]); err != nil {
		return nil, false, err
	}
	fast := h[0] != 3
	headerLen, bodyLen := 4, 0
	if fast {
		if !allowFast || h[0]&3 != 0 {
			return nil, false, protocolError("invalid fast-path header")
		}
		headerLen, bodyLen = 2, int(h[1])
		if h[1]&0x80 != 0 {
			if _, err := io.ReadFull(r, h[2:3]); err != nil {
				return nil, false, err
			}
			headerLen, bodyLen = 3, int(h[1]&0x7f)<<8|int(h[2])
		}
	} else {
		if h[1] != 0 {
			return nil, false, protocolError("invalid TPKT reserved byte")
		}
		if _, err := io.ReadFull(r, h[2:]); err != nil {
			return nil, false, err
		}
		bodyLen = int(binary.BigEndian.Uint16(h[2:]))
	}
	bodyLen -= headerLen
	if (fast && bodyLen < 1) || (!fast && bodyLen < 3) {
		return nil, false, protocolError("invalid RDP frame length")
	}
	if fast {
		p := make([]byte, bodyLen+1)
		p[0] = h[0]
		_, err := io.ReadFull(r, p[1:])
		return p, true, err
	}
	p := make([]byte, bodyLen)
	_, err := io.ReadFull(r, p)
	return p, false, err
}

func writeTPKT(w io.Writer, p []byte) error {
	if len(p) < 3 || len(p) > 65531 {
		return protocolError("invalid TPKT payload length")
	}
	buf := binary.BigEndian.AppendUint16([]byte{3, 0}, uint16(len(p)+4))
	buf = append(buf, p...)
	defer clear(buf)
	return writeRDP(w, buf)
}

func writeRDP(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}

func protocolError(message string) error {
	return &Error{Kind: KindServer, Reason: ReasonProtocol, Err: errors.New(message)}
}

func securityError(code uint32, message string) error {
	return &Error{Kind: KindSecurity, Reason: ReasonProtocol, Code: code, Err: fmt.Errorf("%s (0x%08x)", message, code)}
}

func transportError(err error) error {
	var rdpErr *Error
	if errors.As(err, &rdpErr) {
		return err
	}
	return &Error{Kind: KindNetwork, Err: err}
}
