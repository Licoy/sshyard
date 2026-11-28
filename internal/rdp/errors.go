package rdp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
)

// Kind is the connection stage or origin of a failure.
type Kind int

const (
	KindOther Kind = iota
	KindNetwork
	KindAuth
	KindCert
	KindSecurity
	KindServer
	KindLicense
)

// Reason is a failure detail that the application can present to the user.
type Reason int

const (
	ReasonNone Reason = iota
	ReasonLogonFailure
	ReasonAccountLocked
	ReasonAccountDisabled
	ReasonAccountRestricted
	ReasonAccountExpired
	ReasonPasswordExpired
	ReasonPasswordMustChange
	ReasonLogonTypeNotGranted
	ReasonLoggedOff
	ReasonIdleTimeout
	ReasonLogonTimeout
	ReasonReplaced
	ReasonAdminEnded
	ReasonServerDenied
	ReasonProtocol
)

// Error describes an RDP connection failure.
type Error struct {
	Kind   Kind
	Reason Reason
	Code   uint32
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("RDP connection error (0x%08x)", e.Code)
}

func (e *Error) Unwrap() error { return e.Err }

// Classify finds a typed failure in the error chain.
func Classify(err error) (Kind, Reason) {
	var rdpErr *Error
	if errors.As(err, &rdpErr) {
		return rdpErr.Kind, rdpErr.Reason
	}
	var certErr *CertError
	if errors.As(err, &certErr) {
		return KindCert, ReasonNone
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
		return KindNetwork, ReasonNone
	}
	return KindOther, ReasonNone
}

// Retryable reports whether another attempt could repair a network failure.
func Retryable(err error) bool {
	kind, reason := Classify(err)
	return kind == KindNetwork && reason == ReasonNone && !errors.Is(err, context.Canceled)
}
