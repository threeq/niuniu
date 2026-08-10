package lanhost

import (
	"fmt"
	"os"

	"github.com/hashicorp/mdns"
	"github.com/niuniu-dev/niuniu/go-shared/lanproto"
)

// Publisher registers an mDNS entry for this desktop's LAN host.
type Publisher struct {
	server *mdns.Server
}

// Publish starts advertising the desktop's LAN tunnel endpoint over mDNS.
// desktopID is the stable desktop identifier, xpub is the X25519 public key,
// and port is the TCP port returned by Server.Start.
func Publish(desktopID string, xpub []byte, port int) (*Publisher, error) {
	txt := (&lanproto.TXTRecord{
		Version:         lanproto.ProtocolVersion,
		DesktopID:       desktopID,
		XpubFingerprint: lanproto.FingerprintXpub(xpub),
		Port:            port,
	}).Encode()

	hostname, _ := os.Hostname()
	svc, err := mdns.NewMDNSService(
		fmt.Sprintf("niuniu-%s", desktopID),
		lanproto.ServiceName,
		"",
		hostname+".",
		port,
		nil,
		txt,
	)
	if err != nil {
		return nil, fmt.Errorf("lanhost: mdns service: %w", err)
	}
	srv, err := mdns.NewServer(&mdns.Config{Zone: svc})
	if err != nil {
		return nil, fmt.Errorf("lanhost: mdns server: %w", err)
	}
	return &Publisher{server: srv}, nil
}

// Shutdown stops the mDNS advertisement.
func (p *Publisher) Shutdown() {
	if p.server != nil {
		p.server.Shutdown()
	}
}
