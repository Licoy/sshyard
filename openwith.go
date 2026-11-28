package main

import (
	"context"
	"os/exec"
	"runtime"

	"github.com/Licoy/sshyard/internal/app"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// openWith lets the user choose the app that opens path: the system's
// own chooser on Windows, an app picked in Applications on macOS, a
// program in /usr/bin on Linux.
func openWith(path, title string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "shell32.dll,OpenAs_RunDLL", path)
	case "darwin":
		apps, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, DefaultPath: "/Applications", Filters: []mygo.FileFilter{{Name: title, Extensions: []string{"app"}}}})
		if err != nil {
			return err
		}
		if len(apps) == 0 {
			return context.Canceled
		}
		cmd = exec.Command("open", "-a", apps[0], path)
	default:
		progs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, DefaultPath: "/usr/bin"})
		if err != nil {
			return err
		}
		if len(progs) == 0 {
			return context.Canceled
		}
		cmd = exec.Command(progs[0], path)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// workspaceWindows are the open windows of the workspace but the main
// one, which hide and show with it, and close without asking as the app
// quits. Main thread only.
var workspaceWindows = map[*mygo.Window]bool{}

// openWindow opens a window beside the main one: a file editor, or
// another window of the workspace.
func openWindow(s app.WindowSpec) app.Window {
	var w *mygo.Window
	mygo.RunOnMain(func() {
		prepareWindowState(s.StateKey)
		opts := mygo.WindowOptions{
			Title:           s.Title,
			Width:           s.Width,
			Height:          s.Height,
			MinWidth:        420,
			MinHeight:       300,
			StateKey:        s.StateKey,
			BackgroundColor: "light-dark(#EDEDF0,#28282A)",
			Content:         ui.View(s.View),
		}
		if s.Placed {
			opts.X, opts.Y = s.X, s.Y
		}
		if s.Workspace {
			// As the main window (main.go).
			opts.MinWidth, opts.MinHeight = 640, 420
			opts.TitleBarStyle = mygo.TitleBarHiddenInset
			opts.TitleBarHeight = 40
			opts.TrafficLightPosition = &mygo.Point{X: 14, Y: 13}
			opts.HideWindowButtons = ownWindowButtons()
			opts.Vibrancy = windowVibrancy()
		}
		w = mygo.NewWindow(opts)
		rememberWindow(w, s.StateKey)
		if s.Workspace {
			workspaceWindows[w] = true
			w.OnClosed(func() { delete(workspaceWindows, w) })
			if ownWindowButtons() && s.Controls != nil {
				s.Controls(app.WindowControls{
					Minimize:   func() { go w.Minimize() },
					Maximize:   func() { go w.Maximize() },
					Unmaximize: func() { go w.Unmaximize() },
					Close:      func() { go w.Close() },
				})
				if s.Maximized != nil {
					s.Maximized(w.IsMaximized())
					w.OnMaximize(func() { s.Maximized(true) })
					w.OnUnmaximize(func() { s.Maximized(false) })
				}
			}
		}
		w.OnClose(func(e *mygo.CloseEvent) {
			if s.Closing != nil && !s.Closing() {
				e.PreventDefault()
			}
		})
		w.OnClosed(func() {
			if s.Closed != nil {
				s.Closed()
			}
		})
	})
	return window{w}
}

// window is a mygo.Window as an app.Window.
type window struct{ w *mygo.Window }

func (w window) Focus() { w.w.Show() }

// Close closes the window without asking: the app already did.
func (w window) Close()                { w.w.Destroy() }
func (w window) SetTitle(title string) { w.w.SetTitle(title) }
func (w window) SetEdited(edited bool) { w.w.SetDocumentEdited(edited) }

// Position is where the window's top-left corner is on the screen.
func (w window) Position() (x, y int) {
	mygo.RunOnMain(func() { x, y = w.w.Position() })
	return x, y
}
