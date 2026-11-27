package main

import (
	"fmt"
	"log"
	"runtime"

	"github.com/Licoy/sshyard/internal/app"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (d *desktop) clickTray() {
	if d.app.Settings().TrayClick == settings.TrayClickWindow {
		d.show()
		return
	}
	d.togglePanel()
}

func (d *desktop) togglePanel() {
	if d.panel != nil {
		d.closePanel()
		return
	}
	d.openPanel()
}

func (d *desktop) openPanel() {
	if d.panel != nil {
		d.panel.Focus()
		return
	}
	if d.trayErr != nil {
		d.reportTrayError()
		return
	}
	// NewFlyout activates the app synchronously, before returning its window
	// handle; activation must not restore the hidden main window meanwhile.
	d.openingPanel = true
	defer func() { d.openingPanel = false }()
	d.panelView = d.app.NewTrayPanel(d.trayAction)
	d.panelView.ResizeHandle = d.panelResizeHandle
	d.panelView.SetMaterial = d.syncPanelMaterial
	if err := d.createPanel(); err != nil {
		log.Printf("open tray panel: %v", err)
		d.closePanel()
		d.reportError(locales.TrayPanelFailed())
	}
}

func (d *desktop) createPanel() (err error) {
	// MyGo reports native window creation errors by panic; translate only at
	// this window boundary so the existing main window can display the failure.
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("create tray flyout: %v", failure)
		}
	}()
	stop, err := d.observePanel()
	if err != nil {
		return err
	}
	defer stop()
	d.panel = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray: d.tray, Placement: mygo.PlacementBottom, Gap: 4,
		Width: 372, Height: d.initialPanelHeight(), Focusable: true, Shadow: true, Hidden: true,
		Popover: runtime.GOOS == "darwin", Content: ui.View(d.panelView.View),
	})
	d.panel.OnShow(d.refreshPanel)
	d.panel.OnFocus(d.refreshPanel)
	d.panel.OnClosed(func() {
		d.panelView = nil
		d.panel = nil
		d.panelResize = desktopPanelResize{}
		d.releasePanel()
	})
	// Register lifecycle callbacks before the native popup becomes interactive.
	d.panel.Show()
	if d.panel == nil || !d.panel.IsVisible() {
		return fmt.Errorf("native tray flyout did not become visible")
	}
	d.syncPanelResizeEdge()
	d.refreshPanel()
	return d.watchPanelDismiss()
}

func (d *desktop) closePanel() {
	if d.panel != nil {
		d.panel.Close()
	} else if d.panelView != nil {
		d.panelView = nil
		d.releasePanel()
	}
}

func (d *desktop) refreshPanel() {
	d.matchPanelAppearance()
	if d.panel != nil {
		d.panel.Invalidate()
	}
}

func (d *desktop) trayAction(action app.TrayAction) {
	d.closePanel()
	if action == app.TrayQuit {
		mygo.App.Quit()
		return
	}
	// Locking needs no window, nor does a new one.
	if action == app.TrayLock || action == app.TrayNewWindow {
		d.app.RunTrayAction(action)
		return
	}
	d.show()
	d.app.RunTrayAction(action)
}
