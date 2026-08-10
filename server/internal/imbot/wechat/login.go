package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// QR-scan login for a WeChat personal-account bot. The bot_token that the rest
// of the adapter needs is not something a user can paste — it is minted by
// scanning a QR code with the WeChat app. These package functions wrap the two
// iLink login endpoints so the service layer can drive the flow (show QR ->
// poll -> obtain credential) without embedding protocol details.
//
// Login always talks to the fixed public host; the confirmed response carries
// the per-bot base_url that subsequent API calls should use.

const (
	// loginBaseURL is the fixed iLink host for the QR login handshake.
	loginBaseURL = "https://ilinkai.weixin.qq.com"
	// botTypeClaw is the iLink bot_type for this personal-bot build.
	botTypeClaw = "3"
	// qrPollTimeout is the client-side cap on one get_qrcode_status long-poll.
	qrPollTimeout = 40 * time.Second
)

// QRCode is the result of starting a login: an opaque qrcode handle used for
// polling, plus the URL to render as a scannable QR image for the user.
type QRCode struct {
	// Code is the opaque qrcode token passed back to PollQRStatus.
	Code string `json:"qrcode"`
	// ImageContent is the URL to encode into a QR image the user scans.
	ImageContent string `json:"qrcode_img_content"`
}

// QRStatus is one poll result. Status is one of: wait, scaned, confirmed,
// expired, need_verifycode, verify_code_blocked, binded_redirect,
// scaned_but_redirect. On "confirmed" the credential fields are populated.
type QRStatus struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	IlinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	IlinkUserID string `json:"ilink_user_id"`
}

// loginClient is a dedicated client whose timeout accommodates the long-poll.
var loginClient = &http.Client{Timeout: qrPollTimeout + 10*time.Second}

// StartQRLogin requests a fresh login QR code from the iLink server. The
// returned QRCode.ImageContent is a URL to render as a scannable code and
// QRCode.Code is the handle for PollQRStatus.
func StartQRLogin(ctx context.Context) (QRCode, error) {
	endpoint := loginBaseURL + "/ilink/bot/get_bot_qrcode?bot_type=" + botTypeClaw
	body, _ := json.Marshal(map[string]any{"local_token_list": []string{}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return QRCode{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WECHAT-UIN", randomWechatUin())
	req.Header.Set("iLink-App-ClientVersion", "1")
	resp, err := loginClient.Do(req)
	if err != nil {
		return QRCode{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return QRCode{}, fmt.Errorf("wechat: get_bot_qrcode status=%d body=%s", resp.StatusCode, truncate(string(raw), 200))
	}
	var qr QRCode
	if err := json.Unmarshal(raw, &qr); err != nil {
		return QRCode{}, fmt.Errorf("wechat: decode get_bot_qrcode: %w", err)
	}
	if qr.Code == "" {
		return QRCode{}, fmt.Errorf("wechat: get_bot_qrcode returned empty qrcode")
	}
	return qr, nil
}

// PollQRStatus long-polls the status of a login QR. verifyCode is optional (the
// numeric pairing code the user reads off the WeChat app when the server
// returns need_verifycode). A client-side timeout is reported as {wait} so the
// caller simply polls again.
func PollQRStatus(ctx context.Context, qrcode, verifyCode string) (QRStatus, error) {
	if qrcode == "" {
		return QRStatus{}, fmt.Errorf("wechat: empty qrcode")
	}
	endpoint := loginBaseURL + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	if verifyCode != "" {
		endpoint += "&verify_code=" + url.QueryEscape(verifyCode)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return QRStatus{}, err
	}
	req.Header.Set("iLink-App-ClientVersion", "1")
	resp, err := loginClient.Do(req)
	if err != nil {
		// A gateway/long-poll timeout is normal control flow: keep waiting.
		if ctx.Err() != nil {
			return QRStatus{}, ctx.Err()
		}
		return QRStatus{Status: "wait"}, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return QRStatus{}, fmt.Errorf("wechat: get_qrcode_status status=%d body=%s", resp.StatusCode, truncate(string(raw), 200))
	}
	var st QRStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return QRStatus{}, fmt.Errorf("wechat: decode get_qrcode_status: %w", err)
	}
	return st, nil
}

// CredentialFromStatus builds the imbot credential Config map from a confirmed
// login status. base_url defaults to the fixed host when the server omits one.
func CredentialFromStatus(st QRStatus) map[string]any {
	base := st.BaseURL
	if base == "" {
		base = loginBaseURL
	}
	return map[string]any{
		"token":      st.BotToken,
		"base_url":   base,
		"account_id": st.IlinkBotID,
		"user_id":    st.IlinkUserID,
	}
}
