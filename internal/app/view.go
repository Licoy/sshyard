package app

import (
	"time"

	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// View builds the window.
func (a *App) View(c *ui.Context) {
	a.mu.Lock()
	a.svc, a.live = c.Services(), true
	a.mu.Unlock()
	// Native callbacks run between frames and use the real clock; the Tester
	// uses its frame clock so clipboard snapshots and toast Undo expire together.
	if a.p.Post == nil {
		a.clip.now = func() time.Time { return c.Now() }
	}
	a.drain()
	a.applyZoom(c, true)
	for _, t := range a.toasts {
		c.AddToast(t(locales.In(c.Locale())))
	}
	a.toasts = nil
	if !a.ready() {
		a.unlockView(c)
		return
	}
	a.commitMetricsMount()
	a.syncMetrics()
	a.wakeUndo(c)
	a.followCwd(c)
	a.watchTmux(c)
	a.watchLogs(locales.In(c.Locale()))
	for _, cn := range a.conns {
		if cn.fs != nil {
			a.pendingOpens(cn)
		}
	}

	base := *c.Theme()
	base.Font = a.fonts.ui
	tok := theme.For(a.settings.Theme, base.Dark)
	c.SetTheme(tok.Apply(base))
	m := locales.In(c.Locale())
	a.localName = m.LocalTerminal()
	a.nameTools(m)
	a.bc.synced = a.syncSet()
	a.fontToasts(c, m)

	a.shortcuts(c)
	// The window: one title bar across the top, on the window's material
	// (or its solid fill) as the sidebar under it, which holds the hosts,
	// or the settings' pages while they show, beside the canvas of the
	// content. In the workspace the content's bottom band is the status
	// bar, and a rail of IconButtons runs down its right edge, with the
	// panel it opens beside the panes.
	// Each part shows the material as the appearance settings say: the
	// sidebar and the title bar (clear), and the content (clearContent),
	// through a lighter fill of its own.
	a.ensureBackground()
	imageBackground := a.hasBackground()
	vibrant := c.Vibrancy()
	clear := vibrant && a.settings.ChromeMaterial != settings.MaterialOpaque
	a.clearContent = imageBackground || vibrant && a.settings.ContentMaterial == settings.MaterialTranslucent
	if vibrant {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(tok.Sidebar)
	}
	a.drawBackground(c, tok)
	ui.Column(c).Fill().TextColor(tok.Text).Children(func() {
		a.titleBar(c, m, tok, clear)
		if a.showSecretsStatus() && a.page != pageSettings {
			a.secretsBanner(c, m, tok)
		}
		// The sidebar and the content, split at a line the user drags to
		// size the sidebar; hidden, the content takes the whole width.
		sidebar := func() {
			sb := ui.Box(c.Key("sidebar")).Fill()
			if imageBackground || !clear {
				sb.Background(a.chromeFill(tok.Sidebar))
			}
			sb.Children(func() {
				c.SetTheme(tok.SidebarTheme(base, clear))
				if a.page == pageSettings {
					a.settingsNav(c, m, tok)
				} else {
					a.sidebarView(c, m, tok)
				}
				c.SetTheme(tok.Apply(base))
				a.drawBackground(c, tok)
				if vibrant && !imageBackground {
					c.Root().Background(ui.Transparent)
				}
			})
		}
		content := func() {
			ui.Column(c.Key("main")).Fill().MinWidth(0).Background(a.contentFill(tok.Canvas, canvasAlpha)).Children(func() {
				ui.Row(c.Key("body")).Grow(1).Shrink(1).MinWidth(0).FillWidth().MinHeight(0).Children(func() {
					ui.Column(c.Key("content")).Grow(1).Shrink(1).MinWidth(0).FillHeight().Background(a.contentRegionFill(tok.Canvas)).Children(func() {
						switch a.page {
						case pageSettings:
							a.settingsPage(c, m, tok)
						default:
							a.terminalPage(c, m, tok)
						}
					})
					if a.page != pageWorkspace {
						return
					}
					a.backgroundSeparator(c, tok.GlassEdge, a.chromeFill(tok.Glass)).Width(1).FillHeight().Shrink(0)
					a.railView(c, m, tok)
				})
				if a.page == pageWorkspace {
					a.statusBar(c, m, tok)
				}
			})
		}
		if a.sidebarHidden() {
			ui.Row(c.Key("frame")).Grow(1).Shrink(1).MinHeight(0).FillWidth().Children(content)
			return
		}
		a.sbWidth = min(max(a.sbWidth, sidebarMin), sidebarMax)
		ui.Split(c.Key("frame"), &a.sbWidth, sidebar, content).Grow(1).Shrink(1).MinHeight(0).FillWidth()
		a.saveSidebarWidth(c)
	})
	a.editorView(c, m)
	a.groupView(c, m)
	a.multiSelectView(c, m)
	a.tabRenameView(c, m)
	a.snippetSheets(c, m)
	a.credView(c, m)
	a.proxyView(c, m)
	a.targetView(c, m)
	a.remoteListView(c, m)
	a.backupView(c, m)
	a.defaultPasswordView(c, m)
	a.fwdView(c, m)
	a.fwdDeleteView(c, m)
	a.highlightView(c, m)
	a.installView(c, m)
	a.quickView(c, m)
	a.paletteView(c, m)
	a.importView(c, m)
	a.migrationView(c, m)
	a.promptsView(c, m)
	a.oscAskView(c, m)
	a.xferAskView(c, m)
	a.xferDropView(c, m)
	a.aiSheets(c, m)
	a.aiAttachmentPreviewView(c, m, tok)
	a.contextMenuView(c)
	a.costView(c, m)
	a.unlockModal(c, m, tok)
	a.masterPasswordView(c, m)
	a.observeLayout(c)
}

// sidebarMin and sidebarMax bound the width the sidebar is dragged to.
const sidebarMin, sidebarMax = 160, 480

// sidebarHidden reports whether the hosts' sidebar is hidden: the
// settings' pages always show theirs.
func (a *App) sidebarHidden() bool {
	return a.settings.SidebarHidden && a.page != pageSettings
}

// setSidebarHidden hides the hosts' sidebar, or shows it again.
func (a *App) setSidebarHidden(on bool) {
	st := a.settings
	st.SidebarHidden = on
	a.ApplySettings(st)
}

// saveSidebarWidth saves the sidebar's width once it rests where it was
// dragged to.
func (a *App) saveSidebarWidth(c *ui.Context) {
	if a.sbWidth == a.settings.SidebarWidth {
		return
	}
	if a.sbWidth != a.sbSeen {
		a.sbSeen, a.sbAt = a.sbWidth, c.Now()
	}
	if wait := splitRest - c.Now().Sub(a.sbAt); wait > 0 {
		c.After(wait)
		return
	}
	st := a.settings
	st.SidebarWidth = a.sbWidth
	a.ApplySettings(st)
}
