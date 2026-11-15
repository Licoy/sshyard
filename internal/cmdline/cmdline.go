// Package cmdline works out what a terminal offers to complete a
// command line typed at a shell's prompt with: the rest of a command the
// shell ran before, and the entries of a folder for the path being typed.
// It keeps nothing: the commands and the folders come from the caller.
package cmdline

import (
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// Suggest is the rest of the first command of sources, taken in order,
// that starts with input and goes on past it, "" for none. Only a command
// of one line that can be typed as it is qualifies (Typeable), and input
// must hold more than blanks.
func Suggest(input string, sources ...[]string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	for _, src := range sources {
		for _, cmd := range src {
			if len(cmd) <= len(input) || !strings.HasPrefix(cmd, input) {
				continue
			}
			rest := strings.TrimRight(cmd[len(input):], " ")
			if rest != "" && Typeable(rest) {
				return rest
			}
		}
	}
	return ""
}

// Typeable reports text that typing sends as it is: valid UTF-8 without
// control characters, of which a tab would complete and a newline run.
func Typeable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return false
		}
	}
	return true
}

// LooksLikePrompt reports text that ends as a shell's prompt does, in $,
// #, %, > or one of the arrows themes draw, a space or none after it: for
// a terminal whose shell gives no other sign of reading a line. The
// prompts of Python's and other interpreters' (>>> and ...) do not.
func LooksLikePrompt(prompt string) bool {
	p := strings.TrimRight(prompt, " ")
	if p == "" || strings.HasSuffix(p, ">>>") || strings.HasSuffix(p, "...") {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(p)
	return strings.ContainsRune("$#%>❯➜»λ", r)
}

// Arg is the word being typed at the end of a command line, as a shell
// splits the line.
type Arg struct {
	// Cmd is the command of the word's simple command: its first word
	// after any assignments and the commands that run another (sudo,
	// env, time…) with their options; "" while it is that word.
	Cmd string
	// Word is the word, its quotes and backslashes undone; Quote the quote
	// it is still open in, '\'' or '"', 0 for none.
	Word  string
	Quote byte
	// Index is the word's place in its command, 0 for the first.
	Index int
	// Redirect is set for the word after <, > or >>, a file.
	Redirect bool
	// Start is where the word starts in the line, in bytes.
	Start int
}

// prefixes run the command that follows them.
var prefixes = []string{"sudo", "doas", "env", "time", "nohup", "nice", "command", "builtin", "exec", "xargs", "watch"}

// Last is the word at the end of line, which is what is typed before the
// cursor; the word is "" when line ends in a blank.
func Last(line string) Arg {
	var words []string // the words of the simple command so far
	var cur strings.Builder
	in := false // a word has started
	quote := byte(0)
	// redirect is set after < or >, until the file's word ends
	redirect := false
	end := func() {
		if in {
			if redirect {
				redirect = false
			} else {
				words = append(words, cur.String())
			}
		}
		cur.Reset()
		in = false
	}
	start := 0
	for i := 0; i < len(line); i++ {
		b := line[i]
		if !in {
			start = i
		}
		switch {
		case quote == '\'':
			if b == '\'' {
				quote = 0
			} else {
				cur.WriteByte(b)
			}
		case quote == '"':
			switch {
			case b == '"':
				quote = 0
			case b == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0:
				i++
				cur.WriteByte(line[i])
			default:
				cur.WriteByte(b)
			}
		case b == '\\':
			in = true
			if i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
			}
		case b == '\'' || b == '"':
			in, quote = true, b
		case b == ' ' || b == '\t':
			end()
		case b == '|' || b == ';' || b == '&' || b == '(' || b == ')':
			end()
			words, redirect = nil, false
		case b == '<' || b == '>':
			if in && !redirect && strings.Trim(cur.String(), "0123456789") == "" {
				// the descriptor of 2>, not a word
				cur.Reset()
				in = false
			}
			end()
			redirect = true
		default:
			in = true
			cur.WriteByte(b)
		}
	}
	if !in {
		start = len(line)
	}
	a := Arg{Word: cur.String(), Quote: quote, Redirect: redirect, Start: start}
	// The command: past assignments, and past what runs another with
	// its options and assignments.
	cmdAt := -1
	for j, w := range words {
		switch {
		case cmdAt < 0 && isAssignment(w):
		case cmdAt < 0:
			cmdAt = j
		case !slices.Contains(prefixes, words[cmdAt]):
		case strings.HasPrefix(w, "-") || isAssignment(w):
		default:
			cmdAt = j
		}
	}
	if cmdAt >= 0 && !(slices.Contains(prefixes, words[cmdAt]) && !strings.HasPrefix(a.Word, "-")) {
		a.Cmd = path.Base(words[cmdAt])
		a.Index = len(words) - cmdAt
	}
	return a
}

// isAssignment reports a word that sets a variable, NAME=value.
func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := w[j]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// folderCommands take folders; fileCommands files or folders.
var (
	folderCommands = []string{"cd", "pushd", "rmdir"}
	fileCommands   = []string{
		"cat", "less", "more", "head", "tail", "vi", "vim", "nvim", "nano", "emacs", "code", "open", "bat",
		"cp", "mv", "rm", "ln", "touch", "mkdir", "chmod", "chown", "chgrp", "ls", "ll", "la", "tree", "du",
		"stat", "file", "wc", "diff", "sort", "uniq", "source", ".", "sh", "bash", "zsh", "python", "python3",
		"node", "perl", "ruby", "php", "tar", "unzip", "zip", "gzip", "gunzip", "xz", "md5sum", "sha256sum",
		"scp", "rsync", "find", "realpath", "readlink", "tailf", "journalctl", "dpkg", "rpm",
	}
)

// Path says whether the word of a names a path to complete, and whether
// a folder alone does there (dirs): a word after cd and the like, or the
// commands that take files, a redirect's, and any word other than an
// option holding a slash or starting with ~/, ./ or ../. A command being
// typed is one only as a path, ./run.
func (a Arg) Path() (ok, dirs bool) {
	w := a.Word
	pathy := strings.Contains(w, "/")
	if strings.HasPrefix(w, "~") && !strings.HasPrefix(w, "~/") {
		return false, false // ~user, which a listing cannot tell
	}
	switch {
	case a.Redirect:
		return true, false
	case a.Cmd == "":
		return pathy, false
	case strings.HasPrefix(w, "-"):
		return false, false
	case slices.Contains(folderCommands, a.Cmd):
		return true, true
	case slices.Contains(fileCommands, a.Cmd):
		return true, false
	}
	return pathy, false
}

// Dir is the folder to list for the path word and the start of the name
// typed there (base): what is before its last slash, from cwd or, after
// ~/, home. ok is false when the folder cannot be told: a word with a
// variable, or one relative to a directory not known ("" cwd or home).
func Dir(word, cwd, home string) (dir, base string, ok bool) {
	if strings.ContainsAny(word, "$`*?[\x00") {
		return "", "", false
	}
	i := strings.LastIndexByte(word, '/')
	part, base := "", word
	if i >= 0 {
		part, base = word[:i+1], word[i+1:]
	}
	switch {
	case strings.HasPrefix(part, "/"):
		dir = part
	case strings.HasPrefix(part, "~/"):
		if !path.IsAbs(home) {
			return "", "", false
		}
		dir = path.Join(home, part[2:])
	case strings.HasPrefix(word, "~"):
		return "", "", false
	default:
		if !path.IsAbs(cwd) {
			return "", "", false
		}
		dir = path.Join(cwd, part)
	}
	return path.Clean(dir), base, true
}

// Entry is a name a folder holds, and whether it is a folder.
type Entry struct {
	Name string
	Dir  bool
}

// Matches are the entries whose name starts with base: folders alone for
// dirs, names starting with a dot only when base does, never . and ..;
// folders first, then by name without regard to case.
func Matches(es []Entry, base string, dirs bool) []Entry {
	var out []Entry
	for _, e := range es {
		switch {
		case e.Name == "." || e.Name == ".." || e.Name == "":
		case dirs && !e.Dir:
		case strings.HasPrefix(e.Name, ".") && !strings.HasPrefix(base, "."):
		case !strings.HasPrefix(e.Name, base):
		default:
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Insert is what to type after base, the start of name already typed in
// quote ('\”, '"' or 0), to complete name: the rest of it as the shell
// reads it there, and a slash after a folder's. ok is false when name
// does not start with base, or holds what cannot be typed.
func Insert(name, base string, dir bool, quote byte) (string, bool) {
	if !strings.HasPrefix(name, base) || !Typeable(name) {
		return "", false
	}
	rest := name[len(base):]
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch quote {
		case '\'':
			if c == '\'' {
				b.WriteString(`'\''`)
				continue
			}
		case '"':
			if strings.IndexByte("\"\\$`", c) >= 0 {
				b.WriteByte('\\')
			}
		default:
			if needsEscape(c) {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(c)
	}
	if dir {
		b.WriteByte('/')
	}
	return b.String(), true
}

// needsEscape reports an ASCII byte a backslash must keep from the shell
// unquoted: all but letters, digits and -_./,:@+=%^; the bytes of
// UTF-8 go as they are.
func needsEscape(c byte) bool {
	switch {
	case c >= 0x80, c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return false
	}
	return strings.IndexByte("-_./,:@+=%^", c) < 0
}
