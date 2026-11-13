package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

func RegisterAuthRoutes(router *gin.Engine, cfg config.Config, deps AuthDeps, mw MiddlewareDeps, logger *slog.Logger) {
	api := router.Group("/api/v1")
	protected := api.Group("")
	protected.Use(authenticationMiddleware(cfg, mw))
	registerAuthRoutes(api, protected, cfg, deps, mw, logger)
}

func registerAuthRoutes(api, protected *gin.RouterGroup, cfg config.Config, deps AuthDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected.GET("/me", func(c *gin.Context) {
		access := accessContext(c)
		scope, err := deps.Authorization.BuildScope(c.Request.Context(), access.Principal, authz.QueryTarget{})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.Me(access.Principal, scope, access.DevIdentity))
	})
	protected.GET("/audit/authorization", func(c *gin.Context) {
		access := accessContext(c)
		rows, err := deps.Authorization.ListAudit(c.Request.Context(), access.Principal, 100)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.AuditEventList{Items: views.AuditEvents(rows)})
	})

	auth := api.Group("/auth")
	auth.GET("/config", func(c *gin.Context) {
		c.JSON(http.StatusOK, views.AuthConfig(
			deps.Identity.OIDCEnabled(),
			deps.Identity.OIDCIssuer(),
			deps.Identity.LDAPEnabled(),
			cfg.Auth.DevAuthEnabled && (cfg.AppEnv == "development" || cfg.AppEnv == "test"),
			cfg.Auth.CookieName,
			cfg.Auth.SessionTTLSeconds,
		))
	})
	auth.GET("/oidc/login", func(c *gin.Context) {
		if !deps.Identity.OIDCEnabled() {
			writeError(c, apperror.ErrNotFound)
			return
		}
		authorizationURL, state, nonce, verifier, err := deps.Identity.BeginOIDC(c.Request.Context())
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
		result, err := deps.Identity.CompleteOIDC(c.Request.Context(), c.Query("code"), verifier, nonce, c.Request.UserAgent(), clientIP(c))
		if err != nil {
			logger.Warn("oidc_callback_failed", "error", err)
			redirectAuthError(c, cfg, "oidc_callback_failed")
			return
		}
		setSessionCookie(c, cfg, result.Token, result.ExpiresAt)
		c.Redirect(http.StatusFound, strings.TrimRight(cfg.WebPublicURL, "/")+"/")
	})
	auth.POST("/ldap/login", func(c *gin.Context) {
		if !deps.Identity.LDAPEnabled() {
			writeError(c, apperror.ErrNotFound)
			return
		}
		var input httpapi.LDAPLoginRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		result, err := deps.Identity.LoginLDAP(c.Request.Context(), input.Username, input.Password, c.Request.UserAgent(), clientIP(c))
		if err != nil {
			logger.Warn("ldap_login_failed", "error", err)
			writeError(c, err)
			return
		}
		setSessionCookie(c, cfg, result.Token, result.ExpiresAt)
		c.JSON(http.StatusOK, views.DevLogin(result))
	})
	auth.POST("/dev-login", func(c *gin.Context) {
		if !cfg.Auth.DevAuthEnabled || (cfg.AppEnv != "development" && cfg.AppEnv != "test") {
			writeError(c, apperror.ErrNotFound)
			return
		}
		var input httpapi.DevLoginRequest
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
		result, err := deps.Identity.LoginDevelopment(c.Request.Context(), input.UserID, input.TenantID, c.Request.UserAgent(), clientIP(c))
		if err != nil {
			writeError(c, err)
			return
		}
		setSessionCookie(c, cfg, result.Token, result.ExpiresAt)
		c.JSON(http.StatusOK, views.DevLogin(result))
	})
	auth.POST("/refresh", func(c *gin.Context) {
		raw := bearerToken(c)
		if raw == "" {
			raw, _ = c.Cookie(cfg.Auth.CookieName)
		}
		token, expiresAt, err := deps.Identity.Refresh(c.Request.Context(), raw)
		if err != nil {
			writeError(c, err)
			return
		}
		setSessionCookie(c, cfg, token, expiresAt)
		c.JSON(http.StatusOK, httpapi.RefreshResponse{Token: token, ExpiresAt: expiresAt})
	})
	auth.POST("/logout", func(c *gin.Context) {
		raw := bearerToken(c)
		if raw == "" {
			raw, _ = c.Cookie(cfg.Auth.CookieName)
		}
		if raw != "" {
			_ = deps.Identity.RevokeSession(c.Request.Context(), raw)
		}
		clearCookie(c, cfg, cfg.Auth.CookieName)
		c.Status(http.StatusNoContent)
	})

	apiKeys := protected.Group("/api-keys")
	apiKeys.GET("", func(c *gin.Context) {
		if !requireSessionCredential(c) {
			return
		}
		rows, err := deps.Identity.ListAPIKeys(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.APIKeyList{Items: views.APIKeys(rows)})
	})
	apiKeys.POST("", func(c *gin.Context) {
		if !requireSessionCredential(c) {
			return
		}
		var input httpapi.APIKeyCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, token, err := deps.Identity.CreateAPIKey(c.Request.Context(), accessContext(c).Principal, views.CreateAPIKeyInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, httpapi.APIKeyCreateResponse{APIKey: views.APIKey(view), Token: token})
	})
	apiKeys.DELETE("/:id", func(c *gin.Context) {
		if !requireSessionCredential(c) {
			return
		}
		if err := deps.Identity.RevokeAPIKey(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	tenantMembers := protected.Group("/tenant/members")
	tenantMembers.GET("", func(c *gin.Context) {
		rows, err := deps.Authorization.ListTenantMembers(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.TenantMemberList{Items: views.TenantMembers(rows)})
	})
	tenantMembers.POST("", func(c *gin.Context) {
		var input httpapi.TenantMemberCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, err := deps.Authorization.AddTenantMember(c.Request.Context(), accessContext(c).Principal, input.UserID, input.Role)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.TenantMember(view))
	})
	tenantMembers.PATCH("/:userID", func(c *gin.Context) {
		var input httpapi.TenantMemberUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		if err := deps.Authorization.UpdateTenantMember(c.Request.Context(), accessContext(c).Principal, c.Param("userID"), input.Role); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	tenantMembers.DELETE("/:userID", func(c *gin.Context) {
		if err := deps.Authorization.RemoveTenantMember(c.Request.Context(), accessContext(c).Principal, c.Param("userID")); err != nil {
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
		rows, err := deps.Authorization.QueryAudit(c.Request.Context(), accessContext(c).Principal, query)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.AuditEventList{Items: views.AuditEvents(rows)})
	})
}
