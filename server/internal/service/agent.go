package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/terminal"
)

// terminalIdleTimeout is how long a PTY process survives after
// all WebSocket connections disconnect. This prevents orphan processes
// when users switch workspaces or close terminal views.
const terminalIdleTimeout = 5 * time.Minute

// workspaceReviewTimeout is how long a workspace stays in "needs_review"
// before automatically transitioning to "attention".
const workspaceReviewTimeout = 30 * time.Minute

// MCPConfigWriter is a minimal interface for generating .mcp.json and the
// per-workspace .claude/settings.json (WorktreeCreate / WorktreeRemove hooks)
// files. Matches the subset of service.MCPConfigGenerator used by
// AgentManager. GenerateClaudeSettings is idempotent and safe to call on
// every spawn — it skips when the file already exists, so existing
// workspaces transparently backfill on the next agent run.
type MCPConfigWriter interface {
	Generate(wsPath string, opts config.MCPGenerateOptions, extras []string, configDir string) (*MCPGenerateResult, error)
	GenerateClaudeSettings(wsPath string) error
	// GenerateCodexConfigToml writes <wsPath>/.codex/config.toml so the
	// Codex CLI (spawned with CODEX_HOME=<wsPath>/.codex) loads the
	// niuniu-mcp server at session start.
	GenerateCodexConfigToml(wsPath string, opts config.MCPGenerateOptions) error
}

type AgentManager struct {
	mu            sync.RWMutex
	processes     map[int64]*terminal.PTYProcess // workspaceID -> process
	wsConnections map[int64]int                  // workspaceID -> number of active WebSocket connections
	idleTimers    map[int64]*time.Timer          // workspaceID -> idle cleanup timer
	reviewTimers  map[int64]*time.Timer          // workspaceID -> review-to-idle timer
	q             *store.Queries
	cfg           *config.AgentConfig
	queueRollback func(ctx context.Context, workspaceID int64)
	notifyHub     *notify.NotificationHub
	mcpSessions   *MCPSessionService
	mcpWriter     MCPConfigWriter
	perm          *PermissionService
	askUser       *AskUserService
	claudeAccount *ClaudeAccountService
	codexAccount  *CodexAccountService
	gitIdentity   *GitIdentityService
	// claudeAcctByWorkspace records the account ID injected at spawn time
	// for each running PTY process; consulted by WorkspacesUsingAccount to
	// gate Delete (C4 / spec IM-7). Guarded by mu.
	claudeAcctByWorkspace map[int64]int64
	// codexAcctByWorkspace mirrors claudeAcctByWorkspace for codex spawns.
	codexAcctByWorkspace map[int64]int64
}

func (m *AgentManager) SetMCPWriter(w MCPConfigWriter) {
	m.mcpWriter = w
}

func (m *AgentManager) SetNotifyHub(hub *notify.NotificationHub) {
	m.notifyHub = hub
}

func (m *AgentManager) SetQueueRollback(fn func(ctx context.Context, workspaceID int64)) {
	m.queueRollback = fn
}

func (m *AgentManager) SetMCPSessionService(svc *MCPSessionService) {
	m.mcpSessions = svc
}

func (m *AgentManager) SetPermissionService(svc *PermissionService) {
	m.perm = svc
}

func (m *AgentManager) SetAskUserService(svc *AskUserService) {
	m.askUser = svc
}

func (m *AgentManager) SetClaudeAccountService(svc *ClaudeAccountService) {
	m.claudeAccount = svc
}

// SetCodexAccountService wires the codex multi-account resolver so PTY spawns
// for codex workspaces can inject CODEX_HOME=<account.config_dir>. Optional;
// nil = back-compat path (no env injection, codex uses ~/.codex/).
func (m *AgentManager) SetCodexAccountService(svc *CodexAccountService) {
	m.codexAccount = svc
}

// SetGitIdentityService wires the per-user git author resolver so PTY spawns
// can inject GIT_AUTHOR_* / GIT_COMMITTER_* env. Optional; when nil the
// agent inherits the OS-global git config (preserving personal-edition
// behavior). Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
func (m *AgentManager) SetGitIdentityService(svc *GitIdentityService) {
	m.gitIdentity = svc
}

func NewAgentManager(q *store.Queries, cfg *config.AgentConfig) *AgentManager {
	return &AgentManager{
		processes:             make(map[int64]*terminal.PTYProcess),
		wsConnections:         make(map[int64]int),
		idleTimers:            make(map[int64]*time.Timer),
		reviewTimers:          make(map[int64]*time.Timer),
		claudeAcctByWorkspace: make(map[int64]int64),
		codexAcctByWorkspace:  make(map[int64]int64),
		q:                     q,
		cfg:                   cfg,
	}
}

// WorkspacesUsingCodexAccount returns the workspace IDs whose live PTY agent
// was spawned with CODEX_HOME for the given accountID. Mirrors
// WorkspacesUsingAccount; registered with CodexAccountService to gate Delete.
func (m *AgentManager) WorkspacesUsingCodexAccount(accountID int64) []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []int64
	for wsID, accID := range m.codexAcctByWorkspace {
		if accID == accountID {
			if _, alive := m.processes[wsID]; alive {
				out = append(out, wsID)
			}
		}
	}
	return out
}

// WorkspacesUsingAccount returns the workspace IDs whose live PTY agent was
// spawned with CLAUDE_CONFIG_DIR for the given accountID. SpawnTrackerFn
// implementation registered with ClaudeAccountService.
//
// Stale entries in claudeAcctByWorkspace are tolerated — the alive check
// against m.processes filters them out so cleanup is lazy (GC happens on
// the next spawn for the same workspaceID, which overwrites or deletes).
func (m *AgentManager) WorkspacesUsingAccount(accountID int64) []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []int64
	for wsID, accID := range m.claudeAcctByWorkspace {
		if accID == accountID {
			if _, alive := m.processes[wsID]; alive {
				out = append(out, wsID)
			}
		}
	}
	return out
}

// LiveWorkspaceIDs returns the workspace IDs with a live PTY agent process. Used
// by the server's MCP-token heartbeat to keep those workspaces' session tokens
// from expiring while the agent is alive (see MCPSessionService.RenewForWorkspace).
func (m *AgentManager) LiveWorkspaceIDs() []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]int64, 0, len(m.processes))
	for wsID := range m.processes {
		out = append(out, wsID)
	}
	return out
}

func (m *AgentManager) Start(ctx context.Context, workspaceID int64, workDir, initialPrompt string, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if proc, exists := m.processes[workspaceID]; exists {
		// Check if process is still alive
		select {
		case <-proc.Done():
			// Process has exited, remove it
			delete(m.processes, workspaceID)
		default:
			// Process is still running
			return fmt.Errorf("agent already running for workspace %d", workspaceID)
		}
	}

	// Resolve which agent CLI to spawn from workspaces.cli_type.
	// Fail closed on lookup error: silently spawning claude when the user
	// picked codex would be a confusing product bug (they get a different
	// agent than they configured). Empty cli_type is a legacy row from
	// before the migration backfilled the column; treat it as 'claude'.
	ws, err := m.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("agent: lookup workspace cli_type: %w", err)
	}
	cliType := ws.CliType
	if cliType == "" {
		cliType = "claude"
	}

	// Build command from config — branch on cli_type. Each engine maps to its
	// own configured binary; defaulting an unknown type to claude would spawn a
	// different agent than the user configured (see the fail-closed note above).
	var command string
	var args []string
	switch cliType {
	case "codex":
		command = m.cfg.CodexCli.Command
		if command == "" {
			command = "codex"
		}
		args = make([]string, len(m.cfg.CodexCli.Args))
		copy(args, m.cfg.CodexCli.Args)
	case "qwen":
		command = m.cfg.QwenCli.Command
		if command == "" {
			command = "qwen"
		}
		args = make([]string, len(m.cfg.QwenCli.Args))
		copy(args, m.cfg.QwenCli.Args)
	default: // claude (and empty, normalized above)
		command = m.cfg.ClaudeCode.Command
		args = make([]string, len(m.cfg.ClaudeCode.Args))
		copy(args, m.cfg.ClaudeCode.Args)
	}

	// If initialPrompt provided, append to args
	if initialPrompt != "" {
		args = append(args, initialPrompt)
	}

	// Fetch workspace env vars (with the active scene's projected env overlaid
	// underneath, so a mounted scene's variables take effect) and convert to
	// slice format.
	envVars, err := sceneenv.Resolve(ctx, m.q, workspaceID)
	if err != nil {
		return fmt.Errorf("fetch workspace env vars: %w", err)
	}
	envSlice := convertEnvVarsToSliceFromStore(envVars)

	// Inject per-CLI account config dir.
	// Claude path: CLAUDE_CONFIG_DIR from claude_accounts.
	// Codex path: if a codex_account is bound (M2.5), inject CODEX_HOME=
	// <account.config_dir>. Otherwise the CLI uses the user's global ~/.codex/
	// (back-compat — pre-M2.5 workspaces stay working).
	var spawnAccountID int64
	var spawnCodexAccountID int64
	if cliType == "claude" && m.claudeAccount != nil {
		if acc, accErr := m.claudeAccount.ResolveForWorkspace(ctx, workspaceID, userID); accErr != nil {
			slog.Warn("agent: resolve claude account failed, spawn without CLAUDE_CONFIG_DIR",
				"workspaceID", workspaceID, "err", accErr)
		} else if acc.ConfigDir != "" {
			if !EnvHasKey(envSlice, "CLAUDE_CONFIG_DIR") {
				envSlice = append(envSlice, "CLAUDE_CONFIG_DIR="+acc.ConfigDir)
				spawnAccountID = acc.ID
			} else {
				slog.Warn("agent: CLAUDE_CONFIG_DIR already set in env preset; skipping niuniu injection",
					"workspaceID", workspaceID, "account", acc.ID)
			}
			go m.claudeAccount.MarkUsed(context.Background(), acc.ID)
		}
	} else if cliType == "codex" {
		// Resolve codex managed account (if any) and inject CODEX_HOME so the
		// CLI reads the per-account auth.json + config.toml. nil = back-compat:
		// no env injection, codex falls back to user's global ~/.codex/.
		if m.codexAccount != nil {
			if acc, accErr := m.codexAccount.ResolveForWorkspace(ctx, workspaceID, userID); accErr != nil {
				slog.Warn("agent: resolve codex account failed, spawn without CODEX_HOME",
					"workspaceID", workspaceID, "err", accErr)
			} else if acc != nil && acc.ConfigDir != "" {
				if !EnvHasKey(envSlice, "CODEX_HOME") {
					envSlice = append(envSlice, "CODEX_HOME="+acc.ConfigDir)
					spawnCodexAccountID = acc.ID
				} else {
					slog.Warn("agent: CODEX_HOME already set in env preset; skipping niuniu injection",
						"workspaceID", workspaceID, "account", acc.ID)
				}
				go m.codexAccount.MarkUsed(context.Background(), acc.ID)
			}
		}
		// Pre-create the MCP session token and write .codex/config.toml
		// BEFORE spawning codex, because codex reads its TOML at startup
		// and does not auto-reload on file change in PTY mode. Failure to
		// generate the file does not abort spawn — log+continue and the
		// codex session will simply have no niuniu-mcp wired in.
		if userID > 0 && m.mcpSessions != nil && m.mcpWriter != nil {
			rawToken, err2 := m.mcpSessions.Create(ctx, workspaceID, MCPSessionTTL)
			if err2 != nil {
				slog.Warn("codex: create MCP session token failed",
					"workspaceID", workspaceID, "err", err2)
			} else {
				projectID, _ := m.q.GetProjectIDForWorkspace(ctx, workspaceID)
				opts := config.MCPGenerateOptions{
					ProjectID:    projectID,
					WorkspaceID:  workspaceID,
					InboxDir:     workDir + "/.team/inboxes",
					SessionToken: rawToken,
				}
				if err3 := m.mcpWriter.GenerateCodexConfigToml(workDir, opts); err3 != nil {
					slog.Warn("codex: write .codex/config.toml failed",
						"workspaceID", workspaceID, "err", err3)
				}
			}
		}
	}

	// Inject per-user GIT_AUTHOR_*/GIT_COMMITTER_* so commits Claude runs in
	// the PTY are attributed to the niuniu user that triggered the session.
	// Optional path: if the service is unwired or the user is unknown,
	// EnvVarsForIdentity returns nil and git falls back to OS-global config.
	// Skip-when-set: an env preset that already declared GIT_AUTHOR_NAME
	// (or any of the four) wins — same precedence rule as the agentproxy
	// chat path. Spec §3.1.5 calls authorship a declarative non-coercive
	// element, so a user override should be respected.
	if m.gitIdentity != nil && userID > 0 && !EnvHasKey(envSlice, "GIT_AUTHOR_NAME") {
		if id, err := m.gitIdentity.Resolve(ctx, userID); err == nil {
			envSlice = append(envSlice, EnvVarsForIdentity(id)...)
		} else {
			slog.Warn("agent: resolve git identity failed; spawn without GIT_AUTHOR_*",
				"workspaceID", workspaceID, "userID", userID, "err", err)
		}
	}

	// Create PTY process with workDir as cwd and workspace env vars
	proc, err := terminal.NewPTYProcess(command, args, workDir, envSlice)
	if err != nil {
		return fmt.Errorf("create PTY process: %w", err)
	}

	// Store in processes map
	m.processes[workspaceID] = proc
	if spawnAccountID > 0 {
		m.claudeAcctByWorkspace[workspaceID] = spawnAccountID
	} else {
		delete(m.claudeAcctByWorkspace, workspaceID)
	}
	if spawnCodexAccountID > 0 {
		m.codexAcctByWorkspace[workspaceID] = spawnCodexAccountID
	} else {
		delete(m.codexAcctByWorkspace, workspaceID)
	}

	// Update workspace agent_status = "running", agent_pid in DB
	pid := int64(proc.Pid())
	err = m.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Int64: pid, Valid: true},
		AgentStatus: sql.NullString{String: "running", Valid: true},
		ID:          workspaceID,
	})
	if err != nil {
		// Close process if DB update fails
		proc.Close()
		delete(m.processes, workspaceID)
		return fmt.Errorf("update workspace agent status: %w", err)
	}

	// Set session user identity
	if userID > 0 {
		if err2 := m.q.UpdateWorkspaceSessionUser(ctx, store.UpdateWorkspaceSessionUserParams{
			CurrentSessionUserID: sql.NullInt64{Int64: userID, Valid: true},
			ID:                   workspaceID,
		}); err2 != nil {
			slog.Warn("set current_session_user_id", "error", err2, "workspaceID", workspaceID)
		}
		// Claude workspaces write .mcp.json + .claude/settings.json after
		// spawn (token is generated here). Codex workspaces handled their
		// .codex/config.toml + token *before* spawn earlier in this
		// function, since codex reads its TOML at startup and does not
		// auto-reload in PTY mode.
		if cliType == "claude" && m.mcpSessions != nil {
			if rawToken, err2 := m.mcpSessions.Create(ctx, workspaceID, MCPSessionTTL); err2 != nil {
				slog.Warn("create MCP session token", "error", err2, "workspaceID", workspaceID)
			} else if m.mcpWriter != nil && rawToken != "" {
				// Regenerate .mcp.json so the token is injected into NIUNIU_MCP_TOKEN env.
				projectID, _ := m.q.GetProjectIDForWorkspace(ctx, workspaceID)
				opts := config.MCPGenerateOptions{
					ProjectID:    projectID,
					WorkspaceID:  workspaceID,
					InboxDir:     workDir + "/.team/inboxes",
					SessionToken: rawToken,
				}
				if _, err3 := m.mcpWriter.Generate(workDir, opts, nil, ""); err3 != nil {
					slog.Warn("regenerate .mcp.json with session token", "error", err3, "workspaceID", workspaceID)
				}
				// Backfill .claude/settings.json for workspaces created
				// before the WorktreeCreate hooks shipped (idempotent —
				// the writer skips when the file already exists).
				if err3 := m.mcpWriter.GenerateClaudeSettings(workDir); err3 != nil {
					slog.Warn("backfill .claude/settings.json", "error", err3, "workspaceID", workspaceID)
				}
			}
		}
	}

	// Update workspace status to "running" and cancel any pending review timer
	if err2 := m.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
		Status: "running",
		ID:     workspaceID,
	}); err2 != nil {
		slog.Error("update workspace status to running", "error", err2, "workspaceID", workspaceID)
	}
	m.cancelReviewTimer(workspaceID)

	// Start goroutine to wait for process exit and update status to "idle"
	go func() {
		proc.Wait()

		m.mu.Lock()
		defer m.mu.Unlock()

		// Remove from map
		delete(m.processes, workspaceID)
		m.cancelIdleTimer(workspaceID)

		bgCtx := context.Background()

		// Clear session user identity on process exit
		if err := m.q.UpdateWorkspaceSessionUser(bgCtx, store.UpdateWorkspaceSessionUserParams{
			CurrentSessionUserID: sql.NullInt64{Valid: false},
			ID:                   workspaceID,
		}); err != nil {
			slog.Warn("clear current_session_user_id on exit", "error", err, "workspaceID", workspaceID)
		}
		if m.mcpSessions != nil {
			if err := m.mcpSessions.Revoke(bgCtx, workspaceID); err != nil {
				slog.Warn("revoke MCP session on exit", "error", err, "workspaceID", workspaceID)
			}
		}

		// Update DB status to "idle"
		err := m.q.UpdateAgentStatus(bgCtx, store.UpdateAgentStatusParams{
			AgentPid:    sql.NullInt64{Valid: false},
			AgentStatus: sql.NullString{String: "idle", Valid: true},
			ID:          workspaceID,
		})
		if err != nil {
			slog.Error("update agent status on exit", "error", err, "workspaceID", workspaceID)
		}
	}()

	return nil
}

func (m *AgentManager) Stop(ctx context.Context, workspaceID int64) error {
	// Cancel any pending permission requests for this workspace; runs after
	// the lock is released (LIFO defer order) so CancelByWorkspace's DB +
	// channel sends don't hold m.mu.
	defer func() {
		if m.perm != nil {
			_ = m.perm.CancelByWorkspace(context.Background(), workspaceID, "session_end")
		}
		if m.askUser != nil {
			_ = m.askUser.CancelByWorkspace(context.Background(), workspaceID, "session_end")
		}
	}()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancelIdleTimer(workspaceID)

	// Clear session user identity
	if err := m.q.UpdateWorkspaceSessionUser(ctx, store.UpdateWorkspaceSessionUserParams{
		CurrentSessionUserID: sql.NullInt64{Valid: false},
		ID:                   workspaceID,
	}); err != nil {
		slog.Warn("clear current_session_user_id on stop", "error", err, "workspaceID", workspaceID)
	}
	if m.mcpSessions != nil {
		if err := m.mcpSessions.Revoke(ctx, workspaceID); err != nil {
			slog.Warn("revoke MCP session on stop", "error", err, "workspaceID", workspaceID)
		}
	}

	// Get process from map
	proc, exists := m.processes[workspaceID]
	if !exists {
		// Already stopped, just update DB
		return m.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
			AgentPid:    sql.NullInt64{Valid: false},
			AgentStatus: sql.NullString{String: "idle", Valid: true},
			ID:          workspaceID,
		})
	}

	// Close process
	if err := proc.Close(); err != nil {
		slog.Warn("close process", "error", err)
	}

	// Remove from map
	delete(m.processes, workspaceID)
	delete(m.wsConnections, workspaceID)

	// Update workspace agent_status = "idle", agent_pid = null
	return m.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Valid: false},
		AgentStatus: sql.NullString{String: "idle", Valid: true},
		ID:          workspaceID,
	})
}

func (m *AgentManager) Send(workspaceID int64, input string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Get process from map
	proc, exists := m.processes[workspaceID]
	if !exists {
		return fmt.Errorf("no agent running for workspace %d", workspaceID)
	}

	// Write input to PTY stdin
	_, err := proc.Write([]byte(input))
	return err
}

func (m *AgentManager) GetProcess(workspaceID int64) (*terminal.PTYProcess, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[workspaceID]
	return proc, exists
}

// RegisterTerminalConnection registers a new WebSocket terminal connection for a workspace.
// It increments the connection count. The returned cleanup function should be called when
// the WebSocket disconnects.
func (m *AgentManager) RegisterTerminalConnection(workspaceID int64) func() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancelIdleTimer(workspaceID) // cancel pending cleanup since we have a new connection
	m.wsConnections[workspaceID]++
	slog.Debug("terminal connection registered", "workspace_id", workspaceID, "count", m.wsConnections[workspaceID])
	return func() { m.releaseTerminalConnection(workspaceID) }
}

func (m *AgentManager) Status(workspaceID int64) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[workspaceID]
	if !exists {
		return "idle"
	}

	// Check if process is still alive
	select {
	case <-proc.Done():
		return "idle"
	default:
		return "running"
	}
}

// StartTerminal starts a shell terminal for a workspace, or returns existing if already running.
// This is independent of the agent - it provides a basic terminal for the workspace.
// It also registers a WebSocket connection and returns a cleanup function that should be
// called when the WebSocket disconnects.
func (m *AgentManager) StartTerminal(ctx context.Context, workspaceID int64) (*terminal.PTYProcess, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if proc, exists := m.processes[workspaceID]; exists {
		select {
		case <-proc.Done():
			delete(m.processes, workspaceID)
			delete(m.wsConnections, workspaceID)
			m.cancelIdleTimer(workspaceID)
		default:
			// Already running, cancel idle timer and increment connection count
			m.cancelIdleTimer(workspaceID)
			m.wsConnections[workspaceID]++
			return proc, func() { m.releaseTerminalConnection(workspaceID) }, nil
		}
	}

	// Get workspace to find its path
	ws, err := m.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("workspace not found: %w", err)
	}

	// Determine shell based on OS
	var shell string
	var args []string
	if runtime.GOOS == "windows" {
		// Use cmd.exe without /q flag - pipes don't need echo suppression
		// and /q might cause cmd to buffer input differently in non-interactive mode
		shell = "cmd.exe"
		args = []string{}
	} else {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
		args = []string{"-l"} // login shell for full environment
	}

	// Per-user git authorship: if a session user is already bound to this
	// workspace (via the agent path) we attribute the user's manual `git
	// commit` to them. When no session is set, fall through to OS-global
	// config (typical for personal-edition browsing).
	var ptyEnv []string
	if m.gitIdentity != nil && ws.CurrentSessionUserID.Valid && ws.CurrentSessionUserID.Int64 > 0 {
		if id, err := m.gitIdentity.Resolve(ctx, ws.CurrentSessionUserID.Int64); err == nil {
			ptyEnv = EnvVarsForIdentity(id)
		} else {
			slog.Warn("terminal: resolve git identity failed; spawn without GIT_AUTHOR_*",
				"workspaceID", workspaceID, "userID", ws.CurrentSessionUserID.Int64, "err", err)
		}
	}

	// Create PTY process with workspace as cwd
	proc, err := terminal.NewPTYProcess(shell, args, ws.Path, ptyEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("create PTY process: %w", err)
	}

	// Store in processes map with initial connection count of 1
	m.processes[workspaceID] = proc
	m.wsConnections[workspaceID] = 1

	// Start goroutine to clean up when process exits
	go func() {
		proc.Wait()

		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.processes, workspaceID)
		delete(m.wsConnections, workspaceID)
		m.cancelIdleTimer(workspaceID)
	}()

	return proc, func() { m.releaseTerminalConnection(workspaceID) }, nil
}

// releaseTerminalConnection decrements the WebSocket connection count for a workspace.
// When the last connection closes, starts an idle timer that will close the PTY
// after terminalIdleTimeout if no new connections arrive.
func (m *AgentManager) releaseTerminalConnection(workspaceID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	count, exists := m.wsConnections[workspaceID]
	if !exists {
		return
	}

	count--
	if count <= 0 {
		delete(m.wsConnections, workspaceID)

		// Start idle timer — if no reconnection within timeout, close the PTY
		if _, hasProc := m.processes[workspaceID]; hasProc {
			m.startIdleTimer(workspaceID)
		}
	} else {
		m.wsConnections[workspaceID] = count
	}
}

// startIdleTimer starts a timer that will close the PTY process for a workspace
// after terminalIdleTimeout. Must be called with m.mu held.
func (m *AgentManager) startIdleTimer(workspaceID int64) {
	// Cancel existing timer if any
	if t, ok := m.idleTimers[workspaceID]; ok {
		t.Stop()
	}

	m.idleTimers[workspaceID] = time.AfterFunc(terminalIdleTimeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		// Check that no new connections arrived while timer was pending
		if m.wsConnections[workspaceID] > 0 {
			delete(m.idleTimers, workspaceID)
			return
		}

		proc, exists := m.processes[workspaceID]
		if !exists {
			delete(m.idleTimers, workspaceID)
			return
		}

		slog.Info("terminal idle timeout: closing PTY", "workspace_id", workspaceID)
		proc.Close()
		delete(m.processes, workspaceID)
		delete(m.idleTimers, workspaceID)
	})
}

// cancelIdleTimer cancels the idle cleanup timer for a workspace.
// Must be called with m.mu held.
func (m *AgentManager) cancelIdleTimer(workspaceID int64) {
	if t, ok := m.idleTimers[workspaceID]; ok {
		t.Stop()
		delete(m.idleTimers, workspaceID)
	}
}

// startReviewTimer starts a timer that transitions the workspace from
// "needs_review" to "attention" after workspaceReviewTimeout.
// Must be called with m.mu held.
func (m *AgentManager) startReviewTimer(workspaceID int64) {
	m.cancelReviewTimer(workspaceID)
	m.reviewTimers[workspaceID] = time.AfterFunc(workspaceReviewTimeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.reviewTimers, workspaceID)
		err := m.q.UpdateWorkspaceStatusConditional(context.Background(), store.UpdateWorkspaceStatusConditionalParams{
			Status:   "attention",
			ID:       workspaceID,
			Status_2: "needs_review",
		})
		if err != nil {
			slog.Error("update workspace status to attention on timeout", "error", err, "workspaceID", workspaceID)
		}
		m.broadcastWorkspaceStatusChanged(context.Background(), workspaceID, "attention")
	})
}

// cancelReviewTimer cancels the review-to-attention timer for a workspace.
// Must be called with m.mu held.
func (m *AgentManager) cancelReviewTimer(workspaceID int64) {
	if t, ok := m.reviewTimers[workspaceID]; ok {
		t.Stop()
		delete(m.reviewTimers, workspaceID)
	}
}

// broadcastWorkspaceStatusChanged emits a workspace status_changed event enriched
// with OwnerType/OwnerID and should_alert_user_ids. Safe to call under m.mu — it
// does not acquire the lock. Failures in DB lookups are logged and broadcast
// continues with degraded values (empty IDs, zero owner) to avoid blocking timers.
func (m *AgentManager) broadcastWorkspaceStatusChanged(ctx context.Context, workspaceID int64, newStatus string) {
	if m.notifyHub == nil {
		return
	}

	ws, err := m.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("broadcastWorkspaceStatusChanged: GetWorkspace failed; broadcasting with zero owner",
			"workspaceID", workspaceID, "error", err)
		// Fall through with zero owner — still useful to invalidate frontend cache.
	}

	rows, err := m.q.GetWorkspaceAlertableUserIDs(ctx, workspaceID)
	if err != nil {
		slog.Warn("broadcastWorkspaceStatusChanged: get alertable user IDs", "workspaceID", workspaceID, "error", err)
	}
	alertIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.Valid {
			alertIDs = append(alertIDs, r.Int64)
		}
	}

	m.notifyHub.Broadcast(notify.Notification{
		Topic:     notify.TopicWorkspace,
		Action:    "status_changed",
		ID:        workspaceID,
		OwnerType: ws.OwnerType,
		OwnerID:   ws.OwnerID,
		Extra: map[string]any{
			"status":                newStatus,
			"should_alert_user_ids": alertIDs,
		},
	})
}

// OnAgentEvent handles agent lifecycle events (done/error) and updates
// the workspace status accordingly.
func (m *AgentManager) OnAgentEvent(ctx context.Context, workspaceID int64, eventType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch eventType {
	case "running":
		m.cancelReviewTimer(workspaceID)
		if err := m.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
			Status: "running",
			ID:     workspaceID,
		}); err != nil {
			slog.Error("update workspace status to running", "error", err, "workspaceID", workspaceID)
		}
		m.broadcastWorkspaceStatusChanged(ctx, workspaceID, "running")
	case "done":
		m.cancelReviewTimer(workspaceID)
		if err := m.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
			Status: "needs_review",
			ID:     workspaceID,
		}); err != nil {
			slog.Error("update workspace status to needs_review", "error", err, "workspaceID", workspaceID)
			return
		}
		// No status_changed broadcast here -- proxy.go broadcasts
		// workspace.agent_done which covers invalidation + toast.
		m.startReviewTimer(workspaceID)
	case "error":
		m.cancelReviewTimer(workspaceID)
		if err := m.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
			Status: "attention",
			ID:     workspaceID,
		}); err != nil {
			slog.Error("update workspace status to attention", "error", err, "workspaceID", workspaceID)
		}
		// No status_changed broadcast here -- proxy.go broadcasts
		// workspace.agent_done which covers invalidation + toast.
		// Run rollback async to avoid holding m.mu during DB calls
		rollback := m.queueRollback
		if rollback != nil {
			go rollback(context.Background(), workspaceID)
		}
	}
}

// RecoverReviewTimers restores review-to-attention timers for workspaces that were
// in "needs_review" status when the server last shut down. Workspaces past the
// timeout are transitioned to "attention" immediately; others get a timer for the
// remaining duration.
func (m *AgentManager) RecoverReviewTimers(ctx context.Context) {
	workspaces, err := m.q.ListWorkspaces(ctx)
	if err != nil {
		slog.Error("recover review timers: list workspaces", "error", err)
		return
	}
	// Collect workspaces needing timers first, then set up timers under a single lock
	type timerTarget struct {
		id        int64
		remaining time.Duration
	}
	var targets []timerTarget

	for _, ws := range workspaces {
		if ws.Status != "needs_review" {
			continue
		}
		elapsed := time.Since(ws.UpdatedAt)
		if elapsed >= workspaceReviewTimeout {
			// Already past timeout, transition to attention
			if err := m.q.UpdateWorkspaceStatusConditional(ctx, store.UpdateWorkspaceStatusConditionalParams{
				Status:   "attention",
				ID:       ws.ID,
				Status_2: "needs_review",
			}); err != nil {
				slog.Error("recover review timers: update status to attention", "error", err, "workspaceID", ws.ID)
				continue
			}
			m.broadcastWorkspaceStatusChanged(ctx, ws.ID, "attention")
		} else {
			targets = append(targets, timerTarget{id: ws.ID, remaining: workspaceReviewTimeout - elapsed})
		}
	}

	// Set up all timers under a single lock acquisition
	if len(targets) > 0 {
		m.mu.Lock()
		for _, t := range targets {
			wsID := t.id
			m.reviewTimers[wsID] = time.AfterFunc(t.remaining, func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				delete(m.reviewTimers, wsID)
				if err := m.q.UpdateWorkspaceStatusConditional(context.Background(), store.UpdateWorkspaceStatusConditionalParams{
					Status:   "attention",
					ID:       wsID,
					Status_2: "needs_review",
				}); err != nil {
					slog.Error("review timer: update status to attention", "error", err, "workspaceID", wsID)
				}
				m.broadcastWorkspaceStatusChanged(context.Background(), wsID, "attention")
			})
		}
		m.mu.Unlock()
	}
	slog.Info("recovered review timers for needs_review workspaces")
}

// convertEnvVarsToSliceFromStore converts store.WorkspaceEnv slice to env slice format
func convertEnvVarsToSliceFromStore(envVars []store.WorkspaceEnv) []string {
	result := make([]string, len(envVars))
	for i, e := range envVars {
		result[i] = e.Key + "=" + e.Value
	}
	return result
}
