package app

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/llm"
	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/internal/settings"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// The files a question can carry: images and PDFs go to the model as they
// are, text files go into the question.
const (
	maxAttachments = 10
	maxAttachFile  = 20 << 20
	maxAttachText  = 512 << 10
	maxAttachTotal = agent.MaxPartBytes
)

// aiAttach is a file attached to the question being written.
type aiAttach struct {
	name string
	mime string // of an image or PDF; "" for text
	data []byte
	bmp  *ui.Bitmap // an image's thumbnail, nil when it does not decode
}

func (f *aiAttach) text() bool  { return f.mime == "" }
func (f *aiAttach) image() bool { return strings.HasPrefix(f.mime, "image/") }

func (f *aiAttach) icon() *icons.Icon {
	switch {
	case f.image():
		return icons.Image
	case f.mime == "application/pdf":
		return icons.FilePDF
	case codeExt[strings.ToLower(filepath.Ext(f.name))]:
		return icons.FileCode
	}
	return icons.FileText
}

// codeExt are the extensions of source files, which show a code icon.
var codeExt = map[string]bool{
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".rs": true, ".c": true,
	".h": true, ".cc": true, ".cpp": true, ".java": true, ".kt": true, ".swift": true, ".rb": true, ".php": true,
	".sh": true, ".bash": true, ".zsh": true, ".ps1": true, ".sql": true, ".json": true, ".yaml": true, ".yml": true,
	".toml": true, ".xml": true, ".html": true, ".css": true, ".lua": true, ".pl": true, ".conf": true, ".ini": true,
}

// attachMIME are the images and documents the models read.
var attachMIME = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "application/pdf": true,
}

// errAttach is why a file cannot be attached, as the toast says it.
type errAttach struct {
	msg func(m locales.Messages) string
}

func (e *errAttach) Error() string { return e.msg(locales.In("en")) }

// readAttach reads the file at path as an attachment, by what it holds:
// an image or PDF, or text, which is UTF-8 without NUL bytes. Anything
// else is refused.
func readAttach(path string) (*aiAttach, error) {
	name := filepath.Base(path)
	f, err := os.Open(path)
	if err != nil {
		return nil, &errAttach{func(m locales.Messages) string { return m.AiAttachReadFailed(name) }}
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, &errAttach{func(m locales.Messages) string { return m.AiAttachUnsupported(name) }}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAttachFile+1))
	if err != nil {
		return nil, &errAttach{func(m locales.Messages) string { return m.AiAttachReadFailed(name) }}
	}
	if len(data) > maxAttachFile {
		return nil, &errAttach{func(m locales.Messages) string { return m.AiAttachTooLarge(name, humanSize(maxAttachFile)) }}
	}
	return attachData(name, data)
}

func attachData(name string, data []byte) (*aiAttach, error) {
	mime, _, _ := strings.Cut(http.DetectContentType(data), ";")
	switch {
	case attachMIME[mime]:
		at := &aiAttach{name: name, mime: mime, data: data}
		if at.image() {
			at.bmp, _ = decodePreview(data)
		}
		return at, nil
	case len(data) > 0 && utf8.Valid(data) && !bytes.ContainsRune(data, 0):
		if len(data) > maxAttachText {
			return nil, &errAttach{func(m locales.Messages) string { return m.AiAttachTooLarge(name, humanSize(maxAttachText)) }}
		}
		return &aiAttach{name: name, data: data}, nil
	}
	return nil, &errAttach{func(m locales.Messages) string { return m.AiAttachUnsupported(name) }}
}

// addAttach attaches f to the pane's question, unless the question has
// as many files as it takes, or they would weigh too much, or the pane's
// agent cannot read it. It says why in a toast.
func (a *App) addAttach(cn *conn, f *aiAttach) {
	if cn.ai.steer != nil {
		return
	}
	var why func(m locales.Messages) string
	total := len(f.data)
	for _, g := range cn.ai.attach {
		total += len(g.data)
	}
	switch {
	case settings.IsACP(cn.ai.agent) && !f.text():
		why = func(m locales.Messages) string { return m.AiAttachTextOnly(f.name) }
	case len(cn.ai.attach) >= maxAttachments:
		why = func(m locales.Messages) string { return m.AiAttachTooMany(strconv.Itoa(maxAttachments)) }
	case total > maxAttachTotal:
		why = func(m locales.Messages) string { return m.AiAttachTotal(humanSize(maxAttachTotal)) }
	}
	if why != nil {
		a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: why(m), Type: "error"} })
		return
	}
	cn.ai.attach = append(cn.ai.attach, f)
}

// attachPaths reads the files at paths, off the UI thread, and attaches
// them to the pane's question.
func (a *App) attachPaths(cn *conn, paths []string) {
	a.attachRead(cn, func(add func(*aiAttach, error)) {
		for _, p := range paths {
			add(readAttach(p))
		}
	})
}

// attachImage attaches an image given as data, as one pasted, named name.
func (a *App) attachImage(cn *conn, name string, data []byte) {
	a.attachRead(cn, func(add func(*aiAttach, error)) {
		if len(data) > maxAttachFile {
			add(nil, &errAttach{func(m locales.Messages) string { return m.AiAttachTooLarge(name, humanSize(maxAttachFile)) }})
			return
		}
		add(attachData(name, data))
	})
}

// attachRead runs read off the UI thread, and attaches each file it adds
// to the pane's question, or says in a toast why it cannot.
func (a *App) attachRead(cn *conn, read func(add func(*aiAttach, error))) {
	current := a.attachDraft(cn)
	if !current() {
		return
	}
	go read(func(f *aiAttach, err error) {
		a.post(func() {
			if !current() {
				return
			}
			var ea *errAttach
			if errors.As(err, &ea) {
				a.toast(func(m locales.Messages) ui.Toast { return ui.Toast{Title: ea.msg(m), Type: "error"} })
				return
			}
			a.addAttach(cn, f)
		})
	})
}

// attachDraft binds an asynchronous picker or read to its original draft.
func (a *App) attachDraft(cn *conn) func() bool {
	s := cn.ai
	gen, attachGen := s.gen, s.attachGen
	return func() bool {
		return a.conns[cn.id] == cn && cn.ai == s && s.gen == gen && s.attachGen == attachGen && s.steer == nil
	}
}

// pasteAttach takes a paste into the question that is files, long text
// or an image, and attaches it: files copied in a file manager, text of
// more than pasteLines lines or pasteRunes characters, as a log, and an
// image copied without text, as a screenshot. It leaves shorter text,
// which the question takes as typed, and reports whether it took the
// paste.
func (a *App) pasteAttach(cn *conn, svc ui.Services, text string) bool {
	formats := svc.ClipboardFormats()
	if slices.Contains(formats, transfer.FileList) || slices.Contains(formats, transfer.URIList) {
		if paths, err := svc.ReadClipboardData(transfer.FileList, transfer.URIList).Files(); err == nil && len(paths) > 0 {
			a.attachPaths(cn, paths)
			return true
		}
	}
	stamp := time.Now().Format("20060102-150405")
	if longPaste(text) {
		name := "text-" + stamp + ".txt"
		if len(text) > maxAttachText {
			a.toast(func(m locales.Messages) ui.Toast {
				return ui.Toast{Title: m.AiAttachTooLarge(name, humanSize(maxAttachText)), Type: "error"}
			})
			return true
		}
		a.addAttach(cn, &aiAttach{name: name, data: []byte(text)})
		return true
	}
	if text != "" || !slices.Contains(formats, transfer.PNG) {
		return false
	}
	data, err := svc.ReadClipboardData(transfer.PNG).Read(transfer.PNG)
	if err != nil || len(data) == 0 {
		return false
	}
	a.attachImage(cn, "image-"+stamp+".png", data)
	return true
}

// Pasted text longer than this attaches as a file rather than going into
// the question.
const (
	pasteLines = 20
	pasteRunes = 2000
)

func longPaste(text string) bool {
	return strings.Count(strings.TrimRight(text, "\n"), "\n")+1 > pasteLines || utf8.RuneCountInString(text) > pasteRunes
}

// pickAttach asks for files through the system's open dialog.
func (a *App) pickAttach(cn *conn, m locales.Messages) {
	current := a.attachDraft(cn)
	if a.p.PickFiles == nil || !current() {
		return
	}
	title := m.AiAttach()
	go func() {
		paths, err := a.p.PickFiles(title)
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() {
			if current() {
				a.attachPaths(cn, paths)
			}
		})
	}()
}

// attachTurn is what the question sends: text with the text files in it,
// as data from outside, and the images and PDFs as parts.
func attachTurn(text string, files []*aiAttach) (string, []llm.Part) {
	var parts []llm.Part
	for _, f := range files {
		if f.text() {
			if text != "" {
				text += "\n\n"
			}
			text += agent.WrapUntrusted("file "+f.name, string(f.data))
			continue
		}
		parts = append(parts, llm.Part{Name: f.name, MIME: f.mime, Data: f.data})
	}
	return text, parts
}
