package app

import (
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// WindowControls are the window's buttons, for a window whose view draws
// them itself (Linux, Windows, where the system's are hidden): main hands
// them over once the window is made. On macOS the traffic lights stay and
// there are none.
type WindowControls struct {
	Minimize, Maximize, Unmaximize, Close func()
}

// SetWindow gives the title bar the window's buttons.
func (a *App) SetWindow(w WindowControls) { a.post(func() { a.main.win = &w }) }

// SetMaximized says whether the window is maximized, for the button that
// maximizes or restores it.
func (a *App) SetMaximized(on bool) { a.post(func() { a.main.maximized = on }) }

// titleBar is the window's one band across the top, which drags it: over
// the sidebar room for the traffic lights (macOS); over the content the
// tabs, joining it, and at the end the app's own buttons, then the
// window's buttons where the app draws them. Every control in it centers
// on the band's middle, the traffic lights' line (barLine).
func (a *App) titleBar(c *ui.Context, m locales.Messages, tok theme.Tokens, clear bool) {
	bar := c.TitleBar()
	// Moving the window should leave keyboard input with the current pane.
	row := ui.Row(c.Key("titlebar")).Height(bandHeight(c)).Shrink(0).FillWidth().AlignItems(ui.Center).
		DragWindow().KeepFocus()
	if a.hasBackground() || !clear {
		row.Background(a.chromeFill(tok.Sidebar))
	}
	// Its foot, which the active tab covers, joining the content.
	row.Draw(func(p *ui.Painter, r ui.Rect) {
		y := r.Y + r.H - 0.5
		p.Line(r.X, y, r.X+r.W, y, 1, tok.Line)
	})
	row.Children(func() {
		// Level with the sidebar, and its separator, below, so that the
		// tabs start where the content does.
		lead := ui.Row(c.Key("tblead"))
		if !a.sidebarHidden() {
			lead.Width(max(a.sbWidth+1, bar.Left))
		}
		lead.Shrink(0).FillHeight().
			Padding(0, theme.Space(2), 0, bar.Left+theme.Space(2)).AlignItems(ui.Center).Children(func() {
			if a.page == pageSettings {
				a.settingsBack(c, m, tok)
			} else {
				ui.Row(c.Key("titlebrand")).Shrink(0).Gap(theme.Space(1)).AlignItems(ui.Center).Children(func() {
					ui.Image(c, a.logo).Size(16, 16).Shrink(0).Radius(theme.RadiusSmall)
					ui.Text(c, m.AppName()).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine()
				})
			}
		})
		a.tabStrip(c, m, tok)
		a.windowButtons(c, m, tok)
	})
}

func (a *App) settingsBack(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	b := ui.ButtonBase(c.Key("settings:back")).Label(m.Back()).Shrink(0).
		Height(theme.Space(6)).Padding(0, theme.Space(2)).Gap(theme.Space(1)).Radius(theme.RadiusControl).
		Background(tok.Fill).Border(1, tok.Line).
		AlignItems(ui.Center).Justify(ui.Start).Cursor(ui.CursorPointer)
	if b.Pressed() {
		b.Background(tok.Press)
	} else if b.Hovered() {
		b.Background(tok.Hover)
	}
	b.Children(func() {
		ui.Icon(c, icons.Back.SVG).Size(Tiny, Tiny).TextColor(tok.TextMuted)
		ui.Text(c, m.Back()).FontSize(theme.Footnote).SingleLine().Shrink(1)
	})
	if b.Clicked() {
		a.closeSettings()
	}
}

// appButtons are the app's own IconButtons in the title bar: updates, its
// source on GitHub, feedback (GitHub issues or the QQ group) and the
// settings. Updates show in the accent while a
// newer version is out; a click checks, and the update window shows what
// it found with the release notes.
func (a *App) appButtons(c *ui.Context, m locales.Messages) {
	v := a.upd.Available()
	label := m.CheckForUpdates()
	if v != "" {
		label = m.UpdateAvailable(v)
	}
	if (IconButton{Icon: icons.Update, Label: label, Active: v != ""}).Clicked(c, "update") {
		a.upd.Check()
	}
	if iconButton(c, "github", icons.GitHub, m.GitHubRepo()) {
		c.OpenURL(repoURL)
	}
	a.menuButton(c, "feedback", IconButton{Icon: icons.Feedback, Label: m.Feedback()}, func() []menuEntry {
		return []menuEntry{
			{Icon: icons.GitHub, Label: m.FeedbackGitHub(), Do: func() { c.OpenURL(issuesURL) }},
			{Icon: icons.QQ, Label: m.FeedbackQQGroup(), Do: func() { c.OpenURL(qqGroupURL) }},
		}
	})
	if a.page == pageSettings {
		if (IconButton{Icon: icons.Close, Label: m.CloseSettings(), Danger: true, Prominent: true}).Clicked(c, "settings") {
			a.closeSettings()
		}
	} else if iconButton(c, "settings", icons.Settings, m.Settings()) {
		a.openSettings(a.prefNow())
	}
}

// barLine is a row of the title bar's controls, centered on the band's
// middle, where the traffic lights are and a tab's title is.
func barLine(c *ui.Context, key string) ui.Element {
	return ui.Row(c.Key(key)).FillHeight().Shrink(0).AlignItems(ui.Center)
}

// windowButtons minimize, maximize or restore, and close the window, where
// the app draws them (WindowControls).
func (a *App) windowButtons(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	w := a.win
	if w == nil {
		return
	}
	barLine(c, "winbtns").Gap(theme.Space(1)).Padding(0, 10, 0, 0).Children(func() {
		ui.Box(c).Width(1).Height(20).Shrink(0).Margin(0, 6, 0, 0).Background(tok.Line)
		if iconButton(c, "winmin", icons.WinMinimize, m.WindowMinimize()) {
			w.Minimize()
		}
		if a.maximized {
			if iconButton(c, "winmax", icons.WinRestore, m.WindowRestore()) {
				w.Unmaximize()
			}
		} else if iconButton(c, "winmax", icons.WinMaximize, m.WindowMaximize()) {
			w.Maximize()
		}
		if (IconButton{Icon: icons.Close, Label: m.WindowClose(), Danger: true}).Clicked(c, "winclose") {
			w.Close()
		}
	})
}
