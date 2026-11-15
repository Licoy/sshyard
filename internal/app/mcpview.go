package app

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/mcp"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// prefAgents is the MCP / Skill page: the switch for agent access,
// which asks first (mcpSheet), and while on, the open API, the MCP
// endpoint, the skill and the connections agents hold.
func (a *App) prefAgents(c *ui.Context, m locales.Messages, tok theme.Tokens, s *settings.Settings, changed *bool) {
	on := s.MCPEnabled
	srv := a.mcp.srv
	if on {
		aiBanner(c, tok.Warning, icons.Warning, m.McpOnBanner(), nil)
	}
	offNote := func(g *settingsGroup) {
		ui.Row(c).MinHeight(44).Padding(8, 14).Children(func() {
			ui.Text(c, m.AgentsOffNote()).FontSize(theme.Body).TextColor(tok.TextMuted)
		})
	}
	copyRow := func(g *settingsGroup, label, value, key, copyLabel string) {
		g.row(label, value, func() {
			if iconButton(c, key, icons.Copy, copyLabel) {
				c.WriteClipboard(value)
				c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
			}
		})
	}

	a.settingsSection(c, tok, icons.API, m.ApiSection(), func(g *settingsGroup) {
		g.row(m.McpEnable(), m.McpEnableNote(), func() {
			sw := on
			if formSwitch(c.Key("mcpon"), &sw).Label(m.McpEnable()).Changed() {
				if sw {
					a.mcpAsk = &mcpAsk{open: true}
				} else {
					s.MCPEnabled = false
					*changed = true
				}
			}
		})
		if !on {
			return
		}
		g.row(m.FieldPort(), "", func() {
			if ui.NumberInput(c.Key("mcpport"), &a.mcpPort, 1024, 65535, 1).Label(m.FieldPort()).Changed() {
				s.MCPPort = int(a.mcpPort)
				*changed = true
			}
		})
		if a.mcp.err != nil {
			g.row(m.McpFailed(a.mcp.err.Error()), "", func() {})
		}
		if srv == nil {
			return
		}
		copyRow(g, m.ApiAddress(), srv.Base()+mcp.APIPath, "api:url", m.ApiCopyURL())
		g.row(m.McpToken(), m.McpTokenNote(), func() {
			ui.Text(c, "••••••••••••").FontSize(theme.Body).TextColor(tok.TextMuted)
			if iconButton(c, "mcp:token", icons.Copy, m.McpCopyToken()) {
				a.copySecret(c, a.mcp.token)
				c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
			}
			if (IconButton{Icon: icons.Refresh, Label: m.McpNewToken(), Danger: true}).Clicked(c, "mcp:newtoken") {
				a.withSecrets(func() {
					if err := a.newMCPToken(); err != nil {
						if a.mcp.err == nil && a.mcp.skillErr != nil {
							a.toastError(m.McpTokenRotatedSkillFailed(skillErrorText(m, a.mcp.skillErr)))
						} else {
							a.toastError(m.ErrGeneric(secretStoreErrorText(m, err)))
						}
					} else {
						a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: m.McpNewTokenDone(), Type: "success"} })
					}
				})
			}
		})
		g.divider()
		ui.Row(c).Padding(8, 14).Children(func() {
			if pill(c, m.ApiCopyCurl(), false, false, false) {
				c.WriteClipboard(a.apiExample())
				c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
			}
		})
	})

	a.settingsSection(c, tok, icons.MCP, m.McpSection(), func(g *settingsGroup) {
		if srv == nil {
			offNote(g)
			return
		}
		copyRow(g, m.McpAddress(), srv.URL(), "mcp:url", m.McpCopyURL())
		g.divider()
		ui.Row(c).Padding(8, 14).Children(func() {
			if pill(c, m.McpCopyConfig(), false, false, false) {
				c.WriteClipboard(a.mcpConfig())
				c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
			}
		})
	})

	a.skillSettings(c, m, tok, srv != nil)

	if !on {
		return
	}
	a.settingsSection(c, tok, icons.Host, m.McpSessions(), func(g *settingsGroup) {
		list := a.mcpSessionList()
		if len(list) == 0 {
			ui.Row(c).MinHeight(44).Padding(8, 14).Children(func() {
				ui.Text(c, m.McpNoSessions()).FontSize(theme.Body).TextColor(tok.TextMuted)
			})
		}
		for _, ss := range list {
			id := ss.id
			g.row(ss.host.Label(), a.pv(ss.host.Address)+" · "+ss.since.Format("15:04:05"), func() {
				if (IconButton{Icon: icons.Disconnect, Label: m.McpDisconnect(), Danger: true}).Clicked(c, "mcp:drop:"+id) {
					a.dropMCPSession(id)
				}
			})
		}
	})
}

// skillSettings is the skill's section: a prompt to install it from the
// server, and for each agent found, where it stands with a button to
// inject, update or remove it. Injecting needs the server on; removing
// does not.
func (a *App) skillSettings(c *ui.Context, m locales.Messages, tok theme.Tokens, serving bool) {
	a.clearResolvedSkillErrors()
	a.settingsSection(c, tok, icons.Skill, m.SkillSection(), func(g *settingsGroup) {
		if a.mcp.skillErr != nil {
			g.row(m.SkillRefreshFailed(skillErrorText(m, a.mcp.skillErr)), "", func() {})
		}
		g.row(m.SkillAbout(), m.SkillAboutNote(), func() {
			if (IconButton{Icon: icons.Copy, Label: m.SkillCopyPrompt(), Disabled: !serving}).Clicked(c, "skill:prompt") {
				c.WriteClipboard(a.skillPrompt())
				c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
			}
		})
		found := 0
		for _, t := range a.skillTargets() {
			st := a.skillStateOf(t)
			if st != skillNoAgent {
				found++
			}
			note := map[skillState]string{skillNoAgent: m.SkillNoAgent(), skillMissing: m.SkillMissing(),
				skillCurrent: m.SkillCurrent(), skillStale: m.SkillStale(), skillConflict: m.SkillConflict(), skillUnavailable: m.SkillUnavailable()}[st]
			g.row(t.name, a.homePath(filepath.Dir(t.file()))+" · "+note, func() {
				in := IconButton{Icon: icons.Download, Label: m.SkillInstallTo(t.name), Size: Small,
					Disabled: !serving || st == skillNoAgent || st == skillCurrent || st == skillConflict || st == skillUnavailable}
				if st == skillStale {
					in.Icon, in.Label = icons.Refresh, m.SkillUpdateIn(t.name)
				}
				if in.Clicked(c, "skill:in:"+t.id) {
					if err := a.installSkill(t); err != nil {
						c.AddToast(ui.Toast{Title: skillErrorText(m, skillTargetError{target: t.name, cause: err}), Type: "error"})
					} else {
						c.AddToast(ui.Toast{Title: m.SkillInstalled(t.name), Type: "success"})
					}
				}
				rm := IconButton{Icon: icons.Delete, Label: m.SkillRemoveFrom(t.name), Size: Small, Danger: true,
					Disabled: st != skillCurrent && st != skillStale}
				if rm.Clicked(c, "skill:rm:"+t.id) {
					if err := a.removeSkill(t); err != nil {
						c.AddToast(ui.Toast{Title: skillErrorText(m, skillTargetError{target: t.name, cause: err}), Type: "error"})
					} else {
						c.AddToast(ui.Toast{Title: m.SkillRemoved(t.name), Type: "success"})
					}
				}
			})
		}
		g.divider()
		ui.Row(c).Padding(8, 14).Children(func() {
			if pill(c, m.SkillInstallAll(), true, false, !serving || found == 0) {
				n, err := a.installSkills()
				if err != nil {
					c.AddToast(ui.Toast{Title: skillErrorText(m, err), Type: "error"})
				} else {
					c.AddToast(ui.Toast{Title: m.SkillInstalledAll(strconv.Itoa(n)), Type: "success"})
				}
			}
		})
	})
}

func skillErrorText(m locales.Messages, err error) string {
	var lines []string
	for _, item := range skillErrorList(err) {
		var failure skillTargetError
		if !errors.As(item, &failure) {
			lines = append(lines, m.SkillWriteFailed())
			continue
		}
		reason := m.SkillWriteFailed()
		switch {
		case errors.Is(failure.cause, errSkillConflict):
			reason = m.SkillConflict()
		case errors.Is(failure.cause, os.ErrPermission):
			reason = m.SkillPermissionDenied()
		case failure.read:
			reason = m.SkillReadFailed()
		}
		lines = append(lines, failure.target+": "+reason)
	}
	return strings.Join(lines, "; ")
}

// homePath is p with the home folder written ~.
func (a *App) homePath(p string) string {
	home := a.p.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if rel, err := filepath.Rel(home, p); err == nil && home != "" && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}

// mcpSheet lays out the risks of the endpoint before it is turned on.
// The confirm button stays disabled for mcpWait after the sheet shows,
// counting down, so it cannot be clicked through unread.
func (a *App) mcpSheet(c *ui.Context, m locales.Messages, tok theme.Tokens) {
	ask := a.mcpAsk
	if ask == nil || a.unlock.modal {
		return
	}
	now := c.Now()
	if ask.at.IsZero() {
		ask.at = now
	}
	left := mcpWait - now.Sub(ask.at)
	secs := int((left + time.Second - 1) / time.Second)
	if left > 0 {
		// Next frame when the count changes.
		c.After(left - time.Duration(secs-1)*time.Second)
	}
	sheet(ui.Modal(c.Key("mcprisk"), &ask.open, func() {
		ui.Column(c).Gap(14).Width(500).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, icons.Warning, tok.Danger, 36)
				sheetTitle(c, m.McpRiskTitle())
			})
			ui.Text(c, m.McpRiskIntro()).FontSize(theme.Body).FillWidth()
			ui.Column(c).Gap(10).FillWidth().Padding(12, 14).Radius(theme.RadiusControl).
				Background(tok.Danger.Alpha(0.12)).Border(1, tok.Danger.Alpha(0.22)).Children(func() {
				for _, risk := range []string{m.McpRiskCommands(), m.McpRiskNoAsk(), m.McpRiskInjection(), m.McpRiskSkill(), m.McpRiskSecrets(), m.McpRiskLocal()} {
					ui.Row(c).Gap(theme.Space(2)).FillWidth().Children(func() {
						ui.Icon(c, icons.Warning.SVG).Size(14, 14).Shrink(0).TextColor(tok.Danger).Margin(2, 0, 0, 0)
						ui.Text(c, risk).FontSize(theme.Footnote).Grow(1).Shrink(1)
					})
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				el, cancel := pillEl(c, m.Cancel(), false, false, false)
				a.mcpHold.apply(c, ask, el)
				if cancel {
					ask.open = false
				}
				label := m.McpConfirm()
				if secs > 0 {
					label = m.McpConfirmWait(strconv.Itoa(secs))
				}
				if pill(c, label, false, true, secs > 0) {
					a.withSecrets(func() {
						if a.mcpAsk != ask || !ask.open {
							return
						}
						if err := a.setMCP(true); err != nil {
							a.toastError(m.ErrGeneric(secretStoreErrorText(m, err)))
							return
						}
						ask.open = false
						a.mcpAsk = nil
					})
				}
			})
		})
	}), tok)
	if a.mcpAsk == ask && !ask.open {
		a.mcpAsk = nil
	}
}
