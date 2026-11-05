package identity

import (
	"strings"
	"testing"
)

func TestGenerateAPIKeyHasLookupPrefixAndSecret(t *testing.T) {
	prefix, secret, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey: %v", err)
	}
	if !strings.HasPrefix(prefix, "rk_") || len(prefix) != len("rk_")+16 {
		t.Fatalf("prefix = %q, want rk_ followed by 16 hex characters", prefix)
	}
	if secret == "" {
		t.Fatal("secret must not be empty")
	}
	raw := prefix + "_" + secret
	parsed, ok := apiKeyPrefix(raw)
	if !ok || parsed != prefix {
		t.Fatalf("apiKeyPrefix(%q) = %q, %v; want %q", raw, parsed, ok, prefix)
	}
}

func TestAPIKeyPrefixRejectsUnusableKeys(t *testing.T) {
	for _, raw := range []string{"", "rk_only", "sk_abc_def", "rk__secret", "rk_prefix_", "plain-token"} {
		if _, ok := apiKeyPrefix(raw); ok {
			t.Fatalf("apiKeyPrefix(%q) must reject the key", raw)
		}
	}
}

func TestHashAPIKeyIsPepperedAndStable(t *testing.T) {
	service := &Service{cfg: AuthOptions{APIKeyPepper: "pepper-one"}}
	other := &Service{cfg: AuthOptions{APIKeyPepper: "pepper-two"}}
	raw := "rk_0011223344556677_secret"
	if service.hashAPIKey(raw) != service.hashAPIKey(raw) {
		t.Fatal("the same key must hash identically")
	}
	if service.hashAPIKey(raw) == other.hashAPIKey(raw) {
		t.Fatal("hashes must depend on the configured pepper")
	}
	if strings.Contains(service.hashAPIKey(raw), "secret") {
		t.Fatal("the hash must not contain the secret")
	}
}
