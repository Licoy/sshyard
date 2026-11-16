// Package portable keeps the data of a portable copy of SSHYard beside its
// executable, in sshyard_data, in place of the user's application data,
// and moves it between the two.
//
// The folder is the switch: a copy whose executable has sshyard_data
// beside it keeps its data there. A move is asked for while the app runs
// (Request) and made by the next launch (Apply), before anything opens a
// file of the data.
package portable

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/keychain"
)

// Dir is the folder beside the executable that holds a portable copy's
// data.
const Dir = "sshyard_data"

// requestFile, beside the executable, asks the next launch to move the
// data: "portable" beside the executable, "appdata" back.
const requestFile = ".sshyard_data-move"

// lockFile is the credential store's lifetime lock, which a running copy
// holds in its data folder.
const lockFile = "secrets.lock"

// movedSuffix names the folder a move leaves behind until it is removed.
const movedSuffix = ".moved"

var (
	// ErrOccupied is a destination that already holds data.
	ErrOccupied = errors.New("portable: destination already holds data")
	// ErrBusy is data that another running copy uses.
	ErrBusy = errors.New("portable: data in use")
)

// Path is the portable data folder of the executable in exeDir.
func Path(exeDir string) string { return filepath.Join(exeDir, Dir) }

// Active reports whether the copy in exeDir keeps its data beside it.
func Active(goos, exeDir string) bool {
	if goos != "windows" && goos != "linux" {
		return false
	}
	fi, err := os.Stat(Path(exeDir))
	return err == nil && fi.IsDir()
}

// Supported reports whether the data of the copy in exeDir may move
// beside it: a copy on Windows or Linux that no installer manages, in a
// folder it can write.
func Supported(goos, exeDir string) bool {
	if goos != "windows" && goos != "linux" {
		return false
	}
	if Active(goos, exeDir) {
		return true
	}
	// The Windows installer's uninstaller would remove the folder, and the
	// data with it.
	if _, err := os.Stat(filepath.Join(exeDir, "Uninstall.exe")); err == nil {
		return false
	}
	f, err := os.CreateTemp(exeDir, ".sshyard-write-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Request asks the next launch to move the data of the copy in exeDir
// beside it (portable) or back to the application data.
func Request(exeDir string, portable bool) error {
	to := "appdata"
	if portable {
		to = "portable"
	}
	return os.WriteFile(filepath.Join(exeDir, requestFile), []byte(to), 0o600)
}

// Apply makes the move Request asked for, between appData, the user's
// application data folder of the app, and the folder beside the
// executable in exeDir; it waits up to wait for the copy that asked to
// quit. A move is whole or not at all: on an error the data stays where
// it was. The request is answered either way.
func Apply(exeDir, appData string, wait time.Duration) error {
	cleanMoved(exeDir, appData)
	req := filepath.Join(exeDir, requestFile)
	b, err := os.ReadFile(req)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer os.Remove(req)
	dir := Path(exeDir)
	_, statErr := os.Stat(dir)
	here := statErr == nil
	switch strings.TrimSpace(string(b)) {
	case "portable":
		if here {
			return nil
		}
		return Move(appData, dir, wait)
	case "appdata":
		if !here {
			return nil
		}
		return Move(dir, appData, wait)
	}
	return nil
}

// cleanMoved removes what an earlier move could not.
func cleanMoved(exeDir, appData string) {
	for _, d := range []string{Path(exeDir) + movedSuffix, appData + movedSuffix} {
		if _, err := os.Lstat(d); err == nil {
			os.RemoveAll(d)
		}
	}
}

// Move moves the data folder src to dst, which must hold no data, waiting
// up to wait for a copy that uses src to quit. It copies src beside dst
// and renames the copy into place, then takes src away; on an error dst
// is gone again and src is as it was. A missing src moves as an empty
// folder.
func Move(src, dst string, wait time.Duration) error {
	if err := vacate(dst); err != nil {
		return err
	}
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dst, 0o700)
	}
	lock, err := lockWaiting(filepath.Join(src, lockFile), wait)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		lock.Close()
		return err
	}
	err = copyTree(src, tmp)
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	// Windows renames no folder with a file open in it.
	lock.Close()
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	moved := src + movedSuffix
	if err := os.Rename(src, moved); err != nil {
		return errors.Join(err, os.RemoveAll(dst))
	}
	os.RemoveAll(moved)
	return nil
}

// vacate makes way for data at dst: a folder holding at most a lock no
// copy holds, such as a copy that has just moved away leaves, is removed.
func vacate(dst string) error {
	es, err := os.ReadDir(dst)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.Name() != lockFile {
			return ErrOccupied
		}
	}
	if len(es) > 0 {
		l, err := keychain.Lock(filepath.Join(dst, lockFile))
		if err != nil {
			return lockErr(err)
		}
		l.Close()
	}
	return os.RemoveAll(dst)
}

func lockWaiting(path string, wait time.Duration) (io.Closer, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := keychain.Lock(path)
		if err == nil || !errors.Is(err, keychain.ErrBusy) || time.Now().After(deadline) {
			return l, lockErr(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func lockErr(err error) error {
	if errors.Is(err, keychain.ErrBusy) {
		return ErrBusy
	}
	return err
}

// copyTree copies the folder src to dst, the lock left out, each file
// synced to disk. A link is an error: the data holds none.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == lockFile {
			return nil
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(to, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(p, to, info.Mode().Perm())
		}
		return &os.PathError{Op: "copy", Path: p, Err: errors.New("not a regular file")}
	})
}

func copyFile(from, to string, perm os.FileMode) (err error) {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
