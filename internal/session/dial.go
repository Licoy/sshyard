// Package session is SSHYard's SSH sessions: it dials with
// golang.org/x/crypto/ssh, authenticates by password, private key or the
// SSH agent,
// checks the host key against known_hosts, opens a shell on a PTY that
// feeds a vt.Terminal, keeps the connection alive and reconnects it.
package session

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Licoy/sshyard/internal/netproxy"
	"golang.org/x/crypto/ssh"
)

// Credentials are what a host authenticates with.
type Credentials struct {
	Password   string
	PrivateKey []byte
	Passphrase []byte
	// Certificate, when set, is an OpenSSH user certificate of
	// PrivateKey (a key-cert.pub line), offered before the bare key.
	Certificate []byte
	// Agent signs with the system SSH agent's keys.
	Agent bool
	// User, when set, logs in instead of Config.User (a credential's
	// own user name).
	User string
	// TryAgent offers the agent's keys when an agent runs; unlike Agent,
	// no agent or an empty one is no error.
	TryAgent bool
	// LocalKeys are this computer's key files, offered with the agent's
	// keys (see offerKeys).
	LocalKeys []LocalKey
	// IdentitiesOnly offers only the keys the config names, as ssh's
	// option of that name; with none named, the files alone.
	IdentitiesOnly bool
	// Prefer is the KeyID offered first: the one that logged in last.
	Prefer string
	// Skip are the KeyIDs never offered.
	Skip []string
	// OnKey hears the KeyID of the TryAgent or local key that logged in.
	OnKey func(id string)
	// PasswordPrompt says the caller asks for a missing or refused
	// password itself and connects again with it, as it does whenever
	// Password is set: a keyboard-interactive round asking only for a
	// password Password does not answer fails the login, rather than being
	// asked through Config.Ask.
	PasswordPrompt bool
}

// ErrPassphraseNeeded reports a private key encrypted with a passphrase
// SSHYard does not have.
var ErrPassphraseNeeded = errors.New("session: the private key needs a passphrase")

// ErrPassphraseWrong reports a passphrase that does not decrypt the key.
var ErrPassphraseWrong = errors.New("session: wrong passphrase for the private key")

// ErrCertificate reports a certificate that cannot be read or is not for
// the private key.
var ErrCertificate = errors.New("session: the certificate does not fit the private key")

// ErrNoCredentials reports a host with nothing to authenticate with.
var ErrNoCredentials = errors.New("session: no password or key")

// ErrNoBanner reports a server that accepted the TCP connection but sent
// nothing, not even its SSH version, before the timeout: sshd hung or
// overloaded, or a firewall that lets the connection through and then
// drops what follows.
var ErrNoBanner = errors.New("session: the server sent no SSH greeting")

// ErrNoShell reports a server that logged the user in but did not start
// the shell within shellWait: it left a request unanswered.
var ErrNoShell = errors.New("session: the server did not start the shell")

// Config is how to connect to a host.
type Config struct {
	Addr string // host:port
	User string
	// Credentials returns the credentials to authenticate with, read
	// from the keychain or asked of the user. It is called for every
	// connection attempt, off the UI goroutine.
	Credentials func(ctx context.Context) (Credentials, error)
	// HostKey checks the server's key; a KnownHosts' Callback.
	HostKey ssh.HostKeyCallback
	// Dial dials the TCP connection; nil is net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds dialing and the SSH handshake; 0 is 15 seconds.
	Timeout time.Duration
	// Pool, when set, shares the connection under Key with the other
	// sessions of the pool on the same Key: they open their own channel
	// (a shell, SFTP) on one authenticated connection.
	Pool *Pool
	Key  string
	// Name names the host in progress reports; ID identifies it in a
	// HopError.
	Name, ID string
	// Jumps are the hosts dialed through first, first hop first.
	Jumps []Hop
	// Proxy, when set, is what the first hop is dialed through.
	Proxy *netproxy.Dialer
	// Progress, when set, hears the name of each hop as it is dialed.
	Progress func(hop string)
	// Ask, when set, asks the user the keyboard-interactive questions of
	// any hop the saved password does not answer (a one-time password,
	// a verification code); nil fails such a login with
	// ErrChallengeUnattended.
	Ask Ask
}

func (c Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 15 * time.Second
}

// authMethods are how cr logs in, answering keyboard-interactive
// questions with kbd, the agent's connection to close once it did, and
// the keys offered with TryAgent and LocalKeys, nil for none.
func authMethods(cr Credentials, kbd ssh.KeyboardInteractiveChallenge) ([]ssh.AuthMethod, io.Closer, *keyOffer, error) {
	var ms []ssh.AuthMethod
	var closer io.Closer
	var offer *keyOffer
	if cr.Agent {
		signers, c, err := agentSigners()
		if err != nil {
			return nil, nil, nil, err
		}
		ms, closer = append(ms, ssh.PublicKeys(signers...)), c
	} else if cr.TryAgent || len(cr.LocalKeys) > 0 {
		var signers []ssh.Signer
		if cr.TryAgent {
			// No agent, or none that answers, is no error here.
			signers, closer, _ = agentSigners()
		}
		if offer = offerKeys(cr, signers); len(offer.signers) > 0 {
			ms = append(ms, ssh.PublicKeys(offer.signers...))
		} else {
			offer = nil
		}
	}
	if len(cr.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(cr.PrivateKey)
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			if len(cr.Passphrase) == 0 {
				closeAgent(closer)
				return nil, nil, nil, ErrPassphraseNeeded
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(cr.PrivateKey, cr.Passphrase)
		}
		if errors.Is(err, x509.IncorrectPasswordError) || (err != nil && len(cr.Passphrase) > 0 && strings.Contains(err.Error(), "decryption password incorrect")) {
			closeAgent(closer)
			return nil, nil, nil, fmt.Errorf("%w: %w", ErrPassphraseWrong, err)
		}
		if err != nil {
			closeAgent(closer)
			return nil, nil, nil, fmt.Errorf("session: private key: %w", err)
		}
		signers := []ssh.Signer{signer}
		if len(cr.Certificate) > 0 {
			pub, _, _, _, err := ssh.ParseAuthorizedKey(cr.Certificate)
			cert, ok := pub.(*ssh.Certificate)
			if err != nil || !ok {
				closeAgent(closer)
				return nil, nil, nil, fmt.Errorf("%w: not a certificate", ErrCertificate)
			}
			cs, err := ssh.NewCertSigner(cert, signer)
			if err != nil {
				closeAgent(closer)
				return nil, nil, nil, fmt.Errorf("%w: %w", ErrCertificate, err)
			}
			signers = []ssh.Signer{cs, signer}
		}
		ms = append(ms, ssh.PublicKeys(signers...))
	}
	if cr.Password != "" {
		ms = append(ms, ssh.Password(cr.Password))
	}
	// Last, as the one that may ask the user: after a key, or along with
	// one, the server may want a one-time password too.
	if len(ms) > 0 && kbd != nil {
		ms = append(ms, ssh.KeyboardInteractive(kbd))
	}
	if len(ms) == 0 {
		closeAgent(closer)
		return nil, nil, nil, ErrNoCredentials
	}
	return ms, closer, offer, nil
}

func closeAgent(c io.Closer) {
	if c != nil {
		c.Close()
	}
}

// Hop is one host dialed through on the way to Config.Addr: a jump
// host, with its own credentials and host key check.
type Hop struct {
	// Name is how progress and errors name the hop.
	Name        string
	Addr        string
	User        string
	Credentials func(ctx context.Context) (Credentials, error)
	HostKey     ssh.HostKeyCallback
	// ID identifies the hop's host to the caller, in a HopError.
	ID string
}

// HopError is a failure at one hop of a jump chain: Index 0 is the first
// jump host, len(Jumps) the target.
type HopError struct {
	Index int
	ID    string
	Name  string
	// Jump is whether the hop is a jump host, not the target.
	Jump bool
	Err  error
}

func (e *HopError) Error() string { return "session: hop " + e.Name + ": " + e.Err.Error() }
func (e *HopError) Unwrap() error { return e.Err }

// Dial connects and authenticates to the host of cfg: through its proxy
// and each of its jump hosts in turn, opening a direct-tcpip channel on
// one to reach the next (no nc on the remote). Every hop checks its host
// key and logs in with its own credentials. Closing the client returned
// closes the hops under it; losing one loses it.
func Dial(ctx context.Context, cfg Config) (*ssh.Client, error) {
	hops := append(slices.Clone(cfg.Jumps), Hop{Name: cfg.Name, Addr: cfg.Addr, User: cfg.User, Credentials: cfg.Credentials, HostKey: cfg.HostKey, ID: cfg.ID})
	var under []*ssh.Client
	closeUnder := func() {
		for i := len(under) - 1; i >= 0; i-- {
			under[i].Close()
		}
	}
	for i, hop := range hops {
		if cfg.Progress != nil {
			cfg.Progress(hop.Name)
		}
		var dial func(ctx context.Context, network, addr string) (net.Conn, error)
		switch {
		case i > 0:
			dial = under[i-1].DialContext
		case cfg.Proxy != nil:
			d := *cfg.Proxy
			d.Forward = cfg.Dial
			dial = d.DialContext
		default:
			dial = cfg.Dial
		}
		c, err := dialHop(ctx, hop, dial, cfg.timeout(), cfg.Ask)
		if err != nil {
			closeUnder()
			if len(cfg.Jumps) > 0 {
				err = &HopError{Index: i, ID: hop.ID, Name: hop.Name, Jump: i < len(cfg.Jumps), Err: err}
			}
			return nil, err
		}
		under = append(under, c)
	}
	client := under[len(under)-1]
	if len(under) > 1 {
		under = under[:len(under)-1]
		go func() {
			client.Wait()
			closeUnder()
		}()
	}
	return client, nil
}

// dialHop connects and logs in to one hop, asking the user with ask the
// questions the saved password does not answer.
func dialHop(ctx context.Context, hop Hop, dial func(ctx context.Context, network, addr string) (net.Conn, error), timeout time.Duration, ask Ask) (*ssh.Client, error) {
	if hop.HostKey == nil {
		return nil, errors.New("session: no host key callback")
	}
	var cr Credentials
	if hop.Credentials != nil {
		var err error
		if cr, err = hop.Credentials(ctx); err != nil {
			return nil, err
		}
	}
	user := hop.User
	if cr.User != "" {
		user = cr.User
	}
	kbd := &responder{password: cr.Password, declinePassword: cr.Password != "" || cr.PasswordPrompt, ask: ask, hop: hop.Name, user: user}
	methods, closer, offer, err := authMethods(cr, kbd.answer)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		defer closer.Close()
	}
	if offer != nil {
		kbd.stop = offer.stopped
	}
	// authMethods puts keyboard-interactive last.
	var unlisted bool
	next := unlistedKbd(methods[len(methods)-1], cr.Password != "", &unlisted)
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if dial == nil {
		dial = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext
	}
	conn, err := dial(ctx, "tcp", hop.Addr)
	if err != nil {
		var pe *netproxy.Error
		if ctx.Err() != nil && !errors.As(err, &pe) {
			return nil, fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
		}
		return nil, err
	}
	// The handshake has no context; a deadline bounds it, stopped while
	// the user answers the server's questions, and closing the connection
	// on cancel stops it.
	dl, _ := ctx.Deadline()
	clock := startClock(parent, conn, dl, timeout)
	defer clock.stop()
	ctx = clock.ctx
	kbd.ctx, kbd.pause, kbd.resume = ctx, clock.pause, clock.resume
	heard := &heardConn{Conn: conn}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	c, chans, reqs, err := ssh.NewClientConn(heard, hop.Addr, &ssh.ClientConfig{
		User:            user,
		Auth:            methods,
		AuthCallback:    next,
		HostKeyCallback: hop.HostKey,
		Timeout:         timeout,
		ClientVersion:   "SSH-2.0-SSHYard",
	})
	if !stop() || err != nil {
		conn.Close()
		if err == nil {
			err = ctx.Err()
		}
		var ne net.Error
		timedOut := ctx.Err() != nil || errors.As(err, &ne) && ne.Timeout()
		if timedOut && !heard.heard.Load() {
			return nil, fmt.Errorf("%w: %w: %w", ErrNoBanner, context.DeadlineExceeded, err)
		}
		if ctx.Err() != nil && !isHostKeyErr(err) {
			return nil, fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
		}
		if kbd.err != nil && !errors.Is(kbd.err, errPasswordWanted) {
			// The user canceled, no one is there to answer, or a key file
			// needs its passphrase.
			return nil, kbd.err
		}
		if unlisted && strings.Contains(err.Error(), "unexpected message type 51") {
			// The server refused the keyboard-interactive login it did not
			// list at once, asking nothing: the login stopped where the keys
			// did, for a key file's passphrase, or was refused.
			if offer != nil && offer.stopped() != nil {
				return nil, offer.stopped()
			}
			err = fmt.Errorf("session: unable to authenticate: %w", err)
		}
		if offer != nil && cr.Password == "" && (keysRefused(err) || errors.Is(err, errPasswordWanted)) {
			err = &KeysRefusedError{Offered: len(offer.signers), Err: err}
		}
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	if offer != nil && cr.OnKey != nil {
		if id := offer.last(); id != "" {
			cr.OnKey(id)
		}
	}
	client := ssh.NewClient(c, chans, reqs)
	heardBy.Store(client, heard)
	go func() {
		client.Wait()
		heardBy.Delete(client)
	}()
	return client, nil
}

// unlistedKbd picks kbd, the keyboard-interactive login, once the server
// refused the keys and, password aside, lists nothing left to try, though
// it does not list keyboard-interactive: a JumpServer before v3.7 answers
// the query whether it would take a key with partial success, which
// x/crypto reads as a refusal, and lists keyboard-interactive, where it
// asks for the one-time password, only after that. password is whether
// the login has a password to try first; picked is set once kbd was
// picked so. Otherwise x/crypto picks.
func unlistedKbd(kbd ssh.AuthMethod, password bool, picked *bool) ssh.ClientAuthCallback {
	const ki = "keyboard-interactive"
	return func(ctx *ssh.ClientAuthContext) (ssh.AuthMethod, error) {
		switch {
		case !slices.Contains(ctx.TriedMethods, "publickey"),
			slices.Contains(ctx.TriedMethods, ki), slices.Contains(ctx.AllowedMethods, ki), slices.Contains(ctx.PartialSuccessMethods, ki),
			password && slices.Contains(ctx.AllowedMethods, "password") && !slices.Contains(ctx.TriedMethods, "password"):
			return nil, nil
		}
		*picked = true
		return kbd, nil
	}
}

// keysRefused reports a login the server refused every key of, or hung up
// on after too many of them.
func keysRefused(err error) bool {
	s := err.Error()
	return strings.Contains(s, "unable to authenticate") || strings.Contains(s, "Too many authentication failures")
}

// heardConn notes whether the server has sent anything, and how many
// times it has.
type heardConn struct {
	net.Conn
	heard atomic.Bool
	// reads counts the reads that brought something: a count, not a
	// time, as a clock's tick may hold a read before and one after.
	reads atomic.Uint64
}

func (c *heardConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.heard.Store(true)
		c.reads.Add(1)
	}
	return n, err
}

// heardBy are the connections under the clients Dial made, while they
// are open, by client.
var heardBy sync.Map // *ssh.Client → *heardConn

// heardCount is how many times the server of client has sent something
// so far (data, a window adjustment, any reply), 0 when unknown.
func heardCount(client *ssh.Client) uint64 {
	if v, ok := heardBy.Load(client); ok {
		return v.(*heardConn).reads.Load()
	}
	return 0
}

func isHostKeyErr(err error) bool {
	var u *UnknownHostError
	var c *HostKeyChangedError
	return errors.As(err, &u) || errors.As(err, &c)
}

// Kind is a category of connection error, for the message the UI shows.
type Kind int

const (
	KindNone Kind = iota
	KindAuth
	KindNetwork
	KindTimeout
	// KindNoBanner: the server took the connection but never answered.
	KindNoBanner
	// KindNoShell: logged in, but the shell never started (ErrNoShell).
	KindNoShell
	KindHostUnknown
	KindHostChanged
	KindPassphrase
	KindNoCredentials
	// KindCertificate: the certificate does not fit the private key.
	KindCertificate
	// KindAgent: the SSH agent is not running or has no keys.
	KindAgent
	// KindProxyAuth: the proxy refused its user name and password.
	KindProxyAuth
	// KindProxy: the proxy could not be reached, or would not connect.
	KindProxy
	// KindLocalKey: a key file of this computer the server accepts needs
	// its passphrase (LocalKeyError).
	KindLocalKey
	// KindChallenge: the server's login questions, a one-time password,
	// were not answered: canceled, or no one to ask
	// (ErrChallengeCanceled, ErrChallengeUnattended).
	KindChallenge
	KindOther
)

// Classify returns the kind of a connection error.
func Classify(err error) Kind {
	var u *UnknownHostError
	var c *HostKeyChangedError
	var ne net.Error
	switch {
	case err == nil:
		return KindNone
	case errors.As(err, &u):
		return KindHostUnknown
	case errors.As(err, &c):
		return KindHostChanged
	case errors.Is(err, ErrChallengeCanceled), errors.Is(err, ErrChallengeUnattended):
		return KindChallenge
	case errors.Is(err, netproxy.ErrAuth):
		return KindProxyAuth
	case errors.As(err, new(*netproxy.Error)), errors.As(err, new(*netproxy.RefusedError)):
		return KindProxy
	case errors.As(err, new(*LocalKeyError)):
		return KindLocalKey
	case errors.As(err, new(*KeysRefusedError)):
		return KindAuth
	case errors.Is(err, ErrAgentUnavailable), errors.Is(err, ErrAgentEmpty):
		return KindAgent
	case errors.Is(err, ErrPassphraseNeeded), errors.Is(err, ErrPassphraseWrong):
		return KindPassphrase
	case errors.Is(err, ErrNoCredentials):
		return KindNoCredentials
	case errors.Is(err, ErrCertificate):
		return KindCertificate
	case strings.Contains(err.Error(), "unable to authenticate"):
		return KindAuth
	case errors.Is(err, ErrNoBanner):
		return KindNoBanner
	case errors.Is(err, ErrNoShell):
		return KindNoShell
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return KindTimeout
	case errors.As(err, new(*net.OpError)), errors.As(err, new(*net.DNSError)),
		errors.As(err, new(*ssh.OpenChannelError)),
		strings.Contains(err.Error(), "connection reset"), strings.Contains(err.Error(), "EOF"):
		return KindNetwork
	}
	return KindOther
}

// Retryable reports whether reconnecting automatically may help: not when
// the user has to do something first.
func (k Kind) Retryable() bool {
	return k == KindNetwork || k == KindTimeout || k == KindNoBanner || k == KindNoShell || k == KindProxy || k == KindOther
}
