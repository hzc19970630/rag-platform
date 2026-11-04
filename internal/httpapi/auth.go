package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/identity"
	"github.com/gin-gonic/gin"
)

func RegisterAuthRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
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
}
