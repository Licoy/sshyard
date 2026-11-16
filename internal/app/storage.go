package app

import (
	"errors"

	"github.com/Licoy/sshyard/internal/portable"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// Storage is where the data of a portable copy may live: beside its
// executable, or in the user's application data (internal/portable).
type Storage struct {
	// Portable is that the data is in PortableDir, beside the executable;
	// else it is in AppDataDir.
	Portable                bool
	PortableDir, AppDataDir string
	// Move asks to move the data to the other place, then restarts the
	// app, which moves it as it starts.
	Move func() error
	// MoveErr is why the move the last launch asked for was not made.
	MoveErr error
}

// storageState is the data location's control and its dialog.
type storageState struct {
	idx    int
	asking bool
}

// storageTarget is where a move goes from where the data is now.
func (s *Storage) target() string {
	if s.Portable {
		return s.AppDataDir
	}
	return s.PortableDir
}

// storageMoveError explains the move the last launch could not make; the
// data it was to move is still where it was, so the place it was to go to
// is the other one.
func storageMoveError(m locales.Messages, s *Storage) string {
	switch {
	case errors.Is(s.MoveErr, portable.ErrOccupied):
		return m.StorageMoveOccupied(s.target())
	case errors.Is(s.MoveErr, portable.ErrBusy):
		return m.StorageMoveBusy()
	}
	return m.StorageMoveFailed(s.MoveErr.Error())
}

// reportStorageMove tells, once, of the move the last launch could not
// make.
func (a *App) reportStorageMove() {
	s := a.p.Storage
	if s == nil || s.MoveErr == nil {
		return
	}
	a.toasts = append(a.toasts, func(m locales.Messages) ui.Toast {
		return ui.Toast{Title: storageMoveError(m, s), Type: "error"}
	})
}

// storageRow chooses where the data lives, on the copies whose data may
// move; the change asks first, then moves the data as the app restarts.
func (a *App) storageRow(c *ui.Context, m locales.Messages, g *settingsGroup) {
	s := a.p.Storage
	if s == nil || s.Move == nil {
		return
	}
	st := &a.storage
	g.row(m.StorageLocation(), m.StorageLocationNote(), func() {
		st.idx = 0
		if s.Portable {
			st.idx = 1
		}
		if formSegmented(c.Key("storage"), &st.idx, false, m.StorageAppData(), m.StoragePortable()).Label(m.StorageLocation()).Changed() {
			st.asking = (st.idx == 1) != s.Portable
		}
	})
	if !st.asking {
		return
	}
	msg := m.StorageMoveToAppData(s.AppDataDir)
	if !s.Portable {
		secrets := m.StorageSecretsKeychain()
		if a.kc != nil && a.kc.PasswordProtected() {
			secrets = m.StorageSecretsPassword()
		}
		msg = m.StorageMoveToPortable(s.PortableDir) + "\n\n" + secrets
	}
	if ui.AlertDialog(c.Key("storagemove"), &st.asking, m.StorageMoveTitle(), msg, m.Cancel(), m.StorageMoveRestart()) == 1 {
		if err := s.Move(); err != nil {
			a.toastError(m.StorageMoveFailed(err.Error()))
		}
	}
}
