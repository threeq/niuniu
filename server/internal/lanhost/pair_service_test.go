package lanhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/relayclient"
	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// TestLanOnlyPairingFullCeremony simulates a full LAN-only pair ceremony:
//   1. Start the Server.
//   2. Start a pairing session (StartLanOnlyPairing in a goroutine).
//   3. Send POST /pair/claim from the "mobile" side.
//   4. Verify the response carries the desktop's identity material.
//   5. Verify StartLanOnlyPairing returns the mobile's key material.
func TestLanOnlyPairingFullCeremony(t *testing.T) {
	desktopID := "test-desktop-lan-pair"
	desktopName := "My Work MacBook"
	desktopIdent, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	mobileIdent, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity mobile: %v", err)
	}

	proxy := relayclient.NewProxyWithIdentity("http://127.0.0.1:1", desktopIdent)
	srv := &Server{
		DesktopID: desktopID,
		Identity:  desktopIdent,
		Proxy:     proxy,
	}
	addr, err := srv.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop(context.Background()) //nolint:errcheck

	// Run pairing in background.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pairResultCh := make(chan *PairSession, 1)
	pairErrCh := make(chan error, 1)
	go func() {
		ps, err := srv.StartLanOnlyPairing(ctx, desktopID, desktopName, desktopIdent)
		if err != nil {
			pairErrCh <- err
			return
		}
		pairResultCh <- ps
	}()

	// Give the handler time to register.
	time.Sleep(20 * time.Millisecond)

	// POST /pair/claim from the mobile.
	claimBody, _ := json.Marshal(LanOnlyPairRequest{
		MobileXpubB64:    base64.StdEncoding.EncodeToString(mobileIdent.XPub),
		MobileSignPubB64: base64.StdEncoding.EncodeToString(mobileIdent.EdPub),
		MobileName:       "iPhone 15 Pro",
		MobilePlatform:   "ios",
	})
	claimURL := fmt.Sprintf("http://%s/pair/claim", addr.String())
	resp, err := http.Post(claimURL, "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatalf("POST /pair/claim: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var claimResp LanOnlyPairResponse
	if err := json.NewDecoder(resp.Body).Decode(&claimResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify desktop identity material in response.
	if claimResp.DesktopID != desktopID {
		t.Errorf("desktop_id: got %q want %q", claimResp.DesktopID, desktopID)
	}
	if claimResp.DesktopName != desktopName {
		t.Errorf("desktop_name: got %q want %q", claimResp.DesktopName, desktopName)
	}
	wantXpub := base64.StdEncoding.EncodeToString(desktopIdent.XPub)
	if claimResp.DesktopXpubB64 != wantXpub {
		t.Errorf("desktop_xpub_b64 mismatch")
	}

	// Wait for StartLanOnlyPairing to return the mobile's key material.
	select {
	case ps := <-pairResultCh:
		if !bytes.Equal(ps.MobileXpub, mobileIdent.XPub) {
			t.Error("mobile xpub mismatch in PairSession")
		}
		if ps.MobileName != "iPhone 15 Pro" {
			t.Errorf("mobile_name: got %q", ps.MobileName)
		}
	case err := <-pairErrCh:
		t.Fatalf("StartLanOnlyPairing: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for PairSession")
	}
}

// TestLanOnlyPairingTimeout verifies that StartLanOnlyPairing returns
// context.DeadlineExceeded when the context is cancelled before a claim arrives.
func TestLanOnlyPairingTimeout(t *testing.T) {
	desktopIdent, _ := pairingcrypto.GenerateIdentity()
	proxy := relayclient.NewProxyWithIdentity("http://127.0.0.1:1", desktopIdent)
	srv := &Server{
		DesktopID: "desk-timeout",
		Identity:  desktopIdent,
		Proxy:     proxy,
	}
	if _, err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop(context.Background()) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := srv.StartLanOnlyPairing(ctx, "desk-timeout", "Test", desktopIdent)
	if err == nil {
		t.Fatal("expected error on timeout but got nil")
	}
}
