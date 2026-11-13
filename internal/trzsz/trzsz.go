// Package trzsz is the client side of trzsz (trzsz.github.io): when trz
// or tsz on the server prints its magic, Find spots it, Send gives trz
// the files it waits for and Receive takes those tsz sends. It speaks
// the protocol's first version, in base64, which every server takes, and
// draws nothing: the caller asks the user and shows how it goes.
package trzsz

import (
	"bytes"
	"context"
	"regexp"

	"github.com/Licoy/sshyard/internal/xfer"
)

// Mode is what the server asks for.
type Mode int

const (
	// Download is tsz: the server sends files.
	Download Mode = iota
	// Upload is trz: the server waits for files.
	Upload
	// UploadDir is trz -d: the server waits for files and folders.
	UploadDir
)

// Offer is a transfer the server started, as its magic said.
type Offer struct {
	Mode Mode
	// Version is the server's trzsz version.
	Version string
	// ID is the transfer's unique id; a terminal redrawn may show the
	// magic again, with the same id.
	ID string
	// Windows is a server on Windows, whose console mangles lines.
	Windows bool
	// Tmux is a server inside tmux, which adds to what it prints.
	Tmux bool
}

const magicPrefix = "::TRZSZ:TRANSFER:"

var magicRe = regexp.MustCompile(`^::TRZSZ:TRANSFER:([SRD]):(\d+\.\d+\.\d+)(:\d+)?(:\d+)?`)

// tmuxControl is how tmux -CC wraps output, which this client does not
// take: the magic in it is left alone.
var tmuxControl = regexp.MustCompile(`%(output|extended-output) %\d+ `)

// Find reports where in p a transfer starts, just after the line of its
// magic, -1 and nil for none: the magic of a transfer already over,
// shown again, does not count, nor one whose line has not all come.
func Find(p []byte) (at int, o *Offer) {
	for off := 0; ; {
		i := bytes.Index(p[off:], []byte(magicPrefix))
		if i < 0 {
			return -1, nil
		}
		i += off
		off = i + 1
		m := magicRe.FindSubmatchIndex(p[i:])
		if m == nil {
			continue
		}
		end := i + m[1]
		nl := bytes.IndexByte(p[end:], '\n')
		if nl < 0 || len(bytes.TrimSpace(p[end:end+nl])) > 0 {
			continue
		}
		after := p[end+nl+1:]
		if redrawn(after) || tmuxControl.Match(p[max(0, i-64):i]) {
			continue
		}
		o := &Offer{Version: string(p[i+m[4] : i+m[5]])}
		switch p[i+m[2]] {
		case 'S':
			o.Mode = Download
		case 'R':
			o.Mode = Upload
		case 'D':
			o.Mode = UploadDir
		}
		if m[6] >= 0 {
			o.ID = string(p[i+m[6]+1 : i+m[7]])
		}
		o.Windows = o.ID == "1" || len(o.ID) == 13 && o.ID[11:] == "10"
		o.Tmux = len(o.ID) == 13 && o.ID[11:] == "20"
		return end + nl + 1, o
	}
}

// redrawn says what follows a magic is the end of its transfer, shown
// again rather than a transfer starting.
func redrawn(after []byte) bool {
	for _, s := range []string{"#CFG:", "Saved", "Cancelled", "Stopped", "Interrupted"} {
		if bytes.Contains(after, []byte(s)) {
			return true
		}
	}
	return false
}

// Options are how a transfer goes.
type Options struct {
	// Progress is told how the files go; nil for nothing.
	Progress xfer.Progress
	// Saved is what the server prints once the files came: n of them,
	// in dir. nil says it in English.
	Saved func(n int, dir string) string
	// Dir is where received files go, for Saved.
	Dir string
}

// Receive takes the files of a Download offer, and its folders, into
// sink. It returns what the shell printed after the transfer.
func Receive(ctx context.Context, c xfer.Conn, o *Offer, sink xfer.Sink, opt Options) (rest []byte, err error) {
	t := newTransfer(ctx, c, o, opt)
	return t.end(t.receive(sink))
}

// Send gives an Upload or UploadDir offer src's files, and its folders
// for UploadDir.
func Send(ctx context.Context, c xfer.Conn, o *Offer, src xfer.Source, opt Options) (rest []byte, err error) {
	t := newTransfer(ctx, c, o, opt)
	return t.end(t.send(src))
}

// Refuse tells the server the user chose nothing, and it ends.
func Refuse(ctx context.Context, c xfer.Conn, o *Offer) (rest []byte, err error) {
	t := newTransfer(ctx, c, o, Options{})
	if err := t.sendAction(false); err != nil {
		return nil, err
	}
	return t.r.rest(), nil
}
