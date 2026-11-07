package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
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
	code := apperror.CodeInternal
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	var maxBytesError *http.MaxBytesError
	var validation *apperror.ValidationError
	invalidJSON := errors.As(err, &syntaxError) || errors.As(err, &typeError) || errors.Is(err, io.ErrUnexpectedEOF)
	switch {
	case errors.As(err, &maxBytesError):
		// Both the JSON cap and the multipart envelope cap surface through
		// MaxBytesReader; report a stable code instead of a 500.
		status = http.StatusRequestEntityTooLarge
		code = apperror.CodePayloadTooLarge
	case errors.Is(err, apperror.ErrUnauthenticated):
		status = http.StatusUnauthorized
		code = apperror.CodeUnauthenticated
	case errors.Is(err, apperror.ErrForbidden):
		status = http.StatusForbidden
		code = apperror.CodeForbidden
	case errors.Is(err, apperror.ErrNotFound):
		status = http.StatusNotFound
		code = apperror.CodeNotFound
	case errors.Is(err, apperror.ErrConflict):
		status = http.StatusConflict
		code = apperror.CodeConflict
	case errors.Is(err, apperror.ErrNotReady):
		// The resource is readable but its content is not available yet: the
		// client must show a processing/failed state, not partial content.
		status = http.StatusConflict
		code = apperror.CodeNotReady
	case errors.Is(err, apperror.ErrRateLimited):
		status = http.StatusTooManyRequests
		code = apperror.CodeRateLimited
		c.Header("Retry-After", "60")
	case errors.Is(err, apperror.ErrQuotaExceeded):
		status = http.StatusTooManyRequests
		code = apperror.CodeQuotaExceeded
	case errors.Is(err, apperror.ErrContentUnavailable):
		// The document exists but its stored payload is missing. Do not echo
		// the storage error: it carries the object key.
		status = http.StatusConflict
		code = apperror.CodeContentUnavailable
	case errors.As(err, &validation), invalidJSON:
		status = http.StatusBadRequest
		code = apperror.CodeInvalidRequest
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
