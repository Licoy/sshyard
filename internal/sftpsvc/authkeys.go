package sftpsvc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"golang.org/x/crypto/ssh"
)

// HasKey reports whether authorized_keys data lists pub, with whatever
// options and comment; lines that do not parse are skipped.
func HasKey(data []byte, pub ssh.PublicKey) bool {
	want := pub.Marshal()
	for _, line := range bytes.Split(data, []byte("\n")) {
		k, _, _, _, err := ssh.ParseAuthorizedKey(line)
		if err == nil && bytes.Equal(k.Marshal(), want) {
			return true
		}
	}
	return false
}

// InstallKey appends line, the authorized_keys line of pub, to
// ~/.ssh/authorized_keys of the login, unless pub is there already
// (added is then false). What it creates it makes private, the directory
// 0700 and the file 0600, as ssh-copy-id does, and the name never has
// any other mode (see mkdirPrivate); the modes of what is
// there it leaves alone, so that a setup the server refuses stays visible
// (see the check that follows an install).
func (c *Client) InstallKey(pub ssh.PublicKey, line string) (added bool, err error) {
	home, err := c.Home()
	if err != nil {
		return false, err
	}
	dir := path.Join(home, ".ssh")
	switch fi, err := c.c.Stat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		// Made by someone else meanwhile, it is used as it is.
		if err := c.mkdirPrivate(dir); err != nil && !errors.Is(err, errAppeared) {
			return false, err
		}
		if fi, err := c.c.Stat(dir); err != nil || !fi.IsDir() {
			return false, fmt.Errorf("%s is not a directory", dir)
		}
	case err != nil:
		return false, err
	case !fi.IsDir():
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	file := path.Join(dir, "authorized_keys")
	var old []byte
	// A file that appears while ours is made is read again and appended
	// to, once.
	for try := 0; ; try++ {
		old = nil
		created := false
		switch f, err := c.c.Open(file); {
		case errors.Is(err, fs.ErrNotExist):
			created = true
		case err != nil:
			return false, fmt.Errorf("read %s: %w", file, err)
		default:
			old, err = readSmall(f, 16<<20)
			f.Close()
			if err != nil {
				return false, fmt.Errorf("read %s: %w", file, err)
			}
		}
		if HasKey(old, pub) {
			return false, nil
		}
		if !created {
			break
		}
		err := c.createPrivate(file, []byte(line+"\n"))
		if !errors.Is(err, errAppeared) || try > 0 {
			return err == nil, err
		}
	}
	out := []byte(line + "\n")
	if len(old) > 0 && old[len(old)-1] != '\n' {
		out = append([]byte("\n"), out...)
	}
	// Not O_APPEND: not every SFTP server honours it. The end is where
	// the file was read to.
	f, err := c.c.OpenFile(file, os.O_WRONLY)
	if err != nil {
		return false, fmt.Errorf("write %s: %w", file, err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return false, fmt.Errorf("write %s: %w", file, err)
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return false, fmt.Errorf("write %s: %w", file, err)
	}
	return true, f.Close()
}

// The SFTP client sends no attributes with MKDIR and OPEN, so what it
// creates gets the server's default mode first. So that the name never
// has it, mkdirPrivate and createPrivate make a sibling with a random
// name, set its mode, and only then rename it into place. Some servers'
// rename replaces what is there (pkg/sftp uses rename(2)), others refuse
// (OpenSSH links); so the name is checked just before, and after a failed
// rename, and when something is there by then, the temporary is removed
// and errAppeared returned: the caller uses what appeared. Between the
// check and the rename a server of the first kind could still replace a
// file made in that instant; nothing over SFTP v3 closes that window.

// errAppeared: the name was taken while the private copy was made.
var errAppeared = errors.New("appeared meanwhile")

// beforeRename, when set (by tests), runs just before the check that
// precedes a rename of tmp to p.
var beforeRename func(tmp, p string)

// renameNew renames tmp to p unless p exists; undo removes tmp.
func (c *Client) renameNew(tmp, p string, undo func()) error {
	if beforeRename != nil {
		beforeRename(tmp, p)
	}
	exists := func() bool {
		_, err := c.c.Lstat(p)
		return err == nil
	}
	if exists() {
		undo()
		return errAppeared
	}
	if err := c.c.Rename(tmp, p); err != nil {
		undo()
		if exists() {
			return errAppeared
		}
		return err
	}
	return nil
}

// tempName is a hidden sibling of p no one else picks.
func tempName(p string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return path.Join(path.Dir(p), ".sshyard-"+hex.EncodeToString(b)+"-"+path.Base(p))
}

// mkdirPrivate creates the directory p with mode 0700.
func (c *Client) mkdirPrivate(p string) error {
	tmp := tempName(p)
	if err := c.c.Mkdir(tmp); err != nil {
		return fmt.Errorf("create %s: %w", p, err)
	}
	fail := func(err error) error {
		c.c.RemoveDirectory(tmp)
		return err
	}
	if err := c.c.Chmod(tmp, 0o700); err != nil {
		return fail(fmt.Errorf("chmod %s: %w", p, err))
	}
	// Whatever another user slipped in before the chmod would move with
	// it; there should be nothing.
	if ents, err := c.c.ReadDir(tmp); err != nil || len(ents) > 0 {
		if err == nil {
			err = errors.New("not empty")
		}
		return fail(fmt.Errorf("create %s: %w", p, err))
	}
	err := c.renameNew(tmp, p, func() { c.c.RemoveDirectory(tmp) })
	if err != nil && !errors.Is(err, errAppeared) {
		return fmt.Errorf("create %s: %w", p, err)
	}
	return err
}

// createPrivate creates the file p with mode 0600 and data in it.
func (c *Client) createPrivate(p string, data []byte) error {
	tmp := tempName(p)
	f, err := c.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	fail := func(err error) error {
		c.c.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fail(fmt.Errorf("chmod %s: %w", p, err))
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fail(fmt.Errorf("write %s: %w", p, err))
	}
	if err := f.Close(); err != nil {
		return fail(fmt.Errorf("write %s: %w", p, err))
	}
	err = c.renameNew(tmp, p, func() { c.c.Remove(tmp) })
	if err != nil && !errors.Is(err, errAppeared) {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return err
}
