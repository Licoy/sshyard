package app

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/applock"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// methodLabel names a method of the app lock.
func methodLabel(m locales.Messages, method applock.Method) string {
	switch method {
	case applock.Password:
		return m.AppLockPassword()
	case applock.TOTP:
		return m.AppLockTotp()
	case applock.Both:
		return m.AppLockBoth()
	}
	return m.AppLockOff()
}

// idleLabel names a choice of applock.IdleChoices.
func idleLabel(m locales.Messages, minutes int) string {
	if minutes <= 0 {
		return m.AppLockNever()
	}
	return m.AppLockMinutes(strconv.Itoa(minutes))
}

// lockView is the main window while the app is locked: the fields that
// open it, and the store's unlock it may need for a one-time code.
func (a *App) lockView(c *ui.Context) {
	base := *c.Theme()
	base.Font = a.fonts.ui
	tok := theme.For(a.settings.Theme, base.Dark)
	c.SetTheme(tok.Apply(base))
	m := locales.In(c.Locale())
	l := &a.lock
	wait := a.waitText(m)
	if wait != "" {
		c.After(time.Second)
	}
	ui.Column(c).Fill().Background(tok.Canvas).TextColor(tok.Text).Children(func() {
		ui.Row(c.Key("locked-title")).Height(bandHeight(c)).FillWidth().DragWindow().Children(func() {
			ui.Spacer(c)
			a.windowButtons(c, m, tok)
		})
		ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(theme.Space(4)).Children(func() {
			ui.Image(c, a.logo).Size(64, 64).Radius(theme.RadiusSheet).Label(m.AppName())
			sheetTitle(c, m.AppLockedTitle())
			// A reset waits for the app to start again; a cancelled quit
			// leaves it here.
			if l.resetting {
				ui.Text(c, m.AppLockResetPending()).FontSize(theme.Body).TextColor(tok.TextMuted).TextAlign(ui.Center).MaxWidth(420)
				if pill(c, m.AppLockResetRestart(), true, false, false) {
					a.relaunch()
				}
				return
			}
			if l.broken {
				ui.Text(c, m.AppLockBroken()).FontSize(theme.Body).TextColor(tok.TextMuted).TextAlign(ui.Center).MaxWidth(420)
				if l.err != "" {
					ui.Text(c, l.err).FontSize(theme.Footnote).TextColor(tok.Danger).TextAlign(ui.Center).MaxWidth(420)
				}
				// The store's record of the lock restores it.
				ui.Row(c).Gap(theme.Space(2)).Children(func() {
					if pill(c, m.AppLockResetEllipsis(), false, true, false) {
						l.resetOpen = true
					}
					if !a.unlocked() && pill(c, m.SecretsRetry(), true, false, a.unlock.busy) {
						a.withSecrets(nil)
					}
				})
				return
			}
			method := l.cfg.Method
			prompt := m.AppLockedPasswordPrompt()
			switch method {
			case applock.TOTP:
				prompt = m.AppLockedCodePrompt()
			case applock.Both:
				prompt = m.AppLockedBothPrompt()
			}
			ui.Text(c, prompt).FontSize(theme.Body).TextColor(tok.TextMuted).TextAlign(ui.Center).MaxWidth(420)
			ui.Column(c).Width(320).Gap(theme.Space(3)).Children(func() {
				ui.Form(c, func() {
					first := true
					field := func(key, label string, value *string, code bool) {
						ui.Field(c, label, func() {
							in := formTextInput(c.Key(key), value).Disabled(l.busy)
							if code {
								in.MaxLength(applock.Digits)
							} else {
								in.Password()
							}
							if first {
								in.AutoFocus()
								l.hold.apply(c, l.gen, in)
								first = false
							}
							// A code alone opens as soon as it is typed.
							if in.Submitted() || code && !method.NeedsPassword() && in.Changed() && len(*value) == applock.Digits {
								a.SubmitAppUnlock(m)
							}
						})
					}
					if method.NeedsPassword() {
						field("lock:pw:"+strconv.Itoa(l.gen), m.AppLockPassword(), &l.password, false)
					}
					if method.NeedsCode() {
						field("lock:code:"+strconv.Itoa(l.gen), m.AppLockCode(), &l.code, true)
					}
				})
				if msg := cmp.Or(wait, l.err); msg != "" {
					ui.Text(c, msg).FontSize(theme.Footnote).TextColor(tok.Danger)
				}
				ui.Row(c).FillWidth().Children(func() {
					if pill(c, m.AppLockResetEllipsis(), false, false, l.busy) {
						l.resetOpen = true
					}
					ui.Spacer(c)
					if pill(c, m.AppLockUnlock(), true, false, l.busy || wait != "") {
						a.SubmitAppUnlock(m)
					}
				})
			})
		})
	})
	if ui.AlertDialog(c, &l.resetOpen, m.AppLockResetTitle(), m.AppLockResetBody(), m.Cancel(), m.AppLockReset()) == 1 {
		if msg := a.ResetAppLock(m); msg != "" {
			l.err = msg
		}
	}
	a.unlockModal(c, m, tok)
	a.masterPasswordView(c, m)
}

// lockedWindowView is an editor's window while the app is locked: it
// opens in the main window, and the window's title names no file.
func (a *App) lockedWindowView(c *ui.Context, cn *conn) {
	base := *c.Theme()
	base.Font = a.fonts.ui
	tok := theme.For(a.settings.Theme, base.Dark)
	c.SetTheme(tok.Apply(base))
	c.Root().Background(tok.Canvas)
	m := locales.In(c.Locale())
	if ew := cn.ed; ew.win != nil && ew.title != m.AppName() {
		ew.title = m.AppName()
		w, title := ew.win, ew.title
		go w.SetTitle(title)
	}
	ui.Column(c).Fill().TextColor(tok.Text).AlignItems(ui.Center).Justify(ui.Center).Gap(theme.Space(4)).Children(func() {
		glyph(c, icons.Lock, tok.TextMuted, 32)
		sheetTitle(c, m.AppLockedTitle())
		if pill(c, m.AppLockShowMain(), true, false, false) {
			a.activateMainWindow()
		}
	})
}

// lockFormView is the sheet setting the app lock up, changing or
// removing it.
func (a *App) lockFormView(c *ui.Context, m locales.Messages) {
	f := a.lock.form
	if f == nil {
		return
	}
	tok := currentTokens(c)
	cur := a.lock.cfg
	title, action := m.AppLockEnableTitle(), m.Save()
	switch f.mode {
	case lockChange:
		title = m.AppLockChangeTitle()
	case lockDisable:
		title, action = m.AppLockDisableTitle(), m.AppLockTurnOff()
	}
	a.ensureSecret(f)
	sheet(stickyModal(c, &a.lock.formOpen, func() {
		ui.Column(c).Gap(18).Width(460).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, icons.Lock, tok.TintAt(6), 36)
				sheetTitle(c, title)
			})
			// Each field submits the sheet; the first one holds the focus.
			first := true
			field := func(key, label string, value *string, code bool) {
				ui.Field(c, label, func() {
					in := formTextInput(c.Key(key), value).Disabled(f.busy)
					if code {
						in.MaxLength(applock.Digits)
					} else {
						in.Password()
					}
					if first {
						in.AutoFocus()
						a.lock.formHold.apply(c, f, in)
						first = false
					}
					if in.Submitted() {
						a.SubmitLockForm(m)
					}
				})
			}
			next := f.next()
			ui.Form(c, func() {
				if f.mode != lockEnable {
					if cur.Method.NeedsPassword() {
						field("lf:curpw", m.AppLockCurrentPassword(), &f.curPassword, false)
					}
					if cur.Method.NeedsCode() {
						field("lf:curcode", m.AppLockCurrentCode(), &f.curCode, true)
					}
				}
				if f.mode == lockDisable {
					return
				}
				labels := make([]string, len(applock.Methods))
				for i, method := range applock.Methods {
					labels[i] = methodLabel(m, method)
				}
				ui.Field(c, m.AppLockMethod(), func() {
					formSegmented(c.Key("lf:method"), &f.method, f.busy, labels...).Label(m.AppLockMethod())
				})
				if next.NeedsPassword() {
					field("lf:pw", m.AppLockPassword(), &f.password, false)
					field("lf:confirm", m.ConfirmPassword(), &f.confirm, false)
				}
				switch {
				case !next.NeedsCode():
				case !a.freshSecret(f):
					ui.Field(c, "", func() {
						ui.Text(c, m.AppLockKeepAuthenticator()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
					})
				default:
					ui.Field(c, "", func() {
						ui.Column(c).Gap(8).FillWidth().MinWidth(0).Children(func() {
							ui.Text(c, m.AppLockScan()).FontSize(theme.Footnote).TextColor(tok.TextMuted)
							ui.Row(c).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
								if f.qr != nil {
									ui.Image(c, f.qr).Size(132, 132).Shrink(0).Radius(theme.RadiusSmall).Label(m.AppLockQrAlt())
								}
								ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
									ui.Text(c, m.AppLockSetupKey()).FontSize(theme.Caption).TextColor(tok.TextMuted)
									ui.Row(c).Gap(4).AlignItems(ui.Center).FillWidth().Children(func() {
										ui.Text(c, groupKey(applock.EncodeSecret(f.secret))).FontSize(theme.Footnote).Font(a.fonts.term).Selectable().Shrink(1).MinWidth(0)
										if iconButton(c, "lf:copykey", icons.Copy, m.AppLockCopyKey()) {
											a.copySecret(c, applock.EncodeSecret(f.secret))
										}
									})
								})
							})
						})
					})
					field("lf:code", m.AppLockCode(), &f.code, true)
				}
			})
			if f.err != "" {
				ui.Text(c, f.err).FontSize(theme.Footnote).TextColor(tok.Danger)
			}
			ui.Box(c).Height(1).FillWidth().Background(tok.Line)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.Cancel(), false, false, false) {
					a.lock.formOpen = false
				}
				if pill(c, action, true, f.mode == lockDisable, f.busy) {
					a.SubmitLockForm(m)
				}
			})
		})
	}), tok)
	if !a.lock.formOpen && a.lock.form == f {
		a.closeLockForm()
	}
}

// groupKey is a setup key in groups of four, as it is easier to type.
func groupKey(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// securityPage is the settings' page of the app lock.
func (a *App) securityPage(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	cfg := a.lock.cfg
	on := cfg.Enabled()
	a.settingsSection(c, tok, icons.Lock, m.AppLockSection(), func(g *settingsGroup) {
		g.row(m.AppLockMethod(), methodLabel(m, cfg.Method), func() {
			if !on {
				if pill(c, m.AppLockEnableEllipsis(), false, false, false) {
					a.openLockForm(lockEnable)
				}
				return
			}
			if pill(c, m.AppLockDisableEllipsis(), false, true, false) {
				a.openLockForm(lockDisable)
			}
			if pill(c, m.AppLockChangeEllipsis(), false, false, false) {
				a.openLockForm(lockChange)
			}
		})
		g.row(m.AppLockIdle(), m.AppLockIdleNote(), func() {
			labels := make([]string, len(applock.IdleChoices))
			for i, n := range applock.IdleChoices {
				labels[i] = idleLabel(m, n)
			}
			sel := idleLabel(m, cfg.IdleMinutes)
			if formSelect(c.Key("lockidle"), &sel, labels).Label(m.AppLockIdle()).Width(140).Changed() {
				if i := slices.Index(labels, sel); i >= 0 {
					if err := a.setLockIdle(applock.IdleChoices[i]); err != nil {
						c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
					}
				}
			}
		})
		g.row(m.AppLockOnSleep(), "", func() {
			sleep := cfg.LockOnSleep
			if formSwitch(c.Key("locksleep"), &sleep).Label(m.AppLockOnSleep()).Changed() {
				if err := a.setLockOnSleep(sleep); err != nil {
					c.AddToast(ui.Toast{Title: m.ErrGeneric(err.Error()), Type: "error"})
				}
			}
		})
		g.row(m.AppLockNow(), "", func() {
			if pill(c, m.AppLockAction(), false, false, !on) {
				a.LockApp()
			}
		})
	})
	ui.Text(c, m.AppLockScopeNote()).FontSize(theme.Footnote).TextColor(tok.TextMuted).Padding(0, 4)
}
