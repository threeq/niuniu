package relayclient

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDesktopSummary_SurvivesRelayShape locks the JSON contract that
// 502'd the desktop's mobile-access settings page once: relay's
// store.Desktop ships `last_seen_at` as a `sql.NullTime` value, which
// JSON-marshals as `{"Time":"...","Valid":true}` — an object, not a
// string. The earlier DesktopSummary version had `LastSeenAt string`
// which made json.Unmarshal blow up with
// `cannot unmarshal object into Go struct field DesktopSummary.last_seen_at of type string`
// for every desktop the user had on the relay, taking the whole list
// endpoint with it.
//
// The fix is to drop the field rather than declare it as a string. This
// test reproduces the exact wire shape the relay emits and asserts the
// decode succeeds — pinning the regression so a future "let's add
// last_seen_at as a string" attempt has to also ship the matching type
// shape on both sides.
func TestDesktopSummary_SurvivesRelayShape(t *testing.T) {
	// One desktop with last_seen_at present (Valid:true), one with it
	// null (Valid:false), plus the unrelated extra fields the relay
	// includes that DesktopSummary doesn't care about.
	wire := []byte(`[
		{
			"id": "d-1",
			"account_id": "acc-1",
			"xpub": null,
			"sign_pub": null,
			"name": "MacBook",
			"os": "darwin",
			"arch": "arm64",
			"version": "1.2.3",
			"last_seen_at": {"Time": "2026-05-02T10:00:00Z", "Valid": true},
			"created_at": "2026-04-01T12:00:00Z",
			"revoked_at": {"Time": "0001-01-01T00:00:00Z", "Valid": false},
			"last_protocol_version": 1
		},
		{
			"id": "d-2",
			"account_id": "acc-1",
			"xpub": null,
			"sign_pub": null,
			"name": "ThinkPad",
			"os": "linux",
			"arch": "amd64",
			"version": "1.0.0",
			"last_seen_at": {"Time": "0001-01-01T00:00:00Z", "Valid": false},
			"created_at": "2026-04-15T08:30:00Z",
			"revoked_at": {"Time": "0001-01-01T00:00:00Z", "Valid": false},
			"last_protocol_version": 1
		}
	]`)
	var got []DesktopSummary
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("decode relay-shape desktops: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 desktops, got %d", len(got))
	}
	if got[0].ID != "d-1" || got[0].Name != "MacBook" || got[0].OS != "darwin" || got[0].Arch != "arm64" {
		t.Fatalf("desktop 0 fields wrong: %+v", got[0])
	}
	if got[0].CreatedAt != "2026-04-01T12:00:00Z" {
		t.Fatalf("created_at not preserved: %q", got[0].CreatedAt)
	}
	if got[1].ID != "d-2" || got[1].Name != "ThinkPad" {
		t.Fatalf("desktop 1 fields wrong: %+v", got[1])
	}
}

// TestDesktopSummary_NoLastSeenAtField makes the "don't add last_seen_at
// as a bare string" intent enforceable. If a future change adds it back
// without also handling the sql.NullTime object shape, this test fails
// loudly at unit-test time instead of silently 502'ing in production.
func TestDesktopSummary_NoLastSeenAtField(t *testing.T) {
	d := DesktopSummary{}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "last_seen_at") {
		t.Fatalf("DesktopSummary serialised with last_seen_at — relay's sql.NullTime object will not fit a string field. See comment on the type. Got: %s", out)
	}
}
