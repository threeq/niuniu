package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niuniu-dev/niuniu/internal/git"
)

const defaultVersionTimeout = 3 * time.Second

// Tool names — order matters for the response.
// "codex" was added 2026-05-19 alongside the Codex CLI workspace support
// (see docs/superpowers/specs/2026-05-19-codex-cli-support-design.md).
// Codex is optional: niuniu still works without it; only codex-type
// workspaces refuse to spawn when codex is missing.
//
// "tesseract" (added 2026-06 for read-accel OCR, issue #283) is the OCR
// engine. It is fully OPTIONAL and never gates anything: when present the
// read_image MCP tool OCRs images to text; when absent that tool degrades
// to model-vision (downsampled image) and points the user at the install
// guide. It rides the same probe/one-click-install path as the dev tools so
// the Settings -> System Dependencies page surfaces it uniformly.
// "uv" (added 2026-06 for the office-mail scene, issue #454) is the Python
// runtime/launcher (uvx) that the email MCP server (ai-zerolab/mcp-email-server)
// runs under. Optional like codex/tesseract: niuniu works without it; only the
// office-mail scene needs it, and its scene card / this page surface it as
// missing so the user can install before the agent hits `uvx: not found`.
// "cairosvg" (added 2026-06 for the fireworks-tech-graph scene, issue #472) is
// the pip package that turns the skill's SVG output into PNG, headless, on both
// Win and Linux. It is the single dependency the diagram scene needs for PNG;
// SVG always works without it. Fully OPTIONAL and never gates anything: when
// absent the fireworks skill degrades to SVG-only (see svg2png helper +
// docs/specs/2026-06-28-cairosvg-sandbox-baking.md). Unlike the other tools it
// is neither a PATH binary (its console script lands in an off-PATH Scripts dir)
// nor an OS-PM/npm package — it is probed via `python -m cairosvg --version` and
// installed via `python -m pip install --user cairosvg`, so its probe/install
// paths are special-cased below.
var toolNames = []string{"node", "python3", "git", "claude", "codex", "tesseract", "uv", "cairosvg"}

// ocrGuideURL is the canonical Tesseract install guide on the marketing site
// (website/src/content/docs-zh/install/ocr-tesseract.mdx, issue #284). It is
// the fallback "manual install" target shown when no package manager is
// available, and must stay in sync with the same URL hard-coded in the SPA
// (web/src/pages/settings/system-deps-settings.tsx) and the read_image MCP
// tool's degrade message (cmd/niuniu-mcp/image_tools.go).
const ocrGuideURL = "https://www.niu6ai.com/docs/install/ocr-tesseract"

// ToolStatus is one row of the dependency probe.
type ToolStatus struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Version string `json:"version"`
	Path    string `json:"path"`
	// Installable mirrors whether Install would actually run a package-manager
	// command for this tool right now (vs. fall back to a redirect URL). It
	// captures per-tool nuance the page-level CanInstall cannot:
	//   - "claude" depends on npm (which ships with node), NOT the OS PM, so
	//     claude is installable on a LTSC Windows box that has node but no
	//     winget — even though CanInstall is false.
	//   - "node"/"python3"/"git" need an OS PM (winget/brew/apt-get).
	//   - Always false in team edition.
	// The SPA gates per-tool install buttons on this; CanInstall stays the
	// page-level "system PM available" signal used by the top-of-page message.
	Installable bool        `json:"installable"`
	Extras      *ToolExtras `json:"extras,omitempty"`
}

// ToolExtras carries tool-specific extra metadata, present only on tools
// that need it (currently just git).
type ToolExtras struct {
	GitIdentity *GitIdentity `json:"git_identity,omitempty"`
}

// GitIdentity is the global git config user.name / user.email pair plus a
// "configured" boolean for easier UI gating.
type GitIdentity struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Configured bool   `json:"configured"`
}

// SystemDepsInfo is the GET /api/system-deps response.
type SystemDepsInfo struct {
	Platform       string `json:"platform"`
	PackageManager string `json:"package_manager"`
	CanInstall     bool   `json:"can_install"`
	// PersonalMode is true when niuniu-server runs in personal/embedded mode
	// (cfg.Auth.Enabled=false). Host-shell ops (open terminal, open path) are
	// only meaningful in this mode. Decorated by SystemDepsHandler.
	PersonalMode bool         `json:"personal_mode"`
	Tools        []ToolStatus `json:"tools"`
}

// SystemDepsService probes installed dev tools and orchestrates one-click install.
type SystemDepsService struct {
	// pluggable for tests
	goos           string
	lookPath       func(name string) (string, error)
	runVersion     func(ctx context.Context, name string) (string, error)
	versionTimeout time.Duration
	versionErrors  map[string]error // optional override used in some tests
	// runModuleVersion probes a pip-installed python module (e.g. cairosvg) via
	// `<python> -m <module> --version`. Pluggable for tests; defaults to
	// defaultRunModuleVersion. Nil is treated as "module unprobeable".
	runModuleVersion func(ctx context.Context, python, module string) (string, error)
	// identityProbe is pluggable for tests; defaults to git.GetGlobalIdentity.
	identityProbe func(ctx context.Context) (name, email string, err error)

	// install lifecycle (used in later tasks)
	mu         sync.Mutex
	current    *installJob
	runInstall runInstallFn
}

func NewSystemDepsService() *SystemDepsService {
	s := &SystemDepsService{
		goos:           runtime.GOOS,
		versionTimeout: defaultVersionTimeout,
	}
	s.lookPath = exec.LookPath
	s.runVersion = s.defaultRunVersion
	s.runModuleVersion = s.defaultRunModuleVersion
	s.identityProbe = git.GetGlobalIdentity
	return s
}

// Probe runs LookPath + --version for every tool.
//
// All four tool probes run concurrently so the worst-case wall time is one
// `--version` timeout (3 s) instead of four (12 s) when every tool is missing
// or hung. The result slice preserves toolNames order regardless of which
// goroutine finishes first.
func (s *SystemDepsService) Probe(ctx context.Context) SystemDepsInfo {
	pm := s.detectPackageManager()
	tools := make([]ToolStatus, len(toolNames))
	var wg sync.WaitGroup
	for i, name := range toolNames {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			tools[i] = s.probeOne(ctx, name)
		}(i, name)
	}
	wg.Wait()

	// Attach git identity to the git tool row when probe succeeds.
	for i := range tools {
		if tools[i].Name != "git" {
			continue
		}
		// Only probe identity when git itself is found; otherwise the call
		// would fail with "git not found" anyway and the UI just shows
		// "未安装" for the whole row.
		if !tools[i].Found || s.identityProbe == nil {
			break
		}
		name, email, err := s.identityProbe(ctx)
		if err != nil {
			break
		}
		tools[i].Extras = &ToolExtras{GitIdentity: &GitIdentity{
			Name:       name,
			Email:      email,
			Configured: name != "" && email != "",
		}}
		break
	}

	isTeam := os.Getenv("NIUNIU_EDITION") == "team"
	canInstall := pm != "" && !isTeam

	// Per-tool installability: claude installs via npm so it only needs npm
	// (which ships with node); other tools need a system PM. Always false in
	// team edition. Mirrors the branching in (*SystemDepsService).Install.
	npmAvailable := false
	pythonAvailable := false
	if !isTeam {
		if _, err := s.lookPath("npm"); err == nil {
			npmAvailable = true
		}
		// cairosvg installs via `python -m pip`, so it needs a python
		// interpreter rather than the OS PM (mirrors the npm carve-out above).
		pythonAvailable = s.pythonCmd() != ""
	}
	for i := range tools {
		if isTeam {
			tools[i].Installable = false
			continue
		}
		switch tools[i].Name {
		case "claude", "codex":
			tools[i].Installable = npmAvailable
		case "cairosvg":
			tools[i].Installable = pythonAvailable
		default:
			tools[i].Installable = pm != ""
		}
	}

	return SystemDepsInfo{
		Platform:       s.goos,
		PackageManager: pm,
		CanInstall:     canInstall,
		Tools:          tools,
	}
}

func (s *SystemDepsService) probeOne(ctx context.Context, name string) ToolStatus {
	// cairosvg is a python module, not a PATH binary: its console script lands
	// in an off-PATH Scripts dir on Windows, so probe the import instead.
	if name == "cairosvg" {
		return s.probePipModule(ctx, name)
	}
	candidates := []string{name}
	if name == "python3" && s.goos == "windows" {
		candidates = append(candidates, "python")
	}
	for _, cand := range candidates {
		path, err := s.lookPath(cand)
		if err != nil {
			continue
		}
		version := s.versionFor(ctx, cand)
		return ToolStatus{Name: name, Found: true, Version: version, Path: path}
	}
	return ToolStatus{Name: name}
}

func (s *SystemDepsService) versionFor(ctx context.Context, cand string) string {
	if err, ok := s.versionErrors[cand]; ok && err != nil {
		return ""
	}
	out, err := s.runVersion(ctx, cand)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func (s *SystemDepsService) defaultRunVersion(ctx context.Context, name string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, s.versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, name, "--version").CombinedOutput()
	return string(out), err
}

// pythonCandidates is the interpreter lookup order. python3 is canonical on all
// platforms; Windows also exposes a bare `python` (mirrors probeOne's fallback).
func (s *SystemDepsService) pythonCandidates() []string {
	if s.goos == "windows" {
		return []string{"python3", "python"}
	}
	return []string{"python3"}
}

// pythonCmd returns the first python interpreter found on PATH, or "" if none.
// Used to gate cairosvg's installability and to build its pip/probe commands.
func (s *SystemDepsService) pythonCmd() string {
	if s.lookPath == nil {
		return ""
	}
	for _, cand := range s.pythonCandidates() {
		if _, err := s.lookPath(cand); err == nil {
			return cand
		}
	}
	return ""
}

// probePipModule reports whether a pip-installed python module is importable by
// running `<python> -m <module> --version`. cairosvg ships a console script but
// it lands in an off-PATH Scripts dir on Windows, so `python -m` is the reliable
// probe. A missing python interpreter (or a nil runModuleVersion in tests) means
// "not found" — never an error, since cairosvg is fully optional.
func (s *SystemDepsService) probePipModule(ctx context.Context, module string) ToolStatus {
	py := s.pythonCmd()
	if py == "" || s.runModuleVersion == nil {
		return ToolStatus{Name: module}
	}
	out, err := s.runModuleVersion(ctx, py, module)
	if err != nil {
		return ToolStatus{Name: module}
	}
	version := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			version = line
			break
		}
	}
	return ToolStatus{Name: module, Found: true, Version: version, Path: py + " -m " + module}
}

func (s *SystemDepsService) defaultRunModuleVersion(ctx context.Context, python, module string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, s.versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, python, "-m", module, "--version").CombinedOutput()
	return string(out), err
}

func (s *SystemDepsService) detectPackageManager() string {
	switch s.goos {
	case "windows":
		if _, err := s.lookPath("winget"); err == nil {
			return "winget"
		}
	case "darwin":
		if _, err := s.lookPath("brew"); err == nil {
			return "brew"
		}
	case "linux":
		if _, err := s.lookPath("apt-get"); err == nil {
			return "apt-get"
		}
	}
	return ""
}

// --- errors ---

var (
	ErrUnknownTool     = newSDErr("unknown tool")
	ErrInstallDisabled = newSDErr("install disabled in this edition")
	ErrJobInFlight     = newSDErr("another install job is in progress")
	ErrJobNotFound     = newSDErr("install job not found")
)

type sdErr struct{ s string }

func newSDErr(s string) error  { return &sdErr{s} }
func (e *sdErr) Error() string { return e.s }

// --- install events ---

// InstallEvent is one streamed item: either a stdout/stderr line or a terminator.
type InstallEvent struct {
	Line     string `json:"line,omitempty"`
	Done     bool   `json:"done,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// installJob holds one running install plus its subscribers.
type installJob struct {
	id       string
	tool     string
	mu       sync.Mutex
	subs     map[chan InstallEvent]struct{}
	history  []InstallEvent // for late subscribers
	done     bool
	exitCode int
}

func (j *installJob) publish(evt InstallEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.history = append(j.history, evt)
	if evt.Done {
		j.done = true
		j.exitCode = evt.ExitCode
	}
	for ch := range j.subs {
		select {
		case ch <- evt:
		default:
			// slow subscriber — drop on floor; subscriber will get history on resub
		}
	}
	if evt.Done {
		for ch := range j.subs {
			close(ch)
		}
		j.subs = nil
	}
}

// fallback URLs for when no package manager is available.
var fallbackURLs = map[string]string{
	"node":      "https://nodejs.org/",
	"python3":   "https://www.python.org/downloads/",
	"git":       "https://git-scm.com/downloads",
	"claude":    "https://docs.claude.com/en/docs/claude-code/setup",
	"codex":     "https://github.com/openai/codex",
	"tesseract": ocrGuideURL,
	"uv":        "https://docs.astral.sh/uv/getting-started/installation/",
	"cairosvg":  "https://cairosvg.org/documentation/",
}

// runInstallFn is the package-injected runner for a tool install. Returning
// the exit code; emit each output line through the callback.
type runInstallFn func(ctx context.Context, name string, args []string, emit func(string)) int

// Install kicks off an install for `tool`. Returns (jobID, fallbackURL, error).
// If fallbackURL is non-empty, the caller should redirect the user to it.
func (s *SystemDepsService) Install(ctx context.Context, tool string) (string, string, error) {
	if !isKnownTool(tool) {
		return "", "", ErrUnknownTool
	}
	pm := s.detectPackageManager()
	if os.Getenv("NIUNIU_EDITION") == "team" {
		return "", "", ErrInstallDisabled
	}
	// claude and codex are special: they need npm only, not the OS package
	// manager (both ship as @scope/package on npm). cairosvg is special too: it
	// installs via `python -m pip`, so it needs a python interpreter rather than
	// the OS PM. Both carve-outs bypass the pm=="" gate below.
	isNpmTool := tool == "claude" || tool == "codex"
	isPipTool := tool == "cairosvg"
	if !isNpmTool && !isPipTool && pm == "" {
		return "", fallbackURLs[tool], nil
	}
	if isNpmTool {
		// require npm; if missing, still fall back to the docs URL.
		if _, err := s.lookPath("npm"); err != nil {
			return "", fallbackURLs[tool], nil
		}
	}
	if isPipTool {
		// require a python interpreter; if missing, fall back to the docs URL.
		if s.pythonCmd() == "" {
			return "", fallbackURLs[tool], nil
		}
	}

	cmd, args := s.commandFor(tool, pm)
	if cmd == "" {
		// No package-manager install for this tool/OS (e.g. uv on linux) —
		// redirect to the manual install docs.
		return "", fallbackURLs[tool], nil
	}

	s.mu.Lock()
	if s.current != nil && !s.current.done {
		s.mu.Unlock()
		return "", "", ErrJobInFlight
	}
	job := &installJob{
		id:   newJobID(),
		tool: tool,
		subs: map[chan InstallEvent]struct{}{},
	}
	s.current = job
	s.mu.Unlock()

	runner := s.runInstall
	if runner == nil {
		runner = defaultRunInstall
	}

	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		// Banner line so the user sees what's about to run.
		job.publish(InstallEvent{Line: fmt.Sprintf("$ %s %s", cmd, strings.Join(args, " "))})
		// Watch-dog: warn if no output within 5s (typical sudo-without-tty scenario).
		go s.watchdog(jobCtx, job, cmd, args)
		exit := runner(jobCtx, cmd, args, func(line string) {
			job.publish(InstallEvent{Line: line})
		})
		if exit != 0 {
			job.publish(InstallEvent{Line: fmt.Sprintf(
				"[niuniu] 安装失败 (exit=%d)。请手动运行：%s %s",
				exit, cmd, strings.Join(args, " "))})
			if s.goos == "windows" {
				job.publish(InstallEvent{Line: "[niuniu] 如首次使用 winget 仍卡在协议页，请先在 PowerShell 跑一次：winget source update"})
			}
		}
		job.publish(InstallEvent{Done: true, ExitCode: exit})
	}()

	return job.id, "", nil
}

func (s *SystemDepsService) watchdog(ctx context.Context, job *installJob, cmd string, args []string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		job.mu.Lock()
		done := job.done
		recent := len(job.history)
		job.mu.Unlock()
		if !done && recent <= 1 { // only the banner line
			job.publish(InstallEvent{
				Line: fmt.Sprintf("[niuniu] 安装可能需要管理员权限。如果卡住，请在终端中手动运行：%s %s", cmd, strings.Join(args, " ")),
			})
		}
	}
}

// Subscribe attaches to the install job identified by jobID. It first replays
// the job's full output history into the returned channel, then either:
//   - closes the channel immediately if the job is already Done, or
//   - delivers further events as they happen until Done.
//
// Note: SystemDepsService keeps only one active job at a time. Once a *new*
// Install call starts, the *previous* jobID becomes unsubscribable (Subscribe
// returns ErrJobNotFound). Frontend / SSE callers should call Subscribe
// immediately after Install returns the jobID — before issuing any further
// Install calls — and treat ErrJobNotFound on a previously-known jobID as
// "stream already finished and superseded".
func (s *SystemDepsService) Subscribe(jobID string) (<-chan InstallEvent, func(), error) {
	s.mu.Lock()
	job := s.current
	s.mu.Unlock()
	if job == nil || job.id != jobID {
		return nil, nil, ErrJobNotFound
	}
	job.mu.Lock()
	// Size the channel to fit current history + a generous headroom for live events.
	ch := make(chan InstallEvent, len(job.history)+256)
	// Replay history first so a late subscriber catches up.
	for _, evt := range job.history {
		ch <- evt
	}
	if job.done {
		// Already finished — close immediately after replay.
		close(ch)
		job.mu.Unlock()
		return ch, func() {}, nil
	}
	if job.subs == nil {
		job.subs = map[chan InstallEvent]struct{}{}
	}
	job.subs[ch] = struct{}{}
	job.mu.Unlock()

	unsub := func() {
		job.mu.Lock()
		if _, ok := job.subs[ch]; ok {
			delete(job.subs, ch)
			close(ch)
		}
		job.mu.Unlock()
	}
	return ch, unsub, nil
}

// --- command builder ---

func (s *SystemDepsService) commandFor(tool, pm string) (string, []string) {
	if tool == "claude" {
		return "npm", []string{"install", "-g", "@anthropic-ai/claude-code"}
	}
	if tool == "codex" {
		return "npm", []string{"install", "-g", "@openai/codex"}
	}
	if tool == "cairosvg" {
		// Pip on every platform — deliberately NOT rsvg-convert/librsvg, which is
		// painful to install on Windows. `--user` installs into the per-user
		// site so it works without admin (verified on Win11: PNG renders headless
		// with no separate libcairo). team edition never reaches here (install
		// is disabled), so root/venv edge cases don't apply.
		py := s.pythonCmd()
		if py == "" {
			return "", nil // no interpreter — Install falls back to the docs URL
		}
		return py, []string{"-m", "pip", "install", "--user", "cairosvg"}
	}
	switch s.goos {
	case "windows":
		ids := map[string]string{
			"node":      "OpenJS.NodeJS.LTS",
			"python3":   "Python.Python.3.13",
			"git":       "Git.Git",
			"tesseract": "UB-Mannheim.TesseractOCR",
			"uv":        "astral-sh.uv",
		}
		if ids[tool] == "" {
			return "", nil // no PM package — Install falls back to the docs URL
		}
		return "winget", []string{
			"install", "-e", "--id", ids[tool],
			"--accept-source-agreements",
			"--accept-package-agreements",
			"--silent",
		}
	case "darwin":
		pkgs := map[string]string{
			"node":      "node",
			"python3":   "python@3.13",
			"git":       "git",
			"tesseract": "tesseract",
			"uv":        "uv",
		}
		if pkgs[tool] == "" {
			return "", nil
		}
		return "brew", []string{"install", pkgs[tool]}
	case "linux":
		pkgs := map[string]string{
			"node":      "nodejs",
			"python3":   "python3",
			"git":       "git",
			"tesseract": "tesseract-ocr",
			// uv is not in apt — handled by the empty-guard below (docs URL).
		}
		if pkgs[tool] == "" {
			return "", nil
		}
		return aptInstallCommand(pkgs[tool], os.Geteuid() == 0)
	}
	_ = pm
	return "", nil
}

// aptInstallCommand builds the apt-get install invocation. When the server runs
// as root — the typical containerized / team-docker case — it calls apt-get
// directly: `sudo` is usually absent from slim images (and unnecessary as root),
// so prefixing it produced "sudo: command not found" / "权限不足" failures. A
// non-root host still falls back to sudo for the interactive desktop flow.
func aptInstallCommand(pkg string, isRoot bool) (string, []string) {
	args := []string{"install", "-y", pkg}
	if isRoot {
		return "apt-get", args
	}
	return "sudo", append([]string{"apt-get"}, args...)
}

// --- helpers ---

func isKnownTool(name string) bool {
	for _, t := range toolNames {
		if t == name {
			return true
		}
	}
	return false
}

var jobIDCounter atomic.Int64

func newJobID() string {
	n := jobIDCounter.Add(1)
	return fmt.Sprintf("sd-%d-%d", time.Now().UnixNano(), n)
}

// SetGitIdentity is a small service-layer wrapper that lets handlers depend on
// SystemDepsService rather than reach into the git package directly.
func (s *SystemDepsService) SetGitIdentity(ctx context.Context, name, email string) error {
	return git.SetGlobalIdentity(ctx, name, email)
}

// defaultRunInstall runs the command and pipes both stdout and stderr line-by-line.
func defaultRunInstall(ctx context.Context, name string, args []string, emit func(string)) int {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		emit(fmt.Sprintf("[error] stdout pipe: %v", err))
		return 1
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		emit(fmt.Sprintf("[error] stderr pipe: %v", err))
		return 1
	}
	if err := cmd.Start(); err != nil {
		emit(fmt.Sprintf("[error] start: %v", err))
		return 1
	}
	var wg sync.WaitGroup
	wg.Add(2)
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			emit(sc.Text())
		}
		if err := sc.Err(); err != nil {
			emit(fmt.Sprintf("[error] scan: %v", err))
		}
	}
	go scan(stdout)
	go scan(stderr)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		emit(fmt.Sprintf("[error] wait: %v", err))
		return 1
	}
	return 0
}
