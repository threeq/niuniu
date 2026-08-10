// Noise KK Go↔JS interop fixtures.
//
// TestGenerateNoiseKKVectors writes testdata/noise_kk_vectors.json with a
// deterministic Noise_KK_25519_ChaChaPoly_SHA256 transcript: both sides'
// static keys and ephemeral privs are fixed, msg1 / msg2 and a post-handshake
// ciphertext frame are captured. The mobile's JS implementation loads this
// fixture and asserts byte-for-byte equivalence — the same protocol-compliance
// check (MixHash(prologue), pre-message order, AEAD AD tagging, etc.) that a
// Go↔JS round-trip on live traffic would exercise.
//
// The fixture is versioned in testdata/ rather than re-generated on every run
// so (a) the mobile's CI doesn't depend on a Go toolchain, and (b) any protocol
// deviation produces a diffable mismatch against a frozen reference.
//
// Regenerate with:
//
//	GEN_NOISE_VECTORS=1 go test ./go-shared/pairingcrypto/ -run TestGenerateNoiseKKVectors
package pairingcrypto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/flynn/noise"
)

type noiseKKVector struct {
	// Static keypairs (priv is used to seed x25519; pub is X25519(priv, base)).
	InitiatorStaticPrivHex string `json:"initiator_static_priv_hex"`
	InitiatorStaticPubHex  string `json:"initiator_static_pub_hex"`
	ResponderStaticPrivHex string `json:"responder_static_priv_hex"`
	ResponderStaticPubHex  string `json:"responder_static_pub_hex"`

	// Ephemeral privs pinned for determinism. flynn/noise draws the ephemeral
	// by reading 32 bytes from its rng; we hand it a bytes.Reader seeded with
	// these values so the whole transcript is reproducible.
	InitiatorEphemeralPrivHex string `json:"initiator_ephemeral_priv_hex"`
	InitiatorEphemeralPubHex  string `json:"initiator_ephemeral_pub_hex"`
	ResponderEphemeralPrivHex string `json:"responder_ephemeral_priv_hex"`
	ResponderEphemeralPubHex  string `json:"responder_ephemeral_pub_hex"`

	// Handshake payloads (may be empty).
	Payload1Hex string `json:"payload1_hex"`
	Payload2Hex string `json:"payload2_hex"`

	// Wire bytes produced by flynn/noise. JS must reproduce these exactly.
	Msg1Hex string `json:"msg1_hex"`
	Msg2Hex string `json:"msg2_hex"`

	// One post-handshake frame in each direction so the JS test can verify the
	// derived CipherState keys + nonce indexing match. Each uses AD=nil.
	InitiatorToResponderPlainHex string `json:"initiator_to_responder_plain_hex"`
	InitiatorToResponderCipherHex string `json:"initiator_to_responder_cipher_hex"`
	ResponderToInitiatorPlainHex string `json:"responder_to_initiator_plain_hex"`
	ResponderToInitiatorCipherHex string `json:"responder_to_initiator_cipher_hex"`
}

// runKK drives one complete KK handshake deterministically and returns a
// populated vector. Both ephemeral privs are 32-byte buffers used as the rng
// input to flynn/noise's DH25519.GenerateKeypair (which reads exactly 32 bytes).
func runKK(initStaticPriv, respStaticPriv, initEph, respEph, payload1, payload2, frame1, frame2 []byte) (noiseKKVector, error) {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

	// Derive static pubs from privs via DH25519 (so the fixture records canonical pubs).
	initStaticKP, err := cs.GenerateKeypair(bytes.NewReader(initStaticPriv))
	if err != nil {
		return noiseKKVector{}, err
	}
	respStaticKP, err := cs.GenerateKeypair(bytes.NewReader(respStaticPriv))
	if err != nil {
		return noiseKKVector{}, err
	}

	// Pre-compute ephemeral pubs for the fixture (flynn/noise consumes the same
	// bytes from its rng below, so these must match).
	initEphKP, err := cs.GenerateKeypair(bytes.NewReader(initEph))
	if err != nil {
		return noiseKKVector{}, err
	}
	respEphKP, err := cs.GenerateKeypair(bytes.NewReader(respEph))
	if err != nil {
		return noiseKKVector{}, err
	}

	initHS, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cs,
		Pattern:       noise.HandshakeKK,
		Initiator:     true,
		StaticKeypair: initStaticKP,
		PeerStatic:    respStaticKP.Public,
		Random:        bytes.NewReader(initEph),
	})
	if err != nil {
		return noiseKKVector{}, err
	}
	respHS, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cs,
		Pattern:       noise.HandshakeKK,
		Initiator:     false,
		StaticKeypair: respStaticKP,
		PeerStatic:    initStaticKP.Public,
		Random:        bytes.NewReader(respEph),
	})
	if err != nil {
		return noiseKKVector{}, err
	}

	msg1, _, _, err := initHS.WriteMessage(nil, payload1)
	if err != nil {
		return noiseKKVector{}, err
	}
	gotPayload1, _, _, err := respHS.ReadMessage(nil, msg1)
	if err != nil {
		return noiseKKVector{}, err
	}
	if !bytes.Equal(gotPayload1, payload1) {
		return noiseKKVector{}, io.ErrUnexpectedEOF // responder payload mismatch; should never happen
	}

	// Per flynn/noise (see its NN/XX tests): Split()'s first return is the
	// initiator→responder direction cipher (c1), the second is responder→initiator (c2).
	// Therefore:
	//   responder WriteMessage completes → (out, c1=responder.recv, c2=responder.send)
	//   initiator ReadMessage completes  → (payload, c1=initiator.send, c2=initiator.recv)
	msg2, respRecv, respSend, err := respHS.WriteMessage(nil, payload2)
	if err != nil {
		return noiseKKVector{}, err
	}
	gotPayload2, initSend, initRecv, err := initHS.ReadMessage(nil, msg2)
	if err != nil {
		return noiseKKVector{}, err
	}
	if !bytes.Equal(gotPayload2, payload2) {
		return noiseKKVector{}, io.ErrUnexpectedEOF
	}

	// Frame 1: initiator → responder.
	ctOut, err := initSend.Encrypt(nil, nil, frame1)
	if err != nil {
		return noiseKKVector{}, err
	}
	if got, derr := respRecv.Decrypt(nil, nil, ctOut); derr != nil || !bytes.Equal(got, frame1) {
		return noiseKKVector{}, io.ErrUnexpectedEOF
	}

	// Frame 2: responder → initiator.
	ctBack, err := respSend.Encrypt(nil, nil, frame2)
	if err != nil {
		return noiseKKVector{}, err
	}
	if got, derr := initRecv.Decrypt(nil, nil, ctBack); derr != nil || !bytes.Equal(got, frame2) {
		return noiseKKVector{}, io.ErrUnexpectedEOF
	}

	return noiseKKVector{
		InitiatorStaticPrivHex:        hex.EncodeToString(initStaticPriv),
		InitiatorStaticPubHex:         hex.EncodeToString(initStaticKP.Public),
		ResponderStaticPrivHex:        hex.EncodeToString(respStaticPriv),
		ResponderStaticPubHex:         hex.EncodeToString(respStaticKP.Public),
		InitiatorEphemeralPrivHex:     hex.EncodeToString(initEph),
		InitiatorEphemeralPubHex:      hex.EncodeToString(initEphKP.Public),
		ResponderEphemeralPrivHex:     hex.EncodeToString(respEph),
		ResponderEphemeralPubHex:      hex.EncodeToString(respEphKP.Public),
		Payload1Hex:                   hex.EncodeToString(payload1),
		Payload2Hex:                   hex.EncodeToString(payload2),
		Msg1Hex:                       hex.EncodeToString(msg1),
		Msg2Hex:                       hex.EncodeToString(msg2),
		InitiatorToResponderPlainHex:  hex.EncodeToString(frame1),
		InitiatorToResponderCipherHex: hex.EncodeToString(ctOut),
		ResponderToInitiatorPlainHex:  hex.EncodeToString(frame2),
		ResponderToInitiatorCipherHex: hex.EncodeToString(ctBack),
	}, nil
}

// TestGenerateNoiseKKVectors writes testdata/noise_kk_vectors.json when
// GEN_NOISE_VECTORS=1. The mobile's JS test reads the fixture verbatim.
func TestGenerateNoiseKKVectors(t *testing.T) {
	if os.Getenv("GEN_NOISE_VECTORS") != "1" {
		t.Skip("set GEN_NOISE_VECTORS=1 to regenerate")
	}

	type inp struct {
		initStaticPriv, respStaticPriv, initEph, respEph string
		payload1, payload2, frame1, frame2               string
	}
	inputs := []inp{
		{
			initStaticPriv: "1111111111111111111111111111111111111111111111111111111111111111",
			respStaticPriv: "2222222222222222222222222222222222222222222222222222222222222222",
			initEph:        "3333333333333333333333333333333333333333333333333333333333333333",
			respEph:        "4444444444444444444444444444444444444444444444444444444444444444",
			payload1:       "",
			payload2:       "",
			frame1:         "",
			frame2:         "",
		},
		{
			initStaticPriv: "a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5",
			respStaticPriv: "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a",
			initEph:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			respEph:        "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
			payload1:       "68656c6c6f",                   // "hello"
			payload2:       "776f726c64",                   // "world"
			frame1:         "66726f6d20696e697469617465",   // "from initiate"
			frame2:         "66726f6d20726573706f6e646572", // "from responder"
		},
		{
			initStaticPriv: "0101010101010101010101010101010101010101010101010101010101010101",
			respStaticPriv: "0202020202020202020202020202020202020202020202020202020202020202",
			initEph:        "0303030303030303030303030303030303030303030303030303030303030303",
			respEph:        "0404040404040404040404040404040404040404040404040404040404040404",
			payload1:       "f00dfeed",
			payload2:       "",
			frame1:         "0011223344",
			frame2:         "aabbccdd",
		},
	}

	vectors := make([]noiseKKVector, 0, len(inputs))
	for i, in := range inputs {
		sp, _ := hex.DecodeString(in.initStaticPriv)
		rp, _ := hex.DecodeString(in.respStaticPriv)
		ie, _ := hex.DecodeString(in.initEph)
		re, _ := hex.DecodeString(in.respEph)
		p1, _ := hex.DecodeString(in.payload1)
		p2, _ := hex.DecodeString(in.payload2)
		f1, _ := hex.DecodeString(in.frame1)
		f2, _ := hex.DecodeString(in.frame2)

		v, err := runKK(sp, rp, ie, re, p1, p2, f1, f2)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		vectors = append(vectors, v)
	}

	b, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile("testdata/noise_kk_vectors.json", b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %d KK vectors", len(vectors))
}

// TestVerifyNoiseKKVectors reads the fixture back and replays the handshake on
// the Go side, ensuring the committed file is still reproducible. This catches
// (a) fixture drift from a flynn/noise upgrade and (b) any hex corruption in
// the JSON before the mobile test ever runs.
func TestVerifyNoiseKKVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/noise_kk_vectors.json")
	if err != nil {
		t.Skipf("testdata/noise_kk_vectors.json not present: %v", err)
	}
	var vectors []noiseKKVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors in fixture")
	}
	for i, v := range vectors {
		sp, _ := hex.DecodeString(v.InitiatorStaticPrivHex)
		rp, _ := hex.DecodeString(v.ResponderStaticPrivHex)
		ie, _ := hex.DecodeString(v.InitiatorEphemeralPrivHex)
		re, _ := hex.DecodeString(v.ResponderEphemeralPrivHex)
		p1, _ := hex.DecodeString(v.Payload1Hex)
		p2, _ := hex.DecodeString(v.Payload2Hex)
		f1, _ := hex.DecodeString(v.InitiatorToResponderPlainHex)
		f2, _ := hex.DecodeString(v.ResponderToInitiatorPlainHex)

		got, err := runKK(sp, rp, ie, re, p1, p2, f1, f2)
		if err != nil {
			t.Fatalf("vector %d: replay: %v", i, err)
		}
		if got.Msg1Hex != v.Msg1Hex {
			t.Errorf("vector %d: msg1 drift\n  got  %s\n  want %s", i, got.Msg1Hex, v.Msg1Hex)
		}
		if got.Msg2Hex != v.Msg2Hex {
			t.Errorf("vector %d: msg2 drift\n  got  %s\n  want %s", i, got.Msg2Hex, v.Msg2Hex)
		}
		if got.InitiatorToResponderCipherHex != v.InitiatorToResponderCipherHex {
			t.Errorf("vector %d: frame1 drift", i)
		}
		if got.ResponderToInitiatorCipherHex != v.ResponderToInitiatorCipherHex {
			t.Errorf("vector %d: frame2 drift", i)
		}
	}
}
