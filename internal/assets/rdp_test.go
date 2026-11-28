package assets

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRDPHostNormalizeAndTarget(t *testing.T) {
	options := RDPOptions{Domain: " CORP ", Security: " nla ", Width: 1920, Height: 1080, Admin: true, NoClipboard: true}
	h := Host{
		Protocol: ProtoRDP, Address: " fd00::1 ", User: " sam ", ProxyID: "proxy", JumpChain: []string{"", "bastion"},
		Auth: AuthPassword, CredentialID: "old", AskPassphrase: true, Certificate: "old", CwdFollow: true,
		TerminalDir: "~", RemoteDir: "/tmp", LocalDir: "/tmp", Tmux: "work", Startup: true, Metrics: MetricsOn,
		AIPolicy: AIPolicy{Mode: "full"}, SFTPSudo: true, FileProtocol: FileSFTP, Serial: &SerialLine{Device: "COM1"},
		NewLine: NewLineCRLF, Backspace: BackspaceBS, LocalEcho: true, Encoding: "GBK", Term: "screen",
		OSC52: "allow", SessionLog: true, TransfersOff: true, RDP: &options,
	}
	h.Normalize()
	want := Host{
		Protocol: ProtoRDP, Address: "fd00::1", Port: 3389, User: "sam", ProxyID: "proxy", JumpChain: []string{"bastion"},
		Auth: AuthPassword, RDP: &RDPOptions{Domain: "CORP", Security: "nla", Width: 1920, Height: 1080, Admin: true, NoClipboard: true},
	}
	if !reflect.DeepEqual(h, want) || options.Domain != " CORP " {
		t.Fatalf("normalized %+v options %+v, input %+v", h, h.RDP, options)
	}
	if !h.IsRDP() || h.SSH() || h.LogsIn() {
		t.Fatalf("RDP capabilities: %+v", h)
	}
	if h.Target() != `rdp://CORP\sam@[fd00::1]` || h.Addr() != "[fd00::1]:3389" {
		t.Fatalf("target %q, addr %q", h.Target(), h.Addr())
	}
	h.Port = 3390
	if h.Target() != `rdp://CORP\sam@[fd00::1]:3390` {
		t.Fatalf("custom-port target %q", h.Target())
	}
	for _, protocol := range []Protocol{ProtoSSH, ProtoTelnet, ProtoSerial} {
		h := Host{Protocol: protocol, RDP: &options}
		h.Normalize()
		if h.RDP != nil {
			t.Fatalf("%q retained RDP options", protocol)
		}
	}
}

func TestRDPHostValidate(t *testing.T) {
	for _, tc := range []struct {
		name, field, reason string
		change              func(*Host)
	}{
		{"defaults", "", "", func(*Host) {}},
		{"password", "", "", func(h *Host) { h.Auth = AuthPassword }},
		{"ask", "", "", func(h *Host) { h.Auth = AuthAsk }},
		{"credential", "", "", func(h *Host) { h.Auth, h.CredentialID = AuthCredential, "c" }},
		{"minimum", "", "", func(h *Host) { h.RDP = &RDPOptions{Security: "nla", Width: 200, Height: 200} }},
		{"maximum", "", "", func(h *Host) { h.RDP = &RDPOptions{Security: "tls", Width: 8192, Height: 8192} }},
		{"missing address", "address", "required", func(h *Host) { h.Address = "" }},
		{"invalid address", "address", "invalid", func(h *Host) { h.Address = "an address" }},
		{"invalid port", "port", "out of range", func(h *Host) { h.Port = 65536 }},
		{"agent", "auth", "unknown method", func(h *Host) { h.Auth = AuthAgent }},
		{"key", "auth", "unknown method", func(h *Host) { h.Auth = AuthKey }},
		{"missing credential", "credential", "required", func(h *Host) { h.Auth = AuthCredential }},
		{"unknown security", "security", "unknown", func(h *Host) { h.RDP = &RDPOptions{Security: "legacy"} }},
		{"missing width", "width", "out of range", func(h *Host) { h.RDP = &RDPOptions{Height: 1080} }},
		{"missing height", "height", "out of range", func(h *Host) { h.RDP = &RDPOptions{Width: 1920} }},
		{"small width", "width", "out of range", func(h *Host) { h.RDP = &RDPOptions{Width: 199, Height: 1080} }},
		{"large height", "height", "out of range", func(h *Host) { h.RDP = &RDPOptions{Width: 1920, Height: 8193} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := Host{Protocol: ProtoRDP, Address: "desk.lan"}
			tc.change(&h)
			h.Normalize()
			err := h.Validate()
			if tc.field == "" && err != nil || tc.field != "" && !fieldIs(err, tc.field, tc.reason) {
				t.Fatalf("Validate() = %v, want %s %s", err, tc.field, tc.reason)
			}
		})
	}
}

func TestRDPHostJSON(t *testing.T) {
	for _, h := range []Host{
		{Address: "ssh.lan", User: "sam"},
		{Protocol: ProtoRDP, Address: "desk.lan", Auth: AuthNone},
		{Protocol: ProtoRDP, Address: "desk.lan", Auth: AuthPassword, RDP: &RDPOptions{Domain: "CORP", Security: "nla", Width: 1920, Height: 1080, Admin: true, NoClipboard: true}},
	} {
		h.Normalize()
		data, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		var got Host
		if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(h, got) {
			t.Fatalf("round trip %s: %+v %v", data, got, err)
		}
		if strings.Contains(string(data), `"rdp":`) != h.IsRDP() {
			t.Fatalf("wrong RDP field: %s", data)
		}
		if h.IsRDP() && h.Auth == AuthNone && !strings.Contains(string(data), `"rdp":{}`) {
			t.Fatalf("default RDP fields serialized: %s", data)
		}
		if h.Auth == AuthPassword && !strings.Contains(string(data), `"noClipboard":true`) {
			t.Fatalf("clipboard option missing: %s", data)
		}
	}
}

func TestParseRDPStreamURL(t *testing.T) {
	for _, tc := range []struct {
		url, addr, user string
		port            int
	}{
		{" rdp://corp.example ", "corp.example", "", 3389},
		{"RDP://sam@corp.example:3391", "corp.example", "sam", 3391},
		{"rdp://sam%40corp.example@[fd00::1]", "fd00::1", "sam@corp.example", 3389},
	} {
		h, ok, err := ParseStreamURL(tc.url)
		if !ok || err != nil || !h.IsRDP() || h.Auth != AuthAsk || h.Address != tc.addr || h.User != tc.user || h.Port != tc.port || h.RDP == nil {
			t.Errorf("%q: %+v %v %v", tc.url, h, ok, err)
		}
	}
	for _, bad := range []string{"rdp://", "rdp://sam:pw@h", "rdp://sam:@h", "rdp://h:0", "rdp://h:65536", "rdp://h:bad", "rdp://h/path", "rdp://h?security=tls", "rdp://h#other"} {
		if _, ok, err := ParseStreamURL(bad); !ok || !errors.Is(err, ErrStreamURL) {
			t.Errorf("%q: %v %v", bad, ok, err)
		}
	}
}

func TestRDPHostStoreReferences(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	jump := addAssetHost(t, s, Host{Address: "jump.lan", User: "sam"})
	proxy, err := s.SaveProxy(Proxy{Name: "proxy", Kind: ProxySOCKS5, Address: "proxy.lan", Port: 1080})
	if err != nil {
		t.Fatal(err)
	}
	pw, err := s.SaveCredential(Credential{Name: "pw", Kind: CredPassword})
	if err != nil {
		t.Fatal(err)
	}
	desk := addAssetHost(t, s, Host{Protocol: ProtoRDP, Address: "desk.lan", Auth: AuthCredential, CredentialID: pw.ID,
		ProxyID: proxy.ID, JumpChain: []string{jump.ID}, RDP: &RDPOptions{Domain: "CORP"}})
	desk.RDP.Domain = "changed"
	snapshot := s.Snapshot()
	for _, h := range snapshot.Hosts {
		if h.IsRDP() {
			h.RDP.Domain = "snapshot modified"
		}
	}
	for _, store := range []*Store{s, mustOpenAssets(t, s.path)} {
		got, ok := store.Get(desk.ID)
		if !ok || got.RDP.Domain != "CORP" || got.ProxyID != proxy.ID {
			t.Fatalf("desktop %+v", got)
		}
		if path, err := store.JumpPath(desk.ID); err != nil || len(path) != 1 || path[0].ID != jump.ID {
			t.Fatalf("jump path %+v %v", path, err)
		}
	}
	if _, err := s.Add(Host{Address: "ssh.lan", User: "sam", JumpChain: []string{desk.ID}}); !fieldIs(err, "jump", "not ssh") {
		t.Fatalf("RDP jump: %v", err)
	}
	if _, err := s.AddForward(Forward{Kind: ForwardDynamic, HostID: desk.ID, ListenPort: 1080}); !fieldIs(err, "host", "not ssh") {
		t.Fatalf("RDP forward: %v", err)
	}
	pw.Kind = CredKey
	if _, err := s.SaveCredential(pw); !fieldIs(err, "kind", "password only") {
		t.Fatalf("RDP credential became a key: %v", err)
	}
	if err := s.Restore(Snapshot{Credentials: []Credential{pw}}); !fieldIs(err, "credential", "password only") {
		t.Fatalf("restore replaced RDP credential with a key: %v", err)
	}
	if got, _ := s.Credential(pw.ID); got.Kind != CredPassword {
		t.Fatal("restore of invalid credential changed the store")
	}
}

func TestRDPHostRestoreRejectsForwardAndJumpUsers(t *testing.T) {
	s := mustOpenAssets(t, "")
	jump := addAssetHost(t, s, Host{Address: "jump.lan", User: "sam"})
	addAssetHost(t, s, Host{Address: "www.lan", User: "sam", JumpChain: []string{jump.ID}})
	forwardHost := addAssetHost(t, s, Host{Address: "forward.lan", User: "sam"})
	if _, err := s.AddForward(Forward{Kind: ForwardDynamic, HostID: forwardHost.ID, ListenPort: 1080}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []Host{jump, forwardHost} {
		h.Protocol = ProtoRDP
		var use *InUseError
		if err := s.Restore(Snapshot{Hosts: []Host{h}}); !errors.As(err, &use) {
			t.Fatalf("restore replaced referenced host with RDP: %v", err)
		}
		if got, _ := s.Get(h.ID); !got.SSH() {
			t.Fatal("restore of invalid host changed the store")
		}
	}
	desk := addAssetHost(t, s, Host{Protocol: ProtoRDP, Address: "desk.lan"})
	f := Forward{ID: NewID(), HostID: desk.ID, Kind: ForwardDynamic, ListenPort: 1081}
	if err := s.Restore(Snapshot{Forwards: []Forward{f}}); !fieldIs(err, "host", "not ssh") {
		t.Fatalf("restore added forward to RDP host: %v", err)
	}
}

func addAssetHost(t *testing.T, s *Store, h Host) Host {
	t.Helper()
	h, err := s.Add(h)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustOpenAssets(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
