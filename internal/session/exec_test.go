package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func dialClient(t *testing.T, srv *sshtest.Server) *ssh.Client {
	t.Helper()
	c, err := Dial(context.Background(), Config{
		Addr: srv.Addr, User: "demo",
		Credentials: password("secret"),
		HostKey:     trusted(t, srv).Callback(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestExecStreamsStdoutAndStderr(t *testing.T) {
	srv := server(t)
	var got atomic.Value
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		got.Store(cmd)
		ch.Write([]byte("out"))
		ch.Stderr().Write([]byte("err"))
		return 0
	}
	var out, errb bytes.Buffer
	if err := Exec(context.Background(), dialClient(t, srv), "say", &out, &errb); err != nil {
		t.Fatal(err)
	}
	if got.Load() != "say" || out.String() != "out" || errb.String() != "err" {
		t.Fatalf("cmd %v stdout %q stderr %q", got.Load(), out.String(), errb.String())
	}
}

func TestExecExitStatus(t *testing.T) {
	srv := server(t)
	srv.Exec = func(ssh.Channel, string) int { return 3 }
	err := Exec(context.Background(), dialClient(t, srv), "fail", nil, nil)
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 3 {
		t.Fatalf("%v", err)
	}
}

func TestExecCancelClosesChannel(t *testing.T) {
	srv := server(t)
	started := make(chan struct{})
	handlerDone := make(chan struct{})
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		defer close(handlerDone)
		// Stdin EOF is only a half-close; keep running until the channel closes.
		io.Copy(io.Discard, ch)
		close(started)
		for {
			if _, err := io.WriteString(ch, "."); err != nil {
				return 0
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := dialClient(t, srv)
	execDone := make(chan error, 1)
	go func() {
		execDone <- Exec(ctx, client, "hold", nil, nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("exec did not start")
	}
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("remote command still running after cancel")
	}
	select {
	case err := <-execDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return")
	}
}

func TestExecNilClient(t *testing.T) {
	if err := Exec(context.Background(), nil, "x", nil, nil); err == nil {
		t.Fatal("nil client")
	}
}

func TestExecCancelledContextDoesNotStart(t *testing.T) {
	srv := server(t)
	var ran atomic.Bool
	srv.Exec = func(ssh.Channel, string) int {
		ran.Store(true)
		return 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Exec(ctx, dialClient(t, srv), "x", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if ran.Load() {
		t.Fatal("started a command on a cancelled context")
	}
}

func TestExecStartedSeesSession(t *testing.T) {
	srv := server(t)
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		ch.Write([]byte("out"))
		return 0
	}
	var saw atomic.Bool
	var out bytes.Buffer
	err := ExecStarted(context.Background(), dialClient(t, srv), "say", &out, nil, func(s *ssh.Session) {
		if s != nil {
			saw.Store(true)
			_ = s.Signal(ssh.SIGTERM)
		}
	})
	if err != nil || !saw.Load() || out.String() != "out" {
		t.Fatalf("err %v saw %v out %q", err, saw.Load(), out.String())
	}
}

func TestExecDoesNotTakePoolRef(t *testing.T) {
	srv := server(t)
	srv.Exec = func(ssh.Channel, string) int { return 0 }
	kh := trusted(t, srv)
	p := NewPool()
	cfg := Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(), Pool: p, Key: "k",
		Credentials: password("secret")}
	client, release, err := p.Acquire(context.Background(), cfg.Key, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if p.Shared(cfg.Key) != 1 {
		t.Fatalf("refs %d", p.Shared(cfg.Key))
	}
	if err := Exec(context.Background(), client, "true", nil, nil); err != nil {
		t.Fatal(err)
	}
	if p.Shared(cfg.Key) != 1 {
		t.Fatalf("Exec took a pool ref: %d", p.Shared(cfg.Key))
	}
}
