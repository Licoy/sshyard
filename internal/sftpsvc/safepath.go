package sftpsvc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

var (
	ErrUnsafePath        = errors.New("sftp: symbolic link or unsafe path is not allowed")
	ErrUnsafeDestination = errors.New("sftp: destination folder is writable by everyone")
	ErrPartChanged       = errors.New("sftp: partial file identity changed")
)

// ErrUnsafeRemoteName is a name one server sent that is not safe as a
// name on another (a relay).
var ErrUnsafeRemoteName = errors.New("sftp: not a safe file name on a server")

// ErrIntoItself is a folder copied into itself or a folder in it.
var ErrIntoItself = errors.New("sftp: a folder can't be copied into itself")

// RemoteName checks a name a server sent before it becomes a name on
// another server (or another folder of the same one): a single name, not
// "." or "..", without "/", NUL or "\" (a separator on Windows servers,
// so "..\x" would leave the folder chosen there). Everything else is
// allowed, as servers allow it.
func RemoteName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w: %q", ErrUnsafeRemoteName, name)
	}
	return nil
}

// RemotePath joins rel, slash-separated names from a server, below the
// remote folder dir: each name must pass RemoteName, and the result
// stays inside dir.
func RemotePath(dir, rel string) (string, error) {
	for _, n := range strings.Split(rel, "/") {
		if err := RemoteName(n); err != nil {
			return "", err
		}
	}
	d := path.Clean(dir)
	p := path.Join(d, rel)
	if !RemoteWithin(d, p) || p == d {
		return "", fmt.Errorf("%w: %q (outside %s)", ErrUnsafeRemoteName, rel, dir)
	}
	return p, nil
}

// RemoteWithin says the remote path p is dir or inside it.
func RemoteWithin(dir, p string) bool {
	dir, p = path.Clean(dir), path.Clean(p)
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// CheckInto refuses copying the remote paths srcs into the folder dir of
// the same server when dir is one of them or inside one (its copy would
// hold itself).
func CheckInto(srcs []string, dir string) error {
	for _, s := range srcs {
		if RemoteWithin(s, dir) {
			return ErrIntoItself
		}
	}
	return nil
}

// ResolvePath rejects symlinks in every existing component. SFTP v3 has no
// openat/no-follow operation: this is a conservative check, not an atomic
// guarantee against a server changing its namespace concurrently.
func (c *Client) ResolvePath(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sub, err := c.operationClient(ctx)
	if err != nil {
		return "", err
	}
	defer sub.Close()
	stop := context.AfterFunc(ctx, func() { sub.Close() })
	defer stop()
	p, err = sub.resolvePath(ctx, p)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return p, err
}

func (c *Client) resolvePath(ctx context.Context, p string) (string, error) {
	if strings.ContainsRune(p, 0) || p == "" {
		return "", errors.New("sftp: invalid path")
	}
	if !path.IsAbs(p) {
		home, err := c.Home()
		if err != nil {
			return "", err
		}
		p = path.Join(home, p)
	}
	p = path.Clean(p)
	cur := "/"
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, name := range parts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		cur = path.Join(cur, name)
		fi, err := c.c.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) && i == len(parts)-1 {
			return p, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", ErrUnsafePath
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return "", errors.New("sftp: parent is not a directory")
		}
	}
	return p, nil
}

// checkTransferParent permits missing descendants, but checks every existing
// ancestor before mkdir. create checks the final directory again afterwards.
func (c *Client) checkTransferParent(ctx context.Context, p string) error {
	return checkParents(p, func(p string) error {
		_, err := c.ResolvePath(ctx, p)
		return err
	})
}

// checkParents is checkTransferParent with resolve checking one path.
func checkParents(p string, resolve func(string) error) error {
	for {
		err := resolve(p)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) || p == path.Dir(p) {
			return err
		}
		p = path.Dir(p)
	}
}
