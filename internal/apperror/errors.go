package apperror

import "errors"

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
