package discovery

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/hashicorp/mdns"
)

const ServiceName = "_niuniu._tcp"

type MDNSBroadcaster struct {
	server *mdns.Server
}

func NewMDNSBroadcaster(host string, port int, version string) (*MDNSBroadcaster, error) {
	hostname, _ := os.Hostname()
	info := []string{
		fmt.Sprintf("version=%s", version),
		fmt.Sprintf("hostname=%s", hostname),
	}

	service, err := mdns.NewMDNSService(hostname, ServiceName, "", "", port, nil, info)
	if err != nil {
		return nil, fmt.Errorf("mdns service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, fmt.Errorf("mdns server: %w", err)
	}

	slog.Info("mDNS broadcast started", "service", ServiceName, "port", port)
	return &MDNSBroadcaster{server: server}, nil
}

func (b *MDNSBroadcaster) Shutdown() {
	if b.server != nil {
		b.server.Shutdown()
		slog.Info("mDNS broadcast stopped")
	}
}
