package sftpsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ErrSudoPassword: sudo asked for a password and none was given, or the
// one given was refused.
var ErrSudoPassword = errors.New("sftp: sudo needs the user's password")

// ErrSudo: sudo refused to start the SFTP server; its message follows.
var ErrSudo = errors.New("sftp: sudo failed")

// ErrSudoNoServer: sudo ran, but the server has no sftp-server to start.
var ErrSudoNoServer = fmt.Errorf("%w: no sftp-server", ErrSudo)

// The marks the remote command writes on stderr: sudo's prompt, the SFTP
// server about to start, and no SFTP server found.
const (
	sudoPrompt   = "[sshyard-sudo-password]"
	sudoReady    = "[sshyard-sudo-ready]"
	sudoNoServer = "[sshyard-sudo-no-server]"
)

// sudoTimeout bounds how long sudo may take to start the server.
var sudoTimeout = 30 * time.Second

// sudoCommand runs the server's sftp-server as root: sudo prompts on
// stderr and reads the password from stdin, then the shell it starts
// says the server is about to take stdin and stdout over.
var sudoCommand = "sudo -S -p '" + sudoPrompt + "' sh -c '" +
	`for p in /usr/lib/openssh/sftp-server /usr/libexec/openssh/sftp-server /usr/lib/ssh/sftp-server /usr/libexec/sftp-server /usr/lib/sftp-server /usr/libexec/ssh/sftp-server /usr/lib/misc/sftp-server /usr/local/libexec/sftp-server "$(command -v sftp-server)"; do ` +
	`if [ -n "$p" ] && [ -x "$p" ]; then echo "` + sudoReady + `" >&2; exec "$p"; fi; done; echo "` + sudoNoServer + `" >&2; exit 127'`

// NewSudo is NewContext with the server's own sftp-server run as root
// through sudo, password answering its prompt ("" for none: sudo then
// must not ask). Every SFTP channel the Client opens goes through sudo.
func NewSudo(ctx context.Context, conn *ssh.Client, password string) (*Client, error) {
	c := &Client{conn: conn, operations: operationSlots(conn), sudo: &sudoAuth{password: password}}
	sub, err := c.newOperation(ctx)
	if err != nil {
		return nil, fmt.Errorf("sftp: %w", err)
	}
	return sub, nil
}

// sudoAuth is how a Client opens its channels through sudo.
type sudoAuth struct{ password string }

// Sudo reports a client whose SFTP server runs as root through sudo.
func (c *Client) Sudo() bool { return c.sudo != nil }

// sudoOperation is negotiateOperation through sudo.
func (c *Client) sudoOperation(ctx context.Context) (*Client, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	complete := false
	defer func() {
		if !complete {
			session.Close()
		}
	}()
	rd, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	wr, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := session.Start(sudoCommand); err != nil {
		return nil, err
	}
	t := time.AfterFunc(sudoTimeout, func() { session.Close() })
	err = sudoHandshake(stderr, wr, c.sudo.password)
	t.Stop()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	// What the server says on stderr from now on is not read.
	go io.Copy(io.Discard, stderr)
	sc, err := sftp.NewClientPipe(rd, wr)
	if err != nil {
		return nil, err
	}
	complete = true
	return opened(&Client{c: sc, conn: c.conn, operation: session, operations: c.operations, sudo: c.sudo}), nil
}

// sudoHandshake reads the stderr of sudoCommand until the SFTP server
// is about to start, answering sudo's first prompt with password. A
// second prompt is a password refused.
func sudoHandshake(stderr io.Reader, stdin io.Writer, password string) error {
	var seen bytes.Buffer
	asked := false
	buf := make([]byte, 512)
	for {
		n, err := stderr.Read(buf)
		seen.Write(buf[:n])
		for {
			s := seen.String()
			i := strings.Index(s, sudoPrompt)
			if i < 0 {
				break
			}
			if asked || password == "" {
				return ErrSudoPassword
			}
			asked = true
			if _, err := io.WriteString(stdin, password+"\n"); err != nil {
				return err
			}
			// What sudo said before its prompt is not kept.
			seen.Reset()
			seen.WriteString(s[i+len(sudoPrompt):])
		}
		s := seen.String()
		if strings.Contains(s, sudoReady) {
			return nil
		}
		if strings.Contains(s, sudoNoServer) {
			return ErrSudoNoServer
		}
		if err != nil {
			if msg := sudoMessage(s); msg != "" {
				return fmt.Errorf("%w: %s", ErrSudo, msg)
			}
			if errors.Is(err, io.EOF) {
				return ErrSudo
			}
			return fmt.Errorf("%w: %w", ErrSudo, err)
		}
	}
}

// sudoMessage is the last line sudo wrote, which says why it stopped.
func sudoMessage(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
