// Package sshtest is an SSH server in process, for SSHYard's tests and
// its development server (cmd/devsshd): password and public-key auth (one
// key, or an authorized_keys file checked as OpenSSH's StrictModes does),
// a PTY shell (a toy line shell, or a real one through Shell), keepalive
// replies, the sftp subsystem of github.com/pkg/sftp, exec requests, and TCP
// forwarding both ways (direct-tcpip channels, tcpip-forward requests).
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// PTY is the terminal a client asked for.
type PTY struct {
	Term       string
	Cols, Rows int
}

// Shell runs a shell on a channel: what the client types arrives on ch,
// and what is written to ch reaches it. resize receives window changes.
// It returns the exit status.
type Shell func(ch ssh.Channel, pty PTY, resize <-chan PTY) int

// Server is an SSH server on a loopback port.
type Server struct {
	Addr     string
	User     string
	Password string
	// AuthorizedKey, when set, may log in too.
	AuthorizedKey ssh.PublicKey
	// AuthorizedKeysFile, when set, is read at each public-key attempt;
	// any key in it may log in. As with OpenSSH's StrictModes, it is
	// ignored when it or its directory is writable by group or others.
	AuthorizedKeysFile string
	// TrustedCA, when set, signs the user certificates that may log in,
	// as OpenSSH's TrustedUserCAKeys: valid now, naming User.
	TrustedCA ssh.PublicKey
	// DirectTCPIP lets clients open direct-tcpip channels (ssh -L, -D,
	// jump hosts); TCPIPForward lets them ask for remote listeners
	// (ssh -R).
	DirectTCPIP, TCPIPForward bool
	// Exec runs "exec" requests; nil refuses them.
	Exec func(ch ssh.Channel, cmd string) int
	// RefuseEnv refuses "env" requests, as a server whose AcceptEnv does
	// not name the variable.
	RefuseEnv bool
	// IgnoreEnv leaves "env" requests unanswered, as warpgate does;
	// IgnoreShell leaves "shell" requests unanswered, as a server that
	// never starts the shell.
	IgnoreEnv, IgnoreShell bool
	// ExecPTY runs "exec" requests on a channel that asked for a PTY
	// first (ssh -t host cmd); nil leaves them to Exec.
	ExecPTY func(ch ssh.Channel, cmd string, pty PTY, resize <-chan PTY) int
	HostKey ssh.Signer
	// SFTPRoot is the working directory of the sftp subsystem.
	SFTPRoot string
	// NoSFTP refuses the sftp subsystem, as a server without one.
	NoSFTP bool
	// SFTPExit, when set, takes the sftp subsystem and ends it at once
	// with that status, as a server whose sftp-server is missing (127).
	SFTPExit int
	// SFTPHandlers, when set, serves the sftp subsystem instead, with
	// handlers it makes for each session (a fake or hostile server).
	SFTPHandlers func() sftp.Handlers
	// SFTPConn, when set, stands between SFTPHandlers' server and its
	// channel (FakeFS.Conn: a server that stops answering).
	SFTPConn func(io.ReadWriteCloser) io.ReadWriteCloser
	// Shell runs sessions; nil is the toy line shell.
	Shell Shell
	// Mute stops replies to keepalives, as a server that hangs does.
	Mute atomic.Bool

	ln       net.Listener
	mu       sync.Mutex
	kbd      func(m ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error)
	conns    map[net.Conn]bool
	logins   atomic.Int32
	lastPTY  PTY
	wg       sync.WaitGroup
	closed   atomic.Bool
	resizeCh chan PTY
	tunnels  atomic.Int32
	envs     atomic.Int32
}

// SetKeyboardInteractive logs in by keyboard-interactive questions with
// fn, as a server asking a one-time password does.
func (s *Server) SetKeyboardInteractive(fn func(m ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error)) {
	s.mu.Lock()
	s.kbd = fn
	s.mu.Unlock()
}

// Envs counts the "env" requests received so far.
func (s *Server) Envs() int { return int(s.envs.Load()) }

// Tunnels counts the direct-tcpip channels opened so far.
func (s *Server) Tunnels() int { return int(s.tunnels.Load()) }

// NewHostKey returns a fresh ed25519 host key.
func NewHostKey() ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		panic(err)
	}
	return s
}

// Start listens on addr ("127.0.0.1:0" for any port) and serves.
func Start(addr, user, password string) (*Server, error) {
	return StartWithKey(addr, user, password, NewHostKey())
}

// StartWithKey is Start with a given host key.
func StartWithKey(addr, user, password string, hostKey ssh.Signer) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{Addr: ln.Addr().String(), User: user, Password: password, HostKey: hostKey, ln: ln, conns: map[net.Conn]bool{}, resizeCh: make(chan PTY, 16)}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Logins counts successful logins.
func (s *Server) Logins() int { return int(s.logins.Load()) }

// LastPTY is the size of the last PTY, after window changes.
func (s *Server) LastPTY() PTY {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPTY
}

// Resizes receives every window change.
func (s *Server) Resizes() <-chan PTY { return s.resizeCh }

// Drop cuts every connection, as a network failure does.
func (s *Server) Drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}

// Close stops the server.
func (s *Server) Close() {
	s.closed.Store(true)
	s.ln.Close()
	s.Drop()
	s.wg.Wait()
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if m.User() == s.User && s.Password != "" && string(pw) == s.Password {
				return nil, nil
			}
			return nil, fmt.Errorf("wrong password for %q", m.User())
		},
		PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() != s.User {
				return nil, fmt.Errorf("unknown user")
			}
			if _, ok := k.(*ssh.Certificate); ok {
				if s.TrustedCA == nil {
					return nil, fmt.Errorf("no certificate authority")
				}
				ca := string(s.TrustedCA.Marshal())
				checker := &ssh.CertChecker{IsUserAuthority: func(auth ssh.PublicKey) bool { return string(auth.Marshal()) == ca }}
				return checker.Authenticate(m, k)
			}
			if s.AuthorizedKey != nil && string(k.Marshal()) == string(s.AuthorizedKey.Marshal()) {
				return nil, nil
			}
			if s.AuthorizedKeysFile != "" && authorized(s.AuthorizedKeysFile, k) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
		ServerVersion: "SSH-2.0-SSHYardTest",
	}
	s.mu.Lock()
	cfg.KeyboardInteractiveCallback = s.kbd
	s.mu.Unlock()
	cfg.AddHostKey(s.HostKey)
	return cfg
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(c)
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

func (s *Server) serveConn(c net.Conn) {
	defer c.Close()
	sc, chans, reqs, err := ssh.NewServerConn(c, s.config())
	if err != nil {
		return
	}
	defer sc.Close()
	s.logins.Add(1)
	fw := &forwards{ln: map[string]net.Listener{}}
	defer fw.closeAll()
	go func() {
		for r := range reqs {
			switch r.Type {
			case "keepalive@openssh.com":
				if s.Mute.Load() {
					continue // never answered
				}
				if r.WantReply {
					r.Reply(true, nil)
				}
			case "tcpip-forward":
				if !s.TCPIPForward {
					r.Reply(false, nil)
					continue
				}
				port, err := fw.listen(sc, r.Payload)
				if err != nil {
					r.Reply(false, nil)
					continue
				}
				r.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
			case "cancel-tcpip-forward":
				r.Reply(fw.cancel(r.Payload), nil)
			default:
				if r.WantReply {
					r.Reply(false, nil)
				}
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() == "direct-tcpip" {
			if !s.DirectTCPIP {
				nc.Reject(ssh.Prohibited, "forwarding is off")
				continue
			}
			go s.direct(nc)
			continue
		}
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.serveSession(ch, creqs)
	}
}

func parsePTY(payload []byte) PTY {
	var p struct {
		Term          string
		Cols, Rows    uint32
		Width, Height uint32
		Modes         string
	}
	if err := ssh.Unmarshal(payload, &p); err != nil {
		return PTY{}
	}
	return PTY{p.Term, int(p.Cols), int(p.Rows)}
}

func (s *Server) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var pty PTY
	hasPTY := false
	resize := make(chan PTY, 16)
	started := false
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			pty = parsePTY(r.Payload)
			hasPTY = true
			s.mu.Lock()
			s.lastPTY = pty
			s.mu.Unlock()
			r.Reply(true, nil)
		case "window-change":
			if len(r.Payload) >= 8 {
				pty.Cols = int(binary.BigEndian.Uint32(r.Payload))
				pty.Rows = int(binary.BigEndian.Uint32(r.Payload[4:]))
				s.mu.Lock()
				s.lastPTY = pty
				s.mu.Unlock()
				select {
				case resize <- pty:
				default:
				}
				select {
				case s.resizeCh <- pty:
				default:
				}
			}
		case "env":
			s.envs.Add(1)
			if !s.IgnoreEnv {
				r.Reply(!s.RefuseEnv, nil)
			}
		case "shell":
			if s.IgnoreShell {
				continue
			}
			if started {
				r.Reply(false, nil)
				continue
			}
			started = true
			r.Reply(true, nil)
			shell := s.Shell
			if shell == nil {
				shell = LineShell
			}
			go func(pty PTY) {
				code := shell(ch, pty, resize)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
				ch.Close()
			}(pty)
		case "exec":
			var e struct{ Cmd string }
			ssh.Unmarshal(r.Payload, &e)
			run := s.Exec
			if hasPTY && s.ExecPTY != nil {
				p := pty // later window-changes go to resize
				run = func(ch ssh.Channel, cmd string) int { return s.ExecPTY(ch, cmd, p, resize) }
			}
			if run == nil || started {
				r.Reply(false, nil)
				continue
			}
			started = true
			r.Reply(true, nil)
			go func() {
				code := run(ch, e.Cmd)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
				ch.Close()
			}()
		case "subsystem":
			var sub struct{ Name string }
			ssh.Unmarshal(r.Payload, &sub)
			if sub.Name != "sftp" || s.NoSFTP {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			if s.SFTPExit != 0 {
				io.WriteString(ch.Stderr(), "sh: /usr/libexec/sftp-server: not found\n")
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(s.SFTPExit)}))
				ch.Close()
				continue
			}
			if s.SFTPHandlers != nil {
				var rw io.ReadWriteCloser = ch
				if s.SFTPConn != nil {
					rw = s.SFTPConn(ch)
				}
				rs := sftp.NewRequestServer(rw, s.SFTPHandlers())
				go func() {
					rs.Serve()
					rs.Close()
					ch.Close()
				}()
				continue
			}
			opts := []sftp.ServerOption{}
			if s.SFTPRoot != "" {
				opts = append(opts, sftp.WithServerWorkingDirectory(s.SFTPRoot))
			}
			srv, err := sftp.NewServer(ch, opts...)
			if err != nil {
				return
			}
			go func() {
				srv.Serve()
				srv.Close()
				ch.Close()
			}()
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}

// LineShell is a toy shell: it echoes what is typed and answers lines.
// "exit" ends it, "size" prints the PTY size, "color" prints SGR colors,
// and "fail" exits with status 3.
func LineShell(ch ssh.Channel, pty PTY, resize <-chan PTY) int {
	io.WriteString(ch, "Welcome to the SSHYard test shell\r\n$ ")
	var line []byte
	buf := make([]byte, 256)
	for {
		select {
		case p := <-resize:
			pty = p
		default:
		}
		n, err := ch.Read(buf)
		if err != nil {
			return 0
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				io.WriteString(ch, "\r\n")
				for {
					select {
					case p := <-resize:
						pty = p
						continue
					default:
					}
					break
				}
				switch cmd {
				case "exit":
					io.WriteString(ch, "bye\r\n")
					return 0
				case "fail":
					return 3
				case "size":
					fmt.Fprintf(ch, "%dx%d\r\n", pty.Cols, pty.Rows)
				case "color":
					io.WriteString(ch, "\x1b[31mred\x1b[0m \x1b[38;2;25;132;252mbrand\x1b[0m\r\n")
				case "":
				default:
					fmt.Fprintf(ch, "you said: %s\r\n", cmd)
				}
				io.WriteString(ch, "$ ")
			case 0x7f, 0x08:
				if len(line) > 0 {
					line = line[:len(line)-1]
					io.WriteString(ch, "\b \b")
				}
			case 0x03:
				line = line[:0]
				io.WriteString(ch, "^C\r\n$ ")
			default:
				line = append(line, b)
				ch.Write([]byte{b})
			}
		}
	}
}

// authorized reports whether k is in the authorized_keys file at path,
// which (with its directory) must not be writable by group or others;
// Windows has no such modes to check.
func authorized(path string, k ssh.PublicKey) bool {
	for _, p := range []string{path, filepath.Dir(path)} {
		fi, err := os.Stat(p)
		if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o022 != 0) {
			return false
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	want := string(k.Marshal())
	for len(b) > 0 {
		pk, _, _, rest, err := ssh.ParseAuthorizedKey(b)
		if err != nil {
			return false
		}
		if string(pk.Marshal()) == want {
			return true
		}
		b = rest
	}
	return false
}

// direct serves a direct-tcpip channel: a TCP connection the server
// makes for the client.
func (s *Server) direct(nc ssh.NewChannel) {
	var d struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &d); err != nil {
		nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	c, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		c.Close()
		return
	}
	s.tunnels.Add(1)
	go ssh.DiscardRequests(reqs)
	pipe(ch, c)
}

// pipe copies both ways until either side ends, then closes both.
func pipe(ch ssh.Channel, c net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(ch, c)
		ch.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(c, ch)
		if tc, ok := c.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	ch.Close()
	c.Close()
}

// forwards are a connection's remote listeners (tcpip-forward).
type forwards struct {
	mu sync.Mutex
	ln map[string]net.Listener
}

type fwdReq struct {
	Addr string
	Port uint32
}

func (f *forwards) listen(sc *ssh.ServerConn, payload []byte) (uint32, error) {
	var r fwdReq
	if err := ssh.Unmarshal(payload, &r); err != nil {
		return 0, err
	}
	addr := r.Addr
	if addr == "" || addr == "localhost" {
		addr = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(r.Port))))
	if err != nil {
		return 0, err
	}
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	f.mu.Lock()
	f.ln[r.Addr+":"+strconv.Itoa(int(port))] = ln
	f.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				ra := c.RemoteAddr().(*net.TCPAddr)
				ch, reqs, err := sc.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
					Addr     string
					Port     uint32
					OrigAddr string
					OrigPort uint32
				}{r.Addr, port, ra.IP.String(), uint32(ra.Port)}))
				if err != nil {
					c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				pipe(ch, c)
			}()
		}
	}()
	return port, nil
}

func (f *forwards) cancel(payload []byte) bool {
	var r fwdReq
	if ssh.Unmarshal(payload, &r) != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := r.Addr + ":" + strconv.Itoa(int(r.Port))
	ln := f.ln[k]
	if ln == nil {
		return false
	}
	ln.Close()
	delete(f.ln, k)
	return true
}

func (f *forwards) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.ln {
		l.Close()
	}
}

// ShellExec is an Exec that runs each command with /bin/sh in dir, on
// this computer: a server's shell, for code that runs commands remotely.
func ShellExec(dir string) func(ch ssh.Channel, cmd string) int {
	return func(ch ssh.Channel, cmd string) int {
		c := exec.Command("/bin/sh", "-c", cmd)
		c.Dir = dir
		c.Stdout, c.Stderr = ch, ch.Stderr()
		// Not Stdin: Run would wait for the client to close it, after the
		// command is gone, and a server sends the exit status at once.
		in, err := c.StdinPipe()
		if err != nil {
			return 127
		}
		go func() {
			io.Copy(in, ch)
			in.Close()
		}()
		err = c.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		if err != nil {
			return 127
		}
		return 0
	}
}
