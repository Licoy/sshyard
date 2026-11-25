//go:build !darwin

package folder

import (
	"fmt"
	"io/fs"

	"github.com/Licoy/sshyard/internal/backup"
)

// startDownload is not needed here: only iCloud Drive on macOS leaves
// placeholders, and other sync clients download a file as it is read.
func startDownload(p string) error {
	return fmt.Errorf("%w: %s: %w", backup.ErrNotFound, p, fs.ErrNotExist)
}
