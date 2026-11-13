package session

import (
	"context"
	"strings"
)

// localPTY is a shell running on this computer in a pseudo-terminal: what
// it prints is read, what is typed written. Close ends it.
type localPTY interface {
	Stream
	// Wait waits for the shell to end.
	Wait() error
}

// termEnv names the terminal to a local shell, as a terminal app does.
func termEnv(term string) []string {
	return []string{"TERM=" + term, "COLORTERM=truecolor", "TERM_PROGRAM=SSHYard"}
}

// mergeEnv keeps the last entry of each name, in the order the names
// first appear; names fold case when fold is set, as on Windows. A name
// may start with "=", as Windows' per-drive directories do.
func mergeEnv(env []string, fold bool) []string {
	at := map[string]int{}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv[min(1, len(kv)):], '='); i >= 0 {
			name = kv[:i+1]
		}
		if fold {
			name = strings.ToUpper(name)
		}
		if i, ok := at[name]; ok {
			out[i] = kv
			continue
		}
		at[name] = len(out)
		out = append(out, kv)
	}
	return out
}

// startPTY starts the local shell; a variable so that tests can stand in.
var startPTY = startLocal

// connectLocal starts the shell of a local session.
func (s *Session) connectLocal() error {
	sh := s.opts.Shell
	if sh.Path == "" {
		sh = Shells()[0]
	}
	return s.connectStream(func(cols, rows int) (Stream, error) {
		return startPTY(sh, cols, rows, s.opts.Term)
	}, nil)
}

// serveLocal waits for the local shell to end, which is never a lost
// connection: it is not reconnected by itself.
func (s *Session) serveLocal(ctx context.Context, p localPTY) error {
	s.mu.Lock()
	out := s.streamOut
	s.mu.Unlock()
	waited := make(chan struct{})
	go func() {
		p.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
	}
	s.endTakeover()
	s.Term.ResetInputModes(false)
	p.Close()
	<-out.done
	s.mu.Lock()
	s.stream, s.streamOut, s.stdin = nil, nil, nil
	s.mu.Unlock()
	return nil
}
