package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

// blockConn stops Write while held is closed, so a client close can sit
// in the socket and the server never sees it.
type blockConn struct {
	net.Conn
	mu   sync.Mutex
	held chan struct{}
}

func (b *blockConn) Write(p []byte) (int, error) {
	b.mu.Lock()
	held := b.held
	b.mu.Unlock()
	if held != nil {
		<-held
	}
	return b.Conn.Write(p)
}

func (b *blockConn) block() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held == nil {
		b.held = make(chan struct{})
	}
}

func (b *blockConn) unblock() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held != nil {
		close(b.held)
		b.held = nil
	}
}

func TestParseLinuxFrame(t *testing.T) {
	const frame = "os linux\n" +
		"cpu 100 0 50 900 10 1 2 3\n" +
		"core 10 0 0 40 0 0 0 0\n" +
		"mem 8388608 4194304 2097152 1048576\n" +
		"load 0.15 0.10 0.05\n" +
		"nic eth0 1000 2000\n" +
		"nic lo 9 9\n" +
		"vol 1000000 400000 /\n" +
		"vol 2000000 500000 /my data\n" +
		"gpu 40 1024 8192 55\n" +
		"gpu - - - -\n"
	s, err := ParseFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if s.OS != "linux" || !s.CPUOK || s.CPU.User != 100 || s.CPU.Idle != 900 || s.CPU.IOWait != 10 || s.CPU.Steal != 3 {
		t.Fatalf("cpu %+v", s.CPU)
	}
	if len(s.Cores) != 1 || s.Cores[0].User != 10 || s.Cores[0].Idle != 40 {
		t.Fatalf("cores %+v", s.Cores)
	}
	if !s.MemOK || s.MemTotal != 8388608 || s.MemAvail != 4194304 || s.SwapTotal != 2097152 || s.SwapFree != 1048576 {
		t.Fatalf("mem %+v", s)
	}
	if !s.LoadOK || s.Load1 != 0.15 || s.Load5 != 0.10 || s.Load15 != 0.05 {
		t.Fatalf("load %v %v %v", s.Load1, s.Load5, s.Load15)
	}
	if len(s.NICs) != 2 || s.NICs[0].Name != "eth0" || s.NICs[0].RX != 1000 || s.NICs[0].TX != 2000 || s.NICs[1].Name != "lo" {
		t.Fatalf("nics %+v", s.NICs)
	}
	if len(s.Vols) != 2 || s.Vols[0].Mount != "/" || s.Vols[0].UsedKB != 400000 || s.Vols[1].Mount != "/my data" || s.Vols[1].TotalKB != 2000000 {
		t.Fatalf("vols %+v", s.Vols)
	}
	if len(s.GPUs) != 2 || !s.GPUs[0].UtilOK || s.GPUs[0].Util != 40 || s.GPUs[0].MemUsed != 1024 || s.GPUs[0].Temp != 55 || s.GPUs[1].UtilOK || s.GPUs[1].MemOK {
		t.Fatalf("gpus %+v", s.GPUs)
	}
}

func TestParseDarwinFrame(t *testing.T) {
	const frame = "os darwin\n" +
		"cpu 10 20 30 40 0 0 0 0\n" +
		"mem 16777216 8388608 1048576 524288\n" +
		"load 1 0.5 0.25\n" +
		"nic en0 100 200\n" +
		"vol 500000 100000 /\n"
	s, err := ParseFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if s.OS != "darwin" || !s.CPUOK || s.CPU.User != 10 || s.CPU.Nice != 20 || s.CPU.System != 30 || s.CPU.Idle != 40 {
		t.Fatalf("cpu %+v", s.CPU)
	}
	if !s.MemOK || s.MemTotal != 16777216 || s.MemAvail != 8388608 || s.SwapTotal != 1048576 || s.SwapFree != 524288 {
		t.Fatalf("mem %+v", s)
	}
	if !s.LoadOK || s.Load1 != 1 || s.Load5 != 0.5 || s.Load15 != 0.25 {
		t.Fatalf("load %v %v %v", s.Load1, s.Load5, s.Load15)
	}
	if len(s.NICs) != 1 || s.NICs[0].Name != "en0" || s.NICs[0].RX != 100 || s.NICs[0].TX != 200 {
		t.Fatalf("nics %+v", s.NICs)
	}
	if len(s.Vols) != 1 || s.Vols[0].Mount != "/" || s.Vols[0].TotalKB != 500000 || s.Vols[0].UsedKB != 100000 {
		t.Fatalf("vols %+v", s.Vols)
	}
	if len(s.GPUs) != 0 || len(s.Cores) != 0 {
		t.Fatalf("gpu %d cores %d", len(s.GPUs), len(s.Cores))
	}
}

func TestParseCPUPct(t *testing.T) {
	s, err := ParseFrame("os darwin\ncpupct 37\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUOK || !s.CPUPctOK || s.CPUPct != 37 || len(s.Cores) != 0 {
		t.Fatalf("%+v", s)
	}
	r := Delta(Sample{}, s, 0)
	if !r.CPUOK || r.CPU != 37 {
		t.Fatalf("reading cpu %v ok %v", r.CPU, r.CPUOK)
	}
	ticks := Delta(
		Sample{CPUOK: true, CPU: CPU{User: 100, Idle: 900}, CPUPctOK: true, CPUPct: 1},
		Sample{CPUOK: true, CPU: CPU{User: 200, Idle: 1000}, CPUPctOK: true, CPUPct: 1},
		time.Second,
	)
	if !ticks.CPUOK || ticks.CPU != 50 {
		t.Fatalf("ticks %v ok %v", ticks.CPU, ticks.CPUOK)
	}
	if _, err := ParseFrame("cpupct no\n"); err == nil {
		t.Fatal("accepted a bad percent")
	}
	over, err := ParseFrame("cpupct 150\n")
	if err != nil || !over.CPUPctOK || over.CPUPct != 100 {
		t.Fatalf("clamp %+v err %v", over, err)
	}
}

func TestParseOSOnlyFrame(t *testing.T) {
	s, err := ParseFrame("os linux\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.OS != "linux" || s.CPUOK || s.MemOK || s.LoadOK {
		t.Fatalf("%+v", s)
	}
}

func TestParseDuplicateLastWins(t *testing.T) {
	s, err := ParseFrame("cpu 1 0 0 0 0 0 0 0\ncpu 5 0 0 0 0 0 0 0\nnic eth0 1 2\nnic eth0 1000 2000\nvol 1 1 /\nvol 9 8 /\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.CPU.User != 5 || len(s.NICs) != 1 || s.NICs[0].RX != 1000 || len(s.Vols) != 1 || s.Vols[0].TotalKB != 9 || s.Vols[0].UsedKB != 8 {
		t.Fatalf("%+v", s)
	}
}

func TestParseTruncatedFrame(t *testing.T) {
	var open Decoder
	samples, err := open.Write([]byte("<<<SSHYARD\nos linux\n"))
	if err != nil || len(samples) != 0 {
		t.Fatalf("samples %d err %v", len(samples), err)
	}
	if !errors.Is(open.Close(), ErrTruncated) {
		t.Fatal(open.Close())
	}

	var big Decoder
	_, err = big.Write([]byte("<<<SSHYARD\n" + strings.Repeat("a", 5000)))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("%v", err)
	}

	// A finished frame followed by a cut one: the finished sample is
	// kept, and closing reports the cut.
	var both Decoder
	samples, err = both.Write([]byte("<<<SSHYARD\nos linux\n>>>\n<<<SSHYARD\nos linux\n"))
	if err != nil || len(samples) != 1 || samples[0].OS != "linux" {
		t.Fatalf("samples %v err %v", samples, err)
	}
	if !errors.Is(both.Close(), ErrTruncated) {
		t.Fatal("close")
	}
}

func TestParseGarbageFrame(t *testing.T) {
	cases := []string{
		"<<<SSHYARD\nnope\n>>>\n",
		"hello\n",
		"",
	}
	for _, in := range cases {
		var d Decoder
		_, err := d.Write([]byte(in))
		if in == "" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !errors.Is(err, ErrGarbage) {
			t.Fatalf("%q → %v", in, err)
		}
	}
	for _, body := range []string{"", "nope 1\n", "cpu no\n", "cpu 1 2\n"} {
		if _, err := ParseFrame(body); !errors.Is(err, ErrGarbage) {
			t.Fatalf("%q → %v", body, err)
		}
	}
	// A good frame and then junk: the good sample is returned with the error.
	var d Decoder
	samples, err := d.Write([]byte("<<<SSHYARD\nos linux\n>>>\njunk\n"))
	if !errors.Is(err, ErrGarbage) || len(samples) != 1 || samples[0].OS != "linux" {
		t.Fatalf("samples %v err %v", samples, err)
	}
}

func TestParseSplitFramesIgnoreHeartbeat(t *testing.T) {
	const stream = ".\n<<<SSHYARD\nos linux\n>>>\n.\n<<<SSHYARD\nos darwin\n>>>\n"
	var d Decoder
	var got []Sample
	for i := 0; i < len(stream); i++ {
		s, err := d.Write([]byte{stream[i]})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, s...)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OS != "linux" || got[1].OS != "darwin" {
		t.Fatalf("%+v", got)
	}
}

func TestParseHostExtras(t *testing.T) {
	const frame = "os linux\n" +
		"osname Ubuntu 22.04.4 LTS\n" +
		"kernel 5.15.0-105-generic\n" +
		"cpu 1 0 0 9 0 0 0 0\n" +
		"gpu 40 1024 8192 55\n" +
		"gprocs 2\n" +
		"gproc 0 1234 900 python3\n" +
		"gproc - 99 - -\n" +
		"logins 3\n" +
		"login alice pts/0 203.0.113.5 2024-11-15 10:00\n" +
		"login bob console - Nov 5 09:12\n" +
		"login carol pts/1 tmux(1234).%0\n"
	s, err := ParseFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if s.OS != "linux" || s.OSName != "Ubuntu 22.04.4 LTS" || s.Kernel != "5.15.0-105-generic" || !s.CPUOK {
		t.Fatalf("os %q name %q kernel %q cpu %v", s.OS, s.OSName, s.Kernel, s.CPUOK)
	}
	want := []GPUProc{{GPU: 0, PID: 1234, Name: "python3", MemMiB: 900, MemOK: true}, {GPU: -1, PID: 99}}
	if !s.GPUProcsOK || s.GPUProcCount != 2 || len(s.GPUProcs) != 2 || s.GPUProcs[0] != want[0] || s.GPUProcs[1] != want[1] {
		t.Fatalf("gprocs ok %v count %d %+v", s.GPUProcsOK, s.GPUProcCount, s.GPUProcs)
	}
	logins := []Login{
		{User: "alice", Line: "pts/0", From: "203.0.113.5", When: "2024-11-15 10:00"},
		{User: "bob", Line: "console", When: "Nov 5 09:12"},
		{User: "carol", Line: "pts/1", From: "tmux(1234).%0"},
	}
	if !s.LoginsOK || s.LoginCount != 3 || len(s.Logins) != 3 {
		t.Fatalf("logins ok %v count %d %+v", s.LoginsOK, s.LoginCount, s.Logins)
	}
	for i, l := range logins {
		if s.Logins[i] != l {
			t.Fatalf("login %d %+v, want %+v", i, s.Logins[i], l)
		}
	}
	r := Delta(Sample{}, s, 0)
	if r.OSName != s.OSName || r.Kernel != s.Kernel || !r.LoginsOK || r.LoginCount != 3 || len(r.Logins) != 3 || !r.GPUProcsOK || r.GPUProcCount != 2 || len(r.GPUProcs) != 2 {
		t.Fatalf("reading %+v", r)
	}
	// Without the lines, nothing is known; an empty list still says so.
	bare, err := ParseFrame("os darwin\nlogins 0\n")
	if err != nil || bare.OSName != "" || bare.Kernel != "" || !bare.LoginsOK || bare.LoginCount != 0 || bare.GPUProcsOK {
		t.Fatalf("bare %+v err %v", bare, err)
	}
}

// A malformed extra line is dropped on its own; the frame's figures stay.
func TestParseHostExtrasMalformedIgnored(t *testing.T) {
	const frame = "os linux\n" +
		"cpu 1 0 0 9 0 0 0 0\n" +
		"osname \x1b‮​\n" +
		"kernel\n" +
		"logins many\n" +
		"logins -1\n" +
		"login alice pts/0\n" +
		"login \x07 pts/0 -\n" +
		"gprocs 1 2\n" +
		"gproc 0 0 10 zero-pid\n" +
		"gproc 64 1 10 far-gpu\n" +
		"gproc x 1 10 bad-gpu\n" +
		"gproc 0 1 -5 negative\n" +
		"gproc 0 1 10\n" +
		"mem 100 50 0 0\n"
	s, err := ParseFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CPUOK || !s.MemOK || s.MemTotal != 100 {
		t.Fatalf("figures lost: %+v", s)
	}
	if s.OSName != "" || s.Kernel != "" || s.LoginsOK || len(s.Logins) != 0 || s.GPUProcsOK || len(s.GPUProcs) != 0 {
		t.Fatalf("kept a bad line: %+v", s)
	}
	// An unknown key is still not a metrics frame.
	if _, err := ParseFrame("os linux\nloginz alice pts/0 -\n"); !errors.Is(err, ErrGarbage) {
		t.Fatalf("unknown key → %v", err)
	}
}

func TestParseHostExtrasCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString("os linux\nlogins 3\ngprocs 4\n")
	for i := range 12 {
		b.WriteString("login u" + strconv.Itoa(i) + " pts/" + strconv.Itoa(i) + " - -\n")
	}
	for i := range 15 {
		b.WriteString("gproc 0 " + strconv.Itoa(i+1) + " 1 p\n")
	}
	b.WriteString("logins 99999999\n")
	s, err := ParseFrame(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Logins) != maxLogins || s.Logins[maxLogins-1].User != "u7" || s.LoginCount != 1<<16 {
		t.Fatalf("logins %d count %d", len(s.Logins), s.LoginCount)
	}
	// A count under the lines it came with is raised to them.
	if len(s.GPUProcs) != maxGPUProcs || s.GPUProcs[0].PID != 1 || s.GPUProcCount != maxGPUProcs {
		t.Fatalf("gprocs %d count %d", len(s.GPUProcs), s.GPUProcCount)
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"Ubuntu 22.04 LTS", 64, "Ubuntu 22.04 LTS"},
		{"  a \t  b  ", 64, "a b"},
		{"a\x1b[31mb\x00c\x7f", 64, "a[31mbc"},
		{"evil‮gnp.exe​", 64, "evilgnp.exe"},
		{"bad\xffutf8", 64, "bad�utf8"},
		{"数据中心服务器", 3, "数据中"},
		{"ab cd", 3, "ab"},
		{"abcdef", 4, "abcd"},
		{"\x01\x02", 8, ""},
	}
	for _, c := range cases {
		if got := cleanText(c.in, c.n); got != c.want {
			t.Fatalf("cleanText(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestDeltaCPUMemoryDiskAndNetwork(t *testing.T) {
	prev := Sample{
		CPUOK: true,
		CPU:   CPU{User: 100, Idle: 900},
		Cores: []CPU{{User: 50, Idle: 450}, {User: 50, Idle: 450}},
		NICs: []NIC{
			{Name: "eth0", RX: 1000, TX: 2000},
			{Name: "eth1", RX: 0, TX: 0},
			{Name: "lo", RX: 100, TX: 100},
		},
	}
	cur := Sample{
		OS:    "linux",
		CPUOK: true,
		CPU:   CPU{User: 200, Idle: 1000},
		Cores: []CPU{{User: 100, Idle: 500}, {User: 150, Idle: 450}},
		NICs: []NIC{
			{Name: "eth0", RX: 3000, TX: 2000},
			{Name: "eth1", RX: 0, TX: 4000},
			{Name: "lo", RX: 9000, TX: 9000},
		},
		MemOK: true, MemTotal: 8388608, MemAvail: 4194304, SwapTotal: 2097152, SwapFree: 1048576,
		LoadOK: true, Load1: 0.15, Load5: 0.10, Load15: 0.05,
		Vols: []Vol{{TotalKB: 1000000, UsedKB: 400000, Mount: "/"}},
		GPUs: []GPU{{Util: 40, UtilOK: true}, {Util: 80, UtilOK: true}},
	}
	r := Delta(prev, cur, 2*time.Second)
	if !r.CPUOK || r.CPU != 50 {
		t.Fatalf("cpu %v ok %v", r.CPU, r.CPUOK)
	}
	if len(r.Cores) != 2 || !r.Cores[0].OK || r.Cores[0].Pct != 50 || !r.Cores[1].OK || r.Cores[1].Pct != 100 {
		t.Fatalf("cores %+v", r.Cores)
	}
	if !r.NetOK || r.NetDown != 1000 || r.NetUp != 2000 {
		t.Fatalf("net down %v up %v ok %v", r.NetDown, r.NetUp, r.NetOK)
	}
	if len(r.NICs) != 2 || r.NICs[0].Name != "eth0" || r.NICs[0].Down != 1000 || r.NICs[0].Up != 0 || r.NICs[1].Name != "eth1" || r.NICs[1].Up != 2000 || r.NICs[1].Down != 0 {
		t.Fatalf("nics %+v", r.NICs)
	}
	if !r.MemOK || r.MemUsedKB != 4194304 || r.MemTotalKB != 8388608 || r.MemPct != 50 {
		t.Fatalf("mem %+v", r)
	}
	if !r.SwapOK || r.SwapUsedKB != 1048576 || r.SwapTotalKB != 2097152 || r.SwapPct != 50 {
		t.Fatalf("swap %+v", r)
	}
	if len(r.Vols) != 1 || r.Vols[0].Pct != 40 || r.Vols[0].Mount != "/" {
		t.Fatalf("vol %+v", r.Vols)
	}
	if !r.LoadOK || r.Load1 != 0.15 || len(r.GPUs) != 2 || r.GPUs[1].Util != 80 {
		t.Fatalf("load/gpu %+v", r)
	}

	first := Delta(Sample{}, cur, 0)
	if first.CPUOK || first.NetOK || !first.MemOK || first.MemUsedKB != 4194304 || len(first.GPUs) != 2 {
		t.Fatalf("first %+v", first)
	}

	reset := Delta(Sample{CPUOK: true, CPU: CPU{User: 500, Idle: 500}}, Sample{CPUOK: true, CPU: CPU{User: 1, Idle: 1}}, time.Second)
	if reset.CPUOK {
		t.Fatal("counter reset still reported a percent")
	}

	drop := Delta(
		Sample{NICs: []NIC{{Name: "eth0", RX: 5000, TX: 80}}},
		Sample{NICs: []NIC{{Name: "eth0", RX: 1000, TX: 80}}},
		2*time.Second,
	)
	if !drop.NetOK || drop.NetDown != 0 || drop.NetUp != 0 {
		t.Fatalf("drop %+v", drop)
	}

	over := Delta(Sample{}, Sample{Vols: []Vol{{TotalKB: 100, UsedKB: 150, Mount: "/"}}}, 0)
	if len(over.Vols) != 1 || over.Vols[0].Pct != 150 {
		t.Fatalf("over %+v", over.Vols)
	}
}

func TestMetricsCommandQuotesMount(t *testing.T) {
	cmd := MetricsCommand(99, "/mnt/my disk")
	if !strings.HasSuffix(cmd, " 2 '/mnt/my disk'") {
		t.Fatalf("suffix %q", endOf(cmd, 40))
	}
	bad := MetricsCommand(1, "/data/$(reboot)")
	if strings.Contains(bad, "reboot") {
		t.Fatal("rejected mount was interpolated")
	}
	if !strings.HasSuffix(bad, " 1 ''") {
		t.Fatalf("suffix %q", endOf(bad, 20))
	}
	if !MetricsMountOK("/mnt/数据") || !MetricsMountOK("") || MetricsMountOK("relative") || MetricsMountOK("/a'") {
		t.Fatal("mount check")
	}
	if MetricsInterval(10) != 10 || MetricsInterval(3) != 2 {
		t.Fatal("interval")
	}
	if strings.Contains(metricsScript, "'") || strings.Contains(metricsScript, "`") {
		t.Fatal("script cannot be wrapped in single quotes")
	}
	// <<<SSHYARD is the frame marker. Anything else starting with << is a here-document.
	if strings.Contains(strings.ReplaceAll(metricsScript, "<<<SSHYARD", ""), "<<") {
		t.Fatal("here-document would be written to a temp file")
	}
}

func TestMetricsCommandFitsDropbear(t *testing.T) {
	for _, mount := range []string{"", "/mnt/my disk", "/mnt/数据", "/" + strings.Repeat("x", 255)} {
		for _, interval := range []int{0, 1, 2, 5, 10} {
			cmd := MetricsCommand(interval, mount)
			if len(cmd) > 9000 {
				t.Fatalf("exec command is %d bytes; Dropbear limits strings to 9000 bytes", len(cmd))
			}
		}
	}
}

func TestMetricsCommandReadsStdin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, skipNoSh(t), "-c", MetricsCommand(99, "/mnt/my disk"))
	cmd.Stdin = strings.NewReader("printf '%s\\n' \"$1\" \"$2\"\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stdin script: %v %s", err, out)
	}
	if string(out) != "2\n/mnt/my disk\n" {
		t.Fatalf("script arguments: %q", out)
	}
}

func endOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func skipNoSh(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	return sh
}

func TestMetricsCommandSyntax(t *testing.T) {
	sh := skipNoSh(t)
	out, err := exec.Command(sh, "-n", "-c", metricsScript).CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	quoted := MetricsCommand(2, "/mnt/my disk")
	out, err = exec.Command(sh, "-n", "-c", quoted).CombinedOutput()
	if err != nil {
		t.Fatalf("command: %v %s", err, out)
	}
}

func TestMetricsScriptExitsWhenStdoutCloses(t *testing.T) {
	sh := skipNoSh(t)
	cmd := exec.Command(sh, "-c", metricsScript, "_", "10", "")
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	buf := make([]byte, 64)
	if n, err := out.Read(buf); n == 0 || err != nil {
		t.Fatalf("no output: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("exited before stdout closed")
	default:
	}
	out.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("collector still running 5s after stdout closed")
	}
}

func TestMetricsScriptInterval(t *testing.T) {
	sh := skipNoSh(t)
	// A slow df must not push the gap out by its own runtime.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "df"), []byte("#!/bin/sh\nsleep 2\nprintf '%s\\n' 'Filesystem 1024-blocks Used Available Capacity Mounted on' 'root 1000000 100000 900000 10% /'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", metricsScript, "_", "10", "")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := out.Read(buf)
			ch <- chunk{append([]byte(nil), buf[:n]...), err}
			if err != nil {
				return
			}
		}
	}()
	var acc []byte
	var marks []time.Time
	deadline := time.Now().Add(36 * time.Second)
	for len(marks) < 3 && time.Now().Before(deadline) {
		select {
		case c := <-ch:
			acc = append(acc, c.b...)
			if c.err != nil && c.err != io.EOF {
				t.Fatal(c.err)
			}
		case <-time.After(time.Until(deadline)):
		}
		for {
			i := bytes.Index(acc, []byte("<<<SSHYARD\n"))
			if i < 0 {
				break
			}
			marks = append(marks, time.Now())
			acc = acc[i+len("<<<SSHYARD\n"):]
		}
	}
	if len(marks) < 3 {
		t.Fatalf("frames %d", len(marks))
	}
	// The second frame follows the one-second warmup and its own 2s df.
	// The next gap still includes the slow sample inside the interval.
	first := marks[1].Sub(marks[0])
	if first > 5*time.Second {
		t.Fatalf("second frame waited a full interval: %s", first)
	}
	// The script counts in whole epoch seconds, so the interval may end up
	// to a second early, and the two slow samples need not take as long.
	gap := marks[2].Sub(marks[1])
	if gap < 8*time.Second || gap > 11*time.Second {
		t.Fatalf("gap %s", gap)
	}
}

// A first emit can cross an epoch-second boundary even when it is quick.
// Exercise the actual scheduling loop with that clock transition, rather than
// relying on the host clock landing near a second boundary during the test.
func TestMetricsScriptWarmupWaitsAcrossSecondBoundary(t *testing.T) {
	sh := skipNoSh(t)
	const loop = "while :; do\n  start=$(date +%s"
	at := strings.LastIndex(metricsScript, loop)
	if at < 0 {
		t.Fatal("sampling loop not found")
	}
	clock := `
# The first sample starts in second 100 and completes in second 101.
date() {
  if [ "$sent" -eq 0 ]; then printf '100\n'; else printf '101\n'; fi
}
emit() {
  printf 'frame\n'
  if [ "$sent" -gt 0 ]; then exit 0; fi
}
sleep() { printf 'sleep %s\n' "$1"; }
`
	cmd := exec.Command(sh, "-c", metricsScript[:at]+clock+metricsScript[at:], "_", "10", "")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	if got, want := string(out), "frame\n.\nsleep 1\nframe\n"; got != want {
		t.Fatalf("first sample must wait one real second before the next emit: %q, want %q", got, want)
	}
}

func TestMetricsScriptSecondFrame(t *testing.T) {
	sh := skipNoSh(t)
	cmd := exec.Command(sh, "-c", metricsScript, "_", "10", "")
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := out.Read(buf)
			ch <- chunk{append([]byte(nil), buf[:n]...), err}
			if err != nil {
				return
			}
		}
	}()
	var acc []byte
	var marks []time.Time
	deadline := time.Now().Add(24 * time.Second)
	for len(marks) < 3 && time.Now().Before(deadline) {
		select {
		case c := <-ch:
			acc = append(acc, c.b...)
			if c.err != nil && c.err != io.EOF && len(marks) < 3 {
				t.Fatal(c.err)
			}
		case <-time.After(time.Until(deadline)):
		}
		for {
			i := bytes.Index(acc, []byte("<<<SSHYARD\n"))
			if i < 0 {
				break
			}
			marks = append(marks, time.Now())
			acc = acc[i+len("<<<SSHYARD\n"):]
		}
	}
	if len(marks) < 3 {
		t.Fatalf("frames %d", len(marks))
	}
	first := marks[1].Sub(marks[0])
	if first < 500*time.Millisecond || first > 2*time.Second {
		t.Fatalf("first gap %s", first)
	}
	gap := marks[2].Sub(marks[1])
	if gap < 9*time.Second || gap > 11*time.Second {
		t.Fatalf("interval gap %s", gap)
	}
}

func TestMetricsDefaultIntervalReady(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd", "netbsd":
	default:
		t.Skip(runtime.GOOS)
	}
	sh := skipNoSh(t)
	cmd := exec.Command(sh, "-c", metricsScript, "_", strconv.Itoa(MetricsInterval(0)), "")
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			ch <- chunk{append([]byte(nil), buf[:n]...), err}
			if err != nil {
				return
			}
		}
	}()
	var dec Decoder
	var frames []Sample
	var ready time.Duration
	deadline := start.Add(3 * time.Second)
	for ready == 0 && time.Now().Before(deadline) {
		select {
		case c := <-ch:
			if len(c.b) > 0 {
				ss, perr := dec.Write(c.b)
				if perr != nil {
					t.Fatalf("parse %v\n%s", perr, c.b)
				}
				frames = append(frames, ss...)
				if len(frames) >= 2 {
					ready = time.Since(start)
				}
			}
			if c.err != nil && ready == 0 {
				t.Fatal(c.err)
			}
		case <-time.After(time.Until(deadline)):
		}
	}
	if ready == 0 || ready > 2*time.Second {
		t.Fatalf("ready at %s frames %d", ready, len(frames))
	}
	r := Delta(frames[0], frames[1], ready)
	disk := false
	for _, v := range r.Vols {
		if v.TotalKB > 0 {
			disk = true
		}
	}
	if !r.CPUOK || !r.MemOK || !disk || !r.NetOK || !r.LoadOK {
		t.Fatalf("cpu %v mem %v disk %v net %v load %v", r.CPUOK, r.MemOK, disk, r.NetOK, r.LoadOK)
	}
}

func TestMetricsScriptDarwinCPU(t *testing.T) {
	sh := skipNoSh(t)
	dir := t.TempDir()
	stubs := map[string]string{
		"uname": "#!/bin/sh\nprintf '%s\\n' Darwin\n",
		"sysctl": "#!/bin/sh\ncase $2 in\n" +
			"kern.cp_time|kern.cp_times) exit 1 ;;\n" +
			"hw.logicalcpu) printf '%s\\n' 4 ;;\n" +
			"hw.memsize) printf '%s\\n' 8589934592 ;;\n" +
			"vm.loadavg) printf '%s\\n' '{ 1.50 0.80 0.40 }' ;;\n" +
			"vm.swapusage) printf '%s\\n' 'total = 2048.00M  used = 512.00M  free = 1536.00M  (encrypted)' ;;\n" +
			"*) exit 1 ;;\n" +
			"esac\n",
		"ps": "#!/bin/sh\nprintf '%s\\n' '  10.0' '  10.0' '  10.0' '  10.0' '   2.0'\n",
		"vm_stat": "#!/bin/sh\nprintf '%s\\n' \\\n" +
			"'Mach Virtual Memory Statistics: (page size of 16384 bytes)' \\\n" +
			"'Pages free:                                1000.' \\\n" +
			"'Pages active:                              2000.' \\\n" +
			"'Pages inactive:                            3000.' \\\n" +
			"'Pages speculative:                          100.' \\\n" +
			"'Pages throttled:                              0.' \\\n" +
			"'Pages wired down:                           400.'\n",
		"netstat": "#!/bin/sh\nprintf '%s\\n' \\\n" +
			"'Name  Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll' \\\n" +
			"'lo0   16384 <Link#1>                         10     0       1000       10     0       1000     0' \\\n" +
			"'en0   1500  <Link#6>    aa:bb:cc:dd:ee:ff    20     0       5000       30     0       7000     0'\n",
		"df": "#!/bin/sh\nprintf '%s\\n' 'Filesystem 1024-blocks Used Available Capacity Mounted on' '/dev/disk3s1 1000000 400000 600000 40% /'\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(sh, "-c", metricsScript, "_", "10", "")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	var dec Decoder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	var frames []Sample
	for len(frames) == 0 && time.Now().Before(deadline) {
		n, err := out.Read(buf)
		if n > 0 {
			ss, perr := dec.Write(buf[:n])
			if perr != nil {
				t.Fatalf("parse %v\n%s", perr, buf[:n])
			}
			frames = append(frames, ss...)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(frames) == 0 {
		t.Fatal("no frame")
	}
	s := frames[0]
	// 10+10+10+10+2 = 42 percent of one CPU, / 4 logical CPUs = 10.5, rounds to 11.
	if s.OS != "darwin" || s.CPUOK || !s.CPUPctOK || s.CPUPct != 11 || len(s.Cores) != 0 {
		t.Fatalf("cpu os %s ticks %v pct %v ok %v cores %d", s.OS, s.CPUOK, s.CPUPct, s.CPUPctOK, len(s.Cores))
	}
	r := Delta(Sample{}, s, 0)
	if !r.CPUOK || r.CPU != 11 {
		t.Fatalf("reading %+v", r.CPU)
	}
	if !s.MemOK || s.MemTotal != 8388608 || s.MemAvail != 8348608 || s.SwapTotal != 2097152 || s.SwapFree != 1572864 {
		t.Fatalf("mem total %d avail %d swap %d free %d", s.MemTotal, s.MemAvail, s.SwapTotal, s.SwapFree)
	}
	if !s.LoadOK || s.Load1 != 1.5 || s.Load5 != 0.8 || s.Load15 != 0.4 {
		t.Fatalf("load %v %v %v", s.Load1, s.Load5, s.Load15)
	}
	if len(s.Vols) != 1 || s.Vols[0].Mount != "/" || s.Vols[0].TotalKB != 1000000 || s.Vols[0].UsedKB != 400000 {
		t.Fatalf("vols %+v", s.Vols)
	}
	if len(s.NICs) != 1 || s.NICs[0].Name != "en0" || s.NICs[0].RX != 5000 || s.NICs[0].TX != 7000 {
		t.Fatalf("nics %+v", s.NICs)
	}
}

// The extras come from who, nvidia-smi, sw_vers and uname, run once or
// cached, and every frame carries them.
func TestMetricsScriptHostExtras(t *testing.T) {
	sh := skipNoSh(t)
	dir := t.TempDir()
	who := "#!/bin/sh\nprintf '%s\\n' 'USER     TTY      IDLE   TIME            HOST' \\\n" +
		"'alice    pts/0        2024-11-15 10:00 (203.0.113.5)' \\\n" +
		"'bob      console      Nov  5 09:12' \\\n" +
		"'carol    pts/1        2024-11-15 11:00 (tmux(1234).%0)'"
	for i := range 8 {
		who += " \\\n'u" + strconv.Itoa(i) + "  pts/" + strconv.Itoa(i+2) + "  2024-11-15 12:00 (10.0.0." + strconv.Itoa(i) + ")'"
	}
	stubs := map[string]string{
		"uname":   "#!/bin/sh\ncase $1 in\n-r) printf '%s\\n' 24.1.0 ;;\n*) printf '%s\\n' Darwin ;;\nesac\n",
		"sw_vers": "#!/bin/sh\ncase $1 in\n-productName) printf '%s\\n' macOS ;;\n-productVersion) printf '%s\\n' 15.1 ;;\nesac\n",
		"who":     who + "\n",
		"nvidia-smi": "#!/bin/sh\ncase $1 in\n" +
			"--query-gpu=*) printf '%s\\n' '40, 1024, 8192, 55, GPU-aaaa-1111' '0, 10, 8192, 40, GPU-bbbb-2222' ;;\n" +
			"--query-compute-apps=*) printf '%s\\n' '1234, 900, GPU-bbbb-2222, /usr/bin/python3' '99, [N/A], GPU-zz*z, /opt/a/my,app' 'bad, 1, GPU-aaaa-1111, x' ;;\n" +
			"*) exit 1 ;;\n" +
			"esac\n",
	}
	for _, name := range []string{"sysctl", "ps", "vm_stat", "netstat", "df"} {
		stubs[name] = "#!/bin/sh\nexit 1\n"
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(sh, "-c", metricsScript, "_", "10", "")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	var dec Decoder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	var frames []Sample
	for len(frames) == 0 && time.Now().Before(deadline) {
		n, err := out.Read(buf)
		if n > 0 {
			ss, perr := dec.Write(buf[:n])
			if perr != nil {
				t.Fatalf("parse %v\n%s", perr, buf[:n])
			}
			frames = append(frames, ss...)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(frames) == 0 {
		t.Fatal("no frame")
	}
	s := frames[0]
	if s.OS != "darwin" || s.OSName != "macOS 15.1" || s.Kernel != "24.1.0" {
		t.Fatalf("os %q name %q kernel %q", s.OS, s.OSName, s.Kernel)
	}
	if len(s.GPUs) != 2 || s.GPUs[0].Util != 40 || s.GPUs[1].MemUsed != 10 {
		t.Fatalf("gpus %+v", s.GPUs)
	}
	// The bad pid is not a process; an unknown or unsafe UUID has no GPU.
	want := []GPUProc{{GPU: 1, PID: 1234, Name: "python3", MemMiB: 900, MemOK: true}, {GPU: -1, PID: 99, Name: "my,app"}}
	if !s.GPUProcsOK || s.GPUProcCount != 2 || len(s.GPUProcs) != 2 || s.GPUProcs[0] != want[0] || s.GPUProcs[1] != want[1] {
		t.Fatalf("gprocs count %d %+v", s.GPUProcCount, s.GPUProcs)
	}
	// The header is skipped, and only eight of the eleven sessions are sent.
	if !s.LoginsOK || s.LoginCount != 11 || len(s.Logins) != maxLogins {
		t.Fatalf("logins count %d %+v", s.LoginCount, s.Logins)
	}
	logins := []Login{
		{User: "alice", Line: "pts/0", From: "203.0.113.5", When: "2024-11-15 10:00"},
		{User: "bob", Line: "console", When: "Nov 5 09:12"},
		{User: "carol", Line: "pts/1", From: "tmux(1234).%0", When: "2024-11-15 11:00"},
		{User: "u0", Line: "pts/2", From: "10.0.0.0", When: "2024-11-15 12:00"},
	}
	for i, l := range logins {
		if s.Logins[i] != l {
			t.Fatalf("login %d %+v, want %+v", i, s.Logins[i], l)
		}
	}
}

func TestMetricsScriptOSRelease(t *testing.T) {
	sh := skipNoSh(t)
	const call = "\nprobe_os\nwhile :; do"
	at := strings.LastIndex(metricsScript, call)
	if at < 0 {
		t.Fatal("probe not found")
	}
	cases := []struct{ file, want string }{
		{"NAME=\"Ubuntu\"\nVERSION_ID=\"22.04\"\nPRETTY_NAME=\"Ubuntu 22.04.4 LTS\"\n", "Ubuntu 22.04.4 LTS"},
		{"NAME='Alpine Linux'\nVERSION_ID=3.20.3", "Alpine Linux 3.20.3"},
		{"NAME=Arch\n", "Arch"},
		{"PRETTY_NAME=\"" + strings.Repeat("x", 100) + "\"\n", strings.Repeat("x", 64)},
		{"ID=void\n", ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		f := filepath.Join(dir, "os-release")
		if err := os.WriteFile(f, []byte(c.file), 0o644); err != nil {
			t.Fatal(err)
		}
		script := strings.Replace(metricsScript[:at], "/etc/os-release /usr/lib/os-release", f, 1) +
			"\nkind=linux\nprobe_os\nprintf \"%s\\n\" \"$os_name\"\n"
		out, err := exec.Command(sh, "-c", script, "_", "10", "").Output()
		if err != nil {
			t.Fatalf("script: %v %s", err, out)
		}
		if got := strings.TrimSuffix(string(out), "\n"); got != c.want {
			t.Fatalf("%q → %q, want %q", c.file, got, c.want)
		}
	}
}

func TestMetricsScriptLiveSample(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd", "netbsd":
	default:
		t.Skip(runtime.GOOS)
	}
	sh := skipNoSh(t)
	cmd := exec.Command(sh, "-c", metricsScript, "_", "1", "")
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	var dec Decoder
	buf := make([]byte, 1024)
	var frames []Sample
	deadline := time.Now().Add(8 * time.Second)
	cpuOK := false
	for time.Now().Before(deadline) && !cpuOK {
		n, err := out.Read(buf)
		if n > 0 {
			ss, perr := dec.Write(buf[:n])
			if perr != nil {
				t.Fatalf("parse %v\n%s", perr, buf[:n])
			}
			frames = append(frames, ss...)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(frames) >= 2 {
			r := Delta(frames[len(frames)-2], frames[len(frames)-1], time.Second)
			cpuOK = r.CPUOK
		}
	}
	if len(frames) < 2 || !cpuOK {
		t.Fatalf("frames %d cpu %v", len(frames), cpuOK)
	}
	last := frames[len(frames)-1]
	r := Delta(frames[len(frames)-2], last, time.Second)
	root := false
	for _, v := range last.Vols {
		if v.Mount == "/" && v.TotalKB > 0 {
			root = true
		}
	}
	if runtime.GOOS == "darwin" {
		if last.OS != "darwin" || !r.CPUOK || !last.MemOK || !last.LoadOK || !root {
			t.Fatalf("darwin os %s cpu %v mem %v load %v disk %v", last.OS, r.CPUOK, last.MemOK, last.LoadOK, root)
		}
		return
	}
	want := "linux"
	if runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" {
		want = "bsd"
	}
	if last.OS != want || !last.MemOK || !last.LoadOK {
		t.Fatalf("os %s mem %v load %v", last.OS, last.MemOK, last.LoadOK)
	}
	if !root {
		t.Fatalf("vols %+v", last.Vols)
	}
}

func twoFrames() string {
	return "<<<SSHYARD\n" +
		"os linux\n" +
		"cpu 100 0 0 900 0 0 0 0\n" +
		">>>\n" +
		"<<<SSHYARD\n" +
		"os linux\n" +
		"cpu 200 0 0 1000 0 0 0 0\n" +
		">>>\n"
}

func TestCollectorStreamsScriptKeepsTerminal(t *testing.T) {
	skipNoSh(t)
	srv := server(t)
	run := sshtest.ShellExec(t.TempDir())
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		// Dropbear exits the entire connection while decoding an oversized string.
		if len(cmd) > 9000 {
			srv.Drop()
			return 1
		}
		return run(ch, cmd)
	}
	s := New(Config{Addr: srv.Addr, User: "demo", Credentials: password("secret"), HostKey: trusted(t, srv).Callback()}, Options{}, 80, 24)
	defer s.Close()
	s.Start()
	waitState(t, s, Connected)
	waitText(t, s, "Welcome")
	c := NewCollector()
	defer c.Stop()
	c.Start(s.Client(), 1, "")
	waitFor(t, "metrics or lost transport", func() bool {
		r, _ := c.View()
		return r.OS != "" || s.Status().State != Connected
	})
	if st := s.Status(); st.State != Connected {
		t.Fatalf("metrics disconnected the interactive terminal: %v", st.Err)
	}
	r, reason := c.View()
	if reason != "" || r.OS == "" || len(r.Vols) == 0 {
		t.Fatalf("metrics reading %+v, reason %q", r, reason)
	}
	c.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("after metrics\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, s, "you said: after metrics")
}

func TestCollectorReadsOneCommand(t *testing.T) {
	srv := server(t)
	var starts atomic.Int32
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		starts.Add(1)
		ch.Write([]byte(twoFrames()))
		for {
			if _, err := io.WriteString(ch, ".\n"); err != nil {
				return 0
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	client := dialClient(t, srv)
	c := NewCollector()
	defer c.Stop()
	c.Start(client, 2, "")
	waitFor(t, "cpu", func() bool {
		r, reason := c.View()
		return reason == "" && r.CPUOK && r.CPU == 50
	})
	c.Start(client, 2, "")
	c.Start(client, 2, "")
	time.Sleep(100 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatalf("starts %d", starts.Load())
	}
	c.Start(client, 10, "")
	waitFor(t, "restart", func() bool { return starts.Load() == 2 })
}

func TestCollectorFailureDoesNotRetry(t *testing.T) {
	srv := server(t)
	var starts atomic.Int32
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		starts.Add(1)
		ch.Write([]byte("bogus\n"))
		io.Copy(io.Discard, ch)
		return 1
	}
	client := dialClient(t, srv)
	c := NewCollector()
	defer c.Stop()
	c.Start(client, 2, "/data")
	waitFor(t, "parse failure", func() bool {
		_, reason := c.View()
		return reason == ReasonParse
	})
	c.Start(client, 10, "/data")
	time.Sleep(150 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatalf("retried a failed client: %d", starts.Load())
	}

	srv.Exec = func(ch ssh.Channel, cmd string) int {
		starts.Add(1)
		io.Copy(io.Discard, ch)
		return 127
	}
	c2 := NewCollector()
	defer c2.Stop()
	c2.Start(dialClient(t, srv), 2, "")
	waitFor(t, "shell", func() bool {
		_, reason := c2.View()
		return reason == ReasonShell
	})
}

func TestCollectorRetriesAfterNewClient(t *testing.T) {
	srv := server(t)
	var starts atomic.Int32
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		starts.Add(1)
		ch.Write([]byte("bogus\n"))
		io.Copy(io.Discard, ch)
		return 1
	}
	c := NewCollector()
	defer c.Stop()
	c.Start(dialClient(t, srv), 2, "")
	waitFor(t, "fail", func() bool {
		_, reason := c.View()
		return reason == ReasonParse
	})
	c.Start(dialClient(t, srv), 2, "")
	waitFor(t, "second try", func() bool { return starts.Load() == 2 })
}

func TestCollectorExecRefused(t *testing.T) {
	srv := server(t)
	c := NewCollector()
	defer c.Stop()
	c.Start(dialClient(t, srv), 2, "")
	waitFor(t, "refused", func() bool {
		_, reason := c.View()
		return reason == ReasonExec
	})
	_, reason := c.View()
	if strings.Contains(reason, "sh") {
		t.Fatalf("reason leaked the command: %s", reason)
	}
}

func TestCollectorStopEndsRemote(t *testing.T) {
	srv := server(t)
	var running atomic.Int32
	var starts atomic.Int32
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		starts.Add(1)
		running.Add(1)
		defer running.Add(-1)
		for {
			if _, err := io.WriteString(ch, ".\n"); err != nil {
				return 0
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	client := dialClient(t, srv)
	c := NewCollector()
	c.Start(client, 2, "")
	waitFor(t, "started", func() bool { return running.Load() == 1 })
	c.Stop()
	waitFor(t, "remote exit", func() bool { return running.Load() == 0 })
	c.Start(client, 2, "")
	waitFor(t, "restart", func() bool { return starts.Load() == 2 })
	c.Stop()
	waitFor(t, "second remote exit", func() bool { return running.Load() == 0 })
}

func TestCollectorStopReturnsWhileCloseHangs(t *testing.T) {
	srv := server(t)
	var starts atomic.Int32
	var current atomic.Int32
	entered := make(chan struct{})
	var once sync.Once
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		current.Add(1)
		defer current.Add(-1)
		starts.Add(1)
		once.Do(func() { close(entered) })
		for {
			if _, err := io.WriteString(ch, ".\n"); err != nil {
				return 0
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	raw, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	bw := &blockConn{Conn: raw}
	cc, chans, reqs, err := ssh.NewClientConn(bw, srv.Addr, &ssh.ClientConfig{
		User:            "demo",
		Auth:            []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: trusted(t, srv).Callback(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	defer bw.unblock()

	c := NewCollector()
	c.Start(client, 2, "")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exec did not start")
	}
	bw.block()
	stopped := make(chan struct{})
	go func() {
		c.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop blocked while the server held the channel")
	}
	c.Start(client, 2, "")
	time.Sleep(200 * time.Millisecond)
	if starts.Load() != 1 || current.Load() > 1 {
		t.Fatalf("second channel opened: starts %d current %d", starts.Load(), current.Load())
	}
	// Cancel the queued run before the first channel finishes, then let
	// the first one go. It must not open the command it never reached.
	c.Stop()
	bw.unblock()
	waitFor(t, "first exit", func() bool { return current.Load() == 0 })
	time.Sleep(200 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatalf("cancelled run opened a channel: starts %d", starts.Load())
	}
}

func TestCollectorDoesNotTakePoolRef(t *testing.T) {
	srv := server(t)
	srv.Exec = func(ch ssh.Channel, cmd string) int {
		io.Copy(io.Discard, ch)
		return 0
	}
	kh := trusted(t, srv)
	p := NewPool()
	cfg := Config{Addr: srv.Addr, User: "demo", HostKey: kh.Callback(), Pool: p, Key: "k",
		Credentials: password("secret")}
	client, release, err := p.Acquire(context.Background(), cfg.Key, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	c := NewCollector()
	defer c.Stop()
	c.Start(client, 2, "")
	time.Sleep(50 * time.Millisecond)
	if p.Shared(cfg.Key) != 1 {
		t.Fatalf("collector took a pool ref: %d", p.Shared(cfg.Key))
	}
}
