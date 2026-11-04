package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/gin-gonic/gin"
)

const accessContextKey = "access_context"

func accessContext(c *gin.Context) authz.AccessContext {
	value, exists := c.Get(accessContextKey)
	if !exists {
		return authz.AccessContext{}
	}
	return value.(authz.AccessContext)
}

func writeError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	var maxBytesError *http.MaxBytesError
	invalidJSON := errors.As(err, &syntaxError) || errors.As(err, &typeError)
	switch {
	case errors.As(err, &maxBytesError):
		// Both the JSON cap and the multipart envelope cap surface through
		// MaxBytesReader; report a stable code instead of a 500.
		status = http.StatusRequestEntityTooLarge
		code = "payload_too_large"
	case errors.Is(err, apperror.ErrUnauthenticated):
		status = http.StatusUnauthorized
		code = "unauthenticated"
	case errors.Is(err, apperror.ErrForbidden):
		status = http.StatusForbidden
		code = "forbidden"
	case errors.Is(err, apperror.ErrNotFound):
		status = http.StatusNotFound
		code = "not_found"
	case errors.Is(err, apperror.ErrConflict):
		status = http.StatusConflict
		code = "conflict"
	case errors.Is(err, apperror.ErrNotReady):
		// The resource is readable but its content is not available yet: the
		// client must show a processing/failed state, not partial content.
		status = http.StatusConflict
		code = "not_ready"
	case errors.Is(err, apperror.ErrRateLimited):
		status = http.StatusTooManyRequests
		code = "rate_limited"
		c.Header("Retry-After", "60")
	case errors.Is(err, apperror.ErrQuotaExceeded):
		status = http.StatusTooManyRequests
		code = "quota_exceeded"
	case errors.Is(err, apperror.ErrContentUnavailable):
		// The document exists but its stored payload is missing. Do not echo
		// the storage error: it carries the object key.
		status = http.StatusConflict
		code = "content_unavailable"
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "unsupported"), strings.Contains(err.Error(), "cannot be empty"), strings.Contains(err.Error(), "maximum"), strings.Contains(err.Error(), "must be"), strings.Contains(err.Error(), "must not be"):
		status = http.StatusBadRequest
		code = "invalid_request"
	case invalidJSON, strings.HasPrefix(err.Error(), "json:"), strings.Contains(err.Error(), "unexpected end"), strings.Contains(err.Error(), "unexpected EOF"), strings.Contains(err.Error(), "invalid character"):
		status = http.StatusBadRequest
		code = "invalid_request"
	}
	response := gin.H{"error": code}
	switch {
	case errors.Is(err, apperror.ErrContentUnavailable):
		response["message"] = "document content is unavailable in object storage"
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError && err != nil:
		response["message"] = err.Error()
	}
	c.AbortWithStatusJSON(status, response)
}

func ginError(message string) error { return &requestError{message: message} }

type requestError struct{ message string }

func (e *requestError) Error() string { return e.message }
