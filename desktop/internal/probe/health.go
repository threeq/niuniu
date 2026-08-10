package probe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HealthResponse is the subset of /api/health we care about.
type HealthResponse struct {
	Version string `json:"version"`
}

// checkHealth does a short-timeout GET to http://<addr>/api/health.
// Returns the parsed response, or an error if unreachable or non-200.
func checkHealth(addr string) (*HealthResponse, error) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("health returned status %d", resp.StatusCode)
	}
	var h HealthResponse
	_ = json.NewDecoder(resp.Body).Decode(&h) // version may be missing in older servers; tolerate
	return &h, nil
}

// versionCompatible returns true when remote version is safe to reuse
// from the perspective of a personal built at localVersion. The rule:
// major and minor must match exactly; patch may differ. Empty on both
// sides is treated as "dev build" and accepted (single-developer use).
// Unparseable versions block.
func versionCompatible(localVersion, remoteVersion string) bool {
	if localVersion == "" && remoteVersion == "" {
		return true
	}
	lMaj, lMin, okL := parseMajorMinor(localVersion)
	rMaj, rMin, okR := parseMajorMinor(remoteVersion)
	if !okL || !okR {
		return false
	}
	return lMaj == rMaj && lMin == rMin
}

func parseMajorMinor(v string) (int, int, bool) {
	s := strings.TrimPrefix(v, "v")
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	var maj, min int
	_, err := fmt.Sscanf(parts[0], "%d", &maj)
	if err != nil {
		return 0, 0, false
	}
	_, err = fmt.Sscanf(parts[1], "%d", &min)
	if err != nil {
		return 0, 0, false
	}
	return maj, min, true
}
