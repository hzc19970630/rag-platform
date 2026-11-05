package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func testKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func secondTestKey() string {
	return base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	cipher, err := NewCipher(testKey())
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	envelope, err := cipher.Encrypt([]byte(`{"bearerToken":"secret"}`))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(envelope, "secret") {
		t.Fatal("envelope must not contain the plaintext credential")
	}
	if !strings.HasPrefix(envelope, "v2:legacy:") {
		t.Fatalf("unexpected envelope prefix: %q", envelope)
	}
	plaintext, err := cipher.Decrypt(envelope)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(plaintext) != `{"bearerToken":"secret"}` {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}
}

func TestEncryptProducesDistinctCiphertexts(t *testing.T) {
	cipher, _ := NewCipher(testKey())
	first, _ := cipher.Encrypt([]byte("same"))
	second, _ := cipher.Encrypt([]byte("same"))
	if first == second {
		t.Fatal("a random nonce must produce distinct ciphertexts")
	}
}

func TestDecryptRejectsTamperingAndWrongKey(t *testing.T) {
	cipher, _ := NewCipher(testKey())
	envelope, _ := cipher.Encrypt([]byte("value"))
	tampered := envelope[:len(envelope)-2] + "AA"
	if _, err := cipher.Decrypt(tampered); err == nil {
		t.Fatal("expected tampered ciphertext to fail")
	}
	other, _ := NewCipher(secondTestKey())
	if _, err := other.Decrypt(envelope); err == nil {
		t.Fatal("expected a different key to fail")
	}
}

func TestNewCipherRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "short", base64.StdEncoding.EncodeToString([]byte("too-short"))} {
		if _, err := NewCipher(key); err == nil {
			t.Fatalf("expected key %q to be rejected", key)
		}
	}
}

// sealV1 reproduces the pre-keyring envelope so the compatibility path is
// tested against the historical format rather than a reimplementation of the
// current one.
func sealV1(t *testing.T, encodedKey, plaintext string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		t.Fatalf("decode test key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("create test gcm: %v", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("generate nonce: %v", err)
	}
	sealed := aead.Seal(nil, nonce, []byte(plaintext), []byte("v1:"))
	return "v1:" + base64.RawStdEncoding.EncodeToString(append(nonce, sealed...))
}

func TestLegacyV1EnvelopeStillDecrypts(t *testing.T) {
	rotated, err := NewKeyring("k2:"+secondTestKey(), testKey(), "k2")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	plaintext, err := rotated.Decrypt(sealV1(t, testKey(), "legacy-secret"))
	if err != nil {
		t.Fatalf("decrypt v1 envelope: %v", err)
	}
	if string(plaintext) != "legacy-secret" {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}

	// Without the legacy key a v1 envelope must fail, not silently fall back.
	keyringOnly, err := NewKeyring("k2:"+secondTestKey(), "", "k2")
	if err != nil {
		t.Fatalf("NewKeyring without legacy key: %v", err)
	}
	if _, err := keyringOnly.Decrypt(sealV1(t, testKey(), "legacy-secret")); err == nil {
		t.Fatal("a v1 envelope must not decrypt without the legacy key")
	}
}

func TestKeyringRotatesEncryptionAndDecryptsOldKeys(t *testing.T) {
	entries := "k1:" + testKey() + ",k2:" + secondTestKey()
	rotated, err := NewKeyring(entries, "", "k2")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	fresh, err := rotated.Encrypt([]byte("new-secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(fresh, "v2:k2:") {
		t.Fatalf("active key must encrypt new envelopes, got %q", fresh)
	}
	if plaintext, err := rotated.Decrypt(fresh); err != nil || string(plaintext) != "new-secret" {
		t.Fatalf("decrypt fresh envelope: %q %v", plaintext, err)
	}

	before, err := NewKeyring(entries, "", "k1")
	if err != nil {
		t.Fatalf("NewKeyring k1: %v", err)
	}
	oldEnvelope, err := before.Encrypt([]byte("old-secret"))
	if err != nil {
		t.Fatalf("Encrypt with k1: %v", err)
	}
	if !strings.HasPrefix(oldEnvelope, "v2:k1:") {
		t.Fatalf("k1 envelope prefix: %q", oldEnvelope)
	}
	if plaintext, err := rotated.Decrypt(oldEnvelope); err != nil || string(plaintext) != "old-secret" {
		t.Fatalf("rotated cipher must decrypt k1 envelopes: %q %v", plaintext, err)
	}

	unknown := "v2:ghost:" + strings.TrimPrefix(oldEnvelope, "v2:k1:")
	if _, err := rotated.Decrypt(unknown); err == nil {
		t.Fatal("an unknown key id must be rejected")
	}
}

func TestNewKeyringRejectsInvalidConfigurations(t *testing.T) {
	cases := map[string]struct{ entries, legacy, active string }{
		"duplicate kid":     {entries: "k1:" + testKey() + ",k1:" + secondTestKey(), active: "k1"},
		"empty kid":         {entries: ":" + testKey(), active: "k1"},
		"missing separator": {entries: testKey(), active: "k1"},
		"invalid key":       {entries: "k1:short", active: "k1"},
		"unknown active":    {entries: "k1:" + testKey(), active: "k9"},
		"no keys at all":    {},
		"legacy duplicate":  {entries: "legacy:" + secondTestKey(), legacy: testKey(), active: "legacy"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewKeyring(testCase.entries, testCase.legacy, testCase.active); err == nil {
				t.Fatalf("expected configuration to be rejected: %+v", testCase)
			}
		})
	}
}
