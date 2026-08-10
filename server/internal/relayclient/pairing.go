package relayclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// errorEnvelope is the structured shape the relay returns on errors:
// `{"error":"<code>","code":"<code>","detail":"..."}` (some handlers emit only
// `error`; others also set `code`). Callers compare `Code`/`Error` exactly when
// they need to branch on a specific reason, and use readErrorBody for display.
type errorEnvelope struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// readErrorEnvelope decodes the response body once into a structured envelope
// *and* returns a human-friendly summary. The body is consumed; callers must
// not re-read resp.Body afterwards.
func readErrorEnvelope(resp *http.Response) (errorEnvelope, string) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var env errorEnvelope
	if json.Unmarshal(body, &env) == nil {
		switch {
		case env.Error != "" && env.Detail != "":
			return env, env.Error + ": " + env.Detail
		case env.Error != "":
			return env, env.Error
		case env.Detail != "":
			return env, env.Detail
		}
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 {
		return env, string(trimmed)
	}
	return env, ""
}

// readErrorBody is the display-only variant retained for callers that don't
// need the structured envelope.
func readErrorBody(resp *http.Response) string {
	_, msg := readErrorEnvelope(resp)
	return msg
}

// PairingSession is returned by CreatePairingSession.
type PairingSession struct {
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PairingSessionStatus is returned by PollPairingSession.
type PairingSessionStatus struct {
	ID                      string `json:"id"`
	Status                  string `json:"status"`
	ClaimedMobileXpubB64    string `json:"claimed_mobile_xpub_b64"`
	ClaimedMobileSignPubB64 string `json:"claimed_mobile_sign_pub_b64"`
	ClaimedMobileName       string `json:"claimed_mobile_name"`
	ClaimedMobilePlatform   string `json:"claimed_mobile_platform"`
}

// ConfirmResult is returned by ConfirmPairingSession.
type ConfirmResult struct {
	Confirmed bool   `json:"confirmed"`
	MobileID  string `json:"mobile_id"`
}

// CreatePairingSession calls POST /api/pairing/sessions with the given desktop_id.
// Returns ErrQuotaExceeded on HTTP 402 {"error":"quota_exceeded"} so callers
// can branch to a "revoke mobile / upgrade plan" UX.
func (c *Client) CreatePairingSession(desktopID string) (*PairingSession, error) {
	body, _ := json.Marshal(map[string]string{"desktop_id": desktopID})
	req, _ := http.NewRequest("POST", c.cfg.RelayURL+"/api/pairing/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		env, detail := readErrorEnvelope(resp)
		if resp.StatusCode == http.StatusPaymentRequired &&
			(env.Code == "quota_exceeded" || env.Error == "quota_exceeded") {
			if detail != "" {
				return nil, fmt.Errorf("%w: %s", ErrQuotaExceeded, detail)
			}
			return nil, ErrQuotaExceeded
		}
		if detail != "" {
			return nil, fmt.Errorf("create_pairing_session: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("create_pairing_session: status %d", resp.StatusCode)
	}
	var out PairingSession
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PollPairingSession calls GET /api/pairing/sessions/:id and returns current status.
func (c *Client) PollPairingSession(sessionID string) (*PairingSessionStatus, error) {
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/pairing/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("poll_pairing_session: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("poll_pairing_session: status %d", resp.StatusCode)
	}
	var out PairingSessionStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConfirmPairingSession calls POST /api/pairing/sessions/:id/confirm.
func (c *Client) ConfirmPairingSession(sessionID string) (*ConfirmResult, error) {
	req, _ := http.NewRequest("POST", c.cfg.RelayURL+"/api/pairing/sessions/"+sessionID+"/confirm", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return nil, fmt.Errorf("confirm_pairing_session: status %d (%s)", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("confirm_pairing_session: status %d", resp.StatusCode)
	}
	var out ConfirmResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RejectPairingSession calls POST /api/pairing/sessions/:id/reject.
func (c *Client) RejectPairingSession(sessionID string) error {
	req, _ := http.NewRequest("POST", c.cfg.RelayURL+"/api/pairing/sessions/"+sessionID+"/reject", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if detail := readErrorBody(resp); detail != "" {
			return fmt.Errorf("reject_pairing_session: status %d (%s)", resp.StatusCode, detail)
		}
		return fmt.Errorf("reject_pairing_session: status %d", resp.StatusCode)
	}
	return nil
}
