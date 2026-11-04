package apperror

// Stable error codes returned in the API error envelope under "error". Clients
// match these values, so they are part of the API contract and must not be
// renamed. The HTTP status that pairs with each code lives in the transport
// layer (internal/httpapi), not here.
const (
	CodeInvalidRequest     = "invalid_request"
	CodeUnauthenticated    = "unauthenticated"
	CodeForbidden          = "forbidden"
	CodeNotFound           = "not_found"
	CodeConflict           = "conflict"
	CodeNotReady           = "not_ready"
	CodeRateLimited        = "rate_limited"
	CodeQuotaExceeded      = "quota_exceeded"
	CodeContentUnavailable = "content_unavailable"
	CodePayloadTooLarge    = "payload_too_large"
	CodeInternal           = "internal_error"
)
