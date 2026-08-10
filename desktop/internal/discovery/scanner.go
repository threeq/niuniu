package discovery

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

const ServiceName = "_niuniu._tcp"

type Instance struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
}

func ParseTXTRecord(info []string, host string, port int) Instance {
	inst := Instance{Host: host, Port: port}
	for _, entry := range info {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "version":
			inst.Version = parts[1]
		case "hostname":
			inst.Hostname = parts[1]
		}
	}
	return inst
}

type Scanner struct {
	mu        sync.RWMutex
	instances []Instance
	interval  time.Duration
	stop      chan struct{}
	once      sync.Once
	onChange  func([]Instance)
}

func NewScanner(interval time.Duration, onChange func([]Instance)) *Scanner {
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &Scanner{interval: interval, stop: make(chan struct{}), onChange: onChange}
}

func (s *Scanner) Start() { go s.run() }

func (s *Scanner) Stop() {
	s.once.Do(func() { close(s.stop) })
}

func (s *Scanner) Instances() []Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Instance, len(s.instances))
	copy(result, s.instances)
	return result
}

func (s *Scanner) run() {
	s.scan()
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(s.interval):
			s.scan()
		}
	}
}

func (s *Scanner) scan() {
	entriesCh := make(chan *mdns.ServiceEntry, 16)
	var found []Instance
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for entry := range entriesCh {
			var host string
			if entry.AddrV4 != nil {
				host = entry.AddrV4.String()
			} else if entry.AddrV6 != nil {
				host = entry.AddrV6.String()
			} else {
				host = strings.TrimSuffix(entry.Host, ".")
			}
			inst := ParseTXTRecord(entry.InfoFields, host, entry.Port)
			found = append(found, inst)
		}
	}()
	params := &mdns.QueryParam{
		Service: ServiceName, Timeout: 2 * time.Second, Entries: entriesCh,
	}
	if err := mdns.Query(params); err != nil {
		slog.Debug("mDNS scan error", "error", err)
	}
	close(entriesCh)
	wg.Wait() // ensure consumer goroutine has finished before reading found
	s.mu.Lock()
	s.instances = found
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(found)
	}
	slog.Debug("mDNS scan complete", "found", len(found))
}

func (i Instance) DisplayName() string {
	if i.Hostname != "" {
		return fmt.Sprintf("%s (%s:%d)", i.Hostname, i.Host, i.Port)
	}
	return fmt.Sprintf("%s:%d", i.Host, i.Port)
}
