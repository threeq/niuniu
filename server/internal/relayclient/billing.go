package relayclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// Plan mirrors the relay's GET /api/billing/plans shape. Exposed so the
// desktop UI can render a "Change plan" picker without re-declaring the schema.
type Plan struct {
	PlanID              string `json:"plan_id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	MaxDesktops         int64  `json:"max_desktops"`
	MaxMobiles          int64  `json:"max_mobiles"`
	MaxPairingsPerDay   int64  `json:"max_pairings_per_day"`
	MaxBandwidthMBMonth int64  `json:"max_bandwidth_mb_month"`
	MaxConcurrentTuns   int64  `json:"max_concurrent_tunnels"`
	PriceCentsUSD       int64  `json:"price_cents_usd"`
	BillingInterval     string `json:"billing_interval"`
}

// UsageSummary mirrors the relay's GET /api/billing/my-usage shape.
type UsageSummary struct {
	PlanID             string `json:"plan_id"`
	DesktopsUsed       int64  `json:"desktops_used"`
	DesktopsLimit      int64  `json:"desktops_limit"`
	MobilesUsed        int64  `json:"mobiles_used"`
	MobilesLimit       int64  `json:"mobiles_limit"`
	PairingsToday      int64  `json:"pairings_today"`
	PairingsDailyLimit int64  `json:"pairings_daily_limit"`
	BandwidthMBUsed    int64  `json:"bandwidth_mb_used"`
	BandwidthMBLimit   int64  `json:"bandwidth_mb_limit"`
}

// ChangePlanResult is the decoded response from POST /api/billing/change-plan.
// Exactly one of Applied / CheckoutURL is meaningful per call:
//   - free-plan changes come back with Applied=true and no URL (account updated server-side);
//   - paid-plan changes come back with CheckoutURL set — caller opens it in the system browser.
type ChangePlanResult struct {
	Applied      bool   `json:"applied"`
	CheckoutURL  string `json:"checkout_url"`
	TargetPlanID string `json:"target_plan_id"`
}

// MobileDeviceSummary mirrors the relay's GET /api/mobile-devices response
// used by the "revoke mobile" UI. The relay defines the full shape in
// relay/internal/api/mobile_devices_handler.go:List.
type MobileDeviceSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	CreatedAt string `json:"created_at"`
}

// DesktopSummary mirrors a subset of the relay's GET /api/desktops response —
// just the fields the desktop UI surfaces in the device list. The relay's
// store.Desktop has more (xpub, sign_pub, last_protocol_version, last_seen_at)
// but the UI only needs identifying / display fields. json.Unmarshal silently
// ignores fields the source has and the destination doesn't.
//
// Note: do NOT add `last_seen_at string` here — the relay's row is
// `sql.NullTime` which JSON-marshals as `{"Time":"...","Valid":true}`, an
// object. Trying to land that in a string field causes
// `json: cannot unmarshal object into Go struct field DesktopSummary.last_seen_at of type string`
// and 502s the whole list endpoint. If we ever need the timestamp in the UI,
// add a typed `LastSeenAt sql.NullTime` field (and JSON-decode side handler)
// rather than a bare string.
type DesktopSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Version   string `json:"version"`
	CreatedAt string `json:"created_at"`
}

// ListPlans calls GET /api/billing/plans. Public endpoint, no auth needed,
// but we reuse the authed client for shared timeouts / TLS config.
func (c *Client) ListPlans() ([]Plan, error) {
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/billing/plans", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("list_plans: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("list_plans: status %d", resp.StatusCode)
	}
	var out []Plan
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// MyPlan calls GET /api/billing/my-plan (requires Login).
func (c *Client) MyPlan() (*Plan, error) {
	if c.access == "" {
		return nil, fmt.Errorf("my_plan: not logged in")
	}
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/billing/my-plan", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("my_plan: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("my_plan: status %d", resp.StatusCode)
	}
	var out Plan
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MyUsage calls GET /api/billing/my-usage (requires Login).
func (c *Client) MyUsage() (*UsageSummary, error) {
	if c.access == "" {
		return nil, fmt.Errorf("my_usage: not logged in")
	}
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/billing/my-usage", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("my_usage: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("my_usage: status %d", resp.StatusCode)
	}
	var out UsageSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChangePlan calls POST /api/billing/change-plan. For free plans the relay
// returns 200 with Applied=true; for paid plans it returns 200 with CheckoutURL.
// 503 `billing_not_configured` indicates the relay hasn't been set up with a
// Stripe key — caller should surface that verbatim.
func (c *Client) ChangePlan(targetPlanID string) (*ChangePlanResult, error) {
	if c.access == "" {
		return nil, fmt.Errorf("change_plan: not logged in")
	}
	body, _ := json.Marshal(map[string]string{"target_plan_id": targetPlanID})
	req, _ := http.NewRequest("POST", c.cfg.RelayURL+"/api/billing/change-plan", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("change_plan: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("change_plan: status %d", resp.StatusCode)
	}
	var out ChangePlanResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMobileDevices calls GET /api/mobile-devices — the account-authed list of
// non-revoked mobile_devices rows for the logged-in account. The desktop UI
// uses this to let the user revoke old mobiles (each revoke frees one
// `max_mobiles` slot).
//
// Note: the previous implementation targeted /api/my/paired-desktops, which is
// a mobile-POV endpoint protected by DPoP mobile-token auth — hitting it with
// an account JWT yielded 401 bad_token.
func (c *Client) ListMobileDevices() ([]MobileDeviceSummary, error) {
	if c.access == "" {
		return nil, fmt.Errorf("list_mobile_devices: not logged in")
	}
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/mobile-devices", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("list_mobile_devices: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("list_mobile_devices: status %d", resp.StatusCode)
	}
	var out []MobileDeviceSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeMobileDevice calls DELETE /api/mobile-devices/:id on the relay. A
// successful revoke marks the mobile's `revoked_at` so it stops counting
// against the account's MaxMobiles cap — this is the recommended action when
// the user hits 402 quota_exceeded on pair-session creation because of stale
// debug-time mobile rows.
func (c *Client) RevokeMobileDevice(mobileID string) error {
	if c.access == "" {
		return fmt.Errorf("revoke_mobile_device: not logged in")
	}
	req, _ := http.NewRequest("DELETE", c.cfg.RelayURL+"/api/mobile-devices/"+mobileID, nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		if detail := readErrorBody(resp); detail != "" {
			return fmt.Errorf("revoke_mobile_device: status %d (%s)", resp.StatusCode, detail)
		}
		return fmt.Errorf("revoke_mobile_device: status %d", resp.StatusCode)
	}
	return nil
}

// ListAccountDesktops calls GET /api/desktops — the account-authed list of
// non-revoked desktops_devices rows for the logged-in account. The desktop
// UI surfaces this alongside the mobile list so the user can see and revoke
// stale desktop registrations (each revoke frees one `max_desktops` slot
// and, more importantly, makes "stale desktopId on the mobile points at
// nothing" diagnosable).
func (c *Client) ListAccountDesktops() ([]DesktopSummary, error) {
	if c.access == "" {
		return nil, fmt.Errorf("list_account_desktops: not logged in")
	}
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/desktops", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("list_account_desktops: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("list_account_desktops: status %d", resp.StatusCode)
	}
	// The relay returns []store.Desktop directly, which has more fields
	// (xpub, sign_pub, last_protocol_version, …) than DesktopSummary. JSON
	// unmarshal silently drops unknown fields so we can decode straight
	// into the trimmed shape.
	var out []DesktopSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeAccountDesktop calls DELETE /api/desktops/:id on the relay. Marks
// the desktop's revoked_at; from that point the relay's tunnel router stops
// accepting WebSocket reconnects from that desktop_token, and any mobile
// trying to RPC against that desktop_id gets a 503 DesktopOffline. Use this
// to clean up stale desktops that show up in the device list but no longer
// correspond to a running niuniu-server process.
func (c *Client) RevokeAccountDesktop(desktopID string) error {
	if c.access == "" {
		return fmt.Errorf("revoke_account_desktop: not logged in")
	}
	req, _ := http.NewRequest("DELETE", c.cfg.RelayURL+"/api/desktops/"+desktopID, nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		if detail := readErrorBody(resp); detail != "" {
			return fmt.Errorf("revoke_account_desktop: status %d (%s)", resp.StatusCode, detail)
		}
		return fmt.Errorf("revoke_account_desktop: status %d", resp.StatusCode)
	}
	return nil
}
