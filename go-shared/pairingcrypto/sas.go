package pairingcrypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"

	"golang.org/x/crypto/hkdf"
)

// DeriveSAS computes the 6-digit verification code.
//
// Canonical input (byte-for-byte; both mobile and desktop must match):
//
//	"niuniu-pair/v1" ||
//	uint16_be(len(nonce))     || nonce ||
//	uint16_be(len(desktop_x)) || desktop_x ||
//	uint16_be(len(mobile_x))  || mobile_x ||
//	uint16_be(len(desktop_s)) || desktop_s ||
//	uint16_be(len(mobile_s))  || mobile_s
//
// The result is HKDF-SHA256 with salt=nil, info="niuniu-sas-v1", length=32.
// The first 4 bytes (big-endian) are reduced mod 1,000,000 to produce 6 digits.
func DeriveSAS(pairingNonce, desktopXpub, mobileXpub, desktopSignPub, mobileSignPub []byte) string {
	var input []byte
	input = append(input, []byte("niuniu-pair/v1")...)
	input = appendLenPrefixed(input, pairingNonce)
	input = appendLenPrefixed(input, desktopXpub)
	input = appendLenPrefixed(input, mobileXpub)
	input = appendLenPrefixed(input, desktopSignPub)
	input = appendLenPrefixed(input, mobileSignPub)

	h := hkdf.New(sha256.New, input, nil, []byte("niuniu-sas-v1"))
	out := make([]byte, 32)
	_, _ = io.ReadFull(h, out)
	code := binary.BigEndian.Uint32(out[0:4]) % 1_000_000
	return fmt.Sprintf("%06d", code)
}

func appendLenPrefixed(dst, field []byte) []byte {
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(field)))
	dst = append(dst, lb[:]...)
	dst = append(dst, field...)
	return dst
}

// ThreeChoicePicker returns the correct code plus two distinct random
// 6-digit decoys in a cryptographically random order, plus the index of
// the correct one so the UI knows which choice is right.
func ThreeChoicePicker(correct string) ([]string, int, error) {
	if len(correct) != 6 {
		return nil, 0, errors.New("pairingcrypto: correct must be 6 digits")
	}
	choices := []string{correct}
	for len(choices) < 3 {
		r, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return nil, 0, err
		}
		decoy := fmt.Sprintf("%06d", r.Int64())
		dup := false
		for _, c := range choices {
			if c == decoy {
				dup = true
				break
			}
		}
		if !dup {
			choices = append(choices, decoy)
		}
	}
	for i := len(choices) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, 0, err
		}
		choices[i], choices[j.Int64()] = choices[j.Int64()], choices[i]
	}
	correctIdx := -1
	for i, c := range choices {
		if c == correct {
			correctIdx = i
			break
		}
	}
	return choices, correctIdx, nil
}
