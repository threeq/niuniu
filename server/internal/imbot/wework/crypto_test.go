package wework

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

// testAESKey is a fixed 43-char EncodingAESKey. "AAAA...=" (44 chars) is the
// base64 of 32 zero bytes; the 43-char WeCom form drops the trailing "=".
const testAESKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestAESKeyFromEncoding_Decodes32Bytes(t *testing.T) {
	key, err := aesKeyFromEncoding(testAESKey)
	if err != nil {
		t.Fatalf("aesKeyFromEncoding: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}
}

func TestAESKeyFromEncoding_RejectsWrongLength(t *testing.T) {
	if _, err := aesKeyFromEncoding("tooshort"); err == nil {
		t.Fatal("expected error for a non-43-char key")
	}
}

// TestCryptoRoundTrip is the REQUIRED round-trip: encrypt a known plaintext then
// decrypt it back and assert equality (plus receiveid).
func TestCryptoRoundTrip(t *testing.T) {
	key, err := aesKeyFromEncoding(testAESKey)
	if err != nil {
		t.Fatalf("aesKeyFromEncoding: %v", err)
	}
	const receiveID = "ww1234567890abcdef"
	plaintext := []byte(`<xml><ToUserName>corp</ToUserName><FromUserName>lucy</FromUserName><MsgType>text</MsgType><Content>你好牛牛</Content><MsgId>42</MsgId></xml>`)

	ciphertext, err := encryptMsg(key, plaintext, receiveID)
	if err != nil {
		t.Fatalf("encryptMsg: %v", err)
	}

	gotMsg, gotReceive, err := decryptMsg(key, ciphertext)
	if err != nil {
		t.Fatalf("decryptMsg: %v", err)
	}
	if string(gotMsg) != string(plaintext) {
		t.Errorf("round-trip msg = %q, want %q", gotMsg, plaintext)
	}
	if string(gotReceive) != receiveID {
		t.Errorf("round-trip receiveid = %q, want %q", gotReceive, receiveID)
	}
}

// TestMsgSignature_MatchAndTamper asserts a matching signature verifies and a
// tampered one fails.
func TestMsgSignature_MatchAndTamper(t *testing.T) {
	const (
		token     = "verifyToken"
		timestamp = "1700000000"
		nonce     = "abc123"
		encrypt   = "someBase64Ciphertext=="
	)
	sig := msgSignature(token, timestamp, nonce, encrypt)

	if !verifySignature(token, timestamp, nonce, encrypt, sig) {
		t.Fatal("verifySignature rejected the correct signature")
	}
	if verifySignature(token, timestamp, nonce, encrypt, sig+"00") {
		t.Error("verifySignature accepted a tampered signature")
	}
	// A different encrypt value must not verify against the original signature.
	if verifySignature(token, timestamp, nonce, encrypt+"x", sig) {
		t.Error("verifySignature accepted signature over different ciphertext")
	}
}

func TestDecryptMsg_RejectsGarbage(t *testing.T) {
	key, err := aesKeyFromEncoding(testAESKey)
	if err != nil {
		t.Fatalf("aesKeyFromEncoding: %v", err)
	}
	if _, _, err := decryptMsg(key, "!!!not base64!!!"); err == nil {
		t.Error("expected error decrypting non-base64 input")
	}
	if _, _, err := decryptMsg(key, "aGVsbG8="); err == nil {
		t.Error("expected error decrypting a non-block-aligned ciphertext")
	}
}

// TestDecryptMsg_RejectsOversizedMsgLen encrypts a well-formed block whose
// declared msg_len is 0xFFFFFFFF and asserts decryptMsg returns a clean error
// rather than panicking on an out-of-range slice — the guard that must stay
// overflow-safe on 32-bit builds.
func TestDecryptMsg_RejectsOversizedMsgLen(t *testing.T) {
	key, err := aesKeyFromEncoding(testAESKey)
	if err != nil {
		t.Fatalf("aesKeyFromEncoding: %v", err)
	}
	plain := make([]byte, 32) // 16 random + 4-byte len + slack
	binary.BigEndian.PutUint32(plain[16:20], 0xFFFFFFFF)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:block.BlockSize()]).CryptBlocks(ct, padded)
	enc := base64.StdEncoding.EncodeToString(ct)
	if _, _, err := decryptMsg(key, enc); err == nil {
		t.Fatal("expected error (not panic) for oversized declared msg_len")
	}
}
