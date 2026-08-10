package wework

// This file implements the 企业微信 (WeCom) callback message crypto — the
// standard "加解密方案" WeCom uses for its self-built-app event callbacks. It is
// self-contained (no import of internal/integration/): the only inputs are the
// EncodingAESKey and verification Token from the credential.
//
// Layout of a decrypted message block (after AES-256-CBC + PKCS#7 unpad):
//
//	[16 random bytes][4-byte big-endian msg_len][msg bytes][receiveid bytes]
//
// where msg is the plaintext callback XML and receiveid is the corp_id. The
// msg_signature that authenticates a callback is
// sha1(sort_and_concat(token, timestamp, nonce, encrypt)) hex-encoded.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// aesKeyFromEncoding decodes a 43-char EncodingAESKey into the 32-byte AES key.
// WeCom drops the trailing "=" base64 padding, so we append it before decoding.
// The IV is the first 16 bytes of the key (WeCom's fixed convention).
func aesKeyFromEncoding(encodingAESKey string) ([]byte, error) {
	encodingAESKey = strings.TrimSpace(encodingAESKey)
	if len(encodingAESKey) != 43 {
		return nil, fmt.Errorf("wework: EncodingAESKey must be 43 chars, got %d", len(encodingAESKey))
	}
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return nil, fmt.Errorf("wework: decode EncodingAESKey: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("wework: EncodingAESKey must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

// msgSignature computes the WeCom callback signature:
// sha1 over the ascending-sorted concatenation of token, timestamp, nonce and
// the base64 ciphertext, hex-encoded lowercase.
func msgSignature(token, timestamp, nonce, encrypt string) string {
	parts := []string{token, timestamp, nonce, encrypt}
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(h.Sum(nil))
}

// verifySignature reports whether the provided msg_signature matches the one
// computed over (token, timestamp, nonce, encrypt).
func verifySignature(token, timestamp, nonce, encrypt, provided string) bool {
	want := msgSignature(token, timestamp, nonce, encrypt)
	// Length-independent compare is fine here — the signature is a fixed-length
	// hex string and not a low-entropy secret; a simple equality is sufficient
	// and matches WeCom's own reference implementation.
	return strings.EqualFold(want, strings.TrimSpace(provided))
}

// pkcs7Pad appends PKCS#7 padding to a multiple of blockSize.
func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	if pad == 0 {
		pad = blockSize
	}
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

// pkcs7Unpad strips PKCS#7 padding, validating the pad length.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	n := len(data)
	if n == 0 || n%blockSize != 0 {
		return nil, fmt.Errorf("wework: bad padded length %d", n)
	}
	pad := int(data[n-1])
	if pad < 1 || pad > blockSize || pad > n {
		return nil, fmt.Errorf("wework: bad padding size %d", pad)
	}
	return data[:n-pad], nil
}

// decryptMsg base64-decodes ciphertext, AES-256-CBC decrypts it, strips padding,
// and unpacks the [random16][be32 len][msg][receiveid] block, returning msg and
// receiveid. It does not itself enforce receiveid==corp_id (the caller may).
func decryptMsg(aesKey []byte, ciphertext string) (msg, receiveID []byte, err error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ciphertext))
	if err != nil {
		return nil, nil, fmt.Errorf("wework: base64 decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 || len(raw)%block.BlockSize() != 0 {
		return nil, nil, fmt.Errorf("wework: ciphertext not block-aligned (len=%d)", len(raw))
	}
	iv := aesKey[:block.BlockSize()]
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, raw)
	plain, err = pkcs7Unpad(plain, block.BlockSize())
	if err != nil {
		return nil, nil, err
	}
	// Layout: 16 random bytes | 4-byte BE length | msg | receiveid.
	if len(plain) < 20 {
		return nil, nil, fmt.Errorf("wework: decrypted block too short (len=%d)", len(plain))
	}
	// Compare against len(plain)-20 (always >=0 here) rather than 20+msgLen so a
	// near-MaxUint32 msgLen cannot overflow int on a 32-bit build and slip past
	// the guard into an out-of-bounds slice.
	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if msgLen < 0 || msgLen > len(plain)-20 {
		return nil, nil, fmt.Errorf("wework: declared msg_len %d exceeds block", msgLen)
	}
	msg = plain[20 : 20+msgLen]
	receiveID = plain[20+msgLen:]
	return msg, receiveID, nil
}

// encryptMsg is the inverse of decryptMsg: it packs [random16][be32 len][msg]
// [receiveid], PKCS#7-pads, AES-256-CBC encrypts, and base64-encodes. It is
// needed both to answer the URL-verification echo (WeCom expects the plaintext
// back — but the round-trippable form is also used to build valid test fixtures)
// and, more importantly, to unit-test the crypto with a real encrypt→decrypt
// round trip. The 16 random bytes come from crypto/rand.
func encryptMsg(aesKey, msg []byte, receiveID string) (string, error) {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	random16 := make([]byte, 16)
	if _, err := rand.Read(random16); err != nil {
		return "", fmt.Errorf("wework: read random iv seed: %w", err)
	}
	var buf bytes.Buffer
	buf.Write(random16)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(msg)))
	buf.Write(lenBuf[:])
	buf.Write(msg)
	buf.WriteString(receiveID)

	padded := pkcs7Pad(buf.Bytes(), block.BlockSize())
	iv := aesKey[:block.BlockSize()]
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}
