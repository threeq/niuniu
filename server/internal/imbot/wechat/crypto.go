package wechat

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// AES-128-ECB media crypto for the iLink CDN. WeChat CDN objects are encrypted
// with AES-128-ECB + PKCS7 padding; the 16-byte key travels on the message in
// one of a few encodings (see parseAESKey). Go's stdlib has no ECB mode (it is
// intentionally omitted as unsafe for general use), so the block loop is
// implemented here — this is the exact scheme the openclaw-weixin SDK uses, so
// it must match byte-for-byte for downloads to decrypt.

// decryptAESECB decrypts ciphertext with AES-128-ECB and strips PKCS7 padding.
// key must be 16 bytes; ciphertext must be a positive multiple of the block
// size. It returns an error rather than panicking on malformed input so a
// corrupt or truncated CDN download fails the fetch cleanly.
func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wechat: aes key: %w", err)
	}
	bs := block.BlockSize()
	if len(ciphertext) == 0 || len(ciphertext)%bs != 0 {
		return nil, fmt.Errorf("wechat: ciphertext len %d not a multiple of block size %d", len(ciphertext), bs)
	}
	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += bs {
		block.Decrypt(out[i:i+bs], ciphertext[i:i+bs])
	}
	return pkcs7Unpad(out, bs)
}

// pkcs7Unpad removes PKCS7 padding, validating that every pad byte matches the
// pad length (so a wrong key, which yields garbage padding, is rejected).
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	n := len(data)
	if n == 0 {
		return nil, fmt.Errorf("wechat: empty plaintext")
	}
	pad := int(data[n-1])
	if pad == 0 || pad > blockSize || pad > n {
		return nil, fmt.Errorf("wechat: invalid pkcs7 pad length %d", pad)
	}
	for _, b := range data[n-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("wechat: invalid pkcs7 padding")
		}
	}
	return data[:n-pad], nil
}

// parseAESKey decodes the CDNMedia.aes_key JSON field into a raw 16-byte key.
// Two encodings appear in the wild (mirrors the SDK's pic-decrypt parseAesKey):
//   - base64 of the raw 16 key bytes           -> images (media.aes_key)
//   - base64 of a 32-char ASCII hex string      -> file/voice/video
//
// image_item may instead carry the key as a bare hex string in its own aeskey
// field; callers hex-decode that separately before reaching here.
func parseAESKey(aesKeyBase64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("wechat: aes_key not base64: %w", err)
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 && isASCIIHex(decoded) {
		raw, err := hex.DecodeString(string(decoded))
		if err != nil {
			return nil, fmt.Errorf("wechat: aes_key hex decode: %w", err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("wechat: aes_key must decode to 16 raw bytes or 32-char hex, got %d bytes", len(decoded))
}

// isASCIIHex reports whether b is entirely ASCII hex digits.
func isASCIIHex(b []byte) bool {
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'f',
			c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(b) > 0
}

// pkcs7Pad is the inverse of pkcs7Unpad, used only by tests that build fixtures.
// Kept unexported and adjacent so the crypto round-trips in one place.
func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}
