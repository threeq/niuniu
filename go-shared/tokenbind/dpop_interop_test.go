package tokenbind

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// dpopVector is a single DPoP test vector.
type dpopVector struct {
	EdPrivHex      string `json:"ed_priv_hex"`
	EdPubHex       string `json:"ed_pub_hex"`
	DeviceTokenHex string `json:"device_token_hex"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	Timestamp      int64  `json:"timestamp"`
	Header         string `json:"header"`
}

// TestGenerateDPoPVectors generates dpop_vectors.json when GEN_DPOP_VECTORS=1.
func TestGenerateDPoPVectors(t *testing.T) {
	if os.Getenv("GEN_DPOP_VECTORS") != "1" {
		t.Skip("set GEN_DPOP_VECTORS=1 to regenerate")
	}

	type input struct {
		edPrivSeed     []byte
		deviceTokenHex string
		method         string
		path           string
		ts             int64
	}

	// Fixed seeds for deterministic key generation.
	seedHexes := []string{
		"0101010101010101010101010101010101010101010101010101010101010101",
		"0202020202020202020202020202020202020202020202020202020202020202",
		"0303030303030303030303030303030303030303030303030303030303030303",
		"0404040404040404040404040404040404040404040404040404040404040404",
		"cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
	}
	// Fixed device tokens (hex-encoded plaintext bytes) for deterministic vectors.
	deviceTokenHexes := []string{
		"deadbeef01020304deadbeef01020304deadbeef01020304deadbeef01020304",
		"aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344",
		"0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f",
		"1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
	inputs := []struct {
		method string
		path   string
		ts     int64
	}{
		{"GET", "/api/workspaces", 1700000000},
		{"POST", "/d/desktop-abc123/rpc", 1700000001},
		{"DELETE", "/api/devices/dev-xyz", 1700000002},
		{"PUT", "/api/settings", 1700000003},
		{"GET", "/api/health", 1700000004},
	}

	var vectors []dpopVector
	for i, inp := range inputs {
		seed, _ := hex.DecodeString(seedHexes[i])
		edPriv := ed25519.NewKeyFromSeed(seed)
		edPub := edPriv.Public().(ed25519.PublicKey)
		deviceToken, _ := hex.DecodeString(deviceTokenHexes[i])

		id := &pairingcrypto.Identity{
			EdPriv: edPriv,
			EdPub:  edPub,
		}
		header := SignDPoP(id, deviceToken, inp.method, inp.path, inp.ts)

		// Verify it round-trips.
		if err := VerifyDPoP(edPub, deviceToken, inp.method, inp.path, header, inp.ts, 60); err != nil {
			t.Fatalf("vector %d: VerifyDPoP failed: %v", i, err)
		}

		vectors = append(vectors, dpopVector{
			EdPrivHex:      hex.EncodeToString(edPriv),
			EdPubHex:       hex.EncodeToString(edPub),
			DeviceTokenHex: hex.EncodeToString(deviceToken),
			Method:         inp.method,
			Path:           inp.path,
			Timestamp:      inp.ts,
			Header:         header,
		})
	}

	b, _ := json.MarshalIndent(vectors, "", "  ")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile("testdata/dpop_vectors.json", b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %d vectors to testdata/dpop_vectors.json", len(vectors))
}

// TestVerifyDPoPVectors reads dpop_vectors.json and verifies Go produces identical headers.
func TestVerifyDPoPVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/dpop_vectors.json")
	if err != nil {
		t.Skipf("testdata/dpop_vectors.json not found: %v", err)
	}
	var vectors []dpopVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	for i, v := range vectors {
		edPriv, _ := hex.DecodeString(v.EdPrivHex)
		edPub, _ := hex.DecodeString(v.EdPubHex)
		deviceToken, _ := hex.DecodeString(v.DeviceTokenHex)

		id := &pairingcrypto.Identity{
			EdPriv: edPriv,
			EdPub:  edPub,
		}
		got := SignDPoP(id, deviceToken, v.Method, v.Path, v.Timestamp)
		if got != v.Header {
			t.Errorf("vector %d: header mismatch\n  got  %s\n  want %s", i, got, v.Header)
		}
		// Also verify it passes VerifyDPoP.
		if err := VerifyDPoP(edPub, deviceToken, v.Method, v.Path, v.Header, v.Timestamp, 60); err != nil {
			t.Errorf("vector %d: VerifyDPoP: %v", i, err)
		}
	}
}
