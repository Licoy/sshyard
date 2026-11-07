package policy

import "strings"

func isWrap(name string) bool {
	switch name {
	case "sudo", "doas", "su", "env", "xargs", "nohup", "nice", "ionice",
		"timeout", "command", "builtin", "exec", "stdbuf", "setsid",
		"chroot", "watch", "flock", "strace", "sh", "bash", "zsh", "dash",
		"ksh", "ash":
		return true
	default:
		return false
	}
}

func isInterp(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "ash",
		"python", "python2", "python3", "perl", "ruby", "php", "node":
		return true
	default:
		return false
	}
}

func (e *shellEnv) wrap(name string, args []arg) partial {
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "ash":
		return e.wrapShell(name, args)
	case "sudo":
		return e.wrapSudo(args)
	case "doas":
		return e.wrapDoas(args)
	case "su":
		return e.wrapSu(args)
	case "env":
		return e.wrapEnv(args)
	case "xargs":
		return e.wrapXargs(args)
	case "timeout":
		return e.wrapTimeout(args)
	case "nice":
		return e.wrapNice(args)
	case "ionice":
		return e.wrapIonice(args)
	case "command":
		return e.wrapCommand(args)
	case "flock":
		return e.wrapFlock(args)
	case "chroot":
		return e.wrapChroot(args)
	case "strace":
		return e.wrapStrace(args)
	case "stdbuf":
		rest, _, _, ok := stripOpts(args, "ioe", nil)
		if !ok {
			return unk("stdbuf")
		}
		return e.wrapRest("stdbuf", rest, "")
	case "watch":
		rest, _, _, ok := stripOpts(args, "n", []string{"interval"})
		if !ok {
			return unk("watch")
		}
		return e.wrapRest("watch", rest, "")
	case "nohup", "setsid", "builtin", "exec":
		short := ""
		long := []string(nil)
		if name == "exec" {
			short = "a"
			long = []string{"name"}
		}
		rest, _, _, ok := stripOpts(args, short, long)
		if !ok {
			return unk(name)
		}
		return e.wrapRest(name, rest, "")
	default:
		return e.core(name, args)
	}
}

func (e *shellEnv) wrapRest(name string, rest []arg, elev Category) partial {
	if len(rest) == 0 {
		return raise(partial{cat: "read", risk: RiskReadOnly, prefix: name, why: name}, RiskMedium, elev)
	}
	inner := e.gradeArgs(rest)
	if inner.prefix != "" {
		inner.prefix = strings.TrimSpace(name + " " + inner.prefix)
	}
	return raise(inner, RiskMedium, elev)
}

func (e *shellEnv) wrapShell(name string, args []arg) partial {
	g := partial{interp: true, prefix: name}
	script, word, found, literal := shellDashC(args)
	if !found {
		return better(g, unk("shell script"))
	}
	if !literal {
		inner := word.subst
		inner.interp = true
		inner = better(inner, unk("dynamic shell script"))
		// sh -c "$(curl …)" is the same attack as curl | sh.
		if word.cmd && word.subst.curl && strings.TrimSpace(word.text) == "" {
			inner = better(inner, crit(name, "pipe to shell"))
		}
		return inner
	}
	if strings.TrimSpace(script) == "" {
		return better(g, unk("shell -c"))
	}
	inner := e.script(script)
	inner.interp = true
	return inner
}

// shellDashC finds the operand of sh -c / bash -lc / --command.
// literal is false when that operand is a substitution.
func shellDashC(args []arg) (script string, word arg, found, literal bool) {
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			return "", args[i], false, false
		}
		t := args[i].text
		if t == "--" {
			return "", arg{}, false, false
		}
		if t == "-c" || t == "--command" {
			return takeShellScript(args, i+1)
		}
		if strings.HasPrefix(t, "--") {
			continue
		}
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			cluster := t[1:]
			for j := 0; j < len(cluster); j++ {
				if cluster[j] != 'c' {
					continue
				}
				if attached := cluster[j+1:]; attached != "" {
					return attached, args[i], true, true
				}
				return takeShellScript(args, i+1)
			}
			continue
		}
		return "", arg{}, false, false
	}
	return "", arg{}, false, false
}

func takeShellScript(args []arg, i int) (string, arg, bool, bool) {
	if i >= len(args) {
		return "", arg{}, true, false
	}
	w := args[i]
	if !w.ok || w.cmd || w.proc {
		return "", w, true, false
	}
	return w.text, w, true, true
}

func (e *shellEnv) wrapSudo(args []arg) partial {
	rest, seen, _, ok := stripOpts(args, "ugphUCDRTt", []string{"user", "group", "prompt", "host", "other-user", "close-from", "chdir", "role", "type", "command-timeout"})
	if !ok {
		return unk("sudo")
	}
	if (seen["i"] || seen["s"] || seen["login"] || seen["shell"]) && len(rest) == 0 {
		why := "sudo -i"
		if seen["s"] || seen["shell"] {
			why = "sudo -s"
		}
		return highPerm(why)
	}
	if seen["e"] || seen["edit"] {
		g := highPerm("sudo -e")
		for _, f := range rest {
			if f.ok {
				g = better(g, e.gradeWrite(f.text, writeMutate))
			}
		}
		return raise(g, RiskMedium, "permission")
	}
	rest, danger := peelLeadAssigns(rest)
	if len(rest) == 0 {
		return better(danger, highPerm("sudo"))
	}
	if shellWithoutScript(rest) {
		// interp keeps curl | sudo sh critical.
		g := highPerm("sudo " + baseName(rest[0].text))
		g.interp = true
		return better(danger, g)
	}
	return better(danger, raise(e.gradeArgs(rest), RiskMedium, "permission"))
}

func (e *shellEnv) wrapDoas(args []arg) partial {
	rest, seen, _, ok := stripOpts(args, "uC", []string{"user"})
	if !ok {
		return unk("doas")
	}
	if seen["s"] && len(rest) == 0 {
		return highPerm("doas -s")
	}
	rest, danger := peelLeadAssigns(rest)
	if len(rest) == 0 {
		return better(danger, highPerm("doas"))
	}
	if shellWithoutScript(rest) {
		g := highPerm("doas " + baseName(rest[0].text))
		g.interp = true
		return better(danger, g)
	}
	return better(danger, raise(e.gradeArgs(rest), RiskMedium, "permission"))
}

func (e *shellEnv) wrapSu(args []arg) partial {
	_, seen, vals, ok := stripOpts(args, "cs", []string{"command", "shell"})
	if !ok {
		return unk("su")
	}
	script := vals["c"]
	if script == "" {
		script = vals["command"]
	}
	if seen["c"] || seen["command"] {
		if strings.TrimSpace(script) == "" {
			return unk("su -c")
		}
		return raise(e.script(script), RiskMedium, "permission")
	}
	return highPerm("su")
}

func (e *shellEnv) wrapEnv(args []arg) partial {
	i := 1
	danger := false
	for i < len(args) {
		if !args[i].ok {
			return better(unk("env"), args[i].subst)
		}
		t := args[i].text
		if t == "--" {
			i++
			break
		}
		if isAssign(args[i]) {
			name, _, _ := strings.Cut(t, "=")
			if dangerVar(name) {
				danger = true
			}
			i++
			continue
		}
		if !strings.HasPrefix(t, "-") {
			break
		}
		name := strings.TrimLeft(t, "-")
		if before, _, ok := strings.Cut(name, "="); ok {
			name = before
		}
		switch name {
		case "u", "unset", "C", "chdir", "S", "split-string":
			if strings.Contains(t, "=") {
				i++
				continue
			}
			i += 2
			continue
		}
		i++
	}
	if i > len(args) {
		return unk("env")
	}
	var g partial
	if i >= len(args) {
		g = partial{cat: "sensitive-read", risk: RiskMedium, prefix: "env", why: "environment disclosure"}
	} else {
		g = e.wrapRest("env", args[i:], "")
	}
	// A dangerous NAME=value only matters when a command follows.
	if danger && i < len(args) {
		g = better(unk("environment"), g)
	}
	return g
}

// dangerVar is an assignment that can change which code runs.
// LANG, TZ and LC_* are not in this set.
func dangerVar(name string) bool {
	switch name {
	case "PATH", "BASH_ENV", "ENV", "IFS", "PROMPT_COMMAND", "SHELLOPTS", "PS4",
		"EDITOR", "VISUAL", "PYTHONPATH", "PYTHONSTARTUP", "PERL5OPT", "NODE_OPTIONS",
		"LESSOPEN", "LESSCLOSE", "GETCONF_DIR":
		return true
	}
	if strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "GIT_") {
		return true
	}
	return strings.HasSuffix(name, "PAGER")
}

// peelLeadAssigns drops leading NAME=value words. A dangerous name is
// Unknown, which ranks above High, and the caller takes the max.
func peelLeadAssigns(args []arg) (rest []arg, danger partial) {
	i := 0
	for i < len(args) && isAssign(args[i]) {
		name, _, _ := strings.Cut(args[i].text, "=")
		if dangerVar(name) {
			danger = unk("environment")
		}
		i++
	}
	return args[i:], danger
}

// shellWithoutScript is sh/bash/… with no -c. sudo bash is a root shell.
func shellWithoutScript(args []arg) bool {
	if len(args) == 0 || !args[0].ok {
		return false
	}
	switch baseName(args[0].text) {
	case "sh", "bash", "zsh", "dash", "ksh", "ash":
	default:
		return false
	}
	_, _, found, _ := shellDashC(args)
	return !found
}

func isAssign(a arg) bool {
	if !a.ok {
		return false
	}
	name, _, ok := strings.Cut(a.text, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 && !nameStart(r) {
			return false
		}
		if i > 0 && !nameCont(r) {
			return false
		}
	}
	return true
}

func (e *shellEnv) wrapXargs(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "ILnaEdEPs", []string{"replace", "max-lines", "max-args", "arg-file", "delimiter", "eof", "max-chars", "max-procs", "process-slot-var"})
	if !ok {
		return unk("xargs")
	}
	if len(rest) == 0 {
		return raise(partial{cat: "read", risk: RiskReadOnly, prefix: "xargs", why: "xargs echo"}, RiskMedium, "")
	}
	return e.wrapRest("xargs", rest, "")
}

func (e *shellEnv) wrapTimeout(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "ks", []string{"kill-after", "signal"})
	if !ok || len(rest) == 0 {
		return unk("timeout")
	}
	if len(rest) == 1 {
		return raise(partial{cat: "read", risk: RiskReadOnly, prefix: "timeout", why: "timeout"}, RiskMedium, "")
	}
	return e.wrapRest("timeout", rest[1:], "")
}

func (e *shellEnv) wrapNice(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "n", []string{"adjustment"})
	if !ok {
		return unk("nice")
	}
	rest = skipNiceAdjust(rest)
	if len(rest) == 0 {
		return raise(partial{cat: "read", risk: RiskReadOnly, prefix: "nice", why: "nice"}, RiskMedium, "")
	}
	return e.wrapRest("nice", rest, "")
}

func skipNiceAdjust(args []arg) []arg {
	if len(args) == 0 || !args[0].ok {
		return args
	}
	t := args[0].text
	if len(t) > 1 && (t[0] == '-' || t[0] == '+') && isDigits(t[1:]) {
		return args[1:]
	}
	return args
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (e *shellEnv) wrapIonice(args []arg) partial {
	rest, seen, _, ok := stripOpts(args, "cnp", []string{"class", "classdata", "pid"})
	if !ok {
		return unk("ionice")
	}
	if len(rest) == 0 {
		why := "ionice"
		if seen["p"] || seen["pid"] {
			why = "ionice -p"
		}
		return partial{cat: "service", risk: RiskMedium, prefix: "ionice", why: why}
	}
	return e.wrapRest("ionice", rest, "")
}

func (e *shellEnv) wrapCommand(args []arg) partial {
	rest, seen, _, ok := stripOpts(args, "", nil)
	if !ok {
		return unk("command")
	}
	if (seen["v"] || seen["V"]) && len(rest) == 0 {
		// command -v was consumed as a bool flag, so the name is in rest
		// only when -v does not take the operand. Re-parse: -v is bool and
		// the command name stays positional. Lookup is read-only.
	}
	if seen["v"] || seen["V"] {
		return partial{cat: "read", risk: RiskReadOnly, prefix: "command -v", why: "command -v"}
	}
	return e.wrapRest("command", rest, "")
}

func (e *shellEnv) wrapFlock(args []arg) partial {
	rest, seen, vals, ok := stripOpts(args, "wcE", []string{"timeout"})
	if !ok {
		return unk("flock")
	}
	if seen["c"] {
		script := vals["c"]
		if strings.TrimSpace(script) == "" {
			return unk("flock -c")
		}
		return raise(e.script(script), RiskMedium, "")
	}
	// flock [opts] FILE COMMAND...
	if len(rest) >= 2 {
		return e.wrapRest("flock", rest[1:], "")
	}
	return partial{cat: "service", risk: RiskMedium, prefix: "flock", why: "flock"}
}

func (e *shellEnv) wrapChroot(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "", []string{"userspec", "groups"})
	if !ok || len(rest) == 0 {
		return unk("chroot")
	}
	if len(rest) == 1 {
		return unk("chroot")
	}
	return e.wrapRest("chroot", rest[1:], "")
}

func (e *shellEnv) wrapStrace(args []arg) partial {
	rest, _, vals, ok := stripOpts(args, "oepsuaPDI", []string{"output", "trace", "attach", "user", "string-limit", "env", "pid"})
	if !ok {
		return unk("strace")
	}
	g := partial{}
	if vals["o"] != "" {
		g = e.gradeWrite(vals["o"], writeRedirect)
	}
	if vals["output"] != "" {
		g = better(g, e.gradeWrite(vals["output"], writeRedirect))
	}
	if len(rest) == 0 {
		return better(g, partial{cat: "service", risk: RiskMedium, prefix: "strace", why: "strace"})
	}
	return better(g, e.wrapRest("strace", rest, ""))
}

func (e *shellEnv) core(name string, args []arg) partial {
	if helpOnly(args) && knownCommand(name) {
		return partial{cat: "read", risk: RiskReadOnly, prefix: name + " --help", why: "help"}
	}
	if isInterp(name) {
		if interpVersion(name, args) {
			return partial{cat: "read", risk: RiskReadOnly, prefix: name + " " + args[1].text, why: "version"}
		}
		// python/perl/php/node (shells are wrappers).
		g := unk("interpreter " + name)
		g.interp = true
		g.prefix = name
		return g
	}
	switch name {
	case "curl", "wget":
		return e.gradeCurl(name, args)
	case "ssh", "scp", "sftp", "telnet", "nc", "netcat", "rsync":
		return e.gradeRemoteTool(name, args)
	case "rm":
		return e.gradeRM(args)
	case "rmdir", "unlink", "truncate", "shred":
		return e.gradeDelete(name, args)
	case "mkdir", "touch":
		return e.gradeCreate(name, args)
	case "mv", "cp", "ln", "install":
		return e.gradeMutate(name, args)
	case "tee":
		return e.gradeTee(args)
	case "sed":
		return e.gradeSed(args)
	case "awk", "gawk", "mawk", "nawk":
		return e.gradeAwk(name, args)
	case "nginx", "apachectl", "apache2ctl", "httpd":
		return gradeWebServer(name, args)
	case "atop":
		return e.gradeAtop(args)
	case "find":
		return e.gradeFind(args)
	case "chmod":
		return e.gradeChmod(args)
	case "chown", "chgrp":
		return e.gradeChown(name, args)
	case "dd":
		return e.gradeDD(args)
	case "systemctl":
		return e.gradeSystemctl(args)
	case "service":
		return e.gradeService(args)
	case "docker":
		return e.gradeDocker(args)
	case "git":
		return e.gradeGit(args)
	case "kubectl":
		return e.gradeKubectl(args)
	case "ip":
		return e.gradeIP(args)
	case "tc":
		return e.gradeTC(args)
	case "sudoedit", "visudo":
		return highPerm(name)
	default:
		if strings.HasPrefix(name, "mkfs") || name == "mke2fs" || name == "mkswap" || name == "mkdosfs" {
			return crit(name, "mkfs")
		}
		if strings.HasPrefix(name, "php-fpm") {
			return gradePHPFPM(name, args)
		}
		if g, ok := e.gradePkg(name, args); ok {
			return g
		}
		if g, ok := e.gradeSimple(name, args); ok {
			return g
		}
		return partial{cat: "unknown", risk: RiskUnknown, prefix: name, why: "unlisted command: " + name}
	}
}

func helpOnly(args []arg) bool {
	if len(args) < 2 {
		return false
	}
	for _, a := range args[1:] {
		if !a.ok || (a.text != "--help" && a.text != "--version") {
			return false
		}
	}
	return true
}

func (e *shellEnv) gradeCurl(name string, args []arg) partial {
	// A plain GET to stdout is Medium "network" (conservative: the bytes
	// leave the host). -o / -O saves a file, so the category is a write and
	// the risk stays at least Medium — a destination under /tmp is not a
	// low-risk write. A disk or auth-path destination still raises further.
	g := partial{cat: "network", risk: RiskMedium, prefix: name, why: "download", curl: true}
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			continue
		}
		t := args[i].text
		dest := ""
		dir := false
		switch {
		case t == "-o" || t == "--output" || (name == "wget" && (t == "-O" || t == "--output-document")):
			if i+1 < len(args) && args[i+1].ok {
				dest = args[i+1].text
			}
			i++
		case name == "wget" && (t == "-P" || t == "--directory-prefix"):
			// curl -P is the FTP port, not a directory.
			if i+1 < len(args) && args[i+1].ok {
				dest = args[i+1].text
			}
			dir = true
			i++
		case name == "curl" && t == "--output-dir":
			if i+1 < len(args) && args[i+1].ok {
				dest = args[i+1].text
			}
			dir = true
			i++
		case strings.HasPrefix(t, "--output="), strings.HasPrefix(t, "--output-document="):
			dest = t[strings.Index(t, "=")+1:]
		case name == "wget" && strings.HasPrefix(t, "--directory-prefix="):
			dest = strings.TrimPrefix(t, "--directory-prefix=")
			dir = true
		case name == "curl" && strings.HasPrefix(t, "--output-dir="):
			dest = strings.TrimPrefix(t, "--output-dir=")
			dir = true
		case name == "curl" && (t == "-O" || t == "--remote-name"):
			dest = e.dir
			if dest == "" {
				dest = e.scope.Home
			}
		}
		if dest == "" || dest == "-" {
			continue
		}
		if dir {
			dest = strings.TrimRight(dest, "/") + "/f"
		}
		g = mergeDownload(g, e.gradeWrite(dest, writeRedirect))
	}
	return g
}

// mergeDownload keeps the network floor and the curl flag. A file
// destination is category "write" at least Medium. Critical, permission
// and unknown destinations replace that.
func mergeDownload(g, w partial) partial {
	g.curl = true
	if w.cat == "" {
		return g
	}
	if Rank(w.risk) > Rank(RiskMedium) || w.cat == "critical" || w.cat == "permission" || w.cat == "unknown" {
		w.curl = true
		out := better(g, w)
		out.curl = true
		return out
	}
	if Rank(g.risk) > Rank(RiskMedium) || g.cat == "critical" || g.cat == "permission" {
		return g
	}
	g.cat = "write"
	g.risk = RiskMedium
	if w.prefix != "" {
		g.prefix = w.prefix
	}
	if w.why != "" {
		g.why = w.why
	}
	return g
}

func (e *shellEnv) gradeRemoteTool(name string, args []arg) partial {
	g := partial{cat: "network", risk: RiskHigh, prefix: name, why: "other host"}
	if name == "ssh" {
		rest, _, _, ok := stripOpts(args, "oibpLJlWRcD", []string{"option", "identity", "bind", "port", "login", "jump", "log", "proxy", "config", "work-dir"})
		if ok && len(rest) >= 2 && rest[0].ok {
			inner := e.gradeArgs(rest[1:])
			g = better(g, inner)
		}
	}
	for _, a := range args[1:] {
		if a.ok && (e.isDisk(a.text) || diskGlob(a.text)) {
			g = better(g, crit(name, "disk device"))
		}
	}
	if name == "rsync" && rsyncDelete(args) {
		for _, a := range args[1:] {
			if !a.ok || strings.HasPrefix(a.text, "-") {
				continue
			}
			if hitsTop(e, a.text) {
				g = better(g, crit("rsync", "rsync --delete"))
			}
		}
	}
	return g
}

func rsyncDelete(args []arg) bool {
	for _, a := range args[1:] {
		if !a.ok {
			continue
		}
		t := a.text
		if t == "--delete" || t == "--del" || strings.HasPrefix(t, "--delete-") {
			return true
		}
	}
	return false
}

func (e *shellEnv) gradeRM(args []arg) partial {
	recursive := false
	end := false
	var targets []arg
	for _, a := range args[1:] {
		if !a.ok {
			targets = append(targets, a)
			continue
		}
		t := a.text
		if !end && t == "--" {
			end = true
			continue
		}
		if !end && strings.HasPrefix(t, "-") && t != "-" {
			if t == "--recursive" || strings.Contains(t, "recursive") && strings.HasPrefix(t, "--") {
				recursive = true
			}
			if !strings.HasPrefix(t, "--") {
				for _, c := range t[1:] {
					if c == 'r' || c == 'R' {
						recursive = true
					}
				}
			}
			continue
		}
		targets = append(targets, a)
	}
	g := partial{cat: "delete", risk: RiskHigh, prefix: "rm", why: "rm"}
	for _, t := range targets {
		if !t.ok {
			g = better(g, unk("rm target"))
			continue
		}
		if e.isDisk(t.text) {
			g = better(g, crit("rm", "disk device"))
			continue
		}
		if recursive && (criticalRaw(t.text) || e.criticalPath(t.text)) {
			g = better(g, crit("rm", "rm -r "+t.text))
		}
	}
	return g
}

func (e *shellEnv) gradeDelete(name string, args []arg) partial {
	g := partial{cat: "delete", risk: RiskHigh, prefix: name, why: name}
	for _, a := range nonFlags(args) {
		if !a.ok {
			g = better(g, unk(name+" target"))
			continue
		}
		if criticalRaw(a.text) || e.isDisk(a.text) || (name != "rmdir" && e.criticalPath(a.text) && a.text == "/") {
			g = better(g, crit(name, name+" "+a.text))
		}
	}
	return g
}

func (e *shellEnv) gradeCreate(name string, args []arg) partial {
	targets := nonFlags(args)
	if len(targets) == 0 {
		return unk(name)
	}
	var gs []partial
	for _, t := range targets {
		if !t.ok {
			gs = append(gs, unk(name+" target"))
			continue
		}
		gs = append(gs, e.gradeWrite(t.text, writeCreate))
	}
	g := fold(gs)
	g.prefix = name
	return g
}

func (e *shellEnv) gradeMutate(name string, args []arg) partial {
	rest, seen, vals, ok := stripOpts(args, "tS", []string{"target-directory", "suffix"})
	if !ok {
		return unk(name)
	}
	dest := ""
	var sources []arg
	if vals["t"] != "" {
		dest = vals["t"]
		sources = rest
	} else if vals["target-directory"] != "" {
		dest = vals["target-directory"]
		sources = rest
	} else if len(rest) > 0 {
		if rest[len(rest)-1].ok {
			dest = rest[len(rest)-1].text
		}
		sources = rest[:len(rest)-1]
	}
	g := partial{cat: "write", risk: RiskMedium, prefix: name, why: name}
	if dest == "" {
		return better(g, unk(name+" dest"))
	}
	if !restOK(rest) {
		g = better(g, unk(name+" dest"))
	}
	g = better(g, e.gradeWrite(dest, writeMutate))
	if name == "cp" {
		for _, s := range sources {
			if s.ok && e.sensitive(s.text) {
				g = better(g, sens(s.text))
			}
		}
	}
	g.prefix = name
	if Rank(g.risk) < Rank(RiskMedium) && g.cat != "critical" && g.cat != "permission" && g.cat != "unknown" {
		g.risk = RiskMedium
		if g.cat == "low-write" || g.cat == "read" {
			g.cat = "write"
		}
	}
	// mv of a critical top, and recursive cp of one, is the same class as rm -rf.
	recursive := seen["r"] || seen["R"] || seen["recursive"] || seen["a"] || seen["archive"]
	if name == "mv" || (name == "cp" && recursive) {
		if hitsTop(e, dest) {
			g = better(g, crit(name, name+" "+dest))
		}
		for _, s := range sources {
			if s.ok && hitsTop(e, s.text) {
				g = better(g, crit(name, name+" "+s.text))
			}
		}
	}
	return g
}

func hitsTop(e *shellEnv, raw string) bool {
	return raw != "" && (criticalRaw(raw) || e.criticalPath(raw))
}

func restOK(args []arg) bool {
	for _, a := range args {
		if !a.ok {
			return false
		}
	}
	return true
}

func (e *shellEnv) gradeTee(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "", nil)
	if !ok {
		return unk("tee")
	}
	if len(rest) == 0 {
		return partial{cat: "read", risk: RiskReadOnly, prefix: "tee", why: "tee"}
	}
	g := partial{cat: "read", risk: RiskReadOnly, prefix: "tee", why: "tee"}
	for _, a := range rest {
		if !a.ok {
			g = better(g, unk("tee target"))
			continue
		}
		g = better(g, e.gradeWrite(a.text, writeRedirect))
	}
	g.prefix = "tee"
	return g
}

func (e *shellEnv) gradeSed(args []arg) partial {
	inPlace := false
	sawScript := false
	var scripts []string
	var files []arg
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			return sedUnk("sed argument")
		}
		t := args[i].text
		switch {
		case t == "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case t == "-e" || t == "--expression":
			if i+1 >= len(args) || !args[i+1].ok {
				return sedUnk("sed -e")
			}
			scripts = append(scripts, args[i+1].text)
			sawScript = true
			i++
		case strings.HasPrefix(t, "--expression="):
			scripts = append(scripts, strings.TrimPrefix(t, "--expression="))
			sawScript = true
		case strings.HasPrefix(t, "-e") && len(t) > 2:
			scripts = append(scripts, t[2:])
			sawScript = true
		case t == "-f" || t == "--file" || strings.HasPrefix(t, "--file=") || (strings.HasPrefix(t, "-f") && len(t) > 2):
			return sedUnk("sed -f")
		case t == "-i" || t == "--in-place" || strings.HasPrefix(t, "--in-place=") || strings.HasPrefix(t, "-i"):
			inPlace = true
		case strings.HasPrefix(t, "-"):
		default:
			if !sawScript {
				scripts = append(scripts, t)
				sawScript = true
				continue
			}
			files = append(files, args[i])
		}
	}
	g := partial{cat: "read", risk: RiskReadOnly, prefix: "sed", why: "sed"}
	if len(scripts) == 0 {
		g = better(g, sedUnk("sed"))
	}
	for _, sc := range scripts {
		exec, outs, bad := scanSed(sc)
		if bad || exec {
			g = better(g, sedUnk("sed script"))
			continue
		}
		for _, out := range outs {
			if strings.TrimSpace(out) == "" {
				g = better(g, sedUnk("sed write"))
				continue
			}
			g = better(g, e.gradeWrite(out, writeRedirect))
		}
	}
	if inPlace {
		g = better(g, partial{cat: "write", risk: RiskMedium, prefix: "sed -i", why: "sed -i"})
		if len(files) > 0 {
			target := files[len(files)-1]
			if !target.ok {
				g = better(g, sedUnk("sed target"))
			} else {
				g = better(g, e.gradeWrite(target.text, writeMutate))
			}
		}
	} else {
		untrusted := false
		for _, f := range files {
			if f.ok && e.sensitive(f.text) {
				g = better(g, sens(f.text))
			} else if f.text != "-" && !e.trustedRead(f.text) {
				untrusted = true
			}
		}
		// A w write already asks, as it did before content reads were checked.
		if untrusted && g.risk == RiskReadOnly {
			g = better(g, partial{cat: "sensitive-read", risk: RiskMedium, why: "unresolved file content read"})
		}
	}
	if g.risk == RiskCritical {
		return g
	}
	if inPlace && g.risk != RiskUnknown {
		g.prefix = "sed -i"
	} else {
		g.prefix = "sed"
	}
	return g
}

func sedUnk(why string) partial {
	u := unk(why)
	u.prefix = "sed"
	return u
}

// scanSed walks one sed script. exec is the e command or the s///e flag.
// files are w/W targets. bad means the script was not understood.
func scanSed(s string) (exec bool, files []string, bad bool) {
	i := 0
	for i < len(s) {
		i = sedSpace(s, i)
		if i >= len(s) {
			break
		}
		if s[i] == '#' {
			i = sedEOL(s, i)
			continue
		}
		if s[i] == ';' || s[i] == '\n' || s[i] == '}' {
			i++
			continue
		}
		var ok bool
		i, ok = skipSedAddr(s, i)
		if !ok {
			return false, nil, true
		}
		i = sedSpace(s, i)
		if i < len(s) && s[i] == '!' {
			i++
			i = sedSpace(s, i)
		}
		if i >= len(s) {
			return false, nil, true
		}
		cmd := s[i]
		i++
		switch cmd {
		case '{':
		case 'e':
			exec = true
			i = sedEOL(s, i)
		case 'w', 'W':
			i = sedSpace(s, i)
			start := i
			i = sedStop(s, i)
			files = append(files, strings.TrimSpace(s[start:i]))
		case 's', 'y':
			var ex bool
			var ws []string
			i, ex, ws, ok = sedSubst(s, i, cmd)
			if !ok {
				return false, nil, true
			}
			if ex {
				exec = true
			}
			files = append(files, ws...)
		case 'a', 'c', 'i', 'r', 'R', ':', 'b', 't', 'T':
			i = sedEOL(s, i)
		default:
			if !strings.ContainsRune("dDnNpPhHgGxlqQz=", rune(cmd)) {
				return false, nil, true
			}
		}
	}
	return exec, files, false
}

func sedSubst(s string, i int, cmd byte) (int, bool, []string, bool) {
	if i >= len(s) || s[i] == '\\' || s[i] == '\n' {
		return i, false, nil, false
	}
	delim := s[i]
	i++
	for parts := 2; parts > 0; parts-- {
		for {
			if i >= len(s) || s[i] == '\n' {
				return i, false, nil, false
			}
			if s[i] == '\\' {
				if i+1 >= len(s) {
					return i, false, nil, false
				}
				i += 2
				continue
			}
			if s[i] == delim {
				i++
				break
			}
			i++
		}
	}
	if cmd == 'y' {
		return i, false, nil, true
	}
	var exec bool
	var files []string
	for i < len(s) && s[i] != '\n' && s[i] != ';' && s[i] != '}' {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case (c >= '0' && c <= '9') || strings.ContainsRune("gpiImM", rune(c)):
			i++
		case c == 'e':
			exec = true
			i++
		case c == 'w' || c == 'W':
			i++
			i = sedSpace(s, i)
			start := i
			i = sedStop(s, i)
			files = append(files, strings.TrimSpace(s[start:i]))
		default:
			return i, false, nil, false
		}
	}
	return i, exec, files, true
}

func skipSedAddr(s string, i int) (int, bool) {
	j, ok := oneSedAddr(s, i)
	if !ok {
		return j, false
	}
	if j < len(s) && s[j] == ',' {
		k, ok := oneSedAddr(s, j+1)
		if !ok || k == j+1 {
			return k, false
		}
		return k, true
	}
	return j, true
}

func oneSedAddr(s string, i int) (int, bool) {
	if i >= len(s) {
		return i, false
	}
	switch s[i] {
	case '/', '\\':
		return skipSedRegex(s, i)
	case '$':
		return i + 1, true
	}
	if s[i] < '0' || s[i] > '9' {
		return i, true
	}
	j := i + 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j < len(s) && (s[j] == '~' || s[j] == '+') {
		j++
		if j >= len(s) || s[j] < '0' || s[j] > '9' {
			return j, false
		}
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
	}
	return j, true
}

func skipSedRegex(s string, i int) (int, bool) {
	if i >= len(s) {
		return i, false
	}
	delim := byte('/')
	if s[i] == '\\' {
		if i+1 >= len(s) {
			return i, false
		}
		delim = s[i+1]
		i += 2
	} else {
		i++
	}
	for i < len(s) {
		if s[i] == '\\' {
			if i+1 >= len(s) {
				return i, false
			}
			i += 2
			continue
		}
		if s[i] == delim {
			return i + 1, true
		}
		if s[i] == '\n' {
			return i, false
		}
		i++
	}
	return i, false
}

func sedSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func sedEOL(s string, i int) int {
	for i < len(s) && s[i] != '\n' {
		i++
	}
	return i
}

func sedStop(s string, i int) int {
	for i < len(s) && s[i] != '\n' && s[i] != ';' && s[i] != '}' {
		i++
	}
	return i
}

func (e *shellEnv) gradeFind(args []arg) partial {
	g := partial{cat: "read", risk: RiskReadOnly, prefix: "find", why: "find"}
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			g = better(g, unk("find argument"))
			continue
		}
		t := args[i].text
		switch t {
		case "-delete":
			g = better(g, partial{cat: "delete", risk: RiskHigh, prefix: "find -delete", why: "find -delete"})
		case "-exec", "-execdir", "-ok", "-okdir":
			cmd, next := takeExec(args, i+1)
			i = next
			if len(cmd) == 0 {
				g = better(g, unk("find -exec"))
			} else {
				g = better(g, e.gradeArgs(cmd))
			}
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			if i+1 < len(args) && args[i+1].ok {
				g = better(g, e.gradeWrite(args[i+1].text, writeRedirect))
				i++
			}
		default:
			if pathLike(t) && e.sensitive(t) {
				g = better(g, sens(t))
			}
		}
	}
	return g
}

func takeExec(args []arg, i int) ([]arg, int) {
	var cmd []arg
	for i < len(args) {
		if args[i].ok && (args[i].text == ";" || args[i].text == "+") {
			return cmd, i
		}
		cmd = append(cmd, args[i])
		i++
	}
	return cmd, i - 1
}

func (e *shellEnv) gradeChmod(args []arg) partial {
	rest, seen, _, ok := stripOpts(args, "", []string{"reference"})
	if !ok {
		return better(highPerm("chmod"), unk("chmod"))
	}
	recursive := seen["R"] || seen["recursive"]
	g := highPerm("chmod")
	g.prefix = "chmod"
	if len(rest) == 0 {
		return g
	}
	mode := ""
	paths := rest
	if rest[0].ok && !pathLike(rest[0].text) && rest[0].text != "/" {
		mode = rest[0].text
		paths = rest[1:]
	}
	world := mode777(mode)
	for _, p := range paths {
		if !p.ok {
			g = better(g, unk("chmod path"))
			continue
		}
		if e.isDisk(p.text) {
			g = better(g, crit("chmod", "disk device"))
			continue
		}
		critPath := criticalRaw(p.text) || e.criticalPath(p.text)
		if recursive && critPath {
			g = better(g, crit("chmod", "chmod -R "+p.text))
			continue
		}
		if world && (p.text == "/" || p.text == "/*") {
			g = better(g, crit("chmod", "chmod 777 "+p.text))
		}
	}
	return g
}

func mode777(mode string) bool {
	switch strings.ToLower(mode) {
	case "777", "0777", "1777", "a+rwx", "ugo+rwx", "a=rwx", "ugo=rwx":
		return true
	default:
		return false
	}
}

func (e *shellEnv) gradeChown(name string, args []arg) partial {
	rest, seen, _, ok := stripOpts(args, "", []string{"from", "reference"})
	if !ok {
		return better(highPerm(name), unk(name))
	}
	recursive := seen["R"] || seen["recursive"]
	g := highPerm(name)
	g.prefix = name
	for _, p := range rest {
		if !p.ok || !pathLike(p.text) && p.text != "/" {
			continue
		}
		if e.isDisk(p.text) {
			g = better(g, crit(name, "disk device"))
			continue
		}
		if recursive && (criticalRaw(p.text) || e.criticalPath(p.text)) {
			g = better(g, crit(name, name+" -R "+p.text))
		}
	}
	return g
}

func (e *shellEnv) gradeDD(args []arg) partial {
	g := partial{cat: "write", risk: RiskMedium, prefix: "dd", why: "dd"}
	saw := false
	for _, a := range args[1:] {
		if !a.ok {
			continue
		}
		key, val, ok := strings.Cut(a.text, "=")
		if !ok {
			continue
		}
		if key == "of" || key == "if" {
			saw = true
			if e.isDisk(val) || diskDevice(val) || diskGlob(val) {
				return crit("dd", "disk device")
			}
		}
		if key == "of" {
			w := e.gradeWrite(val, writeMutate)
			if w.cat != "" && w.risk != RiskReadOnly {
				g = better(g, w)
			}
			if harmlessDevice(val) {
				// of=/dev/null still runs dd; keep medium.
				saw = true
			}
		}
		if key == "if" && e.sensitive(val) {
			g = better(g, sens(val))
		}
	}
	if !saw {
		return g
	}
	return g
}

var systemctlRead = map[string]bool{
	"status": true, "is-active": true, "is-failed": true, "is-enabled": true,
	"is-system-running": true, "list-units": true, "list-unit-files": true,
	"list-sockets": true, "list-timers": true, "list-jobs": true,
	"list-dependencies": true, "list-automounts": true, "list-paths": true,
	"list-mounts": true, "cat": true, "show": true, "get-default": true,
	"show-environment": true, "help": true, "list-machines": true,
}

var systemctlService = map[string]bool{
	"start": true, "stop": true, "restart": true, "reload": true,
	"reload-or-restart": true, "try-restart": true, "try-reload-or-restart": true,
	"enable": true, "disable": true, "mask": true, "unmask": true,
	"isolate": true, "set-default": true, "daemon-reload": true, "daemon-reexec": true,
	"kill": true, "reset-failed": true, "edit": true, "link": true, "preset": true,
	"reenable": true,
}

func (e *shellEnv) gradeSystemctl(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "ntpoHMl", []string{"type", "lines", "property", "output", "host", "machine", "state"})
	if !ok || len(rest) == 0 || !rest[0].ok {
		return unk("systemctl")
	}
	verb := rest[0].text
	switch verb {
	case "reboot", "poweroff", "halt", "kexec":
		return crit("systemctl "+verb, "systemctl "+verb)
	}
	if systemctlRead[verb] {
		g := partial{cat: "read", risk: RiskReadOnly, prefix: "systemctl " + verb, why: "systemctl " + verb}
		for _, a := range rest[1:] {
			if a.ok && pathLike(a.text) && e.sensitive(a.text) {
				g = better(g, sens(a.text))
			}
		}
		g.prefix = "systemctl " + verb
		return g
	}
	if systemctlService[verb] {
		return partial{cat: "service", risk: RiskMedium, prefix: "systemctl " + verb, why: "systemctl " + verb}
	}
	return partial{cat: "unknown", risk: RiskUnknown, prefix: "systemctl " + verb, why: "unlisted command: systemctl " + verb}
}

func (e *shellEnv) gradeService(args []arg) partial {
	verb := ""
	for _, a := range args[1:] {
		if !a.ok {
			continue
		}
		switch a.text {
		case "start", "stop", "restart", "reload", "force-reload", "condrestart", "try-restart":
			verb = a.text
		case "status":
			if verb == "" {
				verb = "status"
			}
		}
	}
	if verb == "status" {
		return partial{cat: "read", risk: RiskReadOnly, prefix: "service status", why: "service status"}
	}
	if verb == "" {
		return unk("service")
	}
	return partial{cat: "service", risk: RiskMedium, prefix: "service " + verb, why: "service " + verb}
}

var dockerRead = map[string]bool{
	"ps": true, "logs": true, "inspect": true, "images": true, "version": true,
	"info": true, "top": true, "stats": true, "diff": true, "port": true, "history": true,
}

var dockerService = map[string]bool{
	"stop": true, "rm": true, "restart": true, "kill": true, "run": true, "start": true,
}

func (e *shellEnv) gradeDocker(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "Hl", []string{"host", "log-level", "context", "config"})
	if !ok || len(rest) == 0 || !rest[0].ok {
		return unk("docker")
	}
	verb := rest[0].text
	groups := map[string]bool{"container": true, "image": true, "volume": true, "network": true, "system": true, "compose": true}
	if groups[verb] && len(rest) > 1 && rest[1].ok {
		verb = rest[1].text
		rest = rest[1:]
	}
	if dockerRead[verb] {
		return partial{cat: "read", risk: RiskReadOnly, prefix: "docker " + verb, why: "docker " + verb}
	}
	if verb == "exec" && len(rest) >= 3 {
		// docker exec [opts] CONTAINER COMMAND
		cmd, _, _, ok := stripOpts(append([]arg{{text: "exec", ok: true}}, rest[1:]...), "uew", []string{"user", "env", "workdir", "detach-keys"})
		if ok && len(cmd) >= 2 {
			inner := e.gradeArgs(cmd[1:])
			inner = raise(inner, RiskMedium, "")
			if inner.prefix == "" {
				inner.prefix = "docker exec"
			}
			return inner
		}
	}
	if dockerService[verb] {
		return partial{cat: "service", risk: RiskMedium, prefix: "docker " + verb, why: "docker " + verb}
	}
	return partial{cat: "unknown", risk: RiskUnknown, prefix: "docker " + verb, why: "unlisted command: docker " + verb}
}

func (e *shellEnv) gradeGit(args []arg) partial {
	rest, cfgs, execPath, ok := gitGlobals(args)
	if !ok || execPath || len(rest) == 0 || !rest[0].ok {
		return gitUnk()
	}
	g := e.gitVerb(rest)
	for _, c := range cfgs {
		k, bad := gitConfigGrade(c)
		if bad {
			return gitUnk()
		}
		if k.cat != "" {
			g = better(g, k)
		}
	}
	return g
}

func (e *shellEnv) gitVerb(rest []arg) partial {
	verb := rest[0].text
	switch verb {
	case "status", "log", "diff":
		g := partial{cat: "read", risk: RiskReadOnly, prefix: "git " + verb, why: "git " + verb}
		for _, a := range rest[1:] {
			if a.ok && pathLike(a.text) && e.sensitive(a.text) {
				g = better(g, sens(a.text))
			}
		}
		g = better(g, e.gitOutput(rest[1:]))
		if g.risk != RiskCritical {
			g.prefix = "git " + verb
		}
		return g
	case "format-patch":
		w := e.gitOutput(rest[1:])
		if w.cat == "" {
			return partial{cat: "unknown", risk: RiskUnknown, prefix: "git format-patch", why: "unlisted command: git format-patch"}
		}
		g := better(partial{cat: "write", risk: RiskMedium, prefix: "git format-patch", why: "git format-patch"}, w)
		if g.risk != RiskCritical {
			g.prefix = "git format-patch"
		}
		return g
	default:
		return partial{cat: "unknown", risk: RiskUnknown, prefix: "git " + verb, why: "unlisted command: git " + verb}
	}
}

func gitUnk() partial {
	u := unk("git option")
	u.prefix = "git"
	return u
}

type gitKV struct {
	key     string
	val     string
	fromEnv bool
}

// gitGlobals splits options that precede the subcommand. --exec-path can
// point at another git binary, so the whole command stays Unknown.
// -C is a directory. An option that does not parse is not ok.
func gitGlobals(args []arg) (verb []arg, cfgs []gitKV, execPath, ok bool) {
	i := 1
	for i < len(args) {
		if !args[i].ok {
			return nil, nil, false, false
		}
		t := args[i].text
		if t == "--" {
			return args[i+1:], cfgs, execPath, true
		}
		if t == "-" || !strings.HasPrefix(t, "-") {
			return args[i:], cfgs, execPath, true
		}
		if strings.HasPrefix(t, "--") {
			var next int
			var good bool
			cfgs, execPath, next, good = gitLong(args, i, cfgs, execPath)
			if !good {
				return nil, nil, false, false
			}
			i = next
			continue
		}
		var next int
		var good bool
		cfgs, next, good = gitShort(args, i, cfgs)
		if !good {
			return nil, nil, false, false
		}
		i = next
	}
	return nil, cfgs, execPath, true
}

func gitLong(args []arg, i int, cfgs []gitKV, execPath bool) ([]gitKV, bool, int, bool) {
	name, v, has := strings.Cut(strings.TrimPrefix(args[i].text, "--"), "=")
	switch name {
	case "exec-path":
		return cfgs, true, i + 1, true
	case "config-env":
		raw, next, good := gitOptVal(args, i, has, v)
		if !good {
			return nil, false, 0, false
		}
		key, val, parsed := splitGitKV(raw)
		if !parsed {
			return nil, false, 0, false
		}
		return append(cfgs, gitKV{key: key, val: val, fromEnv: true}), execPath, next, true
	case "git-dir", "work-tree", "namespace", "super-prefix":
		_, next, good := gitOptVal(args, i, has, v)
		if !good {
			return nil, false, 0, false
		}
		return cfgs, execPath, next, true
	case "bare", "no-pager", "paginate", "literal-pathspecs", "glob-pathspecs",
		"noglob-pathspecs", "icase-pathspecs", "no-replace-objects", "version", "help":
		if has {
			return nil, false, 0, false
		}
		return cfgs, execPath, i + 1, true
	default:
		return nil, false, 0, false
	}
}

func gitShort(args []arg, i int, cfgs []gitKV) ([]gitKV, int, bool) {
	cluster := args[i].text[1:]
	j := 0
	for j < len(cluster) {
		switch cluster[j] {
		case 'C':
			if cluster[j+1:] != "" {
				return cfgs, i + 1, true
			}
			if i+1 >= len(args) || !args[i+1].ok {
				return nil, 0, false
			}
			return cfgs, i + 2, true
		case 'c':
			raw := cluster[j+1:]
			next := i + 1
			if raw == "" {
				if i+1 >= len(args) || !args[i+1].ok {
					return nil, 0, false
				}
				raw = args[i+1].text
				next = i + 2
			}
			key, val, parsed := splitGitKV(raw)
			if !parsed {
				return nil, 0, false
			}
			return append(cfgs, gitKV{key: key, val: val}), next, true
		case 'p':
			j++
		default:
			return nil, 0, false
		}
	}
	return cfgs, i + 1, true
}

func gitOptVal(args []arg, i int, has bool, v string) (string, int, bool) {
	if has {
		return v, i + 1, true
	}
	if i+1 >= len(args) || !args[i+1].ok {
		return "", 0, false
	}
	return args[i+1].text, i + 2, true
}

func splitGitKV(raw string) (key, val string, ok bool) {
	key, val, ok = strings.Cut(raw, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return "", "", false
	}
	return key, val, true
}

func gitConfigGrade(c gitKV) (partial, bool) {
	key := strings.ToLower(strings.TrimSpace(c.key))
	net := partial{cat: "network", risk: RiskMedium, prefix: "git -c", why: "git config " + key}
	if key == "http.proxy" || (strings.HasPrefix(key, "http.") && strings.HasSuffix(key, ".proxy")) {
		return net, false
	}
	if strings.HasPrefix(key, "protocol.") && strings.HasSuffix(key, ".allow") {
		// --config-env hides the value, so "always" cannot be checked.
		if c.fromEnv {
			return gitUnk(), false
		}
		if strings.EqualFold(strings.TrimSpace(c.val), "always") {
			return net, false
		}
		return partial{}, false
	}
	if gitExecKey(key) {
		return partial{cat: "exec-config", risk: RiskHigh, prefix: "git -c", why: "git config " + key}, false
	}
	return partial{}, false
}

func gitExecKey(key string) bool {
	switch key {
	case "core.sshcommand", "core.pager", "core.editor", "core.fsmonitor",
		"core.hookspath", "core.askpass", "core.gitproxy", "sequence.editor",
		"diff.external", "credential.helper", "uploadpack.packobjectshook",
		"include.path":
		return true
	}
	if strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path") {
		return true
	}
	if strings.HasPrefix(key, "pager.") || strings.HasPrefix(key, "alias.") {
		return true
	}
	if strings.HasPrefix(key, "diff.") && (strings.HasSuffix(key, ".textconv") || strings.HasSuffix(key, ".command")) {
		return true
	}
	if strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".smudge") || strings.HasSuffix(key, ".process")) {
		return true
	}
	if strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") {
		return true
	}
	if strings.HasPrefix(key, "credential.") && strings.HasSuffix(key, ".helper") {
		return true
	}
	if strings.HasPrefix(key, "gpg.") && strings.HasSuffix(key, ".program") {
		return true
	}
	for _, suf := range []string{"command", "program", "helper", "editor", "hooks", "hook"} {
		if strings.HasSuffix(key, suf) {
			return true
		}
	}
	return false
}

func (e *shellEnv) gitOutput(args []arg) partial {
	var g partial
	for i := 0; i < len(args); i++ {
		if !args[i].ok {
			continue
		}
		t := args[i].text
		switch {
		case t == "-o" || t == "--output":
			if i+1 < len(args) && args[i+1].ok {
				g = better(g, e.gradeWrite(args[i+1].text, writeRedirect))
			}
		case strings.HasPrefix(t, "--output="):
			g = better(g, e.gradeWrite(strings.TrimPrefix(t, "--output="), writeRedirect))
		case strings.HasPrefix(t, "-o") && !strings.HasPrefix(t, "--") && len(t) > 2:
			g = better(g, e.gradeWrite(t[2:], writeRedirect))
		}
	}
	return g
}

func (e *shellEnv) gradeKubectl(args []arg) partial {
	rest, _, vals, ok := stripOpts(args, "no", []string{"namespace", "context", "kubeconfig", "cluster", "user", "output"})
	if !ok || len(rest) == 0 || !rest[0].ok {
		return unk("kubectl")
	}
	g := partial{}
	if vals["kubeconfig"] != "" && e.sensitive(vals["kubeconfig"]) {
		g = sens(vals["kubeconfig"])
	}
	verb := rest[0].text
	switch verb {
	case "get", "describe", "logs":
		r := partial{cat: "read", risk: RiskReadOnly, prefix: "kubectl " + verb, why: "kubectl " + verb}
		return better(r, g)
	default:
		u := partial{cat: "unknown", risk: RiskUnknown, prefix: "kubectl " + verb, why: "unlisted command: kubectl " + verb}
		return better(u, g)
	}
}

func (e *shellEnv) gradeIP(args []arg) partial {
	rest, _, _, ok := stripOpts(args, "", nil)
	if !ok || len(rest) == 0 || !rest[0].ok {
		return unk("ip")
	}
	obj := ipObject(rest[0].text)
	act := ""
	if len(rest) > 1 && rest[1].ok && !strings.HasPrefix(rest[1].text, "-") {
		act = rest[1].text
	}
	readAct := act == "" || act == "show" || act == "list" || act == "help" || act == "get"
	switch obj {
	case "addr", "route", "link":
		if readAct && obj != "link" || (obj == "link" && readAct) {
			// mutations below
		}
		mutating := false
		switch obj {
		case "route":
			switch act {
			case "add", "del", "delete", "replace", "change", "append", "flush":
				mutating = true
			}
		case "link":
			switch act {
			case "set", "add", "del", "delete":
				mutating = true
			}
		case "addr":
			switch act {
			case "add", "del", "delete", "flush", "replace", "change":
				mutating = true
			}
		}
		if mutating {
			return partial{cat: "network", risk: RiskHigh, prefix: "ip " + obj + " " + act, why: "ip " + obj + " " + act}
		}
		if readAct {
			prefix := "ip " + obj
			if act != "" {
				prefix += " " + act
			}
			return partial{cat: "read", risk: RiskReadOnly, prefix: strings.TrimSpace(prefix), why: prefix}
		}
	}
	if obj == "" {
		return unk("ip")
	}
	return partial{cat: "unknown", risk: RiskUnknown, prefix: "ip " + obj, why: "unlisted command: ip " + obj}
}

// tcSafeOpts only change how tc prints a report, never what it does.
const tcSafeOpts = "sdjpgN"

// tcObjects are the traffic-control objects whose report form reads state
// and leaves the qdiscs alone.
var tcObjects = map[string]bool{"qdisc": true, "class": true, "filter": true, "action": true, "chain": true}

// gradeTC: an object with no command, or show/list/ls, only reads kernel
// traffic-control state. add, del, change, replace, link and attach rewrite
// it and are High "network", like ip. -n (another namespace) and -b (run a
// batch file) hide what runs, so they and every other form are Unknown.
func (e *shellEnv) gradeTC(args []arg) partial {
	rest := args[1:]
	for len(rest) > 0 {
		if !rest[0].ok {
			return namedUnk("tc", "tc argument")
		}
		t := rest[0].text
		if t == "--" {
			rest = rest[1:]
			break
		}
		if t == "-" || !strings.HasPrefix(t, "-") {
			break
		}
		if strings.HasPrefix(t, "--") {
			switch strings.TrimPrefix(t, "--") {
			case "color", "help", "version":
				rest = rest[1:]
				continue
			}
			return namedUnk("tc", "tc option "+t)
		}
		if strings.Trim(t[1:], tcSafeOpts) != "" {
			return namedUnk("tc", "tc option "+t)
		}
		rest = rest[1:]
	}
	if len(rest) == 0 || !rest[0].ok {
		return namedUnk("tc", "tc")
	}
	obj := rest[0].text
	if !tcObjects[obj] {
		return namedUnk("tc", "unlisted command: tc "+obj)
	}
	act := ""
	if len(rest) > 1 && rest[1].ok && !strings.HasPrefix(rest[1].text, "-") {
		act = rest[1].text
	}
	switch act {
	case "", "show", "list", "ls":
		prefix := strings.TrimSpace("tc " + obj + " " + act)
		return partial{cat: "read", risk: RiskReadOnly, prefix: prefix, why: prefix}
	case "add", "del", "delete", "change", "replace", "link", "attach":
		prefix := "tc " + obj + " " + act
		return partial{cat: "network", risk: RiskHigh, prefix: prefix, why: prefix}
	}
	return namedUnk("tc", "unlisted command: tc "+obj+" "+act)
}

func ipObject(s string) string {
	switch s {
	case "addr", "address", "a":
		return "addr"
	case "route", "r":
		return "route"
	case "link", "l":
		return "link"
	default:
		return ""
	}
}

func (e *shellEnv) gradePkg(name string, args []arg) (partial, bool) {
	switch name {
	case "apt", "apt-get", "yum", "dnf", "zypper", "apk", "pacman", "brew", "snap",
		"pip", "pip3", "gem", "cargo", "npm", "go":
	default:
		return partial{}, false
	}
	if name == "npm" {
		if npmGlobalInstall(args) {
			return partial{cat: "install", risk: RiskMedium, prefix: "npm install", why: "npm install -g"}, true
		}
		return partial{}, false
	}
	if name == "pacman" {
		if pacmanMutate(args) {
			return partial{cat: "install", risk: RiskMedium, prefix: "pacman", why: "pacman"}, true
		}
		return partial{}, false
	}
	rest, _, _, ok := stripOpts(args, "yq", []string{"yes"})
	if !ok || len(rest) == 0 || !rest[0].ok {
		return partial{}, false
	}
	verb := rest[0].text
	if pkgMutates(name, verb) {
		prefix := name + " " + verb
		if verb == "add" {
			prefix = name + " add"
		}
		return partial{cat: "install", risk: RiskMedium, prefix: prefix, why: prefix}, true
	}
	return partial{}, false
}

func pkgMutates(name, verb string) bool {
	switch verb {
	case "install", "remove", "uninstall", "upgrade", "reinstall", "purge", "erase",
		"dist-upgrade", "full-upgrade":
		return true
	case "add", "del":
		return name == "apk"
	case "update":
		return name == "yum" || name == "dnf" || name == "zypper" || name == "brew" || name == "snap"
	case "in", "rm", "up":
		return name == "zypper"
	case "refresh":
		return name == "snap"
	default:
		return false
	}
}

func npmGlobalInstall(args []arg) bool {
	global := false
	install := false
	for _, a := range args[1:] {
		if !a.ok {
			continue
		}
		switch a.text {
		case "-g", "--global":
			global = true
		case "i", "install":
			install = true
		}
	}
	return global && install
}

func pacmanMutate(args []arg) bool {
	for _, a := range args[1:] {
		if !a.ok || !strings.HasPrefix(a.text, "-") || strings.HasPrefix(a.text, "--") {
			continue
		}
		for _, c := range a.text[1:] {
			if c == 'S' || c == 'R' || c == 'U' {
				return true
			}
		}
	}
	for _, a := range args[1:] {
		if !a.ok {
			continue
		}
		if pkgMutates("pacman", a.text) {
			return true
		}
	}
	return false
}

func (e *shellEnv) gradeSimple(name string, args []arg) (partial, bool) {
	switch name {
	case "iptables", "ip6tables", "nft", "ufw", "firewall-cmd", "nmcli":
		return partial{cat: "network", risk: RiskHigh, prefix: name, why: name}, true
	case "ifconfig":
		for _, a := range args[1:] {
			if a.ok && (a.text == "up" || a.text == "down") {
				return partial{cat: "network", risk: RiskHigh, prefix: "ifconfig", why: "ifconfig " + a.text}, true
			}
		}
		return partial{cat: "read", risk: RiskReadOnly, prefix: "ifconfig", why: "ifconfig"}, true
	case "route":
		for _, a := range args[1:] {
			if !a.ok {
				continue
			}
			if a.text == "add" || a.text == "del" || a.text == "delete" {
				return partial{cat: "network", risk: RiskHigh, prefix: "route " + a.text, why: "route " + a.text}, true
			}
		}
		return partial{cat: "read", risk: RiskReadOnly, prefix: "route", why: "route"}, true
	case "kill", "pkill", "killall", "supervisorctl":
		if name == "supervisorctl" {
			for _, a := range args[1:] {
				if !a.ok {
					continue
				}
				switch a.text {
				case "start", "stop", "restart", "reload", "update", "add", "remove":
					return partial{cat: "service", risk: RiskMedium, prefix: "supervisorctl " + a.text, why: "supervisorctl " + a.text}, true
				}
			}
			return partial{}, false
		}
		return partial{cat: "service", risk: RiskMedium, prefix: name, why: name}, true
	case "chmod", "chown", "chgrp", "setfacl", "useradd", "usermod", "userdel",
		"groupadd", "groupmod", "groupdel", "passwd", "chpasswd", "visudo":
		return highPerm(name), true
	case "reboot", "shutdown", "poweroff", "halt", "telinit", "init":
		return crit(name, name), true
	case "wipefs":
		return crit("wipefs", "wipefs"), true
	case "fdisk":
		if hasFlag(args, "-l") || hasFlag(args, "--list") {
			return partial{cat: "read", risk: RiskReadOnly, prefix: "fdisk -l", why: "fdisk -l"}, true
		}
		return crit("fdisk", "fdisk"), true
	case "parted":
		if hasFlag(args, "-l") || hasFlag(args, "--list") || hasWord(args, "print") {
			return partial{cat: "read", risk: RiskReadOnly, prefix: "parted", why: "parted print"}, true
		}
		return crit("parted", "parted"), true
	case "sgdisk":
		if (hasFlag(args, "-p") || hasFlag(args, "--print")) && !sgdiskWrite(args) {
			return partial{cat: "read", risk: RiskReadOnly, prefix: "sgdisk -p", why: "sgdisk -p"}, true
		}
		return crit("sgdisk", "sgdisk"), true
	}
	if readOnlyCmd(name) {
		if !verifiedReadArgs(name, args) {
			u := unk("unverified " + name + " arguments")
			u.prefix = name
			return u, true
		}
		if name == "rg" || name == "printenv" || recursiveRead(name, args) || processEnvironment(name, args) {
			return partial{cat: "sensitive-read", risk: RiskMedium, prefix: name, why: "potential secret disclosure"}, true
		}
		if name == "sort" && sortCompress(args) {
			u := unk("sort --compress-program")
			u.prefix = "sort"
			return u, true
		}
		if (name == "less" || name == "more") && pagerCmd(args) {
			u := unk(name + " command")
			u.prefix = name
			return u, true
		}
		g := partial{cat: "read", risk: RiskReadOnly, prefix: name, why: name}
		if noPathOperands(name) {
			return g, true
		}
		for _, a := range args[1:] {
			if !a.ok {
				continue
			}
			paths := operandPaths(name, a.text)
			for _, p := range paths {
				if e.sensitive(p) {
					g = better(g, sens(p))
				}
			}
		}
		g = better(g, e.sortOutput(name, args))
		if g.risk == RiskReadOnly && contentReader(name) && e.readsUntrusted(name, args) {
			g = better(g, partial{cat: "sensitive-read", risk: RiskMedium, why: "unresolved file content read"})
		}
		g.prefix = name
		return g, true
	}
	if name == "cd" || name == "pushd" || name == "popd" || name == "test" || name == "[" {
		return partial{cat: "read", risk: RiskReadOnly, prefix: name, why: name}, true
	}
	if name == "eval" {
		g := unk("eval")
		for _, a := range args[1:] {
			if a.ok && strings.TrimSpace(a.text) != "" {
				g = better(g, e.script(a.text))
			}
		}
		if Rank(g.risk) < Rank(RiskUnknown) {
			g = better(g, unk("eval"))
		}
		g = better(g, unk("eval"))
		return g, true
	}
	if name == "source" || name == "." {
		return unk("source"), true
	}
	return partial{}, false
}

func sortCompress(args []arg) bool {
	for _, a := range args[1:] {
		if a.ok && (a.text == "--compress-program" || strings.HasPrefix(a.text, "--compress-program=")) {
			return true
		}
	}
	return false
}

func pagerCmd(args []arg) bool {
	for _, a := range args[1:] {
		if !a.ok {
			continue
		}
		t := a.text
		if strings.HasPrefix(t, "+") || strings.HasPrefix(t, "--lesskey") {
			return true
		}
		if strings.HasPrefix(t, "-k") && !strings.HasPrefix(t, "--") {
			return true
		}
	}
	return false
}

func (e *shellEnv) sortOutput(name string, args []arg) partial {
	if name != "sort" {
		return partial{}
	}
	for i := 1; i < len(args); i++ {
		if !args[i].ok {
			continue
		}
		if (args[i].text == "-o" || args[i].text == "--output") && i+1 < len(args) && args[i+1].ok {
			return e.gradeWrite(args[i+1].text, writeRedirect)
		}
		if strings.HasPrefix(args[i].text, "-o") && !strings.HasPrefix(args[i].text, "--") && len(args[i].text) > 2 {
			return e.gradeWrite(args[i].text[2:], writeRedirect)
		}
		if strings.HasPrefix(args[i].text, "--output=") {
			return e.gradeWrite(strings.TrimPrefix(args[i].text, "--output="), writeRedirect)
		}
	}
	return partial{}
}

func operandPaths(name, text string) []string {
	if text == "" || text == "-" || strings.HasPrefix(text, "-") {
		if v, ok := strings.CutPrefix(text, "--"); ok {
			if _, p, has := strings.Cut(v, "="); has && pathLike(p) {
				return []string{p}
			}
		}
		return nil
	}
	// grep's pattern is not a path unless it looks like one.

	return []string{text}
}

func pathLike(s string) bool {
	return strings.Contains(s, "/") || strings.Contains(s, `\`) || strings.HasPrefix(s, "~") || strings.Contains(s, "$")
}

// noPathOperands is a read-only command whose operands are not files.
func noPathOperands(name string) bool {
	switch name {
	case "echo", "printf", "env", "printenv", "true", "false", ":", "pwd", "date", "uname",
		"hostname", "id", "whoami", "uptime", "free", "which", "ps", "ss", "netstat", "journalctl",
		"sysctl", "dmesg", "pidstat", "vmstat", "iostat", "mpstat", "lscpu", "lsblk", "lsmem",
		"lspci", "lsusb", "w", "who", "last", "lastb", "tr", "seq", "type", "swapon":
		return true
	default:
		return false
	}
}

func readOnlyCmd(name string) bool {
	switch name {
	case "ls", "cat", "head", "tail", "less", "more", "grep", "rg", "egrep", "fgrep",
		"stat", "df", "du", "free", "uptime", "uname", "ps", "top", "journalctl", "getconf", "nproc",
		"ss", "netstat", "hostname", "id", "whoami", "which", "whereis", "file",
		"wc", "sort", "uniq", "date", "echo", "printf", "pwd", "true", "false",
		":", "env", "printenv", "sysctl", "dmesg", "pidstat", "cut", "tac", "nl", "column", "swapon":
		return true
	default:
		return anyArgsReader(name)
	}
}

func hasFlag(args []arg, flag string) bool {
	for _, a := range args[1:] {
		if a.ok && a.text == flag {
			return true
		}
	}
	return false
}

func hasWord(args []arg, w string) bool {
	for _, a := range args[1:] {
		if a.ok && a.text == w {
			return true
		}
	}
	return false
}

func sgdiskWrite(args []arg) bool {
	const writeFlags = "dnzZo"
	for _, a := range args[1:] {
		if !a.ok || !strings.HasPrefix(a.text, "-") || a.text == "--print" || a.text == "-p" {
			continue
		}
		if strings.HasPrefix(a.text, "--") && a.text != "--print" {
			return true
		}
		for _, c := range a.text[1:] {
			if strings.ContainsRune(writeFlags, c) {
				return true
			}
		}
	}
	return false
}

func nonFlags(args []arg) []arg {
	var out []arg
	end := false
	for _, a := range args[1:] {
		if !a.ok {
			out = append(out, a)
			continue
		}
		if !end && a.text == "--" {
			end = true
			continue
		}
		if !end && strings.HasPrefix(a.text, "-") && a.text != "-" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func stripOpts(args []arg, shortVal string, longVal []string) (rest []arg, seen map[string]bool, vals map[string]string, ok bool) {
	seen = map[string]bool{}
	vals = map[string]string{}
	ok = true
	takes := func(letter byte) bool { return strings.ContainsRune(shortVal, rune(letter)) }
	longTakes := func(name string) bool {
		for _, v := range longVal {
			if v == name {
				return true
			}
		}
		return false
	}
	i := 1
	for i < len(args) {
		a := args[i]
		if !a.ok {
			return args[i:], seen, vals, false
		}
		t := a.text
		if t == "--" {
			return args[i+1:], seen, vals, true
		}
		if t == "-" || !strings.HasPrefix(t, "-") {
			return args[i:], seen, vals, true
		}
		if strings.HasPrefix(t, "--") {
			name, v, has := strings.Cut(strings.TrimPrefix(t, "--"), "=")
			seen[name] = true
			if has {
				vals[name] = v
				i++
				continue
			}
			if longTakes(name) {
				if i+1 >= len(args) || !args[i+1].ok {
					return args[i:], seen, vals, false
				}
				vals[name] = args[i+1].text
				i += 2
				continue
			}
			i++
			continue
		}
		cluster := t[1:]
		for j := 0; j < len(cluster); j++ {
			letter := cluster[j]
			seen[string(letter)] = true
			if takes(letter) {
				attached := cluster[j+1:]
				if attached != "" {
					vals[string(letter)] = attached
				} else {
					if i+1 >= len(args) || !args[i+1].ok {
						return args[i:], seen, vals, false
					}
					vals[string(letter)] = args[i+1].text
					i++
				}
				break
			}
		}
		i++
	}
	return nil, seen, vals, true
}
