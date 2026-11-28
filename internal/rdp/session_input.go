package rdp

import (
	"cmp"
	"image"
	"slices"
	"unicode/utf8"
)

const pauseScan = 0xe11d // Input adapter marker for the E1 Pause sequence.

type heldInput struct {
	keys    map[wireInput]bool
	buttons uint8
	pos     image.Point
}

// Key queues a positional Set 1 key transition.
func (s *Session) Key(scancode uint16, extended, down bool) {
	if scancode == pauseScan {
		if down {
			s.queueInput([]wireInput{
				scanInput(0x1d, false, true, true), scanInput(0x45, false, false, true),
				scanInput(0x1d, false, true, false), scanInput(0x45, false, false, false),
			})
		}
		return
	}
	if scancode == 0 || scancode > 0xff {
		return
	}
	s.queueKeys([]wireInput{scanInput(uint8(scancode), extended, false, down)})
}

// Unicode queues a Unicode key transition for a server that supports it.
func (s *Session) Unicode(r rune, down bool) {
	if utf8.ValidRune(r) {
		s.queueKeys(unicodeInput(r, down))
	}
}

func (s *Session) queueKeys(events []wireInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectedLocked()
	if c == nil || (events[0].kind == inputUnicode && c.input&inputFlagUnicode == 0) {
		return
	}
	if c.held.keys == nil {
		c.held.keys = make(map[wireInput]bool)
	}
	for _, event := range events {
		key := event
		key.flags &^= inputRelease
		if event.flags&inputRelease == 0 {
			c.held.keys[key] = true
		} else {
			delete(c.held.keys, key)
		}
	}
	c.enqueue(events)
}

// MouseMove queues a remote desktop position, coalescing adjacent moves.
func (s *Session) MouseMove(x, y int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectedLocked()
	if c == nil {
		return
	}
	pos := s.mousePositionLocked(x, y)
	c.held.pos = pos
	event := pointerInput(pointerMove, uint16(pos.X), uint16(pos.Y), false)
	if last := len(c.requests.input) - 1; last >= 0 {
		prev := c.requests.input[last]
		if prev.kind == inputMouse && prev.data[0] == pointerMove {
			c.requests.input[last] = event
			return
		}
	}
	c.enqueue([]wireInput{event})
}

// MouseButton queues a button press or release at a desktop position.
func (s *Session) MouseButton(x, y int, b Button, down bool) {
	if b < ButtonLeft || b > ButtonX2 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectedLocked()
	if c == nil || b >= ButtonX1 && c.input&inputFlagMouseX == 0 {
		return
	}
	pos := s.mousePositionLocked(x, y)
	c.held.pos = pos
	if down {
		c.held.buttons |= 1 << b
	} else {
		c.held.buttons &^= 1 << b
	}
	c.enqueue([]wireInput{mouseButton(pos, b, down)})
}

func mouseButton(pos image.Point, button Button, down bool) wireInput {
	flags := [...]uint16{pointerButton1, pointerButton2, pointerButton3, pointerXButton1, pointerXButton2}[button]
	if down {
		flags |= pointerDown
	}
	return pointerInput(flags, uint16(pos.X), uint16(pos.Y), button >= ButtonX1)
}

func (s *Session) mousePositionLocked(x, y int) image.Point {
	return image.Pt(min(max(0, x), s.status.Width-1), min(max(0, y), s.status.Height-1))
}

// Wheel queues horizontal and vertical wheel deltas in Windows wheel units.
func (s *Session) Wheel(x, y, dx, dy int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectedLocked()
	if c == nil {
		return
	}
	pos := s.mousePositionLocked(x, y)
	c.held.pos = pos
	var events []wireInput
	for _, axis := range []struct {
		flags uint16
		delta int
	}{{pointerWheel, dy}, {pointerHWheel, dx}} {
		if axis.flags == pointerHWheel && c.input&inputFlagHWheel == 0 {
			continue
		}
		for delta := axis.delta; delta != 0 && len(events) < maxInputEvents; {
			part := min(255, max(-256, delta))
			flags := axis.flags | uint16(part)&pointerWheelMask
			events = append(events, pointerInput(flags, uint16(pos.X), uint16(pos.Y), false))
			delta -= part
		}
	}
	c.enqueue(events)
}

// SyncLocks sends the local toggle key state.
func (s *Session) SyncLocks(capsLock, numLock, scrollLock, kanaLock bool) {
	var flags uint8
	for i, on := range []bool{scrollLock, numLock, capsLock, kanaLock} {
		if on {
			flags |= 1 << i
		}
	}
	s.queueInput([]wireInput{syncInput(flags)})
}

// ReleaseAll releases the keys and buttons currently pressed by this client.
func (s *Session) ReleaseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectedLocked()
	if c == nil {
		return
	}
	var events []wireInput
	for key := range c.held.keys {
		key.flags |= inputRelease
		events = append(events, key)
	}
	// Keep the release sequence deterministic for simultaneous key holds.
	slices.SortFunc(events, func(a, b wireInput) int {
		return cmp.Compare(uint32(a.kind)<<24|uint32(a.flags)<<16|uint32(a.data[0]),
			uint32(b.kind)<<24|uint32(b.flags)<<16|uint32(b.data[0]))
	})
	pos := s.mousePositionLocked(c.held.pos.X, c.held.pos.Y)
	for button := ButtonLeft; button <= ButtonX2; button++ {
		if c.held.buttons&(1<<button) != 0 {
			events = append(events, mouseButton(pos, button, false))
		}
	}
	c.held = heldInput{pos: pos}
	c.enqueue(events)
}

// CtrlAltDel queues a secure attention chord.
func (s *Session) CtrlAltDel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connectedLocked()
	if c == nil {
		return
	}
	var events []wireInput
	var release []wireInput
	for _, code := range []uint8{0x1d, 0x38} {
		key := scanInput(code, false, false, true)
		if !c.held.keys[key] && !c.held.keys[scanInput(code, true, false, true)] {
			events = append(events, key)
			release = append(release, scanInput(code, false, false, false))
		}
	}
	events = append(events, scanInput(0x53, true, false, true), scanInput(0x53, true, false, false))
	slices.Reverse(release)
	c.enqueue(append(events, release...))
}

func (s *Session) queueInput(events []wireInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.connectedLocked(); c != nil {
		c.enqueue(events)
	}
}

func (s *Session) connectedLocked() *activeConn {
	if s.status.State == Connected && s.run != nil {
		return s.run.conn
	}
	return nil
}
