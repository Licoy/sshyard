package policy

import "strings"

// Only verified option forms may retain the automatic read-only grant, since
// familiar reader programs also have execution, output and configuration flags.
func verifiedReadArgs(name string, args []arg) bool {
	if name == "echo" || name == "printf" || anyArgsReader(name) {
		return true
	}
	if name == "ps" {
		ok, _ := processArgs(args)
		return ok
	}
	if name == "getconf" {
		if len(args) != 2 || !args[1].ok || args[1].text == "" {
			return false
		}
		variable := args[1].text
		const letters = "_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		return strings.ContainsRune(letters, rune(variable[0])) && strings.Trim(variable, letters+"0123456789") == ""
	}
	if name == "nproc" {
		return verifiedNprocArgs(args)
	}
	operands, _, ok := readOperands(name, args)
	if !ok {
		return false
	}
	for _, a := range operands {
		if !a.ok {
			continue
		}
		t := a.text
		if (name == "less" || name == "more") && strings.HasPrefix(t, "+") {
			return false
		}
		if name == "date" && !strings.HasPrefix(t, "+") {
			return false
		}
		// sysctl name=value sets a kernel parameter.
		if name == "sysctl" && strings.Contains(t, "=") {
			return false
		}
	}
	// uniq's second operand is an output file; hostname's operand changes
	// it, and swapon's turns on swap.
	return !(name == "uniq" && len(operands) > 1 || (name == "hostname" || name == "swapon") && len(operands) > 0)
}

// anyArgsReader is a reporting tool with no option that writes, runs a
// program or prints file content, so every argument form stays read-only.
func anyArgsReader(name string) bool {
	switch name {
	case "vmstat", "iostat", "mpstat", "lscpu", "lsblk", "lsmem", "lspci", "lsusb",
		"w", "who", "last", "lastb", "lsof", "tr", "seq", "basename", "dirname",
		"readlink", "realpath", "type", "md5sum", "sha1sum", "sha256sum", "sha512sum", "cksum":
		return true
	default:
		return false
	}
}

// readOperands checks a reader's arguments against the options it is known
// to accept; ok is false for any other option. operands are the arguments
// that are not options, dynamic ones included, and seen holds each option
// given: short letters and long names.
func readOperands(name string, args []arg) (operands []arg, seen map[string]bool, ok bool) {
	seen = map[string]bool{}
	short, values := "", ""
	long := "help version"
	// counts accepts -NUM, as in head -20 or journalctl -b -1.
	counts := false
	switch name {
	case "ls":
		short = "aAbBcCdDfFgGhHiIklLmMnNopqQrRsStTuUvwxX1"
		long += " all almost-all directory human-readable long reverse recursive"
	case "cat":
		short = "AbEnstTuv"
	case "head", "tail":
		short, values, counts = "qvz", "nc", true
		long += " lines= bytes= quiet silent verbose zero-terminated"
	case "grep", "egrep", "fgrep", "rg":
		short, values = "EFGivwxonHhclLqsrRzZUabIP", "efmABC"
		long += " fixed-strings ignore-case line-number files-with-matches count word-regexp line-regexp recursive no-heading hidden no-ignore" +
			" extended-regexp perl-regexp invert-match only-matching no-filename with-filename text regexp= max-count= after-context= before-context= context="
	case "sort":
		short, values = "bdfgMhnRrsuVzmcC", "okStT"
		long += " reverse unique numeric-sort stable output= key= field-separator= human-numeric-sort general-numeric-sort ignore-case month-sort version-sort ignore-leading-blanks dictionary-order"
	case "uniq":
		short, values = "cduiz", "fsw"
		long += " count repeated unique ignore-case skip-fields= skip-chars= check-chars="
	case "wc":
		short = "clmwL"
		long += " lines words bytes chars max-line-length"
	case "cut":
		short, values = "snz", "bcdf"
		long += " complement only-delimited zero-terminated bytes= characters= delimiter= fields= output-delimiter="
	case "tac":
		short, values = "br", "s"
		long += " before regex separator="
	case "nl":
		short, values = "p", "bdfhilnsvw"
	case "column":
		short, values = "tnJexLd", "csoNRHWEOl"
	case "less", "more":
		short = "EFGIMNQRSTUVWXceginqrsuw"
	case "df":
		short = "ahHiklmPstxT"
	case "du":
		short = "ahHiklmPstx"
	case "free":
		short = "bhkmgwt"
	case "uname":
		short = "asnrvmpio"
	case "top":
		short, values = "bHicS1", "ndpowE"
	case "journalctl":
		short, values, counts = "brxeqfkaml", "nupoSUgtD", true
		long += " no-pager dmesg reverse utc all merge full no-full quiet catalog pager-end list-boots no-hostname boot" +
			" since= until= unit= user-unit= priority= output= lines= identifier= grep= facility="
	case "dmesg":
		short, values = "TxHkudLtPrSe", "lfs"
		long += " ctime decode human kernel userspace nopager notime raw reltime show-delta syslog level= facility= since= until= time-format="
	case "sysctl":
		short, values = "anNbeqAX", "r"
		long += " all values names binary ignore quiet pattern="
	case "pidstat":
		short, values = "dhIlrRstuvwH", "pCGT"
	case "swapon":
		short = "s"
		long += " show show= summary noheadings raw bytes"
	case "ss", "netstat":
		short = "alntupxro46"
	case "stat", "file":
		short = "Lbih"
	case "id":
		short = "Gnruzg"
	case "pwd":
		short = "LP"
	case "date":
		short = "uR"
	case "printenv":
		short = "0"
	}
	options := true
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			operands = append(operands, args[i])
			continue
		}
		t := args[i].text
		if options && t == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(t, "--") {
			key, _, attached := strings.Cut(t[2:], "=")
			seen[key] = true
			// A name listed both bare and with = takes an optional value.
			plain := strings.Contains(" "+long+" ", " "+key+" ")
			if strings.Contains(" "+long+" ", " "+key+"= ") && (attached || !plain) {
				if !attached {
					i++
					if i >= len(args) {
						return nil, nil, false
					}
				}
				continue
			}
			if attached || !strings.Contains(" "+long+" ", " "+key+" ") {
				return nil, nil, false
			}
			continue
		}
		if options && strings.HasPrefix(t, "-") && t != "-" {
			if counts && isDigits(t[1:]) {
				continue
			}
			for j, c := range t[1:] {
				seen[string(c)] = true
				if strings.ContainsRune(values, c) {
					if j+2 == len(t) {
						i++
						if i >= len(args) {
							return nil, nil, false
						}
					}
					break
				}
				if !strings.ContainsRune(short, c) {
					return nil, nil, false
				}
			}
			continue
		}
		operands = append(operands, args[i])
	}
	return operands, seen, true
}

// contentReader prints what is inside the files it is given.
func contentReader(name string) bool {
	switch name {
	case "cat", "head", "tail", "less", "more", "grep", "rg", "egrep", "fgrep",
		"sort", "uniq", "wc", "file", "cut", "tac", "nl", "column":
		return true
	default:
		return false
	}
}

// readsUntrusted reports whether a content reader opens a file that is not
// a trusted read; standard input is not a file.
func (e *shellEnv) readsUntrusted(name string, args []arg) bool {
	operands, seen, ok := readOperands(name, args)
	if !ok {
		return true
	}
	if name == "grep" || name == "egrep" || name == "fgrep" || name == "rg" {
		// Patterns from a file are that file's content.
		if seen["f"] || seen["file"] {
			return true
		}
		// Without -e the first operand is the pattern.
		if !seen["e"] && !seen["regexp"] && len(operands) > 0 {
			operands = operands[1:]
		}
	}
	for _, a := range operands {
		if a.ok && a.text == "-" {
			continue
		}
		if !a.ok || !e.trustedRead(a.text) {
			return true
		}
	}
	return false
}

// trustedRoots hold system files only root can place links in, so a
// literal path there cannot lead a shell to a secret the gate did not see.
// Writable places such as /tmp and home still ask, as do globs and braces.
var trustedRoots = []string{"/etc", "/var/log", "/sys", "/usr", "/lib", "/www/server", "/www/wwwlogs"}

// trustedRead reports whether a shell command may read raw's content
// without asking: a literal, non-sensitive path under trustedRoots, or a
// /proc entry that cannot reach another process's files or memory.
func (e *shellEnv) trustedRead(raw string) bool {
	if e.local || raw == "" || strings.ContainsAny(raw, "*?[]{}\\") {
		return false
	}
	abs, ok := e.resolve(raw)
	if !ok || e.sensitive(raw) {
		return false
	}
	abs = toSlash(abs)
	if strings.HasPrefix(abs, "/proc/") {
		return procRead(abs)
	}
	for _, root := range trustedRoots {
		if withinPOSIX(root, abs) {
			return true
		}
	}
	return false
}

// procFiles are the per-process /proc entries a read may open: counters and
// state, never the environment, command line, memory or links to files.
var procFiles = map[string]bool{
	"status": true, "stat": true, "statm": true, "limits": true, "io": true, "sched": true,
	"schedstat": true, "oom_score": true, "oom_score_adj": true, "cgroup": true,
	"smaps_rollup": true, "wchan": true, "comm": true, "loginuid": true,
}

func procRead(abs string) bool {
	parts := strings.Split(strings.TrimPrefix(abs, "/proc/"), "/")
	first := parts[0]
	if first == "self" || first == "thread-self" || isDigits(first) {
		return len(parts) == 2 && procFiles[parts[1]]
	}
	switch first {
	case "kcore", "kmsg", "kmem", "mem", "sysrq-trigger":
		return false
	}
	return true
}

func recursiveRead(name string, args []arg) bool {
	if name != "grep" && name != "egrep" && name != "fgrep" {
		return false
	}
	for _, a := range args[1:] {
		if a.ok && (a.text == "--recursive" || strings.HasPrefix(a.text, "-") && !strings.HasPrefix(a.text, "--") && strings.ContainsAny(a.text[1:], "rR")) {
			return true
		}
	}
	return false
}

func knownCommand(name string) bool {
	if readOnlyCmd(name) || isWrap(name) || isInterp(name) || awkName(name) {
		return true
	}
	switch name {
	case "rm", "rmdir", "unlink", "truncate", "shred", "mkdir", "touch", "mv", "cp", "ln", "install", "tee", "sed", "find", "chmod", "chown", "chgrp", "dd", "systemctl", "service", "docker", "git", "kubectl", "ip", "tc", "reboot", "shutdown":
		return true
	}
	return false
}

func processEnvironment(name string, args []arg) bool {
	if name != "ps" {
		return false
	}
	_, environment := processArgs(args)
	return environment
}

// processArgs distinguishes BSD's bare e (show environment) from POSIX -e
// (select all processes), and validates every format before it can be replaced.
func processArgs(args []arg) (bool, bool) {
	const flags, values = "aefjluxwyFHLTAdNMZ", "oOpCuUgGtqs"
	environment := false
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			return false, environment
		}
		t := args[i].text
		switch t {
		case "--help", "--version", "--forest", "--no-headers", "--headers":
			continue
		case "-":
			return false, environment
		}
		if !strings.HasPrefix(t, "-") {
			if t == "" || strings.Trim(t, "aefjluxw0123456789") != "" {
				return false, environment
			}
			environment = environment || strings.Contains(t, "e")
			continue
		}
		var letter rune
		value, attached := "", false
		if strings.HasPrefix(t, "--") {
			key, v, has := strings.Cut(t[2:], "=")
			switch key {
			case "sort":
				letter = 'k'
			case "format":
				letter = 'o'
			case "pid", "ppid", "user", "User", "group", "Group", "cols", "columns", "width", "tty", "sid":
				letter = 'p'
			default:
				return false, environment
			}
			value, attached = v, has
		} else {
			for j, c := range t[1:] {
				if strings.ContainsRune(values, c) {
					letter = c
					value = t[2+j:]
					attached = value != ""
					break
				}
				if !strings.ContainsRune(flags, c) {
					return false, environment
				}
			}
			if letter == 0 {
				continue
			}
		}
		if !attached {
			i++
			if i >= len(args) || !args[i].ok {
				return false, environment
			}
			value = args[i].text
		}
		if value == "" || (letter == 'o' || letter == 'O' || letter == 'k') && !processFields(value, letter == 'k') {
			return false, environment
		}
	}
	return true, environment
}

func processFields(value string, sort bool) bool {
	for _, field := range strings.Split(value, ",") {
		if sort && (strings.HasPrefix(field, "+") || strings.HasPrefix(field, "-")) {
			field = field[1:]
		}
		// Keep the field name from pid=HEADER or wchan:28.
		if !sort {
			field, _, _ = strings.Cut(field, "=")
		}
		field, _, _ = strings.Cut(field, ":")
		switch field {
		case "pid", "ppid", "pgid", "sid", "tid", "lwp", "nlwp", "user", "ruser", "euser", "uid", "ruid", "euid",
			"gid", "group", "tty", "tt", "stat", "state", "s", "pcpu", "%cpu", "c", "pmem", "%mem", "rss", "rsz",
			"vsz", "vsize", "sz", "size", "comm", "ucomm", "cmd", "args", "command", "etime", "etimes", "lstart",
			"start", "start_time", "stime", "time", "cputime", "cputimes", "bsdtime", "ni", "nice", "pri",
			"priority", "psr", "wchan", "class", "cls", "f", "flags", "maj_flt", "min_flt":
		default:
			return false
		}
	}
	return true
}

func verifiedNprocArgs(args []arg) bool {
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			return false
		}
		t := args[i].text
		if t == "--all" || t == "--help" || t == "--version" {
			continue
		}
		_, value, attached := strings.Cut(t, "=")
		if t == "--ignore" {
			i++
			if i >= len(args) || !args[i].ok {
				return false
			}
			value = args[i].text
		} else if !attached || !strings.HasPrefix(t, "--ignore=") {
			return false
		}
		if value == "" || strings.Trim(value, "0123456789") != "" {
			return false
		}
	}
	return true
}
