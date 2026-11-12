package migration

import (
	"strconv"
	"strings"
)

func parseMobaXterm(data []byte, name string) (Result, error) {
	sections, err := iniRead(data)
	if err != nil {
		return Result{}, err
	}
	r := Result{}
	found := false
	// MobaXterm.ini names the user a session logs in as by <default>.
	defaultUser := ""
	for _, s := range sections {
		lower := strings.ToLower(s.name)
		if lower != "bookmarks" && !strings.HasPrefix(lower, "bookmarks_") {
			if v := s.get("DefaultLoginName"); v != "" {
				defaultUser = v
			}
		}
	}
	for _, s := range sections {
		lower := strings.ToLower(s.name)
		if lower == "passwords" || lower == "credentials" {
			if len(s.pairs) > 0 {
				r.Issues = append(r.Issues, Issue{Code: "encrypted-secret", Field: s.name})
			}
			continue
		}
		if lower != "bookmarks" && !strings.HasPrefix(lower, "bookmarks_") {
			continue
		}
		found = true
		for _, p := range s.pairs {
			if strings.EqualFold(p.key, "SubRep") || strings.EqualFold(p.key, "ImgNum") {
				continue
			}
			// A damaged bookmark is left out; the others still import.
			parts := strings.Split(p.value, "#")
			var fields []string
			if len(parts) >= 3 {
				if _, err := strconv.Atoi(parts[1]); err == nil {
					fields = strings.Split(parts[2], "%")
				}
			}
			if len(fields) < 4 {
				r.Issues = append(r.Issues, Issue{Code: "damaged-entry", Name: p.key, Line: p.line})
				continue
			}
			if fields[0] != "0" {
				r.Issues = append(r.Issues, Issue{Code: "unsupported-protocol", Name: p.key, Field: "SessionType", Line: p.line})
				continue
			}
			e := Entry{Name: p.key, Address: fields[1], Port: iniPort(fields[2]), User: fields[3], Group: strings.ReplaceAll(s.get("SubRep"), "\\", "/")}
			if e.User == "<default>" {
				e.User = defaultUser
				if e.User == "" {
					iniIssue(&e, "default-user", "DefaultLoginName")
				}
			}
			if len(fields) > 14 && fields[14] != "" {
				e.KeyPath = fields[14]
				if strings.Contains(e.KeyPath, "_CurrentDrive_") {
					e.KeyPath = ""
					iniIssue(&e, "unsupported-field", "PrivateKey")
				}
			}
			for _, f := range []struct {
				i    int
				name string
			}{{7, "ExecuteCommand"}, {8, "SSHGateway"}, {19, "ProxyType"}, {20, "ProxyHost"}, {26, "ProxyCommand"}, {35, "Certificate"}} {
				if len(fields) > f.i && fields[f.i] != "" && fields[f.i] != "0" {
					iniIssue(&e, "unsupported-field", f.name)
				}
			}
			if len(parts) > 5 && strings.TrimSpace(parts[5]) != "" {
				iniIssue(&e, "unsupported-field", "Comments")
			}
			r.Entries = append(r.Entries, e)
		}
	}
	if !found {
		return r, ErrFormat
	}
	return r, nil
}
