package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type tokenSigner struct {
	keys     map[string][]byte
	active   string
	issuer   string
	audience string
	ttl      time.Duration
}

type sessionClaims struct {
	Subject   string
	TenantID  string
	SessionID string
	TokenID   string
	ExpiresAt time.Time
	IssuedAt  time.Time
}

type tokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type tokenPayload struct {
	Subject   string `json:"sub"`
	TenantID  string `json:"tenant"`
	SessionID string `json:"sid"`
	TokenID   string `json:"jti"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

func newTokenSigner(cfg AuthOptions) (*tokenSigner, error) {
	keys := make(map[string][]byte, len(cfg.SigningKeys))
	for _, entry := range cfg.SigningKeys {
		kid, secret, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(kid) == "" || secret == "" {
			return nil, fmt.Errorf("invalid signing key entry %q", kid)
		}
		keys[kid] = []byte(secret)
	}
	if _, ok := keys[cfg.ActiveKeyID]; !ok {
		return nil, fmt.Errorf("active signing key %q is not configured", cfg.ActiveKeyID)
	}
	ttl := time.Duration(cfg.SessionTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &tokenSigner{keys: keys, active: cfg.ActiveKeyID, issuer: cfg.JWTIssuer, audience: cfg.JWTAudience, ttl: ttl}, nil
}

func (s *tokenSigner) issue(claims sessionClaims) (string, error) {
	header, err := json.Marshal(tokenHeader{Algorithm: "HS256", Type: "JWT", KeyID: s.active})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(tokenPayload{
		Subject: claims.Subject, TenantID: claims.TenantID, SessionID: claims.SessionID, TokenID: claims.TokenID,
		Issuer: s.issuer, Audience: s.audience, ExpiresAt: claims.ExpiresAt.Unix(), IssuedAt: claims.IssuedAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := encodeSegment(header) + "." + encodeSegment(payload)
	signature := s.sign(s.active, signingInput)
	return signingInput + "." + encodeSegment(signature), nil
}

func (s *tokenSigner) verify(raw string) (sessionClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return sessionClaims{}, fmt.Errorf("malformed session token")
	}
	headerBytes, err := decodeSegment(parts[0])
	if err != nil {
		return sessionClaims{}, fmt.Errorf("malformed session token header")
	}
	var header tokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return sessionClaims{}, fmt.Errorf("malformed session token header")
	}
	if header.Algorithm != "HS256" {
		return sessionClaims{}, fmt.Errorf("unsupported session token algorithm")
	}
	key, ok := s.keys[header.KeyID]
	if !ok {
		return sessionClaims{}, fmt.Errorf("unknown session signing key")
	}
	signature, err := decodeSegment(parts[2])
	if err != nil {
		return sessionClaims{}, fmt.Errorf("malformed session token signature")
	}
	expected := signWithKey(key, parts[0]+"."+parts[1])
	if !hmac.Equal(signature, expected) {
		return sessionClaims{}, fmt.Errorf("invalid session token signature")
	}
	payloadBytes, err := decodeSegment(parts[1])
	if err != nil {
		return sessionClaims{}, fmt.Errorf("malformed session token payload")
	}
	var payload tokenPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return sessionClaims{}, fmt.Errorf("malformed session token payload")
	}
	now := time.Now()
	if payload.Issuer != s.issuer || payload.Audience != s.audience {
		return sessionClaims{}, fmt.Errorf("session token issuer or audience mismatch")
	}
	if payload.Subject == "" || payload.TenantID == "" || payload.SessionID == "" || payload.TokenID == "" {
		return sessionClaims{}, fmt.Errorf("session token is missing required claims")
	}
	expires := time.Unix(payload.ExpiresAt, 0)
	if !expires.After(now) {
		return sessionClaims{}, fmt.Errorf("session token expired")
	}
	issued := time.Unix(payload.IssuedAt, 0)
	if issued.After(now.Add(time.Minute)) {
		return sessionClaims{}, fmt.Errorf("session token issued in the future")
	}
	return sessionClaims{Subject: payload.Subject, TenantID: payload.TenantID, SessionID: payload.SessionID, TokenID: payload.TokenID, ExpiresAt: expires, IssuedAt: issued}, nil
}

func (s *tokenSigner) sign(kid, signingInput string) []byte {
	return signWithKey(s.keys[kid], signingInput)
}

func signWithKey(key []byte, signingInput string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

func encodeSegment(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeSegment(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}
