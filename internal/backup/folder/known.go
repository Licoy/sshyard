package folder

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Place is a sync client's folder found on this computer.
type Place struct {
	// Name is the sync client's, which is not translated.
	Name string
	// Path is the folder's full path.
	Path string
}

// ICloudDrive is the name Known gives iCloud Drive.
const ICloudDrive = "iCloud"

// Known are the sync clients' folders on this computer, iCloud Drive first,
// for the operating system goos, the user's home and environment getenv.
// Only folders that are there are given.
func Known(goos, home string, getenv func(string) string) []Place {
	var cands []Place
	join := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	switch goos {
	case "darwin":
		cands = append(cands, Place{ICloudDrive, join("Library", "Mobile Documents", "com~apple~CloudDocs")})
		// File Provider clients keep their folders in CloudStorage, named
		// for the client and the account.
		if des, err := os.ReadDir(join("Library", "CloudStorage")); err == nil {
			for _, de := range des {
				for _, client := range []string{"Dropbox", "OneDrive", "Box", "pCloud"} {
					if n := de.Name(); n == client || strings.HasPrefix(n, client+"-") {
						cands = append(cands, Place{client, join("Library", "CloudStorage", n)})
					}
				}
			}
		}
		cands = append(cands, Place{"Dropbox", join("Dropbox")})
	case "windows":
		cands = append(cands, Place{ICloudDrive, join("iCloudDrive")})
		for _, env := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
			if p := getenv(env); p != "" {
				cands = append(cands, Place{"OneDrive", p})
			}
		}
		cands = append(cands, Place{"Dropbox", join("Dropbox")})
	default:
		cands = append(cands,
			Place{"Dropbox", join("Dropbox")},
			Place{"Nextcloud", join("Nextcloud")},
			Place{"ownCloud", join("ownCloud")},
			Place{"Syncthing", join("Sync")})
	}
	var out []Place
	for _, p := range cands {
		if !filepath.IsAbs(p.Path) || slices.ContainsFunc(out, func(o Place) bool { return o.Path == filepath.Clean(p.Path) }) {
			continue
		}
		if fi, err := os.Stat(p.Path); err == nil && fi.IsDir() {
			out = append(out, Place{p.Name, filepath.Clean(p.Path)})
		}
	}
	return out
}

// PlaceOf is the known place holding dir, if any.
func PlaceOf(places []Place, dir string) (Place, bool) {
	dir = filepath.Clean(dir)
	for _, p := range places {
		if rel, err := filepath.Rel(p.Path, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p, true
		}
	}
	return Place{}, false
}
