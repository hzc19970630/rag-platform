package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
)

// A spoofed X-Forwarded-For from an untrusted peer must not become the
// principal's audit IP. Gin trusts every proxy unless the router configures a
// trusted chain, so this is the routing-level half of the A5 guarantee.
func TestSpoofedForwardedForDoesNotChangeAuditIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{AppEnv: "test"})
	var auditIP string
	router.POST("/audit-ip", func(c *gin.Context) {
		auditIP = withRequestMetadata(c, authz.Principal{}, "").IPAddress
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/audit-ip", nil)
	request.RemoteAddr = "192.0.2.10:3456"
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	router.ServeHTTP(httptest.NewRecorder(), request)

	if auditIP != "192.0.2.10" {
		t.Fatalf("spoofed forwarded header changed the audit IP: got %q, want the TCP peer 192.0.2.10", auditIP)
	}
}

// Trusted proxy entries are the only peers whose forwarded headers are read.
// A trusted peer contributes its client address; an untrusted peer cannot.
func TestTrustedProxyChainResolvesForwardedClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{AppEnv: "test", TrustedProxies: []string{"192.0.2.0/24"}})
	var auditIP string
	router.GET("/audit-ip", func(c *gin.Context) {
		auditIP = withRequestMetadata(c, authz.Principal{}, "").IPAddress
		c.Status(http.StatusNoContent)
	})

	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"trusted peer contributes the client", "192.0.2.10:3456", "203.0.113.7", "203.0.113.7"},
		{"trusted chain drops its own hop", "192.0.2.10:3456", "203.0.113.7, 192.0.2.20", "203.0.113.7"},
		{"untrusted peer is the client", "198.51.100.9:3456", "203.0.113.7", "198.51.100.9"},
		{"malformed value falls back to the peer", "192.0.2.10:3456", "not-an-ip", "192.0.2.10"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/audit-ip", nil)
			request.RemoteAddr = testCase.remoteAddr
			request.Header.Set("X-Forwarded-For", testCase.forwarded)
			router.ServeHTTP(httptest.NewRecorder(), request)
			if auditIP != testCase.want {
				t.Fatalf("audit IP = %q, want %q", auditIP, testCase.want)
			}
		})
	}
}

func TestCookieSessionAllowsCrossSiteRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{AppEnv: "test"}
	cfg.Auth.CookieName = "rag_session"
	router := NewRouter(cfg)
	router.GET("/read", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/read", nil)
	request.Host = "api.example.com"
	request.AddCookie(&http.Cookie{Name: cfg.Auth.CookieName, Value: "session-token"})
	request.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("safe methods must not be blocked by CSRF protection, got %d", recorder.Code)
	}
}

// A session cookie must not authenticate a cross-site state-changing request.
// SameSite=Lax covers cross-site form posts, but same-site sibling origins still
// send the cookie, so the router checks Origin/Referer as well.
func TestCookieSessionRejectsCrossSiteWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{AppEnv: "test", WebPublicURL: "http://localhost:5173"}
	cfg.Auth.CookieName = "rag_session"
	router := NewRouter(cfg)
	router.POST("/write", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	cases := []struct {
		name    string
		cookie  bool
		origin  string
		referer string
		want    int
	}{
		{"cross-site origin", true, "https://evil.example", "", http.StatusForbidden},
		{"cross-site referer", true, "", "https://evil.example/write", http.StatusForbidden},
		{"opaque origin", true, "null", "", http.StatusForbidden},
		{"same-origin", true, "https://api.example.com", "", http.StatusNoContent},
		{"configured web origin", true, "http://localhost:5173", "", http.StatusNoContent},
		{"api client without cookie", false, "https://evil.example", "", http.StatusNoContent},
		{"non-browser cookie client", true, "", "", http.StatusNoContent},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://api.example.com/write", nil)
			request.Host = "api.example.com"
			if testCase.cookie {
				request.AddCookie(&http.Cookie{Name: cfg.Auth.CookieName, Value: "session-token"})
			}
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			if testCase.referer != "" {
				request.Header.Set("Referer", testCase.referer)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("expected %d, got %d: %s", testCase.want, recorder.Code, recorder.Body.String())
			}
		})
	}
}
