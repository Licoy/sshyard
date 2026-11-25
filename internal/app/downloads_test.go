package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/assets"
	"github.com/Licoy/sshyard/locales"
)

func TestDownloadsDir(t *testing.T) {
	home, known, custom := t.TempDir(), t.TempDir(), t.TempDir()
	downloads := filepath.Join(home, "Downloads")
	if err := os.Mkdir(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	fileHome := t.TempDir()
	file := filepath.Join(fileHome, "Downloads")
	write(t, file, "")
	missing := filepath.Join(home, "missing")
	for _, tt := range []struct{ name, home, known, want string }{
		{"system Downloads", home, known, known},
		{"unavailable system Downloads", home, "", downloads},
		{"missing system Downloads", home, missing, downloads},
		{"system Downloads is a file", home, file, downloads},
		{"missing home Downloads", known, "", known},
		{"home Downloads is a file", fileHome, "", fileHome},
		{"unknown home", "", "", ""},
		{"relative home", ".", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultDownloadsDir(tt.home, tt.known); got != tt.want {
				t.Fatalf("defaultDownloadsDir = %q, want %q", got, tt.want)
			}
		})
	}
	a := &App{p: Platform{HomeDir: home}}
	for _, tt := range []struct{ name, custom, want string }{
		{"selected directory", custom, custom},
		{"missing custom directory", missing, downloads},
		{"custom directory is a file", file, downloads},
		{"relative custom directory", ".", downloads},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a.settings.DownloadDir = tt.custom
			if got := a.downloadsDir(); got != tt.want {
				t.Fatalf("downloadsDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilesLocalStartsInDownloads(t *testing.T) {
	home, custom, browsed := t.TempDir(), t.TempDir(), t.TempDir()
	downloads, project := filepath.Join(home, "Downloads"), filepath.Join(home, "project")
	for _, dir := range []string{downloads, project} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{p: Platform{HomeDir: home}}
	for _, tt := range []struct{ name, hostDir, custom, want string }{
		{"unconfigured host", "", "", downloads},
		{"configured downloads", "", custom, custom},
		{"host override", "project", custom, project},
		{"invalid host override", "missing", "", downloads},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a.settings.DownloadDir = tt.custom
			cn := &conn{host: assets.Host{LocalDir: tt.hostDir}, fs: &filesState{}}
			a.listLocalFirst(cn)
			if l := &cn.fs.local; !l.loaded || l.dir != tt.want || l.err != "" {
				t.Fatalf("local folder = %q (%v, %q), want %q", l.dir, l.loaded, l.err, tt.want)
			}
			a.goTo(cn, false, browsed)
			a.settings.DownloadDir = custom
			a.listLocalFirst(cn)
			if l := &cn.fs.local; l.dir != browsed || l.err != "" {
				t.Fatalf("local folder after navigation = %q (%q), want %q", l.dir, l.err, browsed)
			}
		})
	}
}

func TestDownloadPickersUseDefaultDir(t *testing.T) {
	preferred, chosen := t.TempDir(), t.TempDir()
	starts := make(chan string, 1)
	a := &App{p: Platform{HomeDir: t.TempDir(), PickFolder: func(_, start string) (string, error) {
		starts <- start
		return chosen, nil
	}}}
	a.settings.DownloadDir = preferred
	m := locales.In("en")
	expectStart := func() {
		t.Helper()
		select {
		case got := <-starts:
			if got != preferred {
				t.Fatalf("picker started in %q, want %q", got, preferred)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("picker did not open")
		}
	}
	a.downloadTo(&conn{fs: &filesState{}}, []string{"/remote.txt"}, m)
	expectStart()

	ask := &xferAsk{mode: xferDownload, open: true, picking: make(chan struct{}), reply: make(chan xferReply, 1)}
	a.xfer.ask = ask
	a.pickXfer(ask, m, true)
	a.settings.DownloadDir = chosen
	expectStart()
	select {
	case got := <-ask.reply:
		if got.dir != chosen {
			t.Fatalf("terminal download destination = %q, want %q", got.dir, chosen)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal download picker did not return")
	}
}
