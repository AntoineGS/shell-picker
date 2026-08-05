package fzfsidecar

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
)

var (
	errTransport            = errors.New("fzf sidecar: HTTP transport failure")
	errResponse             = errors.New("fzf sidecar: invalid HTTP response")
	errResponseTooBig       = errors.New("fzf sidecar: HTTP response too large")
	errInvalidJSON          = errors.New("fzf sidecar: invalid JSON response")
	errInvalidState         = errors.New("fzf sidecar: invalid fzf state")
	errStateTooLarge        = errors.New("fzf sidecar: selected state too large")
	errInconsistentSnapshot = errors.New("fzf sidecar: inconsistent fzf snapshot")
	errInvalidAction        = errors.New("fzf sidecar: invalid fzf action")
	errInvalidStatus        = errors.New("fzf sidecar: unexpected HTTP status")
	errInvalidMimeType      = errors.New("fzf sidecar: unexpected HTTP content type")
	errTransientCycle       = errors.New("fzf sidecar: transient polling cycle")
)

type idleConnectionCloser interface {
	CloseIdleConnections()
}

type transportError struct {
	cause error
}

func (err *transportError) Error() string { return errTransport.Error() }

func (err *transportError) Unwrap() error { return err.cause }

func (err *transportError) Is(target error) bool { return target == errTransport }

type transientCycleError struct {
	cause error
}

func (err *transientCycleError) Error() string { return errTransientCycle.Error() }

func (err *transientCycleError) Unwrap() error { return err.cause }

func (err *transientCycleError) Is(target error) bool { return target == errTransientCycle }

type operationDiagnostic struct {
	status int
	reason ObserverReason
}

type operationError struct {
	cause      error
	diagnostic operationDiagnostic
}

func (err *operationError) Error() string {
	if err == nil || err.cause == nil {
		return "fzf sidecar: operation failure"
	}
	return err.cause.Error()
}

func (err *operationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func wrapOperationError(cause error, diagnostic operationDiagnostic) error {
	if cause == nil {
		return nil
	}
	var existing *operationError
	if errors.As(cause, &existing) {
		return cause
	}
	return &operationError{cause: cause, diagnostic: diagnostic}
}

func diagnosticForError(err error) operationDiagnostic {
	if err == nil {
		return operationDiagnostic{reason: ObserverReasonHTTPStatus}
	}
	var operationErr *operationError
	if errors.As(err, &operationErr) && operationErr != nil {
		return operationErr.diagnostic
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return operationDiagnostic{reason: ObserverReasonContextCanceled}
	case errors.Is(err, errResponseTooBig):
		return operationDiagnostic{reason: ObserverReasonResponseTooLarge}
	case errors.Is(err, errInvalidMimeType):
		return operationDiagnostic{reason: ObserverReasonInvalidMIME}
	case errors.Is(err, errInvalidJSON):
		return operationDiagnostic{reason: ObserverReasonInvalidJSON}
	case errors.Is(err, errInvalidState):
		return operationDiagnostic{reason: ObserverReasonInvalidState}
	case errors.Is(err, errInvalidAction):
		return operationDiagnostic{reason: ObserverReasonInvalidAction}
	case errors.Is(err, errInconsistentSnapshot):
		return operationDiagnostic{reason: ObserverReasonInconsistentSnapshot}
	case errors.Is(err, errTransport):
		return operationDiagnostic{reason: ObserverReasonTransport}
	default:
		return operationDiagnostic{reason: ObserverReasonResponse}
	}
}

func isTransientTransportCause(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	return false
}

func isBoundedHTTPTransportError(err error) bool {
	var urlError *url.Error
	if !errors.As(err, &urlError) || urlError == nil {
		return false
	}
	return isTransientTransportCause(urlError.Err) || isConnectionRefused(urlError.Err)
}

func requestContextError(parent, requestContext context.Context) error {
	if parent != nil {
		if err := parent.Err(); err != nil {
			return err
		}
	}
	if requestContext != nil {
		if err := requestContext.Err(); err != nil {
			return err
		}
	}
	return nil
}
