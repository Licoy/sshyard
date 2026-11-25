//go:build darwin

package folder

import (
	"errors"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var errDownload = errors.New("folder: iCloud Drive did not start the download")

// foundation loads Foundation once, for a process that has not yet.
var foundation = sync.OnceValue(func() error {
	_, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	return err
})

// startDownload asks iCloud Drive to download the file p, which it keeps
// only in the cloud.
func startDownload(p string) error {
	if err := foundation(); err != nil {
		return err
	}
	cls := func(name string) objc.ID { return objc.ID(objc.GetClass(name)) }
	sel := objc.RegisterName
	pool := cls("NSAutoreleasePool").Send(sel("new"))
	defer pool.Send(sel("drain"))
	b := append([]byte(p), 0)
	s := cls("NSString").Send(sel("stringWithUTF8String:"), &b[0])
	runtime.KeepAlive(b)
	u := cls("NSURL").Send(sel("fileURLWithPath:"), s)
	fm := cls("NSFileManager").Send(sel("defaultManager"))
	if u == 0 || fm == 0 || !objc.Send[bool](fm, sel("startDownloadingUbiquitousItemAtURL:error:"), u, uintptr(0)) {
		return errDownload
	}
	return nil
}
