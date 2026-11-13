// Package migration reads third-party session exports without modifying them.
package migration

import (
	"errors"
	"fmt"
	"strconv"
)

type Source string

const (
	Xshell    Source = "Xshell"
	MobaXterm Source = "MobaXterm"
	PuTTY     Source = "PuTTY"
	WindTerm  Source = "WindTerm"
	Electerm  Source = "Electerm"
	Tabby     Source = "Tabby"
	Netcatty  Source = "Netcatty"
	Remmina   Source = "Remmina"
	MRemoteNG Source = "mRemoteNG"
	WinSCP    Source = "WinSCP"
	// Text is a host list of key=value lines, typed, pasted or read
	// from a file; it is not one of the clients Sources lists.
	Text Source = "Text"
	// CSV is a host table whose header names the columns with the keys
	// of a Text host list; it is not one of the clients Sources lists.
	CSV Source = "CSV"
)

func Sources() []Source {
	return []Source{Xshell, MobaXterm, PuTTY, WindTerm, Electerm, Tabby, Netcatty, Remmina, MRemoteNG, WinSCP}
}

type Result struct {
	Entries []Entry
	Issues  []Issue
	// Encoding is the legacy code page files were decoded in, empty when
	// every file was UTF-8 or UTF-16.
	Encoding Encoding
}

// Secrets are transient bytes and must never be serialized to application metadata.
type Entry struct {
	ID, Name, Address, User, Group   string
	Port                             int
	Password, PrivateKey, Passphrase []byte `json:"-" yaml:"-"`
	KeyPath                          string
	Jumps                            []Hop
	Issues                           []Issue
}
type Hop struct {
	Ref, Name, Address, User string
	Port                     int
}

// Issue is a diagnostic; Line is the source line it is about, 0 when none.
type Issue struct {
	Code, Name, Field string
	Line              int
}

var (
	ErrFormat    = errors.New("unsupported or malformed migration format")
	ErrTooLarge  = errors.New("migration input exceeds limit")
	ErrNoEntries = errors.New("no SSH entries found")
	ErrRead      = errors.New("cannot read migration input")
	ErrEncrypted = errors.New("encrypted migration input is not supported")
	// ErrNoSessions is an ErrFormat for a file without the sections a
	// source keeps its sessions in.
	ErrNoSessions = fmt.Errorf("%w: no session section", ErrFormat)
)

// FormatError is an ErrFormat at a line of a file: File is its base name,
// empty when the input was not a file.
type FormatError struct {
	File string
	Line int
}

func (e *FormatError) Error() string {
	return ErrFormat.Error() + " at " + e.File + ":" + strconv.Itoa(e.Line)
}
func (e *FormatError) Unwrap() error { return ErrFormat }
