package app

import (
	"errors"
	"testing"

	"github.com/Licoy/sshyard/internal/portable"
	"github.com/Licoy/sshyard/locales"
)

func TestStorageMoveErrorNamesWhereTheMoveWent(t *testing.T) {
	m := locales.In("en")
	s := &Storage{PortableDir: `D:\SSHYard\sshyard_data`, AppDataDir: `C:\AppData\SSHYard`, MoveErr: portable.ErrOccupied}
	if got, want := storageMoveError(m, s), m.StorageMoveOccupied(s.PortableDir); got != want {
		t.Fatalf("to portable: %q, want %q", got, want)
	}
	s.Portable = true
	if got, want := storageMoveError(m, s), m.StorageMoveOccupied(s.AppDataDir); got != want {
		t.Fatalf("back: %q, want %q", got, want)
	}
	s.MoveErr = errors.Join(errors.New("x"), portable.ErrBusy)
	if got := storageMoveError(m, s); got != m.StorageMoveBusy() {
		t.Fatalf("busy: %q", got)
	}
	s.MoveErr = errors.New("disk full")
	if got := storageMoveError(m, s); got != m.StorageMoveFailed("disk full") {
		t.Fatalf("other: %q", got)
	}
}

func TestReportStorageMoveOnlyAfterAFailedMove(t *testing.T) {
	a := &App{p: Platform{Storage: &Storage{}}}
	a.reportStorageMove()
	if len(a.toasts) != 0 {
		t.Fatal("reported a move that did not fail")
	}
	a.p.Storage.MoveErr = portable.ErrBusy
	a.reportStorageMove()
	if len(a.toasts) != 1 || a.toasts[0](locales.In("en")).Type != "error" {
		t.Fatal("failed move not reported")
	}
}
