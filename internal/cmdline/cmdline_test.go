package cmdline

import (
	"slices"
	"strings"
	"testing"
)

// TestSuggest: the first source ranks first, the newest command of a
// source first, and a command of several lines, with a tab or ending
// where the input does never comes.
func TestSuggest(t *testing.T) {
	host := []string{"git status", "git push origin main", "ls"}
	other := []string{"git stash pop", "docker ps"}
	for _, c := range []struct{ input, want string }{
		{"git st", "atus"},
		{"git sta", "tus"},
		{"git stas", "h pop"}, // from the other hosts', after this one's
		{"docker", " ps"},
		{"ls", ""},            // nothing past it
		{"  ", ""},            // blanks alone
		{"", ""},              // nothing typed
		{"git status --", ""}, // longer than any
		{"Git", ""},           // case counts
	} {
		if got := Suggest(c.input, host, other); got != c.want {
			t.Errorf("Suggest(%q) = %q, want %q", c.input, got, c.want)
		}
	}
	odd := []string{"echo a\necho b", "printf 'a\tb'", "make   ", "make test   "}
	if got := Suggest("echo", odd); got != "" {
		t.Errorf("a command of two lines suggested: %q", got)
	}
	if got := Suggest("printf", odd); got != "" {
		t.Errorf("a command with a tab suggested: %q", got)
	}
	if got := Suggest("make", odd); got != " test" {
		t.Errorf("blanks past the input: %q, want %q", got, " test")
	}
}

func TestLooksLikePrompt(t *testing.T) {
	for _, p := range []string{"user@web:~$ ", "[root@db /]# ", "host% ", "PS C:\\> ", "C:\\>", "~/src ❯ "} {
		if !LooksLikePrompt(p) {
			t.Errorf("%q is a prompt", p)
		}
	}
	for _, p := range []string{"", "   ", ">>> ", "... ", "Password: ", "Continue? [y/N] ", "Name:"} {
		if LooksLikePrompt(p) {
			t.Errorf("%q is no prompt", p)
		}
	}
}

// TestLast splits the line as a shell does, telling the word typed, the
// command it is for and whether it is a path to complete.
func TestLast(t *testing.T) {
	for _, c := range []struct {
		line     string
		want     Arg
		ok, dirs bool
	}{
		{"cd /va", Arg{Cmd: "cd", Word: "/va", Index: 1}, true, true},
		{"cd ", Arg{Cmd: "cd", Index: 1}, true, true},
		{"ls -la ", Arg{Cmd: "ls", Index: 2}, true, false},
		{"ls -l", Arg{Cmd: "ls", Word: "-l", Index: 1}, false, false},
		{"vim My\\ Fi", Arg{Cmd: "vim", Word: "My Fi", Index: 1}, true, false},
		{`cat "My Fi`, Arg{Cmd: "cat", Word: "My Fi", Quote: '"', Index: 1}, true, false},
		{`cat 'it''s`, Arg{Cmd: "cat", Word: "its", Quote: '\'', Index: 1}, true, false},
		{"sudo tail -f /var/lo", Arg{Cmd: "tail", Word: "/var/lo", Index: 2}, true, false},
		{"sudo ", Arg{}, false, false},
		{"FOO=1 /usr/bin/python3 ./", Arg{Cmd: "python3", Word: "./", Index: 1}, true, false},
		{"echo hi | grep x ./sr", Arg{Cmd: "grep", Word: "./sr", Index: 2}, true, false},
		{"echo hi | grep pattern", Arg{Cmd: "grep", Word: "pattern", Index: 1}, false, false},
		{"make && cd ..", Arg{Cmd: "cd", Word: "..", Index: 1}, true, true},
		{"echo hi > out", Arg{Cmd: "echo", Word: "out", Redirect: true, Index: 2}, true, false},
		{"run 2>/tmp/e", Arg{Cmd: "run", Word: "/tmp/e", Redirect: true, Index: 1}, true, false},
		{"./scr", Arg{Word: "./scr"}, true, false},
		{"gi", Arg{Word: "gi"}, false, false},
		{"cd ~ro", Arg{Cmd: "cd", Word: "~ro", Index: 1}, false, false},
		{"cd ~/Do", Arg{Cmd: "cd", Word: "~/Do", Index: 1}, true, true},
	} {
		got := Last(c.line)
		if start := len(c.line) - len(rawWord(c.line)); got.Start != start {
			t.Errorf("Last(%q) starts at %d, want %d", c.line, got.Start, start)
		}
		got.Start = 0
		if got != c.want {
			t.Errorf("Last(%q) = %+v, want %+v", c.line, got, c.want)
		}
		if ok, dirs := got.Path(); ok != c.ok || dirs != c.dirs {
			t.Errorf("Last(%q).Path() = %v, %v; want %v, %v", c.line, ok, dirs, c.ok, c.dirs)
		}
	}
}

// rawWord is the end of line after its last blank or operator, as the
// cases of TestLast type it, a blank escaped or quoted being the word's.
func rawWord(line string) string {
	line = strings.NewReplacer(`\ `, "__", `"My Fi`, "_My_Fi").Replace(line)
	return line[strings.LastIndexAny(line, " |&;<>")+1:]
}

func TestDir(t *testing.T) {
	for _, c := range []struct {
		word, cwd, home string
		dir, base       string
		ok              bool
	}{
		{"src", "/home/u", "/home/u", "/home/u", "src", true},
		{"src/ma", "/home/u", "", "/home/u/src", "ma", true},
		{"../", "/home/u/app", "", "/home/u", "", true},
		{"/etc/ng", "", "", "/etc", "ng", true},
		{"/", "", "", "/", "", true},
		{"~/Do", "", "/Users/u", "/Users/u", "Do", true},
		{"~/a/b", "/x", "/Users/u", "/Users/u/a", "b", true},
		{"~/Do", "/x", "", "", "", false},      // home not known
		{"src", "", "/Users/u", "", "", false}, // nor where the shell is
		{"$HOME/x", "/x", "/h", "", "", false},
		{"*.go", "/x", "/h", "", "", false},
	} {
		dir, base, ok := Dir(c.word, c.cwd, c.home)
		if dir != c.dir || base != c.base || ok != c.ok {
			t.Errorf("Dir(%q, %q, %q) = %q, %q, %v; want %q, %q, %v", c.word, c.cwd, c.home, dir, base, ok, c.dir, c.base, c.ok)
		}
	}
}

// TestMatches filters a folder's entries by the start typed: folders
// alone for cd, hidden ones once a dot is typed, folders first.
func TestMatches(t *testing.T) {
	es := []Entry{{"src", true}, {"setup.py", false}, {".secret", false}, {".ssh", true}, {"Scripts", true}, {"..", true}, {"README", false}}
	names := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}
	for _, c := range []struct {
		base string
		dirs bool
		want []string
	}{
		{"s", false, []string{"src", "setup.py"}},
		{"", false, []string{"Scripts", "src", "README", "setup.py"}},
		{"", true, []string{"Scripts", "src"}},
		{".", false, []string{".ssh", ".secret"}},
		{".s", true, []string{".ssh"}},
		{"x", false, nil},
	} {
		if got := names(Matches(es, c.base, c.dirs)); !slices.Equal(got, c.want) {
			t.Errorf("Matches(%q, dirs %v) = %q, want %q", c.base, c.dirs, got, c.want)
		}
	}
}

// TestInsert types the rest of a name as the shell reads it where the
// word stands: escaped bare, in double or in single quotes.
func TestInsert(t *testing.T) {
	for _, c := range []struct {
		name, base string
		dir        bool
		quote      byte
		want       string
		ok         bool
	}{
		{"src", "s", true, 0, "rc/", true},
		{"setup.py", "se", false, 0, "tup.py", true},
		{"My Files", "My", true, 0, `\ Files/`, true},
		{"a&b(1).txt", "a", false, 0, `\&b\(1\).txt`, true},
		{"My Files", "My", true, '"', " Files/", true},
		{`a"$b`, "a", false, '"', `\"\$b`, true},
		{"it's", "i", false, '\'', `t'\''s`, true},
		{"中文.txt", "中", false, 0, "文.txt", true},
		{"src", "src", true, 0, "/", true},
		{"src", "x", false, 0, "", false},
		{"bad\nname", "b", false, 0, "", false},
	} {
		got, ok := Insert(c.name, c.base, c.dir, c.quote)
		if got != c.want || ok != c.ok {
			t.Errorf("Insert(%q, %q, %v, %q) = %q, %v; want %q, %v", c.name, c.base, c.dir, c.quote, got, ok, c.want, c.ok)
		}
	}
}
