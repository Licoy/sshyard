package portable

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still there: %v", path, err)
	}
}

func dirs(t *testing.T) (exe, appData string) {
	root := t.TempDir()
	exe, appData = filepath.Join(root, "app"), filepath.Join(root, "config", "SSHYard")
	if err := os.MkdirAll(exe, 0o700); err != nil {
		t.Fatal(err)
	}
	return exe, appData
}

func TestApplyMovesDataBesideAndBack(t *testing.T) {
	exe, appData := dirs(t)
	write(t, filepath.Join(appData, "hosts.json"), "hosts")
	write(t, filepath.Join(appData, "secrets.enc"), "sealed")
	write(t, filepath.Join(appData, "ai-history", "h1"), "chat")
	write(t, filepath.Join(appData, lockFile), "")

	if err := Request(exe, true); err != nil {
		t.Fatal(err)
	}
	if err := Apply(exe, appData, 0); err != nil {
		t.Fatal(err)
	}
	dir := Path(exe)
	if !Active("windows", exe) || !Active("linux", exe) {
		t.Fatal("data not beside the executable")
	}
	if read(t, filepath.Join(dir, "hosts.json")) != "hosts" || read(t, filepath.Join(dir, "secrets.enc")) != "sealed" || read(t, filepath.Join(dir, "ai-history", "h1")) != "chat" {
		t.Fatal("data not copied")
	}
	gone(t, filepath.Join(dir, lockFile))
	gone(t, appData)
	gone(t, appData+movedSuffix)
	gone(t, filepath.Join(exe, requestFile))

	if err := Request(exe, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(exe, appData, 0); err != nil {
		t.Fatal(err)
	}
	if Active("windows", exe) {
		t.Fatal("data still beside the executable")
	}
	if read(t, filepath.Join(appData, "secrets.enc")) != "sealed" || read(t, filepath.Join(appData, "ai-history", "h1")) != "chat" {
		t.Fatal("data not moved back")
	}
	gone(t, dir)
	gone(t, dir+movedSuffix)
}

func TestApplyWithoutDataMakesEmptyFolder(t *testing.T) {
	exe, appData := dirs(t)
	if err := Request(exe, true); err != nil {
		t.Fatal(err)
	}
	if err := Apply(exe, appData, 0); err != nil {
		t.Fatal(err)
	}
	if !Active("linux", exe) {
		t.Fatal("no portable folder")
	}
	gone(t, appData)
}

func TestApplyWithoutRequestMovesNothing(t *testing.T) {
	exe, appData := dirs(t)
	write(t, filepath.Join(appData, "hosts.json"), "hosts")
	if err := Apply(exe, appData, 0); err != nil {
		t.Fatal(err)
	}
	if Active("windows", exe) || read(t, filepath.Join(appData, "hosts.json")) != "hosts" {
		t.Fatal("moved without a request")
	}
}

func TestApplyKeepsDataWhenDestinationHoldsData(t *testing.T) {
	exe, appData := dirs(t)
	write(t, filepath.Join(Path(exe), "hosts.json"), "portable")
	write(t, filepath.Join(appData, "hosts.json"), "other")
	if err := Request(exe, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(exe, appData, 0); !errors.Is(err, ErrOccupied) {
		t.Fatalf("err = %v, want ErrOccupied", err)
	}
	if read(t, filepath.Join(Path(exe), "hosts.json")) != "portable" || read(t, filepath.Join(appData, "hosts.json")) != "other" {
		t.Fatal("data changed")
	}
	gone(t, filepath.Join(exe, requestFile))
}

func TestApplyReplacesLeftoverLock(t *testing.T) {
	exe, appData := dirs(t)
	write(t, filepath.Join(Path(exe), "hosts.json"), "portable")
	write(t, filepath.Join(appData, lockFile), "")
	if err := Request(exe, false); err != nil {
		t.Fatal(err)
	}
	if err := Apply(exe, appData, 0); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(appData, "hosts.json")) != "portable" {
		t.Fatal("data not moved back")
	}
}

func TestApplyKeepsDataInUse(t *testing.T) {
	exe, appData := dirs(t)
	write(t, filepath.Join(appData, "hosts.json"), "hosts")
	l, err := keychain.Lock(filepath.Join(appData, lockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := Request(exe, true); err != nil {
		t.Fatal(err)
	}
	if err := Apply(exe, appData, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if Active("windows", exe) || read(t, filepath.Join(appData, "hosts.json")) != "hosts" {
		t.Fatal("data in use moved")
	}
	gone(t, Path(exe)+".tmp")
}

func TestSupported(t *testing.T) {
	exe, _ := dirs(t)
	if Supported("darwin", exe) {
		t.Fatal("macOS supported")
	}
	if !Supported("windows", exe) || !Supported("linux", exe) {
		t.Fatal("writable copy not supported")
	}
	write(t, filepath.Join(exe, "Uninstall.exe"), "")
	if Supported("windows", exe) {
		t.Fatal("installed copy supported")
	}
	if es, _ := os.ReadDir(exe); len(es) != 1 {
		t.Fatalf("probe left files: %v", es)
	}
}
