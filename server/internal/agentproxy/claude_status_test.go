package agentproxy

import (
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
)

// TestRateWindowOrNilIfStale pins the staleness contract for the usage pill's
// 5-hour reset: a captured rate_limit_event whose reset has already passed must
// not be surfaced (it would show a stale, past time that no longer matches the
// CLI), while a still-future or time-less observation is kept verbatim.
func TestRateWindowOrNilIfStale(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name    string
		rl      *event.RateLimitData
		wantNil bool
	}{
		{"nil observation", nil, true},
		{"empty status", &event.RateLimitData{Status: ""}, true},
		{"future reset kept", &event.RateLimitData{Status: "allowed_warning", ResetsAt: now.Unix() + 3600}, false},
		{"past reset dropped", &event.RateLimitData{Status: "allowed_warning", ResetsAt: now.Unix() - 1}, true},
		{"exact-now reset kept", &event.RateLimitData{Status: "rejected", ResetsAt: now.Unix()}, false},
		{"zero reset kept", &event.RateLimitData{Status: "allowed_warning", ResetsAt: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rateWindowOrNilIfStale(tc.rl, now)
			if (got == nil) != tc.wantNil {
				t.Fatalf("rateWindowOrNilIfStale() nil=%v, want nil=%v", got == nil, tc.wantNil)
			}
			if got != nil && (got.Status != tc.rl.Status || got.ResetsAt != tc.rl.ResetsAt || got.Overage != tc.rl.Overage) {
				t.Fatalf("window mismatch: got %+v, src %+v", got, tc.rl)
			}
		})
	}
}
