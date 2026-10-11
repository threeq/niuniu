// White-box tests for fingerprintBody — the value-free summary of an outbound
// request body written into external_api_audit (Epic #726 / #728 / #747).
//
// The invariant under test is narrow and load-bearing: the fingerprint must
// describe the SHAPE of what left the machine (hash + size + top-level field
// names) while carrying none of its content.
package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFingerprintBody_ShapeWithoutValues(t *testing.T) {
	body := map[string]any{
		"title":    "quarterly revenue projection",
		"password": "hunter2-do-not-log",
		"nested":   map[string]any{"token": "sk-live-abc123"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	fp := fingerprintBody(raw)

	// The hash must cover exactly the bytes that were sent, so a later replay
	// of the same logical call can be recognized.
	if want := fmt.Sprintf("%x", sha256.Sum256(raw)); fp.sha256 != want {
		t.Errorf("sha256 = %q, want %q", fp.sha256, want)
	}
	if fp.bytes != int64(len(raw)) {
		t.Errorf("bytes = %d, want %d", fp.bytes, len(raw))
	}
	// Field NAMES only, in stable sorted order (so the column is diffable).
	if fp.fields != `["nested","password","title"]` {
		t.Errorf("fields = %s, want %s", fp.fields, `["nested","password","title"]`)
	}

	// No value from the body may survive anywhere in the fingerprint. This is
	// the assertion that keeps a write-audit row from becoming a second copy of
	// the payload.
	for _, secret := range []string{
		"hunter2-do-not-log",
		"quarterly revenue projection",
		"sk-live-abc123",
	} {
		if strings.Contains(fp.sha256, secret) {
			t.Errorf("sha256 leaked value %q", secret)
		}
		if strings.Contains(fp.fields, secret) {
			t.Errorf("fields leaked value %q", secret)
		}
	}
}

func TestFingerprintBody_EmptyBody(t *testing.T) {
	for _, raw := range [][]byte{nil, {}} {
		fp := fingerprintBody(raw)
		if fp.sha256 != "" {
			t.Errorf("empty body: sha256 = %q, want empty", fp.sha256)
		}
		if fp.bytes != 0 {
			t.Errorf("empty body: bytes = %d, want 0", fp.bytes)
		}
		if fp.fields != "[]" {
			t.Errorf("empty body: fields = %q, want []", fp.fields)
		}
	}
}

// A body is always a JSON object in practice (ProxyInput.Body is a map), but a
// non-object must degrade to "no field names" rather than fail the audit write.
func TestFingerprintBody_NonObjectRecordsNoFields(t *testing.T) {
	fp := fingerprintBody([]byte(`["a","b"]`))
	if fp.fields != "[]" {
		t.Errorf("fields = %q, want []", fp.fields)
	}
	if fp.bytes != int64(len(`["a","b"]`)) {
		t.Errorf("bytes = %d, want %d", fp.bytes, len(`["a","b"]`))
	}
	if fp.sha256 == "" {
		t.Error("sha256 should still be computed for a non-object body")
	}
}

// A pathological body (thousands of top-level keys) must not bloat the audit
// row; the list is capped and the cap is visible.
func TestFingerprintBody_FieldListIsCapped(t *testing.T) {
	big := map[string]any{}
	for i := 0; i < maxAuditBodyFields+25; i++ {
		big[fmt.Sprintf("field_%03d", i)] = i
	}
	raw, err := json.Marshal(big)
	if err != nil {
		t.Fatal(err)
	}

	fp := fingerprintBody(raw)

	var got []string
	if err := json.Unmarshal([]byte(fp.fields), &got); err != nil {
		t.Fatalf("fields is not a JSON array: %v (%s)", err, fp.fields)
	}
	if len(got) != maxAuditBodyFields+1 {
		t.Fatalf("len(fields) = %d, want %d", len(got), maxAuditBodyFields+1)
	}
	if got[len(got)-1] != "..." {
		t.Errorf("last field = %q, want the truncation marker %q", got[len(got)-1], "...")
	}
}
