//go:build windows

package session

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// conPTY is a shell on a Windows pseudo console (ConPTY, Windows 10 1809
// and later).
type conPTY struct {
	hpc  windows.Handle
	proc windows.Handle
	in   *os.File // what is typed, to the console
	out  *os.File // what the console draws
	done chan struct{}
	once sync.Once
}

func localShells() []Shell {
	return windowsShells(winProbe{
		getenv: os.Getenv,
		exists: func(path string) bool {
			fi, err := os.Stat(path)
			return err == nil && !fi.IsDir()
		},
		lookPath: exec.LookPath,
		gitRoots: gitRoots(),
		distros:  wslDistros(),
	})
}

// gitRoots are where the registry says Git for Windows is installed, for
// all users and for this one.
func gitRoots() []string {
	var roots []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, `SOFTWARE\GitForWindows`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if dir, _, err := k.GetStringValue("InstallPath"); err == nil && dir != "" {
			roots = append(roots, dir)
		}
		k.Close()
	}
	return roots
}

// wslDistros are this user's WSL distributions, the default first, as
// the registry lists them: reading it, unlike running wsl.exe, does not
// start WSL.
func wslDistros() []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Lxss`, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	def, _, _ := k.GetStringValue("DefaultDistribution")
	ids, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var names []string
	for _, id := range ids {
		d, err := registry.OpenKey(k, id, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name, _, err := d.GetStringValue("DistributionName")
		d.Close()
		if err != nil || name == "" {
			continue
		}
		if strings.EqualFold(id, def) {
			names = append([]string{name}, names...)
		} else {
			names = append(names, name)
		}
	}
	return names
}

// localEnv is the environment of a local shell: this app's, with the
// terminal named and the shell's own entries. PowerShell 7's update
// notice is off unless the user chose otherwise: under some languages it
// miscounts its padding and ends the shell as it starts (PowerShell
// 7.6.6).
func localEnv(sh Shell, term string) []string {
	env := append([]string{"POWERSHELL_UPDATECHECK=Off"}, os.Environ()...)
	env = append(env, termEnv(term)...)
	return mergeEnv(append(env, sh.Env...), true)
}

// envBlock is env as CreateProcess takes it: each entry ended by a NUL,
// the block by another. Entries with a NUL of their own are left out.
func envBlock(env []string) *uint16 {
	var b []uint16
	for _, kv := range env {
		if u, err := windows.UTF16FromString(kv); err == nil {
			b = append(b, u...)
		}
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	b = append(b, 0)
	return &b[0]
}

func startLocal(sh Shell, cols, rows int, term string) (localPTY, error) {
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, err
	}
	var hpc windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &hpc)
	// The console has its own handles to these ends now.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	if err != nil {
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, err
	}
	p := &conPTY{hpc: hpc, in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out"), done: make(chan struct{})}
	if err := p.spawn(sh, localEnv(sh, term)); err != nil {
		p.closeConsole()
		return nil, err
	}
	go func() {
		windows.WaitForSingleObject(p.proc, windows.INFINITE)
		close(p.done)
	}()
	return p, nil
}

// spawn starts sh attached to the console, in the user's home.
func (p *conPTY) spawn(sh Shell, env []string) error {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	// The attribute's value is the console handle itself, not a pointer
	// to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&p.hpc)), unsafe.Sizeof(p.hpc)); err != nil {
		return err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// No standard handles of ours: the console's are the shell's.
	si.Flags = windows.STARTF_USESTDHANDLES
	cmd, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{sh.Path}, sh.Args...)))
	if err != nil {
		return err
	}
	var dir *uint16
	if home, err := os.UserHomeDir(); err == nil {
		dir, _ = windows.UTF16PtrFromString(home)
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmd, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, envBlock(env), dir, &si.StartupInfo, &pi); err != nil {
		return err
	}
	windows.CloseHandle(pi.Thread)
	p.proc = pi.Process
	return nil
}

func (p *conPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *conPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *conPTY) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (p *conPTY) Wait() error {
	<-p.done
	var code uint32
	if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
		return err
	}
	if code != 0 {
		return errors.New("session: the shell exited with an error")
	}
	return nil
}

// ClearScreen tells the console that the terminal cleared its screen, as
// Windows Terminal's ClearPseudoConsole does: the console clears its
// buffer and repaints with the cursor's line at the top. Without it, the
// console keeps the prompt on its old row and, as it redraws with
// absolute cursor moves (PSReadLine's predictions), takes the cursor back
// down there. It does nothing before Windows 11 24H2, whose console may
// shut down on a signal it doesn't know, or once the console is closed.
func (p *conPTY) ClearScreen() error {
	if windowsBuild() < 26100 || p.hpc == 0 {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	// kernel32 doesn't export ClearPseudoConsole. The HPCON points at the
	// console's handles, first the pipe of its signals, which take a
	// 16-bit code: PTY_SIGNAL_CLEAR_WINDOW is 2.
	signals := **(**windows.Handle)(unsafe.Pointer(&p.hpc))
	var n uint32
	return windows.WriteFile(signals, []byte{2, 0}, &n, nil)
}

var procRtlGetVersion = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlGetVersion")

// windowsBuild returns the build of Windows 10 or 11, the maximum on a
// later major version. RtlGetVersion tells the version whatever the
// executable's manifest declares.
var windowsBuild = sync.OnceValue(func() uint32 {
	var v struct {
		size, major, minor, build, platform uint32
		servicePack                         [128]uint16
	}
	v.size = uint32(unsafe.Sizeof(v))
	procRtlGetVersion.Call(uintptr(unsafe.Pointer(&v)))
	switch {
	case v.major > 10:
		return ^uint32(0)
	case v.major < 10:
		return 0
	}
	return v.build
})

// Close closes the console, which ends the shell attached to it.
func (p *conPTY) Close() error {
	p.once.Do(func() {
		p.closeConsole()
		if p.proc != 0 {
			go func() {
				<-p.done
				windows.CloseHandle(p.proc)
			}()
		}
	})
	return nil
}

func (p *conPTY) closeConsole() {
	// Closing the console waits for its output to be read: the reader
	// goroutine drains it until the pipe ends.
	windows.ClosePseudoConsole(p.hpc)
	p.in.Close()
	if p.proc == 0 {
		p.out.Close()
		return
	}
	go func() {
		<-p.done
		p.out.Close()
	}()
}
