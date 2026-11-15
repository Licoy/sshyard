package app

import (
	"errors"
	"strings"

	"github.com/Licoy/sshyard/internal/netproxy"
	"github.com/Licoy/sshyard/internal/sftpsvc"
	"github.com/Licoy/sshyard/locales"
)

// securityErrorText recognizes typed failures through wrappers, while keeping
// unknown errors intact for the existing diagnostic display.
func securityErrorText(m locales.Messages, err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []error{sftpsvc.ErrReadLimit, sftpsvc.ErrUnsafePath, sftpsvc.ErrUnsafeDestination, sftpsvc.ErrPartChanged, sftpsvc.ErrOperationsBusy, sftpsvc.ErrUnsafeRemoteName, sftpsvc.ErrIntoItself, netproxy.ErrClientLimit, netproxy.ErrHandshakeTooLarge} {
		if errors.Is(err, known) {
			return securityText(m, known.Error())
		}
	}
	return securityText(m, err.Error())
}

// securityText translates only complete, known messages, including the SFTP
// package's own repeated wrapper prefix; file contents are never rewritten.
func securityText(m locales.Messages, s string) string {
	switch s {
	case "ai.command_output_limit":
		return m.AiCommandOutputLimit()
	case netproxy.ErrClientLimit.Error():
		return m.SecurityProxyClientLimit()
	case netproxy.ErrHandshakeTooLarge.Error():
		return m.SecurityProxyHandshakeLimit()
	}
	if !strings.HasPrefix(s, "sftp: ") {
		return s
	}
	raw := s
	for strings.HasPrefix(raw, "sftp: ") {
		raw = strings.TrimPrefix(raw, "sftp: ")
	}
	switch raw {
	case "file exceeds read limit":
		return m.SecuritySftpReadLimit()
	case "symbolic link or unsafe path is not allowed":
		return m.SecuritySftpUnsafePath()
	case "destination folder is writable by everyone":
		return m.SecuritySftpUnsafeDestination()
	case "partial file identity changed":
		return m.SecuritySftpPartChanged()
	case "too many pending operations":
		return m.SecuritySftpOperationsBusy()
	case "invalid path":
		return m.SecuritySftpInvalidPath()
	case "parent is not a directory":
		return m.SecuritySftpNotDirectory()
	case "not a regular file":
		return m.SecuritySftpNotRegular()
	case "not a safe file name on a server":
		return m.SecuritySftpUnsafeRemoteName()
	case "a folder can't be copied into itself":
		return m.SecuritySftpIntoItself()
	}
	return s
}
