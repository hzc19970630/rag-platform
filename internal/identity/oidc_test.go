package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/config"
)

// oidcTestProvider is a wire-level OpenID provider: discovery, JWKS and token
// endpoints over real HTTP with RS256 signatures, so the client is exercised
// the same way it meets a hosted identity provider.
type oidcTestProvider struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	kid      string
	clientID string
	nonce    string
	issuer   string
}

func newOIDCTestProvider(t *testing.T, clientID string) *oidcTestProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	provider := &oidcTestProvider{key: key, kid: "test-key", clientID: clientID}
	provider.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"issuer": provider.server.URL, "authorization_endpoint": provider.server.URL + "/authorize",
				"token_endpoint": provider.server.URL + "/token", "jwks_uri": provider.server.URL + "/jwks",
			})
		case "/jwks":
			_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "kid": provider.kid, "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		case "/token":
			if err := request.ParseForm(); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			if request.Form.Get("code") != "valid-code" || request.Form.Get("code_verifier") != "valid-verifier" {
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			idToken, err := provider.signToken(provider.claims())
			if err != nil {
				http.Error(writer, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]string{"id_token": idToken, "access_token": "access", "token_type": "Bearer"})
		default:
			http.NotFound(writer, request)
		}
	}))
	provider.issuer = provider.server.URL
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *oidcTestProvider) claims() map[string]any {
	return map[string]any{
		"iss": p.issuer, "aud": p.clientID, "sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": p.nonce, "email": "user@example.com", "email_verified": true,
		"name": "Test User", "tenant_id": "tenant-42",
	}
}

func (p *oidcTestProvider) signToken(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (p *oidcTestProvider) client(t *testing.T) *OIDCClient {
	t.Helper()
	return NewOIDCClient(config.OIDCConfig{
		Enabled: true, Issuer: p.server.URL, ClientID: p.clientID, ClientSecret: "secret",
		RedirectURL: "http://localhost/callback", Scopes: []string{"openid", "email", "profile"},
		TenantClaim: "tenant_id", EmailClaim: "email", NameClaim: "name",
	})
}

func TestOIDCAuthorizationURLUsesPKCEStateAndNonce(t *testing.T) {
	provider := newOIDCTestProvider(t, "client-1")
	authorizationURL, err := provider.client(t).AuthorizationURL(context.Background(), "state-1", "nonce-1", "verifier-1")
	if err != nil {
		t.Fatalf("AuthorizationURL: %v", err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := parsed.Query()
	challenge := sha256.Sum256([]byte("verifier-1"))
	if query.Get("state") != "state-1" || query.Get("nonce") != "nonce-1" {
		t.Fatalf("state/nonce missing from authorization URL: %s", authorizationURL)
	}
	if query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE challenge missing from authorization URL: %s", authorizationURL)
	}
	if query.Get("client_id") != "client-1" || query.Get("response_type") != "code" || query.Get("scope") != "openid email profile" {
		t.Fatalf("authorization parameters incomplete: %s", authorizationURL)
	}
}

func TestOIDCExchangeVerifiesSignedIdentity(t *testing.T) {
	provider := newOIDCTestProvider(t, "client-1")
	provider.nonce = "nonce-1"
	identity, err := provider.client(t).Exchange(context.Background(), "valid-code", "valid-verifier", "nonce-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if identity.Subject != "user-42" || identity.Email != "user@example.com" || !identity.EmailVerified || identity.TenantID != "tenant-42" || identity.Name != "Test User" {
		t.Fatalf("identity claims not mapped: %+v", identity)
	}
	if identity.Issuer != provider.issuer {
		t.Fatalf("issuer = %q, want %q", identity.Issuer, provider.issuer)
	}
}

func TestOIDCExchangeRejectsInvalidResponses(t *testing.T) {
	provider := newOIDCTestProvider(t, "client-1")
	client := provider.client(t)
	if _, err := client.Exchange(context.Background(), "wrong-code", "valid-verifier", ""); err == nil {
		t.Fatal("a rejected authorization code must fail the exchange")
	}

	provider.nonce = "nonce-1"
	if _, err := client.Exchange(context.Background(), "valid-code", "valid-verifier", "different-nonce"); err == nil {
		t.Fatal("a mismatched nonce must be rejected")
	}
}

func TestOIDCRejectsUnsignedClaims(t *testing.T) {
	provider := newOIDCTestProvider(t, "client-1")
	client := provider.client(t)
	if _, err := client.Exchange(context.Background(), "valid-code", "valid-verifier", ""); err != nil {
		t.Fatalf("setup exchange: %v", err)
	}

	claims := provider.claims()
	claims["tenant_id"] = "attacker-tenant"
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": provider.kid})
	payload, _ := json.Marshal(claims)
	forged := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString([]byte("forged"))
	if _, err := client.verifyIDToken(context.Background(), forged, ""); err == nil {
		t.Fatal("a forged signature must be rejected")
	}

	wrongIssuer := provider.claims()
	wrongIssuer["iss"] = "https://evil.example.com"
	token, err := provider.signToken(wrongIssuer)
	if err != nil {
		t.Fatalf("sign wrong issuer: %v", err)
	}
	if _, err := client.verifyIDToken(context.Background(), token, ""); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("an issuer mismatch must be rejected, got %v", err)
	}

	wrongAudience := provider.claims()
	wrongAudience["aud"] = "other-client"
	token, err = provider.signToken(wrongAudience)
	if err != nil {
		t.Fatalf("sign wrong audience: %v", err)
	}
	if _, err := client.verifyIDToken(context.Background(), token, ""); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("an audience mismatch must be rejected, got %v", err)
	}

	expired := provider.claims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	token, err = provider.signToken(expired)
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}
	if _, err := client.verifyIDToken(context.Background(), token, ""); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired token must be rejected, got %v", err)
	}
}

func TestOIDCDiscoveryIssuerMismatchFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"issuer": "https://attacker.example.com", "authorization_endpoint": "https://attacker.example.com/authorize",
			"token_endpoint": "https://attacker.example.com/token", "jwks_uri": "https://attacker.example.com/jwks",
		})
	}))
	defer server.Close()
	client := NewOIDCClient(config.OIDCConfig{
		Enabled: true, Issuer: server.URL, ClientID: "client-1",
		RedirectURL: "http://localhost/callback", Scopes: []string{"openid"},
	})
	if _, err := client.loadDiscovery(context.Background()); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("discovery must reject an issuer that differs from configuration, got %v", err)
	}
}
