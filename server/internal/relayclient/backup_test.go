package relayclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// fakeBackupServer implements PUT, GET, and DELETE /api/accounts/identity-backup
// backed by an in-memory store. It validates the Authorization header.
func fakeBackupServer(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	type entry struct {
		blobB64 string
		meta    *pairingcrypto.BackupMeta
	}
	store := map[string]*entry{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/identity-backup", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var body struct {
				WrappedBlobB64 string                    `json:"wrapped_blob_b64"`
				Meta           *pairingcrypto.BackupMeta `json:"meta"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.WrappedBlobB64 == "" || body.Meta == nil {
				http.Error(w, "bad_request", http.StatusBadRequest)
				return
			}
			store["default"] = &entry{blobB64: body.WrappedBlobB64, meta: body.Meta}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"saved": true})
		case http.MethodGet:
			e, ok := store["default"]
			if !ok {
				http.Error(w, "not_found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wrapped_blob_b64": e.blobB64,
				"meta":             e.meta,
			})
		case http.MethodDelete:
			delete(store, "default")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/accounts/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": wantToken})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestBackupIdentityRoundTrip verifies that BackupIdentity + RestoreIdentity
// preserve the identity via the actual WrapIdentity/UnwrapIdentity crypto.
func TestBackupIdentityRoundTrip(t *testing.T) {
	const token = "test-access-token"
	srv := fakeBackupServer(t, token)

	id, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	cli := NewClient(&Config{RelayURL: srv.URL, Email: "u@x"})
	cli.access = token // inject token directly

	const accountID = "acct-123"
	const passphrase = "s3cr3t-passphrase"

	if err := cli.BackupIdentity(accountID, passphrase, id); err != nil {
		t.Fatalf("BackupIdentity: %v", err)
	}

	restored, err := cli.RestoreIdentity(accountID, passphrase)
	if err != nil {
		t.Fatalf("RestoreIdentity: %v", err)
	}

	if !bytes.Equal(restored.XPriv, id.XPriv) {
		t.Fatal("XPriv mismatch after restore")
	}
	if !bytes.Equal(restored.EdPub, id.EdPub) {
		t.Fatal("EdPub mismatch after restore")
	}
}

// TestRestoreIdentityNotFound verifies that ErrBackupNotFound is returned
// when the relay responds with 404.
func TestRestoreIdentityNotFound(t *testing.T) {
	const token = "test-token"
	srv := fakeBackupServer(t, token) // empty store → GET returns 404

	cli := NewClient(&Config{RelayURL: srv.URL})
	cli.access = token

	_, err := cli.RestoreIdentity("acct-123", "passphrase")
	if !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("expected ErrBackupNotFound, got %v", err)
	}
}

// TestBackupIdentityRequiresLogin verifies that calling BackupIdentity before
// Login returns an error rather than panicking or sending an empty token.
func TestBackupIdentityRequiresLogin(t *testing.T) {
	cli := NewClient(&Config{RelayURL: "http://localhost:9"})
	id, _ := pairingcrypto.GenerateIdentity()
	err := cli.BackupIdentity("acct", "pass", id)
	if err == nil {
		t.Fatal("expected error when not logged in")
	}
}

// TestRestoreIdentityWrongPassphrase verifies that providing an incorrect
// passphrase causes unwrap to fail.
func TestRestoreIdentityWrongPassphrase(t *testing.T) {
	const token = "test-token"
	srv := fakeBackupServer(t, token)

	id, _ := pairingcrypto.GenerateIdentity()

	// Create a valid backup with a known passphrase, then inject it into the
	// fake server by wrapping + base64-encoding manually.
	blob, meta, err := pairingcrypto.WrapIdentity("correct", "acct-456", id)
	if err != nil {
		t.Fatalf("WrapIdentity: %v", err)
	}

	// PUT via HTTP directly to seed the fake server.
	body, _ := json.Marshal(map[string]any{
		"wrapped_blob_b64": base64.StdEncoding.EncodeToString(blob),
		"meta":             meta,
	})
	req, _ := http.NewRequest("PUT", srv.URL+"/api/accounts/identity-backup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	_, _ = http.DefaultClient.Do(req)

	cli := NewClient(&Config{RelayURL: srv.URL})
	cli.access = token

	_, err = cli.RestoreIdentity("acct-456", "wrong-passphrase")
	if err == nil {
		t.Fatal("expected error on wrong passphrase")
	}
}
