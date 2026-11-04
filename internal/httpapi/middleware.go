package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/identity"
	"github.com/gin-gonic/gin"
)

const (
	oidcStateCookie    = "rag_oidc_state"
	oidcNonceCookie    = "rag_oidc_nonce"
	oidcVerifierCookie = "rag_oidc_verifier"
	oidcCookieTTL      = 600
)

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
