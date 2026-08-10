package relayproto

import (
	"encoding/json"
	"testing"
)

func TestHelloRoundTrip(t *testing.T) {
	h := Hello{
		DesktopID:          "abc-123",
		Version:            1,
		SupportedVersions:  []uint16{1},
		DesktopSignPubHex:  "deadbeef",
		ClientCapabilities: []string{"yamux/1"},
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Hello
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DesktopID != h.DesktopID || got.Version != h.Version {
		t.Fatalf("mismatch: %+v", got)
	}
}
