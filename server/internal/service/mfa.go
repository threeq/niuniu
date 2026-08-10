package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// MFAService manages TOTP multi-factor authentication: provisioning, validation,
// backup codes, and trusted-device tokens.
type MFAService struct {
	q       *store.Queries
	keyring *crypto.Keyring
}

func NewMFAService(q *store.Queries, keyring *crypto.Keyring) *MFAService {
	return &MFAService{q: q, keyring: keyring}
}

// SetupResult is returned by Setup — the caller must show the QR data URL to
// the user. The secret stays encrypted at rest; the plaintext secret is only
// used to derive the provisioning URI.
type SetupResult struct {
	ProvisioningURI string
	QRDataURI       string
	Secret          string
}

// Setup generates a fresh TOTP secret, encrypts it with the MFA keyring, and
// stores it in user_mfa (unconfirmed). The caller must subsequently call
// Enable with a valid code to confirm the user scanned the QR.
func (s *MFAService) Setup(ctx context.Context, userID int64, issuer, accountName string) (*SetupResult, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp key: %w", err)
	}

	secretPT := []byte(key.Secret())
	secretCT, err := s.keyring.Encrypt(secretPT)
	if err != nil {
		return nil, fmt.Errorf("encrypt mfa secret: %w", err)
	}

	if err := s.q.CreateMFA(ctx, store.CreateMFAParams{
		UserID:           userID,
		Method:           "totp",
		SecretCiphertext: secretCT,
	}); err != nil {
		return nil, fmt.Errorf("store mfa secret: %w", err)
	}

	return &SetupResult{
		ProvisioningURI: key.URL(),
		QRDataURI:       key.URL(),
		Secret:          key.Secret(),
	}, nil
}

// Enable confirms the user has correctly scanned the TOTP secret by validating
// a code. On success, flips user_mfa.enabled_at + users.mfa_enabled, and
// generates count backup codes.
func (s *MFAService) Enable(ctx context.Context, userID int64, code string, backupCodeCount int) ([]string, error) {
	if err := s.validateCode(ctx, userID, code); err != nil {
		return nil, err
	}
	if err := s.q.EnableMFA(ctx, userID); err != nil {
		return nil, fmt.Errorf("enable mfa: %w", err)
	}
	if err := s.q.SetUserMFAEnabled(ctx, store.SetUserMFAEnabledParams{
		MfaEnabled: 1,
		ID:         userID,
	}); err != nil {
		return nil, fmt.Errorf("set user mfa_enabled: %w", err)
	}
	codes, err := s.generateBackupCodes(ctx, userID, backupCodeCount)
	if err != nil {
		return nil, fmt.Errorf("generate backup codes: %w", err)
	}
	return codes, nil
}

// ValidateCode checks a TOTP code against the stored (encrypted) secret.
// Supports ±1 step window (30s each side) for clock drift.
func (s *MFAService) validateCode(ctx context.Context, userID int64, code string) error {
	mfa, err := s.q.GetMFAByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("mfa not configured")
		}
		return err
	}
	secretPT, err := s.keyring.Decrypt(mfa.SecretCiphertext)
	if err != nil {
		return fmt.Errorf("decrypt mfa secret: %w", err)
	}
	valid := totp.Validate(code, string(secretPT))
	if !valid {
		return fmt.Errorf("invalid totp code")
	}
	return nil
}

// ValidateCodeOrBackup tries TOTP first, then backup codes.
func (s *MFAService) ValidateCodeOrBackup(ctx context.Context, userID int64, code string) error {
	if err := s.validateCode(ctx, userID, code); err == nil {
		return nil
	}
	return s.consumeBackupCode(ctx, userID, code)
}

// Disable removes MFA for the user. Requires a valid code to prevent hijack.
func (s *MFAService) Disable(ctx context.Context, userID int64, code string) error {
	if err := s.ValidateCodeOrBackup(ctx, userID, code); err != nil {
		return err
	}
	if err := s.q.DeleteMFA(ctx, userID); err != nil {
		return err
	}
	if err := s.q.DeleteBackupCodesByUser(ctx, userID); err != nil {
		slog.Warn("delete backup codes on disable", "user_id", userID, "error", err)
	}
	if err := s.q.DeleteTrustedDevicesByUser(ctx, userID); err != nil {
		slog.Warn("delete trusted devices on disable", "user_id", userID, "error", err)
	}
	return s.q.SetUserMFAEnabled(ctx, store.SetUserMFAEnabledParams{
		MfaEnabled: 0,
		ID:         userID,
	})
}

// MFAModel returns the current MFA row for a user (or sql.ErrNoRows).
func (s *MFAService) MFAModel(ctx context.Context, userID int64) (store.UserMfa, error) {
	return s.q.GetMFAByUserID(ctx, userID)
}

// Status returns the MFA status summary for the current user.
type MFAStatus struct {
	Enabled            bool `json:"enabled"`
	BackupCodesRemain  int  `json:"backup_codes_remain"`
	TrustedDeviceCount int  `json:"trusted_device_count"`
}

func (s *MFAService) Status(ctx context.Context, userID int64) (MFAStatus, error) {
	var st MFAStatus
	_, err := s.q.GetMFAByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return st, nil
		}
		return st, err
	}
	st.Enabled = true
	n, _ := s.q.CountActiveBackupCodes(ctx, userID)
	st.BackupCodesRemain = int(n)
	devices, _ := s.q.ListTrustedDevicesForUser(ctx, userID)
	st.TrustedDeviceCount = len(devices)
	return st, nil
}

// RegenerateBackupCodes replaces all existing backup codes with fresh ones.
func (s *MFAService) RegenerateBackupCodes(ctx context.Context, userID int64, code string, count int) ([]string, error) {
	if err := s.ValidateCodeOrBackup(ctx, userID, code); err != nil {
		return nil, err
	}
	if err := s.q.DeleteBackupCodesByUser(ctx, userID); err != nil {
		return nil, err
	}
	return s.generateBackupCodes(ctx, userID, count)
}

func (s *MFAService) generateBackupCodes(ctx context.Context, userID int64, count int) ([]string, error) {
	codes := make([]string, count)
	for i := 0; i < count; i++ {
		b := make([]byte, 5)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		code := base64.StdEncoding.EncodeToString(b)[:8]
		codes[i] = code
		hash := sha256.Sum256([]byte(code))
		hashHex := hex.EncodeToString(hash[:])
		if err := s.q.InsertBackupCode(ctx, store.InsertBackupCodeParams{
			UserID:   userID,
			CodeHash: hashHex,
		}); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

func (s *MFAService) consumeBackupCode(ctx context.Context, userID int64, code string) error {
	hash := sha256.Sum256([]byte(code))
	hashHex := hex.EncodeToString(hash[:])
	err := s.q.ConsumeBackupCode(ctx, store.ConsumeBackupCodeParams{
		UserID:   userID,
		CodeHash: hashHex,
	})
	if err != nil {
		return fmt.Errorf("invalid backup code")
	}
	return nil
}

// TrustDevice creates a trusted-device token. Returns the raw token for the
// cookie and stores its hash.
func (s *MFAService) TrustDevice(ctx context.Context, userID int64, ua string, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := sha256.Sum256([]byte(token))
	tokenHashHex := hex.EncodeToString(tokenHash[:])

	if err := s.q.CreateTrustedDevice(ctx, store.CreateTrustedDeviceParams{
		UserID:    userID,
		TokenHash: tokenHashHex,
		UserAgent: ua,
		ExpiresAt: time.Now().Add(ttl),
	}); err != nil {
		return "", err
	}
	return token, nil
}

// CheckTrustedDevice looks up the token. Returns true if valid and unexpired.
// Also touches last_seen_at.
func (s *MFAService) CheckTrustedDevice(ctx context.Context, rawToken string) (int64, bool) {
	tokenHash := sha256.Sum256([]byte(rawToken))
	tokenHashHex := hex.EncodeToString(tokenHash[:])
	dev, err := s.q.GetTrustedDeviceByHash(ctx, tokenHashHex)
	if err != nil {
		return 0, false
	}
	if dev.ExpiresAt.Before(time.Now()) {
		return 0, false
	}
	_ = s.q.TouchTrustedDevice(ctx, dev.ID)
	return dev.UserID, true
}

// RevokeTrustedDevice removes one trusted device.
func (s *MFAService) RevokeTrustedDevice(ctx context.Context, userID, deviceID int64) error {
	return s.q.DeleteTrustedDevice(ctx, store.DeleteTrustedDeviceParams{
		ID:     deviceID,
		UserID: userID,
	})
}

// ListTrustedDevices returns all active trusted devices for a user.
func (s *MFAService) ListTrustedDevices(ctx context.Context, userID int64) ([]store.MfaTrustedDevice, error) {
	return s.q.ListTrustedDevicesForUser(ctx, userID)
}

// AdminResetMFA forcefully clears MFA for a user (support flow).
func (s *MFAService) AdminResetMFA(ctx context.Context, userID int64) error {
	_ = s.q.DeleteMFA(ctx, userID)
	_ = s.q.DeleteBackupCodesByUser(ctx, userID)
	_ = s.q.DeleteTrustedDevicesByUser(ctx, userID)
	return s.q.SetUserMFAEnabled(ctx, store.SetUserMFAEnabledParams{
		MfaEnabled: 0,
		ID:         userID,
	})
}
