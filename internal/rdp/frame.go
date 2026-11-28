package rdp

import (
	"image"
	"image/draw"
)

const dirtyHistory = 64

type desktopFrame struct {
	image   *image.RGBA
	version uint64
	dirty   [dirtyHistory]image.Rectangle
	oldest  uint64
}

// CopyFrame copies the desktop pixels changed after since. A nil or
// mismatched destination or a version outside the history gets a full copy.
func (s *Session) CopyFrame(dst *image.RGBA, since uint64) (*image.RGBA, uint64, image.Rectangle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.frame
	if f.image == nil {
		return nil, f.version, image.Rectangle{}
	}
	bounds := f.image.Bounds()
	rect := bounds
	if dst == nil || dst.Bounds() != bounds {
		dst = image.NewRGBA(bounds)
	} else if since >= f.oldest && since <= f.version {
		rect = image.Rectangle{}
		for version := since + 1; version <= f.version; version++ {
			rect = rect.Union(f.dirty[version%dirtyHistory])
		}
	}
	if !rect.Empty() {
		draw.Draw(dst, rect, f.image, rect.Min, draw.Src)
	}
	return dst, f.version, rect
}

// Pointer returns the latest immutable cursor shape and hot spot.
func (s *Session) Pointer() Pointer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pointerShape
}

// PointerPosition returns the last explicit position set by the server.
func (s *Session) PointerPosition() (image.Point, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pointerPos, s.hasPointerPos
}

func (s *Session) setPointer(run *sessionRun, p Pointer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == run {
		p.Version = s.pointerShape.Version + 1
		s.pointerShape = p
		s.notifyLocked()
	}
}

func (s *Session) setPointerPosition(run *sessionRun, pos image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == run && (!s.hasPointerPos || s.pointerPos != pos) {
		s.pointerPos, s.hasPointerPos = pos, true
		s.notifyLocked()
	}
}

func (s *Session) resetDesktopLocked() {
	s.frame = desktopFrame{version: s.frame.version + 1}
	s.pointerShape = Pointer{Default: true, Version: s.pointerShape.Version + 1}
	s.pointerPos, s.hasPointerPos = image.Point{}, false
	s.clipboard, s.clipboardPending = "", false
	s.status.Width, s.status.Height = 0, 0
}

func (s *Session) resizeDesktop(run *sessionRun, width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != run {
		return
	}
	f := &s.frame
	bounds := image.Rect(0, 0, width, height)
	if f.image != nil && f.image.Bounds() == bounds {
		return
	}
	f.image = image.NewRGBA(bounds)
	draw.Draw(f.image, bounds, image.Black, image.Point{}, draw.Src)
	f.version++
	f.oldest = f.version
	if s.status.Connects > 0 {
		s.status.Width, s.status.Height = width, height
	}
	s.notifyLocked()
}

func (s *Session) bitmapPatch(run *sessionRun, dest image.Rectangle, src *image.RGBA) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != run {
		return nil
	}
	f := &s.frame
	if f.image == nil || dest.Empty() || !dest.In(f.image.Bounds()) || src.Bounds().Dx() < dest.Dx() || src.Bounds().Dy() < dest.Dy() {
		return protocolError("RDP bitmap dimensions exceed the desktop")
	}
	draw.Draw(f.image, dest, src, src.Bounds().Min, draw.Src)
	f.version++
	f.dirty[f.version%dirtyHistory] = dest
	if f.version-f.oldest > dirtyHistory {
		f.oldest = f.version - dirtyHistory
	}
	s.notifyLocked()
	return nil
}
