package rdp

import (
	"bytes"
	"slices"
	"testing"
)

func TestRDPFastInputAnnotatedMouse(t *testing.T) {
	// MS-RDPBCGR 4.7 decrypted TS_FP_INPUT_PDU mouse move in TLS framing.
	want := []byte{0x04, 0x09, 0x20, 0x00, 0x08, 0xab, 0x02, 0x6f, 0x01}
	got, err := encodeFastInput([]wireInput{pointerInput(pointerMove, 683, 367, false)})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("fast mouse = %x, %v; want %x", got, err, want)
	}
}

func TestRDPInputEventWire(t *testing.T) {
	events := []wireInput{scanInput(0x1d, false, true, true), scanInput(0x4b, true, false, false)}
	events = append(events, unicodeInput('😀', true)...)
	events = append(events, unicodeInput('😀', false)...)
	events = append(events,
		pointerInput(0x0388, 23, 45, false), // signed vertical -120
		pointerInput(0x0478, 23, 45, false), // signed horizontal +120
		pointerInput(0x8002, 23, 45, true),  // XBUTTON2 down
		syncInput(0x05),                     // Caps Lock and Scroll Lock
	)
	fast := []byte{
		0x28, 40,
		0x04, 0x1d,
		0x03, 0x4b,
		0x80, 0x3d, 0xd8, 0x80, 0x00, 0xde,
		0x81, 0x3d, 0xd8, 0x81, 0x00, 0xde,
		0x20, 0x88, 0x03, 23, 0, 45, 0,
		0x20, 0x78, 0x04, 23, 0, 45, 0,
		0x40, 0x02, 0x80, 23, 0, 45, 0,
		0x65,
	}
	slow := []byte{
		10, 0, 0, 0,
		0, 0, 0, 0, 4, 0, 0x00, 0x02, 0x1d, 0x00, 0, 0,
		0, 0, 0, 0, 4, 0, 0x00, 0x81, 0x4b, 0x00, 0, 0,
		0, 0, 0, 0, 5, 0, 0, 0, 0x3d, 0xd8, 0, 0,
		0, 0, 0, 0, 5, 0, 0, 0, 0x00, 0xde, 0, 0,
		0, 0, 0, 0, 5, 0, 0, 0x80, 0x3d, 0xd8, 0, 0,
		0, 0, 0, 0, 5, 0, 0, 0x80, 0x00, 0xde, 0, 0,
		0, 0, 0, 0, 0x01, 0x80, 0x88, 0x03, 23, 0, 45, 0,
		0, 0, 0, 0, 0x01, 0x80, 0x78, 0x04, 23, 0, 45, 0,
		0, 0, 0, 0, 0x02, 0x80, 0x02, 0x80, 23, 0, 45, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 5, 0, 0, 0,
	}
	for _, check := range []struct {
		name string
		fn   func([]wireInput) ([]byte, error)
		want []byte
	}{
		{"fast", encodeFastInput, fast},
		{"slow", encodeSlowInput, slow},
	} {
		t.Run(check.name, func(t *testing.T) {
			got, err := check.fn(events)
			if err != nil || !bytes.Equal(got, check.want) {
				t.Fatalf("input = %x, %v; want %x", got, err, check.want)
			}
			if _, err := check.fn(nil); err == nil {
				t.Fatal("empty input PDU accepted")
			}
			if _, err := check.fn(make([]wireInput, maxInputEvents+1)); err == nil {
				t.Fatal("over-limit input PDU accepted")
			}
		})
	}
}

func TestRDPFastInputLongLength(t *testing.T) {
	event := pointerInput(pointerMove, 683, 367, false)
	events := slices.Repeat([]wireInput{event}, 18)
	got, err := encodeFastInput(events)
	want := append([]byte{0, 0x80, 130, 18}, bytes.Repeat([]byte{0x20, 0, 8, 0xab, 2, 0x6f, 1}, 18)...)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("long fast input = %x, %v; want %x", got, err, want)
	}
	invalid, err := encodeFastInput(unicodeInput(0xd800, true))
	if err != nil || !bytes.Equal(invalid, []byte{0x04, 5, 0x80, 0xfd, 0xff}) {
		t.Fatalf("invalid rune produced %x, %v", invalid, err)
	}
}
