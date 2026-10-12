//go:build linux

package sandbox

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The box's own half of the Linux fence. The parent creates the namespaces;
// these two mechanisms the box applies to ITSELF, before it evaluates a single
// line of function code, so there is no window in which untrusted code runs
// unrestricted.
//
//   - Landlock removes the filesystem: a ruleset that handles every access
//     right but contains no rule grants nothing, so any open/exec/truncate
//     fails with EACCES for the rest of the process's life. Unlike seccomp it
//     needs no syscall-number list, and unlike a chroot it needs no privilege.
//   - seccomp-bpf closes the syscalls Landlock does not cover — sockets,
//     process creation, ptrace, kernel-module and namespace manipulation.
//
// Neither is allowed to fail silently: ApplySelfFence returns an error and the
// box reports it, and the host's self-check runs the same path.

// Landlock syscall numbers are identical on amd64 and arm64 (they sit in the
// arch-independent range), so they are spelled out rather than derived.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1
)

// Landlock access rights, accumulated per ABI version. A ruleset that claims a
// right the running kernel does not know is rejected with EINVAL, so the set is
// masked down to what landlockABI reports.
const (
	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llReadFile   = 1 << 2
	llReadDir    = 1 << 3
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13
	llTruncate   = 1 << 14
	llIoctlDev   = 1 << 15
)

func landlockAccessMask(abi int) uint64 {
	mask := uint64(llExecute | llWriteFile | llReadFile | llReadDir | llRemoveDir |
		llRemoveFile | llMakeChar | llMakeDir | llMakeReg | llMakeSock |
		llMakeFifo | llMakeBlock | llMakeSym)
	if abi >= 2 {
		mask |= llRefer
	}
	if abi >= 3 {
		mask |= llTruncate
	}
	if abi >= 5 {
		mask |= llIoctlDev
	}
	return mask
}

// landlockABI reports the Landlock ABI version, or 0 when Landlock is absent.
func landlockABI() (int, string) {
	abi, _, errno := unix.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		if errno == unix.ENOSYS {
			return 0, "Landlock is not available in this kernel (needs 5.13+)"
		}
		return 0, fmt.Sprintf("Landlock probe failed: %v", errno)
	}
	return int(abi), fmt.Sprintf("Landlock ABI v%d", abi)
}

// applyLandlock installs the deny-everything filesystem ruleset.
func applyLandlock() error {
	abi, detail := landlockABI()
	if abi == 0 {
		return fmt.Errorf("landlock unavailable: %s", detail)
	}
	attr := struct {
		HandledAccessFS  uint64
		HandledAccessNet uint64
	}{HandledAccessFS: landlockAccessMask(abi)}
	size := unsafe.Sizeof(attr.HandledAccessFS)
	if abi >= 4 {
		size = unsafe.Sizeof(attr)
	}
	fd, _, errno := unix.Syscall(sysLandlockCreateRuleset,
		uintptr(unsafe.Pointer(&attr)), size, 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer unix.Close(int(fd))
	// No rule is added: the ruleset handles every right and grants none.
	if _, _, errno := unix.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

// --- seccomp --------------------------------------------------------------

const (
	prSetNoNewPrivs   = 38
	prSetSeccomp      = 22
	seccompModeFilter = 2

	seccompRetAllow       = 0x7fff0000
	seccompRetErrno       = 0x00050000
	seccompRetKillProcess = 0x80000000

	bpfLD  = 0x00
	bpfW   = 0x00
	bpfABS = 0x20
	bpfJMP = 0x05
	bpfJEQ = 0x10
	bpfK   = 0x00
	bpfRET = 0x06
)

type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

type sockFprog struct {
	Len    uint16
	_      [6]byte
	Filter *sockFilter
}

func seccompAvailable() bool { return true }

func seccompDetail() string {
	return "a syscall filter denies sockets, process creation, ptrace, module and namespace operations"
}

// seccompDeniedCommon is the arch-independent denylist. Everything not listed
// stays allowed so
// the Go runtime's scheduler, GC and allocator keep working; the denylist
// covers capabilities the box has no legitimate use for.
var seccompDeniedCommon = []uintptr{
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND,
	unix.SYS_LISTEN, unix.SYS_ACCEPT, unix.SYS_ACCEPT4,
	unix.SYS_SENDTO, unix.SYS_RECVFROM, unix.SYS_SENDMSG, unix.SYS_RECVMSG,
	unix.SYS_SENDMMSG, unix.SYS_RECVMMSG, unix.SYS_SHUTDOWN,
	unix.SYS_GETSOCKOPT, unix.SYS_SETSOCKOPT, unix.SYS_GETSOCKNAME, unix.SYS_GETPEERNAME,
	unix.SYS_EXECVE, unix.SYS_EXECVEAT,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
	unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_MOUNT_SETATTR,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT,
	unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
	unix.SYS_KEXEC_LOAD, unix.SYS_REBOOT, unix.SYS_SWAPON, unix.SYS_SWAPOFF,
	unix.SYS_SETHOSTNAME, unix.SYS_SETDOMAINNAME, unix.SYS_ACCT, unix.SYS_QUOTACTL,
	unix.SYS_FANOTIFY_INIT, unix.SYS_KILL, unix.SYS_TKILL,
}

// auditArch is the AUDIT_ARCH_* value seccomp reports for this build.
func buildAuditArch() uint32 {
	switch runtime.GOARCH {
	case "amd64":
		return 0xC000003E // AUDIT_ARCH_X86_64
	case "arm64":
		return 0xC00000B7 // AUDIT_ARCH_AARCH64
	default:
		return 0
	}
}

// setNoNewPrivs sets the flag Landlock AND seccomp both require.
//
// Landlock's restrict_self refuses with EPERM unless no_new_privs is already
// set or the caller holds CAP_SYS_ADMIN. Inside a user namespace the box is
// uid 0 and has that capability, so the ordering mistake went unnoticed there;
// unprivileged — which is what the degraded Linux path is — it is a hard
// failure. Setting it once, first, is what makes both layers work without
// privileges.
func setNoNewPrivs() error {
	if _, _, errno := unix.RawSyscall(unix.SYS_PRCTL, prSetNoNewPrivs, 1, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	return nil
}

// applySeccomp installs the syscall filter. It requires no_new_privs to have
// been set already (see ApplySelfFence), which is what lets the filter be
// installed without CAP_SYS_ADMIN.
func applySeccomp() error {
	arch := buildAuditArch()
	if arch == 0 {
		return fmt.Errorf("no seccomp architecture filter is defined for %s", runtime.GOARCH)
	}
	prog := buildSeccompProgram(arch)
	if _, _, errno := unix.RawSyscall(unix.SYS_PRCTL, prSetSeccomp, seccompModeFilter,
		uintptr(unsafe.Pointer(&prog))); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_SECCOMP): %w", errno)
	}
	return nil
}

// seccompDenied is the full denylist for this build: the arch-independent set
// plus whatever only exists on this architecture (seccompDeniedArchList).
func seccompDenied() []uintptr {
	return append(append([]uintptr{}, seccompDeniedCommon...), seccompDeniedArchList()...)
}

// buildSeccompProgram assembles the BPF program described in seccompDenied.
func buildSeccompProgram(auditArch uint32) sockFprog {
	filters := make([]sockFilter, 0, 4+2*len(seccompDenied())+1)
	// Reject any other architecture outright: a filter written for x86_64
	// syscall numbers means nothing for a 32-bit compat call, and letting one
	// through would hand back the syscalls just denied.
	filters = append(filters,
		sockFilter{Code: bpfLD | bpfW | bpfABS, K: 4},                 // arch
		sockFilter{Code: bpfJMP | bpfJEQ | bpfK, Jt: 1, K: auditArch}, // == arch -> skip kill
		sockFilter{Code: bpfRET | bpfK, K: seccompRetKillProcess},
		sockFilter{Code: bpfLD | bpfW | bpfABS, K: 0}, // syscall nr
	)
	for _, nr := range seccompDenied() {
		filters = append(filters,
			sockFilter{Code: bpfJMP | bpfJEQ | bpfK, Jt: 0, Jf: 1, K: uint32(nr)},
			sockFilter{Code: bpfRET | bpfK, K: seccompRetErrno | uint32(unix.EPERM)},
		)
	}
	filters = append(filters, sockFilter{Code: bpfRET | bpfK, K: seccompRetAllow})
	return sockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
}

// ApplySelfFence is the box's startup hardening. It must run BEFORE any
// function code is evaluated; the helper calls it on boot.
//
// Order matters: no_new_privs first (Landlock's restrict_self refuses without
// it when the box is not privileged), then the filesystem rules, then the
// syscall filter. The last two are also ordered deliberately — once seccomp is
// installed, the syscalls Landlock needs are already gone.
func ApplySelfFence(memoryMB int) error {
	if err := setNoNewPrivs(); err != nil {
		return err
	}
	if err := applyLandlock(); err != nil {
		return err
	}
	if err := applySeccomp(); err != nil {
		return err
	}
	return applyCPULimit(memoryMB)
}

// applyCPULimit bounds CPU seconds. RLIMIT_AS is deliberately NOT used: the Go
// runtime reserves a large virtual arena, so an address-space limit sized for
// the function's real memory need would abort the box on startup. Memory is
// enforced by the supervisor's RSS watchdog instead (see runner.go).
func applyCPULimit(_ int) error {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CPU, &lim); err != nil {
		return err
	}
	cpu := uint64(30)
	lim.Cur = cpu
	lim.Max = cpu
	if err := unix.Setrlimit(unix.RLIMIT_CPU, &lim); err != nil {
		return fmt.Errorf("setrlimit(RLIMIT_CPU): %w", err)
	}
	// A core dump of a fenced process is pure liability.
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
	return nil
}
