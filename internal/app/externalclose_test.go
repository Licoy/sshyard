package app

import (
	"testing"

	"github.com/Licoy/sshyard/internal/layout"
)

func TestExternalEditsGatePaneAndTabClose(t *testing.T) {
	a, _ := newTestApp(t)
	fs := a.newFiles()
	stopped := false
	fs.edits = []*extEdit{{state: extFailed, cancel: func() { stopped = true }}}
	files := &conn{id: 2, kind: layout.Files, fs: fs}
	term := &conn{id: 1, kind: layout.Terminal}
	a.conns[term.id], a.conns[files.id] = term, files
	tab := a.ws.Open(term.item())
	if !a.ws.Place(files.item(), term.id, layout.Right) {
		t.Fatal("could not place files pane")
	}

	a.CloseSession(files.id)
	if !fs.extCloseAsk || a.conns[files.id] != files || stopped {
		t.Fatal("closing the pane did not wait for confirmation")
	}
	fs.extCloseAsk = false
	a.extConfirmClose(files)
	a.drain()
	if a.conns[files.id] != files || stopped {
		t.Fatal("canceling did not preserve the pane and edit")
	}

	a.closeTab(tab)
	if !fs.extCloseAsk || a.conns[term.id] != term || stopped {
		t.Fatal("closing the tab did not keep its other panes while asking")
	}
	a.extConfirmClose(files)
	a.drain()
	if a.conns[files.id] != nil || a.conns[term.id] != nil || !stopped {
		t.Fatal("confirmation did not close the tab and stop the edit")
	}
}

func TestExternalEditsGateWindowCloseAndQuit(t *testing.T) {
	a, _ := newTestApp(t)
	w := newWinState(2, layout.New())
	a.wins = append(a.wins, w)
	fs := a.newFiles()
	fs.edits = []*extEdit{{state: extFailed, cancel: func() {}}}
	files := &conn{id: 1, kind: layout.Files, fs: fs}
	a.conns[files.id] = files
	w.ws.Open(files.item())

	a.closeWindowSessions(w)
	a.drain()
	if !fs.extCloseAsk || a.focusWin != w || a.conns[files.id] != files {
		t.Fatal("closing the window did not ask on its files pane")
	}
	fs.extCloseAsk = false

	retries := 0
	retry := func() { retries++ }
	if a.AllowQuit(retry) || !fs.extCloseAsk {
		t.Fatal("quit did not ask before closing the window")
	}
	fs.extCloseAsk = false
	a.extConfirmClose(files)
	a.drain()
	if retries != 0 || a.conns[files.id] != files {
		t.Fatal("canceling quit did not preserve the window and edit")
	}
	if a.AllowQuit(retry) {
		t.Fatal("quit was allowed without confirmation")
	}
	a.extConfirmClose(files)
	a.drain()
	if retries != 1 || !a.AllowQuit(retry) {
		t.Fatal("confirmation did not allow a retry")
	}
	a.SetQuitting(false)
	if a.AllowQuit(retry) {
		t.Fatal("quit cancellation retained the earlier approval")
	}

	fs.extCloseAsk = false
	a.closeWindowSessions(w)
	a.drain()
	a.extConfirmClose(files)
	a.drain()
	if a.winOf(files.id) != nil || a.conns[files.id] != nil || len(a.wins) != 1 {
		t.Fatal("confirmation did not close the window and its files pane")
	}
}

func TestExternalEditsGateDetachedEditorClose(t *testing.T) {
	a, _ := newTestApp(t)
	fs := a.newFiles()
	fs.edits = []*extEdit{{state: extFailed, cancel: func() {}}}
	cn := &conn{id: 1, kind: layout.Editor, fs: fs, ed: &edWin{}}
	a.conns[cn.id] = cn

	cn.ed.dirty.Store(true)
	if a.editorClosing(cn) {
		t.Fatal("editor with an unsaved built-in change closed")
	}
	a.drain()
	if !cn.ed.closeOpen || fs.extCloseAsk {
		t.Fatal("the native editor close skipped the built-in warning")
	}
	cn.ed.closeOpen = false
	if a.AllowQuit(func() {}) {
		t.Fatal("quit allowed a dirty built-in editor to close")
	}
	a.drain()
	if !cn.ed.closeOpen || fs.extCloseAsk {
		t.Fatal("quit did not check the built-in editor before asking about external edits")
	}
	cn.ed.dirty.Store(false)
	cn.ed.closeOpen = false
	if a.editorClosing(cn) || !fs.extCloseAsk {
		t.Fatal("native editor close did not ask about external edits")
	}
	fs.extCloseAsk = false

	retries := 0
	retry := func() { retries++ }
	if a.AllowQuit(retry) || !fs.extCloseAsk {
		t.Fatal("quit did not check the detached editor")
	}
	a.extConfirmClose(cn)
	a.drain()
	if retries != 1 || !a.AllowQuit(retry) || !a.editorClosing(cn) {
		t.Fatal("the approved quit did not allow the native editor to close")
	}
	a.SetQuitting(false)
	if a.editorClosing(cn) || !fs.extCloseAsk {
		t.Fatal("the earlier quit approval outlived quit cancellation")
	}
	a.extConfirmClose(cn)
	a.drain()
	if a.conns[cn.id] != nil {
		t.Fatal("external edit confirmation did not close the detached editor")
	}
}
