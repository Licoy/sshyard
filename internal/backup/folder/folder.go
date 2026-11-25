// Package folder keeps backups in a folder of this computer, most often one
// that a sync client (iCloud Drive, OneDrive, Dropbox, Nextcloud,
// Syncthing…) carries to the user's other devices. A backup is done once
// it is written here; the sync client uploads it afterwards.
package folder

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/backup"
)

var (
	// ErrPath reports a folder that is not given by its full path.
	ErrPath = errors.New("folder: not a full path")
	// ErrDenied reports a folder this process may not read or write.
	ErrDenied = errors.New("folder: permission denied")
)

// Folder is a folder of this computer that keeps backups.
type Folder struct {
	// Dir is the folder's full path; it is made when missing, as long as
	// its parent is there.
	Dir string
}

var _ backup.Remote = (*Folder)(nil)

// dir is the folder's clean path, or ErrPath.
func (f *Folder) dir() (string, error) {
	d := strings.TrimSpace(f.Dir)
	if !filepath.IsAbs(d) {
		return "", ErrPath
	}
	return filepath.Clean(d), nil
}

// file is the path of a backup named name, which must be a plain name.
func (f *Folder) file(name string) (string, error) {
	if _, _, ok := backup.Parse(name); !ok || filepath.Base(name) != name {
		return "", fmt.Errorf("folder: not a backup name: %q", name)
	}
	d, err := f.dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// placeholder is the stand-in iCloud Drive leaves, up to macOS 13, for a
// file kept only in the cloud: ".<name>.icloud" beside where it was.
func placeholder(p string) string {
	return filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".icloud")
}

// fromPlaceholder is the backup a placeholder's name stands for.
func fromPlaceholder(name string) (string, bool) {
	inner, ok := strings.CutPrefix(name, ".")
	if !ok {
		return "", false
	}
	inner, ok = strings.CutSuffix(inner, ".icloud")
	return inner, ok
}

// pathErr makes a missing file backup.ErrNotFound and a refused one
// ErrDenied, keeping the path error.
func pathErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %w", backup.ErrNotFound, err)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: %w", ErrDenied, err)
	}
	return err
}

// Check finds the folder, or its parent while it is not made yet.
func (f *Folder) Check(ctx context.Context) error {
	d, err := f.dir()
	if err != nil {
		return err
	}
	fi, err := os.Stat(d)
	if errors.Is(err, fs.ErrNotExist) {
		fi, err = os.Stat(filepath.Dir(d))
	}
	if err != nil {
		return pathErr(err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a folder", backup.ErrNotFound, d)
	}
	_, err = os.ReadDir(d)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return pathErr(err)
}

// List is the backups in the folder, none while it is not made. A backup
// iCloud Drive keeps only in the cloud is listed, its size unknown.
func (f *Folder) List(ctx context.Context) ([]backup.Object, error) {
	d, err := f.dir()
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(d)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, pathErr(err)
	}
	seen := map[string]bool{}
	var out []backup.Object
	for _, de := range des {
		if !de.Type().IsRegular() {
			continue
		}
		name, size := de.Name(), int64(0)
		if inner, ok := fromPlaceholder(name); ok {
			name = inner
		} else if fi, err := de.Info(); err == nil {
			size = fi.Size()
		}
		if o, ok := backup.ObjectOf(name, size); ok && !seen[name] {
			seen[name] = true
			out = append(out, o)
		}
	}
	backup.SortNewest(out)
	return out, nil
}

// downloadPoll is how often Get looks for a backup iCloud Drive downloads.
const downloadPoll = 500 * time.Millisecond

// Get reads the backup name, first asking iCloud Drive to download it
// when only its placeholder is here.
func (f *Folder) Get(ctx context.Context, name string) ([]byte, error) {
	p, err := f.file(name)
	if err != nil {
		return nil, err
	}
	b, err := read(p)
	if !errors.Is(err, fs.ErrNotExist) {
		return b, pathErr(err)
	}
	if _, perr := os.Stat(placeholder(p)); perr != nil {
		return nil, pathErr(err)
	}
	if err := startDownload(p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, backup.Timeout)
	defer cancel()
	t := time.NewTicker(downloadPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
		if b, err := read(p); !errors.Is(err, fs.ErrNotExist) {
			return b, pathErr(err)
		}
	}
}

// read reads the file p up to backup.MaxSize.
func read(p string) ([]byte, error) {
	r, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return backup.ReadAll(r)
}

// Put writes the backup name, making the folder when it is missing. It is
// written under a hidden name first, so that neither a sync client nor
// another device's List sees half of it.
func (f *Folder) Put(ctx context.Context, name string, data []byte) error {
	p, err := f.file(name)
	if err != nil {
		return err
	}
	d := filepath.Dir(p)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return pathErr(err)
	}
	tmp, err := os.CreateTemp(d, "."+name+".*.tmp")
	if err != nil {
		return pathErr(err)
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return pathErr(err)
}

// Delete removes the backup name, or its placeholder, which has iCloud
// Drive delete it.
func (f *Folder) Delete(ctx context.Context, name string) error {
	p, err := f.file(name)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		err = os.Remove(placeholder(p))
	}
	return pathErr(err)
}
