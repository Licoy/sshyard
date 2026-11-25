package folder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/backup"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	des, _ := os.ReadDir(dir)
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

func TestFolderRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := &Folder{Dir: filepath.Join(t.TempDir(), "SSHYard")}
	if err := f.Check(ctx); err != nil {
		t.Fatalf("check before the folder is made: %v", err)
	}
	if objs, err := f.List(ctx); err != nil || objs != nil {
		t.Fatalf("list before the folder is made = %v, %v", objs, err)
	}
	old, newer := backup.Name(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "ab12"), backup.Name(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), "cd34")
	for _, n := range []string{old, newer} {
		if err := f.Put(ctx, n, []byte("sealed "+n)); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(f.Dir, "notes.txt"), []byte("x"), 0o600)
	objs, err := f.List(ctx)
	if err != nil || len(objs) != 2 || objs[0].Name != newer || objs[1].Name != old || objs[0].Device != "cd34" || objs[0].Size == 0 {
		t.Fatalf("list = %+v, %v", objs, err)
	}
	if got := names(t, f.Dir); !slices.Equal(got, []string{old, newer, "notes.txt"}) {
		t.Fatalf("folder holds %v: a temporary file stayed", got)
	}
	if fi, _ := os.Stat(filepath.Join(f.Dir, old)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", fi.Mode())
	}
	if b, err := f.Get(ctx, old); err != nil || string(b) != "sealed "+old {
		t.Fatalf("get = %q, %v", b, err)
	}
	if err := f.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(ctx, old); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
	if err := f.Delete(ctx, old); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("delete deleted = %v", err)
	}
}

func TestFolderRefuses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, d := range []string{"", "relative/SSHYard"} {
		if err := (&Folder{Dir: d}).Check(ctx); !errors.Is(err, ErrPath) {
			t.Fatalf("check %q = %v", d, err)
		}
	}
	if err := (&Folder{Dir: filepath.Join(dir, "missing", "SSHYard")}).Check(ctx); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("check without parent = %v", err)
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if err := (&Folder{Dir: file}).Check(ctx); !errors.Is(err, backup.ErrNotFound) {
		t.Fatalf("check of a file = %v", err)
	}
	f := &Folder{Dir: dir}
	for _, n := range []string{"../" + backup.Name(time.Now(), "ab12"), "notes.txt"} {
		if err := f.Put(ctx, n, []byte("x")); err == nil {
			t.Fatalf("put %q succeeded", n)
		}
		if _, err := f.Get(ctx, n); err == nil {
			t.Fatalf("get %q succeeded", n)
		}
	}
}

func TestFolderPlaceholder(t *testing.T) {
	ctx := context.Background()
	f := &Folder{Dir: t.TempDir()}
	n := backup.Name(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "ab12")
	ph := filepath.Join(f.Dir, "."+n+".icloud")
	os.WriteFile(ph, []byte("plist"), 0o600)
	objs, err := f.List(ctx)
	if err != nil || len(objs) != 1 || objs[0].Name != n || objs[0].Size != 0 {
		t.Fatalf("list = %+v, %v", objs, err)
	}
	if err := f.Delete(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ph); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("placeholder stayed")
	}
}

func TestKnown(t *testing.T) {
	home := t.TempDir()
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{home}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	icloud := mk("Library", "Mobile Documents", "com~apple~CloudDocs")
	onedrive := mk("Library", "CloudStorage", "OneDrive-Personal")
	mk("Library", "CloudStorage", "GoogleDrive-me@example.com")
	dropbox := mk("Dropbox")
	none := func(string) string { return "" }
	got := Known("darwin", home, none)
	want := []Place{{ICloudDrive, icloud}, {"OneDrive", onedrive}, {"Dropbox", dropbox}}
	if !slices.Equal(got, want) {
		t.Fatalf("darwin = %v, want %v", got, want)
	}
	if p, ok := PlaceOf(got, filepath.Join(icloud, "SSHYard")); !ok || p.Name != ICloudDrive {
		t.Fatalf("place of a folder in iCloud Drive = %v, %v", p, ok)
	}
	if _, ok := PlaceOf(got, filepath.Join(home, "Library")); ok {
		t.Fatal("a folder outside every place has one")
	}

	winDrive, biz := mk("iCloudDrive"), mk("OneDrive - Work")
	env := func(k string) string {
		switch k {
		case "OneDrive", "OneDriveCommercial":
			return biz
		case "OneDriveConsumer":
			return filepath.Join(home, "missing")
		}
		return ""
	}
	if got, want := Known("windows", home, env), []Place{{ICloudDrive, winDrive}, {"OneDrive", biz}, {"Dropbox", dropbox}}; !slices.Equal(got, want) {
		t.Fatalf("windows = %v, want %v", got, want)
	}

	sync := mk("Sync")
	if got, want := Known("linux", home, none), []Place{{"Dropbox", dropbox}, {"Syncthing", sync}}; !slices.Equal(got, want) {
		t.Fatalf("linux = %v, want %v", got, want)
	}
	if got := Known("linux", "", none); got != nil {
		t.Fatalf("no home = %v", got)
	}
}
