package main

import (
	"runtime"
	"testing"

	"github.com/Licoy/sshyard/internal/theme"
	"github.com/egoist/mygo"
)

func TestThemeSource(t *testing.T) {
	for m, want := range map[theme.Mode]mygo.ThemeSource{theme.Auto: mygo.ThemeSystem, theme.Light: mygo.ThemeLight, theme.Dark: mygo.ThemeDark} {
		if got := themeSource(m); got != want {
			t.Errorf("themeSource(%q) = %q, want %q", m, got, want)
		}
	}
}

// TestAppMenuLeavesAppShortcuts verifies that the macOS menu bar takes none
// of the window's shortcuts the menu would otherwise get first: ⌘W (close
// pane), ⌘R (reconnect) and ⌘=, ⌘- and ⌘0 (zoom). The menu bar is set on
// macOS only, and its roles expand to other platforms' items elsewhere.
func TestAppMenuLeavesAppShortcuts(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the app menu is set on macOS only")
	}
	taken := map[string]bool{"CmdOrCtrl+W": true, "CmdOrCtrl+R": true, "CmdOrCtrl+Plus": true, "CmdOrCtrl+=": true, "CmdOrCtrl+-": true, "CmdOrCtrl+0": true}
	var walk func([]*mygo.MenuItem)
	walk = func(items []*mygo.MenuItem) {
		for _, it := range items {
			if taken[it.Accelerator] {
				t.Errorf("menu item %q (role %q) takes %s", it.Label, it.Role, it.Accelerator)
			}
			walk(it.Submenu)
		}
	}
	walk(appMenu().Items())
}
