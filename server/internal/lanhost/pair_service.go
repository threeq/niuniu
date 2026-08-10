package lanhost

// pair_service.go implements LAN-only pairing (spec §6.5).
//
// The desktop enters "LAN-only pair mode" (today via the /api/relay/pairing
// REST surface on the server; historically a `niuniu-desktop pair` CLI
// existed but has been removed).  While active it:
//  1. Advertises _niuniu-pair._tcp over mDNS.
//  2. Accepts POST /pair/claim from the mobile (no auth required).
//  3. Returns the desktop identity material so the mobile can pin the keys.
//
// This path never involves the relay.  No account, no device tokens.

import (
	"context"
	"time"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

const (
	// LanOnlyPairTimeout is how long the desktop advertises and accepts LAN-only
	// pair claims before automatically stopping.
	LanOnlyPairTimeout = 120 * time.Second

	// lanPairServiceType is the mDNS service type for LAN-only pairing.
	// Distinct from the normal LAN tunnel service so mobile can filter.
	lanPairServiceType = "_niuniu-pair._tcp"
)

// LanOnlyPairRequest is the body of POST /pair/claim.
type LanOnlyPairRequest struct {
	MobileXpubB64    string `json:"mobile_xpub_b64"`
	MobileSignPubB64 string `json:"mobile_sign_pub_b64"`
	MobileName       string `json:"mobile_name,omitempty"`
	MobilePlatform   string `json:"mobile_platform,omitempty"`
}

// LanOnlyPairResponse is the response body for a successful POST /pair/claim.
type LanOnlyPairResponse struct {
	DesktopID         string `json:"desktop_id"`
	DesktopName       string `json:"desktop_name"`
	DesktopXpubB64    string `json:"desktop_xpub_b64"`
	DesktopSignPubB64 string `json:"desktop_sign_pub_b64"`
}

// PairSession carries the result of a completed LAN-only pair claim so the
// caller can persist the new trusted mobile.
type PairSession struct {
	MobileXpub     []byte
	MobileSignPub  []byte
	MobileName     string
	MobilePlatform string
}

// StartLanOnlyPairing activates LAN-only pairing mode on the already-started
// Server s for up to LanOnlyPairTimeout.  It sets an atomic flag that enables
// the /pair/claim route (always registered at Start time) and waits for the
// first successfully claimed PairSession, ctx cancellation, or timeout.
//
// The handler-swap approach previously used was racy: mutating httpSrv.Handler
// while the server is actively serving requests is not safe.  This
// implementation uses an atomic flag instead — no handler is swapped.
//
// The caller should call Server.AddTrustedMobile with PairSession.MobileXpub
// after this returns.
//
// NOTE: The Server must have been started before calling this function.
func (s *Server) StartLanOnlyPairing(
	ctx context.Context,
	desktopID, desktopName string,
	identity *pairingcrypto.Identity,
) (*PairSession, error) {
	resultCh := make(chan *PairSession, 1)

	// Populate the pairing context fields while holding the lock, then flip the
	// atomic flag.  The flag is checked first in handlePairClaim so there is no
	// window where the channel or identity could be read before they are set.
	s.mu.Lock()
	s.pairResultCh = resultCh
	s.pairIdentity = identity
	s.pairDesktopID = desktopID
	s.pairDesktopName = desktopName
	s.mu.Unlock()

	s.pairMode.Store(1)

	defer func() {
		s.pairMode.Store(0)
		s.mu.Lock()
		s.pairResultCh = nil
		s.pairIdentity = nil
		s.mu.Unlock()
	}()

	deadline := time.NewTimer(LanOnlyPairTimeout)
	defer deadline.Stop()

	select {
	case ps := <-resultCh:
		return ps, nil
	case <-deadline.C:
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
