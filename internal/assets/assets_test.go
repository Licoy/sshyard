package assets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/settings"
)

func TestCRUDAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	web, err := s.Add(Host{Name: " web ", Address: "10.0.0.5", User: "deploy", Group: "Prod"})
	if err != nil {
		t.Fatal(err)
	}
	if web.ID == "" || web.Name != "web" || web.Port != 22 || web.Auth != AuthAsk || !web.Created.Equal(clock) {
		t.Fatalf("added %+v", web)
	}
	db, _ := s.Add(Host{Name: "db", Address: "db.internal", Port: 2222, User: "root", Group: "prod", Auth: AuthAgent})
	s.Add(Host{Name: "Lab", Address: "192.168.1.9", User: "pi"})

	if got := s.List(); len(got) != 3 || got[0].Name != "db" || got[1].Name != "Lab" || got[2].Name != "web" {
		t.Fatalf("List order %v", got)
	}
	clock = clock.Add(time.Hour)
	web.Address = "10.0.0.6"
	upd, err := s.Update(web)
	if err != nil || upd.Address != "10.0.0.6" || !upd.Updated.Equal(clock) || !upd.Created.Equal(web.Created) {
		t.Fatalf("update %+v %v", upd, err)
	}
	if err := s.SetFavorite(db.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(db.ID); err != nil {
		t.Fatal(err)
	}

	// Reopen: everything persisted, and nothing secret is in the file.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Len() != 3 {
		t.Fatalf("reopened %d hosts", s2.Len())
	}
	got, ok := s2.Get(db.ID)
	if !ok || !got.Favorite || got.Port != 2222 || !got.LastUsed.Equal(clock) {
		t.Fatalf("reopened db %+v", got)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(strings.ToLower(string(b)), "password\":") {
		t.Error("store file has a password field")
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows has no Unix modes
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	if err := s2.Delete(web.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get(web.ID); ok {
		t.Error("deleted host still there")
	}
	if err := s2.Delete(web.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := s2.Update(Host{ID: "nope", Address: "x", User: "y"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
}

func TestHostMetricsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	on, err := s.Add(Host{Name: "on", Address: "10.0.0.1", User: "u", Metrics: MetricsOn})
	if err != nil || on.Metrics != MetricsOn {
		t.Fatalf("%+v %v", on, err)
	}
	off, err := s.Add(Host{Name: "off", Address: "10.0.0.2", User: "u", Metrics: MetricsOff})
	if err != nil || off.Metrics != MetricsOff {
		t.Fatalf("%+v %v", off, err)
	}
	bad, err := s.Add(Host{Name: "bad", Address: "10.0.0.3", User: "u", Metrics: "sideways"})
	if err != nil || bad.Metrics != MetricsFollow {
		t.Fatalf("%+v %v", bad, err)
	}
	follow, err := s.Add(Host{Name: "follow", Address: "10.0.0.4", User: "u"})
	if err != nil || follow.Metrics != MetricsFollow {
		t.Fatalf("%+v %v", follow, err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := s2.Get(on.ID); !ok || g.Metrics != MetricsOn {
		t.Fatalf("on %+v", g)
	}
	if g, ok := s2.Get(off.ID); !ok || g.Metrics != MetricsOff {
		t.Fatalf("off %+v", g)
	}
	if g, ok := s2.Get(bad.ID); !ok || g.Metrics != "" {
		t.Fatalf("bad %+v", g)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"metrics"`) != 2 {
		t.Fatalf("follow should be omitted:\n%s", b)
	}
}

func TestGroupsFavoritesSearch(t *testing.T) {
	s, _ := Open("")
	s.Add(Host{Name: "a", Address: "a.example", User: "u", Group: "Prod", Favorite: true})
	s.Add(Host{Name: "b", Address: "b.example", User: "u", Group: "dev"})
	s.Add(Host{Name: "c", Address: "c.example", User: "admin", Group: "Prod"})
	s.Add(Host{Name: "d", Address: "d.example", User: "u"})
	if g := s.Groups(); len(g) != 2 || g[0] != "dev" || g[1] != "Prod" {
		t.Errorf("groups %v", g)
	}
	if n := len(s.InGroup("Prod")); n != 2 {
		t.Errorf("Prod has %d", n)
	}
	if n := len(s.InGroup("")); n != 1 {
		t.Errorf("ungrouped %d", n)
	}
	if f := s.Favorites(); len(f) != 1 || f[0].Name != "a" {
		t.Errorf("favorites %v", f)
	}
	if r := s.Search("prod ADMIN"); len(r) != 1 || r[0].Name != "c" {
		t.Errorf("search %v", r)
	}
	if r := s.Search(""); len(r) != 4 {
		t.Errorf("empty search %d", len(r))
	}
}

func TestValidation(t *testing.T) {
	s, _ := Open("")
	cases := map[string]Host{
		"address":    {User: "u"},
		"port":       {Address: "h", User: "u", Port: 70000},
		"user":       {Address: "h"},
		"auth":       {Address: "h", User: "u", Auth: "telepathy"},
		"credential": {Address: "h", User: "u", Auth: AuthCredential},
	}
	for field, h := range cases {
		_, err := s.Add(h)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	if _, err := s.Add(Host{Address: "bad host", User: "u"}); err == nil {
		t.Error("address with a space accepted")
	}
	if h, err := s.Add(Host{Address: "h", User: "u", Auth: AuthNone}); err != nil || h.Auth != AuthNone {
		t.Errorf("SSH host without a password: %+v, %v", h, err)
	}
}

func TestTargetAndAddr(t *testing.T) {
	h := Host{Address: "example.com", User: "root", Port: 22}
	if h.Target() != "root@example.com" || h.Addr() != "example.com:22" {
		t.Error(h.Target(), h.Addr())
	}
	h = Host{Address: "::1", User: "me", Port: 2200}
	if h.Target() != "me@::1:2200" || h.Addr() != "[::1]:2200" {
		t.Error(h.Target(), h.Addr())
	}
	if (Host{Address: "x"}).Label() != "x" {
		t.Error("label falls back to the address")
	}
}

func TestCorruptAndFutureFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	os.WriteFile(p, []byte("{"), 0o600)
	if _, err := Open(p); err == nil {
		t.Error("corrupt file opened")
	}
	os.WriteFile(p, []byte(`{"version":9,"hosts":[]}`), 0o600)
	if _, err := Open(p); err == nil {
		t.Error("future version opened")
	}
}

// TestOldVersionDiscarded: a file of an older schema is wiped and an
// empty one of the current version written; no migration.
func TestOldVersionDiscarded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.json")
	os.WriteFile(p, []byte(`{"version":1,"hosts":[{"id":"old1","address":"a","user":"u","auth":"password","saveSecret":true}]}`), 0o600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 || len(s.Discarded()) != 1 || s.Discarded()[0] != "old1" {
		t.Fatalf("%d hosts, discarded %v", s.Len(), s.Discarded())
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"version": 2`) || strings.Contains(string(b), "old1") {
		t.Fatalf("rewritten as %s", b)
	}
	s2, _ := Open(p)
	if len(s2.Discarded()) != 0 {
		t.Error("discarded twice")
	}
}

func TestCredentials(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.json")
	s, _ := Open(p)
	if _, err := s.SaveCredential(Credential{Kind: CredKey}); err == nil {
		t.Error("credential without a name")
	}
	c, err := s.SaveCredential(Credential{Name: "work key", Kind: CredKey, Fingerprint: "SHA256:abc", Encrypted: true, AskPassphrase: true})
	if err != nil || c.ID == "" {
		t.Fatal(c, err)
	}
	if _, err := s.SaveCredential(Credential{Name: "WORK KEY", Kind: CredPassword}); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := s.Add(Host{Address: "h", User: "u", Auth: AuthCredential, CredentialID: "nope"}); err == nil {
		t.Error("host with an unknown credential")
	}
	a, _ := s.Add(Host{Name: "a", Address: "a", User: "u", Auth: AuthCredential, CredentialID: c.ID})
	b, _ := s.Add(Host{Name: "b", Address: "b", User: "u", Auth: AuthCredential, CredentialID: c.ID})
	if us := s.CredentialUsers(c.ID); len(us) != 2 {
		t.Fatalf("users %v", us)
	}
	var inUse *InUseError
	if err := s.DeleteCredential(c.ID); !errors.As(err, &inUse) || len(inUse.Hosts) != 2 {
		t.Fatalf("delete in use: %v", err)
	}
	for _, h := range []Host{a, b} {
		h.Auth = AuthAsk
		s.Update(h)
	}
	if got, _ := s.Get(a.ID); got.CredentialID != "" {
		t.Error("ask kept the credential")
	}
	if err := s.DeleteCredential(c.ID); err != nil {
		t.Fatal(err)
	}
	s.SaveCredential(Credential{Name: "pw", Kind: CredPassword, Fingerprint: "x", Encrypted: true})
	s2, _ := Open(p)
	if cs := s2.Credentials(); len(cs) != 1 || cs[0].Fingerprint != "" || cs[0].Encrypted {
		t.Fatalf("reopened %+v", cs)
	}
}

// TestJumpRepeats: a host met twice on the expanded path is refused like
// a loop: target through [A, B] with B through A would go A, A, B.
func TestJumpRepeats(t *testing.T) {
	s, _ := Open("")
	a, _ := s.Add(Host{Name: "a", Address: "a", User: "u"})
	b, _ := s.Add(Host{Name: "b", Address: "b", User: "u", JumpChain: []string{a.ID}})
	if _, err := s.Add(Host{Name: "t", Address: "t", User: "u", JumpChain: []string{a.ID, b.ID}}); !errors.Is(err, ErrJumpLoop) {
		t.Fatalf("repeat saved: %v", err)
	}
	if _, err := s.Add(Host{Name: "t", Address: "t", User: "u", JumpChain: []string{a.ID, a.ID}}); !errors.Is(err, ErrJumpLoop) {
		t.Fatalf("same hop twice saved: %v", err)
	}
	tg, err := s.Add(Host{Name: "t", Address: "t", User: "u", JumpChain: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Made a repeat afterwards, by editing a hop: refused too.
	a.JumpChain = []string{}
	c, _ := s.Add(Host{Name: "c", Address: "c", User: "u", JumpChain: []string{a.ID}})
	tg.JumpChain = []string{c.ID, b.ID}
	if _, err := s.Update(tg); !errors.Is(err, ErrJumpLoop) {
		t.Fatalf("repeat by edit saved: %v", err)
	}
	if path, err := s.JumpPath(tg.ID); err != nil || len(path) != 2 {
		t.Fatalf("path %v %v", path, err)
	}
}

func TestJumpChainAndProxy(t *testing.T) {
	s, _ := Open("")
	px, err := s.SaveProxy(Proxy{Name: "corp", Kind: ProxySOCKS5, Address: "proxy.corp", User: "me"})
	if err != nil || px.Port != 1080 {
		t.Fatal(px, err)
	}
	c, _ := s.Add(Host{Name: "c", Address: "c", User: "u"})
	b, _ := s.Add(Host{Name: "b", Address: "b", User: "u", JumpChain: []string{c.ID}})
	a, err := s.Add(Host{Name: "a", Address: "a", User: "u", JumpChain: []string{b.ID}, ProxyID: px.ID})
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.JumpPath(a.ID)
	if err != nil || len(path) != 2 || path[0].ID != c.ID || path[1].ID != b.ID {
		t.Fatalf("path %v %v", path, err)
	}
	// c through a: a loop.
	c.JumpChain = []string{a.ID}
	if _, err := s.Update(c); !errors.Is(err, ErrJumpLoop) {
		t.Fatalf("loop saved: %v", err)
	}
	b.JumpChain = []string{b.ID}
	if _, err := s.Update(b); !errors.Is(err, ErrJumpLoop) {
		t.Fatalf("self jump saved: %v", err)
	}
	var inUse *InUseError
	if err := s.DeleteProxy(px.ID); !errors.As(err, &inUse) || inUse.Hosts[0] != a.ID {
		t.Fatalf("delete proxy in use: %v", err)
	}
	if err := s.Delete(b.ID); !errors.As(err, &inUse) {
		t.Fatalf("delete jump host in use: %v", err)
	}
	if _, err := s.SaveProxy(Proxy{Name: "x", Kind: "ftp", Address: "p"}); err == nil {
		t.Error("unknown proxy kind")
	}
}

func TestForwards(t *testing.T) {
	s, _ := Open("")
	h, _ := s.Add(Host{Name: "h", Address: "h", User: "u"})
	if _, err := s.SaveForward(Forward{Kind: ForwardLocal, HostID: h.ID, ListenPort: 18080}); err == nil {
		t.Error("local forward without a target")
	}
	f, err := s.SaveForward(Forward{Kind: ForwardLocal, HostID: h.ID, ListenPort: 18080, TargetAddr: "127.0.0.1", TargetPort: 8000, Protocol: ProtoSOCKS5})
	if err != nil || f.ListenAddr != "127.0.0.1" || f.Protocol != "" || f.Exposed() {
		t.Fatal(f, err)
	}
	d, _ := s.SaveForward(Forward{Kind: ForwardDynamic, HostID: h.ID, ListenAddr: "0.0.0.0", ListenPort: 1081, TargetAddr: "x", TargetPort: 1, User: "me"})
	if d.TargetAddr != "" || !d.Exposed() || d.Label() != "-D 1081" {
		t.Fatalf("%+v", d)
	}
	var inUse *InUseError
	if err := s.Delete(h.ID); !errors.As(err, &inUse) || len(inUse.Forwards) != 2 {
		t.Fatalf("delete host with forwards: %v", err)
	}
	s.DeleteForward(f.ID)
	s.DeleteForward(d.ID)
	if err := s.Delete(h.ID); err != nil {
		t.Fatal(err)
	}
}

// TestAddWithID: AddCredential and AddProxy keep the ID they are given
// and refuse one in use or none.
func TestAddWithID(t *testing.T) {
	s, _ := Open("")
	id := NewID()
	c, err := s.AddCredential(Credential{ID: id, Name: "k", Kind: CredPassword})
	if err != nil || c.ID != id || c.Created.IsZero() {
		t.Fatal(c, err)
	}
	if _, err := s.AddCredential(Credential{ID: id, Name: "k2", Kind: CredPassword}); !errors.Is(err, ErrExists) {
		t.Errorf("reused ID: %v", err)
	}
	if _, err := s.AddCredential(Credential{Name: "k3", Kind: CredPassword}); err == nil {
		t.Error("added without an ID")
	}
	if _, err := s.SaveCredential(Credential{ID: NewID(), Name: "k4", Kind: CredPassword}); !errors.Is(err, ErrNotFound) {
		t.Errorf("save of an unknown ID: %v", err)
	}
	pid := NewID()
	p, err := s.AddProxy(Proxy{ID: pid, Name: "p", Kind: ProxySOCKS5, Address: "h"})
	if err != nil || p.ID != pid || p.Port != 1080 {
		t.Fatal(p, err)
	}
	if _, err := s.AddProxy(Proxy{ID: pid, Name: "p2", Kind: ProxySOCKS5, Address: "h"}); !errors.Is(err, ErrExists) {
		t.Errorf("reused proxy ID: %v", err)
	}
}

func TestHostOSC52(t *testing.T) {
	h := Host{Address: "h", User: "u", OSC52: "bogus"}
	h.Normalize()
	if h.OSC52 != "" {
		t.Fatalf("bogus kept: %q", h.OSC52)
	}
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Add(Host{Name: "n", Address: "h", User: "u", OSC52: settings.OSC52Deny})
	if err != nil || saved.OSC52 != settings.OSC52Deny {
		t.Fatal(err, saved.OSC52)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get(saved.ID)
	if !ok || got.OSC52 != settings.OSC52Deny {
		t.Fatalf("%+v", got)
	}
}

// TestEmptyGroups: a group made on its own is kept, saved and listed
// while empty; renaming moves its hosts, deleting ungroups them.
func TestEmptyGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, _ := Open(path)
	web, _ := s.Add(Host{Name: "web", Address: "10.0.0.5", User: "u", Group: "Prod"})
	if err := s.AddGroup("  Staging "); err != nil {
		t.Fatal(err)
	}
	var fe *FieldError
	if err := s.AddGroup("prod"); !errors.As(err, &fe) || fe.Reason != "taken" {
		t.Fatalf("a group of a host's name in another case: %v", err)
	}
	if err := s.AddGroup(" "); !errors.As(err, &fe) || fe.Reason != "required" {
		t.Fatalf("a blank group: %v", err)
	}
	s, _ = Open(path)
	if g := s.Groups(); len(g) != 2 || g[0] != "Prod" || g[1] != "Staging" {
		t.Fatalf("groups after reopening %v", g)
	}
	if err := s.SetGroup(web.ID, "Staging"); err != nil {
		t.Fatal(err)
	}
	// Prod had only web: an implicit group goes with its last host.
	if g := s.Groups(); len(g) != 1 || g[0] != "Staging" {
		t.Fatalf("groups after moving %v", g)
	}
	if err := s.RenameGroup("Staging", "QA"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Get(web.ID); h.Group != "QA" {
		t.Fatalf("renamed group's host is in %q", h.Group)
	}
	if err := s.RenameGroup("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renaming a missing group: %v", err)
	}
	if err := s.DeleteGroup("QA"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Get(web.ID); h.Group != "" || len(s.Groups()) != 0 {
		t.Fatalf("after deleting: %q %v", h.Group, s.Groups())
	}
}

func TestIconAndColor(t *testing.T) {
	s, _ := Open("")
	h, err := s.Add(Host{Address: "h", User: "u", Icon: " 🚀🔥✨ ", Color: "purple"})
	if err != nil || h.Icon != "🚀🔥" || h.Color != "purple" {
		t.Fatalf("%q %q %v", h.Icon, h.Color, err)
	}
	h.Icon, h.Color = "icon:database", "#ff0000"
	h, _ = s.Update(h)
	if h.Icon != "icon:database" || h.Color != "" {
		t.Fatalf("%q %q", h.Icon, h.Color)
	}
}
