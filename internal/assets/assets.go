// Package assets is SSHYard's asset store: the hosts the user saved, with
// their groups and favorites, and the credentials, proxies and port
// forwards they use, kept as JSON in the user's data directory.
// Secrets are never stored here; they live in the system keychain
// (package keychain), under the ID of the credential, proxy or forward
// they belong to.
package assets

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/mattn/go-runewidth"
)

// MetricsMode is a host's metrics bar: follow the global setting, on, or off.
// Off hides the bar while the global setting is on. The global setting off
// hides every host, including one set to on.
type MetricsMode string

const (
	// MetricsFollow uses the global metrics setting.
	MetricsFollow MetricsMode = ""
	// MetricsOn shows the bar while the global setting is on.
	MetricsOn MetricsMode = "on"
	// MetricsOff never collects or shows the bar for this host.
	MetricsOff MetricsMode = "off"
)

// AuthMethod is how a host authenticates.
type AuthMethod string

const (
	// AuthPassword and AuthKey use secrets owned by this host.
	AuthPassword AuthMethod = "password"
	AuthKey      AuthMethod = "key"
	// AuthCredential uses the credential of the host's CredentialID.
	AuthCredential AuthMethod = "credential"
	// AuthAgent signs with the keys of the system's SSH agent.
	AuthAgent AuthMethod = "agent"
	// AuthAsk keeps nothing: the password is asked at each connection
	// and held in memory only.
	AuthAsk AuthMethod = "ask"
	// AuthNone is a Telnet or serial host logged in to by hand, in the
	// terminal: its prompts are not answered.
	AuthNone AuthMethod = "none"
)

// Host is a saved host, an SSH or Telnet server or a serial device:
// where it is and how to reach it. It holds no secret; host-owned and
// reusable secrets live in the encrypted store.
type Host struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Protocol is how the host is reached; empty is SSH.
	Protocol Protocol   `json:"protocol,omitempty"`
	Address  string     `json:"address"`
	Port     int        `json:"port"`
	User     string     `json:"user"`
	Group    string     `json:"group,omitempty"`
	Favorite bool       `json:"favorite,omitempty"`
	Auth     AuthMethod `json:"auth"`
	// Icon is the host's icon: one of the app's own, by its key after
	// IconPrefix ("icon:database"), or a short text the user typed, as
	// an emoji. Empty picks one from the address.
	Icon string `json:"icon,omitempty"`
	// Color is the tint behind the icon, a name of theme.TintNames.
	// Empty picks one from the address.
	Color string `json:"color,omitempty"`
	// CredentialID is the credential of AuthCredential.
	CredentialID  string `json:"credentialId,omitempty"`
	AskPassphrase bool   `json:"askPassphrase,omitempty"`
	// Certificate is the OpenSSH user certificate of an AuthKey host's
	// key, one key-cert.pub line; public, so it is kept here.
	Certificate string `json:"certificate,omitempty"`
	// JumpChain are the hosts connected through, in order, before this
	// one (ProxyJump); each with its own credential and host key check.
	JumpChain []string `json:"jumpChain,omitempty"`
	// ProxyID is the proxy the first hop is dialed through.
	ProxyID string `json:"proxyId,omitempty"`
	// CwdFollow lets a Files pane follow the directory the terminal of
	// the same tab reports (OSC 7). New hosts have it on.
	CwdFollow bool `json:"cwdFollow"`
	// TerminalDir, RemoteDir and LocalDir are where an SSH host's
	// terminal, the server side of its Files panes and their side on
	// this computer start; "~" or "~/…" is under the home folder. Empty
	// is the home folder.
	TerminalDir string `json:"terminalDir,omitempty"`
	RemoteDir   string `json:"remoteDir,omitempty"`
	LocalDir    string `json:"localDir,omitempty"`
	// SFTPSudo runs the SFTP server of an SSH host's Files panes as root
	// through sudo, which the login password, or one typed, answers.
	SFTPSudo bool `json:"sftpSudo,omitempty"`
	// Tmux is the tmux session an SSH host's terminals attach to each
	// time their shell starts, created when there is none; empty is
	// none.
	Tmux string `json:"tmux,omitempty"`
	// OSC52 overrides the global OSC 52 write policy (allow, ask or
	// deny). Empty follows the global setting.
	OSC52 string `json:"osc52,omitempty"`
	// TransfersOff keeps the host's terminals from taking ZMODEM and
	// trzsz transfers, whatever the global setting.
	TransfersOff bool `json:"transfersOff,omitempty"`
	// Metrics is whether this host shows the metrics bar: follow the
	// global setting, always, or never.
	Metrics MetricsMode `json:"metrics,omitempty"`
	// AIPolicy is the host's AI permission override. An empty mode
	// inherits the global default.
	AIPolicy AIPolicy `json:"aiPolicy,omitempty"`
	// Serial is a serial host's port and its settings.
	Serial *SerialLine `json:"serial,omitempty"`
	// NewLine is what Enter sends to a Telnet or serial host.
	NewLine NewLine `json:"newline,omitempty"`
	// LocalEcho shows what is typed to a Telnet or serial host that
	// does not echo it.
	LocalEcho bool `json:"localEcho,omitempty"`
	// Encoding is the output encoding the host's new terminals decode
	// with, a vt.OutputEncoding; empty is UTF-8.
	Encoding string `json:"encoding,omitempty"`
	// Term is the TERM the host's new terminals report; empty is the
	// setting's (settings.TerminalType).
	Term string `json:"term,omitempty"`
	// SessionLog logs each of the host's terminals from when it
	// connects.
	SessionLog bool `json:"sessionLog,omitempty"`
	// Provider is who the server is rented from, as the user names it.
	Provider string `json:"provider,omitempty"`
	// Renewal is the host's subscription; its zero value keeps none.
	Renewal Renewal `json:"renewal,omitzero"`
	// Note is the user's own note on the host, as what it is for.
	Note     string    `json:"note,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
}

// AIPolicy is how an AI pane on this host may act. Paths are remote
// POSIX paths. Mode empty means inherit.
type AIPolicy struct {
	Mode          string   `json:"mode,omitempty"`
	WritableRoots []string `json:"writableRoots,omitempty"`
	Sensitive     []string `json:"sensitive,omitempty"`
}

// Label is the name shown for the host.
func (h Host) Label() string {
	if h.Name != "" {
		return h.Name
	}
	if h.Protocol == ProtoSerial && h.Serial != nil {
		return h.Serial.Device
	}
	return h.Address
}

// Target is user@address, with :port when it is not 22; a Telnet URL,
// or a serial port with its speed and frame.
func (h Host) Target() string {
	if !h.SSH() {
		return h.streamTarget()
	}
	s := h.Address
	if h.User != "" {
		s = h.User + "@" + s
	}
	if h.Port != 0 && h.Port != 22 {
		s += ":" + strconv.Itoa(h.Port)
	}
	return s
}

// Addr is address:port for dialing.
func (h Host) Addr() string {
	port := h.Port
	if port == 0 {
		port = h.Protocol.DefaultPort()
	}
	addr := h.Address
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
		addr = "[" + addr + "]"
	}
	return addr + ":" + strconv.Itoa(port)
}

// FieldError reports an invalid field of a host.
type FieldError struct {
	Field  string // "name", "address", "port", "user", "auth", "credential", "kind", "target", …
	Reason string
}

func (e *FieldError) Error() string { return fmt.Sprintf("assets: %s: %s", e.Field, e.Reason) }

// Normalize trims the host's fields and fills defaults.
func (h *Host) Normalize() {
	h.Name = strings.TrimSpace(h.Name)
	h.Address = strings.TrimSpace(h.Address)
	h.User = strings.TrimSpace(h.User)
	h.Group = CleanGroup(h.Group)
	h.Provider, h.Note = strings.TrimSpace(h.Provider), strings.TrimSpace(h.Note)
	h.Tmux = strings.TrimSpace(h.Tmux)
	if h.Encoding = strings.TrimSpace(h.Encoding); h.Encoding == "UTF-8" {
		h.Encoding = ""
	}
	h.Term = strings.TrimSpace(h.Term)
	h.Renewal.normalize()
	h.Icon = IconText(h.Icon)
	if _, ok := theme.LightTokens().TintNamed(h.Color); !ok {
		h.Color = ""
	}
	h.NewLine = NewLine(strings.TrimSpace(string(h.NewLine)))
	if h.Port == 0 {
		h.Port = h.Protocol.DefaultPort()
	}
	if !h.SSH() {
		h.normalizeStream()
	} else {
		h.Serial, h.NewLine, h.LocalEcho = nil, "", false
		h.TerminalDir, h.RemoteDir, h.LocalDir = cleanDir(h.TerminalDir), cleanDir(h.RemoteDir), cleanDir(h.LocalDir)
	}
	if h.Auth == "" {
		h.Auth = AuthAsk
	}
	if h.Auth != AuthKey {
		h.AskPassphrase, h.Certificate = false, ""
	}
	if h.Auth != AuthCredential {
		h.CredentialID = ""
	}
	h.JumpChain = slices.DeleteFunc(slices.Clone(h.JumpChain), func(id string) bool { return id == "" })
	if len(h.JumpChain) == 0 {
		h.JumpChain = nil
	}
	switch h.OSC52 {
	case "", settings.OSC52Allow, settings.OSC52Ask, settings.OSC52Deny:
	default:
		h.OSC52 = ""
	}
	switch h.Metrics {
	case MetricsFollow, MetricsOn, MetricsOff:
	default:
		h.Metrics = MetricsFollow
	}
	h.AIPolicy.normalize()
}

// IconPrefix starts a Host.Icon naming one of the app's icons.
const IconPrefix = "icon:"

// IconText is what a host keeps of its icon: one of the app's as it is,
// else the text trimmed, at most two emoji or four letters wide, whole
// characters only.
func IconText(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, IconPrefix) {
		return s
	}
	return runewidth.Truncate(s, 4, "")
}

func (p *AIPolicy) normalize() {
	p.Mode = strings.TrimSpace(strings.ToLower(p.Mode))
	switch p.Mode {
	case "", "ask", "review", "full":
	default:
		p.Mode = ""
	}
	p.WritableRoots = cleanRemotePaths(p.WritableRoots)
	p.Sensitive = cleanRemotePaths(p.Sensitive)
}

// cleanDir is a default folder trimmed, "" when it has a line break or
// NUL, which no folder name typed in a field has.
func cleanDir(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\x00\r\n") {
		return ""
	}
	return s
}

// cleanRemotePaths trims remote POSIX paths, drops blanks, NULs and
// duplicates, and keeps the order.
func cleanRemotePaths(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || strings.ContainsRune(s, 0) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Validate checks a normalized host.
func (h Host) Validate() error {
	if h.Term != "" && !session.TermOK(h.Term) {
		return &FieldError{"term", "invalid"}
	}
	if !h.SSH() {
		return h.validateStream()
	}
	switch {
	case h.Address == "":
		return &FieldError{"address", "required"}
	case strings.ContainsAny(h.Address, " \t/@"):
		return &FieldError{"address", "invalid"}
	case h.Port < 1 || h.Port > 65535:
		return &FieldError{"port", "out of range"}
	case h.User == "":
		return &FieldError{"user", "required"}
	case h.Auth != AuthPassword && h.Auth != AuthKey && h.Auth != AuthCredential && h.Auth != AuthAgent && h.Auth != AuthAsk:
		return &FieldError{"auth", "unknown method"}
	case h.Auth == AuthCredential && h.CredentialID == "":
		return &FieldError{"credential", "required"}
	}
	return nil
}

// ErrNotFound reports an ID the store does not have.
var ErrNotFound = errors.New("assets: not found")

// ErrExists is AddCredential's or AddProxy's error for an ID in use.
var ErrExists = errors.New("assets: ID in use")

// ErrJumpLoop reports a jump chain that comes back to a host already on
// it (A through B, B through A), or reaches one host twice (A through B
// and C, with C through B too).
var ErrJumpLoop = errors.New("assets: the jump chain loops")

// InUseError reports an entry other entries still use, so it can not be
// deleted: the hosts (and forwards) using it.
type InUseError struct {
	Hosts    []string // IDs
	Forwards []string // IDs
}

func (e *InUseError) Error() string {
	return fmt.Sprintf("assets: in use by %d hosts and %d forwards", len(e.Hosts), len(e.Forwards))
}

// SchemaVersion is the version of the data file. A file of any other
// (older) version is discarded, not migrated.
const SchemaVersion = 2

// Store keeps hosts, credentials, proxies and port forwards in a JSON
// file. It is safe for concurrent use.
type Store struct {
	path string
	now  func() time.Time

	mu       sync.Mutex
	hosts    []Host
	creds    []Credential
	proxies  []Proxy
	forwards []Forward
	snippets []Snippet
	// groups are the groups made on their own, kept while empty; the
	// groups hosts name are groups too.
	groups     []string
	groupOrder []string
	// groupLooks are the looks of the groups given one.
	groupLooks map[string]GroupLook
	// discarded are the host IDs of an older file Open threw away.
	discarded []string
}

type file struct {
	Version     int                  `json:"version"`
	Hosts       []Host               `json:"hosts"`
	Credentials []Credential         `json:"credentials"`
	Proxies     []Proxy              `json:"proxies"`
	Forwards    []Forward            `json:"forwards"`
	Groups      []string             `json:"groups,omitempty"`
	GroupOrder  []string             `json:"groupOrder,omitempty"`
	GroupLooks  map[string]GroupLook `json:"groupLooks,omitempty"`
	Snippets    []Snippet            `json:"snippets,omitempty"`
}

// Open reads the store at path; a missing file is an empty store. A file
// of an older schema version is discarded and an empty store written in
// its place: there is no migration. An empty path keeps the store in
// memory only.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("assets: %s: %w", path, err)
	}
	switch {
	case f.Version > SchemaVersion:
		return nil, fmt.Errorf("assets: %s has version %d, newer than this app reads", path, f.Version)
	case f.Version < SchemaVersion:
		var old struct {
			Hosts []struct {
				ID string `json:"id"`
			} `json:"hosts"`
		}
		json.Unmarshal(b, &old)
		for _, h := range old.Hosts {
			if h.ID != "" {
				s.discarded = append(s.discarded, h.ID)
			}
		}
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	}
	s.hosts, s.creds, s.proxies, s.forwards, s.groups, s.snippets = f.Hosts, f.Credentials, f.Proxies, f.Forwards, f.Groups, f.Snippets
	s.groupOrder, s.groupLooks = f.GroupOrder, f.GroupLooks
	return s, nil
}

// Discarded are the IDs of the hosts of an older data file Open threw
// away, so that the caller can drop what it kept for them elsewhere.
func (s *Store) Discarded() []string { return slices.Clone(s.discarded) }

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	f := file{Version: SchemaVersion, Hosts: s.hosts, Credentials: s.creds, Proxies: s.proxies, Forwards: s.forwards, Groups: s.groups, GroupOrder: s.groupOrder, GroupLooks: s.groupLooks, Snippets: s.snippets}
	// Empty lists are written as [], not null.
	if f.Hosts == nil {
		f.Hosts = []Host{}
	}
	if f.Credentials == nil {
		f.Credentials = []Credential{}
	}
	if f.Proxies == nil {
		f.Proxies = []Proxy{}
	}
	if f.Forwards == nil {
		f.Forwards = []Forward{}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return settings.WriteFileAtomic(s.path, b, 0o600)
}

// commit saves, or undoes what fn changed when saving fails.
func (s *Store) commit(fn func()) error {
	h, c, p, f, g := slices.Clone(s.hosts), slices.Clone(s.creds), slices.Clone(s.proxies), slices.Clone(s.forwards), slices.Clone(s.groups)
	n := slices.Clone(s.snippets)
	order, looks := slices.Clone(s.groupOrder), maps.Clone(s.groupLooks)
	fn()
	if len(s.groupOrder) > 0 {
		s.groupOrder = s.groupNames()
	}
	if err := s.save(); err != nil {
		s.hosts, s.creds, s.proxies, s.forwards, s.groups, s.snippets = h, c, p, f, g, n
		s.groupOrder, s.groupLooks = order, looks
		return err
	}
	return nil
}

// NewID makes a fresh entry ID.
func NewID() string { return newID() }

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Store) index(id string) int {
	return slices.IndexFunc(s.hosts, func(h Host) bool { return h.ID == id })
}

func sorted(hs []Host) []Host {
	out := slices.Clone(hs)
	slices.SortStableFunc(out, func(a, b Host) int {
		if c := strings.Compare(strings.ToLower(a.Label()), strings.ToLower(b.Label())); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// List returns every host, sorted by label.
func (s *Store) List() []Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sorted(s.hosts)
}

// Len is the number of hosts.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hosts)
}

// Get returns the host of id.
func (s *Store) Get(id string) (Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i >= 0 {
		return s.hosts[i], true
	}
	return Host{}, false
}

// checkRefs checks what a host refers to: its credential, its proxy and
// the hosts of its jump chain, which must exist and not loop back.
func (s *Store) checkRefs(h Host) error {
	if h.Auth == AuthCredential {
		i := s.credIndex(h.CredentialID)
		if i < 0 {
			return &FieldError{"credential", "unknown"}
		}
		// A Telnet or serial host types a password; it has no use for a key.
		if !h.SSH() && s.creds[i].Kind != CredPassword {
			return &FieldError{"credential", "password only"}
		}
	}
	if h.ProxyID != "" && s.proxyIndex(h.ProxyID) < 0 {
		return &FieldError{"proxy", "unknown"}
	}
	for _, j := range h.JumpChain {
		if j == h.ID {
			continue
		}
		i := s.index(j)
		if i < 0 {
			return &FieldError{"jump", "unknown"}
		}
		if !s.hosts[i].SSH() {
			return &FieldError{"jump", "not ssh"}
		}
	}
	// Walk every chain from h, with h as it is to be saved.
	get := func(id string) (Host, bool) {
		if id == h.ID {
			return h, true
		}
		if i := s.index(id); i >= 0 {
			return s.hosts[i], true
		}
		return Host{}, false
	}
	// A host met twice on the expanded path, by a loop or by two routes
	// to it, is refused alike.
	seen := map[string]bool{}
	var walk func(id string) error
	walk = func(id string) error {
		if seen[id] {
			return ErrJumpLoop
		}
		seen[id] = true
		x, ok := get(id)
		if !ok {
			return nil
		}
		for _, j := range x.JumpChain {
			if err := walk(j); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(h.ID)
}

// JumpPath is the hosts dialed through to reach id, first hop first,
// each jump host's own chain expanded before it.
func (s *Store) JumpPath(id string) ([]Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Host
	seen := map[string]bool{}
	var walk func(id string) error
	walk = func(id string) error {
		if seen[id] {
			return ErrJumpLoop
		}
		seen[id] = true
		i := s.index(id)
		if i < 0 {
			return ErrNotFound
		}
		for _, j := range s.hosts[i].JumpChain {
			if err := walk(j); err != nil {
				return err
			}
			if k := s.index(j); k >= 0 {
				out = append(out, s.hosts[k])
			}
		}
		return nil
	}
	if err := walk(id); err != nil {
		return nil, err
	}
	return out, nil
}

// Add validates and adds a host, giving it an ID.
func (s *Store) Add(h Host) (Host, error) {
	h.ID = NewID()
	return s.AddHost(h)
}

// AddHost adds a host with a preallocated ID for its encrypted secrets.
func (s *Store) AddHost(h Host) (Host, error) {
	if !ValidID(h.ID) {
		return Host{}, errors.New("assets: AddHost needs a valid ID")
	}
	h.Normalize()
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index(h.ID) >= 0 {
		return Host{}, ErrExists
	}
	if err := s.checkRefs(h); err != nil {
		return Host{}, err
	}
	now := s.now()
	h.Created, h.Updated = now, now
	if err := s.commit(func() { s.hosts = append(s.hosts, h) }); err != nil {
		return Host{}, err
	}
	return h, nil
}

// Update replaces the host with h's ID.
func (s *Store) Update(h Host) (Host, error) {
	h.Normalize()
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(h.ID)
	if i < 0 {
		return Host{}, ErrNotFound
	}
	if err := s.checkRefs(h); err != nil {
		return Host{}, err
	}
	old := s.hosts[i]
	// What jumps through it, or forwards by it, needs it to be SSH.
	if old.SSH() && !h.SSH() {
		if use := s.users(h.ID); use != nil {
			return Host{}, use
		}
	}
	h.Created, h.Updated = old.Created, s.now()
	if h.LastUsed.IsZero() {
		h.LastUsed = old.LastUsed
	}
	if err := s.commit(func() { s.hosts[i] = h }); err != nil {
		return Host{}, err
	}
	return h, nil
}

// Delete removes the host of id. A host other hosts jump through, or
// port forwards enter by, is in use and not deleted (an *InUseError).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return ErrNotFound
	}
	if use := s.users(id); use != nil {
		return use
	}
	return s.commit(func() { s.hosts = slices.Delete(s.hosts, i, i+1) })
}

// users are the hosts that jump through the host of id and the port
// forwards that enter by it; nil for none.
func (s *Store) users(id string) *InUseError {
	use := &InUseError{}
	for _, h := range s.hosts {
		if slices.Contains(h.JumpChain, id) && h.ID != id {
			use.Hosts = append(use.Hosts, h.ID)
		}
	}
	for _, f := range s.forwards {
		if f.HostID == id {
			use.Forwards = append(use.Forwards, f.ID)
		}
	}
	if len(use.Hosts)+len(use.Forwards) == 0 {
		return nil
	}
	return use
}

func (s *Store) modify(id string, fn func(h *Host)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return ErrNotFound
	}
	return s.commit(func() { fn(&s.hosts[i]) })
}

// SetFavorite marks or unmarks a host as a favorite.
func (s *Store) SetFavorite(id string, fav bool) error {
	return s.modify(id, func(h *Host) { h.Favorite = fav })
}

// Touch records that the host was just connected to.
func (s *Store) Touch(id string) error {
	return s.modify(id, func(h *Host) { h.LastUsed = s.now() })
}

// Favorites returns the favorite hosts, sorted.
func (s *Store) Favorites() []Host {
	var out []Host
	for _, h := range s.List() {
		if h.Favorite {
			out = append(out, h)
		}
	}
	return out
}

// Groups returns the names of the groups in display order: those of the hosts and
// those made with AddGroup, empty or not, and the group of each subgroup.
// Hosts without a group are in none of them.
func (s *Store) Groups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupNames()
}

func (s *Store) groupNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(g string) {
		for ; g != "" && !seen[g]; g = GroupParent(g) {
			seen[g] = true
			out = append(out, g)
		}
	}
	for _, g := range s.groups {
		add(g)
	}
	for _, h := range s.hosts {
		add(h.Group)
	}
	rank := make(map[string]int, len(s.groupOrder))
	for i, g := range s.groupOrder {
		rank[g] = i + 1
	}
	slices.SortFunc(out, func(a, b string) int {
		i, j := rank[a], rank[b]
		if i != j {
			if i == 0 {
				return 1
			}
			if j == 0 {
				return -1
			}
			return i - j
		}
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	return treeOrder(out)
}

// AddGroup makes an empty group, for hosts to be moved into, or with a
// path ("Prod/Web") a subgroup. The names are trimmed; a group of that
// name, in any case, is a "taken" FieldError.
func (s *Store) AddGroup(name string) error { return s.AddGroupLook(name, GroupLook{}) }

// AddGroupLook is AddGroup making a group that looks as look.
func (s *Store) AddGroupLook(name string, look GroupLook) error {
	look.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	name = s.groupPath(name)
	if err := s.checkGroup(name, ""); err != nil {
		return err
	}
	return s.commit(func() {
		s.groups = append(s.groups, name)
		s.setGroupLook(name, look)
	})
}

// groupPath trims a group's path, and spells the groups it is in as they
// already are.
func (s *Store) groupPath(name string) string {
	name = CleanGroup(name)
	parts := strings.Split(name, GroupSep)
	groups := s.groupNames()
	for k := len(parts) - 1; k > 0; k-- {
		prefix := strings.Join(parts[:k], GroupSep)
		for _, g := range groups {
			if strings.EqualFold(g, prefix) {
				return g + GroupSep + strings.Join(parts[k:], GroupSep)
			}
		}
	}
	return name
}

// checkGroup checks a new group name, other than old: a name of the path
// left empty is a "required" FieldError, old moved into itself or one of
// its subgroups an "inside" one.
func (s *Store) checkGroup(name, old string) error {
	if slices.Contains(strings.Split(name, GroupSep), "") {
		return &FieldError{Field: "group", Reason: "required"}
	}
	if old != "" && name != old && InGroupTree(name, old) {
		return &FieldError{Field: "group", Reason: "inside"}
	}
	for _, g := range s.groupNames() {
		if strings.EqualFold(g, name) && g != old {
			return &FieldError{Field: "group", Reason: "taken"}
		}
	}
	return nil
}

// RenameGroup renames a group, and moves its hosts and subgroups with it.
func (s *Store) RenameGroup(old, name string) error { return s.editGroup(old, name, nil) }

// EditGroup is RenameGroup giving the group look as well, in one write.
// The group is kept from then on, should it be left empty.
func (s *Store) EditGroup(old, name string, look GroupLook) error {
	look.normalize()
	return s.editGroup(old, name, &look)
}

// editGroup renames old to name, and with a look gives it that.
func (s *Store) editGroup(old, name string, look *GroupLook) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = s.groupPath(name)
	if !slices.Contains(s.groupNames(), old) {
		return ErrNotFound
	}
	if err := s.checkGroup(name, old); err != nil {
		return err
	}
	order := renamed(s.groupNames(), old, name)
	return s.commit(func() {
		s.renameGroup(old, name)
		s.groupOrder = order
		if look != nil {
			s.setGroupLook(name, *look)
		}
	})
}

// renamed returns groups with old, its subgroups' paths too, renamed to
// name.
func renamed(groups []string, old, name string) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = movedGroup(g, old, name)
	}
	return out
}

// movedGroup is g once group old is renamed to name: name for old, the
// path in name for one of its subgroups, g for any other.
func movedGroup(g, old, name string) string {
	if InGroupTree(g, old) {
		return name + g[len(old):]
	}
	return g
}

// renameGroup renames old to name with its subgroups, hosts and looks,
// keeping it from then on, and the group it left, should that be left
// empty. The order is the caller's.
func (s *Store) renameGroup(old, name string) {
	var groups []string
	for _, g := range s.groups {
		if g != old {
			groups = append(groups, movedGroup(g, old, name))
		}
	}
	groups = append(groups, name)
	if p := GroupParent(old); p != "" && p != GroupParent(name) && !slices.Contains(groups, p) {
		groups = append(groups, p)
	}
	s.groups = groups
	for i := range s.hosts {
		s.hosts[i].Group = movedGroup(s.hosts[i].Group, old, name)
	}
	looks := make(map[string]GroupLook, len(s.groupLooks))
	for g, l := range s.groupLooks {
		looks[movedGroup(g, old, name)] = l
	}
	s.groupLooks = looks
}

// DeleteGroup removes a group and its subgroups. Their hosts stay, in no
// group.
func (s *Store) DeleteGroup(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.groupNames(), name) {
		return ErrNotFound
	}
	gone := func(g string) bool { return InGroupTree(g, name) }
	return s.commit(func() {
		s.groupOrder = slices.DeleteFunc(slices.Clone(s.groupOrder), gone)
		s.groups = slices.DeleteFunc(slices.Clone(s.groups), gone)
		s.groupLooks = maps.Clone(s.groupLooks)
		maps.DeleteFunc(s.groupLooks, func(g string, _ GroupLook) bool { return gone(g) })
		for i := range s.hosts {
			if gone(s.hosts[i].Group) {
				s.hosts[i].Group = ""
			}
		}
	})
}

// SetGroup moves a host into a group, "" for none.
func (s *Store) SetGroup(id, group string) error {
	group = CleanGroup(group)
	return s.modify(id, func(h *Host) { h.Group = group })
}

// InGroup returns the hosts of a group, "" for those without one.
func (s *Store) InGroup(group string) []Host {
	var out []Host
	for _, h := range s.List() {
		if h.Group == group {
			out = append(out, h)
		}
	}
	return out
}

// Search returns the hosts whose name, address, user, group, provider or
// note contain every word of q, ignoring case.
func (s *Store) Search(q string) []Host {
	words := strings.Fields(strings.ToLower(q))
	var out []Host
	for _, h := range s.List() {
		hay := strings.ToLower(strings.Join([]string{h.Name, h.Address, h.User, h.Group, h.Provider, h.Note}, " "))
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, h)
		}
	}
	return out
}
