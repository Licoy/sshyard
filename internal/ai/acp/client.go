package acp

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/Licoy/sshyard/internal/ai/agent"
	"github.com/Licoy/sshyard/internal/ai/policy"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	sdk "github.com/coder/acp-go-sdk"
)

func (s *Session) ReadTextFile(ctx context.Context, p sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	ctx, release, err := s.callbackContext(ctx, p.SessionId)
	if err != nil {
		return sdk.ReadTextFileResponse{}, err
	}
	defer release()
	if err := s.checkSession(p.SessionId); err != nil {
		return sdk.ReadTextFileResponse{}, err
	}
	remote, err := s.mapPath(p.Path)
	if err != nil {
		return sdk.ReadTextFileResponse{}, err
	}
	text, err := s.readText(ctx, remote, p.Line, p.Limit)
	if err != nil {
		return sdk.ReadTextFileResponse{}, err
	}
	return sdk.ReadTextFileResponse{Content: text}, nil
}

// readText reads a remote file for the agent, as the gate allows.
func (s *Session) readText(ctx context.Context, remote string, line, limit *int) (string, error) {
	if _, err := sliceLines("", line, limit); err != nil {
		return "", err
	}
	remote, err := s.resolveFile(ctx, remote)
	if err != nil {
		return "", err
	}
	act := policy.Action{Where: policy.Remote, Tool: "read_file", Path: remote}
	start := time.Now()
	v, err := s.authorize(ctx, act, "")
	if err != nil {
		return "", err
	}
	if !v.Run {
		return "", refusalError(v)
	}
	if s.cfg.Files == nil {
		s.finish(act, v, 1, start, 0)
		return "", errNotConnected
	}
	data, _, err := s.readFile(ctx, remote)
	if err != nil {
		s.finish(act, v, failureExit(err), start, 0)
		return "", err
	}
	if len(data) > readCap {
		s.finish(act, v, 1, start, len(data))
		return "", errors.New("file exceeds 10MB")
	}
	output := string(data)
	if s.cfg.Redact {
		output = policy.RedactLines(output)
	}
	text, err := sliceLines(output, line, limit)
	if err != nil {
		s.finish(act, v, 1, start, len(data))
		return "", err
	}
	s.finish(act, v, 0, start, len(data))
	return text, nil
}

func (s *Session) WriteTextFile(ctx context.Context, p sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	ctx, release, err := s.callbackContext(ctx, p.SessionId)
	if err != nil {
		return sdk.WriteTextFileResponse{}, err
	}
	defer release()
	if err := s.checkSession(p.SessionId); err != nil {
		return sdk.WriteTextFileResponse{}, err
	}
	remote, err := s.mapPath(p.Path)
	if err != nil {
		return sdk.WriteTextFileResponse{}, err
	}
	if err := s.writeText(ctx, remote, p.Content); err != nil {
		return sdk.WriteTextFileResponse{}, err
	}
	return sdk.WriteTextFileResponse{}, nil
}

// writeText writes a remote file for the agent, as the gate allows, after
// showing the diff.
func (s *Session) writeText(ctx context.Context, remote, content string) error {
	if len(content) > writeCap {
		return errors.New("content exceeds 10MB")
	}
	remote, err := s.resolveFile(ctx, remote)
	if err != nil {
		return err
	}
	// A diff reads the current file, so authorize that read independently.
	readAct := policy.Action{Where: policy.Remote, Tool: "read_file", Path: remote}
	readStart := time.Now()
	readVerdict, err := s.authorize(ctx, readAct, "")
	if err != nil {
		return err
	}
	if !readVerdict.Run {
		return refusalError(readVerdict)
	}
	if s.cfg.Files == nil {
		s.finish(readAct, readVerdict, 1, readStart, 0)
		return errNotConnected
	}
	data, ver, readErr := s.readFile(ctx, remote)
	if readErr == nil && len(data) > readCap {
		readErr = sftpsvc.ErrReadLimit
	}
	readExit := 0
	if readErr != nil {
		readExit = failureExit(readErr)
	}
	s.finish(readAct, readVerdict, readExit, readStart, len(data))
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	old := ""
	if readErr == nil {
		old = string(data)
	}
	diff := agent.Unified(remote, old, content)
	act := policy.Action{Where: policy.Remote, Tool: "write_file", Path: remote}
	start := time.Now()
	v, err := s.authorize(ctx, act, diff)
	if err != nil {
		return err
	}
	if !v.Run {
		return refusalError(v)
	}
	if s.cfg.Files == nil {
		s.finish(act, v, 1, start, 0)
		return errNotConnected
	}
	// A zero version is a file that was absent when the diff was made.
	// shortcut: the re-check and Write are not atomic, upgrade when a zero version means create-only.
	if ver == (sftpsvc.Version{}) {
		if _, _, err := s.readFile(ctx, remote); err == nil {
			s.finish(act, v, 1, start, 0)
			return errors.New("file appeared since the diff was made")
		} else if !errors.Is(err, fs.ErrNotExist) {
			s.finish(act, v, failureExit(err), start, 0)
			return err
		}
	}
	if _, err := s.cfg.Files.WriteContext(ctx, remote, []byte(content), ver); err != nil {
		s.finish(act, v, failureExit(err), start, 0)
		if errors.Is(err, sftpsvc.ErrChanged) {
			return errors.New("file changed since it was read")
		}
		return err
	}
	s.finish(act, v, 0, start, len(content))
	return nil
}

func (s *Session) RequestPermission(ctx context.Context, p sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	ctx, release, err := s.callbackContext(ctx, p.SessionId)
	if err != nil {
		return sdk.RequestPermissionResponse{}, err
	}
	defer release()
	if err := s.checkSession(p.SessionId); err != nil {
		return sdk.RequestPermissionResponse{}, err
	}
	pctx, cancel := context.WithCancel(ctx)
	s.trackPerm(cancel)
	defer cancel()

	act, diff := actionFrom(p.ToolCall)
	v, err := s.authorize(pctx, act, diff)
	if pctx.Err() != nil {
		return cancelledPerm(), nil
	}
	if err != nil {
		return sdk.RequestPermissionResponse{}, err
	}
	id, cancelled := PermissionChoice(v.Run, p.Options)
	if cancelled {
		return cancelledPerm(), nil
	}
	return sdk.RequestPermissionResponse{Outcome: sdk.RequestPermissionOutcome{
		Selected: &sdk.RequestPermissionOutcomeSelected{OptionId: sdk.PermissionOptionId(id)},
	}}, nil
}

func cancelledPerm() sdk.RequestPermissionResponse {
	return sdk.RequestPermissionResponse{Outcome: sdk.RequestPermissionOutcome{
		Cancelled: &sdk.RequestPermissionOutcomeCancelled{},
	}}
}

// SessionUpdate only queues. A slow UI must not block the SDK read loop.
// The queue drops the oldest update when it is full. The history an
// agent replays while it loads a session is dropped.
func (s *Session) SessionUpdate(ctx context.Context, n sdk.SessionNotification) error {
	s.mu.Lock()
	loading := s.loading
	s.mu.Unlock()
	if loading {
		return nil
	}
	select {
	case s.updates <- n:
		return nil
	default:
	}
	select {
	case <-s.updates:
	default:
	}
	select {
	case s.updates <- n:
	default:
	}
	return nil
}

// authorize asks the gate. When the audit log refuses the action, the
// user sees why as a notice and the agent gets the same reason.
func (s *Session) authorize(ctx context.Context, act policy.Action, diff string) (policy.Verdict, error) {
	v, err := s.gate.Authorize(ctx, act, diff)
	if code, ok := policy.AuditNotice(err); ok {
		s.emit(Update{Kind: "notice", Text: code})
	}
	return v, err
}

// finish records an action that ran. A lost record is shown as a notice
// and returned; the action is never run again.
func (s *Session) finish(act policy.Action, v policy.Verdict, exit int, start time.Time, n int) error {
	err := s.gate.Done(act, v, exit, time.Since(start).Milliseconds(), int64(n))
	if err == nil {
		return nil
	}
	var ae *policy.AuditError
	if errors.As(err, &ae) {
		err = ae.Err
	}
	ae = &policy.AuditError{Ran: true, Err: err}
	code, _ := policy.AuditNotice(ae)
	s.emit(Update{Kind: "notice", Text: code})
	return ae
}

func refusalError(v policy.Verdict) error {
	if v.Refusal != "" {
		return errors.New(v.Refusal)
	}
	return errors.New("refused")
}

func (s *Session) resolveFile(ctx context.Context, p string) (string, error) {
	if s.cfg.Files == nil {
		return p, nil
	}
	return s.cfg.Files.ResolvePath(ctx, p)
}
func (s *Session) readFile(ctx context.Context, p string) ([]byte, sftpsvc.Version, error) {
	return s.cfg.Files.ReadContext(ctx, p, readCap)
}

func failureExit(err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return -1
	}
	return 1
}
func (s *Session) outbound(text string) string {
	if s.cfg.Redact {
		return policy.Redact(text)
	}
	return text
}
