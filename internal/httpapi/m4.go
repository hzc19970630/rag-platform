package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/identity"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/gin-gonic/gin"
)

const (
	oidcStateCookie    = "rag_oidc_state"
	oidcNonceCookie    = "rag_oidc_nonce"
	oidcVerifierCookie = "rag_oidc_verifier"
	oidcCookieTTL      = 600
)

// authenticationMiddleware resolves a request credential in this order: a
// hashed API key, a signed session token (Authorization header or session
// cookie), then the development headers when they are explicitly enabled.
// Development headers are never accepted outside development/test.
func authenticationMiddleware(cfg config.Config, application *app.Application) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		raw := bearerToken(c)
		if raw == "" {
			if cookie, err := c.Cookie(cfg.Auth.CookieName); err == nil {
				raw = cookie
			}
		}
		if raw != "" {
			principal, err := resolveCredential(c, application.Identity, raw)
			if err != nil {
				writeError(c, err)
				return
			}
			principal = withRequestMetadata(c, principal, requestID)
			if !application.Limiter.Allow(principal.TenantID) {
				writeError(c, apperror.ErrRateLimited)
				return
			}
			c.Set(accessContextKey, authz.AccessContext{Principal: principal, RequestID: requestID})
			c.Next()
			return
		}
		if !cfg.Auth.DevAuthEnabled || (cfg.AppEnv != "development" && cfg.AppEnv != "test") {
			writeError(c, apperror.ErrUnauthenticated)
			return
		}
		userID := strings.TrimSpace(c.GetHeader("X-Dev-User-ID"))
		if userID == "" {
			userID = identity.DefaultDevUserID
		}
		tenantID := strings.TrimSpace(c.GetHeader("X-Dev-Tenant-ID"))
		if tenantID == "" {
			tenantID = identity.DefaultDevTenantID
		}
		principal, err := application.Identity.ResolveDevelopmentPrincipal(c.Request.Context(), userID, tenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		principal.CredentialType = "dev"
		principal = withRequestMetadata(c, principal, requestID)
		if !application.Limiter.Allow(principal.TenantID) {
			writeError(c, apperror.ErrRateLimited)
			return
		}
		c.Set(accessContextKey, authz.AccessContext{Principal: principal, RequestID: requestID, DevIdentity: true})
		c.Next()
	}
}

// withRequestMetadata copies the request correlation and client origin onto the
// principal so every audit row written below the HTTP layer carries them.
func withRequestMetadata(c *gin.Context, principal authz.Principal, requestID string) authz.Principal {
	principal.RequestID = requestID
	principal.IPAddress = c.ClientIP()
	principal.UserAgent = c.Request.UserAgent()
	return principal
}

func resolveCredential(c *gin.Context, identities *identity.Service, raw string) (authz.Principal, error) {
	if strings.HasPrefix(raw, "rk_") {
		return identities.AuthenticateAPIKey(c.Request.Context(), raw)
	}
	return identities.Authenticate(c.Request.Context(), raw)
}

func bearerToken(c *gin.Context) string {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if header == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// RegisterM4Routes wires authentication, API keys, tenant membership and audit
// query. The OIDC callback is deliberately unauthenticated: it establishes the
// session by exchanging the authorization code.
func RegisterM4Routes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	auth := router.Group("/api/v1/auth")
	auth.GET("/config", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"oidcEnabled":     application.Identity.OIDCEnabled(),
			"oidcIssuer":      application.Identity.OIDCIssuer(),
			"devAuthEnabled":  cfg.Auth.DevAuthEnabled && (cfg.AppEnv == "development" || cfg.AppEnv == "test"),
			"sessionCookie":   cfg.Auth.CookieName,
			"sessionTTLHours": cfg.Auth.SessionTTLSeconds / 3600,
		})
	})
	auth.GET("/oidc/login", func(c *gin.Context) {
		if !application.Identity.OIDCEnabled() {
			writeError(c, apperror.ErrNotFound)
			return
		}
		authorizationURL, state, nonce, verifier, err := application.Identity.BeginOIDC(c.Request.Context())
		if err != nil {
			writeError(c, err)
			return
		}
		setOIDCCookie(c, cfg, oidcStateCookie, state)
		setOIDCCookie(c, cfg, oidcNonceCookie, nonce)
		setOIDCCookie(c, cfg, oidcVerifierCookie, verifier)
		c.Redirect(http.StatusFound, authorizationURL)
	})
	auth.GET("/callback", func(c *gin.Context) {
		stateCookie, _ := c.Cookie(oidcStateCookie)
		nonce, _ := c.Cookie(oidcNonceCookie)
		verifier, _ := c.Cookie(oidcVerifierCookie)
		clearCookie(c, cfg, oidcStateCookie)
		clearCookie(c, cfg, oidcNonceCookie)
		clearCookie(c, cfg, oidcVerifierCookie)
		if stateCookie == "" || c.Query("state") != stateCookie {
			redirectAuthError(c, cfg, "state_mismatch")
			return
		}
		if providerError := c.Query("error"); providerError != "" {
			redirectAuthError(c, cfg, providerError)
			return
		}
		result, err := application.Identity.CompleteOIDC(c.Request.Context(), c.Query("code"), verifier, nonce, c.Request.UserAgent(), c.ClientIP())
		if err != nil {
			logger.Warn("oidc_callback_failed", "error", err)
			redirectAuthError(c, cfg, "oidc_callback_failed")
			return
		}
		setSessionCookie(c, cfg, result.Token, result.ExpiresAt)
		c.Redirect(http.StatusFound, strings.TrimRight(cfg.WebPublicURL, "/")+"/")
	})
	auth.POST("/dev-login", func(c *gin.Context) {
		if !cfg.Auth.DevAuthEnabled || (cfg.AppEnv != "development" && cfg.AppEnv != "test") {
			writeError(c, apperror.ErrNotFound)
			return
		}
		var input struct {
			UserID   string `json:"userId"`
			TenantID string `json:"tenantId"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		if strings.TrimSpace(input.UserID) == "" {
			input.UserID = identity.DefaultDevUserID
		}
		if strings.TrimSpace(input.TenantID) == "" {
			input.TenantID = identity.DefaultDevTenantID
		}
		result, err := application.Identity.LoginDevelopment(c.Request.Context(), input.UserID, input.TenantID, c.Request.UserAgent(), c.ClientIP())
		if err != nil {
			writeError(c, err)
			return
		}
		setSessionCookie(c, cfg, result.Token, result.ExpiresAt)
		c.JSON(http.StatusOK, result)
	})
	auth.POST("/refresh", func(c *gin.Context) {
		raw := bearerToken(c)
		if raw == "" {
			raw, _ = c.Cookie(cfg.Auth.CookieName)
		}
		token, expiresAt, err := application.Identity.Refresh(c.Request.Context(), raw)
		if err != nil {
			writeError(c, err)
			return
		}
		setSessionCookie(c, cfg, token, expiresAt)
		c.JSON(http.StatusOK, gin.H{"token": token, "expiresAt": expiresAt})
	})
	auth.POST("/logout", func(c *gin.Context) {
		raw := bearerToken(c)
		if raw == "" {
			raw, _ = c.Cookie(cfg.Auth.CookieName)
		}
		if raw != "" {
			_ = application.Identity.RevokeSession(c.Request.Context(), raw)
		}
		clearCookie(c, cfg, cfg.Auth.CookieName)
		c.Status(http.StatusNoContent)
	})

	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	apiKeys := protected.Group("/api-keys")
	apiKeys.GET("", func(c *gin.Context) {
		if !requireSessionCredential(c) {
			return
		}
		rows, err := application.Identity.ListAPIKeys(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	apiKeys.POST("", func(c *gin.Context) {
		if !requireSessionCredential(c) {
			return
		}
		var input identity.CreateAPIKeyInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, token, err := application.Identity.CreateAPIKey(c.Request.Context(), accessContext(c).Principal, input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"apiKey": view, "token": token})
	})
	apiKeys.DELETE("/:id", func(c *gin.Context) {
		if !requireSessionCredential(c) {
			return
		}
		if err := application.Identity.RevokeAPIKey(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	tenantMembers := protected.Group("/tenant/members")
	tenantMembers.GET("", func(c *gin.Context) {
		rows, err := application.Authorization.ListTenantMembers(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	tenantMembers.POST("", func(c *gin.Context) {
		var input struct {
			UserID string `json:"userId" binding:"required"`
			Role   string `json:"role" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, err := application.Authorization.AddTenantMember(c.Request.Context(), accessContext(c).Principal, input.UserID, input.Role)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, view)
	})
	tenantMembers.PATCH("/:userID", func(c *gin.Context) {
		var input struct {
			Role string `json:"role" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		if err := application.Authorization.UpdateTenantMember(c.Request.Context(), accessContext(c).Principal, c.Param("userID"), input.Role); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	tenantMembers.DELETE("/:userID", func(c *gin.Context) {
		if err := application.Authorization.RemoveTenantMember(c.Request.Context(), accessContext(c).Principal, c.Param("userID")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	protected.GET("/audit", func(c *gin.Context) {
		query := authz.AuditQuery{
			ActorID:      c.Query("actorId"),
			Action:       c.Query("action"),
			ResourceType: c.Query("resourceType"),
			ResourceID:   c.Query("resourceId"),
			Limit:        intQuery(c, "limit"),
		}
		if raw := strings.TrimSpace(c.Query("allowed")); raw != "" {
			allowed := raw == "true" || raw == "1"
			query.Allowed = &allowed
		}
		rows, err := application.Authorization.QueryAudit(c.Request.Context(), accessContext(c).Principal, query)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})

	documents := protected.Group("/documents/:id")
	documents.GET("/versions", func(c *gin.Context) {
		page, err := application.Ingestion.ListVersions(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	documents.POST("/reparse", func(c *gin.Context) {
		result, err := application.Ingestion.Reparse(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})
	documents.POST("/versions/:versionID/activate", func(c *gin.Context) {
		view, err := application.Ingestion.ActivateVersion(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("versionID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	documents.POST("/restore", func(c *gin.Context) {
		document, err := application.Ingestion.Restore(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"document": document})
	})
	documents.POST("/purge", func(c *gin.Context) {
		if err := application.Ingestion.Purge(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	// Operator surfaces: task inspection, dead-letter replay/discard, manual
	// cancellation, reconciliation and the alerting metrics snapshot. They are
	// restricted to tenant owners/admins acting with a session credential.
	ops := protected.Group("/ops")
	ops.Use(requireOperator(application))
	ops.GET("/tasks/:key", func(c *gin.Context) {
		inspection, err := application.Tasks.Inspect(c.Request.Context(), accessContext(c).Principal, c.Param("key"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, inspection)
	})
	ops.GET("/dead-letters", func(c *gin.Context) {
		rows, err := application.Tasks.ListDeadLetters(c.Request.Context(), accessContext(c).Principal, c.Query("status"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	ops.POST("/dead-letters/:id/retry", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			writeError(c, apperror.ErrNotFound)
			return
		}
		if err := application.Tasks.RetryDeadLetter(c.Request.Context(), accessContext(c).Principal, id); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/dead-letters/:id/discard", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			writeError(c, apperror.ErrNotFound)
			return
		}
		if err := application.Tasks.DiscardDeadLetter(c.Request.Context(), accessContext(c).Principal, id); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/pipelines/:id/cancel", func(c *gin.Context) {
		if err := application.Tasks.CancelPipeline(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/reconcile", func(c *gin.Context) {
		staleAfter := 5 * time.Minute
		if seconds := intQuery(c, "staleAfterSeconds"); seconds > 0 {
			staleAfter = time.Duration(seconds) * time.Second
		}
		report, err := application.Ingestion.Reconcile(c.Request.Context(), staleAfter)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, report)
	})
	ops.GET("/metrics", func(c *gin.Context) {
		snapshot := observability.Default().Snapshot()
		if depth, err := application.Queue.QueueDepth(c.Request.Context()); err != nil {
			snapshot["queueError"] = err.Error()
		} else {
			snapshot["queues"] = depth
		}
		report, err := application.Ops.Snapshot(c.Request.Context(), accessContext(c).Principal.TenantID, time.Now().Add(-24*time.Hour), cfg.Limits.OutboxMaxAttempts)
		if err != nil {
			snapshot["operations"] = gin.H{"error": err.Error()}
		} else {
			snapshot["operations"] = report
		}
		c.JSON(http.StatusOK, snapshot)
	})
	ops.GET("/metrics/prometheus", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := observability.Default().WritePrometheus(c.Writer); err != nil {
			writeError(c, err)
		}
	})
}

func requireOperator(application *app.Application) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := application.Authorization.RequireTenantOperator(c.Request.Context(), accessContext(c).Principal); err != nil {
			writeError(c, err)
			return
		}
		c.Next()
	}
}

func requireSessionCredential(c *gin.Context) bool {
	if accessContext(c).Principal.CredentialType == "api_key" {
		writeError(c, apperror.ErrForbidden)
		return false
	}
	return true
}

func setSessionCookie(c *gin.Context, cfg config.Config, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: cfg.Auth.CookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: cfg.Auth.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func setOIDCCookie(c *gin.Context, cfg config.Config, name, value string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: "/api/v1/auth", MaxAge: oidcCookieTTL,
		HttpOnly: true, Secure: cfg.Auth.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(c *gin.Context, cfg config.Config, name string) {
	path := "/"
	if name == oidcStateCookie || name == oidcNonceCookie || name == oidcVerifierCookie {
		path = "/api/v1/auth"
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1,
		HttpOnly: true, Secure: cfg.Auth.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func redirectAuthError(c *gin.Context, cfg config.Config, code string) {
	c.Redirect(http.StatusFound, strings.TrimRight(cfg.WebPublicURL, "/")+"/login?error="+code)
}
