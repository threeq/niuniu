package discovery_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/discovery"
	"github.com/stretchr/testify/assert"
)

func TestParseInstance(t *testing.T) {
	info := []string{"version=1.0.0", "hostname=my-mac"}
	inst := discovery.ParseTXTRecord(info, "192.168.1.5", 3000)
	assert.Equal(t, "192.168.1.5", inst.Host)
	assert.Equal(t, 3000, inst.Port)
	assert.Equal(t, "1.0.0", inst.Version)
	assert.Equal(t, "my-mac", inst.Hostname)
}

func TestParseInstance_EmptyTXT(t *testing.T) {
	inst := discovery.ParseTXTRecord(nil, "10.0.0.1", 3000)
	assert.Equal(t, "10.0.0.1", inst.Host)
	assert.Equal(t, "", inst.Version)
	assert.Equal(t, "", inst.Hostname)
}
