package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/identity"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/gin-gonic/gin"
	"log/slog"
)

const accessContextKey = "access_context"

func RegisterM1Routes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))
	protected.GET("/me", func(c *gin.Context) {
		access := accessContext(c)
		scope, err := application.Authorization.BuildScope(c.Request.Context(), access.Principal, authz.QueryTarget{})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"principal": access.Principal, "scope": scope, "developmentIdentity": access.DevIdentity})
	})
	protected.GET("/audit/authorization", func(c *gin.Context) {
		access := accessContext(c)
		rows, err := application.Authorization.ListAudit(c.Request.Context(), access.Principal, 100)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})

	knowledgeBases := protected.Group("/knowledge-bases")
	knowledgeBases.GET("", func(c *gin.Context) {
		rows, err := application.KnowledgeBase.List(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	knowledgeBases.POST("", func(c *gin.Context) {
		var input knowledgebase.CreateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.KnowledgeBase.Create(c.Request.Context(), accessContext(c).Principal, input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})

	byID := knowledgeBases.Group("/:id")
	byID.GET("", func(c *gin.Context) {
		row, err := application.KnowledgeBase.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, row)
	})
	byID.PATCH("", func(c *gin.Context) {
		var input knowledgebase.UpdateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.KnowledgeBase.Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, row)
	})
	byID.DELETE("", func(c *gin.Context) {
		if err := application.KnowledgeBase.Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	members := byID.Group("/members")
	members.GET("", func(c *gin.Context) {
		rows, err := application.KnowledgeBase.ListMembers(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	members.POST("", func(c *gin.Context) {
		var input knowledgebase.AddMemberInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.KnowledgeBase.AddMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})
	members.DELETE("/:userID", func(c *gin.Context) {
		if err := application.KnowledgeBase.RemoveMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("userID")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	_ = logger
}

func developmentIdentityMiddleware(cfg config.Config, identities *identity.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.AppEnv != "development" && cfg.AppEnv != "test" {
			if c.GetHeader("X-Dev-User-ID") != "" || c.GetHeader("X-Dev-Tenant-ID") != "" {
				writeError(c, apperror.ErrUnauthenticated)
				return
			}
			writeError(c, apperror.ErrUnauthenticated)
			return
		}
		userID := c.GetHeader("X-Dev-User-ID")
		if userID == "" {
			userID = identity.DefaultDevUserID
		}
		tenantID := c.GetHeader("X-Dev-Tenant-ID")
		if tenantID == "" {
			tenantID = identity.DefaultDevTenantID
		}
		principal, err := identities.ResolveDevelopmentPrincipal(c.Request.Context(), userID, tenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = "local-request"
		}
		c.Set(accessContextKey, authz.AccessContext{Principal: principal, RequestID: requestID, DevIdentity: true})
		c.Next()
	}
}

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
	invalidJSON := errors.As(err, &syntaxError) || errors.As(err, &typeError)
	switch {
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
