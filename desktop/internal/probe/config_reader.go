package probe

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// readConfiguredPort parses ~/.niuniu/config.yaml (only the server.port
// field) and returns the port. Missing file or parse error → 0.
// We don't depend on the server's full config package to avoid pulling
// the entire server module into desktop.
func readConfiguredPort(dataDir string) int {
	path := filepath.Join(dataDir, "config.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var cfg struct {
		Server struct {
			Port int `yaml:"port"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return 0
	}
	return cfg.Server.Port
}
