package app

import (
	"bytes"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/locales"
)

// TestHostEditorProtocols: the editor switches a new host to Telnet, with
// its port, logins and warning, then to serial, with the ports found and
// the line settings, and saves each.
func TestHostEditorProtocols(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	defer func(old func() ([]string, error)) { serialPorts = old }(serialPorts)
	serialPorts = func() ([]string, error) { return []string{"/dev/ttyUSB0", "/dev/ttyUSB1"}, nil }

	a.openEditor(nil)
	tt.Frame()
	if !tt.HasText(m.FieldProtocol()) || !tt.HasText(m.MetricsBar()) || tt.HasText(m.TelnetPlaintext()) {
		t.Fatalf("ssh editor %q", tt.Texts())
	}
	mustClick(t, tt, m.ProtocolTelnet())
	tt.Frame()
	f := a.form
	if f.protocol() != assets.ProtoTelnet || f.port != "23" || f.auth != m.AuthManual() {
		t.Fatalf("telnet form proto %v port %q auth %q", f.protocol(), f.port, f.auth)
	}
	if !tt.HasText(m.TelnetPlaintext()) || !tt.HasText(m.FieldNewLine()) || !tt.HasText(m.LocalEcho()) ||
		tt.HasText(m.MetricsBar()) || tt.HasText(m.CwdFollow()) {
		t.Fatalf("telnet editor %q", tt.Texts())
	}
	f.name, f.address, f.user, f.newLine, f.localEcho, f.backspace = "switch", "sw.lan", "admin", 2, true, 1
	if !a.SaveForm(m) {
		t.Fatalf("telnet not saved: %q", f.err)
	}
	tn, _ := a.store.Get(a.selected)
	if tn.Protocol != assets.ProtoTelnet || tn.Port != 23 || tn.Auth != assets.AuthNone || tn.NewLine != assets.NewLineCRLF || !tn.LocalEcho ||
		tn.Backspace != assets.BackspaceBS {
		t.Fatalf("saved telnet %+v", tn)
	}

	a.openEditor(nil)
	tt.Frame()
	f = a.form
	mustClick(t, tt, m.ProtocolSerial())
	tt.Frame()
	if f.device != "/dev/ttyUSB0" || f.baud != 115200 {
		t.Fatalf("serial form device %q baud %d", f.device, f.baud)
	}
	for _, s := range []string{m.FieldDevice(), m.FieldBaud(), m.FieldParity(), m.FieldStopBits(), m.RefreshPorts()} {
		if !tt.HasText(s) {
			t.Fatalf("no %q in serial editor %q", s, tt.Texts())
		}
	}
	if slices.Contains(tt.Texts(), m.FieldPort()) || slices.Contains(tt.Texts(), m.Network()) {
		t.Fatalf("serial editor has a network address: %q", tt.Texts())
	}
	f.device, f.baud, f.dataBits, f.parity = "/dev/ttyUSB1", 9600, 2, "even"
	if !a.SaveForm(m) {
		t.Fatalf("serial not saved: %q", f.err)
	}
	sr, _ := a.store.Get(a.selected)
	if sr.Protocol != assets.ProtoSerial || sr.Serial == nil || sr.Target() != "/dev/ttyUSB1 · 9600 7E1" || sr.Label() != "/dev/ttyUSB1" {
		t.Fatalf("saved serial %+v %+v", sr, sr.Serial)
	}

	// Back to SSH: the default port and an SSH login come back.
	a.openEditor(&sr)
	tt.Frame()
	mustClick(t, tt, m.ProtocolSSH())
	if a.form.port != "22" || a.form.auth != a.askLabel(m) {
		t.Fatalf("ssh again: port %q auth %q", a.form.port, a.form.auth)
	}
}

// TestStreamHostGates: a Telnet host opens terminals, and an assistant
// that types into them, only; is no jump host or forward host; and its
// pane offers a Break that reaches the server, from the menu and the
// shortcut's action.
func TestStreamHostGates(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var got []byte
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("switch> "))
		buf := make([]byte, 256)
		for {
			n, err := c.Read(buf)
			mu.Lock()
			got = append(got, buf[:n]...)
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	h, err := a.store.Add(assets.Host{Name: "switch", Protocol: assets.ProtoTelnet, Address: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	ssh, _ := a.store.Add(assets.Host{Name: "web", Address: "web", User: "u"})

	if _, err := a.OpenFiles(h.ID, openAt{}, nil); !errors.Is(err, errTerminalOnly) {
		t.Fatalf("files on telnet: %v", err)
	}
	if _, ids := a.hostChoices(); !slices.Equal(ids, []string{ssh.ID}) {
		t.Fatalf("forward hosts %v", ids)
	}
	for _, e := range a.hostEntries(m, h) {
		if (e.Label == m.OpenFiles() || e.Label == m.InstallPublicKey()) && !e.Disabled {
			t.Errorf("%q enabled on a telnet host", e.Label)
		}
		if e.Label == m.OpenAI() && e.Disabled {
			t.Error("no assistant on a telnet host")
		}
	}

	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	cn := a.conns[id]
	waitFor(t, tt, "telnet connected", func() bool { return cn.s.Status().State == session.Connected })
	waitFor(t, tt, "the prompt", func() bool { return bytes.Contains([]byte(cn.s.Term.ScreenText()), []byte("switch>")) })
	if remoteOnly(cn) || !canAI(cn) {
		t.Fatal("a telnet pane opens files, or no assistant")
	}
	if !canBreak(cn) {
		t.Fatal("cannot break")
	}
	a.page = pageWorkspace
	a.runAction(actSendBreak)
	waitFor(t, tt, "IAC BRK", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return bytes.Contains(got, []byte{255, 243})
	})
}

// TestSerialPaneOnce: a serial port opens in one pane, a second open
// shows it, and a port that is missing says so.
func TestSerialPaneOnce(t *testing.T) {
	a, tt := newTestApp(t)
	m := locales.In("en")
	dev := "/dev/sshyard-test-missing"
	h, err := a.store.Add(assets.Host{Protocol: assets.ProtoSerial, Serial: &assets.SerialLine{Device: dev}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.Connect(h.ID, openAt{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.Connect(h.ID, openAt{pane: id, edge: layout.Right})
	if err != nil || again != id {
		t.Fatalf("second open %d, %v; want %d", again, err, id)
	}
	n := 0
	for _, c := range a.conns {
		if c.host.ID == h.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d panes on one port", n)
	}
	cn := a.conns[id]
	waitFor(t, tt, "the port failing", func() bool { return cn.s.Status().State == session.Failed })
	if got := a.errText(m, cn.host, cn.s.Status().Err); got != m.ErrSerialNotFound(dev) {
		t.Fatalf("error text %q", got)
	}
	if canBreak(cn) {
		t.Fatal("a failed port can break")
	}
}

// TestQuickConnectStream: quick connect takes telnet:// and serial://
// destinations to temporary hosts, and explains a bad one.
func TestQuickConnectStream(t *testing.T) {
	a, _ := newTestApp(t)
	m := locales.In("en")
	a.OpenQuickConnect()
	a.quick.target = "serial://COM9?baud=x"
	if a.QuickConnect(m) || a.quick.err != m.QuickStreamInvalid() {
		t.Fatalf("bad destination: %q", a.quick.err)
	}
	a.quick.target = "serial:///dev/sshyard-test-missing?baud=9600&parity=odd"
	if !a.QuickConnect(m) {
		t.Fatalf("serial: %q", a.quick.err)
	}
	var tmp assets.Host
	for _, h := range a.temps {
		tmp = h
	}
	if tmp.Protocol != assets.ProtoSerial || tmp.Auth != assets.AuthNone || tmp.Target() != "/dev/sshyard-test-missing · 9600 8O1" {
		t.Fatalf("temp %+v", tmp)
	}

	a.OpenQuickConnect()
	a.quick.target, a.quick.password = "telnet://admin@127.0.0.1:1", "pw"
	if !a.QuickConnect(m) {
		t.Fatalf("telnet: %q", a.quick.err)
	}
	for _, h := range a.temps {
		if h.Protocol == assets.ProtoTelnet && (h.Auth != assets.AuthAsk || h.User != "admin" || h.Port != 1) {
			t.Fatalf("telnet temp %+v", h)
		}
	}
}
