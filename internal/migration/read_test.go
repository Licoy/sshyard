package migration

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationReadLimits(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nested, "hosts.json")
	data := []byte(`{"hosts":[{"hostname":"host.example","port":0}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nested, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	r, err := Read(Netcatty, []string{dir, path}, Options{})
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Group != "nested" || !hasIssue(r.Entries[0].Issues, "invalid-entry", "port") {
		t.Fatal("bounded directory import failed")
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err = Read(Netcatty, []string{dir}, Options{}); err != ErrFormat || len(r.Entries) != 0 {
		t.Fatal("partial import returned after damaged file")
	}
	big := filepath.Join(t.TempDir(), "big.json")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Read(Netcatty, []string{big}, Options{}); err != ErrTooLarge {
		t.Fatal("size bound missed")
	}
	many := t.TempDir()
	for i := 0; i <= MaxFiles; i++ {
		if err := os.WriteFile(filepath.Join(many, strconv.Itoa(i)+".json"), []byte(`{"hosts":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Read(Netcatty, []string{many}, Options{}); err != ErrTooLarge {
		t.Fatal("file count bound missed")
	}
}
func TestMigrationValidation(t *testing.T) {
	for _, address := range []string{"bad\x00host", "bad\nhost", " user@host", "bad\thost", "bad host", "host:invalid"} {
		e := Entry{Address: address}
		validateEntry(&e)
		if !hasIssue(e.Issues, "invalid-entry", "address") {
			t.Fatal("invalid address accepted")
		}
	}
	e := Entry{Address: "fe80::1%en0"}
	validateEntry(&e)
	if len(e.Issues) != 0 || e.Port != 22 {
		t.Fatal("IPv6 zone rejected")
	}
	r := Result{Entries: []Entry{{ID: "a", Address: "a", Jumps: []Hop{{Ref: "b"}}}, {ID: "b", Address: "b", Jumps: []Hop{{Ref: "a"}}}, {ID: "c", Address: "c", Jumps: []Hop{{Ref: "missing"}}}}}
	resolveReferences(&r)
	if !hasIssue(r.Entries[0].Issues, "invalid-entry", "jump") || !hasIssue(r.Entries[2].Issues, "missing-reference", "jump") {
		t.Fatal("reference validation failed")
	}
	if strings.Join(func() []string {
		var s []string
		for _, v := range Sources() {
			s = append(s, string(v))
		}
		return s
	}(), ",") != "Xshell,MobaXterm,PuTTY,WindTerm,Electerm,Tabby,Netcatty,Remmina,mRemoteNG,WinSCP" {
		t.Fatal("source ordering changed")
	}
}

func TestMigrationReadSelections(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "one.json")
	two := filepath.Join(dir, "two.json")
	for _, path := range []string{one, two} {
		if err := os.WriteFile(path, []byte(`{"hosts":[{"id":"same","hostname":"example.test"}]}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Read(Netcatty, []string{one, two}, Options{})
	if err != nil || len(r.Entries) != 2 || !hasIssue(r.Entries[0].Issues, "duplicate-id", "id") || !hasIssue(r.Entries[1].Issues, "duplicate-id", "id") {
		t.Fatal("multi-file duplicates missed")
	}
	if _, err := Read(Netcatty, []string{filepath.Join(dir, "missing.json")}, Options{}); err != ErrRead {
		t.Fatal("read error hidden")
	}
	if _, err := Read(Netcatty, []string{t.TempDir()}, Options{}); err != ErrNoEntries {
		t.Fatal("empty source accepted")
	}
	linked := filepath.Join(dir, "linked.json")
	if err := os.Symlink(one, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(Netcatty, []string{linked}, Options{}); err != ErrRead {
		t.Fatal("selected symlink followed")
	}
	var data bytes.Buffer
	data.WriteString(`{"hosts":[`)
	for i := 0; i <= MaxEntries; i++ {
		if i > 0 {
			data.WriteByte(',')
		}
		data.WriteString(`{"hostname":"example.test"}`)
	}
	data.WriteString(`]}`)
	if err := os.WriteFile(one, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(Netcatty, []string{one}, Options{}); err != ErrTooLarge {
		t.Fatal("entry bound missed")
	}
}

func TestMigrationPuTTYDirectoryNames(t *testing.T) {
	dir := t.TempDir()
	names := []string{"prod.example.com", "my node", "my%20encoded%20node"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("HostName=example.test\nProtocol=ssh\nUserName=alice\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Read(PuTTY, []string{dir}, Options{})
	if err != nil || len(r.Entries) != len(names) {
		t.Fatal("PuTTY session filename filtered out")
	}
	found := map[string]bool{}
	for _, e := range r.Entries {
		found[e.Name] = true
	}
	if !found["prod.example.com"] || !found["my node"] || !found["my encoded node"] {
		t.Fatal("PuTTY session name decoded incorrectly")
	}
}

func TestMigrationHopNormalization(t *testing.T) {
	r := Result{Entries: []Entry{{Address: "target", Jumps: []Hop{{Address: "[2001:db8::1]"}}}}}
	resolveReferences(&r)
	h := r.Entries[0].Jumps[0]
	if h.Port != 22 || h.Address != "2001:db8::1" {
		t.Fatal("direct hop not normalized")
	}
}

func TestMigrationReadIssueLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.sessions")
	var data bytes.Buffer
	data.WriteByte('[')
	for i := 0; i <= MaxIssues; i++ {
		if i > 0 {
			data.WriteByte(',')
		}
		data.WriteString(`{"session.target":"example.test","session.protocol":"TELNET"}`)
	}
	data.WriteByte(']')
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := Read(WindTerm, []string{path}, Options{}); err != ErrTooLarge || len(r.Entries) != 0 || len(r.Issues) != 0 {
		t.Fatal("non-SSH diagnostic count exceeded bounded preview")
	}
}
