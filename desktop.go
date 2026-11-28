package main

import (
	"errors"
	"log"
	"runtime"

	"github.com/Licoy/sshyard/icon"
	"github.com/Licoy/sshyard/internal/app"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo"
)

// postUI never runs fn inline: callers may still hold application locks.
// RunOnMain itself is synchronous, including when called from the UI thread.
func postUI(fn func()) { go mygo.RunOnMain(fn) }

// desktop owns only native lifecycle state; hiding retains the same App and view.
// Its callbacks and all settings reads run on MyGo's UI thread.
type desktop struct {
	app                *app.App
	window             *mygo.Window
	tray               *mygo.Tray
	trayErr            error
	quitting           bool
	changingVisibility bool
	panel              *mygo.Window
	panelView          *app.TrayPanel
	panelNative        desktopPanelNative
	openingPanel       bool
	panelResize        desktopPanelResize
}

func newDesktop(a *app.App, w *mygo.Window) *desktop {
	d := &desktop{app: a, window: w}
	d.createTray()
	w.OnClose(d.close)
	mygo.App.OnBeforeQuit(d.beforeQuit)
	// The workspace's windows closed for the quit open again.
	mygo.App.OnQuitCanceled(func() { d.app.SetQuitting(false) })
	mygo.App.OnActivate(func(bool) { d.activate() })
	if d.trayErr != nil {
		log.Printf("create tray: %v", d.trayErr)
		if a.Settings().CloseBehavior == settings.CloseToTray {
			d.reportTrayError()
		}
		return d
	}
	d.updateMenu()
	mygo.App.OnLocaleChanged(func(string) {
		d.updateMenu()
		d.refreshPanel()
	})
	mygo.Theme.OnUpdated(d.refreshPanel)
	if runtime.GOOS != "linux" {
		d.tray.OnClick(d.clickTray)
		d.tray.OnRightClick(func() {
			d.closePanel()
			d.tray.SetMenu(d.menu())
			defer d.tray.SetMenu(nil)
			d.tray.PopUpMenu()
		})
	}
	mygo.App.OnQuit(d.tray.Destroy)
	d.tray.OnAppearanceChange(d.refreshTrayIcon)
	d.refreshTrayIcon()
	return d
}

func (d *desktop) createTray() {
	png, template := icon.TrayPNG(d.app.Settings().TrayIcon, false)
	d.tray, d.trayErr = mygo.NewTray(mygo.TrayOptions{
		ID: "sshyard.main", Icon: png, IconIsTemplate: template, ToolTip: "SSHYard",
	})
}

func (d *desktop) setTrayIcon(style string) error {
	err := d.trayErr
	if err == nil {
		png, template := icon.TrayPNG(style, d.tray.Dark())
		err = d.tray.SetIcon(png, template)
	}
	if err != nil {
		log.Printf("update tray icon: %v", err)
		return errors.New(locales.TrayIconFailed())
	}
	return nil
}

func (d *desktop) refreshTrayIcon() {
	if err := d.setTrayIcon(d.app.Settings().TrayIcon); err != nil {
		d.reportError(err.Error())
	}
}

func (d *desktop) menu() *mygo.Menu {
	items := []*mygo.MenuItem{}
	if runtime.GOOS == "linux" {
		items = append(items, &mygo.MenuItem{Label: locales.TrayOpenHosts(), Click: func(*mygo.MenuItem, *mygo.Window) { d.openPanel() }})
	}
	for _, item := range []struct {
		label  string
		action app.TrayAction
	}{
		{locales.TrayShowWindow(), app.TrayOpenMain},
		{locales.Settings(), app.TraySettings},
		{locales.QuickConnect(), app.TrayQuickConnect},
		{locales.LocalTerminal(), app.TrayLocalTerminal},
		{locales.NewHost(), app.TrayNewHost},
		{locales.NewWindow(), app.TrayNewWindow},
		{locales.AppLockAction(), app.TrayLock},
	} {
		items = append(items, &mygo.MenuItem{Label: item.label, Click: func(*mygo.MenuItem, *mygo.Window) { d.trayAction(item.action) }})
	}
	items = append(items, mygo.Separator(), &mygo.MenuItem{Label: locales.TrayQuit(), Click: func(*mygo.MenuItem, *mygo.Window) { d.trayAction(app.TrayQuit) }})
	return mygo.NewMenu(items)
}

func (d *desktop) updateMenu() {
	// AppIndicator has no direct click event; its menu is the primary action.
	if runtime.GOOS == "linux" {
		d.tray.SetMenu(d.menu())
	}
}

// activate handles an explicit reopen (for example a Dock click), not focus.
func (d *desktop) activate() {
	if d.changingVisibility || d.openingPanel {
		return
	}
	if !d.window.IsVisible() || d.window.IsMinimized() {
		d.show()
	}
}

func (d *desktop) show() {
	d.closePanel()
	d.changingVisibility = true
	defer func() { d.changingVisibility = false }()
	if runtime.GOOS == "darwin" {
		mygo.App.Dock.Show()
	} else {
		d.window.SetSkipTaskbar(false)
	}
	d.window.Restore()
	d.window.Show()
	for w := range workspaceWindows {
		w.Show()
	}
	d.app.SetMinimized(false)
}

func (d *desktop) close(e *mygo.CloseEvent) {
	if d.quitting {
		return
	}
	e.PreventDefault()
	if d.app.Settings().CloseBehavior == settings.CloseQuit {
		// Quit the whole app, including secondary windows; a cancelled editor
		// close must keep the main window and its drafts alive too.
		mygo.App.Quit()
		return
	}
	if d.trayErr != nil {
		d.reportTrayError()
		return
	}
	d.app.SetMinimized(true)
	d.changingVisibility = true
	defer func() { d.changingVisibility = false }()
	d.window.Hide()
	for w := range workspaceWindows {
		w.Hide()
	}
	if runtime.GOOS == "darwin" {
		mygo.App.Dock.Hide()
	} else {
		d.window.SetSkipTaskbar(true)
	}
}

func (d *desktop) beforeQuit(e *mygo.QuitEvent) {
	d.closePanel()
	if e.SessionEnding {
		d.quitting = true
		d.app.SetQuitting(true)
		return
	}
	if !d.app.AllowQuit(mygo.App.Quit) {
		e.PreventDefault()
		d.show()
		return
	}
	// Ask editors before MyGo closes the workspace windows.
	for _, w := range mygo.Windows() {
		// The workspace's windows keep their tabs for the layout.
		if w == d.window || w.IsFlyout() || workspaceWindows[w] {
			continue
		}
		w.Close()
		if !w.IsDestroyed() {
			e.PreventDefault()
			d.show()
			return
		}
	}
	d.quitting = true
	d.app.SetQuitting(true)
	// A later before/will-quit listener may cancel; reset after this native
	// quit sequence rather than leaving future window closes in quit mode.
	postUI(func() { d.quitting = false })
}

func (d *desktop) reportTrayError() {
	d.reportError(locales.TrayUnavailable())
}

func (d *desktop) reportError(message string) {
	d.show()
	d.app.ReportError(message)
}
