//go:build darwin

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// macOS fence: the box is launched through `sandbox-exec` with a Seatbelt
// profile that denies by default, denies all networking, and allows only the
// stdio the supervisor needs.
//
// This backend is implemented but was authored on Windows and therefore has NOT
// been exercised on a real macOS host — the #739 acceptance run on each desktop
// platform is what closes that gap. Because a profile that is too strict would
// make the box unrunnable, the probe executes a trivial command through
// sandbox-exec first and the host refuses to run functions if that fails:
// a fence that cannot be established is reported, never worked around.
//
// Known risk, recorded here because it is a platform fact rather than a bug:
// sandbox-exec is deprecated and has been restricted on recent macOS releases.
// When it is unavailable the probe fails closed. The documented stronger
// alternative — a helper signed with App Sandbox entitlements — is not
// implemented in this build.

type darwinFence struct{}

func platformFence() fence { return darwinFence{} }

func (darwinFence) Backend() string { return "darwin-seatbelt" }

// sandboxExecPath is where sandbox-exec lives on every macOS release that has
// it. Absence is a probe failure, not a fallback trigger.
const sandboxExecPath = "/usr/bin/sandbox-exec"

func (darwinFence) Spawn(req SpawnRequest) (boxProcess, error) {
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return nil, fmt.Errorf("sandbox-exec is unavailable: %w", err)
	}
	profile := seatbeltProfile(req.Path)
	args := append([]string{"-p", profile, req.Path}, req.Args...)
	// The command is executed through sandbox-exec, so the wrapper is the
	// direct child and the box is its child: killing the process group is what
	// keeps a crashed supervisor from leaving the box behind.
	configure := func(cmd *exec.Cmd) (*exec.Cmd, func() error, error) {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		kill := func() error {
			if cmd.Process == nil {
				return nil
			}
			// Negative pid = the whole process group.
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		return cmd, kill, nil
	}
	return startExec(sandboxExecPath, args, boxEnv(), boxWorkDir(), configure)
}

// seatbeltProfile renders the SBPL profile. Deny-by-default plus an explicit
// "deny network*" is the load-bearing part: it is what makes a direct socket
// attempt fail at the OS rather than inside the interpreter, where the function
// has no socket API anyway.
func seatbeltProfile(helperPath string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(deny network*)\n")
	// Process: sandbox-exec's own image, and the helper BY PATH. The helper is
	// named both as given and as its symlink-resolved self, because Seatbelt
	// matches the resolved path and a literal covering only one of the two would
	// make the box fail to start at all. Resolving here is what buys the
	// robustness — deliberately NOT a (subpath ...) of the helper's directory,
	// which would also authorize exec of the sibling niuniu-server binary.
	pathLiterals := helperPathLiterals(helperPath)
	b.WriteString("(allow process-exec (literal \"")
	b.WriteString(sbplEscape(sandboxExecPath))
	b.WriteString("\")")
	for _, p := range pathLiterals {
		b.WriteString(" (literal \"")
		b.WriteString(sbplEscape(p))
		b.WriteString("\")")
	}
	b.WriteString(")\n")
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow signal (target self))\n")
	// The Go runtime queries the CPU count and needs entropy; both are reads.
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow file-read* (literal \"/dev/urandom\") (literal \"/dev/null\"))\n")
	// Loading the helper's own image and the system dylibs it links against.
	b.WriteString("(allow file-read*")
	for _, p := range pathLiterals {
		b.WriteString(" (literal \"")
		b.WriteString(sbplEscape(p))
		b.WriteString("\")")
	}
	b.WriteString(" (subpath \"/usr/lib\") (subpath \"/System/Library\"))\n")
	// Timezone data. A function that formats a local time would otherwise fail
	// with a permission error that says nothing about timezones — a confusing
	// failure for a call that is entirely legitimate. Read-only, and the zone
	// database holds nothing user-specific.
	b.WriteString("(allow file-read* (literal \"/etc/localtime\") (subpath \"/usr/share/zoneinfo\") (subpath \"/var/db/timezone\"))\n")
	// stdio only, and only writing to it.
	b.WriteString("(allow file-write-data (literal \"/dev/null\") (literal \"/dev/stdout\") (literal \"/dev/stderr\") (literal \"/dev/fd/1\") (literal \"/dev/fd/2\"))\n")
	b.WriteString("(allow file-read-metadata)\n")
	return b.String()
}

// helperPathLiterals returns the path itself, plus its symlink-resolved form
// when the two differ. Both are needed: the caller names the path it holds,
// while Seatbelt matches whatever the kernel resolved.
func helperPathLiterals(helperPath string) []string {
	out := []string{helperPath}
	if resolved, err := filepath.EvalSymlinks(helperPath); err == nil && resolved != helperPath {
		out = append(out, resolved)
	}
	return out
}

func sbplEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\"")
}

func (darwinFence) Probe() []ComponentStatus {
	comps := []ComponentStatus{
		{Name: "separate-process", Required: true, OK: true,
			Detail: "the box always runs in its own process"},
	}
	if _, err := os.Stat(sandboxExecPath); err != nil {
		comps = append(comps, ComponentStatus{
			Name: "seatbelt-sandbox-exec", Required: true, OK: false,
			Detail: "sandbox-exec not found at " + sandboxExecPath,
		})
	} else {
		comps = append(comps, ComponentStatus{
			Name: "seatbelt-sandbox-exec", Required: true, OK: true,
			Detail: "sandbox-exec present; a sealed profile denies network and filesystem access",
		})
	}
	// The only honest check is running one: a present-but-restricted
	// sandbox-exec would otherwise pass a presence check and fail every call.
	ok, detail := probeSeatbelt()
	comps = append(comps, ComponentStatus{
		Name: "seatbelt-profile-applies", Required: true, OK: ok, Detail: detail,
	})
	// Memory is the one ceiling Seatbelt does not express and RLIMIT_AS cannot
	// supply for a Go binary, so there is no in-KERNEL hard cap here. The
	// runtime can still OBSERVE and KILL: the supervisor samples RSS (see
	// rss_darwin.go) every 100ms and terminates a box that exceeds its declared
	// ceiling, exactly as the Linux watchdog does. This component stays a
	// deliberate HARDENING-GAP disclosure — it reports the missing kernel cap —
	// rather than over-claiming a hard limit; the sampled kill is documented in
	// the detail so the stronger-than-before reality is not hidden either way.
	comps = append(comps, ComponentStatus{
		Name:     "memory-limit",
		Required: false,
		OK:       false,
		Detail:   "no in-kernel hard memory cap on this platform (Seatbelt expresses none; RLIMIT_AS cannot be used for a Go binary); the supervisor samples RSS and kills the box when it exceeds the ceiling",
	})
	if p := HelperPath(); p != "" {
		ok, detail := darwinProbeSpawn(p)
		comps = append(comps, ComponentStatus{
			Name: "fenced-spawn", Required: true, OK: ok, Detail: detail,
		})
	}
	return comps
}

func darwinProbeSpawn(helper string) (bool, string) {
	p, err := (darwinFence{}).Spawn(SpawnRequest{
		Path:        helper,
		Args:        []string{"--probe"},
		Env:         boxEnv(),
		Dir:         boxWorkDir(),
		MemoryBytes: 64 << 20,
	})
	if err != nil {
		return false, err.Error()
	}
	defer func() {
		_ = p.Kill()
		p.Close()
	}()
	_ = p.Stdin().Close()
	if err := p.Wait(); err != nil {
		return false, fmt.Sprintf("fenced box exited abnormally: %v", err)
	}
	return true, "the box starts inside a deny-default Seatbelt profile and exits cleanly"
}

func probeSeatbelt() (bool, string) {
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return false, "sandbox-exec not found"
	}
	cmd := exec.Command(sandboxExecPath, "-p",
		"(version 1)(deny default)(allow process-exec (literal \"/usr/bin/true\"))(allow file-read*)(allow sysctl-read)",
		"/usr/bin/true")
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Sprintf("sandbox-exec refused the probe profile: %v %s", err, strings.TrimSpace(string(out)))
	}
	return true, "a deny-default profile executes successfully"
}
