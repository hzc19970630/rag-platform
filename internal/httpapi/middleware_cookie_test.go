package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
)

func TestSetSessionCookieAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Auth: config.AuthConfig{CookieName: "rag_session", CookieSecure: true}}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	setSessionCookie(context, cfg, "token-1", time.Now().Add(time.Hour))
	cookie := responseCookie(t, recorder, "rag_session")
	if cookie.Value != "token-1" || cookie.Path != "/" || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("session cookie = %+v", cookie)
	}
	if cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 || cookie.MaxAge > 3600 {
		t.Fatalf("session cookie lifetime = %+v", cookie)
	}

	expiredRecorder := httptest.NewRecorder()
	expiredContext, _ := gin.CreateTestContext(expiredRecorder)
	setSessionCookie(expiredContext, cfg, "token-2", time.Now().Add(-time.Hour))
	expired := responseCookie(t, expiredRecorder, "rag_session")
	if expired.MaxAge != 1 {
		t.Fatalf("expired session cookie MaxAge = %d, want 1", expired.MaxAge)
	}
}

func TestSetAndClearOIDCCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Auth: config.AuthConfig{CookieName: "rag_session", CookieSecure: true}}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	setOIDCCookie(context, cfg, oidcStateCookie, "state-1")
	cookie := responseCookie(t, recorder, oidcStateCookie)
	if cookie.Value != "state-1" || cookie.Path != "/api/v1/auth" || cookie.MaxAge != oidcCookieTTL || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("oidc cookie = %+v", cookie)
	}

	sessionRecorder := httptest.NewRecorder()
	sessionContext, _ := gin.CreateTestContext(sessionRecorder)
	clearCookie(sessionContext, cfg, "rag_session")
	cleared := responseCookie(t, sessionRecorder, "rag_session")
	if cleared.Value != "" || cleared.Path != "/" || cleared.MaxAge != -1 || !cleared.HttpOnly {
		t.Fatalf("cleared session cookie = %+v", cleared)
	}

	oidcRecorder := httptest.NewRecorder()
	oidcContext, _ := gin.CreateTestContext(oidcRecorder)
	clearCookie(oidcContext, cfg, oidcNonceCookie)
	clearedOIDC := responseCookie(t, oidcRecorder, oidcNonceCookie)
	if clearedOIDC.Path != "/api/v1/auth" || clearedOIDC.MaxAge != -1 {
		t.Fatalf("cleared oidc cookie = %+v", clearedOIDC)
	}
}

func TestRedirectAuthError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{WebPublicURL: "https://web.example/"}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	redirectAuthError(context, cfg, "state_mismatch")
	if recorder.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "https://web.example/login?error=state_mismatch" {
		t.Fatalf("redirect location = %q", location)
	}
}

func TestRequireSessionCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKeyRecorder := httptest.NewRecorder()
	apiKeyContext, _ := gin.CreateTestContext(apiKeyRecorder)
	apiKeyContext.Set(accessContextKey, authz.AccessContext{Principal: authz.Principal{CredentialType: "api_key"}})
	if requireSessionCredential(apiKeyContext) {
		t.Fatal("api key credential must not pass requireSessionCredential")
	}
	if apiKeyRecorder.Code != http.StatusForbidden {
		t.Fatalf("api key rejection status = %d, want 403", apiKeyRecorder.Code)
	}

	sessionRecorder := httptest.NewRecorder()
	sessionContext, _ := gin.CreateTestContext(sessionRecorder)
	sessionContext.Set(accessContextKey, authz.AccessContext{Principal: authz.Principal{CredentialType: "session"}})
	if !requireSessionCredential(sessionContext) {
		t.Fatal("session credential must pass requireSessionCredential")
	}
}

func TestBearerTokenParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "empty", header: "", want: ""},
		{name: "wrong scheme", header: "Basic abc", want: ""},
		{name: "missing token", header: "Bearer", want: ""},
		{name: "bearer lowercase", header: "bearer token-1", want: "token-1"},
		{name: "trims token", header: "Bearer  token-2  ", want: "token-2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			if test.header != "" {
				context.Request.Header.Set("Authorization", test.header)
			}
			if got := bearerToken(context); got != test.want {
				t.Fatalf("bearerToken(%q) = %q, want %q", test.header, got, test.want)
			}
		})
	}
}

func TestModelActorCopiesRequestMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set(accessContextKey, authz.AccessContext{
		Principal: authz.Principal{
			SubjectID: "user-1", TenantID: "tenant-1",
			RequestID: "req-1", IPAddress: "127.0.0.1", UserAgent: "vitest",
		},
	})
	actor := modelActor(context)
	if actor.ID != "user-1" || actor.RequestID != "req-1" || actor.IPAddress != "127.0.0.1" || actor.UserAgent != "vitest" {
		t.Fatalf("modelActor = %+v", actor)
	}
}
