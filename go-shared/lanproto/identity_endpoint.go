package lanproto

// IdentityResponse is the JSON body served at /.well-known/niuniu-identity
// by the desktop's LAN host. It allows the mobile to pre-verify it is
// talking to the right desktop before attempting a KK handshake.
type IdentityResponse struct {
	Version         int    `json:"version"`
	DesktopID       string `json:"desktop_id"`
	XpubFingerprint string `json:"xpub_fingerprint"`
}
