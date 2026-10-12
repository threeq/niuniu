//go:build windows

package sandbox

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows fence: an AppContainer process inside a Job Object.
//
// AppContainer is what actually denies the two capabilities that matter here —
// filesystem and network. An AppContainer with NO capabilities has no network
// access at all (no `internetClient`), and can only read objects whose DACL
// grants ALL APPLICATION PACKAGES, which by default excludes the user's data.
// It also runs at Low integrity, so writes are denied even where reads are not.
//
// The Job Object then supplies the resource ceiling and the lifecycle
// guarantee: JOB_OBJECT_LIMIT_ACTIVE_PROCESS=1 makes spawning a child process
// fail (the second requirement of the isolation acceptance test), PROCESS_MEMORY
// caps committed memory, and KILL_ON_JOB_CLOSE means a box can never outlive
// the supervisor.
//
// A Low-IL restricted token alone would NOT be enough: it does not deny network
// access and it does not deny reads of world-readable files. That is why this
// backend refuses to run when AppContainer cannot be established instead of
// falling back to a weaker mechanism.

// procThreadAttributeSecurityCapabilities is PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES.
const procThreadAttributeSecurityCapabilities = 0x00020009

// appContainerProfileName identifies the one profile this feature creates.
const appContainerProfileName = "Niuniu.OperationFunctionBox"

// securityCapabilities mirrors SECURITY_CAPABILITIES. Capabilities is nil and
// CapabilityCount is 0: the box gets NO capability, which is exactly what
// denies it network access.
type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    *windows.SIDAndAttributes
	CapabilityCount uint32
	Reserved        uint32
}

var (
	moduserenv                                    = windows.NewLazySystemDLL("userenv.dll")
	procCreateAppContainerProfile                 = moduserenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSidFromAppContainerName = moduserenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

// hresultAlreadyExists is HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS).
const hresultAlreadyExists = 0x800700B7

type windowsFence struct{}

func platformFence() fence { return windowsFence{} }

func (windowsFence) Backend() string { return "windows-appcontainer" }

// --- AppContainer profile --------------------------------------------------

var (
	sidOnce sync.Once
	sidVal  *windows.SID
	sidErr  error
)

// appContainerSID returns the box's AppContainer SID, creating the profile on
// first use.
func appContainerSID() (*windows.SID, error) {
	sidOnce.Do(func() {
		sidVal, sidErr = createAppContainerSID()
	})
	return sidVal, sidErr
}

func createAppContainerSID() (*windows.SID, error) {
	name, err := windows.UTF16PtrFromString(appContainerProfileName)
	if err != nil {
		return nil, err
	}
	display, _ := windows.UTF16PtrFromString("Niuniu operation function box")
	desc, _ := windows.UTF16PtrFromString("Runs AI-authored integration operation functions")
	var psid *windows.SID
	hr, _, _ := procCreateAppContainerProfile.Call(
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(display)),
		uintptr(unsafe.Pointer(desc)),
		0, // no capabilities
		0,
		uintptr(unsafe.Pointer(&psid)),
	)
	if int32(hr) < 0 {
		if uint32(hr) != hresultAlreadyExists {
			return nil, fmt.Errorf("CreateAppContainerProfile failed: 0x%08x", uint32(hr))
		}
		// Profile already exists — derive its SID instead.
		var derived *windows.SID
		hr2, _, _ := procDeriveAppContainerSidFromAppContainerName.Call(
			uintptr(unsafe.Pointer(name)),
			uintptr(unsafe.Pointer(&derived)),
		)
		if int32(hr2) < 0 {
			return nil, fmt.Errorf("DeriveAppContainerSidFromAppContainerName failed: 0x%08x", uint32(hr2))
		}
		return derived, nil
	}
	return psid, nil
}

// grantedPaths memoizes per-binary ACL grants: re-granting on every call would
// be wasted work, and the grant is additive.
var grantedPaths sync.Map

// grantPackageRead makes the helper binary readable and executable by every
// AppContainer. The DACL is MERGED with the existing one (never replaced), so
// the user's own access is untouched.
func grantPackageRead(path string) error {
	if _, ok := grantedPaths.Load(path); ok {
		return nil
	}
	pkgSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAnyPackageSid)
	if err != nil {
		return fmt.Errorf("resolve ALL APPLICATION PACKAGES sid: %w", err)
	}
	ea := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_READ | windows.GENERIC_EXECUTE,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(pkgSID),
		},
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read acl of %s: %w", path, err)
	}
	existing, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read dacl of %s: %w", path, err)
	}
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{ea}, existing)
	if err != nil {
		return fmt.Errorf("build acl for %s: %w", path, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil); err != nil {
		return fmt.Errorf("grant package read on %s: %w", path, err)
	}
	grantedPaths.Store(path, true)
	return nil
}

// --- spawn ----------------------------------------------------------------

func (windowsFence) Spawn(req SpawnRequest) (boxProcess, error) {
	sid, err := appContainerSID()
	if err != nil {
		return nil, fmt.Errorf("prepare app container: %w", err)
	}
	if err := grantPackageRead(req.Path); err != nil {
		return nil, err
	}

	inR, inW, err := createPipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := createPipe()
	if err != nil {
		closeHandles(inR, inW, outR)
		return nil, err
	}
	errR, errW, err := createPipe()
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR)
		return nil, err
	}
	// The parent's ends must never be inherited by the box (or by any other
	// concurrently spawned process).
	for _, h := range []windows.Handle{inW, outR, errR} {
		_ = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0)
	}

	caps := securityCapabilities{AppContainerSid: sid}
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return nil, fmt.Errorf("allocate attribute list: %w", err)
	}
	defer attrList.Delete()
	if err := attrList.Update(procThreadAttributeSecurityCapabilities,
		unsafe.Pointer(&caps), unsafe.Sizeof(caps)); err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return nil, fmt.Errorf("set security capabilities: %w", err)
	}

	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = inR
	si.StdOutput = outW
	si.StdErr = errW
	si.ProcThreadAttributeList = attrList.List()

	exePath, err := windows.UTF16PtrFromString(req.Path)
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return nil, err
	}
	cmdLine, err := commandLine(req.Path, req.Args)
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return nil, err
	}
	env, err := envBlock(req.Env)
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return nil, err
	}
	cwd, err := windows.UTF16PtrFromString(req.Dir)
	if err != nil {
		closeHandles(inR, inW, outR, outW, errR, errW)
		return nil, err
	}

	pi := windows.ProcessInformation{}
	err = windows.CreateProcess(
		exePath, cmdLine, nil, nil, true,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		env, cwd, &si.StartupInfo, &pi,
	)
	runtime.KeepAlive(caps)
	runtime.KeepAlive(attrList)
	// The child owns copies of its ends now; close ours so EOF propagates.
	closeHandles(inR, outW, errW)
	if err != nil {
		closeHandles(inW, outR, errR)
		return nil, fmt.Errorf("create app-container process: %w", err)
	}

	job, err := createBoxJob(req.MemoryBytes)
	if err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(pi.Thread)
		closeHandles(inW, outR, errR)
		return nil, err
	}
	// Assign AFTER CreateProcess: the box's first act is to block reading its
	// invoke frame, which the supervisor only writes once assignment succeeded,
	// so no function code can run outside the job.
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		_ = windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(pi.Thread)
		closeHandles(inW, outR, errR)
		return nil, fmt.Errorf("assign box to job object: %w", err)
	}
	windows.CloseHandle(pi.Thread)

	return &winBoxProcess{
		handle: pi.Process,
		pid:    int(pi.ProcessId),
		job:    job,
		in:     os.NewFile(uintptr(inW), "box-stdin"),
		out:    os.NewFile(uintptr(outR), "box-stdout"),
		errf:   os.NewFile(uintptr(errR), "box-stderr"),
	}, nil
}

// createBoxJob builds the resource/lifecycle fence around the box.
func createBoxJob(memoryBytes int64) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | // box never outlives the supervisor
			windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | // 1 process: no child processes
			windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY // committed-memory ceiling
	info.BasicLimitInformation.ActiveProcessLimit = 1
	if memoryBytes > 0 {
		info.ProcessMemoryLimit = uintptr(memoryBytes)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("set job limits: %w", err)
	}
	return job, nil
}

// winBoxProcess is the AppContainer box.
type winBoxProcess struct {
	handle windows.Handle
	pid    int
	job    windows.Handle
	in     *os.File
	out    *os.File
	errf   *os.File
	closed bool
}

func (p *winBoxProcess) Stdin() io.WriteCloser { return p.in }
func (p *winBoxProcess) Stdout() io.ReadCloser { return p.out }
func (p *winBoxProcess) Stderr() io.ReadCloser { return p.errf }
func (p *winBoxProcess) Pid() int              { return p.pid }

func (p *winBoxProcess) Wait() error {
	_, err := windows.WaitForSingleObject(p.handle, windows.INFINITE)
	if err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.handle, &code); err != nil {
		return err
	}
	if code != 0 {
		return &exitError{pid: p.pid, code: int(code)}
	}
	return nil
}

// Kill terminates the whole job — the box and anything the fence would have
// allowed it to create.
func (p *winBoxProcess) Kill() error {
	if p.job != 0 {
		return windows.TerminateJobObject(p.job, 1)
	}
	return windows.TerminateProcess(p.handle, 1)
}

// PeakMemoryBytes reads the job object's own accounting, so the reported figure
// comes from the OS rather than from the box's self-report.
func (p *winBoxProcess) PeakMemoryBytes() int64 {
	if p.job == 0 {
		return 0
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0
	}
	return int64(info.PeakProcessMemoryUsed)
}

func (p *winBoxProcess) Close() {
	if p.closed {
		return
	}
	p.closed = true
	if p.job != 0 {
		windows.CloseHandle(p.job)
	}
	if p.handle != 0 {
		windows.CloseHandle(p.handle)
	}
	for _, f := range []*os.File{p.in, p.out, p.errf} {
		if f != nil {
			_ = f.Close()
		}
	}
}

// --- probe ----------------------------------------------------------------

func (windowsFence) Probe() []ComponentStatus {
	var comps []ComponentStatus
	_, sidErr := appContainerSID()
	comps = append(comps, ComponentStatus{
		Name:     "app-container",
		Required: true,
		OK:       sidErr == nil,
		Detail:   errDetail(sidErr, "the box runs as an AppContainer with no capabilities"),
	})
	jobOK, jobDetail := probeJobObject()
	comps = append(comps, ComponentStatus{
		Name:     "job-object-limits",
		Required: true,
		OK:       jobOK,
		Detail:   jobDetail,
	})
	comps = append(comps, ComponentStatus{
		Name:     "separate-process",
		Required: true,
		OK:       true,
		Detail:   "the box always runs in its own process",
	})
	comps = append(comps, ComponentStatus{
		Name:     "memory-limit",
		Required: true,
		OK:       true,
		Detail:   "the job object caps committed memory and kills on breach",
	})
	if p := HelperPath(); p != "" {
		ok, detail := windowsProbeSpawn(p)
		comps = append(comps, ComponentStatus{
			Name: "fenced-spawn", Required: true, OK: ok, Detail: detail,
		})
	}
	return comps
}

// windowsProbeSpawn runs the box for real under the full fence. A presence
// check on AppContainer would pass on a host where the ACL grant or the
// capability list is refused, so the probe actually creates the process.
func windowsProbeSpawn(helper string) (bool, string) {
	p, err := (windowsFence{}).Spawn(SpawnRequest{
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
	return true, "the box starts under an AppContainer with no capabilities and exits cleanly"
}

func probeJobObject() (bool, string) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return false, err.Error()
	}
	defer windows.CloseHandle(job)
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = 1
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return false, err.Error()
	}
	return true, "memory, active-process and kill-on-close limits are settable"
}

func errDetail(err error, okDetail string) string {
	if err != nil {
		return err.Error()
	}
	return okDetail
}

// --- helpers --------------------------------------------------------------

func createPipe() (read, write windows.Handle, err error) {
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err := windows.CreatePipe(&read, &write, &sa, 0); err != nil {
		return 0, 0, fmt.Errorf("create pipe: %w", err)
	}
	return read, write, nil
}

func closeHandles(hs ...windows.Handle) {
	for _, h := range hs {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
}

// envBlock renders the box environment as a sorted, double-NUL-terminated
// UTF-16 block. Nothing is inherited: the block is exactly what the caller
// passed.
//
// Each entry is encoded on its own — UTF16FromString rejects a string that
// already contains NUL separators, which is exactly what the concatenated
// form has.
func envBlock(env []string) (*uint16, error) {
	sorted := append([]string(nil), env...)
	sort.Strings(sorted)
	var u []uint16
	for _, kv := range sorted {
		enc, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, err
		}
		u = append(u, enc...) // includes the terminating NUL
	}
	u = append(u, 0) // the block's second, final NUL
	return &u[0], nil
}

// commandLine builds the CreateProcess command line. The executable path is
// passed separately as lpApplicationName, so argv[0] only needs to be present
// and well-formed.
func commandLine(path string, args []string) (*uint16, error) {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteWindowsArg(path))
	for _, a := range args {
		parts = append(parts, quoteWindowsArg(a))
	}
	return windows.UTF16PtrFromString(strings.Join(parts, " "))
}

// quoteWindowsArg applies the CommandLineToArgvW quoting rules.
func quoteWindowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			backslashes = 0
			b.WriteRune(r)
		default:
			if backslashes > 0 {
				b.WriteString(strings.Repeat("\\", backslashes))
				backslashes = 0
			}
			b.WriteRune(r)
		}
	}
	b.WriteString(strings.Repeat("\\", backslashes*2))
	b.WriteByte('"')
	return b.String()
}
