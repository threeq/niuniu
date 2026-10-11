//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Linux fence: user + network + mount namespaces created by the parent, plus
// Landlock and seccomp applied by the box on itself before it evaluates any
// function code.
//
// The empty network namespace is the load-bearing part for egress: once
// CLONE_NEWNET is set the box owns a fresh stack with no routes and no
// interfaces beyond a down loopback, so a direct socket cannot reach anything
// even if the interpreter were somehow bypassed. Landlock then removes the
// filesystem, and seccomp is the belt-and-braces syscall allowlist.
//
// Implemented but authored on Windows: the Linux acceptance run in #739 is what
// exercises it on a real host. The probe below runs the box for real, so a host
// where any of this is unavailable fails the self-check instead of silently
// running with a weaker fence.

type linuxFence struct{}

func platformFence() fence { return linuxFence{} }

func (linuxFence) Backend() string { return "linux-namespaces-landlock-seccomp" }

func (linuxFence) Spawn(req SpawnRequest) (boxProcess, error) {
	configure := func(cmd *exec.Cmd) (*exec.Cmd, func() error, error) {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			// New user ns (so an unprivileged process may create the others),
			// new network ns (no egress), new mount ns (no host mounts).
			Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
			// Map this user to root INSIDE the namespace only; outside, the box
			// is still the unprivileged user.
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
			// Without this an unprivileged user namespace cannot be created.
			GidMappingsEnableSetgroups: false,
			Setpgid:                    true,
			// The box dies with its supervisor rather than being orphaned.
			Pdeathsig: syscall.SIGKILL,
		}
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
	comps = append(comps, ComponentStatus{Name: "user-namespaces", Required: true, OK: ok, Detail: detail})
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
