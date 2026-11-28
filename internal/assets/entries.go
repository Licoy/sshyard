package assets

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CredentialKind is what a credential holds.
type CredentialKind string

const (
	// CredPassword is a password.
	CredPassword CredentialKind = "password"
	// CredKey is a private key, kept in OpenSSH form (a PuTTY key is
	// converted when imported), maybe with a passphrase.
	CredKey CredentialKind = "key"
)

// Credential is a password or private key that hosts refer to. Its JSON
// holds metadata only; the secret is in the keychain under
// cred/<id>/<kind>.
type Credential struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Kind CredentialKind `json:"kind"`
	// User, when set, logs in instead of the host's user.
	User string `json:"user,omitempty"`
	Note string `json:"note,omitempty"`
	// Fingerprint is the SHA256 fingerprint of a key's public half.
	Fingerprint string `json:"fingerprint,omitempty"`
	// KeyType is the key's algorithm, such as ssh-ed25519.
	KeyType string `json:"keyType,omitempty"`
	// Encrypted records that the key has a passphrase; AskPassphrase
	// that it is not saved but asked once per run.
	Encrypted     bool `json:"encrypted,omitempty"`
	AskPassphrase bool `json:"askPassphrase,omitempty"`
	// Certificate is the OpenSSH user certificate of a key, one
	// key-cert.pub line; public, so it is kept here.
	Certificate string    `json:"certificate,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// ProxyKind is the protocol of a proxy.
type ProxyKind string

const (
	ProxyHTTP   ProxyKind = "http"   // HTTP CONNECT
	ProxySOCKS5 ProxyKind = "socks5" // SOCKS5, names resolved by the proxy
)

// Proxy is an HTTP or SOCKS5 proxy the first hop to a host is dialed
// through. Its password is in the keychain under proxy/<id>/password.
type Proxy struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    ProxyKind `json:"kind"`
	Address string    `json:"address"`
	Port    int       `json:"port"`
	// User, when set, authenticates (Basic for HTTP, username and
	// password for SOCKS5) with the password in the keychain.
	User    string    `json:"user,omitempty"`
	Note    string    `json:"note,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Addr is address:port for dialing.
func (p Proxy) Addr() string { return Host{Address: p.Address, Port: p.Port}.Addr() }

// ForwardKind is the kind of a port forward.
type ForwardKind string

const (
	ForwardLocal   ForwardKind = "local"   // ssh -L
	ForwardRemote  ForwardKind = "remote"  // ssh -R
	ForwardDynamic ForwardKind = "dynamic" // ssh -D
)

// ForwardProtocol is what a dynamic forward's proxy speaks.
type ForwardProtocol string

const (
	ProtoAuto   ForwardProtocol = "" // tell from the first bytes
	ProtoHTTP   ForwardProtocol = "http"
	ProtoSOCKS4 ForwardProtocol = "socks4" // and 4a
	ProtoSOCKS5 ForwardProtocol = "socks5"
)

// Forward is a port forwarding rule through a host. Its listener's
// password (dynamic forwards) is in the keychain under
// forward/<id>/password.
type Forward struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Kind   ForwardKind `json:"kind"`
	HostID string      `json:"hostId"`
	// Listen is where connections come in: on this computer for local
	// and dynamic forwards, on the server for remote ones.
	ListenAddr string `json:"listenAddr"`
	ListenPort int    `json:"listenPort"`
	// Target is where they go: seen from the server for local
	// forwards, from this computer for remote ones; none for dynamic.
	TargetAddr string          `json:"targetAddr,omitempty"`
	TargetPort int             `json:"targetPort,omitempty"`
	Protocol   ForwardProtocol `json:"protocol,omitempty"`
	// User, when set, is the username a dynamic forward's clients
	// authenticate with (HTTP and SOCKS5).
	User      string    `json:"user,omitempty"`
	AutoStart bool      `json:"autoStart,omitempty"`
	Note      string    `json:"note,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// Listen is ListenAddr:ListenPort.
func (f Forward) Listen() string {
	return Host{Address: f.ListenAddr, Port: f.ListenPort}.Addr()
}

// Target is TargetAddr:TargetPort.
func (f Forward) Target() string {
	return Host{Address: f.TargetAddr, Port: f.TargetPort}.Addr()
}

// Label is the name shown for the forward.
func (f Forward) Label() string {
	if f.Name != "" {
		return f.Name
	}
	switch f.Kind {
	case ForwardDynamic:
		return "-D " + strconv.Itoa(f.ListenPort)
	case ForwardRemote:
		return "-R " + strconv.Itoa(f.ListenPort)
	}
	return "-L " + strconv.Itoa(f.ListenPort)
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// Normalize trims the credential's fields.
func (c *Credential) Normalize() {
	c.Name, c.User, c.Note = strings.TrimSpace(c.Name), strings.TrimSpace(c.User), strings.TrimSpace(c.Note)
	if c.Kind != CredKey {
		c.Fingerprint, c.KeyType, c.Encrypted, c.AskPassphrase = "", "", false, false
	}
	if !c.Encrypted {
		c.AskPassphrase = false
	}
}

// Validate checks a normalized credential.
func (c Credential) Validate() error {
	switch {
	case c.Name == "":
		return &FieldError{"name", "required"}
	case c.Kind != CredPassword && c.Kind != CredKey:
		return &FieldError{"kind", "unknown"}
	}
	return nil
}

// Normalize trims the proxy's fields and fills defaults.
func (p *Proxy) Normalize() {
	p.Name, p.Address, p.User, p.Note = strings.TrimSpace(p.Name), strings.TrimSpace(p.Address), strings.TrimSpace(p.User), strings.TrimSpace(p.Note)
	if p.Port == 0 {
		p.Port = map[ProxyKind]int{ProxyHTTP: 8080, ProxySOCKS5: 1080}[p.Kind]
	}
}

// Validate checks a normalized proxy.
func (p Proxy) Validate() error {
	switch {
	case p.Name == "":
		return &FieldError{"name", "required"}
	case p.Kind != ProxyHTTP && p.Kind != ProxySOCKS5:
		return &FieldError{"kind", "unknown"}
	case p.Address == "" || strings.ContainsAny(p.Address, " \t/@"):
		return &FieldError{"address", "required"}
	case !validPort(p.Port):
		return &FieldError{"port", "out of range"}
	}
	return nil
}

// Normalize trims the forward's fields and fills defaults: it listens on
// 127.0.0.1 unless told otherwise.
func (f *Forward) Normalize() {
	f.Name, f.ListenAddr, f.TargetAddr = strings.TrimSpace(f.Name), strings.TrimSpace(f.ListenAddr), strings.TrimSpace(f.TargetAddr)
	f.User, f.Note = strings.TrimSpace(f.User), strings.TrimSpace(f.Note)
	if f.ListenAddr == "" {
		f.ListenAddr = "127.0.0.1"
	}
	if f.Kind == ForwardDynamic {
		f.TargetAddr, f.TargetPort = "", 0
	} else {
		f.Protocol, f.User = "", ""
	}
}

// Validate checks a normalized forward.
func (f Forward) Validate() error {
	switch {
	case f.Kind != ForwardLocal && f.Kind != ForwardRemote && f.Kind != ForwardDynamic:
		return &FieldError{"kind", "unknown"}
	case f.HostID == "":
		return &FieldError{"host", "required"}
	case !validPort(f.ListenPort):
		return &FieldError{"port", "out of range"}
	case f.Kind != ForwardDynamic && (f.TargetAddr == "" || !validPort(f.TargetPort)):
		return &FieldError{"target", "required"}
	case !slices.Contains([]ForwardProtocol{ProtoAuto, ProtoHTTP, ProtoSOCKS4, ProtoSOCKS5}, f.Protocol):
		return &FieldError{"protocol", "unknown"}
	}
	return nil
}

// Exposed reports whether the forward listens beyond the loopback
// address, where others on the network may reach it.
func (f Forward) Exposed() bool {
	a := f.ListenAddr
	return a != "127.0.0.1" && a != "localhost" && a != "::1" && !strings.HasPrefix(a, "127.")
}

func (s *Store) credIndex(id string) int {
	return slices.IndexFunc(s.creds, func(c Credential) bool { return c.ID == id })
}

func (s *Store) proxyIndex(id string) int {
	return slices.IndexFunc(s.proxies, func(p Proxy) bool { return p.ID == id })
}

func (s *Store) forwardIndex(id string) int {
	return slices.IndexFunc(s.forwards, func(f Forward) bool { return f.ID == id })
}

func byName[T any](xs []T, name func(T) string) []T {
	out := slices.Clone(xs)
	slices.SortStableFunc(out, func(a, b T) int { return strings.Compare(strings.ToLower(name(a)), strings.ToLower(name(b))) })
	return out
}

// Credentials returns every credential, sorted by name.
func (s *Store) Credentials() []Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	return byName(s.creds, func(c Credential) string { return c.Name })
}

// Credential returns the credential of id.
func (s *Store) Credential(id string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.credIndex(id); i >= 0 {
		return s.creds[i], true
	}
	return Credential{}, false
}

// SaveCredential adds a credential (no ID) or replaces the one of its ID.
// Names are unique, ignoring case.
func (s *Store) SaveCredential(c Credential) (Credential, error) {
	return s.saveCredential(c, false)
}

// AddCredential adds a credential under the ID it has (from NewID), so
// its secrets can be stored before it is.
func (s *Store) AddCredential(c Credential) (Credential, error) {
	if c.ID == "" {
		return Credential{}, errors.New("assets: AddCredential needs an ID")
	}
	return s.saveCredential(c, true)
}

func (s *Store) saveCredential(c Credential, add bool) (Credential, error) {
	c.Normalize()
	if err := c.Validate(); err != nil {
		return Credential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.creds {
		if o.ID != c.ID && strings.EqualFold(o.Name, c.Name) {
			return Credential{}, &FieldError{"name", "taken"}
		}
	}
	now := s.now()
	i := s.credIndex(c.ID)
	if i >= 0 && c.Kind != CredPassword {
		// Only SSH supports key credentials.
		for _, h := range s.hosts {
			if !h.SSH() && h.Auth == AuthCredential && h.CredentialID == c.ID {
				return Credential{}, &FieldError{"kind", "password only"}
			}
		}
	}
	switch {
	case add && i >= 0:
		return Credential{}, ErrExists
	case add:
		c.Created = now
	case c.ID == "":
		c.ID, c.Created = newID(), now
	case i < 0:
		return Credential{}, ErrNotFound
	default:
		c.Created = s.creds[i].Created
	}
	c.Updated = now
	err := s.commit(func() {
		if i < 0 {
			s.creds = append(s.creds, c)
		} else {
			s.creds[i] = c
		}
	})
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

// CredentialUsers are the hosts that use the credential of id, sorted.
func (s *Store) CredentialUsers(id string) []Host {
	var out []Host
	for _, h := range s.List() {
		if h.Auth == AuthCredential && h.CredentialID == id {
			out = append(out, h)
		}
	}
	return out
}

// DeleteCredential removes a credential no host uses; one in use is an
// *InUseError listing the hosts.
func (s *Store) DeleteCredential(id string) error {
	if us := s.CredentialUsers(id); len(us) > 0 {
		e := &InUseError{}
		for _, h := range us {
			e.Hosts = append(e.Hosts, h.ID)
		}
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.credIndex(id)
	if i < 0 {
		return ErrNotFound
	}
	return s.commit(func() { s.creds = slices.Delete(s.creds, i, i+1) })
}

// Proxies returns every proxy, sorted by name.
func (s *Store) Proxies() []Proxy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return byName(s.proxies, func(p Proxy) string { return p.Name })
}

// Proxy returns the proxy of id.
func (s *Store) Proxy(id string) (Proxy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.proxyIndex(id); i >= 0 {
		return s.proxies[i], true
	}
	return Proxy{}, false
}

// SaveProxy adds a proxy (no ID) or replaces the one of its ID.
func (s *Store) SaveProxy(p Proxy) (Proxy, error) {
	return s.saveProxy(p, false)
}

// AddProxy adds a proxy under the ID it has (from NewID), so its password
// can be stored before it is.
func (s *Store) AddProxy(p Proxy) (Proxy, error) {
	if p.ID == "" {
		return Proxy{}, errors.New("assets: AddProxy needs an ID")
	}
	return s.saveProxy(p, true)
}

func (s *Store) saveProxy(p Proxy, add bool) (Proxy, error) {
	p.Normalize()
	if err := p.Validate(); err != nil {
		return Proxy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.proxies {
		if o.ID != p.ID && strings.EqualFold(o.Name, p.Name) {
			return Proxy{}, &FieldError{"name", "taken"}
		}
	}
	now := s.now()
	i := s.proxyIndex(p.ID)
	switch {
	case add && i >= 0:
		return Proxy{}, ErrExists
	case add:
		p.Created = now
	case p.ID == "":
		p.ID, p.Created = newID(), now
	case i < 0:
		return Proxy{}, ErrNotFound
	default:
		p.Created = s.proxies[i].Created
	}
	p.Updated = now
	err := s.commit(func() {
		if i < 0 {
			s.proxies = append(s.proxies, p)
		} else {
			s.proxies[i] = p
		}
	})
	if err != nil {
		return Proxy{}, err
	}
	return p, nil
}

// ProxyUsers are the hosts that use the proxy of id, sorted.
func (s *Store) ProxyUsers(id string) []Host {
	var out []Host
	for _, h := range s.List() {
		if h.ProxyID == id {
			out = append(out, h)
		}
	}
	return out
}

// DeleteProxy removes a proxy no host uses; one in use is an
// *InUseError listing the hosts.
func (s *Store) DeleteProxy(id string) error {
	if us := s.ProxyUsers(id); len(us) > 0 {
		e := &InUseError{}
		for _, h := range us {
			e.Hosts = append(e.Hosts, h.ID)
		}
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.proxyIndex(id)
	if i < 0 {
		return ErrNotFound
	}
	return s.commit(func() { s.proxies = slices.Delete(s.proxies, i, i+1) })
}

// Forwards returns every port forward, sorted by label.
func (s *Store) Forwards() []Forward {
	s.mu.Lock()
	defer s.mu.Unlock()
	return byName(s.forwards, Forward.Label)
}

// Forward returns the port forward of id.
func (s *Store) Forward(id string) (Forward, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.forwardIndex(id); i >= 0 {
		return s.forwards[i], true
	}
	return Forward{}, false
}

// SaveForward adds a port forward (no ID) or replaces the one of its ID.
func (s *Store) SaveForward(f Forward) (Forward, error) { return s.saveForward(f, false) }

// AddForward adds a port forward under the ID it has, when it has one
// (made with NewID, so that its keychain entry can be written first).
func (s *Store) AddForward(f Forward) (Forward, error) { return s.saveForward(f, true) }

func (s *Store) saveForward(f Forward, add bool) (Forward, error) {
	f.Normalize()
	if err := f.Validate(); err != nil {
		return Forward{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(f.HostID); i < 0 {
		return Forward{}, &FieldError{"host", "unknown"}
	} else if !s.hosts[i].SSH() {
		return Forward{}, &FieldError{"host", "not ssh"}
	}
	now := s.now()
	i := -1
	if f.ID == "" || add {
		if f.ID == "" {
			f.ID = newID()
		} else if s.forwardIndex(f.ID) >= 0 {
			return Forward{}, &FieldError{"id", "taken"}
		}
		f.Created = now
	} else if i = s.forwardIndex(f.ID); i < 0 {
		return Forward{}, ErrNotFound
	} else {
		f.Created = s.forwards[i].Created
	}
	f.Updated = now
	err := s.commit(func() {
		if i < 0 {
			s.forwards = append(s.forwards, f)
		} else {
			s.forwards[i] = f
		}
	})
	if err != nil {
		return Forward{}, err
	}
	return f, nil
}

// DeleteForward removes a port forward.
func (s *Store) DeleteForward(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.forwardIndex(id)
	if i < 0 {
		return ErrNotFound
	}
	return s.commit(func() { s.forwards = slices.Delete(s.forwards, i, i+1) })
}
