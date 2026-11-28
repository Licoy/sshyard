package rdp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"image"
	"sync"
	"time"
)

type sessionIdentity struct {
	addr, user, domain string
	security           Security
}

func identityOf(cfg Config) sessionIdentity {
	return sessionIdentity{addr: cfg.Addr, user: cfg.User, domain: cfg.Domain, security: cfg.Security}
}

type arcTicket struct {
	connectionPeer
	cookie arcCookie
}

type connectionPeer struct {
	identity sessionIdentity
	protocol uint32
	cert     [32]byte
}

type licenseTicket struct {
	connectionPeer
	license *rdpClientLicense
}

type activeConn struct {
	transport     *rdpTransport
	cfg           Config
	cert          [32]byte
	wireMu        sync.Mutex
	channels      *virtualChannels
	caps          serverCapabilities
	graphics      graphicsState
	active        bool
	activating    bool
	reachedReady  bool
	finishBoot    func() bool
	width, height int

	// Requests and input bookkeeping are protected by Session.mu.
	wake     chan struct{}
	requests writeRequests
	writeErr error
	held     heldInput
	input    uint16
}

func (s *Session) runAttempt(run *sessionRun, cfg Config, arc *arcTicket) (bool, error) {
	if err := cfg.validate(); err != nil {
		return false, err
	}
	password, err := attemptPassword(run.ctx, cfg)
	if err != nil {
		return false, err
	}
	defer clear(password)
	boot, stopBoot := context.WithTimeout(run.ctx, cfg.Timeout)
	defer stopBoot()
	report := func(step Step) { s.report(run, cfg, step) }
	t, err := openTransport(boot, cfg, password, report)
	if err != nil {
		return false, bootstrapError(boot, err)
	}
	defer t.Close()
	c := s.newActiveConn(run, t, cfg)
	stopLifetime := context.AfterFunc(run.ctx, c.disconnect)
	defer stopLifetime()
	stopBootstrapClose := context.AfterFunc(boot, func() { _ = t.Close() })
	defer stopBootstrapClose()
	c.finishBoot = func() bool {
		ok := stopBootstrapClose() && boot.Err() == nil
		stopBoot()
		return ok
	}
	if err = c.sendClientInfo(password, arc); err != nil {
		return false, bootstrapError(boot, err)
	}
	clear(password)
	report(StepLicense)

	writerCtx, stopWriter := context.WithCancel(run.ctx)
	defer stopWriter()
	writerDone := make(chan struct{})
	go s.writeLoop(writerCtx, c, writerDone)
	err = s.readLoop(run, c)
	stopWriter()
	_ = t.Close()
	<-writerDone
	if c.writeErr != nil {
		err = c.writeErr
	}
	if !c.reachedReady {
		err = bootstrapError(boot, err)
	}
	return c.reachedReady, err
}

func bootstrapError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Kind: KindNetwork, Err: context.DeadlineExceeded}
	}
	return err
}

func attemptPassword(ctx context.Context, cfg Config) ([]uint16, error) {
	if cfg.Password == nil {
		return nil, nil
	}
	text, err := cfg.Password(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Kind: KindNetwork, Err: ctx.Err()}
		}
		return nil, &Error{Kind: KindAuth, Err: errors.New("could not obtain the RDP password")}
	}
	units, err := infoUnits(text)
	if err != nil {
		return nil, &Error{Kind: KindAuth, Err: err}
	}
	return units, nil
}

func (s *Session) newActiveConn(run *sessionRun, t *rdpTransport, cfg Config) *activeConn {
	c := &activeConn{transport: t, cfg: cfg, wake: make(chan struct{}, 1)}
	if raw, ok := t.Conn.(*tls.Conn); ok {
		if peer := raw.ConnectionState().PeerCertificates; len(peer) > 0 {
			c.cert = sha256.Sum256(peer[0].Raw)
		}
	}
	c.channels = newVirtualChannels(t.channels, c.sendChannel, func(text string) { s.receiveClipboard(run, c, text) })
	c.graphics.bitmap = func(rect image.Rectangle, patch *image.RGBA) error { return s.bitmapPatch(run, rect, patch) }
	c.graphics.pointer = func(p Pointer) { s.setPointer(run, p) }
	c.graphics.position = func(pos image.Point) { s.setPointerPosition(run, pos) }
	return c
}

func (c *activeConn) disconnect() {
	if c.wireMu.TryLock() {
		_ = c.transport.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_ = c.transport.disconnect()
		c.wireMu.Unlock()
	}
	_ = c.transport.Close()
}

func (c *activeConn) sendClientInfo(password []uint16, arc *arcTicket) error {
	var cookie []byte
	if arc != nil && arc.connectionPeer == c.peer() && c.cert != [32]byte{} {
		cookie = arc.cookie.clientPacket()
		defer clear(cookie)
	}
	p, err := clientInfo(c.cfg, c.transport.LocalAddr(), c.transport.protocol, password, cookie)
	if err != nil {
		return &Error{Kind: KindAuth, Err: err}
	}
	defer clear(p)
	return c.transport.sendData(c.transport.globalID, p)
}

func (c *activeConn) peer() connectionPeer {
	return connectionPeer{identity: identityOf(c.cfg), protocol: c.transport.protocol, cert: c.cert}
}

func (s *Session) readLoop(run *sessionRun, c *activeConn) error {
	license := newLicenseExchange(c.cfg, c.transport)
	defer license.clearKeys()
	peer := c.peer()
	s.mu.Lock()
	if s.license != nil && s.license.connectionPeer == peer && c.cert != [32]byte{} {
		license.cached = s.license.license
	}
	s.mu.Unlock()
	startedLicensing, complete := false, false
	for {
		channel, p, fast, err := c.transport.receiveData()
		if err != nil {
			return err
		}
		c.wireMu.Lock()
		if !fast && channel == c.transport.globalID && !c.active && !c.reachedReady {
			handled, done, licenseErr := license.receive(p)
			if handled || licenseErr != nil {
				startedLicensing, complete = true, done
				if done && license.issued != nil {
					s.saveLicense(run, peer, license.issued)
				}
				c.wireMu.Unlock()
				if licenseErr != nil {
					return licenseErr
				}
				continue
			}
			if startedLicensing && !complete {
				c.wireMu.Unlock()
				return &Error{Kind: KindLicense, Reason: ReasonProtocol, Err: errors.New("RDP server ended licensing prematurely")}
			}
		}
		err = s.receive(run, c, channel, p, fast)
		c.wireMu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (s *Session) saveLicense(run *sessionRun, peer connectionPeer, license *rdpClientLicense) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == run && peer.identity == identityOf(s.cfg) && peer.cert != [32]byte{} {
		s.license = &licenseTicket{connectionPeer: peer, license: license}
	}
}

func (s *Session) receive(run *sessionRun, c *activeConn, channel uint16, p []byte, fast bool) error {
	if fast {
		if !c.active || p[0]&0xc0 != 0 {
			return protocolError("unexpected secured RDP fast-path output")
		}
		return graphicsError(c.graphics.fastOutput(p[1:]))
	}
	if channel != c.transport.globalID {
		return c.channels.receive(channel, p)
	}
	for len(p) > 0 {
		share, next, err := readSharePDU(p)
		if err != nil {
			return err
		}
		if c.cfg.Log != nil {
			c.cfg.Log(shareLogName(share))
		}
		if err := s.receiveShare(run, c, share); err != nil {
			return err
		}
		p = next
	}
	return nil
}

func (s *Session) receiveShare(run *sessionRun, c *activeConn, p sharePDU) error {
	switch p.kind {
	case shareDemandActive:
		return s.activate(run, c, p.data)
	case shareDeactivate:
		c.active = false
		s.deactivate(run, c)
	case shareRedirect:
		return &Error{Kind: KindServer, Reason: ReasonServerDenied, Err: errors.New("RDP server redirection is unsupported")}
	case shareData:
		shareID, kind, data, err := readDataPDU(p.data)
		if err != nil {
			return err
		}
		if c.active && shareID != c.caps.shareID {
			return protocolError("RDP share identifier changed")
		}
		return s.receiveShareData(run, c, kind, data)
	}
	return nil
}

func (s *Session) receiveShareData(run *sessionRun, c *activeConn, kind byte, p []byte) error {
	switch kind {
	case dataUpdate:
		return graphicsError(c.graphics.slowUpdate(p))
	case dataPointer:
		return graphicsError(c.graphics.slowPointer(p))
	case dataSynchronize:
		if len(p) != 4 || binary.LittleEndian.Uint16(p) != 1 {
			return protocolError("invalid RDP Synchronize PDU")
		}
	case dataControl:
		if len(p) != 8 {
			return protocolError("invalid RDP Control PDU")
		}
	case dataFontMap:
		if len(p) != 8 || !c.activating {
			return protocolError("invalid RDP Font Map PDU")
		}
		return s.ready(run, c)
	case dataSaveSession:
		cookie, err := sessionInfo(p)
		if err == nil && cookie != nil && c.caps.arc {
			s.saveARC(run, c, *cookie)
		}
		return err
	case dataErrorInfo:
		return errorInfo(p)
	case dataMonitor:
		return s.monitorLayout(run, c, p)
	case 0x25: // Shutdown Denied
		if len(p) != 0 {
			return protocolError("invalid RDP Shutdown Denied PDU")
		}
	}
	return nil
}

func (c *activeConn) sendShare(kind byte, data []byte) error {
	t := c.transport
	return c.sendChannel(t.globalID, dataPDU(t.userID, c.caps.shareID, kind, data))
}

func (c *activeConn) sendChannel(channel uint16, data []byte) error {
	if err := c.writeDeadline(); err != nil {
		return err
	}
	return c.transport.sendData(channel, data)
}

func (c *activeConn) writeDeadline() error {
	if err := c.transport.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return transportError(err)
	}
	return nil
}

func shareLogName(p sharePDU) string {
	switch p.kind {
	case shareDemandActive:
		return "Demand Active PDU"
	case shareDeactivate:
		return "Deactivate All PDU"
	case shareRedirect:
		return "Server Redirection PDU"
	case shareData:
		return "Share Data PDU"
	default:
		return "Share Control PDU"
	}
}
