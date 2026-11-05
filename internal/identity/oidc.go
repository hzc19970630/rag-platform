package identity

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OIDCClient is a discovery-based OpenID Connect client. It deliberately
// supports the standard authorization-code flow with PKCE and RS256 ID tokens,
// so any compliant provider works without a provider-specific adapter.
type OIDCClient struct {
	cfg    OIDCOptions
	client *http.Client

	mu        sync.Mutex
	discovery *oidcDiscovery
	keys      map[string]*rsa.PublicKey
}

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type OIDCIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	TenantID      string
}

type oidcTokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

type jwksDocument struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func NewOIDCClient(cfg OIDCOptions) *OIDCClient {
	return &OIDCClient{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, keys: map[string]*rsa.PublicKey{}}
}

func (c *OIDCClient) Enabled() bool { return c != nil && c.cfg.Enabled }

func (c *OIDCClient) AuthorizationURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	discovery, err := c.loadDiscovery(ctx)
	if err != nil {
		return "", err
	}
	challenge := sha256.Sum256([]byte(verifier))
	values := url.Values{}
	values.Set("client_id", c.cfg.ClientID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", c.cfg.RedirectURL)
	values.Set("scope", strings.Join(c.cfg.Scopes, " "))
	values.Set("state", state)
	values.Set("nonce", nonce)
	values.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	values.Set("code_challenge_method", "S256")
	separator := "?"
	if strings.Contains(discovery.AuthorizationEndpoint, "?") {
		separator = "&"
	}
	return discovery.AuthorizationEndpoint + separator + values.Encode(), nil
}

func (c *OIDCClient) Exchange(ctx context.Context, code, verifier, nonce string) (OIDCIdentity, error) {
	discovery, err := c.loadDiscovery(ctx)
	if err != nil {
		return OIDCIdentity{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.cfg.RedirectURL)
	form.Set("client_id", c.cfg.ClientID)
	form.Set("code_verifier", verifier)
	if c.cfg.ClientSecret != "" {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return OIDCIdentity{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.client.Do(request)
	if err != nil {
		return OIDCIdentity{}, fmt.Errorf("oidc token exchange: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return OIDCIdentity{}, fmt.Errorf("read oidc token response: %w", err)
	}
	var token oidcTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return OIDCIdentity{}, fmt.Errorf("decode oidc token response: %w", err)
	}
	if response.StatusCode != http.StatusOK || token.Error != "" {
		return OIDCIdentity{}, fmt.Errorf("oidc token exchange failed: %s %s", token.Error, token.Description)
	}
	if token.IDToken == "" {
		return OIDCIdentity{}, fmt.Errorf("oidc token response did not include an id_token")
	}
	return c.verifyIDToken(ctx, token.IDToken, nonce)
}

func (c *OIDCClient) verifyIDToken(ctx context.Context, raw, expectedNonce string) (OIDCIdentity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return OIDCIdentity{}, fmt.Errorf("malformed id_token")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return OIDCIdentity{}, fmt.Errorf("malformed id_token header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return OIDCIdentity{}, fmt.Errorf("malformed id_token header")
	}
	if header.Alg != "RS256" {
		return OIDCIdentity{}, fmt.Errorf("unsupported id_token algorithm %q", header.Alg)
	}
	key, err := c.signingKey(ctx, header.Kid)
	if err != nil {
		return OIDCIdentity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return OIDCIdentity{}, fmt.Errorf("malformed id_token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return OIDCIdentity{}, fmt.Errorf("invalid id_token signature")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return OIDCIdentity{}, fmt.Errorf("malformed id_token payload")
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return OIDCIdentity{}, fmt.Errorf("malformed id_token payload")
	}
	issuer, _ := claims["iss"].(string)
	if strings.TrimSuffix(issuer, "/") != strings.TrimSuffix(c.cfg.Issuer, "/") {
		return OIDCIdentity{}, fmt.Errorf("id_token issuer mismatch")
	}
	if !audienceContains(claims["aud"], c.cfg.ClientID) {
		return OIDCIdentity{}, fmt.Errorf("id_token audience mismatch")
	}
	if expires, ok := numericClaim(claims["exp"]); !ok || time.Now().Unix() >= expires {
		return OIDCIdentity{}, fmt.Errorf("id_token expired")
	}
	if expectedNonce != "" {
		nonce, _ := claims["nonce"].(string)
		if nonce != expectedNonce {
			return OIDCIdentity{}, fmt.Errorf("id_token nonce mismatch")
		}
	}
	subject, _ := claims["sub"].(string)
	if strings.TrimSpace(subject) == "" {
		return OIDCIdentity{}, fmt.Errorf("id_token is missing sub")
	}
	identity := OIDCIdentity{
		Issuer:   strings.TrimSuffix(issuer, "/"),
		Subject:  subject,
		Email:    stringClaim(claims, c.cfg.EmailClaim),
		Name:     stringClaim(claims, c.cfg.NameClaim),
		TenantID: stringClaim(claims, c.cfg.TenantClaim),
	}
	if identity.Email == "" {
		identity.Email = stringClaim(claims, "email")
	}
	if identity.Name == "" {
		identity.Name = stringClaim(claims, "name")
	}
	switch value := claims["email_verified"].(type) {
	case bool:
		identity.EmailVerified = value
	case string:
		identity.EmailVerified = strings.EqualFold(value, "true")
	}
	return identity, nil
}

func (c *OIDCClient) loadDiscovery(ctx context.Context) (oidcDiscovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discovery != nil {
		return *c.discovery, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.cfg.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return oidcDiscovery{}, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("oidc discovery: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("oidc discovery returned status %d", response.StatusCode)
	}
	var discovery oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&discovery); err != nil {
		return oidcDiscovery{}, fmt.Errorf("decode oidc discovery: %w", err)
	}
	if discovery.AuthorizationEndpoint == "" || discovery.TokenEndpoint == "" || discovery.JWKSURI == "" {
		return oidcDiscovery{}, fmt.Errorf("oidc discovery document is incomplete")
	}
	if discovery.Issuer != "" && strings.TrimSuffix(discovery.Issuer, "/") != strings.TrimSuffix(c.cfg.Issuer, "/") {
		return oidcDiscovery{}, fmt.Errorf("oidc discovery issuer mismatch")
	}
	c.discovery = &discovery
	return discovery, nil
}

func (c *OIDCClient) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	key, ok := c.keys[kid]
	c.mu.Unlock()
	if ok {
		return key, nil
	}
	if err := c.refreshKeys(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok = c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("id_token signing key %q not found", kid)
	}
	return key, nil
}

func (c *OIDCClient) refreshKeys(ctx context.Context) error {
	discovery, err := c.loadDiscovery(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.JWKSURI, nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("oidc jwks: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc jwks returned status %d", response.StatusCode)
	}
	var document jwksDocument
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return fmt.Errorf("decode oidc jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, item := range document.Keys {
		if item.Kty != "RSA" || item.N == "" || item.E == "" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(item.N)
		if err != nil {
			continue
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(item.E)
		if err != nil {
			continue
		}
		exponent := 0
		for _, value := range exponentBytes {
			exponent = exponent<<8 + int(value)
		}
		if exponent == 0 {
			continue
		}
		keys[item.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponent}
	}
	if len(keys) == 0 {
		return fmt.Errorf("oidc jwks contains no RSA signing keys")
	}
	c.mu.Lock()
	c.keys = keys
	c.mu.Unlock()
	return nil
}

func audienceContains(value any, expected string) bool {
	switch current := value.(type) {
	case string:
		return current == expected
	case []any:
		for _, item := range current {
			if audience, ok := item.(string); ok && audience == expected {
				return true
			}
		}
	}
	return false
}

func stringClaim(claims map[string]any, name string) string {
	if name == "" {
		return ""
	}
	value, _ := claims[name].(string)
	return strings.TrimSpace(value)
}

func numericClaim(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return int64(number), true
}
