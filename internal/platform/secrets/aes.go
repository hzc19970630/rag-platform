package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const envelopePrefix = "v1:"

// Cipher encrypts connector credentials before they reach PostgreSQL. It is
// deliberately a small envelope rather than a KMS client: the key source can be
// replaced by a managed KMS later without changing the stored format, because
// the version prefix is part of the value.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher accepts a 32-byte AES-256 key encoded as standard/raw base64 or
// hex. An empty key is a configuration error: silently falling back to a
// generated key would make previously stored credentials undecryptable.
func NewCipher(encoded string) (*Cipher, error) {
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create connector credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create connector credential gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func decodeKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("CONNECTOR_ENCRYPTION_KEY is required")
	}
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(encoded); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(encoded); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, fmt.Errorf("CONNECTOR_ENCRYPTION_KEY must be a 32-byte key encoded as base64 or hex")
}

func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	if c == nil || c.aead == nil {
		return "", fmt.Errorf("connector credential cipher is not configured")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate credential nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, []byte(envelopePrefix))
	return envelopePrefix + base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (c *Cipher) Decrypt(envelope string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, fmt.Errorf("connector credential cipher is not configured")
	}
	if !strings.HasPrefix(envelope, envelopePrefix) {
		return nil, fmt.Errorf("unsupported connector credential envelope")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(envelope, envelopePrefix))
	if err != nil {
		return nil, fmt.Errorf("decode connector credential envelope: %w", err)
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) <= nonceSize {
		return nil, fmt.Errorf("connector credential envelope is truncated")
	}
	plaintext, err := c.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], []byte(envelopePrefix))
	if err != nil {
		return nil, fmt.Errorf("decrypt connector credential: %w", err)
	}
	return plaintext, nil
}
