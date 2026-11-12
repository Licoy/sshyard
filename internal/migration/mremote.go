package migration

import (
	"encoding/xml"
	"io"
	"strings"
)

type mremoteNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr    `xml:",any,attr"`
	Nodes   []mremoteNode `xml:"Node"`
}

func mremoteAttrs(attrs []xml.Attr) iniSection {
	s := iniSection{}
	for _, a := range attrs {
		s.pairs = append(s.pairs, iniPair{key: a.Name.Local, value: a.Value})
	}
	return s
}
func parseMRemoteNG(data []byte, name string) (Result, error) {
	text, err := iniText(data)
	if err != nil {
		return Result{}, err
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "<") {
		return mremoteCSV(text)
	}
	d := xml.NewDecoder(strings.NewReader(text))
	d.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	var root mremoteNode
	if err := d.Decode(&root); err != nil || root.XMLName.Local != "Connections" {
		return Result{}, ErrFormat
	}
	// Decode must consume the complete document, including trailing malformed data.
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Result{}, ErrFormat
		}
		if c, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(c)) != "" {
			return Result{}, ErrFormat
		}
	}
	attrs := mremoteAttrs(root.Attrs)
	if strings.EqualFold(attrs.get("FullFileEncryption"), "true") {
		return Result{Issues: []Issue{{Code: "encrypted-secret", Field: "FullFileEncryption"}}}, ErrEncrypted
	}
	if len(root.Nodes) == 0 {
		return Result{}, ErrFormat
	}
	r := Result{}
	if err := mremoteWalk(root.Nodes, iniSection{}, "", 0, &r); err != nil {
		return Result{}, err
	}
	return r, nil
}
func mremoteWalk(nodes []mremoteNode, parent iniSection, group string, depth int, r *Result) error {
	if depth > 64 {
		return ErrFormat
	}
	for _, node := range nodes {
		s := mremoteAttrs(node.Attrs)
		effective := iniSection{name: s.get("Name"), pairs: append([]iniPair(nil), s.pairs...)}
		for _, p := range s.pairs {
			if !strings.HasPrefix(p.key, "Inherit") || !strings.EqualFold(p.value, "true") {
				continue
			}
			field := strings.TrimPrefix(p.key, "Inherit")
			if field == "Description" {
				field = "Descr"
			}
			effective.pairs = append(effective.pairs, iniPair{key: field, value: parent.get(field)})
		}
		if strings.EqualFold(s.get("Type"), "Container") {
			child := strings.Trim(strings.Join([]string{group, s.get("Name")}, "/"), "/")
			if err := mremoteWalk(node.Nodes, effective, child, depth+1, r); err != nil {
				return err
			}
			continue
		}
		mremoteAppend(effective, group, r)
	}
	return nil
}
func mremoteAppend(s iniSection, group string, r *Result) {
	e := Entry{ID: s.get("Id"), Name: s.get("Name"), Address: s.get("Hostname"), User: s.get("Username"), Port: iniPort(s.get("Port")), Group: group}
	protocol := strings.ToUpper(s.get("Protocol"))
	if protocol != "SSH1" && protocol != "SSH2" && protocol != "SSH" {
		r.Issues = append(r.Issues, Issue{Code: "unsupported-protocol", Name: e.Name, Field: "Protocol"})
		return
	}
	iniReport(&e, s, "encrypted-secret", "Password")
	iniReport(&e, s, "unsupported-field", "Descr", "UserField", "ExtApp", "PreExtApp", "PostExtApp", "ProxyHost", "Proxy", "SSHTunnelConnectionName", "OpeningCommand", "SSHOptions", "EnvironmentTags")
	if p := s.get("PuttySession"); p != "" && p != "Default Settings" {
		iniIssue(&e, "unsupported-field", "PuttySession")
	}
	r.Entries = append(r.Entries, e)
}
