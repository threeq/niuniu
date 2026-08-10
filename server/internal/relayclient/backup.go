package relayclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// ErrBackupNotFound signals the account has no identity backup stored on the relay.
var ErrBackupNotFound = errors.New("relayclient: no identity backup for account")

// BackupIdentity wraps id with passphrase+accountID and uploads it to the relay.
// accountID is the relay account UUID — it is included in the KDF domain
// separator but not transmitted (the relay derives it from the JWT).
func (c *Client) BackupIdentity(accountID, passphrase string, id *pairingcrypto.Identity) error {
	if c.access == "" {
		return errors.New("relayclient: login first")
	}
	blob, meta, err := pairingcrypto.WrapIdentity(passphrase, accountID, id)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"wrapped_blob_b64": base64.StdEncoding.EncodeToString(blob),
		"meta":             meta,
	})
	req, _ := http.NewRequest("PUT", c.cfg.RelayURL+"/api/accounts/identity-backup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("BackupIdentity: status %d", resp.StatusCode)
	}
	return nil
}

// RestoreIdentity fetches the wrapped blob from the relay and unwraps it with
// the given passphrase. Returns ErrBackupNotFound if the account has no backup.
func (c *Client) RestoreIdentity(accountID, passphrase string) (*pairingcrypto.Identity, error) {
	if c.access == "" {
		return nil, errors.New("relayclient: login first")
	}
	req, _ := http.NewRequest("GET", c.cfg.RelayURL+"/api/accounts/identity-backup", nil)
	req.Header.Set("Authorization", "Bearer "+c.access)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrBackupNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RestoreIdentity: status %d", resp.StatusCode)
	}
	var out struct {
		WrappedBlobB64 string                    `json:"wrapped_blob_b64"`
		Meta           *pairingcrypto.BackupMeta `json:"meta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(out.WrappedBlobB64)
	if err != nil {
		return nil, err
	}
	return pairingcrypto.UnwrapIdentity(passphrase, accountID, blob, out.Meta)
}
