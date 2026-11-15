//go:build darwin

package awake

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// System is the hold of macOS: a power management assertion that the
// system is not to sleep while the user is idle
// (kIOPMAssertionTypePreventUserIdleSystemSleep), which leaves the display
// free to sleep.
func System() Backend { return &macHold{} }

const (
	kCFStringEncodingUTF8       = 0x08000100
	kIOPMAssertionLevelOn       = 255
	preventUserIdleSleep        = "PreventUserIdleSystemSleep"
	assertionName               = "SSHYard is transferring files"
	kIOReturnSuccess      int32 = 0
)

var iokit = sync.OnceValues(func() (f struct{ str, cfRelease, create, release uintptr }, err error) {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return f, err
	}
	io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return f, err
	}
	for _, sym := range []struct {
		lib  uintptr
		name string
		to   *uintptr
	}{
		{cf, "CFStringCreateWithCString", &f.str},
		{cf, "CFRelease", &f.cfRelease},
		{io, "IOPMAssertionCreateWithName", &f.create},
		{io, "IOPMAssertionRelease", &f.release},
	} {
		if *sym.to, err = purego.Dlsym(sym.lib, sym.name); err != nil {
			return f, err
		}
	}
	return f, nil
})

type macHold struct {
	id   uint32
	held bool
}

// cfString is s as a CFString the caller releases, 0 when it could not
// be made.
func cfString(fn uintptr, s string) uintptr {
	b := append([]byte(s), 0)
	r, _, _ := purego.SyscallN(fn, 0, uintptr(unsafe.Pointer(&b[0])), kCFStringEncodingUTF8)
	runtime.KeepAlive(b)
	return r
}

func (h *macHold) Hold() error {
	if h.held {
		return nil
	}
	f, err := iokit()
	if err != nil {
		return err
	}
	typ, name := cfString(f.str, preventUserIdleSleep), cfString(f.str, assertionName)
	defer func() {
		for _, s := range []uintptr{typ, name} {
			if s != 0 {
				purego.SyscallN(f.cfRelease, s)
			}
		}
	}()
	if typ == 0 || name == 0 {
		return errors.New("awake: no CFString")
	}
	var id uint32
	r, _, _ := purego.SyscallN(f.create, typ, kIOPMAssertionLevelOn, name, uintptr(unsafe.Pointer(&id)))
	if int32(r) != kIOReturnSuccess {
		return fmt.Errorf("awake: IOPMAssertionCreateWithName: %#x", uint32(r))
	}
	h.id, h.held = id, true
	return nil
}

func (h *macHold) Release() error {
	if !h.held {
		return nil
	}
	h.held = false
	f, err := iokit()
	if err != nil {
		return err
	}
	if r, _, _ := purego.SyscallN(f.release, uintptr(h.id)); int32(r) != kIOReturnSuccess {
		return fmt.Errorf("awake: IOPMAssertionRelease: %#x", uint32(r))
	}
	return nil
}
