//go:build linux

package awake

import (
	"io"
	"os/exec"
	"time"
)

// System is the hold of Linux: a logind inhibitor of idle and sleep,
// taken by systemd-inhibit for as long as the program it runs, cat
// reading a pipe of the app's, runs. Closing the pipe ends it, and so
// does the app ending however it does. Without systemd-inhibit (no
// systemd) nothing is held.
func System() Backend { return &linuxHold{} }

// releaseWait is how long the inhibitor may take to end once told.
const releaseWait = 2 * time.Second

type linuxHold struct {
	cmd   *exec.Cmd
	stdin io.Closer
	done  chan struct{}
}

func (h *linuxHold) Hold() error {
	if h.cmd != nil {
		return nil
	}
	bin, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--what=idle", "--who=SSHYard", "--why=Transferring files", "--mode=block", "cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return err
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	h.cmd, h.stdin, h.done = cmd, stdin, done
	return nil
}

func (h *linuxHold) Release() error {
	if h.cmd == nil {
		return nil
	}
	cmd, stdin, done := h.cmd, h.stdin, h.done
	h.cmd, h.stdin, h.done = nil, nil, nil
	stdin.Close()
	select {
	case <-done:
	case <-time.After(releaseWait):
		cmd.Process.Kill()
		<-done
	}
	return nil
}
