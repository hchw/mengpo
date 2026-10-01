// Package providercrypto provides reversible, authenticated encryption for
// per-tenant provider secrets. The master key is injected from the
// orchestrator's secret store; it is never persisted and never logged. A
// missing master key is a hard error: the service must never silently fall back
// to storing plaintext.
package providercrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// KeyVersion identifies the current envelope format.
	KeyVersion    = 1
	masterKeySize = 32 // AES-256
)

var (
	ErrMissingMasterKey   = errors.New("provider secret master key is not configured")
	ErrInvalidMasterKey   = errors.New("provider secret master key must decode to 32 bytes")
	ErrCiphertextTooShort = errors.New("provider secret ciphertext is truncated")
)

// Sealer encrypts and decrypts provider secrets with AES-256-GCM.
type Sealer struct {
	aead       cipher.AEAD
	keyVersion int
}

// New builds a Sealer from the configured master key. The key may be plain
// 32-byte text, standard base64, or hex.
func New(masterKey string) (*Sealer, error) {
	key, err := decodeMasterKey(masterKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build provider cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build provider GCM: %w", err)
	}
	return &Sealer{aead: aead, keyVersion: KeyVersion}, nil
}

// KeyVersion returns the envelope format version stored alongside ciphertext.
func (s *Sealer) KeyVersion() int { return s.keyVersion }

// Seal encrypts plaintext, returning nonce||ciphertext. The result is
// authenticated: Open fails on any tampering.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	if s == nil || s.aead == nil {
		return nil, ErrMissingMasterKey
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(ciphertext []byte) ([]byte, error) {
	if s == nil || s.aead == nil {
		return nil, ErrMissingMasterKey
	}
	nonceSize := s.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrCiphertextTooShort
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt provider secret: %w", err)
	}
	return plaintext, nil
}

func decodeMasterKey(masterKey string) ([]byte, error) {
	trimmed := strings.TrimSpace(masterKey)
	if trimmed == "" {
		return nil, ErrMissingMasterKey
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) == masterKeySize {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil && len(decoded) == masterKeySize {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == masterKeySize {
		return decoded, nil
	}
	if len(trimmed) == masterKeySize {
		return []byte(trimmed), nil
	}
	return nil, ErrInvalidMasterKey
}
