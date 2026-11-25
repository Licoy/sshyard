package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
)

func TestChooseDownloadDir(t *testing.T) {
	start, chosen := t.TempDir(), t.TempDir()
	m := locales.In("en")
	for _, tc := range []struct {
		name, path string
		err        error
		want       string
	}{
		{"selected", chosen, nil, chosen},
		{"canceled", "", nil, start},
		{"picker error", chosen, errors.New("picker failed"), start},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := t.TempDir()
			pump := newDispatchPump()
			a, err := New(Platform{
				DataDir: data, Secrets: keychain.NewMemory(), Post: pump.post,
				PickFolder: func(_ string, dir string) (string, error) {
					if dir != start {
						t.Errorf("picker opened in %q; want %q", dir, start)
					}
					return tc.path, tc.err
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Close)
			s := a.Settings()
			s.DownloadDir = start
			if err := a.ApplySettings(s); err != nil {
				t.Fatal(err)
			}
			a.chooseDownloadDir(m, a.downloadsDir())
			s.OpenConfirmMB++
			if err := a.ApplySettings(s); err != nil {
				t.Fatal(err)
			}
			pump.next(t)
			saved, err := settings.Load(filepath.Join(data, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			if a.Settings().DownloadDir != tc.want || saved.DownloadDir != tc.want || saved.OpenConfirmMB != s.OpenConfirmMB {
				t.Fatalf("active folder %q, saved folder %q, max file size %d; want %q, %d", a.Settings().DownloadDir, saved.DownloadDir, saved.OpenConfirmMB, tc.want, s.OpenConfirmMB)
			}
			if (len(a.toasts) > 0) != (tc.err != nil) {
				t.Errorf("unexpected error toasts: %d, picker error: %v", len(a.toasts), tc.err)
			}
		})
	}
}
