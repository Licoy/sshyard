// SSHYard is a cross-platform SSH client with a native interface: hosts,
// tabs and split panes of terminals drawn by its own VT engine, and SFTP.
package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Licoy/sshyard/internal/app"
	"github.com/Licoy/sshyard/internal/awake"
	"github.com/Licoy/sshyard/internal/gtkfix"
	"github.com/Licoy/sshyard/internal/keychain"
	"github.com/Licoy/sshyard/internal/macfix"
	"github.com/Licoy/sshyard/internal/netwatch"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/internal/updater"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func themeSource(m theme.Mode) mygo.ThemeSource {
	switch m {
	case theme.Light:
		return mygo.ThemeLight
	case theme.Dark:
		return mygo.ThemeDark
	}
	return mygo.ThemeSystem
}

// platform is the app's hooks into MyGo.
func platform() app.Platform {
	return app.Platform{
		Post:           postUI,
		ResolveStorage: storagePaths,
		Version:        mygo.App.Version(),
		Secrets:        keychain.System{},
		Updates:        updater.MygoBackend{},
		SetTheme:       func(m theme.Mode) { mygo.Theme.SetSource(themeSource(m)) },
		SetLocale:      mygo.App.SetLocale,
		OpenPath:       mygo.Shell.OpenPath,
		CopySecret: func(text string) error {
			return mygo.Clipboard.Write(transfer.TextData(text), mygo.ClipboardOptions{Concealed: true})
		},
		PickFile: func(title string) (string, error) {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, ShowHiddenFiles: true})
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		},
		PickFiles: func(title string) ([]string, error) {
			return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, Multiple: true, ShowHiddenFiles: true})
		},
		SaveFile: func(title, path string) (string, error) {
			return mygo.Dialog.Save(mygo.SaveDialogOptions{Title: title, DefaultPath: path, CreateDirectories: true})
		},
		PickFolder: func(title, start string) (string, error) {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, DefaultPath: start, Directory: true, CreateDirectories: true, ShowHiddenFiles: true})
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		},
		OpenWith:     openWith,
		OpenWindow:   openWindow,
		WatchNetwork: netwatch.Watch,
		Awake:        awake.System(),
	}
}

func storagePaths() (app.StoragePaths, error) {
	canonical, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return app.StoragePaths{}, err
	}
	dir := os.Getenv("SSHYARD_DATA_DIR")
	if dir == "" {
		dir = canonical
	}
	return app.StoragePaths{DataDir: dir, LockPath: filepath.Join(canonical, "secrets.lock"), Scope: mygo.App.Name()}, nil
}

// updatePlugin is the update window, which checks soon after launch and
// at the interval of the saved settings while their automatic checks are
// on. It reads them before the app runs, so a new interval applies at the
// next launch; the app keeps the automatic checks in step after.
func updatePlugin() mygo.Plugin {
	s := settings.Default()
	if p, err := storagePaths(); err == nil {
		if saved, err := settings.Load(filepath.Join(p.DataDir, "settings.json")); err == nil {
			s = saved
		}
	}
	return updater.Plugin(time.Duration(s.UpdateIntervalHours)*time.Hour, s.CheckUpdates)
}

// windowVibrancy is the material behind the window: the sidebar material
// on macOS only (on Windows a material window loses its menu bar, and
// Linux has none).
func windowVibrancy() mygo.Vibrancy {
	if runtime.GOOS == "darwin" {
		return mygo.VibrancySidebar
	}
	return mygo.VibrancyNone
}

// ownWindowButtons says the app draws the window's buttons in its title
// bar: on Linux and Windows. macOS keeps its traffic lights.
func ownWindowButtons() bool { return runtime.GOOS != "darwin" }

// appMenu is MyGo's default menu bar (macOS) with Close Window on ⇧⌘W:
// ⌘W closes the focused pane, as in Terminal, and the menu would take it
// before the window does.
func appMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Role: mygo.RoleFileMenu, Submenu: []*mygo.MenuItem{{Role: mygo.RoleClose, Accelerator: "CmdOrCtrl+Shift+W"}}},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	})
}

func main() {
	mygo.Use(updatePlugin())
	if runtime.GOOS == "darwin" {
		mygo.App.SetMenu(appMenu())
	}
	mygo.App.WhenReady(func() {
		// Linux: size the window buttons' bar to its buttons, and keep text
		// in its color with cairo before 1.17.8 (see gtkfix).
		gtkfix.Apply()
		gtkfix.TextColor()
		// Startup failures are presented with safe, localized diagnostics in the UI.
		var d *desktop
		p := platform()
		p.SetTrayIcon = func(style string) error { return d.setTrayIcon(style) }
		p.SetRememberLayout = changeRememberLayout
		p.Notify = func(id, title, body string, clicked func()) error {
			n := mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body})
			n.OnClick(func() {
				postUI(d.show)
				clicked()
			})
			return n.Show()
		}
		a, _ := app.New(p)
		installWindowStates(a)
		mygo.App.OnQuit(a.Close)
		prepareWindowState("main")
		w := mygo.NewWindow(mygo.WindowOptions{
			Title:     "SSHYard",
			Width:     1200,
			Height:    760,
			MinWidth:  820,
			MinHeight: 520,
			StateKey:  "main",
			// One unified toolbar: the page draws the title area, the
			// window controls sit over it, inset on macOS.
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TitleBarHeight:       40,
			TrafficLightPosition: &mygo.Point{X: 14, Y: 13},
			// Linux and Windows: the title bar draws the window's
			// buttons, in the app's look (app.WindowControls).
			HideWindowButtons: ownWindowButtons(),
			// The sidebar's glass: the system's material on macOS, where
			// the sidebar leaves its fill clear; a solid fill elsewhere.
			Vibrancy:        windowVibrancy(),
			BackgroundColor: "light-dark(#EDEDF0,#28282A)",
			// Native UI only: no web view anywhere in SSHYard.
			Content: ui.View(a.View),
		})
		rememberWindow(w, "main")
		// macOS: right clicks over the title bar reach the tabs (see macfix),
		// once the window has made MyGo's view class.
		macfix.TitleBarClicks()
		d = newDesktop(a, w)
		a.Start()
		go updater.CleanLeftovers()
		w.OnMinimize(func() { a.SetMinimized(true) })
		w.OnRestore(func() { a.SetMinimized(false) })
		if ownWindowButtons() {
			// Off the frame that clicked them: closing the window there
			// would take it down under the view.
			a.SetWindow(app.WindowControls{
				Minimize:   func() { go w.Minimize() },
				Maximize:   func() { go w.Maximize() },
				Unmaximize: func() { go w.Unmaximize() },
				Close:      func() { go w.Close() },
			})
			a.SetMaximized(w.IsMaximized())
			w.OnMaximize(func() { a.SetMaximized(true) })
			w.OnUnmaximize(func() { a.SetMaximized(false) })
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
