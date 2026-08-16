package agentproxy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/niuniu-dev/niuniu/internal/agentbackend"
	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// StatusTransitioner is called when an agent completes or errors, allowing
// the workspace status to be updated. Implemented by service.AgentManager.
type StatusTransitioner interface {
	OnAgentEvent(ctx context.Context, workspaceID int64, eventType string)
}

// MCPConfigWriter generates .mcp.json and the per-workspace
// .claude/settings.json (WorktreeCreate / WorktreeRemove hooks) files.
// GenerateClaudeSettings is idempotent — safe to call on every spawn so
// pre-existing workspaces backfill the hooks transparently.
// MCPGenerateResult mirrors service.GenerateResult so this package can
// stay import-light (no `service` dependency, which would form a cycle:
// service already imports agentproxy). The concrete
// service.MCPConfigGenerator returns *service.GenerateResult; the
// SetMCPWriter wiring in server/server.go wraps it via
// service.NewAgentProxyMCPWriter so the named-type mismatch is
// translated at the adapter boundary, never inside this package.
type MCPGenerateResult struct {
	WrittenServers []string `json:"written_servers"`
	Unavailable    []string `json:"unavailable"`
}

type MCPConfigWriter interface {
	Generate(wsPath string, opts config.MCPGenerateOptions, extras []string, configDir string) (*MCPGenerateResult, error)
	GenerateClaudeSettings(wsPath string) error
	GenerateCodexConfigToml(wsPath string, opts config.MCPGenerateOptions) error
	GenerateCodexConfigArgs(opts config.MCPGenerateOptions) ([]string, error)
	// NiuniuMcpServer resolves the niuniu-mcp server entry (command/args/env) for
	// a workspace so an MCP-client agent (goose) can consume niuniu tools.
	NiuniuMcpServer(opts config.MCPGenerateOptions) (config.McpServerEntry, error)
	// SetWorkspaceKBReadonly write-denies the given KB dataset roots in
	// <wsPath>/.claude/settings.json (KB base4: directories exposed read-only).
	// An empty roots slice clears the managed entries.
	SetWorkspaceKBReadonly(wsPath string, roots []string) error
}

// MemoryFileWriter generates .learnings.generated.md for workspaces and runs the
// automatic memory-evolution pass that keeps that file in sync with the project.
type MemoryFileWriter interface {
	GenerateMemoryFile(ctx context.Context, projectID int64, dir string) string
	// EvolveProjectMemory auto-evolves the project's memory (supersede stale
	// entries, archive unused ones) and returns the number of memories changed.
	EvolveProjectMemory(ctx context.Context, projectID int64) (int, error)
}

// MCPSessionManager issues and revokes per-workspace MCP tokens.
// Implemented by service.MCPSessionService.
type MCPSessionManager interface {
	Create(ctx context.Context, workspaceID int64, ttl time.Duration) (string, error)
	Revoke(ctx context.Context, workspaceID int64) error
}

// WorkspaceAlertResolver resolves the set of user IDs that should receive
// toast notifications for a workspace event. Implemented by
// service.WorkspaceService. Defined as an interface here to avoid a
// circular import (service already imports agentproxy).
type WorkspaceAlertResolver interface {
	AlertableUserIDs(ctx context.Context, workspaceID int64) ([]int64, error)
}

// ServerSettingsReader is the minimal interface agentproxy depends on for
// reading admin-tunable global K/V settings. Implemented by
// *service.ServerSettingsService — defined here to avoid an agentproxy→service
// import cycle (service already imports agentproxy).
type ServerSettingsReader interface {
	GetInt(ctx context.Context, key string, def int) int
}

// limitedBuffer is a thread-safe buffer that keeps only the last maxSize bytes.
// Used to capture stderr without unbounded memory growth.
type limitedBuffer struct {
	mu      sync.Mutex
	buf     []byte
	maxSize int
}

func newLimitedBuffer(maxSize int) *limitedBuffer {
	return &limitedBuffer{maxSize: maxSize}
}

func (lb *limitedBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.buf = append(lb.buf, p...)
	if len(lb.buf) > lb.maxSize {
		lb.buf = lb.buf[len(lb.buf)-lb.maxSize:]
	}
	return len(p), nil
}

func (lb *limitedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return string(lb.buf)
}

// AgentProxy is the top-level manager — one per backend instance.
// ClaudeUsageRecorder is an injection seam so agentproxy can report observed
// rate_limit_event payloads to ClaudeUsageService without importing it
// (which would create a service→agentproxy→service cycle).
type ClaudeUsageRecorder interface {
	RecordRateLimit(accountID int64, rateLimitType string, resetsAtUnix int64, status string)
}

// RateLimitScheduler is the injection seam that lets agentproxy auto-create a
// one-shot schedule when a turn is rejected by the upstream rate limiter, so
// the workspace resumes (dequeues + sends) on its own once the window resets.
// Implemented by scheduler.Scheduler; wired via SetRateLimitScheduler. Same
// one-way import direction as ClaudeUsageRecorder (scheduler imports
// agentproxy, never the reverse).
type RateLimitScheduler interface {
	// OnRateLimited is called when a rate_limit_event with status "rejected"
	// arrives carrying a future resetsAt. Implementations should be idempotent
	// per (workspace, resetsAt) so repeated rejects in the same window don't
	// pile up duplicate schedules.
	OnRateLimited(ctx context.Context, workspaceID int64, resetsAtUnix int64)

	// OnAutohostWait is called by the autohost watchdog when it stops continuing
	// to WAIT for in-flight background work: it creates a one-shot resume schedule
	// at runAtUnix that sends `message` (the continue prompt) so the agent
	// re-checks. Idempotent: at most one unfired autohost-wait schedule per
	// workspace at a time. Returns true when a resume is pending (created now or
	// already pending); false when scheduling failed — the caller must then fall
	// back to a normal stop so the workspace is not left running with no resume.
	OnAutohostWait(ctx context.Context, workspaceID int64, runAtUnix int64, message string) bool

	// CancelAutoResume deletes the workspace's UNFIRED auto-resume one-shot
	// schedules (rate-limit + autohost-wait). Called when a user takes over a
	// workspace that only LOOKS idle but is holding the Enqueue gate closed
	// behind a scheduled resume: the user is driving now, so a later auto-resume
	// must not double-fire a continue prompt against a queue the user already
	// drained. Best-effort; idempotent (no pending schedule → no-op).
	CancelAutoResume(ctx context.Context, workspaceID int64)
}

// SessionStateRecorder snapshots workspace git state when a session goes
// idle and reports drift on the next resume. Same injection-seam pattern
// as ClaudeUsageRecorder — keeps service→agentproxy import direction.
type SessionStateRecorder interface {
	CaptureSnapshot(ctx context.Context, workspaceID int64, sessionID, lastUserMsg string) error
	DriftMessage(ctx context.Context, workspaceID int64, sessionID string) (string, error)
}

// GitIdentityResolver is the minimal interface agentproxy depends on for
// per-user git author attribution. Implemented by service.GitIdentityService.
// Returns ("","",nil) when the user has no identity to inject (zero / unknown
// userID); caller treats that as "skip injection" so git falls back to OS
// config — preserving personal-edition behavior.
// Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md §3.1
type GitIdentityResolver interface {
	ResolveNameEmail(ctx context.Context, userID int64) (name, email string, err error)
}

// PermissionGate bridges codex `approval/request` notifications to niuniu's
// PermissionService so codex workspaces share the same approval UI / SSE
// cards / persistence as Claude's MCP-tool path. Implemented by
// service.PermissionService.
//
// approvedBehavior result: "allow" -> codex receives approved; anything else
// (including "deny", "timeout", "cancelled") -> codex receives denied.
// Returning err != nil bubbles up to the codex turn and triggers turn-fail
// handling (treated as denial for safety).
type PermissionGate interface {
	Request(
		ctx context.Context,
		workspaceID int64, ownerType string, ownerID int64,
		sessionID, toolName string, toolInput map[string]any,
	) (behavior string, err error)
}

type AgentProxy struct {
	sessions          map[int64]*WorkspaceSession
	hub               *SessionHub
	q                 *store.Queries
	cfg               *config.Config
	mu                sync.RWMutex
	stopOnce          sync.Once
	statusHook        StatusTransitioner
	eventBus          *event.Bus
	notifyHub         *notify.NotificationHub
	debouncer         *notify.Debouncer
	mcpWriter         MCPConfigWriter
	kbResolver        KBDatasetResolver // optional; nil = no KB direct-read exposure
	memoryFileWriter  MemoryFileWriter
	mcpSessions       MCPSessionManager
	bgDebouncer       *notify.BgTaskDebouncer
	usageRecorder     ClaudeUsageRecorder  // optional; nil = no-op
	rateLimitSched    RateLimitScheduler   // optional; nil = no auto-resume schedule
	sessionStateSvc   SessionStateRecorder // optional; nil = no-op
	workspaceAlertSvc WorkspaceAlertResolver
	stopCh            chan struct{}        // closed by Stop() to terminate gcInflightLoop
	serverSettings    ServerSettingsReader // optional; admin-tunable K/V reader
	gitIdentity       GitIdentityResolver  // optional; nil = no GIT_AUTHOR_* injection
	permissionGate    PermissionGate       // optional; nil = codex approval bridge auto-denies (safe default)
}

// SetPermissionGate wires the codex approval-bridge so approval/request
// notifications route through niuniu's PermissionService SSE flow. nil is
// safe: codex_exec will auto-deny any approval prompt instead of crashing.
func (p *AgentProxy) SetPermissionGate(g PermissionGate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.permissionGate = g
	for _, s := range p.sessions {
		s.permissionGate = g
	}
}

// SetGitIdentityResolver wires the per-user git author resolver. Optional;
// when nil the agent inherits OS-global git config (preserving personal-
// edition behavior). Newly-spawned sessions pick up the resolver via the
// shared AgentProxy reference; we don't propagate to live sessions because
// the agent is already running.
func (p *AgentProxy) SetGitIdentityResolver(r GitIdentityResolver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gitIdentity = r
	for _, s := range p.sessions {
		s.gitIdentity = r
	}
}

// SetUsageRecorder registers the claude-usage recorder. Called from server.New
// after both services are constructed.
func (p *AgentProxy) SetUsageRecorder(r ClaudeUsageRecorder) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usageRecorder = r
}

// SetRateLimitScheduler registers the auto-resume scheduler. Called from
// server.New after the scheduler is constructed. nil disables auto-resume.
func (p *AgentProxy) SetRateLimitScheduler(s RateLimitScheduler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rateLimitSched = s
}

// SetSessionStateRecorder registers the session-state snapshot recorder.
// Called from server.New after the service is constructed; back-fills any
// already-live sessions for parity with SetWorkspaceAlertResolver.
func (p *AgentProxy) SetSessionStateRecorder(r SessionStateRecorder) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessionStateSvc = r
	for _, s := range p.sessions {
		s.sessionStateSvc = r
	}
}

// streamPersistThrottle bounds how often a streaming text/thinking block flushes
// its accumulated content to the DB mid-stream. Every delta is still broadcast
// live to the UI; only the persisted snapshot is throttled. The full content is
// always flushed at content_block_stop / turn finalization, so throttling never
// loses data — it just collapses the per-delta UPDATE storm that otherwise
// saturates SQLite's single writer (acute with many concurrent agents, e.g. an
// Epic wave).
const streamPersistThrottle = 300 * time.Millisecond

// WorkspaceSession manages a long-lived Claude Code process for a workspace.
// The process stays alive across multiple messages (Mode C).
// If the process dies or the server restarts, it auto-recovers with --resume.
type WorkspaceSession struct {
	workspaceID      int64
	issueID          int64  // workspaces.issue_id, 0 if unlinked; cached at session start
	sessionId        string // Claude Code session ID (from init event or DB)
	workDir          string // workspace directory
	cliType          string // "claude" or "codex"; controls spawn and JSON parser
	cliAdapter       adapter.Adapter
	status           SessionStatus
	hub              *SessionHub
	q                *store.Queries
	cfg              *config.Config
	statusHook       StatusTransitioner
	eventBus         *event.Bus
	notifyHub        *notify.NotificationHub
	debouncer        *notify.Debouncer
	isTemporary      bool              // true for temporary workspaces (legacy: task analysis removed)
	mcpWriter        MCPConfigWriter   // generates .mcp.json for this workspace
	kbResolver       KBDatasetResolver // resolves bound KB read-only dataset dirs (KB base4)
	memoryFileWriter MemoryFileWriter  // generates .learnings.generated.md
	sessionToken     string            // raw MCP session token written into .mcp.json env

	mu             sync.Mutex
	running        bool               // true while a message is being processed
	turnUserMsgId  string             // ID of the current turn's persisted user message
	lastTurnError  bool               // true if last turn ended with error (attention state)
	lastTurnResult string             // result text from the most recent turn completion event
	cancel         context.CancelFunc // cancel the process context

	// Long-lived process state
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	reader    *bufio.Reader
	stderrBuf *limitedBuffer // captures stderr for diagnostics
	procMu    sync.Mutex     // guards process start/stop
	alive     bool           // true when process is running

	// Codex app-server process state. Kept separate from cmd/stdin/scanner so
	// Claude's stream-json path remains unchanged while Codex uses thread/turn
	// JSON-RPC.
	codexApp      *codexAppServerClient
	codexThreadID string

	// ompBackend is the reusable agentbackend.Backend driving omp workspaces
	// (RPC over `omp --mode rpc`). Lazily created on the first omp turn; owned
	// by the session. Guarded by s.mu.
	ompBackend agentbackend.Backend

	// gooseBackend is the reusable agentbackend.Backend driving goose workspaces
	// (ACP over `goose acp`). Lazily created on the first goose turn; owned by
	// the session. Guarded by s.mu.
	gooseBackend agentbackend.Backend

	// Per-turn state (reset each Send)
	turnDone  chan struct{} // signaled when result event arrives
	turnMsgId string        // current message correlation ID

	// lastActivityAt is bumped by readLoop on every line received from the
	// long-lived process. Send()'s inactivity watchdog reads it to tell a
	// genuinely long turn (steady stream output) apart from a wedged/lost
	// process (no output at all): only the latter is killed. Guarded by s.mu.
	lastActivityAt time.Time
	// turnInactivityTimeout overrides defaultTurnInactivityTimeout when > 0
	// (tests set a tiny value; production leaves it zero). Read once per wait.
	turnInactivityTimeout time.Duration

	// stopRequested is set by Stop() and checked by SendLoop every iteration so
	// a user-initiated stop actually TERMINATES the loop. Without it, killing
	// the process merely unblocks the in-flight Send via turnDone and SendLoop —
	// seeing a non-error turn — would autohost-continue and restart the process,
	// making Stop look like a no-op. Reset to false on SendLoop entry. s.mu.
	stopRequested bool

	// Per-turn accumulators (reset each Send)
	assistantTextBlocks []string // separate text blocks split by tool calls
	lastBlockWasTool    bool     // tracks whether to start a new text block
	thinkingContent     string
	toolUseNames        map[string]string // toolUseId → toolName
	toolUseIds          map[int]string    // blockIndex → toolUseId
	toolInputBufs       map[int]string    // blockIndex → accumulated input JSON
	textBlockBufs       map[int]string    // blockIndex → accumulated text content (for inline persistence)
	textBlockMessageIDs map[int]string    // blockIndex → persisted markdown message row id
	textBlockPersisted  map[int]bool      // blockIndex set when text deltas have already been persisted
	textBlockLastFlush  map[int]time.Time // blockIndex → last mid-stream DB UPDATE time (throttle)
	textBlockSeq        int               // sequence counter for text block IDs
	hasStreamEvents     bool
	thinkingPersisted   bool
	thinkingLastFlush   time.Time // last mid-stream thinking DB UPDATE time (throttle)
	thinkingMsgID       string    // stable row id for the turn's single thinking block

	// Idle timeout
	idleTimer *time.Timer

	// Pipeline integration
	activeRunID       int64  // tracks active harness run for cost attribution
	lastPhaseComplete string // detected phase marker from text events

	// Autohost watchdog state (see autohost.go).
	//   - autohostConsec: consecutive auto-continue turns since the last
	//     externally-sourced message. Reset by SendLoop on real user input.
	//   - autohostErrorConsec: consecutive auto-recovery attempts since the
	//     last clean (non-error) turn. Reset by SendLoop when a turn ends
	//     without lastTurnError.
	autohostConsec      int
	autohostErrorConsec int
	autohostGoalHint    string
	// autohostScheduledWait is set true by autohostMaybeContinue when, instead of
	// stopping, it scheduled a paced resume to WAIT for pending background work.
	// SendLoop reads it to keep the workspace "running" (not needs_review) at the
	// terminal finalize. Reset at the top of each autohostMaybeContinue and by
	// autohostReset. Guarded by s.mu.
	autohostScheduledWait bool

	// Task tracking state — survives across turns, reset only on new session init.
	// All maps are written from the single line-reader goroutine (handleStreamEvent
	// / handleAssistantFallback paths), so no s.mu is needed here. Concurrent
	// callers would need to add locking.
	taskIdMap          map[string]string // claudeTaskId → agent_task_id. Populated by TaskCreate result lines and by TodoWrite when todo.id is explicit.
	taskBatchId        string            // session batch ID, generated on init
	pendingTaskCreates map[string]bool   // toolUseId set for pending TaskCreate results

	// Usage tracking — updated from stream events and result events
	lastOutputTokens int                  // from message_delta or result
	lastRateLimit    *event.RateLimitData // from rate_limit_event
	modelName        string               // from NIUNIU_MODEL env or CLI

	// Auto-compaction state (see auto_compact.go). lastContextTokens is the live
	// context-window occupancy (uncached input + cache read + cache creation)
	// taken from each message_start event (one API request's prompt size) — the
	// signal the threshold heuristic reads. The result event is deliberately NOT
	// used: its usage is the turn-cumulative sum across every tool round-trip.
	// autoCompactSuppressed is set once an auto /compact has been injected
	// for the current high-water episode and re-armed only when occupancy falls
	// back below the threshold, so a no-op /compact can never loop. compactTurnActive
	// marks that the turn currently in flight is an auto-injected /compact, so the
	// autohost watchdog does not mistake the compaction summary for task completion.
	lastContextTokens     int
	autoCompactSuppressed bool
	compactTurnActive     bool

	// topLevelAgentID 是本会话对应的 workspace_agent.id（coordinator）。
	// 0 表示未绑定。Task 8 在 session init 时填充。用于 agent_message 归属。
	topLevelAgentID int64

	// workspaceAlertSvc resolves which user IDs should receive toast
	// notifications for workspace events (agent_done). Nil disables enrichment.
	workspaceAlertSvc WorkspaceAlertResolver

	// sessionStateSvc snapshots workspace state on idle and reports drift on
	// resume. Nil = feature disabled (e.g., in tests or pre-migration servers).
	sessionStateSvc SessionStateRecorder

	// driftChecked is set true after the first Send within a (re-)resumed
	// session has consulted SessionStateRecorder for external changes. Reset
	// when the CLI emits a system/init carrying a different session_id (i.e.,
	// a fresh session began). Guarded by s.mu.
	driftChecked bool

	// lastUserMsgContent caches the most recent user-typed content so the next
	// session-end snapshot can record what the agent was last asked to do.
	// Surfaces in the resume banner ("you last asked: …"). Guarded by s.mu.
	lastUserMsgContent string

	// inflight tracks in-flight background tasks for sidebar bg_tasks display.
	inflight *InflightTracker
	// parent is the owning AgentProxy; used to access the bg-task debouncer
	// without per-session pointer fanout (architect review C2).
	parent *AgentProxy

	// userID is the niuniu user that started this session (the value
	// passed to GetOrStartSession). Used by ResolveForWorkspace to derive
	// the visibility caller for spawned Claude CLI processes. The "current
	// message sender" in multi-user org workspaces may differ — this field
	// intentionally locks to *session start* identity.
	userID int64

	// permissionGate bridges codex `approval/request` notifications to niuniu
	// PermissionService. nil = auto-deny (safe default; PTY/managed niuniu
	// deployments always wire this from server.New).
	permissionGate PermissionGate

	// gitIdentity resolves the niuniu user's (name, email) for injecting
	// GIT_AUTHOR_*/GIT_COMMITTER_* into the Claude CLI subprocess env.
	// Optional; nil = inherit OS-global git config.
	gitIdentity GitIdentityResolver

	// serverSettings is the ServerSettingsReader injected from AgentProxy. The
	// autohost LLM judge that consumed it was removed; the seam is kept wired
	// (SetServerSettings) for future admin-tunable autohost settings. Nil in
	// tests/legacy construction.
	serverSettings ServerSettingsReader

	// rand is a per-session RNG seeded at construction (not shared with the
	// global rand, to avoid contention across concurrent sessions). Previously
	// drove the LLM-judge gray-rollout sampling; retained for any future
	// per-session sampling need.
	rand *rand.Rand

	// autohostChainID is allocated at the top of each SendLoop and reset on
	// defer, scoping autohost decisions to one user-initiated turn. ownerType/
	// ownerID cache the workspace's owner (used by the codex approval bridge).
	autohostChainID string
	ownerType       string
	ownerID         int64
}

const idleTimeout = 30 * time.Minute

// defaultTurnInactivityTimeout bounds how long a single turn may produce NO
// output at all before the watchdog treats the long-lived process as
// wedged/lost and kills it. A working agent streams events continuously (text /
// tool_use / tool_result / stream_event), so this measures INACTIVITY, not
// total turn duration — a legitimately long turn (e.g. a 700s build with steady
// output) keeps resetting the clock and is never killed, while a process that
// has gone silent (hung on a tool/MCP/network call, CLI deadlock, or simply
// lost) is reaped so SendLoop can recover instead of blocking on turnDone
// forever. Overridable per-session via WorkspaceSession.turnInactivityTimeout.
const defaultTurnInactivityTimeout = 15 * time.Minute

// phaseCompleteRe matches structured phase completion markers in Claude output.
var phaseCompleteRe = regexp.MustCompile(`<!--\s*PHASE:([\w-]+):COMPLETE\s*-->`)

var taskCreateResultRe = regexp.MustCompile(`Task #(\d+)`)

func NewAgentProxy(q *store.Queries, cfg *config.Config) *AgentProxy {
	p := &AgentProxy{
		sessions: make(map[int64]*WorkspaceSession),
		hub:      NewSessionHub(),
		q:        q,
		cfg:      cfg,
		stopCh:   make(chan struct{}),
	}
	go p.gcInflightLoop()
	return p
}

func (p *AgentProxy) GetOrStartSession(ctx context.Context, workspaceID int64, userID int64) (*WorkspaceSession, error) {
	p.mu.RLock()
	existing := p.sessions[workspaceID]
	p.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}

	ws, err := p.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[workspaceID]; s != nil {
		return s, nil
	}

	var sessionId string
	if ws.SessionID.Valid {
		sessionId = ws.SessionID.String
	}

	s := &WorkspaceSession{
		workspaceID:       workspaceID,
		issueID:           ws.IssueID.Int64, // 0 when invalid/NULL
		sessionId:         sessionId,
		workDir:           ws.Path,
		cliType:           normalizedCliType(ws.CliType),
		cliAdapter:        adapter.For(adapter.Type(normalizedCliType(ws.CliType))),
		status:            StatusIdle,
		hub:               p.hub,
		q:                 p.q,
		cfg:               p.cfg,
		statusHook:        p.statusHook,
		eventBus:          p.eventBus,
		notifyHub:         p.notifyHub,
		debouncer:         p.debouncer,
		isTemporary:       ws.IsTemporary == 1,
		mcpWriter:         p.mcpWriter,
		kbResolver:        p.kbResolver,
		memoryFileWriter:  p.memoryFileWriter,
		workspaceAlertSvc: p.workspaceAlertSvc,
		sessionStateSvc:   p.sessionStateSvc,
		inflight:          NewInflightTracker(),
		parent:            p,
		userID:            userID,
		permissionGate:    p.permissionGate,
		serverSettings:    p.serverSettings,
		gitIdentity:       p.gitIdentity,
		rand:              rand.New(rand.NewSource(time.Now().UnixNano() ^ workspaceID)),
		ownerType:         ws.OwnerType,
		ownerID:           ws.OwnerID,
		autohostChainID:   "", // allocated per SendLoop run (T1.8)
	}
	p.sessions[workspaceID] = s
	// Bind to the top-level workspace_agents row up front so that even messages
	// sent before the first system/init event are attributed correctly. If
	// sessionId is empty (fresh session), this falls back to the first
	// non-subagent row in the workspace; init will rebind once the CLI emits it.
	s.rebindTopLevelAgent(ctx)

	// Set session user identity (only when a real user initiated the session).
	// userID is 0 when the session is started by an autonomous caller — e.g.
	// scheduler.go and the gate/floor-gate paths pass 0.
	if userID > 0 {
		if err := p.q.UpdateWorkspaceSessionUser(ctx, store.UpdateWorkspaceSessionUserParams{
			CurrentSessionUserID: sql.NullInt64{Int64: userID, Valid: true},
			ID:                   workspaceID,
		}); err != nil {
			slog.Warn("set current_session_user_id for proxy session", "error", err, "workspaceID", workspaceID)
		}
	}
	// Always issue an MCP session token when the session manager is wired,
	// regardless of userID. Harness- and scheduler-started agents need a
	// token to call back into /mcp/* (those endpoints are gated by Bearer
	// auth — without the token every blackboard / phase / inbox call comes
	// back 401 and the run is functionally broken). Token validation only
	// resolves to a workspaceID; userID is read separately by MCPTokenAuth
	// from current_session_user_id and stays 0 for autonomous starts (the
	// /mcp/* routes don't enforce per-user authorization in personal mode).
	if p.mcpSessions != nil {
		if rawToken, err := p.mcpSessions.Create(ctx, workspaceID, 24*time.Hour); err != nil {
			slog.Warn("create MCP session token for proxy session", "error", err, "workspaceID", workspaceID)
		} else {
			s.sessionToken = rawToken
		}
	}

	return s, nil
}

func (p *AgentProxy) GetSession(workspaceID int64) *WorkspaceSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[workspaceID]
}

// LiveWorkspaceIDs returns the workspace IDs with a live proxy session. A proxy
// session persists across per-turn subprocess spawns (agent_status flips to
// "idle" between turns while the session stays alive), so this in-memory map —
// not the DB status — is the correct liveness signal for the server's MCP-token
// heartbeat, which keeps live workspaces' session tokens from expiring during
// idle 停留 gaps.
func (p *AgentProxy) LiveWorkspaceIDs() []int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]int64, 0, len(p.sessions))
	for wsID := range p.sessions {
		out = append(out, wsID)
	}
	return out
}

// effectiveGitUserID resolves the niuniu user whose git identity should author
// commits for the CURRENT turn. It must NOT use the session's frozen s.userID
// alone: the interactive chat path reaches the proxy via Deliver(userID=0), so
// s.userID is 0 (injection silently dropped), and on a shared workspace it locks
// to whoever *started* the session — so every member's commits got misattributed
// to the starter ("团队版 git 作者署名串了"). The real sender of each turn is
// written to workspaces.current_session_user_id by SetSessionUser before every
// Deliver, so that column is the authoritative per-turn author. Resolution order:
//  1. current_session_user_id — the user who sent this turn
//  2. s.userID — an explicit non-zero start (PTY / GetOrStartSession)
//  3. workspace owner when owner_type=user — stable per-workspace fallback
//
// Returns 0 when none apply (autonomous run on an org-owned workspace with no
// recorded sender) → the caller skips injection and git uses OS-global config.
func (s *WorkspaceSession) effectiveGitUserID(ctx context.Context) int64 {
	if ws, err := s.q.GetWorkspace(ctx, s.workspaceID); err == nil {
		if ws.CurrentSessionUserID.Valid && ws.CurrentSessionUserID.Int64 > 0 {
			return ws.CurrentSessionUserID.Int64
		}
	}
	if s.userID > 0 {
		return s.userID
	}
	if s.ownerType == "user" && s.ownerID > 0 {
		return s.ownerID
	}
	return 0
}

// SetSessionUser records the authenticated, interacting user as the workspace's
// current session user. Interactive chat sends reach the proxy through
// Deliver(userID=0) (and GetOrStartSession early-returns an existing session
// without touching the identity), so without this an interactively-started
// session keeps current_session_user_id = NULL. MCPTokenAuth then can't resolve
// auth_user_id on org-owned workspaces whose created_by is also unset, and every
// credential-scoped MCP tool (/mcp/external-*, /mcp/data-proxy, /mcp/dashboards)
// 401s — exactly the "team-edition external data source 401" failure.
//
// Mirrors the PTY path (service/agent.go), which already sets this at start.
// No-op for userID <= 0 so autonomous callers (scheduler / autohost / gate)
// never overwrite a real identity with 0.
func (p *AgentProxy) SetSessionUser(ctx context.Context, workspaceID, userID int64) {
	if userID <= 0 {
		return
	}
	if err := p.q.UpdateWorkspaceSessionUser(ctx, store.UpdateWorkspaceSessionUserParams{
		CurrentSessionUserID: sql.NullInt64{Int64: userID, Valid: true},
		ID:                   workspaceID,
	}); err != nil {
		slog.Warn("set current_session_user_id for interactive send", "error", err, "workspaceID", workspaceID)
	}
}

// Deliver sends content to the workspace agent. If the session is currently
// running, the message is queued via workspace_queue and (true, queueID, nil)
// is returned. If the session is idle a SendLoop is started and (false, 0, nil)
// is returned. Callers that need neither value may ignore the returns.
func (p *AgentProxy) Deliver(ctx context.Context, workspaceID int64, workDir, content, attachments string) (queued bool, queueID int64, err error) {
	if workDir == "" || content == "" {
		return
	}
	sess, err := p.GetOrStartSession(ctx, workspaceID, 0)
	if err != nil || sess == nil {
		err = fmt.Errorf("agent session unavailable: %w", err)
		slog.Warn("deliver: agent session unavailable", "workspaceID", workspaceID, "error", err)
		return
	}
	queued, queueID, err = sess.Enqueue(ctx, content, attachments)
	if err != nil {
		slog.Warn("deliver: enqueue failed", "workspaceID", workspaceID, "error", err)
		return
	}
	if queued {
		slog.Info("deliver: message queued (session running)", "workspaceID", workspaceID)
		return
	}
	go sess.SendLoop(context.Background(), workDir, content, attachments)
	slog.Info("deliver: started send loop", "workspaceID", workspaceID)
	return
}

// PrepareUserSend opens the Enqueue gate for a genuine manual user send when the
// workspace only LOOKS idle but a scheduled autohost resume / pending wakeup is
// still holding the queue closed. Call it from the manual send path BEFORE
// Deliver so the message starts a fresh loop immediately instead of silently
// queueing until the scheduled resume fires. Must NOT be called from the
// scheduler resume / dispatch paths (those legitimately queue behind a wait).
// No-op when no session exists or a live loop is running.
// See WorkspaceSession.userTakeoverClearWait.
func (p *AgentProxy) PrepareUserSend(ctx context.Context, workspaceID int64) {
	sess := p.GetSession(workspaceID)
	if sess == nil {
		return
	}
	sess.userTakeoverClearWait(ctx)
}

// RemoveSession stops and removes the session for a workspace, freeing all
// associated resources (process, replay buffer, SSE subscribers).
// Safe to call even if no session exists.
func (p *AgentProxy) RemoveSession(ctx context.Context, workspaceID int64) {
	p.mu.Lock()
	session := p.sessions[workspaceID]
	delete(p.sessions, workspaceID)
	p.mu.Unlock()

	if session != nil {
		session.killProcess()
		// Clean up generated .mcp.json to avoid stale configs
		if session.workDir != "" {
			os.Remove(filepath.Join(session.workDir, ".mcp.json"))
		}
		if session.inflight != nil {
			session.inflight.Clear()
		}
	}

	// Clear session user identity and revoke MCP token
	if err := p.q.UpdateWorkspaceSessionUser(ctx, store.UpdateWorkspaceSessionUserParams{
		CurrentSessionUserID: sql.NullInt64{Valid: false},
		ID:                   workspaceID,
	}); err != nil {
		slog.Warn("clear current_session_user_id on remove session", "error", err, "workspaceID", workspaceID)
	}
	if p.mcpSessions != nil {
		if err := p.mcpSessions.Revoke(ctx, workspaceID); err != nil {
			slog.Warn("revoke MCP session on remove session", "error", err, "workspaceID", workspaceID)
		}
	}

	p.hub.RemoveWorkspace(workspaceID)
}

func (p *AgentProxy) GetHub() *SessionHub                      { return p.hub }
func (p *AgentProxy) Q() *store.Queries                        { return p.q }
func (p *AgentProxy) SetStatusHook(h StatusTransitioner)       { p.statusHook = h }
func (p *AgentProxy) SetEventBus(bus *event.Bus)               { p.eventBus = bus }
func (p *AgentProxy) SetMCPWriter(w MCPConfigWriter)           { p.mcpWriter = w }
func (p *AgentProxy) SetKBResolver(r KBDatasetResolver)        { p.kbResolver = r }
func (p *AgentProxy) SetMemoryFileWriter(svc MemoryFileWriter) { p.memoryFileWriter = svc }
func (p *AgentProxy) SetNotifyHub(hub *notify.NotificationHub) {
	p.notifyHub = hub
	p.debouncer = notify.NewDebouncer(hub, 500*time.Millisecond)
	p.bgDebouncer = notify.NewBgTaskDebouncer(200*time.Millisecond, hub.Broadcast)
}

// SetMCPSessionService wires the MCP session service for token lifecycle management.
func (p *AgentProxy) SetMCPSessionService(svc MCPSessionManager) {
	p.mcpSessions = svc
}

// SetServerSettings wires the admin-tunable K/V settings reader. Safe to call
// any time; back-fills already-live sessions. Sessions may read
// s.serverSettings without holding p.mu, so back-fill under each session's own
// mutex to avoid a data race.
func (p *AgentProxy) SetServerSettings(r ServerSettingsReader) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serverSettings = r
	for _, s := range p.sessions {
		s.mu.Lock()
		s.serverSettings = r
		s.mu.Unlock()
	}
}

// SetWorkspaceAlertResolver wires the service that computes should_alert_user_ids
// for workspace toast events (agent_done). Called by server.New after both
// AgentProxy and WorkspaceService are constructed.
//
// CONCURRENCY CONTRACT: this setter must be called during boot, before any
// agent sessions start. Reads of WorkspaceSession.workspaceAlertSvc occur
// from the line-reader goroutine without holding p.mu — safe only because
// the field is set exactly once at startup and never mutated again. If a
// future caller wants to swap the resolver post-boot, wrap session-side
// reads in s.mu first.
func (p *AgentProxy) SetWorkspaceAlertResolver(svc WorkspaceAlertResolver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workspaceAlertSvc = svc
	for _, s := range p.sessions {
		s.workspaceAlertSvc = svc
	}
}

// emitBgTask routes a workspace bg-task change through the debouncer. Called
// by WorkspaceSession after tracker mutations. The resolveOwner closure is
// invoked at fire time, so SQL lookups are bounded by the 200ms debounce.
func (p *AgentProxy) emitBgTask(workspaceID int64, resolveOwner func() (string, int64, bool)) {
	if p.bgDebouncer == nil {
		return
	}
	p.bgDebouncer.Notify(workspaceID, resolveOwner)
}

func (s *WorkspaceSession) Status() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *WorkspaceSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionId
}

func (s *WorkspaceSession) WorkDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workDir
}

// resolveAgentID returns the workspace_agent_id that should own the resulting
// agent_message row — the session's top-level agent.
func (s *WorkspaceSession) resolveAgentID(parentToolUseId string) int64 {
	return s.topLevelAgentID
}

// rebindTopLevelAgent is retained as a no-op after the workspace_agents table
// was decommissioned (team_dispatch / multi-agent orchestration removed). There
// is no longer a per-agent row to bind to, so s.topLevelAgentID stays 0 and
// messages are attributed at the workspace grain. Kept as a method so the call
// sites in the session lifecycle remain stable.
func (s *WorkspaceSession) rebindTopLevelAgent(ctx context.Context) {
	_ = ctx
}

// ClaudeStatus holds computed usage data for the /claude-status API.
// Mirrors the JSON schema from Claude Code's statusline API.
type ClaudeStatus struct {
	Model         *ClaudeStatusModel      `json:"model,omitempty"`
	ContextWindow *ClaudeStatusCtx        `json:"context_window,omitempty"`
	Cost          *ClaudeStatusCost       `json:"cost,omitempty"`
	RateLimits    *ClaudeStatusRateLimits `json:"rate_limits,omitempty"`
}

type ClaudeStatusModel struct {
	DisplayName string `json:"display_name"`
}

type ClaudeStatusCtx struct {
	UsedPercentage float64 `json:"used_percentage"`
	InputTokens    int     `json:"input_tokens"`
	MaxTokens      int     `json:"max_tokens"`
}

type ClaudeStatusCost struct {
	TotalCostUSD float64 `json:"total_cost_usd"`
}

type ClaudeStatusRateLimits struct {
	FiveHour *ClaudeStatusRateWindow `json:"five_hour,omitempty"`
}

type ClaudeStatusRateWindow struct {
	Status   string `json:"status"`              // "allowed", "allowed_warning", "rejected"
	ResetsAt int64  `json:"resets_at,omitempty"` // unix timestamp (seconds)
	Overage  string `json:"overage_status,omitempty"`
}

// contextWindowSize returns the max context tokens for a given model name, 0
// when unknown. Substring matching (not exact) so vendor suffixes / snapshot
// dates still hit. Order matters: check vendor-specific patterns before the
// generic Claude family, and check [1m]-style long-window markers first within
// a vendor. Curated from each platform's docs / LiteLLM's
// model_prices_and_context_window.json — extend as new models ship.
func contextWindowSize(model string) int {
	m := strings.ToLower(model)
	switch {
	// Explicit long-window marker wins (e.g. deepseek-v4-pro[1m], glm-5.1[1m]).
	case strings.Contains(m, "[1m]"), strings.HasSuffix(m, "-1m"):
		return 1_000_000
	// Claude family: 200K base (extended-window models carry the 1M marker above).
	case strings.Contains(m, "haiku"), strings.Contains(m, "sonnet"), strings.Contains(m, "opus"),
		strings.Contains(m, "claude"):
		return 200_000
	// DeepSeek: 128K base (V3.x/R1).
	case strings.Contains(m, "deepseek"):
		return 128_000
	// Qwen / 通义: 262K (4x of 65,536).
	case strings.Contains(m, "qwen"), strings.Contains(m, "qwq"):
		return 262_144
	// Kimi / Moonshot: 262K.
	case strings.Contains(m, "kimi"), strings.Contains(m, "moonshot"):
		return 262_144
	// GLM / 智谱: GLM-5.x ships a 1M window (user-verified on 火山方舟
	// glm-5.2); GLM-4.x stays 200K.
	case strings.Contains(m, "glm-5"), strings.Contains(m, "glm5"):
		return 1_000_000
	case strings.Contains(m, "glm"), strings.Contains(m, "chatglm"):
		return 200_000
	// MiniMax M-series: 1M.
	case strings.Contains(m, "minimax"):
		return 1_000_000
	// GPT / OpenAI: GPT-5 family 400K, GPT-4.x 128K.
	case strings.Contains(m, "gpt-5"), strings.Contains(m, "gpt5"):
		return 400_000
	case strings.Contains(m, "gpt-4"), strings.Contains(m, "gpt4"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return 128_000
	// Gemma / Gemini: 1M / 2M — default to 1M.
	case strings.Contains(m, "gemini"), strings.Contains(m, "gemma"):
		return 1_000_000
	default:
		return 0
	}
}

// GetClaudeStatus returns usage data computed from in-memory stream events + DB costs.
func (s *WorkspaceSession) GetClaudeStatus(ctx context.Context) *ClaudeStatus {
	s.mu.Lock()
	occupancy := s.lastContextTokens
	rl := s.lastRateLimit
	model := s.modelName
	s.mu.Unlock()

	status := &ClaudeStatus{}

	// Model
	if model != "" {
		status.Model = &ClaudeStatusModel{DisplayName: model}
	}

	// Context window: the displayed percentage reflects the live occupancy
	// (uncached input + cache read + cache creation), the SAME signal that drives
	// auto-compaction (see lastContextTokens). Using input_tokens alone under-reports
	// drastically for cached conversations — the replayed history sits in cache_read,
	// so the pill would read a few percent while compaction fired at 70%.
	//
	// The denominator is the workspace-configured budget (autoCompactBudget, env
	// NIUNIU_AUTO_COMPACT_BUDGET), NOT the model's physical window — so the pill and
	// the auto-compaction trigger measure occupancy against the exact same budget.
	// If a user sets the budget to 1000k, the pill reads 70% precisely when
	// compaction fires at 70%. With no override, autoCompactBudget returns its
	// own default (autoCompactDefaultBudget, 1M).
	maxTokens := s.autoCompactBudget(ctx)
	if occupancy > 0 {
		pct := float64(occupancy) / float64(maxTokens) * 100
		if pct > 100 {
			pct = 100
		}
		status.ContextWindow = &ClaudeStatusCtx{
			UsedPercentage: pct,
			InputTokens:    occupancy,
			MaxTokens:      maxTokens,
		}
	} else {
		// Always return context window info with max_tokens so frontend knows the limit
		status.ContextWindow = &ClaudeStatusCtx{
			MaxTokens: maxTokens,
		}
	}

	// Cumulative cost from DB
	costs, err := s.q.ListWorkspaceCosts(ctx, s.workspaceID)
	if err == nil {
		var total float64
		for _, c := range costs {
			total += c.CostUsd
		}
		if total > 0 {
			status.Cost = &ClaudeStatusCost{TotalCostUSD: total}
		}
	}

	// Rate limits from rate_limit_event. Dropped when the window has already
	// elapsed (see rateWindowOrNilIfStale) so we never surface a stale, past
	// reset time that no longer matches the live CLI.
	if w := rateWindowOrNilIfStale(rl, time.Now()); w != nil {
		status.RateLimits = &ClaudeStatusRateLimits{FiveHour: w}
	}

	return status
}

// rateWindowOrNilIfStale converts the last observed rate_limit_event into the
// status DTO, returning nil when it should not be surfaced. We only refresh
// s.lastRateLimit when a new event streams in, so a captured reset can outlive
// its window; once `now` passes ResetsAt the limit no longer applies and the
// stale time would mislead the pill. Mirrors the staleness filter in
// claude_usage.go (rateLimitFor). A zero ResetsAt means the event carried no
// reset — we keep the status (can't judge staleness, and an in-progress warning
// without a time is still meaningful).
func rateWindowOrNilIfStale(rl *event.RateLimitData, now time.Time) *ClaudeStatusRateWindow {
	if rl == nil || rl.Status == "" {
		return nil
	}
	if rl.ResetsAt > 0 && time.Unix(rl.ResetsAt, 0).Before(now) {
		return nil
	}
	return &ClaudeStatusRateWindow{
		Status:   rl.Status,
		ResetsAt: rl.ResetsAt,
		Overage:  rl.Overage,
	}
}

// Enqueue adds a message to the DB queue if the session is busy.
// Returns (true, queueID, nil) if queued, (false, 0, nil) if session is idle.
func (s *WorkspaceSession) Enqueue(ctx context.Context, content, attachmentsJSON string) (bool, int64, error) {
	// The busy gate mirrors finalizeSendLoopTurn's "keep the workspace running"
	// decision, NOT just the live SendLoop. A workspace can have no live loop
	// (s.running=false) yet still be "running" from the user's POV (persisted
	// agent_status='running', the badge the UI shows) because a resume is already
	// scheduled to re-drive it: an autohost paced bg-wait (autohostScheduledWait)
	// or a pending future agent wakeup. In both cases the scheduled resume
	// re-enters SendLoop via proxy.Deliver (scheduler.trigger -> Deliver), which
	// drains the queue, so queuing here is safe (the message is never stranded).
	//
	// Without this, a message landing in that window — a drag-into-instruct
	// dispatch (DispatchInstructColumn -> sendKickoff -> Deliver) OR a manual send
	// (SendMessage -> Deliver), both of which funnel through here — bypasses the
	// queue and spawns a SECOND SendLoop that races the scheduled resume (the
	// double-loop guard then drops one, losing either the message or the resume).
	s.mu.Lock()
	busy := s.running || s.autohostScheduledWait
	wsID := s.workspaceID
	s.mu.Unlock()
	if !busy && !s.hasPendingFutureWakeup() {
		return false, 0, nil
	}
	// DB calls outside mutex — running check above is sufficient gate
	maxPos, err := s.q.GetMaxQueuePosition(ctx, wsID)
	if err != nil {
		return false, 0, fmt.Errorf("get max queue position: %w", err)
	}
	item, err := s.q.CreateQueueItem(ctx, store.CreateQueueItemParams{
		WorkspaceID: wsID,
		Content:     content,
		Position:    maxPos + 1000,
		Source:      "user",
		Attachments: sql.NullString{String: attachmentsJSON, Valid: attachmentsJSON != ""},
	})
	if err != nil {
		return false, 0, fmt.Errorf("create queue item: %w", err)
	}
	slog.Info("agent enqueue: message queued to DB", "workspaceID", wsID, "queueId", item.ID)
	s.hub.Broadcast(wsID, NewOutputEvent(event.EventQueueUpdate, "", "", "system", wsID))
	return true, item.ID, nil
}

// SetActiveRunID sets the active run ID for tracking purposes.
func (s *WorkspaceSession) SetActiveRunID(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRunID = id
}

// ActiveRunID returns the active run ID.
func (s *WorkspaceSession) ActiveRunID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRunID
}

// BgTasks returns a snapshot of in-flight background tasks for this session.
// Safe to call concurrently. Returns empty slice when nothing is in flight.
func (s *WorkspaceSession) BgTasks() []BgTask {
	if s.inflight == nil {
		return nil
	}
	return s.inflight.Snapshot()
}

// cliProcessPid returns the OS PID of the live CLI process (claude/codex), or
// 0 if the process is not running. Read under procMu since cmd is mutated by
// start/kill paths in that lock's scope. Used by the bg-task GC probe — a
// non-positive return tells the probe to skip cleanly.
func (s *WorkspaceSession) cliProcessPid() int32 {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	if !s.alive || s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	pid := s.cmd.Process.Pid
	if pid <= 0 {
		return 0
	}
	return int32(pid)
}

// hasPendingFutureWakeup returns true iff inflight contains at least one
// ScheduleWakeup whose ScheduledFor is in the future. Used by
// finalizeSendLoopTurn to defer the needs_review transition when the agent
// is about to be auto-resumed by a wakeup. Bash[bg] and Subagent kinds do
// not auto-resume the main agent loop, so they are intentionally ignored.
func (s *WorkspaceSession) hasPendingFutureWakeup() bool {
	if s.inflight == nil {
		return false
	}
	now := time.Now()
	for _, t := range s.inflight.Snapshot() {
		if t.Kind == BgTaskWakeup && t.ScheduledFor.After(now) {
			return true
		}
	}
	return false
}

// clearPendingWakeups drops every pending future ScheduleWakeup from the inflight
// tracker. Called on a user-initiated Stop: an explicit interrupt is a full stop,
// so the agent's "check back later" timer must not keep holding the Enqueue gate
// closed (Enqueue queues while hasPendingFutureWakeup()). Without this, a manual
// send right after Stop would re-queue behind the stale wakeup and sit idle until
// that wakeup fires — exactly the "message only queues, can't interrupt" trap.
// The user is taking over now, so the planned self-wakeup is moot.
func (s *WorkspaceSession) clearPendingWakeups() {
	if s.inflight == nil {
		return
	}
	now := time.Now()
	for _, t := range s.inflight.Snapshot() {
		if t.Kind == BgTaskWakeup && t.ScheduledFor.After(now) {
			s.inflight.Remove(t.ToolUseID)
		}
	}
}

// userTakeoverClearWait opens the Enqueue gate for a genuine manual user send on
// a workspace that only LOOKS idle. After an autohost paced bg-wait
// (autohostScheduledWait) or an agent ScheduleWakeup, the live SendLoop returns
// and the session broadcasts EventIdle (so the chat input unlocks and the badge
// reads "idle"), yet the gate stays closed — autohostScheduledWait / a pending
// future wakeup keeps Enqueue queueing — and a one-shot resume schedule is queued
// to re-drive the loop later. The result the user sees: "workspace is idle but my
// message just queues and won't run now." A user actively sending is an explicit
// "take over" (same semantics as Stop): clear the gate-holding state, drop the
// pending wakeup, and cancel the queued auto-resume so this send starts a fresh
// SendLoop immediately instead of stranding behind the scheduled resume.
//
// Returns false (no-op) when a live loop is running — queueing behind it is
// correct — or when the gate was already open (truly idle). Mirrors Stop's
// clearPendingWakeups, additionally covering the autohostScheduledWait branch
// that Stop does NOT clear.
func (s *WorkspaceSession) userTakeoverClearWait(ctx context.Context) bool {
	s.mu.Lock()
	if s.running {
		// A SendLoop is live: the message must queue behind it. Do not touch the
		// wait flags — clearing autohostScheduledWait here would let finalize flip
		// the workspace out of "running" mid-chain.
		s.mu.Unlock()
		return false
	}
	hadWait := s.autohostScheduledWait
	s.autohostScheduledWait = false
	s.mu.Unlock()

	hadWakeup := s.hasPendingFutureWakeup()
	s.clearPendingWakeups()
	if !hadWait && !hadWakeup {
		// Gate was already open — a normal idle send. Nothing was holding it.
		return false
	}
	// Cancel the one-shot resume so it can't double-drive the loop after the
	// user's own send starts. nil scheduler (tests / legacy) = nothing to cancel.
	if s.parent != nil {
		s.parent.mu.RLock()
		sched := s.parent.rateLimitSched
		s.parent.mu.RUnlock()
		if sched != nil {
			sched.CancelAutoResume(ctx, s.workspaceID)
		}
	}
	slog.Info("user takeover: cleared scheduled wait/wakeup + cancelled auto-resume so manual send runs now",
		"workspaceID", s.workspaceID, "hadWait", hadWait, "hadWakeup", hadWakeup)
	return true
}

// hasPendingBgWork reports whether the workspace has any live background task in
// flight: a Bash[bg] shell, a Subagent (Task), or a future ScheduleWakeup. This
// is the "task is not really done" signal for autohost: while bg work is
// pending, the watchdog must not let the continue-budget force a stop (it would
// abandon an in-progress build/subagent), and finalizeSendLoopTurn must not flip
// the workspace out of "running". Liveness is kept honest elsewhere: the
// gcInflightLoop process-tree probe clears dead Bash shells, and GCStale sweeps
// expired wakeups / zombie subagents — so a stuck entry self-clears within
// bounds and the normal budget gate resumes.
func (s *WorkspaceSession) hasPendingBgWork() bool {
	if s.inflight == nil {
		return false
	}
	now := time.Now()
	for _, t := range s.inflight.Snapshot() {
		switch t.Kind {
		case BgTaskBash, BgTaskSubagent:
			return true
		case BgTaskWakeup:
			if t.ScheduledFor.After(now) {
				return true
			}
		}
	}
	return false
}

// soonestFutureWakeupTime returns the earliest ScheduledFor among future
// wakeups, so the autohost bg-wait resume can be paced to the cadence the agent
// itself chose. ok=false when there is no future wakeup.
func (s *WorkspaceSession) soonestFutureWakeupTime() (time.Time, bool) {
	if s.inflight == nil {
		return time.Time{}, false
	}
	now := time.Now()
	var soonest time.Time
	found := false
	for _, t := range s.inflight.Snapshot() {
		if t.Kind == BgTaskWakeup && t.ScheduledFor.After(now) {
			if !found || t.ScheduledFor.Before(soonest) {
				soonest = t.ScheduledFor
				found = true
			}
		}
	}
	return soonest, found
}

// scheduleBgWaitResume asks the auto-resume scheduler to re-enter this workspace
// later (sending the autohost continue prompt) so the watchdog can keep waiting
// for in-flight background work WITHOUT busy-continuing. Resume time = the
// agent's soonest future wakeup, else now + autohostBgPollInterval. Returns false
// when no scheduler is wired (tests / legacy) — the caller then falls back to the
// normal budget stop so the loop stays bounded. On success it sets
// autohostScheduledWait so finalizeSendLoopTurn keeps the workspace "running".
func (s *WorkspaceSession) scheduleBgWaitResume(ctx context.Context) bool {
	if s.parent == nil || s.isTemporary {
		return false
	}
	s.parent.mu.RLock()
	sched := s.parent.rateLimitSched
	s.parent.mu.RUnlock()
	if sched == nil {
		return false
	}
	runAt, ok := s.soonestFutureWakeupTime()
	if !ok {
		runAt = time.Now().Add(autohostBgPollInterval)
	}
	// Floor the resume far enough out that THIS SendLoop has unwound to idle
	// before it fires — otherwise the scheduler's trigger() sees StatusRunning,
	// skips the one-shot resume, and (since finalize keeps us running) the
	// workspace would be stranded. The agent's own near-term wakeup cadence is
	// clamped up to this floor.
	if min := time.Now().Add(autohostMinResumeDelay); runAt.Before(min) {
		runAt = min
	}
	if !sched.OnAutohostWait(ctx, s.workspaceID, runAt.Unix(), s.readAutohostContinuePrompt(ctx)) {
		// Scheduling failed — don't keep the workspace running with no resume.
		// Let the caller fall back to the normal bounded stop.
		return false
	}
	s.mu.Lock()
	s.autohostScheduledWait = true
	s.mu.Unlock()
	s.emitAutohostSystemInfo(ctx, autohostBgWaitMsg)
	slog.Info("autohost: bg work pending at budget — scheduled paced resume instead of stopping",
		"workspace_id", s.workspaceID, "resume_at", runAt)
	return true
}

// recordBgTaskUse adds an in-flight entry when a Bash[bg]/Task/ScheduleWakeup
// tool_use completes (input is fully buffered). KillBash takes the inverse
// path: it does not add an entry, it removes the bash entry whose BashID
// matches the kill target. Called from handleStreamEvent.
func (s *WorkspaceSession) recordBgTaskUse(toolName, toolUseID, input string) {
	if s.inflight == nil || toolUseID == "" {
		return
	}
	now := time.Now()
	switch toolName {
	case "Bash":
		_, title, ok := parseBashBackground(input)
		if !ok {
			return
		}
		s.inflight.Add(BgTaskBash, toolUseID, title, now)
		s.emitBgTaskNotify()
	case "Task":
		desc, ok := parseSubagentDescription(input)
		if !ok {
			return
		}
		s.inflight.Add(BgTaskSubagent, toolUseID, desc, now)
		s.emitBgTaskNotify()
	case "ScheduleWakeup":
		delay, reason, ok := parseScheduleWakeup(input)
		if !ok {
			return
		}
		s.inflight.AddWakeup(toolUseID, reason, now, delay)
		s.emitBgTaskNotify()
	case "KillBash":
		// Inverse op: KillBash does NOT add an entry. It removes the bash
		// entry whose BashID matches the kill target (looked up via the
		// shell_id captured into the entry by recordBgTaskResult when the
		// spawn ack arrived).
		shellID, ok := parseKillBashInput(input)
		if !ok {
			return
		}
		if s.inflight.RemoveByBashID(shellID) {
			s.emitBgTaskNotify()
		}
	}
}

// recordBgTaskResult reacts to a tool_result whose tool_use_id matches a
// tracked entry. Removal is kind-aware because tool_result has different
// semantics per kind:
//
//   - Subagent (Task): the tool_result is the subagent's final output. The
//     task is genuinely done — remove the entry.
//   - Bash (run_in_background:true): the tool_result is the spawn ack
//     ("Command running in background with ID: <bash_id>"); the command
//     itself is still running. Removing here would zero out bash_count
//     within milliseconds of the start, hiding active bg work from the
//     sidebar. Instead we capture the shell_id into the entry so a later
//     KillBash[shell_id] can remove it, and let GCStale (1h) sweep the
//     rest as zombies.
//   - Wakeup: the tool_result is the schedule ack; the wakeup hasn't fired.
//     Leave the entry; GCStale clears it once ScheduledFor passes.
//
// isError=true short-circuits the "leave it for GC" path: a bash whose spawn
// was denied (permission prompt) or whose input failed validation never
// produces a real bg shell, and a wakeup whose schedule was rejected won't
// fire. Treat those tool_results as definitive removals so the sidebar
// doesn't show 1h-long zombies for non-existent work.
//
// Get→Set/Remove is intentionally non-atomic across the tracker mutex (Get
// takes RLock, Set/Remove take Lock). A concurrent GCStale interleaving is
// the only realistic race; if it deletes the entry first, the sequel
// SetBashID/Remove no-ops, which is the correct outcome.
//
// No-op for unknown ids.
func (s *WorkspaceSession) recordBgTaskResult(toolUseID, content string, isError bool) {
	if s.inflight == nil || toolUseID == "" {
		return
	}
	task, ok := s.inflight.Get(toolUseID)
	if !ok {
		return
	}
	switch task.Kind {
	case BgTaskSubagent:
		if s.inflight.Remove(toolUseID) {
			s.emitBgTaskNotify()
		}
	case BgTaskBash:
		if isError {
			if s.inflight.Remove(toolUseID) {
				s.emitBgTaskNotify()
			}
			return
		}
		if task.BashID != "" {
			return
		}
		if bashID, ok := parseBashSpawnResult(content); ok {
			s.inflight.SetBashID(toolUseID, bashID)
		}
	case BgTaskWakeup:
		if isError {
			if s.inflight.Remove(toolUseID) {
				s.emitBgTaskNotify()
			}
			return
		}
		// no-op; GCStale handles cleanup at ScheduledFor
	}
}

// emitBgTaskNotify routes the change through the parent AgentProxy's debouncer.
// Owner is resolved at fire time (not at call time) so the 200ms debounce
// caps GetWorkspace SQL rate to ≤5/s/workspace. v3 + I-1 fix.
func (s *WorkspaceSession) emitBgTaskNotify() {
	if s.parent == nil {
		return
	}
	wsID := s.workspaceID
	q := s.q
	s.parent.emitBgTask(wsID, func() (string, int64, bool) {
		w, err := q.GetWorkspace(context.Background(), wsID)
		if err != nil {
			slog.Warn("emitBgTaskNotify: GetWorkspace failed",
				"workspace_id", wsID, "err", err)
			return "", 0, false
		}
		if w.OwnerType == "" {
			slog.Warn("emitBgTaskNotify: skipping — empty owner_type",
				"workspace_id", wsID)
			return "", 0, false
		}
		return w.OwnerType, w.OwnerID, true
	})
}

// LastPhaseComplete returns and clears the detected phase completion marker.
func (s *WorkspaceSession) LastPhaseComplete() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	phase := s.lastPhaseComplete
	s.lastPhaseComplete = ""
	return phase
}

// TurnUserMsgId returns the DB ID of the current turn's user message.
func (s *WorkspaceSession) TurnUserMsgId() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnUserMsgId
}

// appendTextContent appends text to the current text block, or starts a new one
// if the previous event was a tool call. Must be called with s.mu held.
func (s *WorkspaceSession) appendTextContent(text string) {
	if s.lastBlockWasTool || len(s.assistantTextBlocks) == 0 {
		s.assistantTextBlocks = append(s.assistantTextBlocks, text)
		s.lastBlockWasTool = false
	} else {
		s.assistantTextBlocks[len(s.assistantTextBlocks)-1] += text
	}
}

// joinedAssistantContent returns all text blocks joined. Must be called with s.mu held.
func (s *WorkspaceSession) joinedAssistantContent() string {
	result := ""
	for _, b := range s.assistantTextBlocks {
		result += b
	}
	return result
}

// GetAccumulatedOutput returns the assistant output accumulated in the current turn.
func (s *WorkspaceSession) GetAccumulatedOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.joinedAssistantContent()
}

func (s *WorkspaceSession) PendingCount(ctx context.Context) int {
	count, err := s.q.CountQueueItems(ctx, s.workspaceID)
	if err != nil {
		return 0
	}
	return int(count)
}

func (s *WorkspaceSession) dequeue(ctx context.Context) (string, string) {
	wsID := s.workspaceID
	// DB call outside mutex — atomic SQL provides safety
	item, err := s.q.DequeueMessage(ctx, wsID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("agent dequeue: DB error", "workspaceID", wsID, "err", err)
		}
		return "", ""
	}
	slog.Info("agent dequeue: got message from DB queue", "workspaceID", wsID, "queueId", item.ID)
	s.hub.Broadcast(wsID, NewOutputEvent(event.EventQueueUpdate, "", "", "system", wsID))
	return item.Content, item.Attachments.String
}

// SendLoop sends one message and then processes any queued messages.
// Stops on error (attention state) per spec: "停止 SendLoop，等待定时任务或手动触发".
//
// When autohost mode is enabled (NIUNIU_PERMISSION_MODE=autohost):
//   - An empty queue at turn-end triggers the continue watchdog
//     (autohostMaybeContinue), bounded by NIUNIU_AUTOHOST_BUDGET.
//   - A turn ending with lastTurnError triggers the recovery watchdog
//     (autohostMaybeRecover), bounded by NIUNIU_AUTOHOST_ERROR_BUDGET.
//
// Each watchdog firing emits a system-info event so the chat log shows
// what the watchdog did and how much budget remains.
func (s *WorkspaceSession) SendLoop(ctx context.Context, workDir, content, attachmentsJSON string) {
	// Panic boundary FIRST: a panic on this goroutine (e.g. a Stop racing the
	// turn teardown) would otherwise crash the whole server. Recover + log the
	// stack, then reset the loop state so the workspace doesn't get stuck
	// "running".
	defer func() {
		if r := recover(); r != nil {
			slog.Error("agentproxy: recovered panic in SendLoop (contained; would have crashed the server)",
				"workspaceID", s.workspaceID, "panic", r, "stack", string(debug.Stack()))
			s.mu.Lock()
			s.running = false
			s.status = StatusIdle
			s.mu.Unlock()
			s.emitBgTaskNotify()
		}
	}()
	// running/status is LOOP-scoped: it represents "a SendLoop is alive for this
	// session" and stays true across every turn AND the autohost watchdog decision
	// that runs between turns. This is the single gate the dispatcher (Enqueue) and
	// the scheduler (Status) consult to decide busy-vs-idle. Previously running was
	// toggled per-turn (set in Send, cleared on the result event), which left a
	// gap between turns where an incoming message saw the session "idle" and
	// the dispatcher spawned a SECOND SendLoop on the same session — two loops
	// racing the same stdin/turnDone/process, an unrecoverable corruption that
	// only a server restart cleared. Owning the flag for the whole loop closes
	// that gap. Send() and the turn-completion handlers no longer touch
	// running/status (only turnDone + lastTurnResult per turn).
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		slog.Warn("agent SendLoop: a loop is already active for this session; dropping concurrent start",
			"workspaceID", s.workspaceID)
		return
	}
	s.running = true
	s.status = StatusRunning
	s.stopRequested = false // fresh loop; clear any stop left by a prior loop
	s.mu.Unlock()
	s.emitBgTaskNotify()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.status = StatusIdle
		s.mu.Unlock()
		s.emitBgTaskNotify()
		// Authoritative loop-end signal. running is loop-scoped (true across the
		// whole autohost chain), but the per-turn `result`→done/error SSE events
		// fire on EVERY turn — so the frontend must NOT infer idle from them or it
		// unlocks the input mid-chain while the backend still queues (the two gates
		// drift). EventIdle here is the single edge that flips the SPA's session
		// gate to idle, and the defer guarantees it on EVERY exit path (clean /
		// error / watchdog kill / stop / paced-wait), so the input can never stay
		// wedged "running" after the loop has actually ended.
		s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventIdle, "", "", "system", s.workspaceID))
	}()

	// Initial content is externally sourced — reset autohost counters.
	s.autohostReset()
	s.autohostResetErrors()
	s.mu.Lock()
	s.autohostGoalHint = content
	s.mu.Unlock()

	// Allocate a fresh autohost chain_id for the duration of this SendLoop, so
	// autohost decisions made across this turn's consecutive continues share one
	// chain identity. Reset on defer so an idle session does not retain a stale
	// UUID.
	s.mu.Lock()
	s.autohostChainID = uuid.NewString()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.autohostChainID = ""
		s.mu.Unlock()
	}()
	// injected = the current content is an auto-injected autohost continue/recover
	// prompt (Send renders it as a system message, not a "You" bubble), rather than
	// a real user / queue / scheduler message. Initial content is a real dispatch.
	injected := false
	for {
		// A user-initiated Stop sets stopRequested and kills the process; bail
		// out before starting (or continuing into) another turn so the loop
		// actually terminates instead of autohost-continuing the killed process.
		s.mu.Lock()
		stopReq := s.stopRequested
		s.mu.Unlock()
		if stopReq {
			slog.Info("agent sendLoop: stop requested — terminating loop", "workspaceID", s.workspaceID)
			return
		}

		if err := s.Send(ctx, workDir, content, attachmentsJSON, injected); err != nil {
			slog.Error("agent sendLoop: send error", "workspaceID", s.workspaceID, "err", err)
			s.finalizeSendLoopTurn(ctx, true, err.Error(), false)
			return
		}

		// Stop may have fired while Send was blocked; killProcess unblocks Send
		// via turnDone, so re-check before treating the turn as a real result.
		s.mu.Lock()
		stopReq = s.stopRequested
		s.mu.Unlock()
		if stopReq {
			slog.Info("agent sendLoop: stop requested after turn — terminating loop", "workspaceID", s.workspaceID)
			return
		}

		s.mu.Lock()
		wasError := s.lastTurnError
		lastResult := s.lastTurnResult
		s.mu.Unlock()
		if wasError {
			// Auto-recover when autohost is on and error budget remains;
			// otherwise drop into attention.
			if ok, prompt := s.autohostMaybeRecover(ctx); ok {
				s.emitAutohostSystemInfo(ctx, "autohost: 上一轮错误，自动恢复重试")
				content = prompt
				attachmentsJSON = ""
				injected = true
				continue
			}
			slog.Info("agent sendLoop: stopping — last turn was error (attention)", "workspaceID", s.workspaceID)
			s.finalizeSendLoopTurn(ctx, true, lastResult, false)
			return
		}
		// Clean turn — reset error streak.
		s.autohostResetErrors()

		// Auto-compaction: when the live context window has crossed the configured
		// budget threshold, inject a one-shot /compact turn before anything else so
		// the freed context benefits the queued/next user turn (and any autohost
		// continue). Claude-only; the suppressed flag prevents re-triggering until
		// the context shrinks back under the threshold, so a no-op /compact can
		// never loop. Injected as a system turn (not a "You" bubble); the transient
		// ping explains it was automatic.
		if ok, prompt := s.maybeAutoCompact(ctx); ok {
			s.emitAutohostSystemInfo(ctx, autoCompactNoticeMsg)
			content = prompt
			attachmentsJSON = ""
			injected = true
			continue
		}

		next, nextAttach := s.dequeue(ctx)
		if next != "" {
			slog.Info("agent sendLoop: processing queued message", "workspaceID", s.workspaceID, "remaining", s.PendingCount(ctx))
			content = next
			attachmentsJSON = nextAttach
			injected = false
			// Real user/external input — reset auto-continue counter.
			s.autohostReset()
			continue
		}

		// Queue is empty. If autohost mode is on and budget remains,
		// auto-inject a continue prompt instead of going idle. Send renders the
		// injected prompt as a system message — so no extra ping here.
		if ok, prompt := s.autohostMaybeContinue(ctx); ok {
			content = prompt
			attachmentsJSON = ""
			injected = true
			continue
		}

		// The watchdog may have declined because a queued item landed
		// concurrently (e.g. an error-rollback 'retry') after the dequeue
		// above — autohostMaybeContinue yields to the queue in that case. Try
		// one more dequeue before going idle so the late-arriving item runs
		// now instead of sitting stranded until the next external trigger.
		if next, nextAttach = s.dequeue(ctx); next != "" {
			slog.Info("agent sendLoop: draining late-arrived queued message", "workspaceID", s.workspaceID, "remaining", s.PendingCount(ctx))
			content = next
			attachmentsJSON = nextAttach
			injected = false
			s.autohostReset()
			continue
		}

		// Reaching here on a clean turn means the autohost watchdog declined to
		// continue (or autohost is off). When autohost is active and the watchdog
		// genuinely STOPPED (LLM met / sentinel / budget with no bg work), this is
		// a terminal stop — finalize must transition even if a stale wakeup lingers.
		// But if the watchdog instead scheduled a paced resume to WAIT for pending
		// background work (autohostScheduledWait), the workspace must stay running.
		s.mu.Lock()
		scheduledWait := s.autohostScheduledWait
		s.mu.Unlock()
		autohostTerminalStop := !scheduledWait && s.readPermissionMode(ctx) == AutohostMode
		s.finalizeSendLoopTurn(ctx, false, lastResult, autohostTerminalStop)
		// EventIdle is broadcast by the SendLoop defer on return (single source of
		// truth for the loop-end gate), so no explicit broadcast is needed here.
		return
	}
}

func (s *WorkspaceSession) finalizeSendLoopTurn(ctx context.Context, isError bool, result string, autohostTerminalStop bool) {
	// 保持 running 不流转的两种情况 (仅对 clean 结束生效; isError 必须立刻置
	// attention 触发用户介入):
	//   1. autohostScheduledWait —— autohost 在 budget 耗尽且仍有后台任务时, 没有
	//      停机, 而是安排了一条定时 resume 稍后重判 (见 scheduleBgWaitResume)。
	//      到点 resume 会重新 SendLoop -> finalize, 那时后台任务做完了才真正流转。
	//   2. hasPendingFutureWakeup —— 非 autohost 路径的历史行为: agent 排了未到期
	//      wakeup, 先别翻 needs_review, 等下一轮 finalize。
	// 注意只挡这两种, 不挡「裸 Bash[bg]/Subagent 且非 autohost」—— 那条没有 resume
	// 机制, 挡了会把非 autohost 工作空间一直卡在 running 直到 1h GC (回归)。
	//
	// 例外: autohostTerminalStop (SendLoop 用 !scheduledWait && mode==autohost 算出)
	// —— autohost 判定真正完成 (met / sentinel) 后立即流转, 残留 stale wakeup 不挡。
	s.mu.Lock()
	scheduledWait := s.autohostScheduledWait
	s.mu.Unlock()
	if !isError && !autohostTerminalStop && (scheduledWait || s.hasPendingFutureWakeup()) {
		slog.Info("agent finalizeSendLoopTurn: deferring done — paced autohost wait / pending wakeup",
			"workspaceID", s.workspaceID, "scheduledWait", scheduledWait)
		return
	}
	// The loop is genuinely ending (idle/attention) — clear the compact-turn
	// marker so a stale flag can't make a later autohost session spuriously
	// "continue after compact". In autohost mode the flag is normally consumed by
	// autohostMaybeContinue before reaching here; this just covers the non-autohost
	// path that never reads it.
	s.mu.Lock()
	s.compactTurnActive = false
	s.mu.Unlock()
	// Notify workspace status hook (done vs error) only after SendLoop has made
	// its final autohost/queue decision. A clean turn may still be followed by
	// an automatic continue prompt, in which case the workspace must stay running.
	if s.statusHook != nil && !s.isTemporary {
		if isError {
			s.statusHook.OnAgentEvent(ctx, s.workspaceID, "error")
		} else {
			s.statusHook.OnAgentEvent(ctx, s.workspaceID, "done")
		}
	}

	// Publish to event bus for desktop SSE subscribers.
	// Skipped for temporary workspaces to avoid spurious agent_done events.
	if s.eventBus != nil && !s.isTemporary {
		if isError {
			s.eventBus.Publish(event.OutputEvent{
				Type: event.EventAgentFailed, Content: result,
				WorkspaceId: s.workspaceID, Ts: time.Now().UnixMilli(),
			})
		} else {
			// Carry the agent's actual final reply so IM / other bus consumers can
			// forward the real answer (not a fixed "done" template). result is the
			// turn's last assistant text (s.lastTurnResult); fall back to a sentinel
			// only when the turn produced no visible text.
			doneContent := strings.TrimSpace(result)
			if doneContent == "" {
				doneContent = "completed"
			}
			s.eventBus.Publish(event.OutputEvent{
				Type: event.EventAgentDone, Content: doneContent,
				WorkspaceId: s.workspaceID, Ts: time.Now().UnixMilli(),
			})
		}
	}

	// Broadcast via NotificationHub for WebSocket push.
	// Include the resulting workspace status so the frontend can show
	// the correct toast + sound (needs_review for done, attention for error).
	// Skipped for temporary workspaces to avoid noisy sidebar notifications.
	if s.notifyHub != nil && !s.isTemporary {
		finalStatus := "needs_review"
		if isError {
			finalStatus = "attention"
		}

		// Fetch workspace owner metadata once; needed by the hub for per-org
		// fanout filtering. A missing row is non-fatal — broadcast proceeds
		// with a zero owner (hub treats it as "all connections") rather than
		// silently dropping the event.
		ws, wsErr := s.q.GetWorkspace(ctx, s.workspaceID)
		if wsErr != nil {
			slog.Warn("agent_done: GetWorkspace failed; broadcasting with zero owner",
				"workspace_id", s.workspaceID, "error", wsErr)
		}

		// Compute alertable user IDs for the agent_done toast.
		var alertIDs []int64
		if s.workspaceAlertSvc != nil {
			ids, aErr := s.workspaceAlertSvc.AlertableUserIDs(ctx, s.workspaceID)
			if aErr != nil {
				slog.Warn("agent_done: AlertableUserIDs failed; muting alerts under alertScope='mine'",
					"workspace_id", s.workspaceID, "error", aErr)
				alertIDs = []int64{}
			} else {
				alertIDs = ids
			}
		} else {
			alertIDs = []int64{}
		}

		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicWorkspace,
			Action:    "agent_done",
			ID:        s.workspaceID,
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
			Extra: map[string]any{
				"status":                finalStatus,
				"should_alert_user_ids": alertIDs,
			},
		})
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicDiff,
			Action:    "changed",
			ID:        s.workspaceID,
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
		})
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicGitStatus,
			Action:    "changed",
			ID:        s.workspaceID,
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
		})
	}
}

// emitAutohostSystemInfo broadcasts a non-persisted system-info event so the
// chat UI can surface what the watchdog did. Not persisted: these are
// observational signals, not part of the dialogue history.
func (s *WorkspaceSession) emitAutohostSystemInfo(_ context.Context, text string) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventSystemInfo, text, "", "system", s.workspaceID))
}

// ensureProcess starts the long-lived Claude Code process if it's not already running.
// Uses --resume if we have a prior session ID (recovery after restart).
func (s *WorkspaceSession) ensureProcess(ctx context.Context, workDir string) error {
	s.procMu.Lock()
	defer s.procMu.Unlock()

	if s.alive && s.cmd != nil && s.cmd.Process != nil {
		return nil // already running
	}

	// Validate workspace directory exists before attempting to start
	if info, err := os.Stat(workDir); err != nil {
		slog.Error("agent: workspace directory does not exist",
			"workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		return fmt.Errorf("workspace directory not found: %w", err)
	} else if !info.IsDir() {
		slog.Error("agent: workspace path is not a directory",
			"workspaceID", s.workspaceID, "workDir", workDir)
		return fmt.Errorf("workspace path is not a directory: %s", workDir)
	}

	// CWD = workspace root directory (contains .worktrees/ with all repo worktrees)
	// Each worktree is added via --add-dir so Claude can access all repos
	slog.Info("agent: starting long-lived process", "workspaceID", s.workspaceID, "workDir", workDir)

	// Add each worktree directory so Claude can access all repos
	var worktreeDirs []string
	worktrees, wtErr := s.q.ListWorktrees(ctx, s.workspaceID)
	if wtErr != nil {
		slog.Warn("agent: failed to list worktrees", "workspaceID", s.workspaceID, "err", wtErr)
	} else {
		slog.Info("agent: found worktrees", "workspaceID", s.workspaceID, "count", len(worktrees))
		for _, wt := range worktrees {
			if _, err := os.Stat(wt.WorktreePath); err != nil {
				slog.Warn("agent: worktree path does not exist",
					"workspaceID", s.workspaceID, "path", wt.WorktreePath, "err", err)
			}
			worktreeDirs = append(worktreeDirs, wt.WorktreePath)
		}
	}

	s.mu.Lock()
	sessionId := s.sessionId
	s.mu.Unlock()

	// Read workspace env vars for CLI flag overrides
	agentCommand := s.cfg.Agent.ClaudeCode.Command
	agentExtraArgs := s.cfg.Agent.ClaudeCode.Args
	var customMCPConfig string
	var permissionMode string
	var userAllowedRaw string
	var model string

	wsEnvVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr == nil {
		for _, e := range wsEnvVars {
			switch e.Key {
			case "NIUNIU_AGENT_COMMAND":
				if e.Value != "" {
					agentCommand = e.Value
				}
			case "NIUNIU_AGENT_ARGS":
				if e.Value != "" {
					agentExtraArgs = strings.Fields(e.Value)
				}
			case "NIUNIU_PERMISSION_MODE":
				permissionMode = e.Value
			case "NIUNIU_MODEL":
				if e.Value != "" {
					model = e.Value
					s.mu.Lock()
					s.modelName = e.Value
					s.mu.Unlock()
				}
			case "NIUNIU_ALLOWED_TOOLS":
				userAllowedRaw = e.Value
			case "NIUNIU_MCP_CONFIG":
				if e.Value != "" {
					customMCPConfig = e.Value
				}
			}
		}
	}

	adapterExtraArgs := []string{}
	// Generate MCP config for this workspace so Claude can access niuniu tools.
	// Priority: NIUNIU_MCP_CONFIG env var > auto-generated.
	mcpConfigAttached := false
	if customMCPConfig != "" {
		adapterExtraArgs = append(adapterExtraArgs, "--mcp-config", customMCPConfig)
		mcpConfigAttached = true
	} else if s.mcpWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		mcpPath := filepath.Join(workDir, ".mcp.json")
		opts := config.MCPGenerateOptions{
			ProjectID:    projectID,
			WorkspaceID:  s.workspaceID,
			InboxDir:     filepath.Join(workDir, ".team", "inboxes"),
			SessionToken: s.sessionToken,
		}
		if err := s.mcpWriter.GenerateClaudeSettings(workDir); err != nil {
			slog.Warn("agent: failed to backfill .claude/settings.json",
				"workspaceID", s.workspaceID, "error", err)
		}
		if _, err := s.mcpWriter.Generate(workDir, opts, nil, ""); err != nil {
			slog.Warn("agent: failed to generate MCP config for workspace",
				"workspaceID", s.workspaceID, "error", err)
			if s.notifyHub != nil {
				// Fetch workspace owner metadata for hub fanout filtering.
				// A missing row is non-fatal — broadcast proceeds with a zero owner
				// (hub treats it as "all connections") rather than silently dropping.
				ws, wsErr := s.q.GetWorkspace(ctx, s.workspaceID)
				if wsErr != nil {
					slog.Warn("mcp_error: GetWorkspace failed; broadcasting with zero owner",
						"workspace_id", s.workspaceID, "error", wsErr)
				}
				s.notifyHub.Broadcast(notify.Notification{
					Topic:     notify.TopicWorkspace,
					Action:    "mcp_error",
					ID:        s.workspaceID,
					OwnerType: ws.OwnerType,
					OwnerID:   ws.OwnerID,
					Extra: map[string]string{
						"message": "MCP 配置生成失败，工作空间工具不可用。请运行 make build 构建。",
					},
				})
			}
		}
		// Only add --mcp-config if the file actually exists on disk.
		// Generate returns nil without writing when no MCP binary is found.
		if _, err := os.Stat(mcpPath); err == nil {
			adapterExtraArgs = append(adapterExtraArgs, "--mcp-config", mcpPath)
			mcpConfigAttached = true
		} else {
			slog.Warn("agent: .mcp.json not found after generation, starting without MCP tools",
				"workspaceID", s.workspaceID, "mcpPath", mcpPath)
		}
	}

	// Per-workspace strict MCP: when enabled, the agent ignores global ~/.claude
	// MCP config + plugins and uses ONLY the projected .mcp.json. Default off
	// (column default 0) preserves the global∪workspace union behavior.
	if mcpConfigAttached {
		if ws, err := s.q.GetWorkspace(ctx, s.workspaceID); err == nil && ws.StrictMcpConfig == 1 {
			adapterExtraArgs = append(adapterExtraArgs, "--strict-mcp-config")
		}
	}

	// KB base4 (the "C" ability): expose the workspace's bound knowledge-base
	// dataset directories to the agent for direct Read/Grep/Glob. Read access is
	// granted via permissions.additionalDirectories and write access is denied —
	// both written into .claude/settings.json by SetWorkspaceKBReadonly. We use
	// additionalDirectories rather than --add-dir on purpose: --add-dir would load
	// a dataset's .claude skills/agents/plugins (KB content is arbitrary, so that
	// is unsafe), whereas additionalDirectories grants file access only. The
	// instruction file is refreshed so the agent is told where the KBs are. All
	// best-effort: any failure logs and the spawn proceeds without KB exposure.
	if s.kbResolver != nil {
		kbDirs, kbErr := s.kbResolver.WorkspaceDatasetDirs(ctx, s.workspaceID)
		if kbErr != nil {
			slog.Warn("agent: resolve KB dataset dirs failed",
				"workspaceID", s.workspaceID, "error", kbErr)
		}
		kbRoots := kbDatasetRoots(kbDirs)
		if s.mcpWriter != nil {
			if err := s.mcpWriter.SetWorkspaceKBReadonly(workDir, kbRoots); err != nil {
				slog.Warn("agent: set KB read-only access failed",
					"workspaceID", s.workspaceID, "error", err)
			}
		}
		if err := writeKBInstructionBlock(workDir, s.cliType, kbDirs); err != nil {
			slog.Warn("agent: write KB instruction block failed",
				"workspaceID", s.workspaceID, "error", err)
		}
		if len(kbRoots) > 0 {
			slog.Info("agent: exposing KB dataset dirs (read-only)",
				"workspaceID", s.workspaceID, "count", len(kbRoots))
		}
	}

	// Compute permission-related CLI flags now that the MCP-config decision is
	// done. Centralized here so there's exactly one place that emits
	// --permission-mode / --permission-prompt-tool / --allowedTools.
	//
	// mcpAvailable must reflect whether a --mcp-config was actually attached,
	// not just whether s.mcpWriter is non-nil. Otherwise, when Generate fails
	// (e.g. niuniu-mcp binary not found), we'd still pass
	// --permission-prompt-tool mcp__niuniu__niuniu_permission_prompt to Claude
	// CLI without a matching MCP server, which exits with
	// "MCP tool ... not found. Available MCP tools: none".
	mcpAvailable := mcpConfigAttached
	if (permissionMode == "" || permissionMode == "default" || permissionMode == "acceptEdits") && !mcpAvailable {
		slog.Warn("agent: permission-prompt-tool skipped, niuniu MCP unavailable",
			"workspaceID", s.workspaceID)
	}
	driverAdapter := s.cliAdapter
	if driverAdapter == nil {
		driverAdapter = adapter.ClaudeAdapter{}
	}
	adapterExtraArgs = append(adapterExtraArgs, driverAdapter.PermissionArgs(adapter.PermissionOptions{
		Mode:           permissionMode,
		UserAllowedCSV: userAllowedRaw,
		MCPAvailable:   mcpAvailable,
	})...)

	// Auto-evolve then regenerate .learnings.generated.md for this workspace. The
	// evolution pass (supersede contradicted memories, archive stale ones) runs
	// silently at session start so the file the agent reads is already corrected —
	// memory keeps pace with the project with zero user action.
	if s.memoryFileWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		if projectID > 0 {
			if changed, err := s.memoryFileWriter.EvolveProjectMemory(ctx, projectID); err != nil {
				slog.Warn("agent: memory evolution failed", "workspaceID", s.workspaceID, "error", err)
			} else if changed > 0 {
				slog.Info("agent: memory evolved", "workspaceID", s.workspaceID, "changed", changed)
			}
			// Staleness/correction against the project's current code now runs as a
			// scheduled agent task (see service.MemoryService maintenance
			// orchestrator), not inline at session start.
			path := s.memoryFileWriter.GenerateMemoryFile(ctx, projectID, workDir)
			if path != "" {
				slog.Info("agent: learnings file generated", "workspaceID", s.workspaceID, "path", path)
			}
		}
	}

	adapterExtraArgs = append(adapterExtraArgs, agentExtraArgs...)
	agentCommand, args := driverAdapter.BuildSpawn(adapter.SpawnOptions{
		Command:      agentCommand,
		ExtraArgs:    adapterExtraArgs,
		WorkDir:      workDir,
		SessionID:    sessionId,
		Model:        model,
		WorktreeDirs: worktreeDirs,
	})

	// Store resolved working dir for API queries
	s.mu.Lock()
	s.workDir = workDir
	s.mu.Unlock()

	slog.Info("agent: launching",
		"workspaceID", s.workspaceID,
		"workDir", workDir,
		"command", agentCommand,
		"args", args,
		"resume", sessionId != "")

	cmdCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	cmd := exec.CommandContext(cmdCtx, agentCommand, args...)
	cmd.Dir = workDir
	// Spawn as a new process group (Unix) so killProcess can tear down the whole
	// tree — claude + every child it forks (Bash tools, niuniu-mcp, MCP servers).
	// On Windows this is nil; taskkill /T walks the tree instead.
	cmd.SysProcAttr = newProcessGroupAttr()

	workspaceEnv := make([]adapter.EnvVar, 0, len(wsEnvVars))
	if envErr == nil {
		for _, e := range wsEnvVars {
			workspaceEnv = append(workspaceEnv, adapter.EnvVar{Key: e.Key, Value: e.Value})
		}
	}

	// Inject CLAUDE_CONFIG_DIR for the resolved Claude account.
	// User-set value in env preset wins (adapterEnvHasKey skip);
	// see spec §"Edge cases" / §"Spawn-time integration".
	// Resolve internally degrades to default row on any failure (spec §解析链),
	// so a hard error here only happens for catastrophic DB issues — log + skip
	// injection rather than crash spawn.
	// Claude uses the host's global ~/.claude/ (per-account switching removed).
	var accountConfigDir string

	// Inject per-user GIT_AUTHOR_*/GIT_COMMITTER_* so commits Claude makes
	// in the chat session are attributed to the niuniu user that started it.
	// Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md §3.1
	var gitName, gitEmail string
	if gitUID := s.effectiveGitUserID(cmdCtx); s.gitIdentity != nil && gitUID > 0 {
		if name, email, err := s.gitIdentity.ResolveNameEmail(cmdCtx, gitUID); err == nil && name != "" && email != "" {
			gitName, gitEmail = name, email
		} else if err != nil {
			slog.Warn("agentproxy: resolve git identity failed; spawn without GIT_AUTHOR_*",
				"workspaceID", s.workspaceID, "userID", gitUID, "err", err)
		}
	}
	cmdEnv := driverAdapter.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:     workspaceEnv,
		AccountConfigDir: accountConfigDir,
		GitAuthorName:    gitName,
		GitAuthorEmail:   gitEmail,
	})
	cmd.Env = cmdEnv

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	// Capture stderr for diagnostics — keeps last 8KB
	stderrBuf := newLimitedBuffer(8192)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		slog.Error("agent: failed to start process",
			"workspaceID", s.workspaceID,
			"workDir", workDir,
			"command", agentCommand,
			"args", args,
			"err", err)
		return fmt.Errorf("start process: %w", err)
	}

	slog.Info("agent: process started", "workspaceID", s.workspaceID, "pid", cmd.Process.Pid)

	pid := int64(cmd.Process.Pid)
	s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Int64: pid, Valid: true},
		AgentStatus: sql.NullString{String: "running", Valid: true},
		ID:          s.workspaceID,
	})

	s.cmd = cmd
	s.stdin = stdinPipe
	// bufio.Reader (not Scanner): a tool_result line carrying a base64 image
	// (e.g. read_image vision payload) easily exceeds any fixed Scanner token
	// cap — the old 256KB cap made the readLoop die with "token too long",
	// stranding the turn (tool spins forever). ReadString('\n') grows per line
	// without a ceiling. 256KB is just the read-chunk size; lines accumulate
	// across refills.
	s.reader = bufio.NewReaderSize(stdoutPipe, 256*1024)
	s.stderrBuf = stderrBuf
	s.alive = true
	s.workDir = workDir

	// Start background read loop
	go s.readLoop(ctx)

	// Start process monitor — detects unexpected exit
	go func() {
		defer recoverSessionGoroutine("processMonitor", s.workspaceID)
		waitErr := cmd.Wait()
		s.procMu.Lock()
		s.alive = false
		s.procMu.Unlock()

		// Log exit details including exit code and stderr
		exitCode := -1
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
		} else {
			exitCode = 0
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		slog.Warn("agent: process exited",
			"workspaceID", s.workspaceID,
			"exitCode", exitCode,
			"stderr", stderr,
			"err", waitErr)

		// Broadcast error to frontend if process died unexpectedly
		if exitCode != 0 && stderr != "" {
			errMsg := fmt.Sprintf("Agent process exited (code %d): %s", exitCode, stderr)
			if len(errMsg) > 500 {
				errMsg = errMsg[:500] + "..."
			}
			s.mu.Lock()
			msgId := s.turnMsgId
			s.mu.Unlock()
			errEv := NewOutputEvent(EventError, errMsg, msgId, "assistant", s.workspaceID)
			s.hub.Broadcast(s.workspaceID, errEv)
		}

		// A long-lived process never exits between turns in normal operation: a
		// clean turn ends with a `result` event and the process stays alive. So a
		// non-zero exit (crash) while a SendLoop is running means the in-flight
		// turn failed — mark it an error so SendLoop recovers (bounded retry) or
		// drops into attention, rather than reading the previous turn's clean
		// result and silently autohost-continuing a crash loop. Intentional kills
		// (Stop / idle reaper) set stopRequested / leave no live loop, so this
		// does not interfere with them. exitCode 0 (graceful) is left untouched.
		if exitCode != 0 {
			s.mu.Lock()
			if s.running && !s.stopRequested {
				s.lastTurnError = true
				s.lastTurnResult = fmt.Sprintf("agent process exited unexpectedly (exit code %d)", exitCode)
			}
			s.mu.Unlock()
		}

		s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
			AgentPid:    sql.NullInt64{Valid: false},
			AgentStatus: sql.NullString{String: "idle", Valid: true},
			ID:          s.workspaceID,
		})

		// Signal done so Send() unblocks. running/status is loop-scoped (owned by
		// SendLoop), so it is intentionally NOT cleared here.
		s.mu.Lock()
		ch := s.turnDone
		s.mu.Unlock()
		s.emitBgTaskNotify()
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()

	return nil
}

// readLoop continuously reads stdout NDJSON and dispatches events.
// Runs in a background goroutine for the lifetime of the process.
func (s *WorkspaceSession) readLoop(ctx context.Context) {
	defer recoverSessionGoroutine("readLoop", s.workspaceID)
	slog.Info("agent readLoop: started", "workspaceID", s.workspaceID)
	for {
		// ReadString returns the data read so far together with the error, so a
		// final newline-less line (EOF) is still processed before we break.
		line, readErr := s.reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			s.processLine(ctx, line)
		}
		if readErr != nil {
			if readErr != io.EOF {
				slog.Warn("agent readLoop: read error", "workspaceID", s.workspaceID, "err", readErr)
			}
			break
		}
	}
	slog.Info("agent readLoop: exited", "workspaceID", s.workspaceID)
}

// processLine parses one NDJSON stdout line and dispatches its event.
func (s *WorkspaceSession) processLine(ctx context.Context, line string) {
	cliAdapter := s.cliAdapter
	if cliAdapter == nil {
		cliAdapter = adapter.For(adapter.Type(s.cliType))
	}
	events, parseErr := cliAdapter.ParseLine(line)
	if parseErr != nil {
		slog.Warn("agent readLoop: parse error", "workspaceID", s.workspaceID, "err", parseErr)
		return
	}
	if len(events) == 0 {
		return
	}
	ev := events[0]
	// Log all stream_event types at Info to verify which events Claude CLI actually forwards
	if ev.Type == "stream_event" && (ev.StreamEventType == "message_start" || ev.StreamEventType == "message_delta" || ev.StreamEventType == "message_stop") {
		slog.Info("agent readLoop: message-level stream_event", "streamType", ev.StreamEventType, "inputTokens", ev.InputTokens, "outputTokens", ev.OutputTokens, "workspaceID", s.workspaceID)
	}
	if ev.Type == "rate_limit_event" {
		slog.Info("agent readLoop: rate_limit_event", "status", ev.RateLimitStatus, "rawJSON", ev.RateLimitRawJSON, "workspaceID", s.workspaceID)
	}
	if ev.Type == "result" {
		slog.Info("agent readLoop: result", "inputTokens", ev.InputTokens, "outputTokens", ev.OutputTokens, "cost", ev.TotalCostUSD, "workspaceID", s.workspaceID)
	}

	s.mu.Lock()
	msgId := s.turnMsgId
	s.lastActivityAt = time.Now() // feed the Send() inactivity watchdog
	s.mu.Unlock()

	s.handleEvent(ctx, ev, msgId)
}

// handleEvent processes a single parsed event from the long-lived process.
func (s *WorkspaceSession) handleEvent(ctx context.Context, ev ParsedEvent, msgId string) {
	// agentID attributes events from this turn to the session's top-level
	// workspace_agents row.
	agentID := s.resolveAgentID(ev.ParentToolUseId)
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" && ev.SessionID != "" {
			s.mu.Lock()
			isNewSession := s.sessionId != ev.SessionID
			s.sessionId = ev.SessionID
			if isNewSession {
				// Different session_id means the CLI started a fresh session
				// (not a resume); the prior drift-checked flag no longer applies.
				s.driftChecked = false
			}
			s.mu.Unlock()
			s.q.UpdateSessionColumns(ctx, store.UpdateSessionColumnsParams{
				SessionID:     sql.NullString{String: ev.SessionID, Valid: true},
				SessionStatus: sql.NullString{String: string(StatusRunning), Valid: true},
				ID:            s.workspaceID,
			})
			// Bind this session to its top-level workspace_agents row so subsequent
			// messages get correct agent attribution (Task 8).
			s.rebindTopLevelAgent(ctx)
			initEv := NewOutputEvent(EventSystemInfo, "session started", msgId, "system", s.workspaceID)
			cliAdapter := s.cliAdapter
			if cliAdapter == nil {
				cliAdapter = adapter.For(adapter.Type(s.cliType))
			}
			initEv.CliType = cliAdapter.DisplayName("")
			s.hub.Broadcast(s.workspaceID, initEv)
			// Reset task tracking state for new session
			s.taskIdMap = make(map[string]string)
			s.pendingTaskCreates = make(map[string]bool)
			s.taskBatchId = uuid.NewString()
			// Interrupt any stale in_progress tasks from previous session
			s.q.InterruptInProgressTasks(ctx, s.workspaceID)
		} else if ev.Subtype == "api_retry" {
			sysEv := NewOutputEvent(EventSystemInfo, fmt.Sprintf("API retry #%d: %s", ev.RetryAttempt, ev.RetryError), msgId, "system", s.workspaceID)
			s.hub.Broadcast(s.workspaceID, sysEv)
		}

	case "stream_event":
		s.mu.Lock()
		s.hasStreamEvents = true
		s.mu.Unlock()
		s.handleStreamEvent(ctx, ev, msgId)

	case "assistant":
		// Assistant lines carry one request's usage even when the gateway omits
		// stream usage (火山方舟/百炼). Same per-request authority as
		// message_start — see handleStreamEvent for why the cumulative result
		// event is NOT allowed to write this field.
		if ctxTokens := ev.InputTokens + ev.CacheReadTokens + ev.CacheCreationTokens; ctxTokens > 0 {
			s.mu.Lock()
			s.lastContextTokens = ctxTokens
			s.mu.Unlock()
		}
		s.mu.Lock()
		has := s.hasStreamEvents
		s.mu.Unlock()
		if !has {
			s.handleAssistantFallback(ctx, ev, msgId)
		}

	case "user":
		for _, tr := range ev.ToolResults {
			if s.pendingTaskCreates != nil && s.pendingTaskCreates[tr.ToolUseId] {
				if matches := taskCreateResultRe.FindStringSubmatch(tr.Content); len(matches) > 1 {
					s.taskIdMap[matches[1]] = tr.ToolUseId
				}
				delete(s.pendingTaskCreates, tr.ToolUseId)
			}
			out := NewOutputEvent(EventToolResult, tr.Content, msgId, "user", s.workspaceID)
			out.ToolUseId = tr.ToolUseId
			s.mu.Lock()
			out.ToolName = s.toolUseNames[tr.ToolUseId]
			s.mu.Unlock()
			out.IsError = tr.IsError
			s.persistAndBroadcast(ctx, out, agentID)
			if s.debouncer != nil {
				s.debouncer.Notify(s.workspaceID)
			}
			s.recordBgTaskResult(tr.ToolUseId, tr.Content, tr.IsError)
		}

	case "result":
		if ev.IsError {
			s.persistAndBroadcast(ctx, NewOutputEvent(EventError, ev.Result, msgId, "assistant", s.workspaceID), agentID)
			if staleTasks, err := s.q.ListInProgressTasks(ctx, s.workspaceID); err == nil && len(staleTasks) > 0 {
				s.q.InterruptInProgressTasks(ctx, s.workspaceID)
				for _, t := range staleTasks {
					t.Status = "interrupted"
					taskEv := NewOutputEvent(EventTaskUpdate, "", msgId, "system", s.workspaceID)
					taskEv.TaskData = taskToEventData(t)
					s.hub.Broadcast(s.workspaceID, taskEv)
				}
			}
		} else {
			s.mu.Lock()
			ac := s.joinedAssistantContent()
			s.mu.Unlock()
			if ev.Result != "" && ac == "" {
				s.mu.Lock()
				s.assistantTextBlocks = []string{ev.Result}
				s.mu.Unlock()
				s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventText, ev.Result, msgId, "assistant", s.workspaceID))
			}
			done := NewOutputEvent(EventDone, "", msgId, "assistant", s.workspaceID)
			done.CostUsd = ev.TotalCostUSD
			done.NumTurns = ev.NumTurns
			done.DurationMs = ev.DurationMs
			done.InputTokens = ev.InputTokens
			done.OutputTokens = ev.OutputTokens
			done.CacheCreationTokens = ev.CacheCreationTokens
			done.CacheReadTokens = ev.CacheReadTokens
			// NOTE: context-window occupancy (s.lastContextTokens, drives
			// auto-compaction) is taken from message_start (a single request's
			// prompt size, see handleStreamEvent). The result event's usage is
			// the turn-CUMULATIVE total (it shares the object with num_turns /
			// total_cost_usd), so its cache_read sums every request in an agentic
			// tool loop and overstates the live context. EXCEPT when the endpoint
			// never emitted a usable message_start (some Anthropic-compatible
			// providers — 火山方舟/百炼 gateways — omit stream usage): then the
			// result sum is the ONLY signal available, and a bounded overestimate
			// beats a permanently-0 pill. Use input+cache_read only (skip
			// cache_creation, the double-counted part).
			if ev.InputTokens+ev.CacheReadTokens > 0 {
				s.mu.Lock()
				if s.lastContextTokens == 0 {
					s.lastContextTokens = ev.InputTokens + ev.CacheReadTokens
				}
				s.mu.Unlock()
			}
			if ev.OutputTokens > 0 {
				s.mu.Lock()
				s.lastOutputTokens = ev.OutputTokens
				s.mu.Unlock()
			}
			s.persistAndBroadcast(ctx, done, agentID)

			if !s.isTemporary {
				activeRunID := s.ActiveRunID()
				s.q.CreateWorkspaceCost(ctx, store.CreateWorkspaceCostParams{
					WorkspaceID:  s.workspaceID,
					SessionID:    sql.NullString{String: s.sessionId, Valid: s.sessionId != ""},
					MessageID:    sql.NullString{String: msgId, Valid: true},
					CostUsd:      ev.TotalCostUSD,
					NumTurns:     int64(ev.NumTurns),
					DurationMs:   ev.DurationMs,
					HarnessRunID: sql.NullInt64{Int64: activeRunID, Valid: activeRunID != 0},
				})
				// Token usage accounting (cost in $ is no longer surfaced; we
				// track tokens by type). Lifetime totals + hourly history.
				// Errors are logged but never block the SSE stream (matches
				// CreateWorkspaceCost above); a silent failure here would make a
				// PG overflow / constraint issue undiagnosable.
				if err := s.q.UpsertWorkspaceStatsAI(ctx, store.UpsertWorkspaceStatsAIParams{
					WorkspaceID:         s.workspaceID,
					OwnerType:           s.ownerType,
					OwnerID:             s.ownerID,
					TotalTurns:          int64(ev.NumTurns),
					TotalDurationMs:     ev.DurationMs,
					InputTokens:         int64(ev.InputTokens),
					OutputTokens:        int64(ev.OutputTokens),
					CacheCreationTokens: int64(ev.CacheCreationTokens),
					CacheReadTokens:     int64(ev.CacheReadTokens),
				}); err != nil {
					slog.Warn("UpsertWorkspaceStatsAI failed", "workspaceID", s.workspaceID, "error", err)
				}
				if err := s.q.UpsertWorkspaceTokenHourly(ctx, store.UpsertWorkspaceTokenHourlyParams{
					WorkspaceID:         s.workspaceID,
					BucketHour:          time.Now().UTC().Truncate(time.Hour),
					InputTokens:         int64(ev.InputTokens),
					OutputTokens:        int64(ev.OutputTokens),
					CacheCreationTokens: int64(ev.CacheCreationTokens),
					CacheReadTokens:     int64(ev.CacheReadTokens),
				}); err != nil {
					slog.Warn("UpsertWorkspaceTokenHourly failed", "workspaceID", s.workspaceID, "error", err)
				}
			}
		}

		// Persist accumulated content.
		// When stream events are available, text blocks are already persisted
		// inline during content_block_stop to preserve correct ordering with
		// tool events. Only persist here as fallback for non-streaming mode.
		s.mu.Lock()
		hasStream := s.hasStreamEvents
		blocks := make([]string, len(s.assistantTextBlocks))
		copy(blocks, s.assistantTextBlocks)
		tc := s.thinkingContent
		thinkingPersisted := s.thinkingPersisted
		thinkMsgID := s.thinkingMsgID
		s.mu.Unlock()
		if !hasStream {
			for idx, block := range blocks {
				if block != "" {
					ev := NewOutputEvent(EventText, block, msgId, "assistant", s.workspaceID)
					s.persistEventWithID(ctx, fmt.Sprintf("%s-text-%03d", msgId, idx), ev, agentID)
				}
			}
		}
		if tc != "" && !thinkingPersisted {
			s.persistEvent(ctx, NewOutputEvent(EventThinking, tc, msgId, "assistant", s.workspaceID), agentID)
		} else if tc != "" && thinkMsgID != "" {
			// Authoritative final flush of streamed thinking — per-delta UPDATEs
			// are throttled, so the tail may not yet be persisted.
			if err := s.q.UpdateAgentMessageContent(ctx, store.UpdateAgentMessageContentParams{ID: thinkMsgID, Content: tc}); err != nil {
				slog.Warn("agent thinking stream: final flush failed",
					"workspaceID", s.workspaceID, "messageID", thinkMsgID, "err", err)
			}
		}

		s.q.UpdateSessionColumns(ctx, store.UpdateSessionColumnsParams{
			SessionID:     sql.NullString{String: s.sessionId, Valid: s.sessionId != ""},
			SessionStatus: sql.NullString{String: string(StatusIdle), Valid: true},
			ID:            s.workspaceID,
		})

		// Capture workspace state for the next --resume drift detection.
		// Best-effort: failures only log; never block turn completion.
		// Skipped for temporary workspaces (no resume narrative needed).
		if s.sessionStateSvc != nil && s.sessionId != "" && !s.isTemporary {
			s.mu.Lock()
			lastMsg := s.lastUserMsgContent
			s.mu.Unlock()
			wsID := s.workspaceID
			sid := s.sessionId
			recorder := s.sessionStateSvc
			go func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := recorder.CaptureSnapshot(bgCtx, wsID, sid, lastMsg); err != nil {
					slog.Warn("session state snapshot failed", "workspaceID", wsID, "err", err)
				}
			}()
		}

		// Signal Send() that this turn is complete. running/status is loop-scoped
		// (owned by SendLoop) and intentionally NOT cleared here — only the
		// per-turn result state + turnDone are updated.
		s.mu.Lock()
		s.lastTurnError = ev.IsError
		s.lastTurnResult = ev.Result
		ch := s.turnDone
		s.mu.Unlock()
		s.emitBgTaskNotify()
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}

	case "rate_limit_event":
		slog.Debug("rate_limit_event", "status", ev.RateLimitStatus, "type", ev.RateLimitType, "resetsAt", ev.RateLimitResetsAt, "workspaceID", s.workspaceID)
		s.mu.Lock()
		s.lastRateLimit = &event.RateLimitData{
			Status:   ev.RateLimitStatus,
			Type:     ev.RateLimitType,
			ResetsAt: ev.RateLimitResetsAt,
			Overage:  ev.RateLimitOverage,
		}
		s.mu.Unlock()
		// Nudge any open client to refetch /claude-status immediately so the
		// usage pill's reset time updates live instead of waiting for the 60s
		// poll. Non-persisted: it's a refresh signal, not dialogue history.
		if s.hub != nil {
			s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventClaudeStatusChanged, "", msgId, "system", s.workspaceID))
		}
		// Forward the observation to the claude-usage panel so it can show the
		// authoritative reset time (the only data source that has it — Anthropic
		// doesn't expose a queryable endpoint we can reach).
		if ev.RateLimitStatus == "rejected" || ev.RateLimitStatus == "allowed_warning" {
			sysEv := NewOutputEvent(EventSystemInfo, "Rate limit: "+ev.RateLimitStatus, msgId, "system", s.workspaceID)
			s.persistAndBroadcast(ctx, sysEv, agentID)
		}
		// Auto-resume: when the turn is outright rejected and we know when the
		// window resets, schedule a one-shot task at reset time that dequeues
		// and sends pending messages so the workspace continues on its own.
		// Skipped for temporary workspaces (no durable queue / schedule).
		if ev.RateLimitStatus == "rejected" && ev.RateLimitResetsAt > 0 && !s.isTemporary && s.parent != nil {
			s.parent.mu.RLock()
			sched := s.parent.rateLimitSched
			s.parent.mu.RUnlock()
			if sched != nil {
				sched.OnRateLimited(ctx, s.workspaceID, ev.RateLimitResetsAt)
			}
		}
	}
}

// handleStreamEvent processes streaming delta events.
func (s *WorkspaceSession) handleStreamEvent(ctx context.Context, ev ParsedEvent, msgId string) {
	slog.Debug("stream_event", "type", ev.StreamEventType, "inputTokens", ev.InputTokens, "outputTokens", ev.OutputTokens, "workspaceID", s.workspaceID)

	switch ev.StreamEventType {
	case "message_start":
		// Live context-window occupancy = this single request's full prompt
		// (uncached input + cache read + cache creation). message_start fires
		// once PER API request, so the value is one prompt's true size. The
		// result event is NOT usable here: its usage SUMS every request in the
		// turn, and an agentic turn re-reads the cached prefix on each tool
		// round-trip, so cache_read piles up N-fold. Driving occupancy off the
		// result sum made one multi-tool question look like hundreds of K of
		// context and tripped auto-compaction after a single message.
		if ctxTokens := ev.InputTokens + ev.CacheReadTokens + ev.CacheCreationTokens; ctxTokens > 0 {
			s.mu.Lock()
			s.lastContextTokens = ctxTokens
			s.mu.Unlock()
		}

	case "message_delta":
		if ev.OutputTokens > 0 {
			s.mu.Lock()
			s.lastOutputTokens = ev.OutputTokens
			s.mu.Unlock()
		}

	case "content_block_start":
		switch ev.BlockType {
		case "text":
			s.mu.Lock()
			s.textBlockBufs[ev.BlockIndex] = ""
			s.mu.Unlock()
		case "tool_use":
			s.mu.Lock()
			s.toolUseNames[ev.ToolUseId] = ev.ToolUseName
			s.toolUseIds[ev.BlockIndex] = ev.ToolUseId
			s.lastBlockWasTool = true
			s.mu.Unlock()
			out := NewOutputEvent(EventToolUse, "", msgId, "assistant", s.workspaceID)
			out.ToolName = ev.ToolUseName
			out.ToolUseId = ev.ToolUseId
			s.persistAndBroadcast(ctx, out, s.resolveAgentID(ev.ParentToolUseId))
		case "thinking":
			// Broadcast-only — don't persist an empty row. The first
			// thinking_delta below INSERTs the single stable row for
			// this turn (thinkingMsgID), and subsequent deltas UPDATE
			// the same row. This matches text_delta's pattern where
			// content_block_start is also a UI-only marker.
			s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventThinking, "", msgId, "assistant", s.workspaceID))
		}

	case "content_block_delta":
		switch ev.DeltaType {
		case "text_delta":
			if ev.DeltaText != "" {
				s.mu.Lock()
				// Codex 的 textDeltaEvents 把所有文本增量都用 BlockIndex=0,
				// 不重置会把工具后的文本合并到工具前同一行 (timestamp 是早期的),
				// 导致 chat 历史按 created_at 排序时全部文本挤在前面、工具堆在末尾。
				// Claude 每个块由 SDK 分配唯一 BlockIndex, 这里是 no-op。
				if s.lastBlockWasTool {
					delete(s.textBlockBufs, ev.BlockIndex)
					delete(s.textBlockMessageIDs, ev.BlockIndex)
					delete(s.textBlockPersisted, ev.BlockIndex)
				}
				s.appendTextContent(ev.DeltaText)
				s.textBlockBufs[ev.BlockIndex] += ev.DeltaText
				allContent := s.joinedAssistantContent()
				if matches := phaseCompleteRe.FindAllStringSubmatch(allContent, -1); len(matches) > 0 {
					s.lastPhaseComplete = matches[len(matches)-1][1]
				}
				blockContent := s.textBlockBufs[ev.BlockIndex]
				textMsgID := s.textBlockMessageIDs[ev.BlockIndex]
				if textMsgID == "" {
					textMsgID = fmt.Sprintf("%s-text-%03d", msgId, s.textBlockSeq)
					s.textBlockSeq++
					s.textBlockMessageIDs[ev.BlockIndex] = textMsgID
				}
				firstPersist := !s.textBlockPersisted[ev.BlockIndex]
				s.textBlockPersisted[ev.BlockIndex] = true
				// Throttle mid-stream UPDATEs: flush at most once per
				// streamPersistThrottle per block. content_block_stop always flushes
				// the full block, so a skipped intermediate UPDATE only delays the
				// snapshot, never loses data.
				if s.textBlockLastFlush == nil {
					s.textBlockLastFlush = make(map[int]time.Time)
				}
				flushUpdate := false
				if firstPersist {
					s.textBlockLastFlush[ev.BlockIndex] = time.Now()
				} else if now := time.Now(); now.Sub(s.textBlockLastFlush[ev.BlockIndex]) >= streamPersistThrottle {
					s.textBlockLastFlush[ev.BlockIndex] = now
					flushUpdate = true
				}
				s.mu.Unlock()
				agentID := s.resolveAgentID(ev.ParentToolUseId)
				if firstPersist {
					s.persistEventWithID(ctx, textMsgID, NewOutputEvent(EventText, blockContent, msgId, "assistant", s.workspaceID), agentID)
				} else if flushUpdate {
					if err := s.q.UpdateAgentMessageContent(ctx, store.UpdateAgentMessageContentParams{ID: textMsgID, Content: blockContent}); err != nil {
						slog.Warn("agent text stream: update persisted markdown failed",
							"workspaceID", s.workspaceID, "messageID", textMsgID, "err", err)
					}
				}
				s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventText, ev.DeltaText, msgId, "assistant", s.workspaceID))
			}
		case "input_json_delta":
			s.mu.Lock()
			prevLen := len(s.toolInputBufs[ev.BlockIndex])
			s.toolInputBufs[ev.BlockIndex] += ev.DeltaText
			newLen := len(s.toolInputBufs[ev.BlockIndex])
			if newLen/200 > prevLen/200 {
				if tuId, ok := s.toolUseIds[ev.BlockIndex]; ok {
					out := NewOutputEvent(EventToolUse, "", msgId, "assistant", s.workspaceID)
					out.ToolUseId = tuId
					out.ToolName = s.toolUseNames[tuId]
					out.ToolInput = truncate(s.toolInputBufs[ev.BlockIndex], 500)
					s.mu.Unlock()
					s.persistAndBroadcast(ctx, out, s.resolveAgentID(ev.ParentToolUseId))
					return
				}
			}
			s.mu.Unlock()
		case "thinking_delta":
			if ev.DeltaText != "" {
				agentID := s.resolveAgentID(ev.ParentToolUseId)
				s.mu.Lock()
				s.thinkingContent += ev.DeltaText
				thinkMsgID := s.thinkingMsgID
				if thinkMsgID == "" {
					thinkMsgID = fmt.Sprintf("%s-thinking", msgId)
					s.thinkingMsgID = thinkMsgID
				}
				firstPersist := !s.thinkingPersisted
				s.thinkingPersisted = true
				full := s.thinkingContent
				// Throttle mid-stream UPDATEs (see streamPersistThrottle). The full
				// thinking content is flushed at turn finalization, so a skipped
				// intermediate UPDATE never loses data.
				flushUpdate := false
				if firstPersist {
					s.thinkingLastFlush = time.Now()
				} else if now := time.Now(); now.Sub(s.thinkingLastFlush) >= streamPersistThrottle {
					s.thinkingLastFlush = now
					flushUpdate = true
				}
				s.mu.Unlock()
				if firstPersist {
					s.persistEventWithID(ctx, thinkMsgID, NewOutputEvent(EventThinking, full, msgId, "assistant", s.workspaceID), agentID)
				} else if flushUpdate {
					if err := s.q.UpdateAgentMessageContent(ctx, store.UpdateAgentMessageContentParams{ID: thinkMsgID, Content: full}); err != nil {
						slog.Warn("agent thinking stream: update persisted thinking failed",
							"workspaceID", s.workspaceID, "messageID", thinkMsgID, "err", err)
					}
				}
				// Broadcast only the delta — frontend appends it to the live
				// streaming block. Do NOT persist here (that's what creates
				// one DB row per word when DeepSeek sends tiny deltas).
				s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventThinking, ev.DeltaText, msgId, "assistant", s.workspaceID))
			}
		}

	case "content_block_stop":
		s.mu.Lock()
		if input, ok := s.toolInputBufs[ev.BlockIndex]; ok {
			toolUseId := s.toolUseIds[ev.BlockIndex]
			toolName := s.toolUseNames[toolUseId]

			slog.Debug("tool completed", "toolName", toolName, "toolUseId", toolUseId, "workspaceID", s.workspaceID)

			var fullTaskInput string
			if isTaskTool(toolName) {
				fullTaskInput = input
			}

			out := NewOutputEvent(EventToolUse, "", msgId, "assistant", s.workspaceID)
			out.ToolUseId = toolUseId
			out.ToolName = toolName
			out.ToolInput = input
			delete(s.toolInputBufs, ev.BlockIndex)
			delete(s.toolUseIds, ev.BlockIndex)
			s.mu.Unlock()

			// Resolve caller's agentID BEFORE any dispatch insert so the
			// tool_use(Agent) row is attributed to the calling agent, not the
			// new subagent.
			agentID := s.resolveAgentID(ev.ParentToolUseId)
			s.persistEvent(ctx, out, agentID)
			broadcastOut := out
			broadcastOut.ToolInput = truncate(input, 500)
			s.hub.Broadcast(s.workspaceID, broadcastOut)

			// Track in-flight background tasks for the sidebar bg_tasks display.
			s.recordBgTaskUse(toolName, toolUseId, input)

			if fullTaskInput != "" {
				s.parseAndPersistTask(ctx, toolName, toolUseId, fullTaskInput, msgId)
			}
			return
		}
		if text, ok := s.textBlockBufs[ev.BlockIndex]; ok {
			alreadyPersisted := s.textBlockPersisted[ev.BlockIndex]
			textMsgID := s.textBlockMessageIDs[ev.BlockIndex]
			delete(s.textBlockBufs, ev.BlockIndex)
			delete(s.textBlockPersisted, ev.BlockIndex)
			delete(s.textBlockMessageIDs, ev.BlockIndex)
			delete(s.textBlockLastFlush, ev.BlockIndex)
			if text != "" {
				seq := s.textBlockSeq
				s.textBlockSeq++
				s.mu.Unlock()
				if alreadyPersisted {
					// Authoritative final flush of the full block — the per-delta
					// UPDATEs are throttled, so the last chunk may not yet be persisted.
					if err := s.q.UpdateAgentMessageContent(ctx, store.UpdateAgentMessageContentParams{ID: textMsgID, Content: text}); err != nil {
						slog.Warn("agent text stream: final flush failed",
							"workspaceID", s.workspaceID, "messageID", textMsgID, "err", err)
					}
					return
				}
				out := NewOutputEvent(EventText, text, msgId, "assistant", s.workspaceID)
				s.persistEventWithID(ctx, fmt.Sprintf("%s-text-%03d", msgId, seq), out, s.resolveAgentID(ev.ParentToolUseId))
				return
			}
		}
		s.mu.Unlock()
	}
}

// handleAssistantFallback processes complete assistant messages (no stream_events).
func (s *WorkspaceSession) handleAssistantFallback(ctx context.Context, ev ParsedEvent, msgId string) {
	// Resolve caller's agentID once (the session's top-level agent).
	agentID := s.resolveAgentID(ev.ParentToolUseId)
	for _, tb := range ev.ThinkingBlocks {
		s.mu.Lock()
		s.thinkingContent += tb.Thinking
		s.mu.Unlock()
		s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventThinking, tb.Thinking, msgId, "assistant", s.workspaceID))
	}
	for _, tu := range ev.ToolUseBlocks {
		if isTaskTool(tu.Name) {
			s.parseAndPersistTask(ctx, tu.Name, tu.Id, tu.Input, msgId)
		}
		s.mu.Lock()
		s.lastBlockWasTool = true
		s.mu.Unlock()
		out := NewOutputEvent(EventToolUse, "", msgId, "assistant", s.workspaceID)
		out.ToolName = tu.Name
		out.ToolUseId = tu.Id
		out.ToolInput = tu.Input
		s.persistAndBroadcast(ctx, out, agentID)
	}
	for _, tb := range ev.TextBlocks {
		if tb.Text != "" {
			s.mu.Lock()
			s.appendTextContent(tb.Text)
			s.mu.Unlock()
			s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventText, tb.Text, msgId, "assistant", s.workspaceID))
		}
	}
}

// pinPreviewMaxLen bounds the auto-pin preview snippet, mirroring the frontend
// pin preview (chat-panel previewOf: collapse whitespace then slice(0, 200)).
const pinPreviewMaxLen = 200

// pinPreview collapses runs of whitespace into single spaces and truncates the
// result to pinPreviewMaxLen runes (rune-safe so a multi-byte char is never
// split, keeping the stored value valid UTF-8).
func pinPreview(content string) string {
	collapsed := strings.Join(strings.Fields(content), " ")
	r := []rune(collapsed)
	if len(r) > pinPreviewMaxLen {
		collapsed = string(r[:pinPreviewMaxLen])
	}
	return collapsed
}

// autoPinUserMessage adds a just-dispatched user message to the workspace's
// pinned messages by default (issue: "已经发给 agent 的用户发送的消息默认加到
// pin 消息中"). The pin is keyed by the user message's server messageId so it
// lines up with the chat row's DOM id (msg-<messageId>) and the manual
// pin/unpin path's per-block key. Idempotent via the CreatePinnedMessage upsert.
//
// Best-effort: a pin failure only logs — it must never break the send. Callers
// gate on "real user message" (not autohost-injected, not temporary) before
// invoking, matching the user-message stats increment.
func (s *WorkspaceSession) autoPinUserMessage(ctx context.Context, messageID, content string) {
	if s.q == nil {
		return
	}
	if _, err := s.q.CreatePinnedMessage(ctx, store.CreatePinnedMessageParams{
		WorkspaceID: s.workspaceID,
		MessageID:   messageID,
		Role:        "user",
		Preview:     pinPreview(content),
	}); err != nil {
		slog.Warn("autoPinUserMessage failed", "workspaceID", s.workspaceID, "err", err)
		return
	}
	// Tell open pin panels to refetch. Owner comes from the cached session
	// fields (same source the stats increment uses) so the hub can fan out
	// per-org without an extra GetWorkspace.
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicPinned,
			Action:    "changed",
			ID:        s.workspaceID,
			OwnerType: s.ownerType,
			OwnerID:   s.ownerID,
		})
	}
}

// Send writes a message to the long-lived process stdin.
// It blocks until the result event is received (one turn complete).
// Auto-starts/recovers the process if needed.
func (s *WorkspaceSession) Send(ctx context.Context, workDir, content, attachmentsJSON string, autohostInjected bool) error {
	// running/status is owned by SendLoop (loop-scoped); Send only manages the
	// per-turn state below. No busy guard here: Send is called solely by SendLoop,
	// which already holds running for the whole loop.
	//
	// autohostInjected: this turn's content is an auto-injected autohost
	// continue/recover prompt, not a real user message. It is still delivered to
	// the agent as a user turn (stdin role "user"), but persisted + echoed to the
	// chat as a "system" message so the UI renders it as an autohost notice rather
	// than a "You" bubble; it also doesn't count as a user message (stats) or
	// update "last user message".
	msgRole := "user"
	if autohostInjected {
		msgRole = "system"
	}
	s.mu.Lock()
	s.lastTurnError = false
	// Reset per-turn state
	s.assistantTextBlocks = nil
	s.lastBlockWasTool = false
	s.thinkingContent = ""
	s.toolUseNames = make(map[string]string)
	s.toolUseIds = make(map[int]string)
	s.toolInputBufs = make(map[int]string)
	s.textBlockBufs = make(map[int]string)
	s.textBlockMessageIDs = make(map[int]string)
	s.textBlockPersisted = make(map[int]bool)
	s.textBlockLastFlush = make(map[int]time.Time)
	s.textBlockSeq = 0
	s.hasStreamEvents = false
	s.thinkingPersisted = false
	s.thinkingLastFlush = time.Time{}
	s.thinkingMsgID = ""
	s.lastPhaseComplete = ""
	s.turnDone = make(chan struct{}, 1)
	msgId := uuid.NewString()
	userMsgId := uuid.NewString()
	s.turnMsgId = msgId
	s.mu.Unlock()
	s.emitBgTaskNotify()

	slog.Info("agent send: starting", "workspaceID", s.workspaceID)

	// Reset idle timer
	s.resetIdleTimer()

	// Persist user message
	userRowId := uuid.NewString()
	userRunID := s.ActiveRunID()
	s.q.CreateAgentMessage(ctx, store.CreateAgentMessageParams{
		ID:               userRowId,
		WorkspaceID:      s.workspaceID,
		WorkspaceAgentID: sql.NullInt64{},
		Role:             msgRole,
		Content:          content,
		MessageID:        userMsgId,
		EventType:        EventText,
		HarnessRunID:     sql.NullInt64{Int64: userRunID, Valid: userRunID != 0},
		Attachments:      sql.NullString{String: attachmentsJSON, Valid: attachmentsJSON != ""},
	})
	s.mu.Lock()
	s.turnUserMsgId = userRowId
	s.mu.Unlock()

	// Count user messages per workspace (survives /clear). All REAL user sends —
	// fresh, queue-dequeued, scheduler, harness — funnel through here; auto-injected
	// autohost prompts are not user messages and are excluded.
	if !s.isTemporary && !autohostInjected {
		if err := s.q.IncrWorkspaceStatsUserMessage(ctx, store.IncrWorkspaceStatsUserMessageParams{
			WorkspaceID: s.workspaceID,
			OwnerType:   s.ownerType,
			OwnerID:     s.ownerID,
		}); err != nil {
			slog.Warn("IncrWorkspaceStatsUserMessage failed", "workspaceID", s.workspaceID, "error", err)
		}
		// Auto-pin the user message by default so the pin panel doubles as an
		// index of everything the user has asked the agent. Same "real user
		// send" gate as the stats increment above; keyed by userMsgId to match
		// the chat row's DOM id / manual pin key.
		s.autoPinUserMessage(ctx, userMsgId, content)
	}

	// Echo the user message via SSE so the chat UI renders the bubble for
	// queue-dequeued / scheduler / harness sends. For fresh user sends the
	// SPA reconciles its optimistic insert against this event by content.
	// Attachments piggyback on the echo so queue-dequeued sends (which have
	// no optimistic bubble to reconcile against) can still render previews.
	echoEv := NewOutputEvent(EventText, content, userMsgId, msgRole, s.workspaceID)
	echoEv.Attachments = attachmentsJSON
	s.hub.Broadcast(s.workspaceID, echoEv)

	// Activate workspace on first interaction
	ws, wsErr := s.q.GetWorkspace(ctx, s.workspaceID)
	if wsErr == nil && ws.Status == "created" {
		s.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
			Status: "running",
			ID:     s.workspaceID,
		})
	}

	// Update workspace status to "running"
	if s.statusHook != nil {
		s.statusHook.OnAgentEvent(ctx, s.workspaceID, "running")
	}

	// On the FIRST Send within a resumed session, detect external changes
	// to the workspace files (manual edits, sibling git ops, pulls). Runs
	// synchronously so the drift summary lands in this turn's prompt rather
	// than the next one. Best-effort: errors only log. The persisted
	// user_message row keeps `content` (original), only `contentToSend`
	// (what stdin sees) is mutated.
	s.mu.Lock()
	checked := s.driftChecked
	sid := s.sessionId
	recorder := s.sessionStateSvc
	if !autohostInjected {
		s.lastUserMsgContent = content
	}
	s.mu.Unlock()
	contentToSend := content
	if !checked && recorder != nil && sid != "" && !s.isTemporary {
		driftCtx, driftCancel := context.WithTimeout(ctx, 5*time.Second)
		drift, derr := recorder.DriftMessage(driftCtx, s.workspaceID, sid)
		driftCancel()
		s.mu.Lock()
		s.driftChecked = true
		s.mu.Unlock()
		if derr != nil {
			slog.Warn("session drift detection failed", "workspaceID", s.workspaceID, "err", derr)
		} else if drift != "" {
			contentToSend = drift + "\n" + content
			driftEv := NewOutputEvent(EventSystemInfo, drift, msgId, "system", s.workspaceID)
			s.hub.Broadcast(s.workspaceID, driftEv)
		}
	}

	cliAdapter := s.cliAdapter
	if cliAdapter == nil {
		cliAdapter = adapter.For(adapter.Type(s.cliType))
	}
	if cliAdapter.Type() == adapter.TypeCodex {
		return s.runCodexAppServerTurn(ctx, workDir, contentToSend, msgId)
	}
	if cliAdapter.Type() == adapter.TypeOmp {
		return s.runOMPBackendTurn(ctx, workDir, contentToSend, msgId)
	}
	if cliAdapter.Type() == adapter.TypeGoose {
		return s.runGooseBackendTurn(ctx, workDir, contentToSend, msgId)
	}
	switch cliAdapter.ProcessMode() {
	case adapter.ProcessOneShot:
		return s.runOneShotTurn(ctx, workDir, contentToSend, msgId)
	case adapter.ProcessLongRunning:
		// Continue below.
	default:
		return fmt.Errorf("unsupported CLI process mode %q", cliAdapter.ProcessMode())
	}

	// Ensure a long-running CLI process is active (auto-start or recover).
	if err := s.ensureProcess(ctx, workDir); err != nil {
		slog.Error("agent: ensureProcess failed",
			"workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		// running/status cleared by SendLoop's defer when the loop exits on this error.
		errEv := NewOutputEvent(EventError, "Failed to start agent: "+err.Error(), msgId, "assistant", s.workspaceID)
		s.hub.Broadcast(s.workspaceID, errEv)
		return err
	}

	// Write message to stdin as JSON
	inputMsg := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role":    "user",
			"content": contentToSend,
		},
	}
	data, err := json.Marshal(inputMsg)
	if err != nil {
		// running/status cleared by SendLoop's defer when the loop exits on this error.
		return fmt.Errorf("marshal input: %w", err)
	}

	slog.Info("agent send: writing to stdin", "workspaceID", s.workspaceID, "contentLen", len(content))

	s.procMu.Lock()
	stdinWriter := s.stdin
	s.procMu.Unlock()

	if _, err := stdinWriter.Write(append(data, '\n')); err != nil {
		slog.Error("agent send: stdin write failed", "workspaceID", s.workspaceID, "err", err)
		// Process likely died — close stdin so it gets EOF, mark as dead, next call will recover
		stdinWriter.Close()
		s.procMu.Lock()
		s.alive = false
		s.procMu.Unlock()
		// running/status cleared by SendLoop's defer when the loop exits on this error.
		return fmt.Errorf("stdin write: %w", err)
	}

	// Wait for the result event (turn complete), context cancellation, or the
	// inactivity watchdog. The watchdog is what guarantees the workspace can
	// never wedge: turnDone is only ever signaled by a `result` event or by
	// process exit, so a process that is alive-but-silent (hung tool/MCP/network
	// call, CLI deadlock, or a lost process that never emits anything) would
	// otherwise block here forever — ctx is context.Background() and never
	// cancels. We poll lastActivityAt (bumped by readLoop on every line) and, if
	// the process has produced NO output for the whole window, kill it and fail
	// the turn so SendLoop drops into recover/attention instead of blocking.
	s.mu.Lock()
	s.lastActivityAt = time.Now() // baseline: writing stdin counts as activity
	s.mu.Unlock()
	return s.waitForTurnComplete(ctx, "agent send")
}

// waitForTurnComplete blocks until the current turn finishes (turnDone fires),
// the context is cancelled, or the inactivity watchdog reaps a wedged process.
//
// This is the guarantee that a workspace can never wedge: turnDone is only ever
// signaled by a `result` event or by process exit, so a process that is alive
// but silent (hung on a tool/MCP/network call, a CLI deadlock, or a lost
// process that emits nothing) would otherwise block here forever — the turn ctx
// is context.Background() and never cancels. We poll lastActivityAt (bumped on
// every line/event the process produces) and, if there has been NO output for
// the whole window, kill the process and fail the turn so SendLoop recovers
// instead of blocking. A legitimately long turn streams output continuously and
// keeps resetting the clock, so it is never killed. Callers must have set
// s.turnDone and seeded s.lastActivityAt for the turn before calling.
func (s *WorkspaceSession) waitForTurnComplete(ctx context.Context, label string) error {
	s.mu.Lock()
	ch := s.turnDone
	window := s.turnInactivityTimeout
	s.mu.Unlock()
	if window <= 0 {
		window = defaultTurnInactivityTimeout
	}

	// Tick several times per window so detection latency is a fraction of it.
	ticker := time.NewTicker(window / 5)
	defer ticker.Stop()
	for {
		select {
		case <-ch:
			slog.Info(label+": turn complete", "workspaceID", s.workspaceID)
			return nil
		case <-ctx.Done():
			slog.Warn(label+": context cancelled", "workspaceID", s.workspaceID)
			return ctx.Err()
		case <-ticker.C:
			s.mu.Lock()
			idle := time.Since(s.lastActivityAt)
			s.mu.Unlock()
			if idle < window {
				continue // still streaming — a long turn, not a wedged one
			}
			slog.Error(label+": turn watchdog — no output within inactivity window, killing unresponsive process",
				"workspaceID", s.workspaceID, "idle", idle.String())
			// Mark the turn as an error BEFORE killing so SendLoop treats this as
			// a failed turn (recover with budget / attention), not a clean turn
			// to silently autohost-continue. killProcess then restarts a fresh
			// process on the next turn (--resume preserves session context).
			s.mu.Lock()
			s.lastTurnError = true
			s.lastTurnResult = "agent unresponsive: produced no output within the watchdog window; the process was killed and will restart"
			s.mu.Unlock()
			s.killProcess()
			return fmt.Errorf("turn watchdog: agent produced no output for %s", idle)
		}
	}
}

// resetIdleTimer resets the idle timeout. If no messages arrive within the timeout,
// the process is killed to free resources.
func (s *WorkspaceSession) resetIdleTimer() {
	s.procMu.Lock()
	defer s.procMu.Unlock()

	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.idleTimer = time.AfterFunc(idleTimeout, func() {
		// Don't kill a process that's still doing work. running is loop-scoped:
		// true for the whole SendLoop including a long turn or the autohost LLM
		// judge between turns. The idle timer only resets on Send entry, so a
		// single >idleTimeout turn (or a slow judge) would otherwise be killed
		// mid-flight. Re-arm and re-check later; only a genuinely idle session
		// (no SendLoop) is reaped.
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if running {
			s.resetIdleTimer()
			return
		}
		slog.Info("agent: idle timeout, killing process", "workspaceID", s.workspaceID)
		s.killProcess()
	})
}

// recoverSessionGoroutine is a defer-able panic boundary for the per-session
// background goroutines (SendLoop, readLoop, process monitor). A panic in any of
// these used to be UNRECOVERED and crash the entire multi-tenant server process
// (gin's recovery middleware only covers request handlers, not these detached
// goroutines). This logs the full stack (so the root cause is captured instead
// of lost in a fatal crash) and contains the blast radius to the one workspace.
// It is a safety net + instrumentation, NOT a substitute for fixing the
// underlying panic the stack reveals.
func recoverSessionGoroutine(where string, workspaceID int64) {
	if r := recover(); r != nil {
		slog.Error("agentproxy: recovered panic in session goroutine (contained; would have crashed the server)",
			"where", where, "workspaceID", workspaceID, "panic", r, "stack", string(debug.Stack()))
	}
}

// killProcess terminates the long-lived process.
func (s *WorkspaceSession) killProcess() {
	s.procMu.Lock()
	codexApp := s.codexApp
	if codexApp != nil {
		s.codexApp = nil
		s.codexThreadID = ""
		s.cmd = nil
		s.stdin = nil
		s.reader = nil
		s.stderrBuf = nil
		s.alive = false
	}
	s.procMu.Unlock()
	if codexApp != nil {
		_ = codexApp.Close()
		slog.Info("agent: codex app-server killed", "workspaceID", s.workspaceID)
	}

	// Tear down the omp backend process (RPC over stdio) if one was started.
	s.mu.Lock()
	ompBackend := s.ompBackend
	s.ompBackend = nil
	s.mu.Unlock()
	if ompBackend != nil {
		_ = ompBackend.Close(context.Background())
		slog.Info("agent: omp backend killed", "workspaceID", s.workspaceID)
	}

	// Tear down the goose backend process (ACP over stdio) if one was started.
	s.mu.Lock()
	gooseBackend := s.gooseBackend
	s.gooseBackend = nil
	s.mu.Unlock()
	if gooseBackend != nil {
		_ = gooseBackend.Close(context.Background())
		slog.Info("agent: goose backend killed", "workspaceID", s.workspaceID)
	}

	s.procMu.Lock()
	defer s.procMu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil && s.alive {
		s.stdin.Close()
		pid := s.cmd.Process.Pid
		// Kill the WHOLE process tree, not just the claude PID — otherwise child
		// tools / MCP servers orphan and keep running. (Req: "真实杀死 claude 进程".)
		if err := killPIDTree(pid); err != nil {
			slog.Warn("agent: tree-kill failed; falling back to single-process kill",
				"workspaceID", s.workspaceID, "pid", pid, "err", err)
			_ = s.cmd.Process.Kill()
		}
		// Cancel cmdCtx so CommandContext releases its wait goroutine/resources.
		if s.cancel != nil {
			s.cancel()
		}
		s.alive = false
		slog.Info("agent: process tree killed", "workspaceID", s.workspaceID, "pid", pid)
	}
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}

	// Unblock any Send() waiting on turnDone right now, instead of relying solely
	// on the process-monitor goroutine to observe the exit (it may lag, or — if
	// alive was already false — never fire for this turn). Lock order is
	// procMu → s.mu, matching ensureProcess. Buffered cap-1, non-blocking.
	s.mu.Lock()
	ch := s.turnDone
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// persistEventWithID saves an OutputEvent with a caller-specified ID.
// agentID attributes the message to a workspace_agent (0 = unknown, stored as NULL).
func (s *WorkspaceSession) persistEventWithID(ctx context.Context, id string, ev OutputEvent, agentID int64) {
	isErr := int64(0)
	if ev.IsError {
		isErr = 1
	}
	runID := s.ActiveRunID()
	s.q.CreateAgentMessage(ctx, store.CreateAgentMessageParams{
		ID:               id,
		WorkspaceID:      s.workspaceID,
		WorkspaceAgentID: sql.NullInt64{Int64: agentID, Valid: agentID != 0},
		Role:             ev.Role,
		Content:          ev.Content,
		MessageID:        ev.MessageId,
		EventType:        ev.Type,
		ToolName:         sql.NullString{String: ev.ToolName, Valid: ev.ToolName != ""},
		ToolInput:        sql.NullString{String: ev.ToolInput, Valid: ev.ToolInput != ""},
		ToolUseID:        sql.NullString{String: ev.ToolUseId, Valid: ev.ToolUseId != ""},
		IsError:          isErr,
		CostUsd:          sql.NullFloat64{Float64: ev.CostUsd, Valid: ev.CostUsd != 0},
		NumTurns:         sql.NullInt64{Int64: int64(ev.NumTurns), Valid: ev.NumTurns != 0},
		DurationMs:       sql.NullInt64{Int64: ev.DurationMs, Valid: ev.DurationMs != 0},
		InputTokens:      sql.NullInt64{Int64: int64(ev.InputTokens), Valid: ev.InputTokens != 0},
		OutputTokens:     sql.NullInt64{Int64: int64(ev.OutputTokens), Valid: ev.OutputTokens != 0},
		HarnessRunID:     sql.NullInt64{Int64: runID, Valid: runID != 0},
		Attachments:      sql.NullString{},
	})
	s.emitActivity(agentID, ev)
}

// persistEvent saves an OutputEvent to the database without broadcasting.
// agentID attributes the message to a workspace_agent (0 = unknown, stored as NULL).
func (s *WorkspaceSession) persistEvent(ctx context.Context, ev OutputEvent, agentID int64) {
	isErr := int64(0)
	if ev.IsError {
		isErr = 1
	}
	runID := s.ActiveRunID()
	s.q.CreateAgentMessage(ctx, store.CreateAgentMessageParams{
		ID:               uuid.NewString(),
		WorkspaceID:      s.workspaceID,
		WorkspaceAgentID: sql.NullInt64{Int64: agentID, Valid: agentID != 0},
		Role:             ev.Role,
		Content:          ev.Content,
		MessageID:        ev.MessageId,
		EventType:        ev.Type,
		ToolName:         sql.NullString{String: ev.ToolName, Valid: ev.ToolName != ""},
		ToolInput:        sql.NullString{String: ev.ToolInput, Valid: ev.ToolInput != ""},
		ToolUseID:        sql.NullString{String: ev.ToolUseId, Valid: ev.ToolUseId != ""},
		IsError:          isErr,
		CostUsd:          sql.NullFloat64{Float64: ev.CostUsd, Valid: ev.CostUsd != 0},
		NumTurns:         sql.NullInt64{Int64: int64(ev.NumTurns), Valid: ev.NumTurns != 0},
		DurationMs:       sql.NullInt64{Int64: ev.DurationMs, Valid: ev.DurationMs != 0},
		InputTokens:      sql.NullInt64{Int64: int64(ev.InputTokens), Valid: ev.InputTokens != 0},
		OutputTokens:     sql.NullInt64{Int64: int64(ev.OutputTokens), Valid: ev.OutputTokens != 0},
		HarnessRunID:     sql.NullInt64{Int64: runID, Valid: runID != 0},
		Attachments:      sql.NullString{},
	})
	s.emitActivity(agentID, ev)
}

// emitActivity broadcasts a team:agent_activity event for envelope-level kinds
// (tool_use / tool_result / thinking). Non-envelope kinds and agentID==0 are
// silently skipped.
// Activity events are derived signals — they are NOT persisted to agent_messages.
func (s *WorkspaceSession) emitActivity(agentID int64, ev OutputEvent) {
	if agentID == 0 || s.hub == nil {
		return
	}
	var kind string
	switch ev.Type {
	case EventToolUse:
		kind = "tool_use"
	case EventToolResult:
		kind = "tool_result"
	case EventThinking:
		kind = "thinking"
	default:
		return
	}
	now := time.Now().UnixMilli()
	s.hub.Broadcast(s.workspaceID, OutputEvent{
		Type:        EventTeamAgentActivity,
		Ts:          now,
		WorkspaceId: s.workspaceID,
		AgentActivity: &AgentActivityData{
			WorkspaceAgentID: agentID,
			Activity: &ActivityEntry{
				Kind:       kind,
				ToolName:   ev.ToolName,
				OccurredAt: now,
			},
		},
	})
}

// persistAndBroadcast saves an OutputEvent to the database and broadcasts it.
// agentID attributes the message to a workspace_agent (0 = unknown, stored as NULL).
func (s *WorkspaceSession) persistAndBroadcast(ctx context.Context, ev OutputEvent, agentID int64) {
	s.persistEvent(ctx, ev, agentID)
	s.hub.Broadcast(s.workspaceID, ev)
}

// Stop cancels the running process and updates DB status immediately.
// The session ID is preserved so the next message resumes with --resume.
func (s *WorkspaceSession) Stop(ctx context.Context) error {
	// Signal the (possibly blocked) SendLoop to terminate BEFORE killing the
	// process. killProcess unblocks an in-flight Send via turnDone; SendLoop
	// then sees stopRequested and exits instead of autohost-continuing.
	s.mu.Lock()
	s.stopRequested = true
	s.mu.Unlock()
	s.killProcess()
	s.mu.Lock()
	s.running = false
	s.status = StatusIdle
	s.mu.Unlock()
	// A user interrupt is a full stop: drop any pending future wakeup so the
	// Enqueue gate opens and a manual send right after Stop runs immediately,
	// instead of re-queueing behind the agent's stale "check back later" timer.
	s.clearPendingWakeups()
	s.emitBgTaskNotify()
	s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Valid: false},
		AgentStatus: sql.NullString{String: "idle", Valid: true},
		ID:          s.workspaceID,
	})
	if s.statusHook != nil {
		s.statusHook.OnAgentEvent(ctx, s.workspaceID, "done")
	}
	// Immediately notify the frontend so it exits the running state.
	// SendLoop will also broadcast EventIdle when the process exits, but
	// that may take time. Broadcasting here ensures a fast UI response.
	s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventIdle, "", "", "system", s.workspaceID))
	// Mirror the SendLoop turn-completion broadcast so the React Query
	// `['workspace', id]` cache gets invalidated by the notification
	// dispatcher (INVALIDATION_MAP['workspace']). Without this, the chat
	// input badge stays "running" even though DB was updated to "idle"
	// — until a manual page refresh. SendLoop's own broadcast (line ~1545)
	// only fires when the agent process emits a result event; killing the
	// process bypasses that path entirely.
	if s.notifyHub != nil && !s.isTemporary {
		ws, wsErr := s.q.GetWorkspace(ctx, s.workspaceID)
		var ownerType string
		var ownerID int64
		if wsErr != nil {
			slog.Warn("agent_done: GetWorkspace failed in Stop; broadcasting with zero owner",
				"workspace_id", s.workspaceID, "error", wsErr)
		} else {
			ownerType = ws.OwnerType
			ownerID = ws.OwnerID
		}
		// Manual stop is "needs_review" semantics — same as natural completion.
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicWorkspace,
			Action:    "agent_done",
			ID:        s.workspaceID,
			OwnerType: ownerType,
			OwnerID:   ownerID,
			Extra: map[string]any{
				"status":                "needs_review",
				"should_alert_user_ids": []int64{}, // user-initiated stop — no toast spam
			},
		})
	}
	return nil
}

// ClearSession resets the session ID and kills the process.
// The next message starts a fresh conversation.
func (s *WorkspaceSession) ClearSession(ctx context.Context) {
	s.killProcess()

	s.mu.Lock()
	s.sessionId = ""
	s.mu.Unlock()

	s.q.UpdateSessionColumns(ctx, store.UpdateSessionColumnsParams{
		SessionID:     sql.NullString{Valid: false},
		SessionStatus: sql.NullString{String: string(StatusIdle), Valid: true},
		ID:            s.workspaceID,
	})
}

func normalizedCliType(cliType string) string {
	switch cliType {
	case "codex":
		return "codex"
	case "qwen":
		return "qwen"
	case "omp":
		return "omp"
	case "goose":
		return "goose"
	}
	return "claude"
}

// taskToEventData converts a store.WorkspaceTask to an event.TaskData pointer.
func taskToEventData(t store.WorkspaceTask) *event.TaskData {
	td := &event.TaskData{
		ID:          t.ID,
		AgentTaskId: t.AgentTaskID,
		Subject:     t.Subject,
		Description: t.Description,
		ActiveForm:  t.ActiveForm,
		Status:      t.Status,
		Phase:       t.Phase,
		MessageId:   t.MessageID,
		BatchId:     t.BatchID,
	}
	if t.StartedAt.Valid {
		ms := t.StartedAt.Time.UnixMilli()
		td.StartedAt = &ms
	}
	if t.CompletedAt.Valid {
		ms := t.CompletedAt.Time.UnixMilli()
		td.CompletedAt = &ms
	}
	return td
}

// isTaskTool returns true if the tool name is a task-tracking tool.
func isTaskTool(name string) bool {
	switch name {
	case "TaskCreate", "TaskUpdate", "TodoWrite":
		return true
	}
	return false
}

// parseAndPersistTask dispatches to handleTaskCreate or handleTaskUpdate based on tool name.
func (s *WorkspaceSession) parseAndPersistTask(ctx context.Context, toolName, toolUseId, fullInput, msgId string) {
	if s.taskBatchId == "" {
		s.taskBatchId = uuid.NewString()
	}

	switch toolName {
	case "TaskCreate":
		s.handleTaskCreate(ctx, toolUseId, fullInput, msgId)
	case "TaskUpdate":
		s.handleTaskUpdate(ctx, fullInput, msgId)
	case "TodoWrite":
		s.handleTodoWrite(ctx, toolUseId, fullInput, msgId)
	}
}

func (s *WorkspaceSession) handleTaskCreate(ctx context.Context, toolUseId, fullInput, msgId string) {
	var input struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
		ActiveForm  string `json:"activeForm"`
		Metadata    struct {
			Phase string `json:"phase"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(fullInput), &input); err != nil {
		slog.Warn("task create: parse failed", "workspaceID", s.workspaceID, "err", err)
		return
	}

	phase := input.Metadata.Phase
	if phase == "" {
		phase = "tasks"
	}

	taskRunID := s.ActiveRunID()
	task, err := s.q.UpsertWorkspaceTask(ctx, store.UpsertWorkspaceTaskParams{
		WorkspaceID:  s.workspaceID,
		AgentTaskID:  toolUseId,
		Subject:      input.Subject,
		Description:  input.Description,
		ActiveForm:   input.ActiveForm,
		Status:       "pending",
		Phase:        phase,
		MessageID:    msgId,
		BatchID:      s.taskBatchId,
		HarnessRunID: sql.NullInt64{Int64: taskRunID, Valid: taskRunID != 0},
	})
	if err != nil {
		slog.Warn("task create: db upsert failed", "workspaceID", s.workspaceID, "err", err)
		return
	}

	if s.pendingTaskCreates == nil {
		s.pendingTaskCreates = make(map[string]bool)
	}
	s.pendingTaskCreates[toolUseId] = true

	ev := NewOutputEvent(EventTaskUpdate, "", msgId, "system", s.workspaceID)
	ev.TaskData = taskToEventData(task)
	s.hub.Broadcast(s.workspaceID, ev)
}

func (s *WorkspaceSession) handleTaskUpdate(ctx context.Context, fullInput, msgId string) {
	var input struct {
		TaskId     string `json:"taskId"`
		Status     string `json:"status"`
		Subject    string `json:"subject"`
		ActiveForm string `json:"activeForm"`
	}
	if err := json.Unmarshal([]byte(fullInput), &input); err != nil {
		slog.Warn("task update: parse failed", "workspaceID", s.workspaceID, "err", err)
		return
	}

	if s.taskIdMap == nil {
		slog.Debug("task update: taskIdMap not initialized", "workspaceID", s.workspaceID)
		return
	}

	agentTaskId, ok := s.taskIdMap[input.TaskId]
	if !ok {
		slog.Debug("task update: taskId not found in map", "taskId", input.TaskId, "workspaceID", s.workspaceID)
		return
	}

	updated, err := s.q.UpdateWorkspaceTaskByAgent(ctx, store.UpdateWorkspaceTaskByAgentParams{
		WorkspaceID:   s.workspaceID,
		AgentTaskID:   agentTaskId,
		NewSubject:    input.Subject,
		NewActiveForm: input.ActiveForm,
		NewStatus:     input.Status,
	})
	if err != nil {
		slog.Warn("task update: db update failed", "workspaceID", s.workspaceID, "err", err)
		return
	}

	ev := NewOutputEvent(EventTaskUpdate, "", msgId, "system", s.workspaceID)
	ev.TaskData = taskToEventData(updated)
	s.hub.Broadcast(s.workspaceID, ev)
}

// handleTodoWrite handles the TodoWrite tool which can act as both create and update.
// It supports two input formats:
//   - Single task format (same as TaskCreate): {"subject": "...", ...}
//   - Todos array format: {"todos": [{"id": "...", "content": "...", "status": "..."}]}
//
// For the array format, each TodoWrite call carries the FULL desired state of
// the todo list. We reconcile it against existing tasks for this batch:
//   - agent_task_id is derived from todo.ID (if Claude provides one) or from a
//     hash of todo.content, so identical todos collapse to the same row across
//     calls via UPSERT.
//   - Same-content duplicates within a single array are disambiguated by
//     appending an occurrence suffix.
//   - Tasks present in earlier calls but absent from this one are marked
//     'deleted' so they disappear from the UI.
func (s *WorkspaceSession) handleTodoWrite(ctx context.Context, toolUseId, fullInput, msgId string) {
	// Try TaskCreate-style format first (single task with subject field).
	var singleInput struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal([]byte(fullInput), &singleInput); err == nil && singleInput.Subject != "" {
		s.handleTaskCreate(ctx, toolUseId, fullInput, msgId)
		return
	}

	// Try todos array format.
	var arrayInput struct {
		Todos []struct {
			ID         string `json:"id"`
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	// Note: an empty todos array would skip MarkBatchTasksDeletedExcept; the
	// generated SQLite binding rewrites an empty IN-slice to `NOT IN (NULL)`,
	// which evaluates to UNKNOWN and deletes nothing. Treat empty as
	// "unrecognized" so a future "clear all" caller doesn't silently no-op.
	if err := json.Unmarshal([]byte(fullInput), &arrayInput); err == nil && len(arrayInput.Todos) > 0 {
		seen := make(map[string]bool, len(arrayInput.Todos))
		keepIds := make([]string, 0, len(arrayInput.Todos))
		taskRunID := s.ActiveRunID()

		for _, todo := range arrayInput.Todos {
			status := todo.Status
			if status == "" {
				status = "pending"
			}

			// Stable agent_task_id derivation:
			//   1. Explicit todo.id from the caller (preferred — survives renames).
			//   2. Otherwise hash(content) — same content keeps the same row.
			//   3. Same-content duplicates within one array get a suffix.
			var agentTaskId string
			if todo.ID != "" {
				agentTaskId = "todo-id-" + todo.ID
				if s.taskIdMap == nil {
					s.taskIdMap = make(map[string]string)
				}
				s.taskIdMap[todo.ID] = agentTaskId
			} else {
				base := contentHashTaskID(todo.Content)
				agentTaskId = base
				for n := 1; seen[agentTaskId]; n++ {
					agentTaskId = fmt.Sprintf("%s#%d", base, n)
				}
			}
			seen[agentTaskId] = true
			keepIds = append(keepIds, agentTaskId)

			task, err := s.q.UpsertWorkspaceTask(ctx, store.UpsertWorkspaceTaskParams{
				WorkspaceID:  s.workspaceID,
				AgentTaskID:  agentTaskId,
				Subject:      todo.Content,
				Description:  "",
				ActiveForm:   todo.ActiveForm,
				Status:       "pending",
				Phase:        "tasks",
				MessageID:    msgId,
				BatchID:      s.taskBatchId,
				HarnessRunID: sql.NullInt64{Int64: taskRunID, Valid: taskRunID != 0},
			})
			if err != nil {
				slog.Warn("todoWrite: upsert failed", "workspaceID", s.workspaceID, "agentTaskId", agentTaskId, "err", err)
				continue
			}

			// Funnel non-pending status through UpdateWorkspaceTaskByAgent so
			// started_at / completed_at get auto-stamped on transition.
			if status != "pending" {
				if updated, err := s.q.UpdateWorkspaceTaskByAgent(ctx, store.UpdateWorkspaceTaskByAgentParams{
					WorkspaceID:   s.workspaceID,
					AgentTaskID:   agentTaskId,
					NewSubject:    todo.Content,
					NewActiveForm: todo.ActiveForm,
					NewStatus:     status,
				}); err == nil {
					task = updated
				} else {
					slog.Warn("todoWrite: status update failed", "workspaceID", s.workspaceID, "agentTaskId", agentTaskId, "err", err)
				}
			}

			ev := NewOutputEvent(EventTaskUpdate, "", msgId, "system", s.workspaceID)
			ev.TaskData = taskToEventData(task)
			s.hub.Broadcast(s.workspaceID, ev)
		}

		if err := s.q.MarkBatchTasksDeletedExcept(ctx, store.MarkBatchTasksDeletedExceptParams{
			WorkspaceID: s.workspaceID,
			BatchID:     s.taskBatchId,
			KeepIds:     keepIds,
		}); err != nil {
			slog.Warn("todoWrite: reconcile-delete failed", "workspaceID", s.workspaceID, "err", err)
		}
		return
	}

	slog.Debug("todoWrite: unrecognized format", "workspaceID", s.workspaceID, "input", fullInput[:min(200, len(fullInput))])
}

// contentHashTaskID derives a stable agent_task_id from a todo's content.
// Same content always yields the same id, so repeated TodoWrite calls
// collapse identical todos onto the same row via UPSERT instead of inserting
// fresh rows each time.
func contentHashTaskID(content string) string {
	h := sha256.Sum256([]byte(content))
	return "todo-c-" + hex.EncodeToString(h[:8])
}

// gcInflightLoop runs every 10 seconds, sweeps stale entries from each
// session's InflightTracker, and emits a bg_task notify for any workspace
// whose tracker actually changed. Exits when Stop() closes p.stopCh.
//
// 10s is matched to "wakeup ScheduledFor expiry" — when a wakeup's scheduled
// time passes, GCStale removes the entry on the next tick so the sidebar
// bg-task count decreases within ~10s of the wakeup firing. Bash[bg] /
// subagent zombie thresholds (1h / 30m) still gate their own removal, so the
// faster tick is overhead-cheap for those: their stale check returns false
// immediately. Per-workspace bg_task notify is debounced 200ms downstream.
func (p *AgentProxy) gcInflightLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
		}
		now := time.Now()
		p.mu.Lock()
		sessions := make([]*WorkspaceSession, 0, len(p.sessions))
		for _, s := range p.sessions {
			sessions = append(sessions, s)
		}
		p.mu.Unlock()
		for _, s := range sessions {
			if s.inflight == nil {
				continue
			}
			removed := s.inflight.GCStale(now)

			// Process-tree probe to keep the displayed Bash[bg] count in sync
			// with the OS truth.
			//
			// Claude CLI never exposes a Bash[bg] shell's OS PID — bash_id is
			// an opaque internal handle. So we can't identify WHICH specific
			// shells died; we can only ask the OS "how many shell descendants
			// does the CLI process have right now?" and reconcile counts:
			//
			//   alive == 0 and bashCount > 0  → clear all (confirmed zombies)
			//   0 < alive < bashCount         → trim down to alive (partial die)
			//   alive >= bashCount            → leave alone (counts match, or
			//                                    extras are subagent shells we
			//                                    don't track)
			//
			// Trim victims are picked deterministically (no-BashID first, then
			// oldest StartedAt) so the count is honest even if identities
			// aren't precise — sidebar shows what the user actually has running.
			// Probe returns -1 on OS error — skip on the safe side rather than
			// risk wrongly clearing live shells.
			bashCount := s.inflight.CountByKind(BgTaskBash)
			if bashCount > 0 {
				cliPid := s.cliProcessPid()
				if cliPid > 0 {
					alive := countAliveShellDescendantsFn(cliPid)
					switch {
					case alive == 0:
						cleared := s.inflight.ClearBash()
						if len(cleared) > 0 {
							slog.Info("agentproxy: cleared bash entries — no shell descendants",
								"workspace_id", s.workspaceID, "cleared", len(cleared))
							removed = append(removed, cleared...)
						}
					case alive > 0 && alive < bashCount:
						trimmed := s.inflight.TrimBashTo(alive)
						if len(trimmed) > 0 {
							slog.Info("agentproxy: trimmed bash entries to match shell count",
								"workspace_id", s.workspaceID,
								"before", bashCount, "after", alive, "trimmed", len(trimmed))
							removed = append(removed, trimmed...)
						}
					}
				}
			}

			if len(removed) > 0 {
				for _, r := range removed {
					if r.Kind != BgTaskWakeup {
						slog.Warn("agentproxy: stale bg task GC'd",
							"workspace_id", s.workspaceID,
							"kind", r.Kind,
							"tool_use_id", r.ToolUseID,
							"started_at", r.StartedAt)
					}
				}
				s.emitBgTaskNotify()
			}
		}
	}
}

// Stop shuts down the AgentProxy's background goroutine (gcInflightLoop) and
// the underlying SessionHub ping loop. Safe to call multiple times (idempotent
// via sync.Once). Called at server shutdown and in tests via t.Cleanup.
func (p *AgentProxy) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		p.hub.Stop()
	})
}

// defaultSafeToolsLocal mirrors service.DefaultSafeTools. Duplicated here
// because service already imports agentproxy (team_dispatch.go,
// workspace.go), so an agentproxy → service import would form a cycle. Keep
// in sync with service/permission_matcher.go's DefaultSafeTools.
var defaultSafeToolsLocal = []string{"Read", "Glob", "Grep", "LS", "NotebookRead", "TodoRead"}

// AutohostMode is kept in agentproxy for existing call sites; the CLI mapping
// lives in adapter.BuildClaudePermissionArgs.
const AutohostMode = adapter.AutohostMode

// buildPermissionArgs returns the CLI flags governing Claude permissions:
// --permission-mode, optional --permission-prompt-tool (gated on MCP
// availability for default/acceptEdits modes), and --allowedTools merged
// from defaults + user-supplied CSV. Pure function for testability.
func buildPermissionArgs(mode string, userAllowedCSV string, mcpAvailable bool) []string {
	return adapter.BuildClaudePermissionArgs(mode, userAllowedCSV, mcpAvailable)
}

func adapterEnvHasKey(env []adapter.EnvVar, key string) bool {
	for _, e := range env {
		if e.Key == key {
			return true
		}
	}
	return false
}
