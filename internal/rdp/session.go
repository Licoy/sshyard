package rdp

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"
)

// Session owns one remote desktop and reconnects it when configured to do so.
// Set the callbacks before starting the session.
type Session struct {
	OnChange    func()
	OnClipboard func(text string)

	mu            sync.Mutex
	cfg           Config
	opts          Options
	status        Status
	run           *sessionRun
	visible       bool
	resizeTimer   *time.Timer
	resizeSeq     uint64
	resizePending bool
	arc           *arcTicket
	license       *licenseTicket

	frame            desktopFrame
	pointerShape     Pointer
	pointerPos       image.Point
	hasPointerPos    bool
	notifyRunning    bool
	notifyPending    bool
	clipboard        string
	clipboardPending bool
}

type sessionRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   *activeConn // guarded by Session.mu
}

// New prepares an idle session without opening a network connection.
func New(cfg Config, opts Options) *Session {
	return &Session{
		cfg: normalizeConfig(cfg), opts: opts, visible: true,
		status:       Status{State: Idle, Since: time.Now()},
		pointerShape: Pointer{Default: true},
	}
}

// Start opens the connection in the background.
func (s *Session) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State == Idle {
		s.startLocked(Connecting, 0)
	}
}

// Reconnect replaces any running connection with a new attempt.
func (s *Session) Reconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := Connecting
	if s.status.Connects > 0 {
		state = Reconnecting
	}
	attempt := s.status.Attempt + 1
	if s.status.State == Idle {
		attempt = 0
	}
	s.startLocked(state, attempt)
}

func (s *Session) startLocked(state State, attempt int) {
	if s.run != nil {
		s.run.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &sessionRun{ctx: ctx, cancel: cancel}
	s.run = run
	s.status.Attempt = attempt
	s.resetDesktopLocked()
	s.setStatusLocked(state, StepDial, nil)
	go s.runSession(run)
}

// Close cancels pending work and disconnects the desktop.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State == Disconnected && s.status.Err == nil && s.run == nil {
		return
	}
	s.cancelResizeLocked()
	s.resizePending = false
	if s.run != nil {
		s.run.cancel()
		s.run = nil
	}
	s.arc = nil
	s.license = nil
	s.resetDesktopLocked()
	s.setStatusLocked(Disconnected, s.status.Step, nil)
}

// Status reports a copy of the current state and negotiated desktop size.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// SetConfig replaces settings for subsequent connection attempts.
func (s *Session) SetConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelResizeLocked()
	s.resizePending = false
	if identityOf(s.cfg) != identityOf(cfg) {
		s.arc = nil
		s.license = nil
	}
	s.cfg = normalizeConfig(cfg)
}

func (s *Session) setStatusLocked(state State, step Step, err error) {
	if state != s.status.State {
		s.status.Since = time.Now()
	}
	s.status.State, s.status.Step, s.status.Err = state, step, err
	s.notifyLocked()
}

func (s *Session) report(run *sessionRun, cfg Config, step Step) {
	if cfg.Log != nil {
		cfg.Log([...]string{"dial", "TLS", "auth", "license", "capabilities", "ready"}[step])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == run {
		s.status.Step = step
		s.notifyLocked()
	}
}

func (s *Session) notifyLocked() {
	if s.OnChange == nil {
		return
	}
	s.notifyPending = true
	s.scheduleNotificationsLocked()
}

func (s *Session) scheduleNotificationsLocked() {
	if !s.notifyRunning {
		s.notifyRunning = true
		go s.dispatchChanges()
	}
}

func (s *Session) dispatchChanges() {
	for {
		s.mu.Lock()
		if !s.notifyPending && !s.clipboardPending {
			s.notifyRunning = false
			s.mu.Unlock()
			return
		}
		callback := s.OnChange
		changed := s.notifyPending
		clipboard, clipboardCallback := s.clipboard, s.OnClipboard
		hasClip := s.clipboardPending && s.cfg.Clipboard
		s.notifyPending = false
		s.clipboard, s.clipboardPending = "", false
		s.mu.Unlock()
		if hasClip && clipboardCallback != nil {
			clipboardCallback(clipboard)
		}
		if changed && callback != nil {
			callback()
		}
	}
}

var errResizeReconnect = errors.New("RDP display resize requires reconnection")

func (s *Session) runSession(run *sessionRun) {
	defer run.cancel()
	retries := 0
	for {
		s.mu.Lock()
		if s.run != run {
			s.mu.Unlock()
			return
		}
		cfg, arc := s.cfg, s.arc
		connectedBefore := s.status.Connects > 0
		s.mu.Unlock()

		connected, err := s.runAttempt(run, cfg, arc)
		if connected {
			retries = 0
		}
		if run.ctx.Err() != nil {
			return
		}
		delay, retry := connectionRetry(err, s.opts.AutoReconnect && (connectedBefore || connected), retries)
		if delay > 0 {
			retries++
		}
		if !s.afterAttempt(run, err, retry) || !waitRetry(run.ctx, delay) {
			return
		}
	}
}

func connectionRetry(err error, auto bool, retries int) (time.Duration, bool) {
	if errors.Is(err, errResizeReconnect) {
		return 0, true
	}
	if !auto || retries >= 10 || !Retryable(err) {
		return 0, false
	}
	return min(30*time.Second, time.Second<<retries), true
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	if delay == 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Session) afterAttempt(run *sessionRun, err error, retry bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != run {
		return false
	}
	run.conn = nil
	if retry {
		s.status.Attempt++
		s.resetDesktopLocked()
		if errors.Is(err, errResizeReconnect) {
			err = nil
		}
		state := Reconnecting
		if s.status.Connects == 0 {
			state = Connecting
		}
		s.setStatusLocked(state, StepDial, err)
	} else {
		s.run, s.arc = nil, nil
		state := Failed
		kind, reason := Classify(err)
		if s.status.Connects > 0 && kind == KindNetwork {
			state = Disconnected
		}
		switch reason {
		case ReasonLoggedOff, ReasonAdminEnded, ReasonReplaced, ReasonIdleTimeout:
			state = Disconnected
		}
		s.setStatusLocked(state, s.status.Step, err)
	}
	return retry
}
