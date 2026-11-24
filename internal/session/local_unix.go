//go:build !windows

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/creack/pty"
)

type unixPTY struct {
	f    *os.File
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// localShells is the user's login shell, run as one.
func localShells() []Shell {
	sh := os.Getenv("SHELL")
	if !filepath.IsAbs(sh) {
		sh = "/bin/sh"
		if runtime.GOOS == "darwin" {
			sh = "/bin/zsh"
		}
	}
	return []Shell{{ID: sh, Name: filepath.Base(sh), Path: sh, Args: []string{"-l"}}}
}

// startLocal starts sh in dir, or the user's home, as a terminal app does.
func startLocal(sh Shell, dir string, cols, rows int, term string) (localPTY, error) {
	cmd := exec.Command(sh.Path, sh.Args...)
	cmd.Env = localEnv(sh, term)
	cmd.Dir = localDir(dir)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	p := &unixPTY{f: f, cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func localEnv(sh Shell, term string) []string {
	env := append(append(os.Environ(), termEnv(term)...), sh.Env...)
	if runtime.GOOS != "darwin" {
		return env
	}
	env = mergeEnv(env, false)
	ctype := -1
	for i, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "LC_CTYPE" {
			ctype = i
		}
		if (key == "LANG" || key == "LC_CTYPE" || key == "LC_ALL") && value != "" {
			return env
		}
	}
	// macOS GUI launches may supply no locale; zsh then cannot echo UTF-8
	// input correctly. Default only the child's character locale, preserving
	// every explicit locale choice, including C and non-UTF-8 locales.
	if ctype >= 0 {
		env[ctype] = "LC_CTYPE=en_US.UTF-8"
		return env
	}
	return append(env, "LC_CTYPE=en_US.UTF-8")
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *unixPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (p *unixPTY) Wait() error {
	<-p.done
	return p.err
}

// Close hangs the terminal up, which ends the shell; one that is still
// there two seconds later is killed.
func (p *unixPTY) Close() error {
	err := p.f.Close()
	go func() {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			p.cmd.Process.Kill()
		}
	}()
	return err
}
