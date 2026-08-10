package relayproto

// Hello is sent by the desktop immediately after the WSS upgrade.
// It is the payload of the initial HELLO frame (FrameHello, StreamID=0).
type Hello struct {
	DesktopID          string   `json:"desktop_id"`
	Version            uint16   `json:"version"`
	SupportedVersions  []uint16 `json:"supported_versions"`
	DesktopSignPubHex  string   `json:"desktop_sign_pub_hex,omitempty"`
	ClientCapabilities []string `json:"client_capabilities,omitempty"`
}

// HelloAck is the relay's reply immediately after Hello, before CHALLENGE.
// The relay sends this to inform the desktop of the negotiated protocol version.
type HelloAck struct {
	NegotiatedVersion      uint16   `json:"negotiated_version"`
	RelaySupportedVersions []uint16 `json:"relay_supported_versions,omitempty"`
}
