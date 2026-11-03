package secrets

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
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
	if !strings.HasPrefix(envelope, "v1:") {
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
	other, _ := NewCipher(base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789")))
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
