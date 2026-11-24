// Package launch reads the folder a command line asks a local terminal
// to open in.
package launch

import (
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the folder args ask a local terminal to open in, "" for
// none. It takes --path DIR, --path=DIR, -p DIR, or the first argument
// that is not a flag; relative paths are relative to wd, and a leading
// "~" is the user's home folder. Flags of the platform and the launcher
// are ignored, such as macOS' -psn_0_….
func Dir(args []string, wd string) string {
	raw := ""
	afterFlags := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case !afterFlags && arg == "--":
			afterFlags = true
		case !afterFlags && (arg == "--path" || arg == "-p"):
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				raw = args[i]
			}
		case !afterFlags && strings.HasPrefix(arg, "--path="):
			raw = strings.TrimPrefix(arg, "--path=")
		case !afterFlags && strings.HasPrefix(arg, "-"):
			// A flag of the platform or the launcher, not a folder.
		default:
			if raw == "" {
				raw = arg
			}
		}
	}
	if raw == "" {
		return ""
	}
	return resolve(raw, wd)
}

// resolve makes dir absolute, under home for "~", and relative to wd
// when it is not absolute yet.
func resolve(dir, wd string) string {
	switch {
	case dir == "~":
		dir = home()
	case strings.HasPrefix(dir, "~/"), strings.HasPrefix(dir, `~\`):
		dir = filepath.Join(home(), dir[2:])
	}
	if !filepath.IsAbs(dir) {
		if wd == "" {
			wd, _ = os.Getwd()
		}
		dir = filepath.Join(wd, dir)
	}
	return filepath.Clean(dir)
}

// home is the user's home folder, "" when it cannot be found.
func home() string {
	home, _ := os.UserHomeDir()
	return home
}
