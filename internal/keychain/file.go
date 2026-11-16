package keychain

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Lock takes the lifetime lock at path that OpenLocked takes, failing with
// ErrBusy while another process holds it.
func Lock(path string) (io.Closer, error) { return acquireLock(path) }

func readEncrypted(path string) (data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	data, err = io.ReadAll(io.LimitReader(f, maxStoreSize+1))
	if err == nil && len(data) > maxStoreSize {
		return nil, fmt.Errorf("%w: encrypted store exceeds size limit", ErrCorrupt)
	}
	return data, err
}

// writeEncrypted only writes ciphertext. Sync completes before replacement;
// this is an atomic file replacement, not a transaction with metadata files.
func writeEncrypted(path string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".secrets-*")
	if err != nil {
		return err
	}
	defer func() {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}()
	tmp := f.Name()
	defer func() {
		if removeErr := os.Remove(tmp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	err = f.Close()
	f = nil
	if err != nil {
		return err
	}
	return atomicReplace(tmp, path)
}

func openLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}
