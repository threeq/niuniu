package connection

import (
	"log/slog"
	"sync"
	"time"
)

type MonitorConfig struct {
	URL               string
	Interval          time.Duration
	ReconnectInterval time.Duration
	MaxFailures       int
	OnDisconnect      func()
	OnReconnect       func()
	OnMaxFailures     func()
}

type Monitor struct {
	cfg      MonitorConfig
	mgr      *Manager
	stop     chan struct{}
	once     sync.Once
	started  sync.Once
	maxFired bool // true after OnMaxFailures has been called once
}

func NewMonitor(cfg MonitorConfig) *Monitor {
	if cfg.Interval == 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.ReconnectInterval == 0 {
		cfg.ReconnectInterval = 5 * time.Second
	}
	if cfg.MaxFailures == 0 {
		cfg.MaxFailures = 6
	}
	return &Monitor{cfg: cfg, mgr: NewManager(), stop: make(chan struct{})}
}

func (m *Monitor) Start() {
	m.started.Do(func() { go m.run() })
}

func (m *Monitor) Stop() {
	m.once.Do(func() { close(m.stop) })
}

func (m *Monitor) run() {
	failures := 0
	wasConnected := true
	for {
		interval := m.cfg.Interval
		if failures > 0 {
			interval = m.cfg.ReconnectInterval
		}
		select {
		case <-m.stop:
			return
		case <-time.After(interval):
		}
		_, err := m.mgr.CheckHealth(m.cfg.URL)
		if err != nil {
			failures++
			slog.Debug("health check failed", "url", m.cfg.URL, "failures", failures, "error", err)
			if wasConnected && failures == 1 {
				wasConnected = false
				if m.cfg.OnDisconnect != nil {
					m.cfg.OnDisconnect()
				}
			}
			if failures >= m.cfg.MaxFailures && !m.maxFired {
				m.maxFired = true
				if m.cfg.OnMaxFailures != nil {
					m.cfg.OnMaxFailures()
				}
			}
		} else {
			if !wasConnected {
				slog.Info("reconnected", "url", m.cfg.URL)
				if m.cfg.OnReconnect != nil {
					m.cfg.OnReconnect()
				}
			}
			failures = 0
			wasConnected = true
			m.maxFired = false // reset so future disconnects can fire again
		}
	}
}
