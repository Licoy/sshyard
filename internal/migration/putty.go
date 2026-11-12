package migration

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

func parsePuTTY(data []byte, name string) (Result, error) {
	text, err := iniText(data)
	if err != nil {
		return Result{}, err
	}
	isReg := strings.HasPrefix(text, "Windows Registry Editor Version 5.00") || strings.HasPrefix(text, "REGEDIT4")
	if !isReg {
		sections, err := iniRead(data)
		if err != nil {
			return Result{}, err
		}
		if len(sections) != 1 {
			return Result{}, ErrFormat
		}
		label, err := url.PathUnescape(filepath.Base(name))
		if err != nil {
			return Result{}, ErrFormat
		}
		if sections[0].get("HostName") == "" {
			if label == "Default Settings" && sections[0].get("Protocol") != "" {
				return Result{}, nil
			}
			return Result{}, ErrFormat
		}
		return puttyEntries([]iniSection{{name: label, pairs: sections[0].pairs}}), nil
	}
	sections := []iniSection{}
	current := -1
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return Result{}, ErrFormat
			}
			current = -1
			const marker = "\\PuTTY\\Sessions\\"
			at := strings.Index(line, marker)
			if at < 0 {
				continue
			}
			label, err := url.PathUnescape(line[at+len(marker) : len(line)-1])
			if err != nil {
				return Result{}, ErrFormat
			}
			sections = append(sections, iniSection{name: label})
			current = len(sections) - 1
			continue
		}
		if current < 0 {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Result{}, ErrFormat
		}
		key, err = puttyQuoted(key)
		if err != nil {
			return Result{}, err
		}
		if strings.HasPrefix(value, "dword:") {
			n, e := strconv.ParseUint(strings.TrimPrefix(value, "dword:"), 16, 32)
			if e != nil {
				return Result{}, ErrFormat
			}
			value = strconv.FormatUint(n, 10)
		} else {
			value, err = puttyQuoted(value)
			if err != nil {
				return Result{}, err
			}
		}
		sections[current].pairs = append(sections[current].pairs, iniPair{key, value, i + 1})
	}
	if len(sections) == 0 {
		return Result{}, ErrFormat
	}
	return puttyEntries(sections), nil
}
func puttyQuoted(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", ErrFormat
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		if s[i] == '\\' {
			i++
			if i >= len(s)-1 || (s[i] != '\\' && s[i] != '"') {
				return "", ErrFormat
			}
		} else if s[i] == '"' {
			return "", ErrFormat
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}
func puttyEntries(sections []iniSection) Result {
	r := Result{}
	for _, s := range sections {
		if s.name == "Default Settings" && s.get("HostName") == "" {
			continue
		}
		if !strings.EqualFold(s.get("Protocol"), "ssh") {
			r.Issues = append(r.Issues, Issue{Code: "unsupported-protocol", Name: s.name, Field: "Protocol"})
			continue
		}
		e := Entry{Name: s.name, Address: s.get("HostName"), User: s.get("UserName"), Port: iniPort(s.get("PortNumber")), KeyPath: s.get("PublicKeyFile")}
		iniReport(&e, s, "unsupported-field", "ProxyMethod", "ProxyHost", "ProxyTelnetCommand", "ProxyLocalCommand", "PortForwardings", "RemoteCommand", "SerialLine", "Notes", "Tags")
		iniReport(&e, s, "encrypted-secret", "Password", "ProxyPassword")
		r.Entries = append(r.Entries, e)
	}
	return r
}
