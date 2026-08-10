package pairingcrypto

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestGenerateSASVectors(t *testing.T) {
	if os.Getenv("GEN_SAS_VECTORS") != "1" {
		t.Skip("set GEN_SAS_VECTORS=1 to regenerate")
	}
	type vec struct {
		Nonce       string `json:"nonce"`
		DesktopX    string `json:"desktop_x"`
		MobileX     string `json:"mobile_x"`
		DesktopSign string `json:"desktop_sign"`
		MobileSign  string `json:"mobile_sign"`
		Expected    string `json:"expected"`
	}
	// five diverse inputs
	inputs := []struct {
		nonce, dx, mx, ds, ms string
	}{
		{
			"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			"0102030405060708091011121314151617181920212223242526272829303132",
			"aaaabbbbccccddddeeeeffff00001111222233334444555566667777888899aa",
			"33445566778899aabbccddeeff00112233445566778899aabbccddeeff001122",
			"99887766554433221100ffeeddccbbaa99887766554433221100ffeeddccbbaa",
		},
		{
			"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			"0000000000000000000000000000000000000000000000000000000000000001",
			"0000000000000000000000000000000000000000000000000000000000000002",
			"0000000000000000000000000000000000000000000000000000000000000003",
			"0000000000000000000000000000000000000000000000000000000000000004",
		},
		{
			"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
			"f00dfeedf00dfeedf00dfeedf00dfeedf00dfeedf00dfeedf00dfeedf00dfeed",
			"1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321",
		},
		{
			"a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5",
			"5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a",
			"c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3",
			"3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c",
			"7777777777777777777777777777777777777777777777777777777777777777",
		},
		{
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
			"1111111111111111111111111111111111111111111111111111111111111111",
			"2222222222222222222222222222222222222222222222222222222222222222",
			"3333333333333333333333333333333333333333333333333333333333333333",
		},
	}
	var out []vec
	for _, in := range inputs {
		n, _ := hex.DecodeString(in.nonce)
		dx, _ := hex.DecodeString(in.dx)
		mx, _ := hex.DecodeString(in.mx)
		ds, _ := hex.DecodeString(in.ds)
		ms, _ := hex.DecodeString(in.ms)
		exp := DeriveSAS(n, dx, mx, ds, ms)
		out = append(out, vec{in.nonce, in.dx, in.mx, in.ds, in.ms, exp})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile("testdata/sas_vectors.json", b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
