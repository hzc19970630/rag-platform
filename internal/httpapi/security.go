package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/config"
)

// clientIP returns the client address resolved through the configured trusted
// proxy chain. A trusted proxy that forwards a malformed value is ignored in
// favour of the TCP peer, so audit rows always carry a parseable address.
func clientIP(c *gin.Context) string {
	if ip := net.ParseIP(c.ClientIP()); ip != nil {
		return ip.String()
	}
	return c.RemoteIP()
}

// csrfProtection rejects cookie-authenticated unsafe requests that a browser
// attributes to another origin. SameSite=Lax stops cross-site form posts from
// carrying the session cookie, but a sibling origin on the same site still
// does, so the source is checked explicitly. Bearer and API-key requests are
// not CSRF-able because browsers never attach those credentials automatically.
func csrfProtection(cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSafeMethod(c.Request.Method) || !hasSessionCookie(c, cfg) || requestOriginAllowed(c, cfg) {
			c.Next()
			return
		}
		writeError(c, apperror.ErrForbidden)
	}
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func hasSessionCookie(c *gin.Context, cfg config.Config) bool {
	name := strings.TrimSpace(cfg.Auth.CookieName)
	if name == "" {
		return false
	}
	value, err := c.Cookie(name)
	return err == nil && value != ""
}

// requestOriginAllowed compares the Origin header, falling back to Referer,
// with the request host and the configured browser-facing origins. Requests
// carrying neither header are allowed: non-browser clients do not send them,
// and a cross-site browser write would not carry the cookie in the first place.
func requestOriginAllowed(c *gin.Context, cfg config.Config) bool {
	if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
		if strings.EqualFold(origin, "null") {
			return false
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" {
			return false
		}
		return hostAllowed(parsed.Host, c.Request.Host, cfg)
	}
	referer := strings.TrimSpace(c.GetHeader("Referer"))
	if referer == "" {
		return true
	}
	parsed, err := url.Parse(referer)
	if err != nil || parsed.Host == "" {
		return false
	}
	return hostAllowed(parsed.Host, c.Request.Host, cfg)
}

func hostAllowed(originHost, requestHost string, cfg config.Config) bool {
	if strings.EqualFold(originHost, requestHost) {
		return true
	}
	if web := strings.TrimSpace(cfg.WebPublicURL); web != "" {
		if parsed, err := url.Parse(web); err == nil && parsed.Host != "" && strings.EqualFold(originHost, parsed.Host) {
			return true
		}
	}
	if cfg.AppEnv == "development" || cfg.AppEnv == "test" {
		return strings.EqualFold(originHost, "localhost:5173") || strings.EqualFold(originHost, "127.0.0.1:5173")
	}
	return false
}
