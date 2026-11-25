// Package backup is what SSHYard's backups are made of and where they are
// kept: the archive a backup seals (package vault does the sealing), the
// names of sealed files, and the places that keep them, a folder on a
// WebDAV server (package webdav), an S3 bucket (package s3), a GitHub
// gist (package gist) or a folder of this computer that a sync client may
// carry elsewhere (package folder). A place only ever sees sealed files.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Ext is the extension of a sealed backup file.
const Ext = ".sshyard-backup"

// MaxSize bounds a sealed file read from a place, and an archive's files
// once unpacked.
const MaxSize = 64 << 20

var (
	// ErrAuth reports a place that refused the credentials, or their
	// permissions.
	ErrAuth = errors.New("backup: credentials refused")
	// ErrNotFound reports a folder, bucket, gist or file that is not there.
	ErrNotFound = errors.New("backup: not found")
	// ErrTooLarge reports a file over MaxSize.
	ErrTooLarge = errors.New("backup: file too large")
)

// StatusError is an answer from a place that is not a success: its HTTP
// status, and the code and message it gave, when it gave any.
type StatusError struct {
	Status        int
	Code, Message string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("HTTP %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Is makes 401 and 403 ErrAuth, and 404 ErrNotFound.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrAuth:
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	}
	return false
}

// Object is a backup in a place.
type Object struct {
	Name string
	// Time and Device are when and on which device it was made, from Name.
	Time   time.Time
	Device string
	// Size is in bytes, 0 when the place does not say.
	Size int64
}

// Remote is a place backups are kept. Each backup is a file of its own,
// named by Name and never rewritten; List knows only those files.
type Remote interface {
	// Check reaches the place with its credentials.
	Check(ctx context.Context) error
	// List is the backups there, newest first.
	List(ctx context.Context) ([]Object, error)
	// Get reads one, up to MaxSize.
	Get(ctx context.Context, name string) ([]byte, error)
	// Put stores a new one, making its folder when needed.
	Put(ctx context.Context, name string, data []byte) error
	// Delete removes one.
	Delete(ctx context.Context, name string) error
}

const (
	namePrefix = "SSHYard-"
	nameTime   = "20060102T150405Z"
)

// Name is what a backup made at t on device is called in a place, as
// SSHYard-20261104T101500Z-ab12cd34.sshyard-backup.
func Name(t time.Time, device string) string {
	return namePrefix + t.UTC().Format(nameTime) + "-" + device + Ext
}

// Parse reads a Name back; ok is false for a file that is not a backup's.
func Parse(name string) (t time.Time, device string, ok bool) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return time.Time{}, "", false
	}
	if rest, ok = strings.CutSuffix(rest, Ext); !ok || len(rest) < len(nameTime)+2 || rest[len(nameTime)] != '-' {
		return time.Time{}, "", false
	}
	t, err := time.Parse(nameTime, rest[:len(nameTime)])
	device = rest[len(nameTime)+1:]
	if err != nil || !ValidDevice(device) {
		return time.Time{}, "", false
	}
	return t, device, true
}

// ValidDevice reports whether s can name a device in a file name: 1 to 16
// lowercase letters and digits.
func ValidDevice(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ObjectOf is the backup a file of a place is, from its name; ok is false
// for any other file.
func ObjectOf(name string, size int64) (Object, bool) {
	t, dev, ok := Parse(name)
	if !ok {
		return Object{}, false
	}
	return Object{Name: name, Time: t, Device: dev, Size: size}, true
}

// SortNewest sorts objs newest first.
func SortNewest(objs []Object) {
	slices.SortFunc(objs, func(a, b Object) int {
		if c := b.Time.Compare(a.Time); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
}

// Prune is what to delete so that device keeps its newest keep backups;
// keep 0 keeps them all. Another device's backups are never pruned.
func Prune(objs []Object, device string, keep int) []Object {
	if keep <= 0 {
		return nil
	}
	mine := make([]Object, 0, len(objs))
	for _, o := range objs {
		if o.Device == device {
			mine = append(mine, o)
		}
	}
	SortNewest(mine)
	if len(mine) <= keep {
		return nil
	}
	return mine[keep:]
}

// Timeout bounds one request to a place.
const Timeout = 2 * time.Minute

// Client is the HTTP client a place is reached with: every connection
// dialed by dial (nil is the system's way, with its proxy settings), and
// redirects followed only on the same scheme, host and port, so that
// credentials go nowhere else.
func Client(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if dial != nil {
		tr.Proxy, tr.DialContext = nil, dial
	}
	return &http.Client{Transport: tr, Timeout: Timeout, CheckRedirect: SameHost}
}

// SameHost is a CheckRedirect that refuses to leave the first request's
// scheme, host and port.
func SameHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("backup: too many redirects")
	}
	if first := via[0].URL; req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
		return fmt.Errorf("backup: refusing redirect to %s://%s", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// ReadAll reads r up to MaxSize; more is ErrTooLarge.
func ReadAll(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err == nil && len(b) > MaxSize {
		return nil, ErrTooLarge
	}
	return b, err
}
