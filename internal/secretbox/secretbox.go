// Package secretbox encrypts channel credentials at rest with AES-256-GCM.
// Ciphertext format: version(1) | nonce(12) | sealed.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// Box seals and opens secrets.
type Box struct{ aead cipher.AEAD }

// ErrCiphertext is returned for tampered or foreign ciphertext.
var ErrCiphertext = errors.New("secretbox: invalid ciphertext")

// New builds a box from a 64-char hex key (32 bytes).
func New(hexKey string) (*Box, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("secretbox: key must be 32 bytes hex (got %d bytes)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	out := make([]byte, 1+b.aead.NonceSize(), 1+b.aead.NonceSize()+len(plain)+b.aead.Overhead())
	out[0] = 1
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return b.aead.Seal(out, out[1:], plain, nil), nil
}

// Open decrypts ciphertext produced by Seal.
func (b *Box) Open(ct []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(ct) < 1+n || ct[0] != 1 {
		return nil, ErrCiphertext
	}
	plain, err := b.aead.Open(nil, ct[1:1+n], ct[1+n:], nil)
	if err != nil {
		return nil, ErrCiphertext
	}
	return plain, nil
}
