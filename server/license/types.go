package license

// State is the coarse license state surfaced to the UI and the guard.
type State string

const (
	StateActive        State = "active"
	StateExpiring      State = "expiring"       // active but <= ExpiringWindow to expiry
	StateExpired       State = "expired"        // past expiry (incl. trial)
	StateClockTampered State = "clock_tampered" // now < hwm - ClockTolerance
	StateUnlicensed    State = "unlicensed"     // no valid license installed yet
)

// Status is the computed, UI-safe license status (no raw blob).
// (Payload — the signed license body — lives in the enterprise package; the
// open-source core does not need it.)
type Status struct {
	State State `json:"state"`
	// IsTrial is true while the deployment runs on the built-in trial grant
	// (no signed license installed yet). Derived by LicenseService, not signed.
	IsTrial       bool   `json:"is_trial"`
	Customer      string `json:"customer"`
	SeatsUsed     int64  `json:"seats_used"`
	MaxSeats      int64  `json:"max_seats"`
	ExpiresAt     int64  `json:"expires_at"`
	DaysRemaining int64  `json:"days_remaining"`
	ReadOnly      bool   `json:"read_only"`
	// FeaturesEnabled 列出当前许可证启用的门控功能（功能分级）。前端据此隐藏/置灰
	// 未启用的能力（如多租户组织）。开源个人版为空；企业版含 FeatureOrg。
	FeaturesEnabled []string `json:"features_enabled"`
}
