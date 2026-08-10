package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbe_AllToolsFound_Linux(t *testing.T) {
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"python3": "/usr/bin/python3",
		"git":     "/usr/bin/git",
		"claude":  "/usr/local/bin/claude",
		"apt-get": "/usr/bin/apt-get",
	}, map[string]string{
		"node":    "v20.11.0\n",
		"python3": "Python 3.11.6\n",
		"git":     "git version 2.43.0\n",
		"claude":  "0.2.45\n",
	}, "linux")

	info := svc.Probe(context.Background())

	if info.Platform != "linux" {
		t.Fatalf("platform: got %q want linux", info.Platform)
	}
	if info.PackageManager != "apt-get" {
		t.Fatalf("pm: got %q want apt-get", info.PackageManager)
	}
	if !info.CanInstall {
		t.Fatalf("CanInstall: got false want true")
	}
	// toolNames is node, python3, git, claude, codex (codex added 2026-05-19
	// alongside Codex CLI support). Missing codex from the LookPath map
	// surfaces as Tools[i].Found=false rather than a missing entry.
	if len(info.Tools) != len(toolNames) {
		t.Fatalf("tools count: got %d want %d", len(info.Tools), len(toolNames))
	}
	if info.Tools[0].Name != "node" || !info.Tools[0].Found || info.Tools[0].Version != "v20.11.0" {
		t.Fatalf("node tool: %+v", info.Tools[0])
	}
}

func TestProbe_MissingTool_VersionEmpty(t *testing.T) {
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"git":     "/usr/bin/git",
		"apt-get": "/usr/bin/apt-get",
	}, map[string]string{
		"node": "v20.11.0\n",
		"git":  "git version 2.43.0\n",
	}, "linux")

	info := svc.Probe(context.Background())
	for _, tool := range info.Tools {
		if tool.Name == "python3" {
			if tool.Found || tool.Version != "" {
				t.Fatalf("python3 should be missing: %+v", tool)
			}
			return
		}
	}
	t.Fatalf("python3 not present in result")
}

func TestProbe_VersionTimeout_TreatedAsFound(t *testing.T) {
	// Found via LookPath but --version exits with error → Found=true, Version=""
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"python3": "/usr/bin/python3",
		"git":     "/usr/bin/git",
		"claude":  "/usr/local/bin/claude",
		"apt-get": "/usr/bin/apt-get",
	}, map[string]string{
		"node":    "v20.11.0\n",
		"python3": "Python 3.11.6\n",
		"git":     "git version 2.43.0\n",
		// claude --version errors out — no key
	}, "linux")
	svc.versionErrors = map[string]error{"claude": errors.New("exit 1")}

	info := svc.Probe(context.Background())
	for _, tool := range info.Tools {
		if tool.Name == "claude" {
			if !tool.Found {
				t.Fatalf("claude should still be Found (LookPath succeeded)")
			}
			if tool.Version != "" {
				t.Fatalf("claude version should be empty on --version error, got %q", tool.Version)
			}
			return
		}
	}
}

func TestProbe_Python3FallsBackToPython_OnWindows(t *testing.T) {
	svc := newTestService(map[string]string{
		"node":   `C:\Program Files\nodejs\node.exe`,
		"python": `C:\Python313\python.exe`, // python3 not found, python is
		"git":   `C:\Program Files\Git\cmd\git.exe`,
		"claude": `C:\Users\me\AppData\npm\claude.cmd`,
		"winget": `C:\winget.exe`,
	}, map[string]string{
		"node":   "v20.11.0\n",
		"python": "Python 3.13.0\n",
		"git":    "git version 2.43.0\n",
		"claude": "0.2.45\n",
	}, "windows")

	info := svc.Probe(context.Background())
	for _, tool := range info.Tools {
		if tool.Name == "python3" {
			if !tool.Found || tool.Version != "Python 3.13.0" {
				t.Fatalf("python3 fallback to python failed: %+v", tool)
			}
			return
		}
	}
	t.Fatalf("python3 missing")
}

func TestProbe_TeamEdition_DisablesCanInstall(t *testing.T) {
	t.Setenv("NIUNIU_EDITION", "team")
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"python3": "/usr/bin/python3",
		"git":     "/usr/bin/git",
		"claude":  "/usr/local/bin/claude",
		"apt-get": "/usr/bin/apt-get",
	}, map[string]string{
		"node":    "v20.11.0\n",
		"python3": "Python 3.11.6\n",
		"git":     "git version 2.43.0\n",
		"claude":  "0.2.45\n",
	}, "linux")

	info := svc.Probe(context.Background())
	if info.CanInstall {
		t.Fatalf("CanInstall must be false in team edition")
	}
}

func TestProbe_NoPackageManager_DisablesCanInstall(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"python3": "/usr/bin/python3",
		"git":     "/usr/bin/git",
		"claude":  "/usr/local/bin/claude",
		// no apt-get
	}, map[string]string{
		"node":    "v20.11.0\n",
		"python3": "Python 3.11.6\n",
		"git":     "git version 2.43.0\n",
		"claude":  "0.2.45\n",
	}, "linux")

	info := svc.Probe(context.Background())
	if info.PackageManager != "" {
		t.Fatalf("package_manager should be empty, got %q", info.PackageManager)
	}
	if info.CanInstall {
		t.Fatalf("CanInstall must be false when no package manager")
	}
}

// TestProbe_PerToolInstallable_ClaudeNeedsOnlyNpm guards the LTSC-without-winget
// case: when an OS package manager is missing but npm is on PATH, claude is
// still installable via `npm install -g @anthropic-ai/claude-code` while node /
// python3 / git are not. The page-level CanInstall stays false because there is
// no system PM, but per-tool Installable must reflect the npm path for claude.
func TestProbe_PerToolInstallable_ClaudeNeedsOnlyNpm(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"node": `C:\Program Files\nodejs\node.exe`,
		"npm":  `C:\Program Files\nodejs\npm.cmd`,
		// no winget, no python3, no git, no claude
	}, map[string]string{
		"node": "v20.11.0\n",
	}, "windows")

	info := svc.Probe(context.Background())
	if info.PackageManager != "" {
		t.Fatalf("PackageManager: got %q want empty (no winget on this LTSC fixture)", info.PackageManager)
	}
	if info.CanInstall {
		t.Fatalf("CanInstall must remain false when no system PM (page-level signal)")
	}

	got := map[string]bool{}
	for _, tool := range info.Tools {
		got[tool.Name] = tool.Installable
	}
	if !got["claude"] {
		t.Fatalf("claude.Installable must be true when npm is present (got false)")
	}
	for _, name := range []string{"node", "python3", "git"} {
		if got[name] {
			t.Fatalf("%s.Installable must be false without a system PM (got true)", name)
		}
	}
}

// TestProbe_PerToolInstallable_HappyPath asserts every tool is installable on a
// fully-equipped Linux fixture (apt-get + npm available).
func TestProbe_PerToolInstallable_HappyPath(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"npm":     "/usr/bin/npm",
		"python3": "/usr/bin/python3",
		"git":     "/usr/bin/git",
		"claude":  "/usr/local/bin/claude",
		"apt-get": "/usr/bin/apt-get",
	}, map[string]string{
		"node":    "v20.11.0\n",
		"python3": "Python 3.11.6\n",
		"git":     "git version 2.43.0\n",
		"claude":  "0.2.45\n",
	}, "linux")

	info := svc.Probe(context.Background())
	if !info.CanInstall {
		t.Fatalf("CanInstall: want true (apt-get available)")
	}
	for _, tool := range info.Tools {
		if !tool.Installable {
			t.Fatalf("%s.Installable: want true on fully-equipped fixture, got false", tool.Name)
		}
	}
}

// TestProbe_PerToolInstallable_TeamEditionAllFalse confirms team edition zeroes
// every per-tool Installable regardless of PM/npm presence.
func TestProbe_PerToolInstallable_TeamEditionAllFalse(t *testing.T) {
	t.Setenv("NIUNIU_EDITION", "team")
	svc := newTestService(map[string]string{
		"node":    "/usr/bin/node",
		"npm":     "/usr/bin/npm",
		"python3": "/usr/bin/python3",
		"git":     "/usr/bin/git",
		"claude":  "/usr/local/bin/claude",
		"apt-get": "/usr/bin/apt-get",
	}, nil, "linux")

	info := svc.Probe(context.Background())
	for _, tool := range info.Tools {
		if tool.Installable {
			t.Fatalf("%s.Installable must be false in team edition, got true", tool.Name)
		}
	}
}

// TestProbe_PerToolInstallable_ClaudeFalseWithoutNpm covers the realistic
// macOS/Linux box that has node bundled in some way that omits npm (rare but
// possible — e.g. a corp-managed install where npm was removed). claude must
// be marked non-installable so the SPA hides the install button rather than
// inviting a click that silently falls back to the docs URL.
func TestProbe_PerToolInstallable_ClaudeFalseWithoutNpm(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"node": "/usr/bin/node",
		// no npm, no apt-get
	}, map[string]string{
		"node": "v20.11.0\n",
	}, "linux")

	info := svc.Probe(context.Background())
	for _, tool := range info.Tools {
		if tool.Name == "claude" && tool.Installable {
			t.Fatalf("claude.Installable must be false when npm is absent")
		}
	}
}

// --- test helpers ---

func newTestService(paths map[string]string, versions map[string]string, goos string) *SystemDepsService {
	svc := NewSystemDepsService()
	svc.goos = goos
	svc.lookPath = func(name string) (string, error) {
		if p, ok := paths[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	svc.runVersion = func(ctx context.Context, name string) (string, error) {
		if v, ok := versions[name]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
	// Default: pip modules (cairosvg) probe as not-installed so existing tests
	// never spawn a real `python -m ...` subprocess. cairosvg-specific tests
	// override this.
	svc.runModuleVersion = func(ctx context.Context, python, module string) (string, error) {
		return "", errors.New("module not found")
	}
	svc.versionTimeout = 100 * time.Millisecond
	return svc
}

// --- cairosvg (pip-module tool, issue #472) ---

func TestProbe_Cairosvg_FoundViaPythonModule(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"python3": "/usr/bin/python3",
		"apt-get": "/usr/bin/apt-get",
	}, nil, "linux")
	svc.runModuleVersion = func(ctx context.Context, python, module string) (string, error) {
		if python == "python3" && module == "cairosvg" {
			return "2.9.0\n", nil
		}
		return "", errors.New("unexpected probe")
	}

	info := svc.Probe(context.Background())
	tool := findTool(t, info, "cairosvg")
	if !tool.Found || tool.Version != "2.9.0" {
		t.Fatalf("cairosvg should be found via module probe: %+v", tool)
	}
	if tool.Path != "python3 -m cairosvg" {
		t.Fatalf("cairosvg path: got %q want %q", tool.Path, "python3 -m cairosvg")
	}
	if !tool.Installable {
		t.Fatalf("cairosvg should be installable when python is present")
	}
}

func TestProbe_Cairosvg_NoPython_NotFoundNotInstallable(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"git":     "/usr/bin/git",
		"apt-get": "/usr/bin/apt-get",
	}, map[string]string{"git": "git version 2.43.0\n"}, "linux")
	// runModuleVersion would succeed, but no python on PATH must short-circuit.
	svc.runModuleVersion = func(ctx context.Context, python, module string) (string, error) {
		return "2.9.0\n", nil
	}

	info := svc.Probe(context.Background())
	tool := findTool(t, info, "cairosvg")
	if tool.Found {
		t.Fatalf("cairosvg must be not-found without a python interpreter: %+v", tool)
	}
	if tool.Installable {
		t.Fatalf("cairosvg must be non-installable without python")
	}
}

// cairosvg installs via pip, independent of the OS package manager: it must be
// installable on a box with python but no apt-get/winget (where node/git are
// not), unlike the pm-gated tools.
func TestProbe_Cairosvg_InstallableWithoutPackageManager(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"python3": "/usr/bin/python3",
		// no apt-get, no npm
	}, map[string]string{}, "linux")

	info := svc.Probe(context.Background())
	if info.CanInstall {
		t.Fatalf("page-level CanInstall must stay false without a system PM")
	}
	got := map[string]bool{}
	for _, tool := range info.Tools {
		got[tool.Name] = tool.Installable
	}
	if !got["cairosvg"] {
		t.Fatalf("cairosvg.Installable must be true when python is present (pip path)")
	}
	for _, name := range []string{"node", "git"} {
		if got[name] {
			t.Fatalf("%s.Installable must be false without a system PM", name)
		}
	}
}

func TestProbe_Cairosvg_TeamEditionNotInstallable(t *testing.T) {
	t.Setenv("NIUNIU_EDITION", "team")
	svc := newTestService(map[string]string{
		"python3": "/usr/bin/python3",
		"apt-get": "/usr/bin/apt-get",
	}, nil, "linux")

	info := svc.Probe(context.Background())
	tool := findTool(t, info, "cairosvg")
	if tool.Installable {
		t.Fatalf("cairosvg.Installable must be false in team edition")
	}
}

func TestInstall_Cairosvg_UsesPipUserInstall(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"python3": "/usr/bin/python3",
		// no apt-get needed — cairosvg bypasses the PM gate.
	}, nil, "linux")
	var ranName string
	var ranArgs []string
	svc.runInstall = func(ctx context.Context, name string, args []string, emit func(string)) int {
		ranName = name
		ranArgs = args
		return 0
	}

	jobID, fallback, err := svc.Install(context.Background(), "cairosvg")
	if err != nil || jobID == "" || fallback != "" {
		t.Fatalf("install: err=%v job=%q fallback=%q", err, jobID, fallback)
	}
	drainInstall(t, svc, jobID)
	if ranName != "python3" {
		t.Fatalf("cairosvg install must use python, got %q", ranName)
	}
	want := []string{"-m", "pip", "install", "--user", "cairosvg"}
	if strings.Join(ranArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("pip args: got %v want %v", ranArgs, want)
	}
}

func TestInstall_Cairosvg_NoPython_ReturnsFallback(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{"apt-get": "/usr/bin/apt-get"}, nil, "linux") // no python
	jobID, fallback, err := svc.Install(context.Background(), "cairosvg")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if jobID != "" {
		t.Fatalf("jobID should be empty when falling back, got %q", jobID)
	}
	if fallback != "https://cairosvg.org/documentation/" {
		t.Fatalf("fallback URL: got %q", fallback)
	}
}

func findTool(t *testing.T, info SystemDepsInfo, name string) ToolStatus {
	t.Helper()
	for _, tool := range info.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not present in probe result", name)
	return ToolStatus{}
}

func drainInstall(t *testing.T, svc *SystemDepsService, jobID string) {
	t.Helper()
	ch, unsub, err := svc.Subscribe(jobID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()
	for evt := range ch {
		if evt.Done {
			return
		}
	}
}

func TestInstall_RejectsUnknownTool(t *testing.T) {
	svc := newTestService(nil, nil, "linux")
	_, _, err := svc.Install(context.Background(), "rust")
	if err == nil || err != ErrUnknownTool {
		t.Fatalf("expected ErrUnknownTool, got %v", err)
	}
}

func TestInstall_TeamEditionDisabled(t *testing.T) {
	t.Setenv("NIUNIU_EDITION", "team")
	svc := newTestService(map[string]string{"apt-get": "/usr/bin/apt-get"}, nil, "linux")
	_, _, err := svc.Install(context.Background(), "node")
	if err != ErrInstallDisabled {
		t.Fatalf("expected ErrInstallDisabled, got %v", err)
	}
}

func TestInstall_NoPackageManager_ReturnsFallback(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(nil, nil, "linux") // no apt-get
	jobID, fallback, err := svc.Install(context.Background(), "node")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if jobID != "" {
		t.Fatalf("jobID should be empty when fallback used, got %q", jobID)
	}
	if fallback != "https://nodejs.org/" {
		t.Fatalf("fallback URL: got %q", fallback)
	}
}

func TestInstall_RunsCommand_StreamsLines(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{"apt-get": "/usr/bin/apt-get"}, nil, "linux")

	var ranName string
	var ranArgs []string
	svc.runInstall = func(ctx context.Context, name string, args []string, emit func(string)) int {
		ranName = name
		ranArgs = args
		emit("==> resolving deps")
		emit("==> installing")
		return 0
	}

	jobID, _, err := svc.Install(context.Background(), "node")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if jobID == "" {
		t.Fatalf("jobID empty")
	}

	ch, unsub, err := svc.Subscribe(jobID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()

	var lines []string
	var done bool
	for evt := range ch {
		if evt.Done {
			done = true
			break
		}
		lines = append(lines, evt.Line)
	}
	if !done {
		t.Fatalf("did not receive done")
	}
	if len(lines) < 2 {
		t.Fatalf("expected >=2 log lines, got %v", lines)
	}
	// apt-get, optionally via sudo when the test process is not root.
	isApt := (ranName == "sudo" && len(ranArgs) > 0 && ranArgs[0] == "apt-get") || ranName == "apt-get"
	if !isApt {
		t.Fatalf("expected (sudo) apt-get..., got %s %v", ranName, ranArgs)
	}
}

func TestAptInstallCommand_DropsSudoAsRoot(t *testing.T) {
	// As root (team docker): no sudo prefix — slim images lack sudo.
	name, args := aptInstallCommand("tesseract-ocr", true)
	if name != "apt-get" || len(args) == 0 || args[0] != "install" {
		t.Fatalf("root: expected apt-get install..., got %s %v", name, args)
	}
	// As a non-root desktop user: keep the sudo fallback.
	name, args = aptInstallCommand("tesseract-ocr", false)
	if name != "sudo" || len(args) == 0 || args[0] != "apt-get" {
		t.Fatalf("non-root: expected sudo apt-get..., got %s %v", name, args)
	}
}

func TestInstall_SecondCall_Conflicts(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{"apt-get": "/usr/bin/apt-get"}, nil, "linux")

	hold := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	svc.runInstall = func(ctx context.Context, name string, args []string, emit func(string)) int {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-hold
		return 0
	}

	_, _, err := svc.Install(context.Background(), "node")
	if err != nil {
		t.Fatalf("first install err: %v", err)
	}
	// Wait until the runInstall goroutine has actually started so the
	// second Install reliably sees an in-flight job.
	<-started
	_, _, err = svc.Install(context.Background(), "git")
	if err != ErrJobInFlight {
		t.Fatalf("expected ErrJobInFlight, got %v", err)
	}
	close(hold)
	// drain so test cleanup is clean
	for calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
}

func TestInstall_ClaudeUsesNpm(t *testing.T) {
	os.Unsetenv("NIUNIU_EDITION")
	svc := newTestService(map[string]string{
		"apt-get": "/usr/bin/apt-get",
		"npm":     "/usr/bin/npm",
	}, nil, "linux")
	var ranName string
	var ranArgs []string
	svc.runInstall = func(ctx context.Context, name string, args []string, emit func(string)) int {
		ranName = name
		ranArgs = args
		return 0
	}
	jobID, _, err := svc.Install(context.Background(), "claude")
	if err != nil || jobID == "" {
		t.Fatalf("install: %v %q", err, jobID)
	}
	// Wait for the install goroutine to actually run (and complete).
	ch, unsub, err := svc.Subscribe(jobID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()
	for evt := range ch {
		if evt.Done {
			break
		}
	}
	if ranName != "npm" {
		t.Fatalf("claude install must use npm, got %q", ranName)
	}
	if len(ranArgs) < 3 || ranArgs[0] != "install" || ranArgs[1] != "-g" || ranArgs[2] != "@anthropic-ai/claude-code" {
		t.Fatalf("npm args: %v", ranArgs)
	}
}

func TestInstallFailureEmitsFallbackHint(t *testing.T) {
	svc := &SystemDepsService{
		goos: "windows",
		runInstall: func(ctx context.Context, name string, args []string, emit func(string)) int {
			emit("first line")
			return 1 // simulate failure
		},
		lookPath: func(string) (string, error) { return "/usr/bin/winget", nil },
	}
	jobID, _, err := svc.Install(context.Background(), "node")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	ch, unsub, err := svc.Subscribe(jobID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsub()
	var lines []string
	var done bool
	deadline := time.After(2 * time.Second)
	for !done {
		select {
		case evt, ok := <-ch:
			if !ok {
				done = true
				break
			}
			if evt.Line != "" {
				lines = append(lines, evt.Line)
			}
			if evt.Done {
				done = true
			}
		case <-deadline:
			t.Fatal("timeout waiting for install completion")
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "[niuniu] 安装失败 (exit=1)") {
		t.Errorf("missing failure line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "winget source update") {
		t.Errorf("missing windows-specific hint, got:\n%s", joined)
	}
}

func TestCommandForWindowsHasAcceptFlags(t *testing.T) {
	s := &SystemDepsService{goos: "windows"}
	for _, tool := range []string{"node", "python3", "git"} {
		_, args := s.commandFor(tool, "winget")
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"--accept-source-agreements",
			"--accept-package-agreements",
			"--silent",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("tool=%s missing %s in args: %s", tool, want, joined)
			}
		}
	}
}

func TestProbeIncludesGitIdentity(t *testing.T) {
	svc := &SystemDepsService{
		goos:     "linux",
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		runVersion: func(ctx context.Context, name string) (string, error) {
			return name + " 1.0.0", nil
		},
		identityProbe: func(ctx context.Context) (string, string, error) {
			return "Alice", "alice@example.com", nil
		},
	}
	info := svc.Probe(context.Background())
	var gitTool *ToolStatus
	for i, tool := range info.Tools {
		if tool.Name == "git" {
			gitTool = &info.Tools[i]
			break
		}
	}
	if gitTool == nil {
		t.Fatal("git tool not in probe response")
	}
	if gitTool.Extras == nil || gitTool.Extras.GitIdentity == nil {
		t.Fatal("Extras.GitIdentity not populated")
	}
	id := gitTool.Extras.GitIdentity
	if !id.Configured || id.Name != "Alice" || id.Email != "alice@example.com" {
		t.Fatalf("unexpected identity: %+v", id)
	}
}

func TestProbeIdentityUnconfigured(t *testing.T) {
	svc := &SystemDepsService{
		goos:     "linux",
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		runVersion: func(ctx context.Context, name string) (string, error) {
			return name + " 1.0.0", nil
		},
		identityProbe: func(ctx context.Context) (string, string, error) {
			return "", "", nil
		},
	}
	info := svc.Probe(context.Background())
	for _, tool := range info.Tools {
		if tool.Name == "git" {
			if tool.Extras == nil || tool.Extras.GitIdentity == nil || tool.Extras.GitIdentity.Configured {
				t.Fatalf("expected Configured=false, got %+v", tool.Extras)
			}
			return
		}
	}
	t.Fatal("git tool not found")
}
