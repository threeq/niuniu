package sandbox

import (
	"io"
	"runtime"
	"sync"
	"time"
)

// ComponentStatus is the result of probing ONE enforcement mechanism. The UI
// renders these verbatim, so the wording is user-facing.
type ComponentStatus struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Detail   string `json:"detail,omitempty"`
}

// Availability is the platform fence's self-check verdict.
//
// Available is false when a REQUIRED component could not be established; the
// host then refuses to run any operation function. Degraded means the fence
// works but a hardening component is missing — the UI must say so explicitly,
// because a silently weaker fence is the failure mode this design exists to
// prevent.
type Availability struct {
	Platform   string            `json:"platform"`
	Backend    string            `json:"backend"`
	Available  bool              `json:"available"`
	Degraded   bool              `json:"degraded"`
	Reason     string            `json:"reason,omitempty"`
	Components []ComponentStatus `json:"components"`
	ProbedAt   time.Time         `json:"probed_at"`
}

// Missing lists the names of required components that failed.
func (a Availability) Missing() []string {
	var out []string
	for _, c := range a.Components {
		if c.Required && !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

// SpawnRequest describes the box process the fence must create. The fence owns
// process creation because the strongest backends cannot be expressed as
// arguments to os/exec: AppContainer needs a SECURITY_CAPABILITIES thread
// attribute list, and Linux needs namespace clone flags.
type SpawnRequest struct {
	// Path is the helper executable.
	Path string
	// Args are the arguments AFTER argv[0] (the fence still owns argv[0]).
	Args []string
	// Env is the COMPLETE environment for the box. Nothing is inherited: the
	// capability layer promises the function cannot read the host environment,
	// and an inherited block would break that promise from below.
	Env []string
	// Dir is the box's working directory.
	Dir string
	// MemoryBytes is the hard memory ceiling for the OS fence.
	MemoryBytes int64
}

// boxProcess is a spawned, fenced child with its stdio wired.
type boxProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Pid() int
	// Wait blocks until the process exits.
	Wait() error
	// Kill terminates the whole fenced unit (process tree / app container).
	Kill() error
	// PeakMemoryBytes reports the fence's own accounting of peak memory, or 0
	// when the platform cannot report it.
	PeakMemoryBytes() int64
	// Close releases fence-owned handles. It never kills.
	Close()
}

// fence is the per-platform hardening backend. Implementations live in
// fence_<goos>.go.
//
// Pluggability (the "个人版 → 团队版" axis in #739): the desktop editions get
// the OS-native backends below, which need no dependency the user has to
// install. A team edition running on a Linux server can add a stronger backend
// — a container (Docker/Podman + cgroup) or a microVM (Firecracker/gVisor) —
// by implementing this interface and returning it from platformFence() when
// configured. Two constraints carry over unchanged:
//
//   - A container is NOT a fallback for a host whose fence failed. It depends
//     on the same kernel primitives, so it is an addition, not a spare tire;
//     the self-check stays authoritative about whether functions may run.
//   - Whichever backend is active, the capability layer and the egress bridge
//     above it are untouched: the box still cannot name a URL, and every call
//     still leaves through the proxy.
type fence interface {
	// Backend names the mechanism, e.g. "windows-appcontainer".
	Backend() string
	// Spawn creates the fenced box process.
	Spawn(req SpawnRequest) (boxProcess, error)
	// Probe reports what this platform can actually enforce right now.
	Probe() []ComponentStatus
}

func currentFence() fence { return platformFence() }

// helperPath is the funcbox executable, published by the service once it has
// resolved the binary. Probes use it to run a REAL end-to-end self-check —
// spawning the box and applying the fence — because a presence check on a
// sandbox mechanism says nothing about whether it actually confines anything.
var (
	helperPathMu sync.RWMutex
	helperPath   string
)

// SetHelperPath tells the sandbox package where the funcbox binary lives.
func SetHelperPath(p string) {
	helperPathMu.Lock()
	helperPath = p
	helperPathMu.Unlock()
	ResetSelfCheckCache()
}

// HelperPath returns the configured funcbox binary path, or "" when unset.
func HelperPath() string {
	helperPathMu.RLock()
	defer helperPathMu.RUnlock()
	return helperPath
}

// selfCheckTTL keeps the probe cheap: macOS and Windows probes create a
// throwaway process, and the UI polls this endpoint.
const selfCheckTTL = 20 * time.Second

var (
	selfCheckMu    sync.Mutex
	selfCheckCache *Availability
	override       *Availability
)

// SelfCheck reports whether operation functions may run on this host. The
// result is cached briefly; pass force to re-probe (the UI's "re-check").
func SelfCheck(force bool) Availability {
	selfCheckMu.Lock()
	defer selfCheckMu.Unlock()
	if override != nil {
		return *override
	}
	if !force && selfCheckCache != nil && time.Since(selfCheckCache.ProbedAt) < selfCheckTTL {
		return *selfCheckCache
	}
	f := currentFence()
	comps := f.Probe()
	// A missing helper is a fence failure, not a reason to skip the probe. The
	// platform probes only add their fenced-spawn check when a helper is
	// configured, so without this a build that forgot to ship the helper would
	// report a healthy fence and then fail every call with "helper not
	// configured" — a silent downgrade of exactly the kind this design exists to
	// prevent.
	if HelperPath() == "" {
		comps = append(comps, ComponentStatus{
			Name:     "sandbox-helper",
			Required: true,
			OK:       false,
			Detail:   "the niuniu-funcbox helper binary was not found next to the server",
		})
	}
	av := verdictFrom(runtime.GOOS+"/"+runtime.GOARCH, f.Backend(), comps)
	selfCheckCache = &av
	return av
}

// verdictFrom turns probed components into the self-check verdict.
//
// Two outcomes matter and they are not the same thing:
//
//   - a REQUIRED component is missing -> the fence cannot be trusted, so
//     Available is false and the host refuses to run any function;
//   - only OPTIONAL components are missing -> the fence still holds but a layer
//     of defence in depth is gone (Linux without user namespaces drops to
//     Landlock + seccomp, which #739 explicitly allows). Degraded says so, so
//     the UI can name the missing layer instead of the user quietly getting
//     less protection than the design promises.
func verdictFrom(platform, backend string, comps []ComponentStatus) Availability {
	av := Availability{
		Platform:   platform,
		Backend:    backend,
		Components: comps,
		ProbedAt:   time.Now(),
	}
	av.Available = true
	for _, c := range comps {
		if c.Required && !c.OK {
			av.Available = false
			av.Reason = "required fence component unavailable: " + c.Name
			if c.Detail != "" {
				av.Reason += " (" + c.Detail + ")"
			}
			break
		}
	}
	if av.Available {
		for _, c := range comps {
			if !c.Required && !c.OK {
				av.Degraded = true
				break
			}
		}
	}
	return av
}

// ResetSelfCheckCache drops the memoized verdict (tests).
func ResetSelfCheckCache() {
	selfCheckMu.Lock()
	selfCheckCache = nil
	selfCheckMu.Unlock()
}

// SetAvailabilityOverride forces the self-check verdict, or clears the override
// with nil.
//
// This exists because the fail-closed path is the one path a passing host can
// never exercise: "the fence is unavailable, so refuse to run" is only
// observable on a machine whose sandbox does not work. Tests use it to prove
// the refusal is real rather than assuming it. Never call it outside a test.
func SetAvailabilityOverride(a *Availability) {
	selfCheckMu.Lock()
	override = a
	selfCheckMu.Unlock()
	ResetSelfCheckCache()
}
