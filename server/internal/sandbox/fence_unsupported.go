//go:build !windows && !darwin && !linux

package sandbox

import (
	"fmt"
	"runtime"
)

// There is no fence implementation for this platform, so operation functions
// are refused outright. Failing closed is the whole point: running AI-authored
// code with no OS confinement would be worse than not offering the feature.
type unsupportedFence struct{}

func platformFence() fence { return unsupportedFence{} }

func (unsupportedFence) Backend() string { return "unsupported" }

func (unsupportedFence) Spawn(SpawnRequest) (boxProcess, error) {
	return nil, fmt.Errorf("no OS sandbox is implemented for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func (unsupportedFence) Probe() []ComponentStatus {
	return []ComponentStatus{{
		Name:     "os-sandbox",
		Required: true,
		OK:       false,
		Detail:   fmt.Sprintf("no sandbox backend is implemented for %s/%s", runtime.GOOS, runtime.GOARCH),
	}}
}
