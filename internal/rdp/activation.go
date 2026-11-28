package rdp

import (
	"context"
	"encoding/binary"
	"errors"
)

// activate negotiates capabilities and sends the client finalization sequence
// (MS-RDPBCGR 2.2.1.13 through 2.2.1.18).
func (s *Session) activate(run *sessionRun, c *activeConn, p []byte) error {
	if c.active {
		return protocolError("duplicate RDP Demand Active PDU")
	}
	s.report(run, c.cfg, StepCapabilities)
	caps, err := demandActive(p)
	if err != nil {
		return err
	}
	c.caps = caps
	c.channels.setChunkSize(caps.chunk)
	c.width, c.height = caps.width, caps.height
	s.resizeDesktop(run, c.width, c.height)
	t := c.transport
	if err := c.sendChannel(t.globalID, confirmActive(t.userID, c.cfg, caps)); err != nil {
		return err
	}
	if err := c.sendShare(dataSynchronize, words(1, serverChannelID)); err != nil {
		return err
	}
	if err := c.sendShare(dataControl, words(4, 0, 0, 0)); err != nil { // CTRLACTION_COOPERATE
		return err
	}
	if err := c.sendShare(dataControl, words(1, 0, 0, 0)); err != nil { // CTRLACTION_REQUEST_CONTROL
		return err
	}
	if err := c.sendShare(dataFontList, words(0, 0, 3, 50)); err != nil {
		return err
	}
	c.active = true
	c.activating = true
	return nil
}

func (s *Session) ready(run *sessionRun, c *activeConn) error {
	c.activating = false
	first := !c.reachedReady
	if first && !c.finishBoot() {
		return &Error{Kind: KindNetwork, Err: context.DeadlineExceeded}
	}
	s.mu.Lock()
	if s.run == run {
		if first {
			s.status.Connects++
		}
		c.reachedReady = true
		run.conn = c
		c.input = c.caps.input
		s.status.Width, s.status.Height = c.width, c.height
		s.setStatusLocked(Connected, StepReady, nil)
		c.enqueue([]wireInput{syncInput(0)})
		if s.resizePending && (s.cfg.Width != c.cfg.Width || s.cfg.Height != c.cfg.Height || s.cfg.Scale != c.cfg.Scale) {
			c.requests.resize = &desktopSize{width: s.cfg.Width, height: s.cfg.Height, scale: s.cfg.Scale}
		}
		s.resizePending = false
		if !s.visible {
			c.requests.visibility = -1
			c.signal()
		}
	}
	s.mu.Unlock()
	if c.cfg.Log != nil {
		c.cfg.Log("ready")
	}
	return nil
}

func (s *Session) deactivate(run *sessionRun, c *activeConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == run && run.conn == c {
		run.conn = nil
		c.held = heldInput{}
		c.requests = writeRequests{}
		s.setStatusLocked(Connecting, StepCapabilities, nil)
	}
}

func (s *Session) saveARC(run *sessionRun, c *activeConn, cookie arcCookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := identityOf(c.cfg)
	if s.run == run && id == identityOf(s.cfg) && c.cert != [32]byte{} {
		s.arc = &arcTicket{connectionPeer: c.peer(), cookie: cookie}
	}
}

func (s *Session) monitorLayout(run *sessionRun, c *activeConn, p []byte) error {
	width, height, err := monitorBounds(p)
	if err != nil {
		return err
	}
	c.width, c.height = width, height
	s.resizeDesktop(run, width, height)
	return nil
}

func monitorBounds(p []byte) (int, int, error) {
	le := binary.LittleEndian
	if len(p) < 4 {
		return 0, 0, protocolError("truncated RDP Monitor Layout PDU")
	}
	n := int(le.Uint32(p))
	if n < 1 || n > 16 || len(p) != 4+20*n {
		return 0, 0, protocolError("invalid RDP Monitor Layout PDU")
	}
	p = p[4:]
	var left, top, right, bottom int64
	for i := range n {
		l, t := int64(int32(le.Uint32(p))), int64(int32(le.Uint32(p[4:])))
		r, b := int64(int32(le.Uint32(p[8:]))), int64(int32(le.Uint32(p[12:])))
		if r < l || b < t {
			return 0, 0, protocolError("invalid RDP monitor bounds")
		}
		if i == 0 {
			left, top, right, bottom = l, t, r, b
		} else {
			left, top, right, bottom = min(left, l), min(top, t), max(right, r), max(bottom, b)
		}
		p = p[20:]
	}
	w, h := right-left+1, bottom-top+1
	if w > 8192 || h > 8192 || !desktopBounds(int(w), int(h)) {
		return 0, 0, &Error{Kind: KindServer, Reason: ReasonProtocol, Err: errors.New("RDP monitor layout exceeds the desktop size limits")}
	}
	return int(w), int(h), nil
}
