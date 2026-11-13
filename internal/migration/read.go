package migration

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxFiles = 1024
const MaxEntries = 10000
const MaxIssues = 10000
const MaxBytes int64 = 32 << 20

// Read accepts selected files or directories; symbolic links are never
// followed. o says how an INI-style source's legacy files are decoded.
func Read(source Source, paths []string, o Options) (Result, error) {
	var result Result
	var total int64
	count := 0
	issueTotal := 0
	seen := map[string]bool{}
	consume := func(path, group string) error {
		if seen[path] {
			return nil
		}
		seen[path] = true
		count++
		if count > MaxFiles {
			return ErrTooLarge
		}
		data, err := readFile(path, MaxBytes-total)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if iniStyle(source) && legacy(data) {
			var used Encoding
			if data, used, err = legacyText(data, o); err != nil {
				return &FormatError{File: filepath.Base(path)}
			}
			if result.Encoding == "" {
				result.Encoding = used
			}
		}
		r, err := parseData(source, data, path)
		var fe *FormatError
		if errors.As(err, &fe) {
			return &FormatError{File: filepath.Base(path), Line: fe.Line}
		}
		if err != nil {
			return err
		}
		if len(result.Entries)+len(r.Entries) > MaxEntries {
			return ErrTooLarge
		}
		for i := range r.Entries {
			e := &r.Entries[i]
			e.KeyPath = keyPath(e.KeyPath, path)
			e.Group = strings.Trim(strings.Join([]string{group, e.Group}, "/"), "/")
			validateEntry(e)
		}
		issueTotal += migrationIssueCount(r)
		if issueTotal > MaxIssues {
			return ErrTooLarge
		}
		result.Entries = append(result.Entries, r.Entries...)
		result.Issues = append(result.Issues, r.Issues...)
		return nil
	}
	for _, path := range paths {
		if err := visitFiles(source, path, consume); err != nil {
			return Result{}, err
		}
	}
	resolveReferences(&result)
	if migrationIssueCount(result) > MaxIssues {
		return Result{}, ErrTooLarge
	}
	if len(result.Entries) == 0 && len(result.Issues) == 0 {
		return result, ErrNoEntries
	}
	return result, nil
}
func readFile(path string, remaining int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrRead
	}
	if info.Size() > remaining {
		return nil, ErrTooLarge
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrRead
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, remaining+1))
	if err != nil {
		return nil, ErrRead
	}
	if int64(len(data)) > remaining {
		return nil, ErrTooLarge
	}
	return data, nil
}
func visitFiles(source Source, path string, consume func(string, string) error) error {
	root, err := filepath.Abs(path)
	if err != nil {
		return ErrRead
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ErrRead
	}
	if !info.IsDir() {
		return consume(root, "")
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrRead
		}
		if d.Type()&os.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		if !acceptFile(source, d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return ErrRead
		}
		if rel == "." {
			rel = ""
		}
		return consume(p, filepath.ToSlash(rel))
	})
}

// iniStyle reports a source whose Windows client writes its exports in the
// system's ANSI code page, decoded as Options say.
func iniStyle(s Source) bool {
	switch s {
	case Xshell, MobaXterm, PuTTY, Remmina, MRemoteNG, WinSCP:
		return true
	}
	return false
}
func acceptFile(s Source, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch s {
	case Xshell:
		return ext == ".xsh"
	case MobaXterm:
		return ext == ".mxtsessions" || ext == ".ini"
	case PuTTY:
		return true // Unix session names have no required extension
	case Remmina:
		return ext == ".remmina"
	case MRemoteNG:
		return ext == ".xml" || ext == ".csv"
	case WinSCP:
		return ext == ".ini"
	case WindTerm:
		return strings.EqualFold(name, "user.sessions") || ext == ".json"
	case Tabby:
		return ext == ".yaml" || ext == ".yml"
	case Electerm:
		return ext == ".json"
	case Netcatty:
		return ext == ".json" || ext == ".csv"
	case Text:
		return ext == ".txt"
	case CSV:
		return ext == ".csv"
	}
	return false
}
func parseData(s Source, data []byte, name string) (Result, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	switch s {
	case Xshell:
		return parseXshell(data, name)
	case MobaXterm:
		return parseMobaXterm(data, name)
	case PuTTY:
		return parsePuTTY(data, name)
	case Remmina:
		return parseRemmina(data, name)
	case MRemoteNG:
		return parseMRemoteNG(data, name)
	case WinSCP:
		return parseWinSCP(data, name)
	case WindTerm:
		return parseWindTerm(data, name)
	case Electerm:
		return parseElecterm(data, name)
	case Tabby:
		return parseTabby(data, name)
	case Netcatty:
		return parseNetcatty(data, name)
	case Text:
		return parseText(data, name)
	case CSV:
		return parseCSV(data, name)
	}
	return Result{}, ErrFormat
}

// validateEntry checks the fields an entry imports, trimmed as hosts are
// saved: a control character makes it invalid, a U+FFFD only warns, since
// a file may hold one that something before misdecoded.
func validateEntry(e *Entry) {
	e.Name, e.Address, e.User = strings.TrimSpace(e.Name), strings.TrimSpace(e.Address), strings.TrimSpace(e.User)
	for _, f := range []struct{ name, value string }{{"name", e.Name}, {"address", e.Address}, {"user", e.User}, {"group", e.Group}} {
		if f.name != "address" && strings.ContainsFunc(f.value, unicode.IsControl) {
			e.Issues = append(e.Issues, Issue{Code: "invalid-entry", Name: e.Name, Field: f.name})
		} else if strings.ContainsRune(f.value, utf8.RuneError) {
			e.Issues = append(e.Issues, Issue{Code: "unreadable-text", Name: e.Name, Field: f.name})
		}
	}
	address := e.Address
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		address = address[1 : len(address)-1]
	}
	if ip, err := netip.ParseAddr(address); err == nil {
		e.Address = ip.String()
	}
	if e.Port == 0 {
		e.Port = 22
	}
	if e.Name == "" {
		e.Name = e.Address
	}
	if e.Address == "" || strings.ContainsAny(e.Address, "/@") || strings.ContainsFunc(e.Address, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || strings.Contains(e.Address, ":") && !validIP(e.Address) {
		e.Issues = append(e.Issues, Issue{Code: "invalid-entry", Name: e.Name, Field: "address"})
	}
	if e.Port < 1 || e.Port > 65535 {
		e.Issues = append(e.Issues, Issue{Code: "invalid-entry", Name: e.Name, Field: "port"})
	}
}

func validIP(s string) bool { _, err := netip.ParseAddr(s); return err == nil }

func migrationIssueCount(result Result) int {
	count := len(result.Issues)
	for _, entry := range result.Entries {
		count += len(entry.Issues)
		if count > MaxIssues {
			return count
		}
	}
	return count
}
