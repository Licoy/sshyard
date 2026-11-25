package app

import "golang.org/x/sys/windows"

func systemDownloadsDir() string {
	dir, _ := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	return dir
}
