package apperror

import (
	"errors"
	"fmt"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not_found")
	ErrConflict        = errors.New("conflict")
	// ErrNotReady means the resource exists and is readable, but its content is
	// not available yet (still processing, superseded or already consumed). The
	// caller must show a status rather than partial content.
	ErrNotReady = errors.New("not_ready")
	// ErrRateLimited is a per-tenant request budget breach.
	ErrRateLimited = errors.New("rate_limited")
	// ErrQuotaExceeded is a durable tenant quota breach, such as a document
	// count or storage limit. It is distinct from a transient rate limit.
	ErrQuotaExceeded = errors.New("quota_exceeded")
	// ErrContentUnavailable means the record exists but its stored payload is
	// absent, for example after an incomplete object-store restore. Storage
	// keys are deliberately not part of this error.
	ErrContentUnavailable = errors.New("content_unavailable")
)

// ValidationError marks caller-supplied input that violates a documented
// constraint. The message is safe to echo to the client; nothing classifies it
// by text.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }

func (e *ValidationError) Unwrap() error { return e.Err }

// Invalidf builds a validation error from a format string.
func Invalidf(format string, args ...any) error {
	return &ValidationError{Err: fmt.Errorf(format, args...)}
}

// Invalid wraps an existing error as a validation error.
func Invalid(err error) error { return &ValidationError{Err: err} }

// AsValidation reports whether err carries a validation error.
func AsValidation(err error) (*ValidationError, bool) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation, true
	}
	return nil, false
}
