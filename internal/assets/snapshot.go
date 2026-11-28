package assets

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// ErrSnapshotChanged means hosts no longer match the preview baseline.
var ErrSnapshotChanged = errors.New("assets: snapshot hosts changed")

// Snapshot is everything the store holds, for a backup. Like the store,
// it holds no secret.
type Snapshot struct {
	Hosts       []Host               `json:"hosts"`
	Credentials []Credential         `json:"credentials"`
	Proxies     []Proxy              `json:"proxies"`
	Forwards    []Forward            `json:"forwards"`
	Groups      []string             `json:"groups,omitempty"`
	GroupOrder  []string             `json:"groupOrder,omitempty"`
	GroupLooks  map[string]GroupLook `json:"groupLooks,omitempty"`
	Snippets    []Snippet            `json:"snippets,omitempty"`
}

// Snapshot returns a copy of every entry.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var order []string
	if len(s.groupOrder) > 0 {
		order = s.groupNames()
	}
	return Snapshot{Hosts: cloneHosts(s.hosts), Credentials: slices.Clone(s.creds), Proxies: slices.Clone(s.proxies), Forwards: slices.Clone(s.forwards), Groups: slices.Clone(s.groups), GroupOrder: order, GroupLooks: maps.Clone(s.groupLooks), Snippets: slices.Clone(s.snippets)}
}

// ValidID reports whether id can name an entry: what NewID makes, and no
// character that would reach into another entry's keychain key.
func ValidID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// upsert replaces the entry of x's ID in xs, or adds x.
func upsert[T any](xs []T, x T, id func(T) string) []T {
	if i := slices.IndexFunc(xs, func(y T) bool { return id(y) == id(x) }); i >= 0 {
		xs[i] = x
		return xs
	}
	return append(xs, x)
}

// freeName is name, or "name (2)", "name (3)"… when another entry has it.
func freeName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s (%d)", name, i); !taken(n) {
			return n
		}
	}
}

// Restore merges a snapshot into the store: each entry replaces the one of
// its ID, or is added, and nothing else is removed. A credential or proxy
// whose name another one here has is renamed "name (2)". Everything is
// checked first: when an entry is invalid, or refers to one that is
// neither here nor in the snapshot, nothing changes.
func (s *Store) Restore(in Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restoreLocked(in)
}

// RestoreIfHostsUnchanged merges only while hosts still equal a prior List result.
// The comparison and persistence share the store lock with other writers.
func (s *Store) RestoreIfHostsUnchanged(in Snapshot, expected []Host) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !reflect.DeepEqual(sorted(s.hosts), expected) {
		return ErrSnapshotChanged
	}
	return s.restoreLocked(in)
}

func (s *Store) restoreLocked(in Snapshot) error {
	h, c, p, f, g := slices.Clone(s.hosts), slices.Clone(s.creds), slices.Clone(s.proxies), slices.Clone(s.forwards), slices.Clone(s.groups)
	n := slices.Clone(s.snippets)
	order, looks := slices.Clone(s.groupOrder), maps.Clone(s.groupLooks)
	s.rev++
	undo := func() {
		s.hosts, s.creds, s.proxies, s.forwards, s.groups, s.snippets = h, c, p, f, g, n
		s.groupOrder, s.groupLooks = order, looks
	}
	if err := s.merge(in); err != nil {
		undo()
		return err
	}
	if err := s.save(); err != nil {
		undo()
		return err
	}
	return nil
}

func (s *Store) merge(in Snapshot) error {
	badID := &FieldError{"id", "invalid"}
	for _, x := range in.Credentials {
		x.Normalize()
		if !ValidID(x.ID) {
			return badID
		}
		if err := x.Validate(); err != nil {
			return err
		}
		x.Name = freeName(x.Name, func(n string) bool {
			return slices.ContainsFunc(s.creds, func(o Credential) bool { return o.ID != x.ID && strings.EqualFold(o.Name, n) })
		})
		s.creds = upsert(s.creds, x, func(c Credential) string { return c.ID })
	}
	for _, x := range in.Proxies {
		x.Normalize()
		if !ValidID(x.ID) {
			return badID
		}
		if err := x.Validate(); err != nil {
			return err
		}
		x.Name = freeName(x.Name, func(n string) bool {
			return slices.ContainsFunc(s.proxies, func(o Proxy) bool { return o.ID != x.ID && strings.EqualFold(o.Name, n) })
		})
		s.proxies = upsert(s.proxies, x, func(p Proxy) string { return p.ID })
	}
	for _, x := range in.Hosts {
		x.Normalize()
		if !ValidID(x.ID) {
			return badID
		}
		if err := x.Validate(); err != nil {
			return err
		}
		s.hosts = upsert(s.hosts, x, func(h Host) string { return h.ID })
	}
	// References are checked once every host is in, as one may jump
	// through another restored after it.
	for _, x := range in.Hosts {
		if i := s.index(x.ID); i >= 0 {
			if err := s.checkRefs(s.hosts[i]); err != nil {
				return err
			}
		}
	}
	for _, x := range in.Forwards {
		x.Normalize()
		if !ValidID(x.ID) {
			return badID
		}
		if err := x.Validate(); err != nil {
			return err
		}
		i := s.index(x.HostID)
		if i < 0 {
			return &FieldError{"host", "unknown"}
		}
		if s.hosts[i].IsRDP() {
			return &FieldError{"host", "not ssh"}
		}
		s.forwards = upsert(s.forwards, x, func(f Forward) string { return f.ID })
	}
	if err := s.checkRestoredRDPRefs(in.Hosts); err != nil {
		return err
	}
	for _, x := range in.Snippets {
		x.Normalize()
		if !ValidID(x.ID) {
			return badID
		}
		if err := x.Validate(); err != nil {
			return err
		}
		s.snippets = upsert(s.snippets, x, func(n Snippet) string { return n.ID })
	}
	for _, name := range in.Groups {
		name = strings.TrimSpace(name)
		if name != "" && !slices.ContainsFunc(s.groupNames(), func(g string) bool { return strings.EqualFold(g, name) }) {
			s.groups = append(s.groups, name)
		}
	}
	// Looks are restored for the groups there are.
	groups := s.groupNames()
	for name, look := range in.GroupLooks {
		look.normalize()
		if slices.Contains(groups, name) {
			s.setGroupLook(name, look)
		}
	}
	return s.restoreGroupOrder(in.GroupOrder)
}

func (s *Store) checkRestoredRDPRefs(restored []Host) error {
	for _, h := range restored {
		if i := s.index(h.ID); i >= 0 && s.hosts[i].IsRDP() {
			if use := s.users(h.ID); use != nil {
				return use
			}
		}
	}
	for _, h := range s.hosts {
		if h.IsRDP() {
			if err := s.checkRefs(h); err != nil {
				return err
			}
		}
	}
	return nil
}
