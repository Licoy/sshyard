package launch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	wd := t.TempDir()
	abs := filepath.Join(wd, "a", "b")
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"none", nil, ""},
		{"relaunch", []string{RelaunchFlag}, ""},
		{"path beside relaunch flag", []string{RelaunchFlag, "--path", "a/b"}, abs},
		{"relaunch flag as folder", []string{"--", RelaunchFlag}, filepath.Join(wd, RelaunchFlag)},
		{"path", []string{"--path", "a/b"}, abs},
		{"path equals", []string{"--path=a/b"}, abs},
		{"short", []string{"-p", "a/b"}, abs},
		{"positional", []string{"a/b"}, abs},
		{"after terminator", []string{"--", "a/b"}, abs},
		{"absolute", []string{abs}, abs},
		{"platform flag", []string{"-psn_0_123", "a/b"}, abs},
		{"path after flag", []string{"-psn_0_123", "--path", "a/b"}, abs},
		{"last path wins", []string{"--path", "a", "--path", "a/b"}, abs},
		{"missing value", []string{"--path"}, ""},
		{"value is flag", []string{"--path", "--other"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Dir(tc.args, wd); got != tc.want {
				t.Errorf("Dir(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestDirHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home folder")
	}
	if got, want := Dir([]string{"--path", "~"}, ""), filepath.Clean(home); got != want {
		t.Errorf("~ = %q, want %q", got, want)
	}
	if got, want := Dir([]string{"--path", "~/x"}, ""), filepath.Join(home, "x"); got != want {
		t.Errorf("~/x = %q, want %q", got, want)
	}
}

func TestDirRelativeToWorkingDir(t *testing.T) {
	wd := t.TempDir()
	if got, want := Dir([]string{"."}, wd), filepath.Clean(wd); got != want {
		t.Errorf("Dir(.) = %q, want %q", got, want)
	}
}
