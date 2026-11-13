package trzsz

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/xfer"
	"github.com/Licoy/sshyard/internal/xfer/xfertest"
)

func TestFind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		at   int
		mode Mode
		id   string
	}{
		{"tsz", "$ tsz a\r\n\x1b[s::TRZSZ:TRANSFER:S:1.1.5:1700000000000:0\r\n", 54, Download, "1700000000000"},
		{"trz", "\x1b[s::TRZSZ:TRANSFER:R:1.2.0:0123456789000:45678\r\nmore", 49, Upload, "0123456789000"},
		{"trz -d", "::TRZSZ:TRANSFER:D:1.1.0\n", 25, UploadDir, ""},
		{"line not all here", "::TRZSZ:TRANSFER:S:1.1.5:17000000", -1, 0, ""},
		{"text after it", "echo ::TRZSZ:TRANSFER:S:1.1.5:1700000000000 done\r\n", -1, 0, ""},
		{"shown again", "::TRZSZ:TRANSFER:S:1.1.5:1700000000000:0\r\n\x1b[uSaved 1 file\r\n", -1, 0, ""},
		{"tmux -CC", "%output %1 ::TRZSZ:TRANSFER:S:1.1.5:1700000000000:0\r\n", -1, 0, ""},
		{"text", "echo TRZSZ\r\n", -1, 0, ""},
	}
	for _, c := range cases {
		at, o := Find([]byte(c.in))
		if at != c.at {
			t.Errorf("%s: at %d, want %d", c.name, at, c.at)
			continue
		}
		if at >= 0 && (o.Mode != c.mode || o.ID != c.id) {
			t.Errorf("%s: %+v", c.name, o)
		}
	}
	_, o := Find([]byte("::TRZSZ:TRANSFER:R:1.1.5:1700000000010:0\r\n"))
	if o == nil || !o.Windows || o.Tmux {
		t.Errorf("windows: %+v", o)
	}
	_, o = Find([]byte("::TRZSZ:TRANSFER:R:1.1.5:1700000000020:0\r\n"))
	if o == nil || o.Windows || !o.Tmux {
		t.Errorf("tmux: %+v", o)
	}
}

// server is trz or tsz on the other end, as the reference server does
// the protocol's first version.
type server struct {
	t   *testing.T
	end *xfertest.End
	r   *bufio.Reader
	// junk is a server in tmux, whose output the terminal adds to.
	junk bool
	// action is what the client said.
	action map[string]any
}

func newServer(t *testing.T, end *xfertest.End) *server {
	return &server{t: t, end: end, r: bufio.NewReader(readerOf{end})}
}

// readerOf reads an End without deadlines.
type readerOf struct{ e *xfertest.End }

func (r readerOf) Read(p []byte) (int, error) {
	r.e.SetReadDeadline(time.Time{})
	return r.e.Read(p)
}

func (s *server) line(typ string) (string, error) {
	line, err := s.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	if !strings.HasPrefix(line, "#"+typ+":") {
		return "", fmt.Errorf("server: got %q, want %s", line, typ)
	}
	return line[len(typ)+2:], nil
}

func (s *server) str(typ string) (string, error) {
	v, err := s.line(typ)
	if err != nil {
		return "", err
	}
	b, err := decode(v, 1<<30)
	return string(b), err
}

func (s *server) send(typ, payload string) {
	if s.junk {
		// tmux redraws: a line the terminal ends in CR, before the one.
		s.end.Write([]byte("\x1b[1;1H garbage\r\n"))
	}
	s.end.Write([]byte("#" + typ + ":" + payload + "\n"))
}

func (s *server) sendStr(typ, v string) { s.send(typ, encode([]byte(v))) }

func (s *server) checkInt(want int64) error {
	v, err := s.line("SUCC")
	if err != nil {
		return err
	}
	if v != strconv.FormatInt(want, 10) {
		return fmt.Errorf("server: SUCC %s, want %d", v, want)
	}
	return nil
}

// handshake reads the action and sends the config.
func (s *server) handshake(cfg map[string]any) (bool, error) {
	a, err := s.str("ACT")
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(a), &s.action); err != nil {
		return false, err
	}
	if s.action["confirm"] != true {
		s.end.Write([]byte("\x1b[u\x1b[0JCancelled\r\n"))
		return false, nil
	}
	if s.junk {
		cfg["tmux_output_junk"] = true
	}
	b, _ := json.Marshal(cfg)
	s.sendStr("CFG", string(b))
	return true, nil
}

type srvFile struct {
	rel  []string
	dir  bool
	data []byte
	perm uint32
}

// tsz sends files.
func (s *server) tsz(cfg map[string]any, files []srvFile, chunk int, badMD5 bool) error {
	ok, err := s.handshake(cfg)
	if err != nil || !ok {
		return err
	}
	s.send("NUM", strconv.Itoa(len(files)))
	if err := s.checkInt(int64(len(files))); err != nil {
		return err
	}
	for i, f := range files {
		name := f.rel[len(f.rel)-1]
		if cfg["directory"] == true {
			b, _ := json.Marshal(map[string]any{"path_id": i, "path_name": f.rel, "is_dir": f.dir, "archive": false, "size": len(f.data), "perm": f.perm})
			name = string(b)
		}
		s.sendStr("NAME", name)
		if _, err := s.str("SUCC"); err != nil {
			return err
		}
		if f.dir {
			continue
		}
		s.send("SIZE", strconv.Itoa(len(f.data)))
		if err := s.checkInt(int64(len(f.data))); err != nil {
			return err
		}
		for at := 0; at < len(f.data); at += chunk {
			part := f.data[at:min(at+chunk, len(f.data))]
			s.send("DATA", encode(part))
			if err := s.checkInt(int64(len(part))); err != nil {
				return err
			}
		}
		sum := md5.Sum(f.data)
		if badMD5 {
			sum[0] ^= 1
		}
		s.send("MD5", encode(sum[:]))
		if got, err := s.line("SUCC"); err != nil {
			return err
		} else if b, _ := decode(got, 64); !bytes.Equal(b, sum[:]) {
			return errors.New("server: md5 answer")
		}
	}
	msg, err := s.str("EXIT")
	if err != nil {
		return err
	}
	s.end.Write([]byte("\x1b[u\x1b[0J" + msg + "\r\n$ "))
	return nil
}

type got struct {
	name string
	dir  bool
	data []byte
}

// trz takes files.
func (s *server) trz(cfg map[string]any) ([]got, error) {
	ok, err := s.handshake(cfg)
	if err != nil || !ok {
		return nil, err
	}
	v, err := s.line("NUM")
	if err != nil {
		return nil, err
	}
	n, _ := strconv.Atoi(v)
	s.send("SUCC", v)
	var out []got
	for range n {
		name, err := s.str("NAME")
		if err != nil {
			return nil, err
		}
		g := got{name: name}
		if cfg["directory"] == true {
			var sf sourceFile
			if err := json.Unmarshal([]byte(name), &sf); err != nil {
				return nil, err
			}
			g = got{name: strings.Join(sf.RelPath, "/"), dir: sf.IsDir}
		}
		s.sendStr("SUCC", g.name)
		if g.dir {
			out = append(out, g)
			continue
		}
		v, err := s.line("SIZE")
		if err != nil {
			return nil, err
		}
		size, _ := strconv.Atoi(v)
		s.send("SUCC", v)
		for len(g.data) < size {
			d, err := s.line("DATA")
			if err != nil {
				return nil, err
			}
			b, err := decode(d, 1<<30)
			if err != nil {
				return nil, err
			}
			g.data = append(g.data, b...)
			s.send("SUCC", strconv.Itoa(len(b)))
		}
		m, err := s.line("MD5")
		if err != nil {
			return nil, err
		}
		if b, _ := decode(m, 64); !bytes.Equal(b, md5sum(g.data)) {
			return nil, errors.New("server: md5")
		}
		s.send("SUCC", m)
		out = append(out, g)
	}
	if _, err := s.str("EXIT"); err != nil {
		return nil, err
	}
	s.end.Write([]byte("\x1b[u\x1b[0JSaved\r\n$ "))
	return out, nil
}

func md5sum(p []byte) []byte { s := md5.Sum(p); return s[:] }

func random(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func TestReceive(t *testing.T) {
	files := []srvFile{
		{rel: []string{"empty"}},
		{rel: []string{"big.bin"}, data: random(1<<20, 1)},
		{rel: []string{"note.txt"}, data: []byte("hello\n")},
	}
	for _, junk := range []bool{false, true} {
		cli, srv := xfertest.Pipe()
		s := newServer(t, srv)
		s.junk = junk
		errc := make(chan error, 1)
		go func() { errc <- s.tsz(map[string]any{"timeout": 5}, files, 64<<10, false) }()
		sink := &xfertest.Sink{Rename: func(n string) string {
			if n == "note.txt" {
				return "note.txt.0"
			}
			return n
		}}
		rest, err := Receive(context.Background(), cli, &Offer{Mode: Download, Tmux: junk}, sink, Options{Dir: "/tmp/x"})
		if err != nil {
			t.Fatalf("junk %v: %v", junk, err)
		}
		if err := <-errc; err != nil {
			t.Fatalf("junk %v: %v", junk, err)
		}
		for _, f := range files {
			name := f.rel[0]
			if name == "note.txt" {
				name = "note.txt.0"
			}
			if !bytes.Equal(sink.Files[name], f.data) || sink.Files[name] == nil && f.data != nil {
				t.Errorf("junk %v: %s not received whole", junk, name)
			}
		}
		if len(sink.Files) != 3 || sink.Aborted != 0 {
			t.Errorf("junk %v: %v, aborted %d", junk, sink.Committed(), sink.Aborted)
		}
		if s.action["binary"] != false || s.action["support_dir"] != true {
			t.Errorf("action %v", s.action)
		}
		// The server prints what the client says.
		time.Sleep(20 * time.Millisecond)
		if out := cli.Close(); !bytes.Contains(append(rest, out...), []byte("Saved 3 files/directories to /tmp/x")) {
			t.Errorf("junk %v: server printed %q", junk, append(rest, out...))
		}
	}
}

func TestReceiveDirectory(t *testing.T) {
	files := []srvFile{
		{rel: []string{"tree"}, dir: true, perm: 0o755},
		{rel: []string{"tree", "sub"}, dir: true, perm: 0o700},
		{rel: []string{"tree", "sub", "f"}, data: []byte("x"), perm: 0o600},
	}
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	errc := make(chan error, 1)
	go func() { errc <- s.tsz(map[string]any{"directory": true}, files, 1024, false) }()
	sink := &xfertest.Sink{}
	if _, err := Receive(context.Background(), cli, &Offer{Mode: Download}, sink, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if strings.Join(sink.Dirs, ",") != "tree,tree/sub" || string(sink.Files["tree/sub/f"]) != "x" {
		t.Fatalf("dirs %v files %v", sink.Dirs, sink.Committed())
	}
	for _, f := range sink.Info {
		if f.Name == "tree/sub/f" && f.Mode != 0o600 {
			t.Errorf("mode %v", f.Mode)
		}
	}
}

func TestReceiveBadMD5(t *testing.T) {
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	errc := make(chan error, 1)
	go func() {
		errc <- s.tsz(map[string]any{}, []srvFile{{rel: []string{"f"}, data: []byte("data")}}, 1024, true)
	}()
	sink := &xfertest.Sink{}
	_, err := Receive(context.Background(), cli, &Offer{Mode: Download}, sink, Options{})
	if err == nil || !strings.Contains(err.Error(), "MD5") {
		t.Fatalf("err %v", err)
	}
	if len(sink.Files) != 0 || sink.Aborted != 1 {
		t.Fatalf("committed %v, aborted %d", sink.Committed(), sink.Aborted)
	}
	// The server is told of the failure.
	if !bytes.Contains(cli.Wrote.Bytes(), []byte("#fail:"+encode([]byte("Check MD5 failed")))) {
		t.Errorf("client wrote %q", cli.Wrote.Bytes())
	}
	<-errc
}

func TestServerFailure(t *testing.T) {
	cli, srv := xfertest.Pipe()
	go func() {
		s := newServer(t, srv)
		s.handshake(map[string]any{})
		s.sendStr("fail", "No such file: a")
		srv.Write([]byte("No such file: a\r\n$ "))
	}()
	_, err := Receive(context.Background(), cli, &Offer{Mode: Download}, &xfertest.Sink{}, Options{})
	if !errors.Is(err, xfer.ErrRefused) || !strings.Contains(err.Error(), "No such file: a") {
		t.Fatalf("err %v", err)
	}
}

func TestSend(t *testing.T) {
	src := &xfertest.Source{Items: []xfertest.Item{
		{File: xfer.File{Name: "a.bin"}, Data: random(700<<10, 2)},
		{File: xfer.File{Name: "empty"}},
	}}
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	type res struct {
		got []got
		err error
	}
	resc := make(chan res, 1)
	go func() { g, err := s.trz(map[string]any{"bufsize": 256 << 10}); resc <- res{g, err} }()
	rest, err := Send(context.Background(), cli, &Offer{Mode: Upload}, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := <-resc
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.got) != 2 || r.got[0].name != "a.bin" || !bytes.Equal(r.got[0].data, src.Items[0].Data) || r.got[1].name != "empty" || len(r.got[1].data) != 0 {
		t.Fatalf("server got %+v", r.got)
	}
	_ = rest
}

func TestSendDirectory(t *testing.T) {
	src := &xfertest.Source{Items: []xfertest.Item{
		{File: xfer.File{Name: "d", Dir: true, Mode: 0o755}},
		{File: xfer.File{Name: "d/f", Mode: 0o644}, Data: []byte("in d")},
		{File: xfer.File{Name: "top"}, Data: []byte("top")},
	}}
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	type res struct {
		got []got
		err error
	}
	resc := make(chan res, 1)
	go func() { g, err := s.trz(map[string]any{"directory": true}); resc <- res{g, err} }()
	if _, err := Send(context.Background(), cli, &Offer{Mode: UploadDir}, src, Options{}); err != nil {
		t.Fatal(err)
	}
	r := <-resc
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.got) != 3 || !r.got[0].dir || r.got[1].name != "d/f" || string(r.got[1].data) != "in d" || r.got[2].name != "top" {
		t.Fatalf("server got %+v", r.got)
	}
}

func TestSendFolderToPlainTrz(t *testing.T) {
	src := &xfertest.Source{Items: []xfertest.Item{{File: xfer.File{Name: "d", Dir: true}}}}
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	go s.trz(map[string]any{})
	_, err := Send(context.Background(), cli, &Offer{Mode: Upload}, src, Options{})
	if err == nil || !strings.Contains(err.Error(), "trz -d") {
		t.Fatalf("err %v", err)
	}
	if src.Opened != 0 {
		t.Error("a file was opened")
	}
}

func TestCancel(t *testing.T) {
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	cli.Tamper = func(p []byte) []byte {
		if bytes.HasPrefix(p, []byte("#SUCC:")) {
			if n++; n == 3 {
				cancel()
			}
		}
		return p
	}
	errc := make(chan error, 1)
	go func() {
		errc <- s.tsz(map[string]any{}, []srvFile{{rel: []string{"big"}, data: random(1<<20, 3)}}, 1024, false)
	}()
	sink := &xfertest.Sink{}
	_, err := Receive(ctx, cli, &Offer{Mode: Download}, sink, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if len(sink.Files) != 0 || sink.Aborted != 1 {
		t.Fatalf("committed %v, aborted %d", sink.Committed(), sink.Aborted)
	}
	if !bytes.Contains(cli.Wrote.Bytes(), []byte("#fail:"+encode([]byte("Stopped")))) {
		t.Errorf("the server was not told")
	}
	srv.Close()
}

func TestRefuse(t *testing.T) {
	cli, srv := xfertest.Pipe()
	s := newServer(t, srv)
	errc := make(chan error, 1)
	go func() { _, err := s.trz(map[string]any{}); errc <- err }()
	if _, err := Refuse(context.Background(), cli, &Offer{Mode: Upload}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if s.action["confirm"] != false {
		t.Fatalf("action %v", s.action)
	}
}

func TestWindowsLines(t *testing.T) {
	cli, srv := xfertest.Pipe()
	// A Windows console redraws: escapes and a character shown twice.
	srv.Write([]byte("\x1b[25;1H#SUCC:1\x1b[?25l2!\n#SUCC:7\r\n\x1b[25;119H7!"))
	tr := newTransfer(context.Background(), cli, &Offer{Windows: true}, Options{})
	for _, want := range []int64{12, 7} {
		n, err := tr.recvInteger("SUCC")
		if err != nil || n != want {
			t.Fatalf("got %d %v, want %d", n, err, want)
		}
	}
	tr.sendLine("X", "y")
	if string(cli.Wrote.Bytes()) != "#X:y!\n" {
		t.Fatalf("wrote %q", cli.Wrote.Bytes())
	}
}

func TestDecodeBounded(t *testing.T) {
	if _, err := decode(encode(make([]byte, 1<<20)), 1000); !errors.Is(err, errLineLong) {
		t.Fatalf("err %v", err)
	}
}
