// Package crypto holds the AES-GCM keyring for encrypting integration
// credentials at rest. The 32-byte master key lives in
// ~/.niuniu/integration_secret (mode 0600). On startup the server calls
// LoadOrCreate; if the file does not exist a fresh random key is
// generated, otherwise the existing key is loaded. Boot fails if the
// file is unreadable — we never silently fall back to plaintext.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

// keyVersion is the first byte of every ciphertext blob. It lets us
// rotate the master key in v1.1 by stamping ciphertexts with the key
// id that produced them. v1 always writes 0x01.
const keyVersion byte = 0x01

// Keyring holds the current AES-GCM key. Future versions will hold a
// map of versioned keys; v1 only carries one.
type Keyring struct {
	current []byte // 32 bytes
}

// LoadOrCreate reads the secret file or generates one. File mode 0600
// (Unix only; Windows permission semantics differ and we don't assert).
func LoadOrCreate(path string) (*Keyring, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) < 32 {
			return nil, fmt.Errorf("integration_secret too short: %d bytes", len(data))
		}
		return &Keyring{current: data[:32]}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("write secret: %w", err)
	}
	return &Keyring{current: key}, nil
}

// Encrypt seals plaintext with AES-GCM and returns
// base64(keyVersion | nonce | ciphertext | tag).
func (k *Keyring) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.current)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, 1+len(nonce)+len(sealed))
	out = append(out, keyVersion)
	out = append(out, nonce...)
	out = append(out, sealed...)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(out)))
	base64.StdEncoding.Encode(encoded, out)
	return encoded, nil
}

// Decrypt reverses Encrypt. Errors on key-version mismatch so an
// operator can detect ciphertext written with a foreign / future key.
func (k *Keyring) Decrypt(b64 []byte) ([]byte, error) {
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(b64)))
	n, err := base64.StdEncoding.Decode(raw, b64)
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	raw = raw[:n]
	if len(raw) < 1 {
		return nil, errors.New("ciphertext too short")
	}
	if raw[0] != keyVersion {
		return nil, fmt.Errorf("unknown key version: %#x", raw[0])
	}
	block, err := aes.NewCipher(k.current)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < 1+gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce := raw[1 : 1+gcm.NonceSize()]
	sealed := raw[1+gcm.NonceSize():]
	return gcm.Open(nil, nonce, sealed, nil)
}
