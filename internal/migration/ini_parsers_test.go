package migration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

func TestINIParsers(t *testing.T) {
	cases := []struct {
		name, normal, nonSSH string
		parse                func([]byte, string) (Result, error)
	}{
		{"Xshell", "[CONNECTION]\nProtocol=SSH\nHost=example.org\nPort=2201\n[CONNECTION:AUTHENTICATION]\nUserName=alice\nUserKey=id.key", "[CONNECTION]\nProtocol=TELNET\nHost=example.org", parseXshell},
		{"PuTTY", "HostName=example.org\nProtocol=ssh\nPortNumber=2201\nUserName=alice\nPublicKeyFile=id.key", "HostName=example.org\nProtocol=telnet", parsePuTTY},
		{"WinSCP", "[Sessions\\team%2Fnode]\nHostName=example.org\nUserName=alice\nPortNumber=2201\nFSProtocol=2\nPublicKeyFile=id.key", "[Sessions\\node]\nHostName=example.org\nFSProtocol=5", parseWinSCP},
		{"Remmina", "[remmina]\nprotocol=SSH\nname=node\nserver=example.org:2201\nusername=alice\nssh_privatekey=id.key", "[remmina]\nprotocol=RDP\nserver=example.org", parseRemmina},
		{"MobaXterm", "[Bookmarks]\nSubRep=team\\prod\n#node=#109#0%example.org%2201%alice%%%%%%%%%%%id.key#font#0# #-1", "[Bookmarks]\nnode=#91#4%example.org%3389%alice#font", parseMobaXterm},
		{"mRemoteNG", `<Connections><Node Name="team" Type="Container" Username="alice" Protocol="SSH2" Port="2201"><Node Type="Connection" Name="node" Hostname="example.org" InheritUsername="True" InheritProtocol="True" InheritPort="True"/></Node></Connections>`, `<Connections><Node Name="node" Hostname="example.org" Protocol="RDP"/></Connections>`, parseMRemoteNG},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, encoding := range []string{"UTF8", "UTF8BOM", "UTF16LE", "UTF16BE"} {
				t.Run(encoding, func(t *testing.T) {
					data := []byte(tc.normal)
					switch encoding {
					case "UTF8BOM":
						data = append([]byte{239, 187, 191}, data...)
					case "UTF16LE":
						data, _ = unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes(data)
					case "UTF16BE":
						data, _ = unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder().Bytes(data)
					}
					r, err := tc.parse(data, "node.xsh")
					if err != nil || len(r.Entries) != 1 {
						t.Fatalf("entries=%d error=%v", len(r.Entries), err)
					}
					e := r.Entries[0]
					if e.Address != "example.org" || e.User != "alice" || e.Port != 2201 {
						t.Fatalf("invalid connection %#v", e)
					}
				})
			}
			r, err := tc.parse([]byte(tc.nonSSH), "node")
			if err != nil || len(r.Entries) != 0 || len(r.Issues) != 1 || r.Issues[0].Code != "unsupported-protocol" {
				t.Fatalf("protocol: %#v %v", r, err)
			}
			for _, bad := range []string{"garbage", "[bad", "<Connections>"} {
				if _, err := tc.parse([]byte(bad), "node"); err == nil {
					t.Errorf("accepted malformed format")
				}
			}
			invalid := strings.ReplaceAll(tc.normal, "2201", "99999")
			r, err = tc.parse([]byte(invalid), "node")
			if err != nil || len(r.Entries) != 1 || r.Entries[0].Port != -1 {
				t.Fatalf("invalid port not preserved: %#v %v", r, err)
			}
		})
	}
}
func TestINILegacyAndSecrets(t *testing.T) {
	data, _ := charmap.Windows1252.NewEncoder().Bytes([]byte("[Bookmarks]\nSubRep=€\nnode=#109#0%example.org%22%alice#font"))
	r, err := parseMobaXterm(data, "sessions")
	if err != nil || r.Entries[0].Group != "€" {
		t.Fatalf("CP1252 failed %v", err)
	}
	if _, err := iniText([]byte{255, 254, 0}); err == nil {
		t.Fatal("accepted truncated UTF16")
	}
	secret := string([]byte{'x', 'y', 'z'})
	r, err = parseXshell([]byte("[CONNECTION]\nProtocol=SSH\nHost=example.org\n[CONNECTION:AUTHENTICATION]\nPassword="+secret), "node")
	if err != nil || len(r.Entries[0].Password) != 0 || len(r.Entries[0].Issues) != 1 || r.Entries[0].Issues[0].Code != "encrypted-secret" {
		t.Fatalf("secret handling failed %v", err)
	}
}
func TestPuTTYRegistry(t *testing.T) {
	data := `Windows Registry Editor Version 5.00
[HKEY_CURRENT_USER\Software\SimonTatham\PuTTY\Sessions\my%20node]
"HostName"="example.org"
"UserName"="alice"
"PortNumber"=dword:00000899
"Protocol"="ssh"
"PublicKeyFile"="C:\\keys\\id.ppk"
"ProxyMethod"=dword:00000002
`
	r, err := parsePuTTY([]byte(data), "test.reg")
	if err != nil || len(r.Entries) != 1 {
		t.Fatalf("parse failed %v", err)
	}
	e := r.Entries[0]
	if e.Name != "my node" || e.Port != 2201 || e.KeyPath != `C:\keys\id.ppk` || len(e.Issues) != 1 {
		t.Fatalf("wrong registry fields %#v", e)
	}
	if _, err := parsePuTTY([]byte(strings.Replace(data, "00000899", "oops", 1)), "test.reg"); err == nil {
		t.Fatal("accepted corrupt DWORD")
	}
}
func TestMRemoteCSVAndEncryption(t *testing.T) {
	data := "Name;Id;Parent;NodeType;Hostname;Protocol;Port;Username;InheritUsername;InheritProtocol;InheritPort\nnode;n;f;Connection;example.org;;;ignored;True;True;True\nteam;f;root;Container;;SSH2;2201;alice;False;False;False"
	r, err := parseMRemoteNG([]byte(data), "sessions.csv")
	if err != nil || len(r.Entries) != 1 {
		t.Fatalf("csv parse %v", err)
	}
	e := r.Entries[0]
	if e.Group != "team" || e.User != "alice" || e.Port != 2201 {
		t.Fatalf("inheritance %#v", e)
	}
	if _, err := parseMRemoteNG([]byte(`<Connections FullFileEncryption="True">encrypted</Connections>`), "encrypted.xml"); err != ErrEncrypted {
		t.Fatal("full-file encryption was not explicitly identified")
	}
	for _, data := range []string{`<Connections><Node></Connections>`, `<Connections><Node Protocol="SSH2"/></Connections><invalid/>`} {
		if _, err := parseMRemoteNG([]byte(data), "test.xml"); err == nil {
			t.Fatal("accepted encrypted/malformed XML")
		}
	}
}

func TestINIUnsupportedFields(t *testing.T) {
	secret := string([]byte{'s', 'e', 'c', 'r', 'e', 't'})
	cases := []struct {
		parse func([]byte, string) (Result, error)
		text  string
		field string
	}{
		{parseWinSCP, "[Sessions\\node]\nHostName=example.org\nFSProtocol=1\nPassword=" + secret, "Password"},
		{parsePuTTY, "HostName=example.org\nProtocol=ssh\nProxyHost=proxy.example.org", "ProxyHost"},
		{parseRemmina, "[remmina]\nprotocol=SSH\nserver=example.org\nssh_tunnel_enabled=1", "ssh_tunnel_enabled"},
		{parseMRemoteNG, `<Connections><Node Name="node" Hostname="example.org" Protocol="SSH2" Password="` + secret + `"/></Connections>`, "Password"},
		{parseMobaXterm, "[Bookmarks]\nnode=#109#0%example.org%22%alice%%%%%gateway.example.org#font", "SSHGateway"},
	}
	for i, tc := range cases {
		r, err := tc.parse([]byte(tc.text), "node")
		if err != nil || len(r.Entries) != 1 {
			t.Fatalf("case %d: %v", i, err)
		}
		e := r.Entries[0]
		if len(e.Password) > 0 || len(e.Issues) != 1 || e.Issues[0].Field != tc.field {
			t.Fatalf("case %d: wrong issues %#v", i, e.Issues)
		}
	}
}

func TestPuTTYUnixDefaultSettings(t *testing.T) {
	for _, data := range []string{"HostName=\nProtocol=ssh\nPresent=1\n", "HostName=\nProtocol=telnet\n"} {
		r, err := parsePuTTY([]byte(data), "Default%20Settings")
		if err != nil || len(r.Entries) != 0 || len(r.Issues) != 0 {
			t.Fatalf("default profile was not skipped: %v", err)
		}
	}
	for _, tc := range []struct{ name, data string }{
		{"Default%20Settings", "garbage"}, {"Default%20Settings", "other=value"}, {"Default%20Settings", ""},
		{"Default%20Settings", "[unknown]\nProtocol=ssh\n"}, {"ordinary", "HostName=\nProtocol=ssh\n"},
	} {
		if _, err := parsePuTTY([]byte(tc.data), tc.name); !errors.Is(err, ErrFormat) {
			t.Fatal("accepted malformed or missing-host session")
		}
	}
}

func TestPuTTYUnixSessionsDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"Default%20Settings": "HostName=\nProtocol=ssh\nPresent=1\n",
		"prod.example":       "HostName=prod.example\nProtocol=ssh\nPortNumber=2201\nUserName=alice\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Read(PuTTY, []string{dir}, Options{})
	if err != nil || len(r.Entries) != 1 {
		t.Fatalf("directory import failed: entries=%d error=%v", len(r.Entries), err)
	}
	e := r.Entries[0]
	if e.Name != "prod.example" || e.Address != "prod.example" || e.Port != 2201 || e.User != "alice" {
		t.Fatal("normal dotted session was not imported")
	}
}
