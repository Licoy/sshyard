package app

import (
	"os"
	"path/filepath"
)

// downloadsDir resolves the shared starting folder for downloads.
func (a *App) downloadsDir() string {
	if dir := a.settings.DownloadDir; isDownloadDir(dir) {
		return dir
	}
	var systemDir string
	if a.p.HomeDir == "" {
		systemDir = systemDownloadsDir()
	}
	return defaultDownloadsDir(a.localHome(), systemDir)
}

func defaultDownloadsDir(home, systemDir string) string {
	if isDownloadDir(systemDir) {
		return systemDir
	}
	if !filepath.IsAbs(home) {
		return ""
	}
	if dir := filepath.Join(home, "Downloads"); isDownloadDir(dir) {
		return dir
	}
	return home
}

func isDownloadDir(dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}
