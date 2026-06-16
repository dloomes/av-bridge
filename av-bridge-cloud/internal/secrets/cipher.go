package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// Cipher encrypts/decrypts small secrets at rest (e.g. per-collector HMAC keys).
//
// The AES-GCM implementation holds the key in-process, sourced from env, which
// suits self-hosted and dev. The interface is the seam to swap in a KMS-backed
// implementation per deployment (AWS KMS / GCP KMS / Azure Key Vault) without
// touching call sites.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

type aesGCM struct{ gcm cipher.AEAD }

// NewAESGCMFromHexKey builds a Cipher from a 32-byte key encoded as 64 hex chars.
func NewAESGCMFromHexKey(hexKey string) (Cipher, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("secret key must be hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes (64 hex chars), got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &aesGCM{gcm: gcm}, nil
}

func (a *aesGCM) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, a.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// Prepend nonce so Decrypt can recover it.
	return a.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (a *aesGCM) Decrypt(ciphertext []byte) ([]byte, error) {
	ns := a.gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	return a.gcm.Open(nil, nonce, ct, nil)
}
