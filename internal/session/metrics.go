package session

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

// Reason codes for a collector that will not try again on this connection.
// The bar translates them; they are not the remote error text (that text
// includes the script).
const (
	ReasonExec  = "exec"  // the server refused the exec channel
	ReasonShell = "shell" // sh was not found
	ReasonParse = "parse" // the output was not a metrics frame
)

// ErrTruncated is a frame that was cut off or longer than 4 KB.
// ErrGarbage is output that is not a metrics frame.
var (
	ErrTruncated = errors.New("session: metrics frame truncated")
	ErrGarbage   = errors.New("session: metrics frame unreadable")
)

const maxFrame = 4096

// CPU counters are cumulative jiffies (or ticks) since boot.
type CPU struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// NIC counters are cumulative bytes since boot.
type NIC struct {
	Name   string
	RX, TX uint64
}

// Vol is one filesystem, in kibibytes.
type Vol struct {
	TotalKB, UsedKB uint64
	Mount           string
}

// GPU is one NVIDIA device. An OK flag is false when that field was "-".
type GPU struct {
	Util, MemUsed, MemTotal, Temp int64
	UtilOK, MemOK, TempOK         bool
}

// Login is one session logged in on the server, as who lists it. From
// is empty for a local login; When is the server's own text for its
// start (who prints no year).
type Login struct {
	User, Line, From, When string
}

// GPUProc is one process holding GPU memory. GPU is the index of its
// device among the frame's GPUs, or -1 when unknown; Name is empty when
// the driver did not name it.
type GPUProc struct {
	GPU    int
	PID    uint64
	Name   string
	MemMiB int64
	MemOK  bool
}

// A frame keeps at most this many sessions and GPU processes; the count
// lines say how many there were.
const (
	maxLogins   = 8
	maxGPUProcs = 10
)

// Sample is one metrics frame. OK flags are false when that line was absent.
type Sample struct {
	OS                   string
	OSName, Kernel       string // the release ("Ubuntu 22.04.4 LTS") and kernel, "" when unknown
	CPU                  CPU
	CPUOK                bool
	CPUPct               float64
	CPUPctOK             bool // set by a cpupct line, which is already a percent
	Cores                []CPU
	MemTotal, MemAvail   uint64
	SwapTotal, SwapFree  uint64
	MemOK                bool
	Load1, Load5, Load15 float64
	LoadOK               bool
	NICs                 []NIC
	Vols                 []Vol
	GPUs                 []GPU
	GPUProcs             []GPUProc
	GPUProcCount         int
	GPUProcsOK           bool // set by a gprocs or gproc line
	Logins               []Login
	LoginCount           int
	LoginsOK             bool // set by a logins or login line
}

// Core is one core's busy percent since the previous sample.
type Core struct {
	Pct float64
	OK  bool
}

// NICRate is one interface's bytes per second since the previous sample.
type NICRate struct {
	Name     string
	Up, Down float64
	OK       bool
}

// VolUse is one filesystem. Pct may be over 100 when used exceeds total.
type VolUse struct {
	Mount           string
	TotalKB, UsedKB uint64
	Pct             float64
}

// Reading is what the bar shows. Tick-based CPU and network stay unknown
// until a second sample. A cpupct line is already a percent. Memory, disk,
// load and GPU come from the latest sample.
type Reading struct {
	OS                      string
	OSName, Kernel          string
	CPU                     float64
	CPUOK                   bool
	Cores                   []Core
	MemUsedKB, MemTotalKB   uint64
	MemPct                  float64
	MemOK                   bool
	SwapUsedKB, SwapTotalKB uint64
	SwapPct                 float64
	SwapOK                  bool
	Vols                    []VolUse
	Load1, Load5, Load15    float64
	LoadOK                  bool
	NetUp, NetDown          float64 // bytes per second, non-loopback
	NetOK                   bool
	NICs                    []NICRate
	GPUs                    []GPU
	GPUProcs                []GPUProc
	GPUProcCount            int
	GPUProcsOK              bool
	Logins                  []Login
	LoginCount              int
	LoginsOK                bool
}

// MetricsInterval returns n when it is 1, 2, 5 or 10, and 2 otherwise.
func MetricsInterval(n int) int {
	switch n {
	case 1, 2, 5, 10:
		return n
	default:
		return 2
	}
}

// MetricsMountOK reports whether mount may be passed to the remote
// script. Empty is fine. Anything else must be an absolute path with no
// shell metacharacters; the command quotes it as well.
func MetricsMountOK(mount string) bool {
	if mount == "" {
		return true
	}
	if len(mount) > 256 || mount[0] != '/' {
		return false
	}
	for _, r := range mount {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '\'', '"', '\\', '$', ';', '&', '|', '<', '>', '(', ')', '{', '}', '[', ']', '*', '?', '!', '~', '#':
			return false
		}
	}
	return true
}

// shellQuote is s as one sh word, a single quote in it included.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// MetricsCommand starts sh reading the script from stdin, with the interval
// and mount as arguments. Keeping the script out of exec requests avoids
// Dropbear's command-string limit. Both arguments are re-checked here.
func MetricsCommand(interval int, mount string) string {
	interval = MetricsInterval(interval)
	if !MetricsMountOK(mount) {
		mount = ""
	}
	return "sh -s -- " + strconv.Itoa(interval) + " " + shellQuote(mount)
}

// ParseFrame parses one frame body (without the <<<SSHYARD / >>> markers).
func ParseFrame(body string) (Sample, error) {
	body = strings.ReplaceAll(body, "\r", "")
	if strings.TrimSpace(body) == "" {
		return Sample{}, ErrGarbage
	}
	var s Sample
	saw := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, rest, _ := strings.Cut(line, " ")
		switch key {
		case "os":
			if rest == "" || strings.ContainsAny(rest, " \t") {
				return Sample{}, ErrGarbage
			}
			s.OS = rest
		case "cpu":
			c, err := parseCPU(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			s.CPU, s.CPUOK = c, true
		case "cpupct":
			p, err := parsePct(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			s.CPUPct, s.CPUPctOK = p, true
		case "core":
			c, err := parseCPU(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			s.Cores = append(s.Cores, c)
		case "mem":
			f, err := parseU64s(rest, 4)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			s.MemTotal, s.MemAvail, s.SwapTotal, s.SwapFree = f[0], f[1], f[2], f[3]
			s.MemOK = true
		case "load":
			a, b, c, err := parseLoad(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			s.Load1, s.Load5, s.Load15, s.LoadOK = a, b, c, true
		case "nic":
			n, err := parseNIC(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			putNIC(&s, n)
		case "vol":
			v, err := parseVol(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			putVol(&s, v)
		case "gpu":
			g, err := parseGPU(rest)
			if err != nil {
				return Sample{}, ErrGarbage
			}
			s.GPUs = append(s.GPUs, g)
		// The lines below only describe the host. One that does not
		// read is dropped on its own: it costs the frame nothing else.
		case "osname":
			if v := cleanText(rest, 64); v != "" {
				s.OSName = v
			}
		case "kernel":
			if v := cleanText(rest, 64); v != "" {
				s.Kernel = v
			}
		case "logins":
			if n, ok := parseCount(rest); ok {
				s.LoginCount, s.LoginsOK = n, true
			}
		case "login":
			if l, ok := parseLogin(rest); ok && len(s.Logins) < maxLogins {
				s.Logins = append(s.Logins, l)
				s.LoginsOK = true
			}
		case "gprocs":
			if n, ok := parseCount(rest); ok {
				s.GPUProcCount, s.GPUProcsOK = n, true
			}
		case "gproc":
			if p, ok := parseGPUProc(rest); ok && len(s.GPUProcs) < maxGPUProcs {
				s.GPUProcs = append(s.GPUProcs, p)
				s.GPUProcsOK = true
			}
		default:
			return Sample{}, ErrGarbage
		}
		saw = true
	}
	if !saw {
		return Sample{}, ErrGarbage
	}
	s.LoginCount = max(s.LoginCount, len(s.Logins))
	s.GPUProcCount = max(s.GPUProcCount, len(s.GPUProcs))
	return s, nil
}

// cleanText is remote text fit to show: valid UTF-8 without control or
// format characters (bidi overrides, zero widths), each run of spaces
// one space, and at most n runes.
func cleanText(s string, n int) string {
	var b strings.Builder
	runes, space := 0, false
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case unicode.IsSpace(r):
			space = runes > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if space {
			if runes+2 > n {
				break
			}
			b.WriteByte(' ')
			runes++
			space = false
		}
		if runes >= n {
			break
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

// parseCount is a count line: one number, at most 1<<16.
func parseCount(rest string) (int, bool) {
	f := strings.Fields(rest)
	if len(f) != 1 {
		return 0, false
	}
	n, err := strconv.ParseUint(f[0], 10, 32)
	if err != nil {
		return 0, false
	}
	return int(min(n, 1<<16)), true
}

// parseLogin is "user line from when…", from "-" for a local login.
func parseLogin(rest string) (Login, bool) {
	f := strings.Fields(rest)
	if len(f) < 3 {
		return Login{}, false
	}
	l := Login{User: cleanText(f[0], 32), Line: cleanText(f[1], 32), When: cleanText(strings.Join(f[3:], " "), 32)}
	if f[2] != "-" {
		l.From = cleanText(f[2], 64)
	}
	if l.User == "" || l.Line == "" {
		return Login{}, false
	}
	return l, true
}

// parseGPUProc is "gpu pid mem name…": gpu and mem may be "-", and name
// "-" is unnamed.
func parseGPUProc(rest string) (GPUProc, bool) {
	f := strings.Fields(rest)
	if len(f) < 4 {
		return GPUProc{}, false
	}
	p := GPUProc{GPU: -1}
	if f[0] != "-" {
		i, err := strconv.Atoi(f[0])
		if err != nil || i < 0 || i >= 64 {
			return GPUProc{}, false
		}
		p.GPU = i
	}
	pid, err := strconv.ParseUint(f[1], 10, 32)
	if err != nil || pid == 0 {
		return GPUProc{}, false
	}
	p.PID = pid
	mem, ok, err := parseOpt(f[2])
	if err != nil || mem < 0 {
		return GPUProc{}, false
	}
	p.MemMiB, p.MemOK = mem, ok
	if name := strings.Join(f[3:], " "); name != "-" {
		p.Name = cleanText(name, 64)
	}
	return p, true
}

func parsePct(rest string) (float64, error) {
	f := strings.Fields(rest)
	if len(f) != 1 {
		return 0, ErrGarbage
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, ErrGarbage
	}
	if v > 100 {
		v = 100
	}
	return v, nil
}

func parseCPU(rest string) (CPU, error) {
	f, err := parseU64s(rest, 8)
	if err != nil {
		return CPU{}, err
	}
	return CPU{f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7]}, nil
}

func parseU64s(rest string, n int) ([]uint64, error) {
	f := strings.Fields(rest)
	if len(f) != n {
		return nil, ErrGarbage
	}
	out := make([]uint64, n)
	for i, s := range f {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, ErrGarbage
		}
		out[i] = v
	}
	return out, nil
}

func parseLoad(rest string) (float64, float64, float64, error) {
	f := strings.Fields(rest)
	if len(f) != 3 {
		return 0, 0, 0, ErrGarbage
	}
	out := [3]float64{}
	for i, s := range f {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, 0, ErrGarbage
		}
		out[i] = v
	}
	return out[0], out[1], out[2], nil
}

func parseNIC(rest string) (NIC, error) {
	f := strings.Fields(rest)
	if len(f) != 3 || f[0] == "" {
		return NIC{}, ErrGarbage
	}
	rx, err1 := strconv.ParseUint(f[1], 10, 64)
	tx, err2 := strconv.ParseUint(f[2], 10, 64)
	if err1 != nil || err2 != nil {
		return NIC{}, ErrGarbage
	}
	return NIC{Name: f[0], RX: rx, TX: tx}, nil
}

func parseVol(rest string) (Vol, error) {
	f := strings.Fields(rest)
	if len(f) < 3 {
		return Vol{}, ErrGarbage
	}
	total, err1 := strconv.ParseUint(f[0], 10, 64)
	used, err2 := strconv.ParseUint(f[1], 10, 64)
	if err1 != nil || err2 != nil {
		return Vol{}, ErrGarbage
	}
	return Vol{TotalKB: total, UsedKB: used, Mount: strings.Join(f[2:], " ")}, nil
}

func parseGPU(rest string) (GPU, error) {
	f := strings.Fields(rest)
	if len(f) != 4 {
		return GPU{}, ErrGarbage
	}
	var g GPU
	var err error
	if g.Util, g.UtilOK, err = parseOpt(f[0]); err != nil {
		return GPU{}, ErrGarbage
	}
	if g.MemUsed, g.MemOK, err = parseOpt(f[1]); err != nil {
		return GPU{}, ErrGarbage
	}
	var totalOK bool
	if g.MemTotal, totalOK, err = parseOpt(f[2]); err != nil {
		return GPU{}, ErrGarbage
	}
	g.MemOK = g.MemOK && totalOK
	if g.Temp, g.TempOK, err = parseOpt(f[3]); err != nil {
		return GPU{}, ErrGarbage
	}
	return g, nil
}

func parseOpt(s string) (int64, bool, error) {
	if s == "-" {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}

func putNIC(s *Sample, n NIC) {
	for i := range s.NICs {
		if s.NICs[i].Name == n.Name {
			s.NICs[i] = n
			return
		}
	}
	s.NICs = append(s.NICs, n)
}

func putVol(s *Sample, v Vol) {
	for i := range s.Vols {
		if s.Vols[i].Mount == v.Mount {
			s.Vols[i] = v
			return
		}
	}
	s.Vols = append(s.Vols, v)
}

// Decoder splits a byte stream into frames. A line "." between frames is
// a heartbeat and is ignored. Write returns every complete sample before
// the first error.
type Decoder struct {
	buf  []byte
	body []byte
	in   bool
}

// Write appends p and returns the frames that finished with this write.
func (d *Decoder) Write(p []byte) ([]Sample, error) {
	var out []Sample
	d.buf = append(d.buf, p...)
	for {
		if !d.in {
			line, ok, err := d.nextLine()
			if err != nil {
				return out, err
			}
			if !ok {
				return out, nil
			}
			switch line {
			case "", ".":
				continue
			case "<<<SSHYARD":
				d.in = true
				d.body = d.body[:0]
			default:
				return out, ErrGarbage
			}
			continue
		}
		line, ok, err := d.nextLine()
		if err != nil {
			return out, err
		}
		if !ok {
			if len(d.body)+len(d.buf) > maxFrame {
				return out, ErrTruncated
			}
			return out, nil
		}
		if line == ">>>" {
			s, perr := ParseFrame(string(d.body))
			d.body = d.body[:0]
			d.in = false
			if perr != nil {
				return out, perr
			}
			out = append(out, s)
			continue
		}
		if len(d.body)+len(line)+1 > maxFrame {
			return out, ErrTruncated
		}
		d.body = append(d.body, line...)
		d.body = append(d.body, '\n')
	}
}

// Close reports a frame that was still open, or a half-read line.
func (d *Decoder) Close() error {
	if d.in || len(strings.TrimSpace(string(d.buf))) > 0 {
		return ErrTruncated
	}
	return nil
}

// nextLine returns the next line. ok is false when the line is not
// finished yet. A line over 4 KB, or an unfinished buffer over 4 KB
// outside a frame, is truncated.
func (d *Decoder) nextLine() (string, bool, error) {
	i := strings.IndexByte(string(d.buf), '\n')
	if i < 0 {
		if !d.in && len(d.buf) > maxFrame {
			return "", false, ErrTruncated
		}
		return "", false, nil
	}
	line := strings.TrimRight(string(d.buf[:i]), "\r")
	d.buf = append([]byte(nil), d.buf[i+1:]...)
	if len(line) > maxFrame {
		return "", false, ErrTruncated
	}
	return line, true, nil
}

func cpuParts(c CPU) (total, idle uint64) {
	idle = c.Idle + c.IOWait
	total = c.User + c.Nice + c.System + idle + c.IRQ + c.SoftIRQ + c.Steal
	return total, idle
}

func cpuPct(prev, cur CPU) (float64, bool) {
	pt, pi := cpuParts(prev)
	ct, ci := cpuParts(cur)
	if ct <= pt || ci < pi {
		return 0, false
	}
	dT := ct - pt
	dI := ci - pi
	if dI > dT {
		return 0, false
	}
	return float64(dT-dI) / float64(dT) * 100, true
}

func loopback(name string) bool { return name == "lo" || name == "lo0" }

// Delta turns two samples into bar figures. dt is the time from prev to
// cur; network rates need it, CPU percent does not. A counter that goes
// backwards is a reset: that CPU is unknown, and that interface's rate
// for the interval is 0.
func Delta(prev, cur Sample, dt time.Duration) Reading {
	r := Reading{
		OS: cur.OS, OSName: cur.OSName, Kernel: cur.Kernel, GPUs: cur.GPUs,
		GPUProcs: cur.GPUProcs, GPUProcCount: cur.GPUProcCount, GPUProcsOK: cur.GPUProcsOK,
		Logins: cur.Logins, LoginCount: cur.LoginCount, LoginsOK: cur.LoginsOK,
	}
	if prev.CPUOK && cur.CPUOK {
		r.CPU, r.CPUOK = cpuPct(prev.CPU, cur.CPU)
	} else if cur.CPUPctOK {
		r.CPU, r.CPUOK = cur.CPUPct, true
	}
	if len(cur.Cores) > 0 {
		r.Cores = make([]Core, len(cur.Cores))
		for i := range cur.Cores {
			if prev.CPUOK && i < len(prev.Cores) {
				r.Cores[i].Pct, r.Cores[i].OK = cpuPct(prev.Cores[i], cur.Cores[i])
			}
		}
	}
	if cur.MemOK {
		r.MemOK, r.SwapOK = true, true
		r.MemTotalKB = cur.MemTotal
		if cur.MemTotal > cur.MemAvail {
			r.MemUsedKB = cur.MemTotal - cur.MemAvail
		}
		if cur.MemTotal > 0 {
			r.MemPct = float64(r.MemUsedKB) / float64(cur.MemTotal) * 100
		}
		r.SwapTotalKB = cur.SwapTotal
		if cur.SwapTotal > cur.SwapFree {
			r.SwapUsedKB = cur.SwapTotal - cur.SwapFree
		}
		if cur.SwapTotal > 0 {
			r.SwapPct = float64(r.SwapUsedKB) / float64(cur.SwapTotal) * 100
		}
	}
	for _, v := range cur.Vols {
		u := VolUse{Mount: v.Mount, TotalKB: v.TotalKB, UsedKB: v.UsedKB}
		if v.TotalKB > 0 {
			u.Pct = float64(v.UsedKB) / float64(v.TotalKB) * 100
		}
		r.Vols = append(r.Vols, u)
	}
	if cur.LoadOK {
		r.Load1, r.Load5, r.Load15, r.LoadOK = cur.Load1, cur.Load5, cur.Load15, true
	}
	sec := dt.Seconds()
	if sec > 0 {
		for _, n := range cur.NICs {
			if loopback(n.Name) {
				continue
			}
			rate := NICRate{Name: n.Name}
			p, ok := findNIC(prev.NICs, n.Name)
			if ok {
				rate.OK = true
				rate.Down = byteRate(n.RX, p.RX, sec)
				rate.Up = byteRate(n.TX, p.TX, sec)
				r.NetDown += rate.Down
				r.NetUp += rate.Up
				r.NetOK = true
			}
			r.NICs = append(r.NICs, rate)
		}
	}
	return r
}

func findNIC(nics []NIC, name string) (NIC, bool) {
	for _, n := range nics {
		if n.Name == name {
			return n, true
		}
	}
	return NIC{}, false
}

func byteRate(cur, prev uint64, sec float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / sec
}

// Collector runs one metrics command on a client it does not own and does
// not dial. One host's panes share one Collector. A failure sticks to
// that client; a later Start with a different client (a reconnect) tries
// once more.
type Collector struct {
	gate sync.Mutex
	mu   sync.Mutex

	client   *ssh.Client
	interval int
	mount    string
	running  bool
	failed   bool
	reason   string
	reading  Reading
	prev     Sample
	prevAt   time.Time
	havePrev bool
	cancel   context.CancelFunc
	done     chan struct{}
	gen      int
}

// NewCollector returns a collector that is not running.
func NewCollector() *Collector { return &Collector{} }

// Start runs the metrics command on client. It does nothing when client
// is nil, when this client already failed, or when the same command is
// already running. A new client replaces the previous run. Start does not
// wait: the new command waits, on its own goroutine, until the previous
// one has left its channel, and a command cancelled before that opens nothing.
func (c *Collector) Start(client *ssh.Client, interval int, mount string) {
	if client == nil {
		return
	}
	interval = MetricsInterval(interval)
	if !MetricsMountOK(mount) {
		mount = ""
	}
	c.gate.Lock()
	defer c.gate.Unlock()

	c.mu.Lock()
	same := c.client == client
	if same && c.failed {
		c.interval, c.mount = interval, mount
		c.mu.Unlock()
		return
	}
	if same && c.running && c.interval == interval && c.mount == mount {
		c.mu.Unlock()
		return
	}
	if !same {
		c.failed = false
		c.reason = ""
		c.reading = Reading{}
		c.havePrev = false
		c.prev = Sample{}
	}
	c.client, c.interval, c.mount = client, interval, mount
	c.gen++
	gen := c.gen
	prev := c.done
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.cancel, c.done, c.running = cancel, done, true
	c.mu.Unlock()
	go c.launch(ctx, cancel, client, interval, mount, gen, prev, done)
}

// Stop cancels the command and returns without waiting for the channel.
// It does not mark the collector failed, so a later Start runs again.
func (c *Collector) Stop() {
	c.gate.Lock()
	defer c.gate.Unlock()
	c.mu.Lock()
	cancel := c.cancel
	c.running = false
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait blocks until the latest command has finished or ctx ends.
// A nil ctx waits until the command finishes.
func (c *Collector) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// View returns the latest reading. reason is a Reason constant when this
// client has failed and is empty otherwise.
func (c *Collector) View() (Reading, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return Reading{}, c.reason
	}
	return c.reading, ""
}

func (c *Collector) fail(gen int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || c.failed {
		return
	}
	c.failed = true
	c.reason = reason
	c.running = false
	c.reading = Reading{}
	c.havePrev = false
	c.prev = Sample{}
}

func (c *Collector) failedNow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

func (c *Collector) observe(gen int, s Sample) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || c.failed {
		return
	}
	if c.havePrev {
		c.reading = Delta(c.prev, s, now.Sub(c.prevAt))
	} else {
		c.reading = Delta(Sample{}, s, 0)
	}
	c.prev, c.prevAt, c.havePrev = s, now, true
}

type frameWriter struct {
	c      *Collector
	cancel context.CancelFunc
	dec    Decoder
	gen    int
}

func (w *frameWriter) Write(p []byte) (int, error) {
	samples, err := w.dec.Write(p)
	for _, s := range samples {
		w.c.observe(w.gen, s)
	}
	if err != nil {
		// Returning the error would deadlock the SSH copy if it
		// then failed the write that reports it. Cancel instead.
		w.c.fail(w.gen, ReasonParse)
		w.cancel()
	}
	return len(p), nil
}

// launch waits for the previous command to leave its channel, then runs
// this one unless it was cancelled first.
func (c *Collector) launch(ctx context.Context, cancel context.CancelFunc, client *ssh.Client, interval int, mount string, gen int, prev, done chan struct{}) {
	defer func() {
		c.mu.Lock()
		if c.gen == gen {
			c.running = false
			c.cancel = nil
		}
		c.mu.Unlock()
		close(done)
	}()
	if prev != nil {
		<-prev
	}
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	ok := c.gen == gen && !c.failed
	c.mu.Unlock()
	if !ok {
		return
	}
	fw := &frameWriter{c: c, cancel: cancel, gen: gen}
	err := execStarted(ctx, client, MetricsCommand(interval, mount), execStreams{
		stdin: strings.NewReader(metricsScript), stdout: fw,
	}, nil)
	if c.failedNow() || ctx.Err() != nil {
		return
	}
	if cerr := fw.dec.Close(); cerr != nil {
		c.fail(gen, ReasonParse)
		return
	}
	c.fail(gen, execReason(err))
}

func execReason(err error) string {
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		if exit.ExitStatus() == 127 {
			return ReasonShell
		}
		return ReasonParse
	}
	if err == nil {
		return ReasonParse
	}
	return ReasonExec
}

// shortcut: frames keep at most 64 cores, 32 interfaces, 8 GPUs, 10 GPU processes and 8 login sessions so they stay under 4 KB; raise those caps for a bigger host.
const metricsScript = `
interval=$1
mount=$2
case $interval in
1|2|5|10) ;;
*) interval=2 ;;
esac
# bash writes a here-document to a temp file; keep every sample in the shell.
set -f
nl=$(printf "\nx")
nl=${nl%x}
uname_s=$(uname -s 2>/dev/null || echo other)
case $uname_s in
Linux) kind=linux ;;
Darwin) kind=darwin ;;
*BSD|DragonFly) kind=bsd ;;
*) kind=other ;;
esac
has_gpu=0
command -v nvidia-smi >/dev/null 2>&1 && has_gpu=1
has_who=0
command -v who >/dev/null 2>&1 && has_who=1
sq=$(printf "\047")
gpu_cache=
gproc_cache=
gproc_total=
frame_n=0
# Sessions change rarely: list them about every ten seconds.
login_every=$((10 / interval))
login_n=0
login_cache=
login_total=
os_name=
os_kernel=
sent=0
body=

add() {
  n=${#body}
  if [ "$n" -ge 3500 ]; then
    return 0
  fi
  body="$body$1
"
}

num_ok() {
  case $1 in
  ""|*[!0-9]*) return 1 ;;
  esac
  return 0
}

to_kb() {
  v=$1
  unit=${v##*[0-9.]}
  num=${v%$unit}
  num=${num%%.*}
  num_ok "$num" || num=0
  case $unit in
  G|g) printf "%s" $((num * 1024 * 1024)) ;;
  M|m) printf "%s" $((num * 1024)) ;;
  K|k) printf "%s" "$num" ;;
  *) printf "%s" $((num / 1024)) ;;
  esac
}

# trim_v and clip_v change v in place, without a subshell.
trim_v() {
  while :; do
    case $v in
    " "*) v=${v# } ;;
    *" ") v=${v% } ;;
    *) break ;;
    esac
  done
}

clip_v() {
  while [ "${#v}" -gt "$1" ]; do
    v=${v%?}
  done
}

unquote_v() {
  case $v in
  \"*\") v=${v#\"}; v=${v%\"} ;;
  "$sq"*"$sq") v=${v#"$sq"}; v=${v%"$sq"} ;;
  esac
}

# add_lines adds each line of $1.
add_lines() {
  _ifs=$IFS
  IFS=$nl
  for line in $1; do
    IFS=$_ifs
    [ -n "$line" ] || continue
    add "$line"
  done
  IFS=$_ifs
}

# probe_os reads the release and the kernel once: they do not change
# while the script runs.
probe_os() {
  os_kernel=$(uname -r 2>/dev/null) || os_kernel=
  case $kind in
  linux)
    for f in /etc/os-release /usr/lib/os-release; do
      [ -r "$f" ] || continue
      pretty=
      name=
      ver=
      while IFS= read -r line || [ -n "$line" ]; do
        case $line in
        PRETTY_NAME=*) v=${line#*=}; unquote_v; pretty=$v ;;
        NAME=*) v=${line#*=}; unquote_v; name=$v ;;
        VERSION_ID=*) v=${line#*=}; unquote_v; ver=$v ;;
        esac
      done < "$f"
      v=$pretty
      [ -n "$v" ] || v="$name $ver"
      trim_v
      os_name=$v
      break
    done
    ;;
  darwin)
    name=$(sw_vers -productName 2>/dev/null) || name=
    ver=$(sw_vers -productVersion 2>/dev/null) || ver=
    v="$name $ver"
    trim_v
    os_name=$v
    ;;
  *)
    if [ -n "$os_kernel" ]; then
      os_name="$uname_s $os_kernel"
    fi
    ;;
  esac
  v=$os_name
  clip_v 64
  os_name=$v
  v=$os_kernel
  clip_v 64
  os_kernel=$v
}

gpu_field() {
  v=$1
  while :; do
    case $v in
    " "*) v=${v# } ;;
    *" ") v=${v% } ;;
    *) break ;;
    esac
  done
  case $v in
  ""|*[!0-9]*) printf "%s" "-" ;;
  *) printf "%s" "$v" ;;
  esac
}

read_stat() {
  [ -r /proc/stat ] || return 0
  cores=0
  while IFS= read -r line; do
    set -- $line
    case $1 in
    cpu)
      add "cpu ${2:-0} ${3:-0} ${4:-0} ${5:-0} ${6:-0} ${7:-0} ${8:-0} ${9:-0}"
      ;;
    cpu[0-9]*)
      if [ "$cores" -ge 64 ]; then
        continue
      fi
      cores=$((cores + 1))
      add "core ${2:-0} ${3:-0} ${4:-0} ${5:-0} ${6:-0} ${7:-0} ${8:-0} ${9:-0}"
      ;;
    *)
      break
      ;;
    esac
  done < /proc/stat
}

read_mem() {
  [ -r /proc/meminfo ] || return 0
  total=0
  avail=0
  free_kb=0
  buffers=0
  cached=0
  swapt=0
  swapf=0
  have_avail=0
  while IFS= read -r line; do
    set -- $line
    case $1 in
    MemTotal:) total=$2 ;;
    MemAvailable:) avail=$2; have_avail=1 ;;
    MemFree:) free_kb=$2 ;;
    Buffers:) buffers=$2 ;;
    Cached:) cached=$2 ;;
    SwapTotal:) swapt=$2 ;;
    SwapFree:) swapf=$2 ;;
    esac
  done < /proc/meminfo
  num_ok "$total" || return 0
  [ "$total" -gt 0 ] || return 0
  if [ "$have_avail" -eq 0 ]; then
    num_ok "$free_kb" || free_kb=0
    num_ok "$buffers" || buffers=0
    num_ok "$cached" || cached=0
    avail=$((free_kb + buffers + cached))
    if [ "$avail" -gt "$total" ]; then
      avail=$total
    fi
  fi
  num_ok "$avail" || avail=0
  num_ok "$swapt" || swapt=0
  num_ok "$swapf" || swapf=0
  add "mem $total $avail $swapt $swapf"
}

read_load() {
  [ -r /proc/loadavg ] || return 0
  IFS= read -r line < /proc/loadavg || return 0
  set -- $line
  [ "$#" -ge 3 ] || return 0
  add "load $1 $2 $3"
}

read_net() {
  [ -r /proc/net/dev ] || return 0
  nics=0
  while IFS= read -r line; do
    set -- $line
    name=$1
    case $name in
    *:*) ;;
    *) continue ;;
    esac
    name=${name%:}
    case $name in
    lo|lo0|"") continue ;;
    esac
    if [ "$#" -lt 10 ]; then
      continue
    fi
    if [ "$nics" -ge 32 ]; then
      break
    fi
    rx=$2
    tx=${10}
    num_ok "$rx" || continue
    num_ok "$tx" || continue
    nics=$((nics + 1))
    add "nic $name $rx $tx"
  done < /proc/net/dev
}

df_path() {
  p=$1
  [ -n "$p" ] || return 0
  data=$(df -P -k "$p" 2>/dev/null) || data=$(df -k "$p" 2>/dev/null) || return 0
  last=
  _ifs=$IFS
  IFS=$nl
  for line in $data; do
    IFS=$_ifs
    [ -n "$line" ] || continue
    last=$line
  done
  IFS=$_ifs
  [ -n "$last" ] || return 0
  set -- $last
  num_ok "$2" || return 0
  num_ok "$3" || return 0
  add "vol $2 $3 $p"
}

read_cpu_bsd() {
  t=$(sysctl -n kern.cp_time 2>/dev/null) || t=
  if [ -z "$t" ]; then
    read_cpu_darwin
    return 0
  fi
  set -- $t
  [ "$#" -ge 4 ] || return 0
  add "cpu $1 $2 $3 $4 0 0 0 0"
  ct=$(sysctl -n kern.cp_times 2>/dev/null) || return 0
  set -- $ct
  cores=0
  while [ "$#" -ge 5 ]; do
    if [ "$cores" -ge 64 ]; then
      break
    fi
    cores=$((cores + 1))
    add "core $1 $2 $3 $5 0 $4 0 0"
    shift 5
  done
}

# Darwin has no cumulative CPU ticks. ps %cpu is one snapshot (a decaying
# average), so the frame stays inside the interval. iostat and top each
# wait a second. The sum is percent of one CPU; divide by logical CPUs.
# Per-core figures stay absent.
read_cpu_darwin() {
  ncpu=$(sysctl -n hw.logicalcpu 2>/dev/null) || return 0
  num_ok "$ncpu" || return 0
  [ "$ncpu" -gt 0 ] || return 0
  data=$(LC_ALL=C ps -A -o %cpu= 2>/dev/null) || return 0
  sum=0
  n=0
  _ifs=$IFS
  IFS=$nl
  for line in $data; do
    IFS=$_ifs
    v=$line
    while :; do
      case $v in
      " "*) v=${v# } ;;
      *" ") v=${v% } ;;
      *) break ;;
      esac
    done
    [ -n "$v" ] || continue
    case $v in
    *.*)
      whole=${v%%.*}
      frac=${v#*.}
      ;;
    *)
      whole=$v
      frac=0
      ;;
    esac
    num_ok "$whole" || continue
    one=${frac%${frac#?}}
    num_ok "$one" || one=0
    sum=$((sum + whole * 10 + one))
    n=$((n + 1))
  done
  IFS=$_ifs
  [ "$n" -gt 0 ] || return 0
  tenths=$((sum / ncpu))
  pct=$(((tenths + 5) / 10))
  if [ "$pct" -gt 100 ]; then
    pct=100
  fi
  add "cpupct $pct"
}

read_mem_bsd() {
  bytes=$(sysctl -n hw.memsize 2>/dev/null) || bytes=0
  num_ok "$bytes" || bytes=0
  total=$((bytes / 1024))
  [ "$total" -gt 0 ] || return 0
  page=4096
  active_p=0
  spec_p=0
  wired_p=0
  got_pages=0
  data=$(vm_stat 2>/dev/null) || data=
  _ifs=$IFS
  IFS=$nl
  for line in $data; do
    IFS=$_ifs
    case $line in
    *"page size of "*)
      rest=${line#*page size of }
      set -- $rest
      num_ok "$1" && page=$1
      ;;
    esac
    set -- $line
    case $2 in
    active:) num_ok "${3%.}" && active_p=${3%.} && got_pages=1 ;;
    speculative:) num_ok "${3%.}" && spec_p=${3%.} && got_pages=1 ;;
    wired) num_ok "${4%.}" && wired_p=${4%.} && got_pages=1 ;;
    esac
  done
  IFS=$_ifs
  [ "$got_pages" -eq 1 ] || return 0
  used_pages=$((active_p + wired_p + spec_p))
  used_kb=$((used_pages * page / 1024))
  if [ "$used_kb" -lt "$total" ]; then
    avail=$((total - used_kb))
  else
    avail=0
  fi
  swapt=0
  swapf=0
  swap=$(sysctl -n vm.swapusage 2>/dev/null) || swap=
  if [ -n "$swap" ]; then
    stripped=
    rest=$swap
    while [ -n "$rest" ]; do
      case $rest in
      *"="*)
        stripped="$stripped${rest%%=*}"
        rest=${rest#*=}
        ;;
      *)
        stripped="$stripped$rest"
        rest=
        ;;
      esac
    done
    set -- $stripped
    if [ "$1" = "total" ]; then
      swapt=$(to_kb "$2")
      swapu=$(to_kb "$4")
      if [ "$swapt" -ge "$swapu" ]; then
        swapf=$((swapt - swapu))
      fi
    fi
  fi
  add "mem $total $avail $swapt $swapf"
}

read_load_bsd() {
  load=$(sysctl -n vm.loadavg 2>/dev/null) || return 0
  set -- $load
  [ "$#" -ge 4 ] || return 0
  add "load $2 $3 $4"
}

read_net_bsd() {
  data=$(netstat -ib 2>/dev/null) || return 0
  nics=0
  _ifs=$IFS
  IFS=$nl
  for line in $data; do
    IFS=$_ifs
    case $line in
    *"<Link"*) ;;
    *) continue ;;
    esac
    set -- $line
    name=$1
    case $name in
    lo|lo0|"") continue ;;
    esac
    if [ "$nics" -ge 32 ]; then
      break
    fi
    case $4 in
    ""|*[!0-9]*)
      ib=$7
      ob=${10}
      ;;
    *)
      ib=$6
      ob=$9
      ;;
    esac
    num_ok "$ib" || continue
    num_ok "$ob" || continue
    nics=$((nics + 1))
    add "nic $name $ib $ob"
  done
  IFS=$_ifs
}

# scan_gprocs lists the processes holding GPU memory, at most 10, each
# with the index of its GPU in gpu_cache, found by its UUID.
scan_gprocs() {
  gproc_total=
  gproc_cache=
  [ -n "$gpu_cache" ] || return 0
  data=$(nvidia-smi --query-compute-apps=pid,used_memory,gpu_uuid,process_name --format=csv,noheader,nounits 2>/dev/null) || return 0
  ids=" "
  i=0
  _ifs=$IFS
  IFS=$nl
  for line in $gpu_cache; do
    IFS=$_ifs
    [ -n "$line" ] || continue
    IFS=,
    set -- $line
    IFS=$_ifs
    v=$5
    trim_v
    case $v in
    ""|*[!A-Za-z0-9-]*) ;;
    *) ids="$ids$v=$i " ;;
    esac
    i=$((i + 1))
  done
  seen=0
  IFS=$nl
  for line in $data; do
    IFS=,
    set -- $line
    if [ "$#" -lt 4 ]; then
      IFS=$_ifs
      continue
    fi
    p=$1
    mu=$2
    id=$3
    shift 3
    # A name with a comma in it was split; join it back.
    name="$*"
    IFS=$_ifs
    v=$p
    trim_v
    num_ok "$v" || continue
    p=$v
    v=$mu
    trim_v
    num_ok "$v" || v=-
    mu=$v
    v=$id
    trim_v
    idx=-
    case $v in
    ""|*[!A-Za-z0-9-]*) ;;
    *)
      case $ids in
      *" $v="*)
        idx=${ids#* $v=}
        idx=${idx%% *}
        ;;
      esac
      ;;
    esac
    seen=$((seen + 1))
    if [ "$seen" -gt 10 ]; then
      continue
    fi
    v=$name
    trim_v
    v=${v##*/}
    clip_v 48
    [ -n "$v" ] || v=-
    gproc_cache="${gproc_cache}gproc $idx $p $mu $v$nl"
  done
  IFS=$_ifs
  gproc_total=$seen
}

read_gpu() {
  [ "$has_gpu" -eq 1 ] || return 0
  frame_n=$((frame_n + 1))
  if [ "$((frame_n % 3))" -eq 1 ]; then
    gpu_cache=$(nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,uuid --format=csv,noheader,nounits 2>/dev/null) || gpu_cache=
    scan_gprocs
  fi
  [ -n "$gpu_cache" ] || return 0
  gpus=0
  _ifs=$IFS
  IFS=$nl
  for line in $gpu_cache; do
    IFS=$_ifs
    [ -n "$line" ] || continue
    if [ "$gpus" -ge 8 ]; then
      break
    fi
    old_ifs=$IFS
    IFS=,
    set -- $line
    IFS=$old_ifs
    u=$(gpu_field "$1")
    mu=$(gpu_field "$2")
    mt=$(gpu_field "$3")
    tp=$(gpu_field "$4")
    gpus=$((gpus + 1))
    add "gpu $u $mu $mt $tp"
  done
  IFS=$_ifs
  [ -n "$gproc_total" ] || return 0
  add "gprocs $gproc_total"
  add_lines "$gproc_cache"
}

# scan_logins lists who is logged in, at most 8 of them: user, line,
# where from ("-" for a local login) and since when, as who prints it.
scan_logins() {
  login_total=
  login_cache=
  [ "$has_who" -eq 1 ] || return 0
  data=$(LC_ALL=C who 2>/dev/null) || return 0
  seen=0
  _ifs=$IFS
  IFS=$nl
  for line in $data; do
    IFS=$_ifs
    set -- $line
    if [ "$#" -lt 2 ] || [ "$1" = USER ]; then
      continue
    fi
    seen=$((seen + 1))
    if [ "$seen" -gt 8 ]; then
      continue
    fi
    user=$1
    tty=$2
    shift 2
    when=
    from=-
    while [ "$#" -gt 0 ]; do
      case $1 in
      \(*)
        v="$*"
        v=${v#\(}
        v=${v%\)}
        set -- $v
        from=${1:--}
        break
        ;;
      esac
      when="$when $1"
      shift
    done
    v=$user
    clip_v 32
    user=$v
    v=$tty
    clip_v 32
    tty=$v
    v=$from
    clip_v 64
    from=$v
    v=${when# }
    clip_v 32
    when=$v
    login_cache="${login_cache}login $user $tty $from $when$nl"
  done
  IFS=$_ifs
  login_total=$seen
}

read_logins() {
  if [ "$((login_n % login_every))" -eq 0 ]; then
    scan_logins
  fi
  login_n=$((login_n + 1))
  [ -n "$login_total" ] || return 0
  add "logins $login_total"
  add_lines "$login_cache"
}

emit_disks() {
  df_path /
  if [ -n "$mount" ] && [ "$mount" != "/" ]; then
    df_path "$mount"
  fi
}

emit() {
  body=
  add "os $kind"
  if [ -n "$os_name" ]; then
    add "osname $os_name"
  fi
  if [ -n "$os_kernel" ]; then
    add "kernel $os_kernel"
  fi
  case $kind in
  linux)
    read_stat
    read_mem
    read_load
    read_net
    emit_disks
    read_gpu
    ;;
  darwin|bsd)
    read_cpu_bsd
    read_mem_bsd
    read_load_bsd
    read_net_bsd
    emit_disks
    read_gpu
    ;;
  *)
    emit_disks
    ;;
  esac
  read_logins
  printf "%s\n" "<<<SSHYARD" || exit
  printf "%s" "$body" || exit
  printf "%s\n" ">>>" || exit
}

probe_os
while :; do
  start=$(date +%s 2>/dev/null || echo 0)
  num_ok "$start" || start=0
  emit
  # Always wait a real second after the first frame: emit may cross an
  # epoch-second boundary without taking a full second. Later frames keep
  # the interval counted from before emit, including the time of a slow sample.
  gap=$interval
  if [ "$sent" -eq 0 ]; then
    gap=1
  fi
  sent=$((sent + 1))
  step=0
  while [ "$step" -lt "$gap" ]; do
    if [ "$sent" -gt 1 ] && [ "$start" -ne 0 ]; then
      now=$(date +%s 2>/dev/null || echo 0)
      num_ok "$now" || now=0
      if [ "$now" -ge "$((start + gap))" ]; then
        break
      fi
    fi
    printf ".\n" || exit
    sleep 1
    step=$((step + 1))
  done
done
`
