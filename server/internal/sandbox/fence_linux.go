//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Observed namespace refusal, recorded at clone time. It is sticky for the
// process: a kernel that refused once refuses deterministically, and re-trying
// on every call would pay a failed clone each time.
var (
	namespacesRefusedMu  sync.Mutex
	namespacesRefusedVal bool
	namespacesRefusedWhy string
)

func noteNamespacesRefused(err error) {
	namespacesRefusedMu.Lock()
	namespacesRefusedVal = true
	namespacesRefusedWhy = err.Error()
	namespacesRefusedMu.Unlock()
	ResetSelfCheckCache()
}

// namespacesRefused reports whether a clone was refused and why.
func namespacesRefused() (bool, string) {
	namespacesRefusedMu.Lock()
	defer namespacesRefusedMu.Unlock()
	return namespacesRefusedVal, namespacesRefusedWhy
}

// resetNamespacesRefused clears the observed refusal. Tests use it to exercise
// the degraded path on a host whose namespaces work; never call it outside a
// test, because it would let a real refusal be forgotten.
func resetNamespacesRefused() {
	namespacesRefusedMu.Lock()
	namespacesRefusedVal = false
	namespacesRefusedWhy = ""
	namespacesRefusedMu.Unlock()
	ResetSelfCheckCache()
}

// Linux fence: user + network + mount namespaces created by the parent, plus
// Landlock and seccomp applied by the box on itself before it evaluates any
// function code.
//
// The empty network namespace is the load-bearing part for egress WHEN it is
// available: once CLONE_NEWNET is set the box owns a fresh stack with no routes
// and no interfaces beyond a down loopback, so a direct socket cannot reach
// anything even if the interpreter were somehow bypassed. Landlock then removes
// the filesystem, and seccomp is the belt-and-braces syscall allowlist.
//
// User namespaces are OPTIONAL, per #739: distributions where unprivileged user
// namespaces are unavailable (a Debian-family sysctl, or the AppArmor
// restriction Ubuntu 23.10+ ships by default) degrade to Landlock + seccomp
// rather than refusing to run. That degradation is a weaker fence, not a
// broken one — the two remaining layers do the accepting work on their own:
//
//   - Landlock denies every filesystem access, so the read-file probe still
//     fails without a mount namespace;
//   - seccomp denies SOCKET/CONNECT outright, so the socket probe still fails
//     without a network namespace. The empty netns was defence in depth on top
//     of that, not the thing that stopped it.
//
// What is lost is the extra layer, so the self-check reports the component as
// missing and the verdict comes back Degraded — the UI names it instead of the
// user silently getting less than the design promises.
//
// Implemented but authored on Windows: the Linux acceptance run in #739 is what
// exercises it on a real host. The probe below spawns the box for real along
// whichever path Spawn takes, so a host where the fence does not actually hold
// fails the self-check instead of silently running unfenced.

type linuxFence struct{}

func platformFence() fence { return linuxFence{} }

func (linuxFence) Backend() string {
	if refused, _ := namespacesRefused(); refused || !usernsUsable() {
		return "linux-landlock-seccomp"
	}
	return "linux-namespaces-landlock-seccomp"
}

func (linuxFence) Spawn(req SpawnRequest) (boxProcess, error) {
	// Namespaces need unprivileged user namespaces to exist at all: an
	// unprivileged process cannot create a network or mount namespace without
	// first creating a user namespace. When they are unavailable the box is
	// spawned into the host namespaces and confined by Landlock + seccomp alone.
	useNamespaces := usernsUsable()
	if refused, _ := namespacesRefused(); refused {
		useNamespaces = false
	}
	p, err := spawnLinuxBox(req, useNamespaces)
	if err == nil || !useNamespaces {
		return p, err
	}
	// The sysctls said yes and the kernel said no: Ubuntu 23.10+ and its
	// derivatives restrict unprivileged user namespaces through AppArmor, which
	// no /proc/sys file reflects, so the refusal only shows up here — at the
	// clone. Degrade to Landlock + seccomp and REMEMBER it, so the self-check
	// names the missing layer instead of the degradation passing unnoticed.
	if !errors.Is(err, syscall.EPERM) {
		return nil, err
	}
	noteNamespacesRefused(err)
	return spawnLinuxBox(req, false)
}

func spawnLinuxBox(req SpawnRequest, useNamespaces bool) (boxProcess, error) {
	configure := func(cmd *exec.Cmd) (*exec.Cmd, func() error, error) {
		attr := &syscall.SysProcAttr{
			Setpgid: true,
			// The box dies with its supervisor rather than being orphaned.
			Pdeathsig: syscall.SIGKILL,
		}
		if useNamespaces {
			// New user ns (so an unprivileged process may create the others),
			// new network ns (no egress), new mount ns (no host mounts).
			attr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS
			// Map this user to root INSIDE the namespace only; outside, the box
			// is still the unprivileged user.
			attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
			attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
			// Without this an unprivileged user namespace cannot be created.
			attr.GidMappingsEnableSetgroups = false
		}
		cmd.SysProcAttr = attr
		kill := func() error {
			if cmd.Process == nil {
				return nil
			}
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		return cmd, kill, nil
	}
	return startExec(req.Path, req.Args, boxEnv(), boxWorkDir(), configure)
}

func (linuxFence) Probe() []ComponentStatus {
	comps := []ComponentStatus{
		{Name: "separate-process", Required: true, OK: true,
			Detail: "the box always runs in its own process"},
	}
	ok, detail := probeUserNamespaces()
	if refused, why := namespacesRefused(); refused {
		// The sysctls looked fine; the clone is what actually said no. Report
		// the observed fact, not the sysctl reading.
		ok, detail = false, "the kernel refused to create the namespace: "+why
	}
	// Optional: absent user namespaces degrade the fence (see the file comment)
	// rather than making it unavailable, so Required stays false and the verdict
	// comes back Degraded.
	comps = append(comps, ComponentStatus{Name: "user-namespaces", Required: false, OK: ok, Detail: detail})
	abi, llDetail := landlockABI()
	comps = append(comps, ComponentStatus{
		Name: "landlock", Required: true, OK: abi > 0, Detail: llDetail,
	})
	comps = append(comps, ComponentStatus{
		Name: "seccomp", Required: true, OK: seccompAvailable(),
		Detail: seccompDetail(),
	})
	comps = append(comps, ComponentStatus{
		Name: "memory-limit", Required: true, OK: true,
		Detail: "the supervisor samples RSS and kills the box when it exceeds the ceiling",
	})
	if p := HelperPath(); p != "" {
		ok, detail := probeSpawn(p)
		comps = append(comps, ComponentStatus{
			Name: "fenced-spawn", Required: true, OK: ok, Detail: detail,
		})
	}
	return comps
}

// usernsUsable answers the yes/no question; probeUserNamespaces supplies the
// explanation the UI shows.
//
// It is only the parent's half of the story. Ubuntu 23.10+ restricts
// unprivileged user namespaces through AppArmor rather than a sysctl, which
// these files cannot see, so a host can look enabled here and still refuse
// CLONE_NEWUSER at spawn time. The fenced-spawn probe is what settles that: it
// spawns for real and fails the fence if the box cannot be confined.
func usernsUsable() bool {
	ok, _ := probeUserNamespaces()
	return ok
}

// probeUserNamespaces checks the sysctls that gate unprivileged user
// namespaces. An unreadable sysctl is not treated as failure — the fenced-spawn
// probe is the authoritative check.
func probeUserNamespaces() (bool, string) {
	if b, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if v := trimSpace(string(b)); v == "0" {
			return false, "max_user_namespaces is 0"
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if trimSpace(string(b)) == "0" {
			return false, "unprivileged_userns_clone is disabled"
		}
	}
	return true, "unprivileged user namespaces appear enabled"
}

// probeSpawn runs the box for real under the full fence and expects a clean
// exit. It is the only probe that can catch a host where the sysctls look fine
// but namespace creation is refused by a security module.
func probeSpawn(helper string) (bool, string) {
	f := linuxFence{}
	p, err := f.Spawn(SpawnRequest{
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
	return true, "the box starts, self-fences and exits cleanly under the full fence"
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
