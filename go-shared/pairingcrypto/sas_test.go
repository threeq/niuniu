package pairingcrypto

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestSASDerivesDeterministic(t *testing.T) {
	nonce, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	dxpub, _ := hex.DecodeString("0102030405060708091011121314151617181920212223242526272829303132")
	mxpub, _ := hex.DecodeString("aaaabbbbccccddddeeeeffff00001111222233334444555566667777888899aa")
	dspub, _ := hex.DecodeString("33445566778899aabbccddeeff00112233445566778899aabbccddeeff001122")
	mspub, _ := hex.DecodeString("99887766554433221100ffeeddccbbaa99887766554433221100ffeeddccbbaa")
	got1 := DeriveSAS(nonce, dxpub, mxpub, dspub, mspub)
	got2 := DeriveSAS(nonce, dxpub, mxpub, dspub, mspub)
	if got1 != got2 {
		t.Fatal("SAS must be deterministic")
	}
	if len(got1) != 6 {
		t.Fatalf("SAS len: %d", len(got1))
	}
	swapped := DeriveSAS(nonce, mxpub, dxpub, dspub, mspub)
	if swapped == got1 {
		t.Fatal("SAS should differ after swapping pubs")
	}
}

func TestSASVectorsFile(t *testing.T) {
	raw, err := os.ReadFile("testdata/sas_vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors []struct {
		Nonce       string `json:"nonce"`
		DesktopX    string `json:"desktop_x"`
		MobileX     string `json:"mobile_x"`
		DesktopSign string `json:"desktop_sign"`
		MobileSign  string `json:"mobile_sign"`
		Expected    string `json:"expected"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	if len(vectors) < 5 {
		t.Fatalf("expect >= 5 vectors; got %d", len(vectors))
	}
	for i, v := range vectors {
		n, _ := hex.DecodeString(v.Nonce)
		dx, _ := hex.DecodeString(v.DesktopX)
		mx, _ := hex.DecodeString(v.MobileX)
		ds, _ := hex.DecodeString(v.DesktopSign)
		ms, _ := hex.DecodeString(v.MobileSign)
		got := DeriveSAS(n, dx, mx, ds, ms)
		if got != v.Expected {
			t.Errorf("vector %d: got %s want %s", i, got, v.Expected)
		}
	}
}

func TestThreeChoicePicker(t *testing.T) {
	nonce, _ := hex.DecodeString("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	correct := DeriveSAS(nonce, []byte{1}, []byte{2}, []byte{3}, []byte{4})
	choices, correctIdx, err := ThreeChoicePicker(correct)
	if err != nil {
		t.Fatalf("ThreeChoicePicker: %v", err)
	}
	if len(choices) != 3 {
		t.Fatalf("want 3 choices, got %d", len(choices))
	}
	if choices[correctIdx] != correct {
		t.Fatalf("correctIdx=%d choices[correctIdx]=%q correct=%q", correctIdx, choices[correctIdx], correct)
	}
	seen := map[string]bool{}
	for _, c := range choices {
		if seen[c] {
			t.Fatalf("duplicate choice: %s", c)
		}
		seen[c] = true
	}
}
