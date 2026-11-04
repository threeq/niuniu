package server

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
)

// Recovery needs no action, so it must not be pushed to the owner's IM chats;
// an actionable alert on an owner-scoped source still is. A global provider
// (no owner) has no chat to push to either way.
func TestIntegrationAlertPushesToIM(t *testing.T) {
	cases := []struct {
		name  string
		alert service.IntegrationHealthAlert
		want  bool
	}{
		{
			name:  "auto-disabled user source",
			alert: service.IntegrationHealthAlert{OwnerType: "user", OwnerID: 1, Kind: service.AlertAutoDisabled},
			want:  true,
		},
		{
			name:  "drift on org source",
			alert: service.IntegrationHealthAlert{OwnerType: "org", OwnerID: 7, Kind: service.AlertDrift},
			want:  true,
		},
		{
			name:  "recovery stays station-in",
			alert: service.IntegrationHealthAlert{OwnerType: "user", OwnerID: 1, Kind: service.AlertRecovered},
			want:  false,
		},
		{
			name:  "global provider has no owner",
			alert: service.IntegrationHealthAlert{Kind: service.AlertAutoDisabled},
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := integrationAlertPushesToIM(c.alert); got != c.want {
				t.Fatalf("integrationAlertPushesToIM(%+v) = %v, want %v", c.alert, got, c.want)
			}
		})
	}
}
