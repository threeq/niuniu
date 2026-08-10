package discovery_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMDNSBroadcaster(t *testing.T) {
	b, err := discovery.NewMDNSBroadcaster("test-host", 3000, "dev")
	require.NoError(t, err)
	assert.NotNil(t, b)
	b.Shutdown()
}
