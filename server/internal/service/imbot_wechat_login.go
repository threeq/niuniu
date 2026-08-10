package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot/wechat"
)

// WeChat 微信ClawBot connect flow. Unlike the app-credential channels, a WeChat
// personal bot has no token to paste: the user must scan a QR code with the
// WeChat app. This file drives that handshake on the server (the browser cannot
// call the iLink host directly with the required headers / long-poll), gated by
// the same one-time onboarding token as the credential form. On confirmation the
// minted credential is redeemed through the ordinary SubmitOnboardingCredential
// path, so a WeChat bot lands in exactly the same owner-level channel model.

// wechatLoginSession is one in-flight QR handshake.
type wechatLoginSession struct {
	qrcode    string
	createdAt time.Time
}

// wechatLoginSessionTTL bounds how long a started QR handshake stays pollable.
const wechatLoginSessionTTL = 10 * time.Minute

func onboardingTokenHash(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// ensureWechatOnboarding validates the raw onboarding token and that it targets
// the wechat platform, returning ErrOnboardingTokenInvalid (→ 410) for an
// unknown/expired/used token or ErrInvalidChannelConfig (→ 400) for a non-wechat
// token. Read-only: it does not consume the token.
func (s *IMBotService) ensureWechatOnboarding(ctx context.Context, rawToken string) error {
	platform, _, _, err := s.GetOnboardingTokenInfo(ctx, rawToken)
	if err != nil {
		return err
	}
	if platform != string(wechatPlatform) {
		return fmt.Errorf("%w: onboarding token is not for the wechat platform", ErrInvalidChannelConfig)
	}
	return nil
}

// wechatPlatform is the platform string for a WeChat channel (kept next to the
// login code so the two references stay in lockstep).
const wechatPlatform = "wechat"

// StartWechatLogin begins a QR-scan login for the wechat onboarding token and
// returns the URL to render as a scannable QR image. Calling it again refreshes
// the code (e.g. after expiry), replacing any prior session for the token.
func (s *IMBotService) StartWechatLogin(ctx context.Context, rawToken string) (qrImageContent string, err error) {
	if err := s.ensureWechatOnboarding(ctx, rawToken); err != nil {
		return "", err
	}
	qr, err := wechat.StartQRLogin(ctx)
	if err != nil {
		return "", fmt.Errorf("wechat: start login: %w", err)
	}
	s.wechatSessions.Store(onboardingTokenHash(rawToken), &wechatLoginSession{
		qrcode:    qr.Code,
		createdAt: time.Now(),
	})
	return qr.ImageContent, nil
}

// WechatLoginResult is the outcome of one poll. Status mirrors the iLink status
// values; when Status=="confirmed" the channel has been created and ChannelID is
// set. Status "expired" (or a missing session) means the caller should restart.
type WechatLoginResult struct {
	Status    string
	ChannelID int64
}

// PollWechatLogin advances the QR handshake once. verifyCode carries the numeric
// pairing code when a prior poll returned need_verifycode. On "confirmed" it
// redeems the onboarding token, creating the channel, and returns its id.
func (s *IMBotService) PollWechatLogin(ctx context.Context, rawToken, verifyCode string) (WechatLoginResult, error) {
	if err := s.ensureWechatOnboarding(ctx, rawToken); err != nil {
		return WechatLoginResult{}, err
	}
	v, ok := s.wechatSessions.Load(onboardingTokenHash(rawToken))
	sess, _ := v.(*wechatLoginSession)
	if !ok || sess == nil || time.Since(sess.createdAt) > wechatLoginSessionTTL {
		s.wechatSessions.Delete(onboardingTokenHash(rawToken))
		return WechatLoginResult{Status: "expired"}, nil
	}

	st, err := wechat.PollQRStatus(ctx, sess.qrcode, verifyCode)
	if err != nil {
		return WechatLoginResult{}, fmt.Errorf("wechat: poll login: %w", err)
	}
	if st.Status != "confirmed" {
		return WechatLoginResult{Status: st.Status}, nil
	}
	if st.BotToken == "" || st.IlinkBotID == "" {
		return WechatLoginResult{}, fmt.Errorf("wechat: login confirmed but token/bot id missing")
	}

	channelID, err := s.SubmitOnboardingCredential(ctx, rawToken, wechat.CredentialFromStatus(st))
	if err != nil {
		return WechatLoginResult{}, err
	}
	s.wechatSessions.Delete(onboardingTokenHash(rawToken))
	return WechatLoginResult{Status: "confirmed", ChannelID: channelID}, nil
}
