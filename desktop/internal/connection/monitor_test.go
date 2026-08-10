package connection_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu-desktop/internal/connection"
	"github.com/stretchr/testify/assert"
)

func TestMonitor_DetectsDisconnect(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.Write([]byte(`{"status":"ok","version":"dev","uptime_seconds":1}`))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	var disconnected atomic.Bool
	var reconnected atomic.Bool
	mon := connection.NewMonitor(connection.MonitorConfig{
		URL: srv.URL, Interval: 50 * time.Millisecond,
		ReconnectInterval: 50 * time.Millisecond, MaxFailures: 3,
		OnDisconnect:  func() { disconnected.Store(true) },
		OnReconnect:   func() { reconnected.Store(true) },
		OnMaxFailures: func() {},
	})
	mon.Start()
	defer mon.Stop()
	time.Sleep(200 * time.Millisecond)
	assert.False(t, disconnected.Load())
	healthy.Store(false)
	time.Sleep(200 * time.Millisecond)
	assert.True(t, disconnected.Load())
	healthy.Store(true)
	time.Sleep(200 * time.Millisecond)
	assert.True(t, reconnected.Load())
}
