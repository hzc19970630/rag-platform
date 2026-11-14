package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	"gorm.io/gorm"
)

func newAuthFlowRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	_, _, db := newTenantHTTPTest(t)
	if err := db.AutoMigrate(&models.AuthSession{}, &models.KnowledgeBaseMember{}); err != nil {
		t.Fatalf("migrate auth sessions: %v", err)
	}
	identities, err := identity.NewService(db, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600,
		APIKeyPepper: "test-pepper", DevAuthEnabled: true,
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	cfg := config.Config{
		AppEnv:       "test",
		WebPublicURL: "https://web.example",
		Auth:         config.AuthConfig{DevAuthEnabled: true, CookieName: "rag_session"},
	}
	router := gin.New()
	RegisterRoutes(router, cfg, RouteDeps{
		Identity: identities, Authorization: authzService, Limiter: ratelimit.New(0),
	}, slog.Default())
	return router, db
}

func authRequest(t *testing.T, router http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func authRequestWithBearer(t *testing.T, router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func responseCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found in %v", name, recorder.Result().Cookies())
	return nil
}

func TestAuthConfigAndDevLoginFlow(t *testing.T) {
	router, _ := newAuthFlowRouter(t)

	configRecorder := authRequest(t, router, http.MethodGet, "/api/v1/auth/config", "")
	if configRecorder.Code != http.StatusOK {
		t.Fatalf("auth config status = %d, want 200", configRecorder.Code)
	}
	var authConfig httpapi.AuthConfig
	if err := json.Unmarshal(configRecorder.Body.Bytes(), &authConfig); err != nil {
		t.Fatalf("decode auth config: %v", err)
	}
	if !authConfig.DevAuthEnabled || authConfig.SessionCookie != "rag_session" {
		t.Fatalf("auth config = %+v", authConfig)
	}

	invalid := authRequest(t, router, http.MethodPost, "/api/v1/auth/dev-login", "{")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid dev-login status = %d, want 400", invalid.Code)
	}

	defaults := authRequest(t, router, http.MethodPost, "/api/v1/auth/dev-login", "{}")
	if defaults.Code != http.StatusForbidden {
		t.Fatalf("default dev-login status = %d, want 403 (no dev membership)", defaults.Code)
	}

	login := authRequest(t, router, http.MethodPost, "/api/v1/auth/dev-login", `{"userId":"owner-a","tenantId":"tenant-a"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("dev-login status = %d, body = %s", login.Code, login.Body.String())
	}
	var session httpapi.DevLoginResponse
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode dev-login: %v", err)
	}
	if session.Token == "" || session.User.ID != "owner-a" || session.Principal.TenantID != "tenant-a" {
		t.Fatalf("dev-login response = %+v", session)
	}
	sessionCookie := responseCookie(t, login, "rag_session")
	if sessionCookie.Value != session.Token || sessionCookie.Path != "/" || !sessionCookie.HttpOnly {
		t.Fatalf("session cookie = %+v", sessionCookie)
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.MaxAge <= 0 {
		t.Fatalf("session cookie attributes = %+v", sessionCookie)
	}

	me := authRequest(t, router, http.MethodGet, "/api/v1/me", "", sessionCookie)
	if me.Code != http.StatusOK {
		t.Fatalf("me with session cookie status = %d, body = %s", me.Code, me.Body.String())
	}

	refresh := authRequest(t, router, http.MethodPost, "/api/v1/auth/refresh", "", sessionCookie)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	var refreshed httpapi.RefreshResponse
	if err := json.Unmarshal(refresh.Body.Bytes(), &refreshed); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	if refreshed.Token == "" {
		t.Fatal("refresh returned an empty token")
	}
	refreshedCookie := responseCookie(t, refresh, "rag_session")
	if refreshedCookie.Value != refreshed.Token {
		t.Fatalf("refreshed cookie = %+v", refreshedCookie)
	}

	logout := authRequest(t, router, http.MethodPost, "/api/v1/auth/logout", "", refreshedCookie)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", logout.Code)
	}
	cleared := responseCookie(t, logout, "rag_session")
	if cleared.Value != "" || cleared.MaxAge != -1 {
		t.Fatalf("cleared cookie = %+v", cleared)
	}
	afterLogout := authRequest(t, router, http.MethodGet, "/api/v1/me", "", refreshedCookie)
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status = %d, want 401", afterLogout.Code)
	}
}

func TestDevLoginDisabledReturnsNotFound(t *testing.T) {
	_, identities, _ := newTenantHTTPTest(t)
	gin.SetMode(gin.TestMode)
	cfg := config.Config{AppEnv: "test", Auth: config.AuthConfig{DevAuthEnabled: false, CookieName: "rag_session"}}
	router := gin.New()
	RegisterAuthRoutes(router, cfg, AuthDeps{Identity: identities}, MiddlewareDeps{Identity: identities, Limiter: ratelimit.New(0)}, slog.Default())

	login := authRequest(t, router, http.MethodPost, "/api/v1/auth/dev-login", `{"userId":"owner-a","tenantId":"tenant-a"}`)
	if login.Code != http.StatusNotFound {
		t.Fatalf("disabled dev-login status = %d, want 404", login.Code)
	}
	oidc := authRequest(t, router, http.MethodGet, "/api/v1/auth/oidc/login", "")
	if oidc.Code != http.StatusNotFound {
		t.Fatalf("disabled oidc-login status = %d, want 404", oidc.Code)
	}
	configRecorder := authRequest(t, router, http.MethodGet, "/api/v1/auth/config", "")
	var authConfig httpapi.AuthConfig
	if err := json.Unmarshal(configRecorder.Body.Bytes(), &authConfig); err != nil {
		t.Fatalf("decode auth config: %v", err)
	}
	if authConfig.DevAuthEnabled {
		t.Fatalf("auth config = %+v, want dev login disabled", authConfig)
	}
}

func TestOIDCCallbackFailures(t *testing.T) {
	router, _, _ := newTenantHTTPTest(t)

	mismatch := authRequest(t, router, http.MethodGet, "/api/v1/auth/callback", "")
	if mismatch.Code != http.StatusFound || mismatch.Header().Get("Location") != "/login?error=state_mismatch" {
		t.Fatalf("state mismatch callback = %d %q", mismatch.Code, mismatch.Header().Get("Location"))
	}
	for _, name := range []string{oidcStateCookie, oidcNonceCookie, oidcVerifierCookie} {
		cleared := responseCookie(t, mismatch, name)
		if cleared.Value != "" || cleared.MaxAge != -1 || cleared.Path != "/api/v1/auth" {
			t.Fatalf("cleared oidc cookie %s = %+v", name, cleared)
		}
	}

	providerError := authRequest(t, router, http.MethodGet, "/api/v1/auth/callback?state=abc&error=access_denied", "", &http.Cookie{Name: oidcStateCookie, Value: "abc"})
	if providerError.Code != http.StatusFound || providerError.Header().Get("Location") != "/login?error=access_denied" {
		t.Fatalf("provider error callback = %d %q", providerError.Code, providerError.Header().Get("Location"))
	}

	callbackCookies := []*http.Cookie{
		{Name: oidcStateCookie, Value: "abc"},
		{Name: oidcNonceCookie, Value: "nonce"},
		{Name: oidcVerifierCookie, Value: "verifier"},
	}
	exchangeFailure := authRequest(t, router, http.MethodGet, "/api/v1/auth/callback?state=abc&code=code", "", callbackCookies...)
	if exchangeFailure.Code != http.StatusFound || exchangeFailure.Header().Get("Location") != "/login?error=oidc_callback_failed" {
		t.Fatalf("exchange failure callback = %d %q", exchangeFailure.Code, exchangeFailure.Header().Get("Location"))
	}
}

func TestOIDCLoginRedirectsWithStateCookies(t *testing.T) {
	_, _, db := newTenantHTTPTest(t)
	var discovery *httptest.Server
	discovery = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"issuer":                 discovery.URL,
			"authorization_endpoint": discovery.URL + "/authorize",
			"token_endpoint":         discovery.URL + "/token",
			"jwks_uri":               discovery.URL + "/jwks",
		})
	}))
	t.Cleanup(discovery.Close)

	oidcIdentities, err := identity.NewService(db, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600, APIKeyPepper: "test-pepper",
		OIDC: identity.OIDCOptions{
			Enabled: true, Issuer: discovery.URL, ClientID: "client-1", ClientSecret: "secret",
			RedirectURL: "http://localhost/callback", Scopes: []string{"openid", "email"},
			TenantClaim: "tenant_id", EmailClaim: "email", NameClaim: "name", DefaultTenant: "tenant-a",
		},
	})
	if err != nil {
		t.Fatalf("oidc identity service: %v", err)
	}
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv:       "test",
		WebPublicURL: "https://web.example",
		Auth:         config.AuthConfig{DevAuthEnabled: true, CookieName: "rag_session"},
	}
	router := gin.New()
	RegisterAuthRoutes(router, cfg, AuthDeps{Identity: oidcIdentities}, MiddlewareDeps{Identity: oidcIdentities, Limiter: ratelimit.New(0)}, slog.Default())

	login := authRequest(t, router, http.MethodGet, "/api/v1/auth/oidc/login", "")
	if login.Code != http.StatusFound {
		t.Fatalf("oidc login status = %d, body = %s", login.Code, login.Body.String())
	}
	location := login.Header().Get("Location")
	if !strings.HasPrefix(location, discovery.URL+"/authorize?") || !strings.Contains(location, "code_challenge=") || !strings.Contains(location, "state=") {
		t.Fatalf("oidc login location = %q", location)
	}
	for _, name := range []string{oidcStateCookie, oidcNonceCookie, oidcVerifierCookie} {
		cookie := responseCookie(t, login, name)
		if cookie.Value == "" || cookie.Path != "/api/v1/auth" || cookie.MaxAge != oidcCookieTTL || !cookie.HttpOnly {
			t.Fatalf("oidc cookie %s = %+v", name, cookie)
		}
	}
}

func TestAPIKeyCredentialCannotManageAPIKeys(t *testing.T) {
	router, _ := newAuthFlowRouter(t)
	login := authRequest(t, router, http.MethodPost, "/api/v1/auth/dev-login", `{"userId":"owner-a","tenantId":"tenant-a"}`)
	var session httpapi.DevLoginResponse
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode dev-login: %v", err)
	}

	create := authRequestWithBearer(t, router, http.MethodPost, "/api/v1/api-keys", `{"name":"ci","capabilities":["knowledge_base:read"]}`, session.Token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create api key status = %d, body = %s", create.Code, create.Body.String())
	}
	var created struct {
		APIKey struct {
			ID string `json:"id"`
		} `json:"apiKey"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode api key: %v", err)
	}
	if !strings.HasPrefix(created.Token, "rk_") || created.APIKey.ID == "" {
		t.Fatalf("created api key = %+v", created)
	}

	list := authRequestWithBearer(t, router, http.MethodGet, "/api/v1/api-keys", "", created.Token)
	if list.Code != http.StatusForbidden {
		t.Fatalf("api-key list with api key status = %d, want 403", list.Code)
	}
	remove := authRequestWithBearer(t, router, http.MethodDelete, "/api/v1/api-keys/"+created.APIKey.ID, "", created.Token)
	if remove.Code != http.StatusForbidden {
		t.Fatalf("api-key delete with api key status = %d, want 403", remove.Code)
	}
}
