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

const (
	legacyKeyID      = "legacy"
	envelopeV1Prefix = "v1:"
	envelopeV2Prefix = "v2:"
)

// Cipher encrypts connector credentials before they reach PostgreSQL. It is
// deliberately a small envelope rather than a KMS client: the key source can be
// replaced by a managed KMS later without changing the stored format, because
// the version and key id are part of the value.
//
// New values are always encrypted with the active key as
// "v2:<kid>:<base64(nonce||sealed)>". Decryption accepts any registered key and
// still reads the historical "v1:" envelope, which was always sealed with the
// legacy key.
type Cipher struct {
	active string
	aeads  map[string]cipher.AEAD
}

// NewCipher keeps the single-key contract for callers and tests: the key is
// registered as kid "legacy" and becomes the active key.
func NewCipher(encoded string) (*Cipher, error) {
	return NewKeyring("", encoded, legacyKeyID)
}

// NewKeyring builds a rotation-capable cipher. entriesCSV is a comma-separated
// list of "kid:key" pairs; legacyKey is registered as kid "legacy"; activeKeyID
// selects the encryption key and defaults to "legacy" when entriesCSV is empty.
// Every registered key stays usable for decryption, so a rotation only needs the
// new key added and ACTIVE_KID switched.
func NewKeyring(entriesCSV, legacyKey, activeKeyID string) (*Cipher, error) {
	aeads := make(map[string]cipher.AEAD)
	register := func(keyID, encoded string) error {
		keyID = strings.TrimSpace(keyID)
		if keyID == "" {
			return fmt.Errorf("connector encryption key id must not be empty")
		}
		if _, exists := aeads[keyID]; exists {
			return fmt.Errorf("connector encryption key id %q is duplicated", keyID)
		}
		aead, err := newAEAD(encoded)
		if err != nil {
			return fmt.Errorf("connector encryption key %q: %w", keyID, err)
		}
		aeads[keyID] = aead
		return nil
	}
	for _, entry := range strings.Split(entriesCSV, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		separator := strings.IndexByte(entry, ':')
		if separator <= 0 || separator == len(entry)-1 {
			return nil, fmt.Errorf("CONNECTOR_ENCRYPTION_KEYS entry %q must be kid:key", entry)
		}
		if err := register(entry[:separator], entry[separator+1:]); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(legacyKey) != "" {
		if err := register(legacyKeyID, legacyKey); err != nil {
			return nil, err
		}
	}
	if len(aeads) == 0 {
		return nil, fmt.Errorf("CONNECTOR_ENCRYPTION_KEY or CONNECTOR_ENCRYPTION_KEYS is required")
	}
	active := strings.TrimSpace(activeKeyID)
	if active == "" {
		active = legacyKeyID
	}
	if _, ok := aeads[active]; !ok {
		return nil, fmt.Errorf("CONNECTOR_ENCRYPTION_ACTIVE_KID %q is not present in the configured keys", active)
	}
	return &Cipher{active: active, aeads: aeads}, nil
}

func newAEAD(encoded string) (cipher.AEAD, error) {
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
	return aead, nil
}

func decodeKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("connector encryption key is required")
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
	return nil, fmt.Errorf("connector encryption key must be a 32-byte key encoded as base64 or hex")
}

func (c *Cipher) activeAEAD() (cipher.AEAD, error) {
	if c == nil || len(c.aeads) == 0 {
		return nil, fmt.Errorf("connector credential cipher is not configured")
	}
	aead, ok := c.aeads[c.active]
	if !ok {
		return nil, fmt.Errorf("connector credential cipher is not configured")
	}
	return aead, nil
}

func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	aead, err := c.activeAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate credential nonce: %w", err)
	}
	aad := envelopeV2Prefix + c.active
	sealed := aead.Seal(nil, nonce, plaintext, []byte(aad))
	return envelopeV2Prefix + c.active + ":" + base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (c *Cipher) Decrypt(envelope string) ([]byte, error) {
	if c == nil || len(c.aeads) == 0 {
		return nil, fmt.Errorf("connector credential cipher is not configured")
	}
	switch {
	case strings.HasPrefix(envelope, envelopeV1Prefix):
		aead, ok := c.aeads[legacyKeyID]
		if !ok {
			return nil, fmt.Errorf("no connector credential key is configured for v1 envelopes")
		}
		return openEnvelope(aead, envelopeV1Prefix, strings.TrimPrefix(envelope, envelopeV1Prefix))
	case strings.HasPrefix(envelope, envelopeV2Prefix):
		payload := strings.TrimPrefix(envelope, envelopeV2Prefix)
		separator := strings.IndexByte(payload, ':')
		if separator <= 0 || separator == len(payload)-1 {
			return nil, fmt.Errorf("malformed connector credential envelope")
		}
		keyID := payload[:separator]
		aead, ok := c.aeads[keyID]
		if !ok {
			return nil, fmt.Errorf("connector credential envelope references unknown key id %q", keyID)
		}
		return openEnvelope(aead, envelopeV2Prefix+keyID, payload[separator+1:])
	default:
		return nil, fmt.Errorf("unsupported connector credential envelope")
	}
}

func openEnvelope(aead cipher.AEAD, aad, encoded string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode connector credential envelope: %w", err)
	}
	nonceSize := aead.NonceSize()
	if len(raw) <= nonceSize {
		return nil, fmt.Errorf("connector credential envelope is truncated")
	}
	plaintext, err := aead.Open(nil, raw[:nonceSize], raw[nonceSize:], []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("decrypt connector credential: %w", err)
	}
	return plaintext, nil
}
