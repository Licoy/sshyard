package rdp

import (
	"context"
	"errors"
	"time"
)

const maxQueuedInput = 1024

type desktopSize struct {
	width, height, scale int
}

type writeRequests struct {
	input      []wireInput
	overflow   bool
	visibility int8 // -1 hide, 1 show, 0 unchanged
	resize     *desktopSize
	clipboard  string
	hasClip    bool
}

func (c *activeConn) enqueue(events []wireInput) {
	if len(events) == 0 {
		return
	}
	if len(c.requests.input)+len(events) > maxQueuedInput {
		c.requests.overflow = true
	} else {
		c.requests.input = append(c.requests.input, events...)
	}
	c.signal()
}

func (c *activeConn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (s *Session) writeLoop(ctx context.Context, c *activeConn, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		}
		if err := s.flushRequests(ctx, c); err != nil {
			c.writeErr = err
			_ = c.transport.Close()
			return
		}
	}
}

func (s *Session) flushRequests(ctx context.Context, c *activeConn) error {
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	s.mu.Lock()
	if ctx.Err() != nil || s.connectedLocked() != c {
		s.mu.Unlock()
		return nil
	}
	r := c.requests
	c.requests = writeRequests{}
	clipEnabled := s.cfg.Clipboard && c.cfg.Clipboard
	s.mu.Unlock()
	if r.overflow {
		return &Error{Kind: KindOther, Err: errors.New("RDP input queue exceeded its limit")}
	}
	for remaining := r.input; len(remaining) > 0; {
		n := min(len(remaining), maxInputEvents)
		if err := c.sendInput(remaining[:n]); err != nil {
			return err
		}
		remaining = remaining[n:]
	}
	if r.visibility != 0 {
		if err := c.sendVisibility(r.visibility > 0); err != nil {
			return err
		}
	}
	if clipEnabled && r.hasClip {
		var invalid *clipboardInputError
		if err := c.channels.setClipboard(r.clipboard); err != nil && !errors.As(err, &invalid) {
			return err
		}
	}
	if r.resize != nil {
		handled, err := c.channels.resize(r.resize.width, r.resize.height, r.resize.scale)
		if err != nil {
			return err
		}
		if !handled {
			return errResizeReconnect
		}
	}
	return nil
}

func (c *activeConn) sendInput(events []wireInput) error {
	if c.caps.input&(inputFlagFast|inputFlagFast2) != 0 {
		p, err := encodeFastInput(events)
		if err != nil {
			return err
		}
		if err := c.writeDeadline(); err != nil {
			return err
		}
		if err := writeRDP(c.transport, p); err != nil {
			return transportError(err)
		}
		return nil
	}
	p, err := encodeSlowInput(events)
	if err != nil {
		return err
	}
	return c.sendShare(dataInput, p)
}

// Resize requests a display update after 300 milliseconds without new sizes.
func (s *Session) Resize(width, height, scale int) {
	size := desktopSize{width: min(max(width, 200), 8192) &^ 1, height: min(max(height, 200), 8192), scale: min(max(scale, 100), 500)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State == Disconnected || s.status.State == Failed {
		return
	}
	s.cancelResizeLocked()
	seq := s.resizeSeq
	s.resizeTimer = time.AfterFunc(300*time.Millisecond, func() { s.applyResize(seq, size) })
}

func (s *Session) cancelResizeLocked() {
	s.resizeSeq++
	if s.resizeTimer != nil {
		s.resizeTimer.Stop()
		s.resizeTimer = nil
	}
}

func (s *Session) applyResize(seq uint64, size desktopSize) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq != s.resizeSeq {
		return
	}
	s.resizeTimer = nil
	s.cfg.Width, s.cfg.Height, s.cfg.Scale = size.width, size.height, size.scale
	if c := s.connectedLocked(); c != nil {
		if c.requests.resize == nil && s.status.Width == size.width && s.status.Height == size.height && c.cfg.Scale == size.scale {
			return
		}
		c.requests.resize = &size
		c.signal()
	} else if s.run != nil {
		s.resizePending = true
	}
}

// SetVisible asks an active server to suppress or resume display updates.
func (s *Session) SetVisible(visible bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if visible == s.visible {
		return
	}
	s.visible = visible
	if c := s.connectedLocked(); c != nil {
		c.requests.visibility = -1
		if visible {
			c.requests.visibility = 1
		}
		c.signal()
	}
}

func (c *activeConn) sendVisibility(visible bool) error {
	if c.caps.suppress {
		if err := c.sendShare(dataSuppress, desktopRectPDU(visible, c.width, c.height)); err != nil {
			return err
		}
	}
	if visible && c.caps.refresh {
		return c.sendShare(dataRefreshRect, desktopRectPDU(true, c.width, c.height))
	}
	return nil
}

// SetClipboard sends supported plain text when clipboard redirection is enabled.
func (s *Session) SetClipboard(text string) {
	if _, err := clipboardTextSize(text); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.connectedLocked(); c != nil && s.cfg.Clipboard && c.cfg.Clipboard {
		c.requests.clipboard, c.requests.hasClip = text, true
		c.signal()
	}
}

func (s *Session) receiveClipboard(run *sessionRun, c *activeConn, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == run && s.cfg.Clipboard && c.cfg.Clipboard && s.OnClipboard != nil {
		s.clipboard, s.clipboardPending = text, true
		s.scheduleNotificationsLocked()
	}
}
