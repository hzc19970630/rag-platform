package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	oidcStateCookie    = "rag_oidc_state"
	oidcNonceCookie    = "rag_oidc_nonce"
	oidcVerifierCookie = "rag_oidc_verifier"
	oidcCookieTTL      = 600
)

func authenticationMiddleware(cfg config.Config, deps MiddlewareDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		raw := bearerToken(c)
		if raw == "" {
			if cookie, err := c.Cookie(cfg.Auth.CookieName); err == nil {
				raw = cookie
			}
		}
		if raw != "" {
			principal, err := resolveCredential(c, deps.Identity, raw)
			if err != nil {
				writeError(c, err)
				return
			}
			principal = withRequestMetadata(c, principal, requestID)
			if !deps.Limiter.Allow(principal.TenantID) {
				writeError(c, apperror.ErrRateLimited)
				return
			}
			c.Set(accessContextKey, authz.AccessContext{Principal: principal, RequestID: requestID})
			runWithTenantTx(c, deps.DB, principal.TenantID)
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
		principal, err := deps.Identity.ResolveDevelopmentPrincipal(c.Request.Context(), userID, tenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		principal.CredentialType = "dev"
		principal = withRequestMetadata(c, principal, requestID)
		if !deps.Limiter.Allow(principal.TenantID) {
			writeError(c, apperror.ErrRateLimited)
			return
		}
		c.Set(accessContextKey, authz.AccessContext{Principal: principal, RequestID: requestID, DevIdentity: true})
		runWithTenantTx(c, deps.DB, principal.TenantID)
	}
}

// runWithTenantTx continues the chain inside the request-scoped tenant
// transaction when RLS is enforced. Outside enforce mode BeginRequestTx is a
// no-op, so shadow deployments and tests keep the pooled behavior.
func runWithTenantTx(c *gin.Context, db *gorm.DB, tenantID string) {
	ctx := tenantdb.WithTenant(c.Request.Context(), tenantID)
	c.Request = c.Request.WithContext(ctx)
	if db == nil {
		c.Next()
		return
	}
	ctx, finish, err := tenantdb.BeginRequestTx(ctx, db, tenantID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Request = c.Request.WithContext(ctx)
	defer func() {
		if recovered := recover(); recovered != nil {
			finish(fmt.Errorf("handler panic: %v", recovered))
			panic(recovered)
		}
		// A failed request must not leave half a tenant's writes committed.
		var cause error
		if status := c.Writer.Status(); status >= http.StatusInternalServerError {
			cause = fmt.Errorf("request finished with status %d", status)
		}
		finish(cause)
	}()
	c.Next()
}

// withRequestMetadata copies the request correlation and client origin onto the
// principal so every audit row written below the HTTP layer carries them.
func withRequestMetadata(c *gin.Context, principal authz.Principal, requestID string) authz.Principal {
	principal.RequestID = requestID
	principal.IPAddress = clientIP(c)
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

func requireOperator(deps MiddlewareDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := deps.Authorization.RequireTenantOperator(c.Request.Context(), accessContext(c).Principal); err != nil {
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
