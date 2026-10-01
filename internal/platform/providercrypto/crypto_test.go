package providercrypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := New(key)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	plaintext := []byte("sk-live-1234567890")
	ciphertext, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}
	decrypted, err := sealer.Open(ciphertext)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round trip = %q, want %q", decrypted, plaintext)
	}
	if sealer.KeyVersion() != KeyVersion {
		t.Fatalf("KeyVersion() = %d", sealer.KeyVersion())
	}
}

func TestMissingMasterKeyIsHardError(t *testing.T) {
	if _, err := New("  "); !errors.Is(err, ErrMissingMasterKey) {
		t.Fatalf("New(\"\") error = %v, want ErrMissingMasterKey", err)
	}
	if _, err := New("too-short"); !errors.Is(err, ErrInvalidMasterKey) {
		t.Fatalf("New(short) error = %v, want ErrInvalidMasterKey", err)
	}
}

func TestOpenDetectsTampering(t *testing.T) {
	sealer, _ := New(hex.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	ciphertext, err := sealer.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff
	if _, err := sealer.Open(ciphertext); err == nil {
		t.Fatal("expected tampered ciphertext to fail authentication")
	}
	if _, err := sealer.Open([]byte{1, 2}); !errors.Is(err, ErrCiphertextTooShort) {
		t.Fatalf("short ciphertext error = %v, want ErrCiphertextTooShort", err)
	}
}

func TestDifferentKeysDoNotInteroperate(t *testing.T) {
	first, _ := New(hex.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	second, _ := New(hex.EncodeToString(bytes.Repeat([]byte{3}, 32)))
	ciphertext, _ := first.Seal([]byte("secret"))
	if _, err := second.Open(ciphertext); err == nil {
		t.Fatal("expected decryption with a different key to fail")
	}
}
