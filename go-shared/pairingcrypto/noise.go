package pairingcrypto

import (
	"errors"

	"github.com/flynn/noise"
)

// KKState is one side of a Noise KK handshake in progress.
type KKState struct {
	hs   *noise.HandshakeState
	send *noise.CipherState
	rcv  *noise.CipherState
}

// NewKKInitiator prepares the initiator side (mobile in our spec).
func NewKKInitiator(self *Identity, peerXPub []byte) (*KKState, error) {
	return newKK(self, peerXPub, true)
}

// NewKKResponder prepares the responder side (desktop in our spec).
func NewKKResponder(self *Identity, peerXPub []byte) (*KKState, error) {
	return newKK(self, peerXPub, false)
}

func newKK(self *Identity, peerXPub []byte, initiator bool) (*KKState, error) {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cs,
		Pattern:       noise.HandshakeKK,
		Initiator:     initiator,
		StaticKeypair: noise.DHKey{Private: self.XPriv, Public: self.XPub},
		PeerStatic:    peerXPub,
	})
	if err != nil {
		return nil, err
	}
	return &KKState{hs: hs}, nil
}

// WriteMessage produces the next handshake message to send, optionally
// bearing a payload. When the handshake completes mid-call, the resulting
// cipher states are captured on k.
func (k *KKState) WriteMessage(payload []byte) ([]byte, error) {
	out, cs0, cs1, err := k.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, err
	}
	if cs0 != nil {
		k.send, k.rcv = cs0, cs1
	}
	return out, nil
}

// ReadMessage consumes an incoming handshake message; returns any payload.
func (k *KKState) ReadMessage(msg []byte) ([]byte, error) {
	out, cs0, cs1, err := k.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, err
	}
	if cs0 != nil {
		// Per flynn/noise: after ReadMessage, cs0 is the receive state and cs1 the send state.
		k.rcv, k.send = cs0, cs1
	}
	return out, nil
}

// Cipher returns the CipherPair after the handshake completes.
func (k *KKState) Cipher() (*CipherPair, error) {
	if k.send == nil || k.rcv == nil {
		return nil, errors.New("pairingcrypto: handshake not yet complete")
	}
	// Capture the handshake hash (h) as a diagnostic fingerprint. Both peers
	// of a successful handshake derive identical h values; a mismatch in logs
	// pinpoints which handshake a cached cipher originated from.
	return &CipherPair{send: k.send, rcv: k.rcv, handshakeHash: k.hs.ChannelBinding()}, nil
}

// CipherPair holds the two directional cipher states produced by a handshake.
//
// Counter-based rekey: if rekeyEvery > 0, the send cipher is rekeyed automatically
// after that many Encrypt calls (and the receive cipher after that many Decrypt calls).
// Both sides must be configured with the same rekeyEvery value so the key material
// stays synchronised — the key derivation depends only on message count, not wall time.
type CipherPair struct {
	send          *noise.CipherState
	rcv           *noise.CipherState
	sendCount     int64
	recvCount     int64
	rekeyEvery    int64 // 0 = disabled
	handshakeHash []byte
}

// HandshakeHash returns the Noise handshake hash (h) at the moment Split
// completed. Both sides of a successful handshake derive identical h; logging
// the first few bytes is a cheap way to fingerprint which handshake a given
// cipher originated from.
func (c *CipherPair) HandshakeHash() []byte { return c.handshakeHash }

// SetRekeyInterval configures automatic rekey after n encrypts/decrypts.
// Set the same value on both sides immediately after the handshake completes.
// Set to 0 to disable (default).
func (c *CipherPair) SetRekeyInterval(n int64) { c.rekeyEvery = n }

// RekeyInterval returns the currently configured rekey interval.
func (c *CipherPair) RekeyInterval() int64 { return c.rekeyEvery }

// Encrypt seals plaintext with the send cipher state (AD nil).
// Triggers a send-side rekey when sendCount reaches rekeyEvery (if non-zero).
func (c *CipherPair) Encrypt(dst, plaintext []byte) ([]byte, error) {
	ct, err := c.send.Encrypt(dst, nil, plaintext)
	if err == nil {
		c.sendCount++
		if c.rekeyEvery > 0 && c.sendCount >= c.rekeyEvery {
			c.send.Rekey()
			c.sendCount = 0
		}
	}
	return ct, err
}

// Decrypt opens ciphertext with the receive cipher state (AD nil).
// Triggers a receive-side rekey when recvCount reaches rekeyEvery (if non-zero).
func (c *CipherPair) Decrypt(dst, ciphertext []byte) ([]byte, error) {
	pt, err := c.rcv.Decrypt(dst, nil, ciphertext)
	if err == nil {
		c.recvCount++
		if c.rekeyEvery > 0 && c.recvCount >= c.rekeyEvery {
			c.rcv.Rekey()
			c.recvCount = 0
		}
	}
	return pt, err
}

// Rekey rotates both cipher states per the Noise spec and resets counters.
// Called explicitly by higher-level code; also called internally when the counter threshold is hit.
func (c *CipherPair) Rekey() {
	c.send.Rekey()
	c.rcv.Rekey()
	c.sendCount = 0
	c.recvCount = 0
}
