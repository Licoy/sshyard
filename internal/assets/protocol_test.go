package assets

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Licoy/sshyard/internal/serial"
)

func fieldIs(err error, field, reason string) bool {
	var fe *FieldError
	return errors.As(err, &fe) && fe.Field == field && (reason == "" || fe.Reason == reason)
}

// TestStreamHostNormalize: Telnet and serial hosts lose what only SSH
// has, get their own defaults, and say where they are.
func TestStreamHostNormalize(t *testing.T) {
	tn := Host{Protocol: ProtoTelnet, Address: " switch.lan ", User: "admin", JumpChain: []string{"x"}, CwdFollow: true,
		Metrics: MetricsOn, AskPassphrase: true, Serial: &SerialLine{Device: "COM1"}, AIPolicy: AIPolicy{Mode: "full"},
		TerminalDir: "/srv", RemoteDir: "/srv", LocalDir: "/tmp", Tmux: "main", SFTPSudo: true}
	tn.Normalize()
	if tn.Port != 23 || tn.Auth != AuthNone || tn.JumpChain != nil || tn.CwdFollow || tn.Metrics != MetricsFollow ||
		tn.AskPassphrase || tn.Serial != nil || tn.AIPolicy.Mode != "" || tn.TerminalDir != "" || tn.RemoteDir != "" || tn.LocalDir != "" || tn.Tmux != "" || tn.SFTPSudo {
		t.Fatalf("telnet %+v", tn)
	}
	if err := tn.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := tn.Target(); got != "telnet://admin@switch.lan" {
		t.Fatalf("target %q", got)
	}
	if tn.SSH() || tn.LogsIn() {
		t.Fatal("a manual telnet host is SSH or logs in")
	}
	tn.Auth = AuthPassword
	if !tn.LogsIn() {
		t.Fatal("a telnet host with a password does not log in")
	}

	sr := Host{Protocol: ProtoSerial, Address: "x", Port: 22, ProxyID: "p", Serial: &SerialLine{Device: "/dev/ttyUSB0", Baud: 9600}}
	sr.Normalize()
	if sr.Address != "" || sr.Port != 0 || sr.ProxyID != "" || sr.Serial.DataBits != 8 || sr.Serial.Parity != "none" || sr.Serial.StopBits != "1" {
		t.Fatalf("serial %+v %+v", sr, sr.Serial)
	}
	if err := sr.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := sr.Label(); got != "/dev/ttyUSB0" {
		t.Fatalf("label %q", got)
	}
	if got := sr.Target(); got != "/dev/ttyUSB0 · 9600 8N1" {
		t.Fatalf("target %q", got)
	}

	ssh := Host{Address: "h", User: "u", NewLine: NewLineLF, LocalEcho: true, Serial: &SerialLine{Device: "COM1"}}
	ssh.Normalize()
	if ssh.NewLine != "" || ssh.LocalEcho || ssh.Serial != nil || !ssh.SSH() {
		t.Fatalf("ssh %+v", ssh)
	}
}

// TestStreamHostValidate: what a Telnet or serial host needs, and the
// logins it may have.
func TestStreamHostValidate(t *testing.T) {
	cases := []struct {
		h            Host
		field, cause string
	}{
		{Host{Protocol: ProtoTelnet}, "address", "required"},
		{Host{Protocol: ProtoTelnet, Address: "a b"}, "address", "invalid"},
		{Host{Protocol: ProtoTelnet, Address: "h", Port: 70000}, "port", ""},
		{Host{Protocol: ProtoSerial}, "device", "required"},
		{Host{Protocol: ProtoSerial, Serial: &SerialLine{Device: "COM1", DataBits: 9}}, "serial", ""},
		{Host{Protocol: ProtoSerial, Serial: &SerialLine{Device: "COM1", Parity: "weird"}}, "serial", ""},
		{Host{Protocol: ProtoTelnet, Address: "h", Auth: AuthKey}, "auth", ""},
		{Host{Protocol: ProtoTelnet, Address: "h", Auth: AuthAgent}, "auth", ""},
		{Host{Protocol: ProtoTelnet, Address: "h", Auth: AuthCredential}, "credential", "required"},
		{Host{Protocol: ProtoTelnet, Address: "h", NewLine: "nul"}, "newline", ""},
		{Host{Protocol: "rlogin", Address: "h"}, "protocol", ""},
	}
	for _, tc := range cases {
		h := tc.h
		h.Normalize()
		if err := h.Validate(); !fieldIs(err, tc.field, tc.cause) {
			t.Errorf("%+v: %v, want %s %s", tc.h, err, tc.field, tc.cause)
		}
	}
	for _, auth := range []AuthMethod{AuthNone, AuthPassword, AuthAsk} {
		h := Host{Protocol: ProtoSerial, Serial: &SerialLine{Device: "COM1"}, Auth: auth, NewLine: NewLineCRLF}
		h.Normalize()
		if err := h.Validate(); err != nil {
			t.Errorf("%s: %v", auth, err)
		}
	}
}

// TestStreamHostRefs: a Telnet or serial host logs in with password
// credentials only, is no jump host and has no forwards, and an SSH host
// in such use stays SSH.
func TestStreamHostRefs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := s.SaveCredential(Credential{Name: "key", Kind: CredKey})
	pw, _ := s.SaveCredential(Credential{Name: "pw", Kind: CredPassword, User: "ops"})
	if _, err := s.Add(Host{Protocol: ProtoTelnet, Address: "sw", Auth: AuthCredential, CredentialID: key.ID}); !fieldIs(err, "credential", "password only") {
		t.Fatalf("telnet with a key: %v", err)
	}
	tn, err := s.Add(Host{Protocol: ProtoTelnet, Address: "sw", Auth: AuthCredential, CredentialID: pw.ID})
	if err != nil {
		t.Fatal(err)
	}
	pw.Kind = CredKey
	if _, err := s.SaveCredential(pw); !fieldIs(err, "kind", "password only") {
		t.Fatalf("a telnet host's credential became a key: %v", err)
	}
	if _, err := s.Add(Host{Address: "web", User: "u", JumpChain: []string{tn.ID}}); !fieldIs(err, "jump", "not ssh") {
		t.Fatalf("jump through telnet: %v", err)
	}
	if _, err := s.AddForward(Forward{Kind: ForwardDynamic, HostID: tn.ID, ListenPort: 1080}); !fieldIs(err, "host", "not ssh") {
		t.Fatalf("forward on telnet: %v", err)
	}

	bastion, _ := s.Add(Host{Address: "bastion", User: "u"})
	web, err := s.Add(Host{Address: "web", User: "u", JumpChain: []string{bastion.ID}})
	if err != nil {
		t.Fatal(err)
	}
	bastion.Protocol = ProtoTelnet
	var inUse *InUseError
	if _, err := s.Update(bastion); !errors.As(err, &inUse) || len(inUse.Hosts) != 1 || inUse.Hosts[0] != web.ID {
		t.Fatalf("a jump host became telnet: %v", err)
	}
	fw, _ := s.Add(Host{Address: "fw", User: "u"})
	if _, err := s.AddForward(Forward{Kind: ForwardDynamic, HostID: fw.ID, ListenPort: 1081}); err != nil {
		t.Fatal(err)
	}
	fw.Protocol, fw.Serial = ProtoSerial, &SerialLine{Device: "COM2"}
	if _, err := s.Update(fw); !errors.As(err, &inUse) || len(inUse.Forwards) != 1 {
		t.Fatalf("a forward's host became serial: %v", err)
	}
	free, _ := s.Add(Host{Address: "free", User: "u"})
	free.Protocol, free.Serial = ProtoSerial, &SerialLine{Device: "COM2", Baud: 9600}
	got, err := s.Update(free)
	if err != nil || got.Address != "" || got.Serial.Device != "COM2" {
		t.Fatalf("free host became serial: %+v %v", got, err)
	}

	// The protocol and line persist.
	s2, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := s2.Get(free.ID); !ok || h.Protocol != ProtoSerial || h.Serial == nil || h.Serial.Baud != 9600 {
		t.Fatalf("reopened %+v", h)
	}
}

// TestParseStreamURL: telnet:// and serial:// destinations, and the
// rest left to SSH.
func TestParseStreamURL(t *testing.T) {
	h, ok, err := ParseStreamURL(" telnet://admin@10.0.0.1:2323 ")
	if !ok || err != nil || h.Protocol != ProtoTelnet || h.Address != "10.0.0.1" || h.Port != 2323 || h.User != "admin" || h.Auth != AuthNone {
		t.Fatalf("telnet %+v %v %v", h, ok, err)
	}
	h, _, err = ParseStreamURL("TELNET://[fe80::1]")
	if err != nil || h.Address != "fe80::1" || h.Port != 23 || h.Name != "[fe80::1]" {
		t.Fatalf("telnet v6 %+v %v", h, err)
	}
	h, ok, err = ParseStreamURL("serial://COM3?baud=9600&data=7&parity=e&stop=2")
	if !ok || err != nil || h.Protocol != ProtoSerial || h.Name != "COM3" {
		t.Fatalf("serial %+v %v %v", h, ok, err)
	}
	if want := (SerialLine{Device: "COM3", Baud: 9600, DataBits: 7, Parity: string(serial.ParityEven), StopBits: "2"}); *h.Serial != want {
		t.Fatalf("line %+v", *h.Serial)
	}
	h, _, err = ParseStreamURL("serial:///dev/ttyUSB0")
	if err != nil || h.Serial.Device != "/dev/ttyUSB0" || h.Serial.Baud != serial.DefaultBaud {
		t.Fatalf("serial path %+v %v", h, err)
	}
	for _, bad := range []string{"telnet://", "telnet://u:pw@h", "telnet://h:0", "telnet://h/x", "serial://", "serial://COM1?baud=x",
		"serial://COM1?parity=q", "serial://COM1?speed=9600", "serial://COM1?data=9"} {
		if _, ok, err := ParseStreamURL(bad); !ok || !errors.Is(err, ErrStreamURL) {
			t.Errorf("%q: %v %v", bad, ok, err)
		}
	}
	for _, ssh := range []string{"root@host", "ssh -p 2222 host", "host:22", "sftp://host"} {
		if _, ok, _ := ParseStreamURL(ssh); ok {
			t.Errorf("%q taken", ssh)
		}
	}
}

// TestHostDefaultDirsNormalize: an SSH host's default folders are kept
// trimmed, and dropped when they could not be a folder typed in a field.
func TestHostDefaultDirsNormalize(t *testing.T) {
	h := Host{Address: "a", User: "u", TerminalDir: "  ~/www ", RemoteDir: "/srv\n/etc", LocalDir: "C:\\Users\\me", Tmux: " work "}
	h.Normalize()
	if h.TerminalDir != "~/www" || h.RemoteDir != "" || h.LocalDir != "C:\\Users\\me" {
		t.Fatalf("dirs %q %q %q", h.TerminalDir, h.RemoteDir, h.LocalDir)
	}
	if h.Tmux != "work" {
		t.Fatalf("tmux %q", h.Tmux)
	}
}

func TestHostTerm(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Add(Host{Name: "box", Address: "10.0.0.5", User: "root", Term: " linux "})
	if err != nil || h.Term != "linux" {
		t.Fatalf("saved %q, %v", h.Term, err)
	}
	for _, h := range []Host{
		{Name: "ssh", Address: "10.0.0.6", User: "root", Term: "xterm 256color"},
		{Name: "telnet", Protocol: ProtoTelnet, Address: "switch.lan", Term: "vt100\r\nexit"},
	} {
		if _, err := s.Add(h); !fieldIs(err, "term", "invalid") {
			t.Errorf("%s with TERM %q: %v", h.Name, h.Term, err)
		}
	}
}
