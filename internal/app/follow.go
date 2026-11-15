package app

import (
	"context"
	"net"
	"path"
	"strings"
	"time"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/layout"
	"github.com/Licoy/sshyard/internal/session"
	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"
)

// cwdSnippet makes bash and zsh report their directory (OSC 7) at every
// prompt, for shells that do not already.
const cwdSnippet = `# bash: ~/.bashrc
PROMPT_COMMAND='printf "\033]7;file://%s%s\033\\" "$HOSTNAME" "$PWD"'"${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
# zsh: ~/.zshrc
_sshyard_cwd() { printf '\033]7;file://%s%s\033\\' "$HOST" "$PWD" }
precmd_functions+=(_sshyard_cwd)`

// noReportAfter is how long a terminal is up without saying where it is
// before a following Files pane offers the snippet.
const noReportAfter = 3 * time.Second

// probeEvery is how often the directory of a shell that does not report
// it is read from the server, while a Files pane follows it.
const probeEvery = 2 * time.Second

// probeTries is how many reads in a row may find nothing before the
// server is taken not to tell: a shell just started may not run yet.
const probeTries = 3

// probeState is how far reading a shell's directory from its server went.
type probeState int

const (
	probeIdle probeState = iota
	probeRunning
	probeFailed // for this connect: not Linux, or the tag was refused
)

// following reports whether the Files pane cn follows the terminal: its
// host allows it and the pane did not stop.
func (a *App) following(cn *conn) bool {
	h, ok := a.host(cn.host.ID)
	if !ok {
		h = cn.host
	}
	return cn.kind == layout.Files && h.CwdFollow && !cn.fs.noFollow
}

// followDir is the folder a shell reported, cleaned ("/a/b/../c/" is
// "/a/c"), or "" if it is not an absolute path (nothing to list).
func followDir(d string) string {
	if !path.IsAbs(d) || strings.ContainsRune(d, 0) {
		return ""
	}
	return path.Clean(d)
}

// followCwd sends each following Files pane to the directory the
// terminal of its tab, on the same host, last reported (OSC 7). With
// several such terminals, the one that reported last leads. While the
// server is read for a shell that does not report (probeCwd), it asks
// for a frame then, notice or none.
func (a *App) followCwd(c *ui.Context) {
	now := c.Now()
	for _, t := range a.ws.Tabs {
		items := t.Items()
		for _, it := range items {
			tc := a.conns[it.ID]
			if tc == nil || tc.kind != layout.Terminal || tc.s == nil {
				continue
			}
			if tc.s.Status().State != session.Connected {
				tc.up = time.Time{}
			} else if tc.up.IsZero() {
				tc.up = now
			}
			if d := followDir(tc.s.Term.Cwd()); d != "" && d != tc.cwd && a.cwdOnHost(tc, tc.s.Term.CwdHost()) {
				a.cwdSeq++
				tc.cwd, tc.cwdSeq = d, a.cwdSeq
				tc.noteCwd(d, tc.s.Status().Connects)
			}
		}
		for _, it := range items {
			fc := a.conns[it.ID]
			if fc == nil || !a.following(fc) {
				continue
			}
			lead := a.leadTerminal(items, fc)
			if lead == nil {
				continue
			}
			if a.probeCwd(lead, now) {
				c.After(probeEvery)
			}
			if lead.cwd == "" {
				continue
			}
			s := &fc.fs.remote
			if s.client == nil {
				// Not connected yet: it lists there when it is.
				s.dir = lead.cwd
				continue
			}
			if fc.fs.followed != lead.cwd {
				fc.fs.followed = lead.cwd
				if s.dir != lead.cwd {
					a.listRemote(s, lead.cwd)
				}
			}
		}
	}
}

// probeCwd reads the directory of tc's shell from its server, for a shell
// that does not report it (OSC 7): the server finds the shell by the tag
// of its environment, or as the connection's one terminal when it took
// no tag (session.ShellCwd). It runs at most every probeEvery, and not
// again on a connection where probeTries reads in a row failed. It
// reports whether it is still reading.
func (a *App) probeCwd(tc *conn, now time.Time) bool {
	if tc.up.IsZero() || tc.s.Term.Cwd() != "" {
		return false
	}
	connects := tc.s.Status().Connects
	if tc.probeConn != connects {
		tc.probeConn, tc.probe, tc.probeAt, tc.probeMiss = connects, probeIdle, time.Time{}, 0
	}
	if tc.probe == probeFailed {
		return false
	}
	if tc.probe != probeIdle || now.Sub(tc.probeAt) < probeEvery {
		return true
	}
	tag, ok := tc.s.Tag()
	if !ok {
		tag = ""
	}
	client := tc.s.Client()
	if client == nil {
		tc.probe = probeFailed
		return false
	}
	tc.probe = probeRunning
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostnameTimeout)
		d, err := session.ShellCwd(ctx, client, tag)
		cancel()
		a.post(func() {
			if tc.probeConn != connects || tc.probe != probeRunning {
				return
			}
			tc.probe, tc.probeAt = probeIdle, now
			if err != nil {
				if tc.probeMiss++; tc.probeMiss >= probeTries {
					tc.probe = probeFailed
				}
				return
			}
			tc.probeMiss = 0
			if tc.s.Term.Cwd() == "" && d != tc.cwd {
				a.cwdSeq++
				tc.cwd, tc.cwdSeq = d, a.cwdSeq
				tc.noteCwd(d, connects)
			}
		})
	}()
	return true
}

// leadTerminal is the terminal of items on fc's host that reported its
// directory last, else any terminal on it, else nil.
func (a *App) leadTerminal(items []layout.Item, fc *conn) *conn {
	var lead *conn
	for _, it := range items {
		tc := a.conns[it.ID]
		if tc == nil || tc.kind != layout.Terminal || tc.host.ID != fc.host.ID {
			continue
		}
		if lead == nil || tc.cwdSeq > lead.cwdSeq {
			lead = tc
		}
	}
	return lead
}

// followAction is the Files pane header's follow switch, there (ok)
// when the host lets its Files panes follow the terminal.
func (a *App) followAction(t *layout.Tab, n *layout.Node, cn *conn, m locales.Messages) (act barAction, ok bool) {
	h, ok := a.host(cn.host.ID)
	if !ok {
		h = cn.host
	}
	if !h.CwdFollow {
		return barAction{}, false
	}
	on := !cn.fs.noFollow
	return barAction{Key: "follow", Button: IconButton{Icon: icons.Follow, On: icons.FollowOn, Label: m.FollowTerminal(), Active: on, Toggle: true, Size: Tiny}, Do: func() {
		t.Focus = n.ID
		cn.fs.noFollow = on
		cn.fs.followed = ""
	}}, true
}

// noReportView says, in a line, when the pane follows a terminal that
// has been up a while without saying where it is, and the server could
// not tell either. The snippet that makes the shell say is a click away.
func (a *App) noReportView(c *ui.Context, t *layout.Tab, cn *conn, tok theme.Tokens, m locales.Messages) {
	if !a.following(cn) || cn.fs.noReportHidden {
		return
	}
	lead := a.leadTerminal(t.Items(), cn)
	if lead == nil || lead.up.IsZero() || lead.s.Term.Cwd() != "" {
		return
	}
	if lead.probe != probeFailed {
		// The server tells (followCwd reads it again in a while).
		return
	}
	if lead.cwd != "" {
		return
	}
	if wait := noReportAfter - c.Now().Sub(lead.up); wait > 0 {
		c.After(wait)
		return
	}
	ui.Row(c.Key("noreport")).FillWidth().AlignItems(ui.Center).Gap(6).Padding(2, 4, 2, 8).Radius(theme.RadiusCard - 2).Background(tok.Fill).Children(func() {
		ui.Icon(c, icons.Follow.SVG).Size(14, 14).Shrink(0).TextColor(tok.TextMuted)
		ui.Text(c, m.FollowNoReport()).FontSize(theme.Footnote).TextColor(tok.TextMuted).SingleLine().Grow(1).Shrink(1)
		if (IconButton{Icon: icons.Info, Label: m.FollowHowTo(), Size: Small}).Clicked(c, "howto") {
			cn.fs.snippetOpen = true
		}
		if (IconButton{Icon: icons.Close, Label: m.HideNotice(), Size: Small}).Clicked(c, "hidenoreport") {
			cn.fs.noReportHidden = true
		}
	})
}

// snippetView is the sheet with the snippet that makes a shell report
// its directory.
func (a *App) snippetView(c *ui.Context, cn *conn, tok theme.Tokens, m locales.Messages) {
	fs := cn.fs
	if !fs.snippetOpen {
		return
	}
	sheet(ui.Modal(c, &fs.snippetOpen, func() {
		ui.Column(c).Gap(14).Width(540).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				tile(c, icons.Follow, theme.Brand, 36)
				sheetTitle(c, m.FollowHowTo())
			})
			ui.Text(c, m.FollowHowToText()).FontSize(theme.Body).TextColor(tok.TextMuted)
			ui.Box(c).FillWidth().Padding(10, 12).Radius(theme.RadiusCard - 2).Background(tok.Fill).Children(func() {
				code := ui.Text(c, cwdSnippet).FontSize(theme.Caption).Selectable()
				if a.fonts.term != "" {
					code.Font(a.fonts.term)
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if pill(c, m.CopySnippet(), false, false, false) {
					c.WriteClipboard(cwdSnippet)
					c.AddToast(ui.Toast{Title: m.Copied(), Type: "success"})
				}
				if pill(c, m.Close(), true, false, false) {
					fs.snippetOpen = false
				}
			})
		})
	}), tok)
}

// nameState is how far asking a terminal's server its hostname went.
type nameState int

const (
	nameUnasked nameState = iota
	nameAsking
	nameKnown
	nameFailed // pinned: name is the first host reported
)

// hostnameTimeout is how long asking the server its hostname may take.
var hostnameTimeout = 10 * time.Second

// cwdOnHost says a directory report naming host is from the terminal's
// own server, not one reached from it (ssh to another host inside the
// terminal): Files panes then follow it. A report naming no host, or
// localhost, counts as the server's own, as does one naming the host's
// address in its settings. Otherwise it is matched against the server's
// hostname, asked once per connection by running "hostname" on it (while
// it is asked, the report waits); should that fail, the first host the
// terminal reports is taken as the server's. Short and full names match
// ("web1" and "web1.example.com").
func (a *App) cwdOnHost(tc *conn, host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") || sameHostName(host, tc.host.Address) {
		return true
	}
	connects := tc.s.Status().Connects
	if tc.nameConn != connects {
		tc.nameConn, tc.name, tc.nameAsk = connects, "", nameUnasked
	}
	switch tc.nameAsk {
	case nameUnasked:
		tc.nameAsk = nameAsking
		a.askHostname(tc, connects)
		return false
	case nameAsking:
		return false
	case nameFailed:
		if tc.name == "" {
			tc.name = host
		}
	}
	return sameHostName(host, tc.name)
}

// askHostname runs "hostname" on the terminal's connection.
func (a *App) askHostname(tc *conn, connects int) {
	client := tc.s.Client()
	go func() {
		name, err := "", error(context.Canceled)
		if client != nil {
			var sess *ssh.Session
			if sess, err = client.NewSession(); err == nil {
				t := time.AfterFunc(hostnameTimeout, func() { sess.Close() })
				var out []byte
				out, err = sess.Output("hostname")
				t.Stop()
				sess.Close()
				name = strings.TrimSpace(string(out))
			}
		}
		if err == nil && (name == "" || strings.ContainsAny(name, " \t\r\n/")) {
			err = context.Canceled
		}
		a.post(func() {
			if tc.nameConn != connects || tc.nameAsk != nameAsking {
				return
			}
			if err != nil {
				tc.nameAsk = nameFailed
				return
			}
			tc.name, tc.nameAsk = name, nameKnown
		})
	}()
}

// sameHostName says a and b name the same host, in any case, a short
// name matching a full one that starts with it.
func sameHostName(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		return false
	}
	sa, _, fa := strings.Cut(a, ".")
	sb, _, fb := strings.Cut(b, ".")
	return fa != fb && strings.EqualFold(sa, sb)
}
