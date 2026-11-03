package identity

import (
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/config"
)

func testSigner(t *testing.T) *tokenSigner {
	t.Helper()
	signer, err := newTokenSigner(config.AuthConfig{
		SigningKeys:       []string{"old:old-secret-at-least-16", "new:new-secret-at-least-16"},
		ActiveKeyID:       "new",
		SessionTTLSeconds: 3600,
		JWTIssuer:         "rag-platform",
		JWTAudience:       "rag-api",
	})
	if err != nil {
		t.Fatalf("newTokenSigner: %v", err)
	}
	return signer
}

func validClaims(expires time.Time) sessionClaims {
	return sessionClaims{
		Subject: "user-1", TenantID: "tenant-1", SessionID: "session-1", TokenID: "token-1",
		ExpiresAt: expires, IssuedAt: time.Now().Add(-time.Minute),
	}
}

func TestTokenSignerIssuesWithActiveKeyAndVerifies(t *testing.T) {
	signer := testSigner(t)
	raw, err := signer.issue(validClaims(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(raw, encodeSegment([]byte(`{"alg":"HS256","typ":"JWT","kid":"new"}`))) {
		t.Fatal("token must be signed with the active key id")
	}
	claims, err := signer.verify(raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "user-1" || claims.TenantID != "tenant-1" || claims.SessionID != "session-1" || claims.TokenID != "token-1" {
		t.Fatalf("claims round trip failed: %+v", claims)
	}
}

func TestTokenSignerKeepsOldKeyForRotation(t *testing.T) {
	oldSigner, err := newTokenSigner(config.AuthConfig{
		SigningKeys: []string{"old:old-secret-at-least-16", "new:new-secret-at-least-16"},
		ActiveKeyID: "old", SessionTTLSeconds: 3600, JWTIssuer: "rag-platform", JWTAudience: "rag-api",
	})
	if err != nil {
		t.Fatalf("newTokenSigner: %v", err)
	}
	raw, err := oldSigner.issue(validClaims(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("issue with old key: %v", err)
	}

	rotated := testSigner(t)
	if _, err := rotated.verify(raw); err != nil {
		t.Fatalf("a token signed by the retired key must verify during rotation: %v", err)
	}

	withoutOld, err := newTokenSigner(config.AuthConfig{
		SigningKeys: []string{"new:new-secret-at-least-16"},
		ActiveKeyID: "new", SessionTTLSeconds: 3600, JWTIssuer: "rag-platform", JWTAudience: "rag-api",
	})
	if err != nil {
		t.Fatalf("newTokenSigner: %v", err)
	}
	if _, err := withoutOld.verify(raw); err == nil {
		t.Fatal("a token whose key was removed must be rejected")
	}
}

func TestTokenSignerRejectsInvalidTokens(t *testing.T) {
	signer := testSigner(t)
	raw, err := signer.issue(validClaims(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	parts := strings.Split(raw, ".")
	tamperedPayload := encodeSegment([]byte(`{"sub":"attacker","tenant":"tenant-1","sid":"session-1","jti":"token-1","iss":"rag-platform","aud":"rag-api","exp":4102444800,"iat":1}`))
	cases := map[string]string{
		"malformed":       "not-a-token",
		"tampered":        parts[0] + "." + tamperedPayload + "." + parts[2],
		"bad signature":   parts[0] + "." + parts[1] + "." + encodeSegment([]byte("bogus")),
		"unknown key id":  encodeSegment([]byte(`{"alg":"HS256","typ":"JWT","kid":"missing"}`)) + "." + parts[1] + "." + parts[2],
		"wrong algorithm": encodeSegment([]byte(`{"alg":"none","typ":"JWT","kid":"new"}`)) + "." + parts[1] + "." + parts[2],
	}
	for name, token := range cases {
		if _, err := signer.verify(token); err == nil {
			t.Fatalf("%s token must be rejected", name)
		}
	}

	expired, err := signer.issue(validClaims(time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatalf("issue expired: %v", err)
	}
	if _, err := signer.verify(expired); err == nil {
		t.Fatal("an expired token must be rejected")
	}

	claims := validClaims(time.Now().Add(time.Hour))
	claims.IssuedAt = time.Now().Add(5 * time.Minute)
	future, err := signer.issue(claims)
	if err != nil {
		t.Fatalf("issue future: %v", err)
	}
	if _, err := signer.verify(future); err == nil {
		t.Fatal("a token issued in the future must be rejected")
	}
}

func TestTokenSignerRequiresConfiguredActiveKey(t *testing.T) {
	if _, err := newTokenSigner(config.AuthConfig{
		SigningKeys: []string{"old:old-secret-at-least-16"}, ActiveKeyID: "missing",
		SessionTTLSeconds: 3600, JWTIssuer: "rag-platform", JWTAudience: "rag-api",
	}); err == nil {
		t.Fatal("an active key id that is not configured must be rejected")
	}
	if _, err := newTokenSigner(config.AuthConfig{
		SigningKeys: []string{":secret"}, ActiveKeyID: "",
		SessionTTLSeconds: 3600, JWTIssuer: "rag-platform", JWTAudience: "rag-api",
	}); err == nil {
		t.Fatal("an entry without kid:secret form must be rejected")
	}
}
