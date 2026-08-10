package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// LocalRunnerService is the server-side half of Epic #526 子B ("保存即连接").
//
// It owns three concerns:
//
//  1. Persistence of the per-workspace runner config (local_dir / prompt
//     snippet / allowed commands / always-allow) in workspace_local_runner.
//  2. An in-memory presence registry of desktop runners connected over the
//     reverse channel (one long-lived WS per bound workspace). Presence is
//     never persisted — a runner is "online" only while its socket is up.
//  3. A per-workspace log fan-out hub: stdout/stderr/exit frames arriving from
//     the runner are appended to a bounded ring buffer and streamed to every
//     SPA log subscriber (#494).
//
// Status is derived, never stored (#476 degradation):
//
//	no config .................................. unbound
//	config + runner online ..................... active
//	config + runner offline, was never online .. connecting
//	config + runner offline, was online before . error   (degraded)
//
// The conditional MCP/prompt injection decision (#470/#471/#492) is exposed as
// a pure method (MCPInjection) so the scene-projection layer can consult it;
// the actual .mcp.json wiring is gated on the local-runner MCP tool loop
// landing (see MCPInjection doc) so live agent sessions are never handed a
// non-functional MCP server.
type LocalRunnerService struct {
	q         *store.Queries
	db        *store.DB
	notifyHub *notify.NotificationHub

	mu         sync.RWMutex
	online     map[int64]*RunnerConn        // workspace_id -> live reverse-channel conn
	everOnline map[int64]bool               // workspace_id -> has ever registered (degradation signal)
	subs       map[int64]map[string]chan []byte // workspace_id -> subID -> SPA log sink
	ring       map[int64][]LocalRunnerLog   // workspace_id -> bounded recent-log buffer
	seq        int64                        // monotonic id source for subs + log entries

	// reproject regenerates a workspace's scene projection (rewrites .mcp.json so
	// the local-runner tool group is enabled/disabled to match presence). Called
	// async on runner connect/disconnect so the NEXT agent launch reads the right
	// tool set — MCP config is a session-start snapshot, so this fixes the "runner
	// came online after the session started, tools stay disabled" staleness (#526).
	reproject func(context.Context, int64)
}

// SetReproject wires the scene re-projection hook (avoids a hard dependency on
// SceneProjector; server.New injects sceneProjector.Apply).
func (s *LocalRunnerService) SetReproject(fn func(context.Context, int64)) {
	s.reproject = fn
}

// reprojectAsync fires the scene re-projection off the caller's goroutine with a
// fresh context (the WS handler ctx is often already cancelled on disconnect).
func (s *LocalRunnerService) reprojectAsync(wsID int64) {
	if s.reproject == nil {
		return
	}
	go s.reproject(context.Background(), wsID)
}

// logRingCap bounds the per-workspace replay buffer so a chatty build can't
// grow memory without bound; late subscribers get at most this many lines.
const logRingCap = 500

// LocalRunnerStatus mirrors the SPA state machine (LocalRunnerStatusDTO).
type LocalRunnerStatus string

const (
	StatusUnbound    LocalRunnerStatus = "unbound"
	StatusConnecting LocalRunnerStatus = "connecting"
	StatusActive     LocalRunnerStatus = "active"
	StatusError      LocalRunnerStatus = "error"
)

// LocalRunnerConfig is the Go-facing runner config (snake_case JSON matches the
// SPA LocalRunnerConfigDTO in lib/local-runner-api.ts).
type LocalRunnerConfig struct {
	LocalDir           string   `json:"local_dir"`
	PromptSnippet      string   `json:"prompt_snippet"`
	AllowedCommands    []string `json:"allowed_commands"`
	AlwaysAllowPersist bool     `json:"always_allow_persist"`
}

// LocalRunnerLog is one streamed log line (matches the SPA LocalRunnerLogEntry).
type LocalRunnerLog struct {
	ID    string `json:"id"`
	Ts    int64  `json:"ts"`
	Level string `json:"level"` // command | stdout | stderr | system
	Text  string `json:"text"`
}

// RunnerConn is a live desktop reverse-channel connection. The WS handler
// drains Send (server->runner command dispatch) and closes Closed on teardown.
type RunnerConn struct {
	WorkspaceID int64
	Send        chan []byte
	Closed      chan struct{}
	closeOnce   sync.Once

	pendingMu sync.Mutex
	pending   map[string]chan RunnerReply // request id -> reply sink
}

func (rc *RunnerConn) close() {
	rc.closeOnce.Do(func() { close(rc.Closed) })
}

// RunnerReply is the desktop's response to an exec/read/sync request.
type RunnerReply struct {
	OK      bool   `json:"ok"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int    `json:"exit"`
	Content string `json:"content"` // local_read payload
	Error   string `json:"error"`
}

// ErrRunnerOffline is returned when a command is dispatched to a workspace whose
// desktop runner is not connected — surfaced to the AI, never swallowed (#476).
var ErrRunnerOffline = errors.New("local runner offline")

// requestTimeout bounds how long an MCP tool call waits for the desktop to
// reply before giving up (build/test can be slow; keep generous).
const requestTimeout = 10 * time.Minute

// LocalRunnerToolGroup is the niuniu-mcp tool-group name for the local-runner
// tools (local_exec/local_read/local_sync). Conditional injection works by
// adding this to the generated .mcp.json --disable-tool-groups list whenever the
// runner is offline, so the tools vanish (#471) and the agent falls back to
// server-side execution.
const LocalRunnerToolGroup = "local-runner"

func NewLocalRunnerService(q *store.Queries, db *store.DB, notifyHub *notify.NotificationHub) *LocalRunnerService {
	return &LocalRunnerService{
		q:          q,
		db:         db,
		notifyHub:  notifyHub,
		online:     make(map[int64]*RunnerConn),
		everOnline: make(map[int64]bool),
		subs:       make(map[int64]map[string]chan []byte),
		ring:       make(map[int64][]LocalRunnerLog),
	}
}

// ---- Persistence -------------------------------------------------------

// GetConfig returns the persisted config, or (nil, nil) when the workspace has
// no runner binding yet.
func (s *LocalRunnerService) GetConfig(ctx context.Context, wsID int64) (*LocalRunnerConfig, error) {
	row, err := s.q.GetWorkspaceLocalRunner(ctx, wsID)
	if err != nil {
		// No row is not an error to callers — it means "unbound".
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	cfg := rowToConfig(row)
	return &cfg, nil
}

// SaveConfig upserts the runner binding for a workspace.
func (s *LocalRunnerService) SaveConfig(ctx context.Context, wsID int64, cfg LocalRunnerConfig) (*LocalRunnerConfig, error) {
	cmds := cfg.AllowedCommands
	if cmds == nil {
		cmds = []string{}
	}
	encoded, err := json.Marshal(cmds)
	if err != nil {
		return nil, fmt.Errorf("encode allowed_commands: %w", err)
	}
	row, err := s.q.UpsertWorkspaceLocalRunner(ctx, store.UpsertWorkspaceLocalRunnerParams{
		WorkspaceID:        wsID,
		LocalDir:           cfg.LocalDir,
		PromptSnippet:      cfg.PromptSnippet,
		AllowedCommands:    string(encoded),
		AlwaysAllowPersist: boolToInt(cfg.AlwaysAllowPersist),
	})
	if err != nil {
		return nil, err
	}
	// Saving a config makes the workspace "connecting" until the desktop
	// registers its reverse channel; nudge any listeners to refetch.
	s.broadcastStatus(ctx, wsID)
	out := rowToConfig(row)
	return &out, nil
}

// DeleteConfig unbinds the runner: clears the persisted config and tears down
// any live reverse-channel connection (工具集撤出 on unbind).
func (s *LocalRunnerService) DeleteConfig(ctx context.Context, wsID int64) error {
	if err := s.q.DeleteWorkspaceLocalRunner(ctx, wsID); err != nil {
		return err
	}
	s.mu.Lock()
	if rc, ok := s.online[wsID]; ok {
		rc.close()
		delete(s.online, wsID)
	}
	delete(s.everOnline, wsID)
	delete(s.ring, wsID)
	s.mu.Unlock()
	s.broadcastStatus(ctx, wsID)
	return nil
}

// Status derives the current state machine value for a workspace.
func (s *LocalRunnerService) Status(ctx context.Context, wsID int64) (LocalRunnerStatus, *LocalRunnerConfig, error) {
	cfg, err := s.GetConfig(ctx, wsID)
	if err != nil {
		return StatusUnbound, nil, err
	}
	return s.statusFor(wsID, cfg), cfg, nil
}

// statusFor is the pure status derivation (no DB) — unit-tested directly.
func (s *LocalRunnerService) statusFor(wsID int64, cfg *LocalRunnerConfig) LocalRunnerStatus {
	if cfg == nil {
		return StatusUnbound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.online[wsID]; ok {
		return StatusActive
	}
	if s.everOnline[wsID] {
		return StatusError // was active, runner dropped -> degraded (#476)
	}
	return StatusConnecting
}

// ---- Presence registry (reverse channel) -------------------------------

// RegisterRunner marks a workspace's desktop runner online and returns its
// connection handle. A previously registered conn for the same workspace is
// superseded (last writer wins) — the old one is closed.
func (s *LocalRunnerService) RegisterRunner(ctx context.Context, wsID int64) *RunnerConn {
	rc := &RunnerConn{
		WorkspaceID: wsID,
		Send:        make(chan []byte, 64),
		Closed:      make(chan struct{}),
		pending:     make(map[string]chan RunnerReply),
	}
	s.mu.Lock()
	if old, ok := s.online[wsID]; ok {
		old.close()
	}
	s.online[wsID] = rc
	s.everOnline[wsID] = true
	s.mu.Unlock()

	s.AppendLog(wsID, "system", "本地执行器已连接 / local runner connected")
	s.broadcastStatus(ctx, wsID)
	// Regenerate .mcp.json so the local-runner tool group is (re)enabled for the
	// next agent launch now that the runner is online.
	s.reprojectAsync(wsID)
	return rc
}

// UnregisterRunner marks a workspace offline when its reverse channel drops.
// Only unregisters if rc is still the active conn (guards against a superseded
// conn's teardown clobbering a newer registration).
func (s *LocalRunnerService) UnregisterRunner(ctx context.Context, rc *RunnerConn) {
	wsID := rc.WorkspaceID
	s.mu.Lock()
	if cur, ok := s.online[wsID]; ok && cur == rc {
		delete(s.online, wsID)
	}
	s.mu.Unlock()
	rc.close()

	// Degradation surfaced to the AI/UI (不静默): a log line + status refetch.
	s.AppendLog(wsID, "system", "本地执行器已断开，工具集撤出 / local runner disconnected, tools withdrawn")
	s.broadcastStatus(ctx, wsID)
	// Regenerate .mcp.json so the local-runner tool group is disabled again for
	// the next agent launch now that the runner is offline.
	s.reprojectAsync(wsID)
}

// IsOnline reports whether a workspace currently has a live runner.
func (s *LocalRunnerService) IsOnline(wsID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.online[wsID]
	return ok
}

// Dispatch queues a command frame to a workspace's runner. Returns false when
// no runner is online (caller surfaces an error to the AI — #476, not silent).
func (s *LocalRunnerService) Dispatch(wsID int64, frame []byte) bool {
	rc, ok := s.onlineConn(wsID)
	if !ok {
		return false
	}
	select {
	case rc.Send <- frame:
		return true
	case <-rc.Closed:
		return false
	}
}

func (s *LocalRunnerService) onlineConn(wsID int64) (*RunnerConn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rc, ok := s.online[wsID]
	return rc, ok
}

// runnerReconnectGrace bounds how long a dispatch waits for a mid-reconnect
// runner to come back before reporting it offline. Var (not const) so tests can
// shrink it.
var runnerReconnectGrace = 8 * time.Second

// waitOnline blocks until the workspace's runner is online, or the grace period
// / ctx elapses. Returns the live conn on success. A no-op fast path when the
// runner is already online keeps the common case free of any delay.
func (s *LocalRunnerService) waitOnline(ctx context.Context, wsID int64, d time.Duration) (*RunnerConn, bool) {
	if rc, ok := s.onlineConn(wsID); ok {
		return rc, true
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-deadline.C:
			return nil, false
		case <-tick.C:
			if rc, ok := s.onlineConn(wsID); ok {
				return rc, true
			}
		}
	}
}

// ExecCommand runs a command on the workspace's desktop runner and returns its
// result. Backs the MCP local_exec tool.
func (s *LocalRunnerService) ExecCommand(ctx context.Context, wsID int64, command string) (RunnerReply, error) {
	return s.request(ctx, wsID, "exec", command, "")
}

// ReadFile reads a file from the runner's bound directory (MCP local_read).
func (s *LocalRunnerService) ReadFile(ctx context.Context, wsID int64, path string) (RunnerReply, error) {
	return s.request(ctx, wsID, "read", "", path)
}

// Sync forces a remote->local worktree sync before exec (MCP local_sync; the
// actual git checkout + dirty-diff apply happens desktop-side, #472).
func (s *LocalRunnerService) Sync(ctx context.Context, wsID int64) (RunnerReply, error) {
	return s.request(ctx, wsID, "sync", "", "")
}

// request dispatches a correlated request frame to the runner and awaits its
// reply. Returns ErrRunnerOffline when the runner is not connected or drops
// mid-flight (in-flight calls fail loudly so the AI perceives degradation).
func (s *LocalRunnerService) request(ctx context.Context, wsID int64, kind, command, path string) (RunnerReply, error) {
	var zero RunnerReply
	rc, ok := s.onlineConn(wsID)
	if !ok {
		// Grace: the runner may just be mid-reconnect (a network blip). Wait a
		// short while for it to come back before failing, so a brief reconnect gap
		// doesn't surface to the AI as a spurious "offline" (the client resets its
		// backoff on drop so it typically re-registers within ~1s).
		rc, ok = s.waitOnline(ctx, wsID, runnerReconnectGrace)
		if !ok {
			return zero, ErrRunnerOffline
		}
	}

	s.mu.Lock()
	s.seq++
	id := "req-" + strconv.FormatInt(s.seq, 10)
	s.mu.Unlock()

	ch := make(chan RunnerReply, 1)
	rc.pendingMu.Lock()
	rc.pending[id] = ch
	rc.pendingMu.Unlock()
	defer func() {
		rc.pendingMu.Lock()
		delete(rc.pending, id)
		rc.pendingMu.Unlock()
	}()

	frame, err := json.Marshal(map[string]any{
		"type": "request", "id": id, "kind": kind, "command": command, "path": path,
	})
	if err != nil {
		return zero, err
	}
	if kind == "exec" {
		s.AppendLog(wsID, "command", command)
	}
	select {
	case rc.Send <- frame:
	case <-rc.Closed:
		return zero, ErrRunnerOffline
	case <-ctx.Done():
		return zero, ctx.Err()
	}

	select {
	case rep := <-ch:
		return rep, nil
	case <-rc.Closed:
		return zero, ErrRunnerOffline
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-time.After(requestTimeout):
		return zero, fmt.Errorf("local runner request timed out")
	}
}

// HandleRunnerFrame parses one frame from the desktop reverse channel: log lines
// stream to the log hub, exit markers become a system line, and response frames
// are routed back to the awaiting request(). Called by the WS read loop.
func (s *LocalRunnerService) HandleRunnerFrame(wsID int64, rc *RunnerConn, raw []byte) {
	var f struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Level   string `json:"level"`
		Text    string `json:"text"`
		Code    int    `json:"code"`
		OK      bool   `json:"ok"`
		Stdout  string `json:"stdout"`
		Stderr  string `json:"stderr"`
		Exit    int    `json:"exit"`
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	switch f.Type {
	case "log":
		level := f.Level
		if level == "" {
			level = "stdout"
		}
		s.AppendLog(wsID, level, f.Text)
	case "exit":
		s.AppendLog(wsID, "system", fmt.Sprintf("命令结束 exit=%d / command exited", f.Code))
	case "response":
		rc.pendingMu.Lock()
		ch := rc.pending[f.ID]
		rc.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- RunnerReply{OK: f.OK, Stdout: f.Stdout, Stderr: f.Stderr, Exit: f.Exit, Content: f.Content, Error: f.Error}:
			default:
			}
		}
	case "pong":
		// heartbeat only
	}
}

// DisableToolGroupsFor returns the extra tool groups to hide for a workspace at
// .mcp.json generation time. The local-runner group is hidden whenever the
// runner is offline, so the tools appear only when a runner is live (#471).
func (s *LocalRunnerService) DisableToolGroupsFor(wsID int64) []string {
	if s.IsOnline(wsID) {
		return nil
	}
	return []string{LocalRunnerToolGroup}
}

// PromptFragmentFor returns the configured claude.md prompt fragment to splice
// into the worktree prompt when the runner is bound + online, else "".
func (s *LocalRunnerService) PromptFragmentFor(ctx context.Context, wsID int64) string {
	frag, ok := s.MCPInjection(ctx, wsID)
	if !ok {
		return ""
	}
	return frag
}

// ---- Log fan-out hub ---------------------------------------------------

// SubscribeLogs registers a SPA log sink and returns its id, channel, and the
// current replay buffer (recent lines already seen by the runner).
func (s *LocalRunnerService) SubscribeLogs(wsID int64) (string, chan []byte, []LocalRunnerLog) {
	ch := make(chan []byte, 128)
	s.mu.Lock()
	s.seq++
	id := "sub-" + strconv.FormatInt(s.seq, 10)
	if s.subs[wsID] == nil {
		s.subs[wsID] = make(map[string]chan []byte)
	}
	s.subs[wsID][id] = ch
	replay := append([]LocalRunnerLog(nil), s.ring[wsID]...)
	s.mu.Unlock()
	return id, ch, replay
}

// UnsubscribeLogs removes a SPA log sink.
func (s *LocalRunnerService) UnsubscribeLogs(wsID int64, id string) {
	s.mu.Lock()
	if m, ok := s.subs[wsID]; ok {
		delete(m, id)
		if len(m) == 0 {
			delete(s.subs, wsID)
		}
	}
	s.mu.Unlock()
}

// AppendLog records a log line and fans it out to all live SPA subscribers.
// level ∈ {command, stdout, stderr, system}.
func (s *LocalRunnerService) AppendLog(wsID int64, level, text string) {
	s.mu.Lock()
	s.seq++
	entry := LocalRunnerLog{
		ID:    "log-" + strconv.FormatInt(s.seq, 10),
		Ts:    s.seq, // monotonic ordinal; wall-clock is stamped client-side on receipt
		Level: level,
		Text:  text,
	}
	buf := append(s.ring[wsID], entry)
	if len(buf) > logRingCap {
		buf = buf[len(buf)-logRingCap:]
	}
	s.ring[wsID] = buf
	sinks := make([]chan []byte, 0, len(s.subs[wsID]))
	for _, ch := range s.subs[wsID] {
		sinks = append(sinks, ch)
	}
	s.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	for _, ch := range sinks {
		select {
		case ch <- data:
		default:
			// Slow consumer — drop rather than block the runner read loop.
		}
	}
}

// ---- Conditional MCP / prompt injection seam (#470/#471/#492) ----------

// MCPInjection reports whether the local-runner MCP tool set + prompt fragment
// should be injected into wsID's agent session, and returns the payload.
//
// The decision is: config bound AND a desktop runner currently online. This is
// the pure gate the scene-projection layer consults: scene_projection_apply.go
// calls PromptFragmentFor to weave the returned fragment into the projected
// claude.md when a runner is live, so a remote agent is steered toward the
// local_exec tools only while the reverse channel is up.
func (s *LocalRunnerService) MCPInjection(ctx context.Context, wsID int64) (promptFragment string, ok bool) {
	if !s.IsOnline(wsID) {
		return "", false
	}
	cfg, err := s.GetConfig(ctx, wsID)
	if err != nil || cfg == nil {
		return "", false
	}
	return cfg.PromptSnippet, true
}

// ---- helpers -----------------------------------------------------------

func (s *LocalRunnerService) broadcastStatus(ctx context.Context, wsID int64) {
	if s.notifyHub == nil {
		return
	}
	status, _, err := s.Status(ctx, wsID)
	if err != nil {
		return
	}
	ownerType, ownerID, ok := s.workspaceOwner(ctx, wsID)
	if !ok {
		// Owner unresolved: skip the notification rather than fall back to a
		// global broadcast. Empty owner metadata makes the hub fan the event
		// out to every tenant, leaking this workspace's id + runner status
		// cross-tenant (multi-tenant red line). Fail closed.
		return
	}
	s.notifyHub.Broadcast(notify.Notification{
		Topic:     notify.TopicLocalRunner,
		Action:    string(status),
		ID:        wsID,
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
}

// workspaceOwner resolves the (owner_type, owner_id) for hub-side filtering.
// ok=false means resolution failed; callers must NOT broadcast (a global
// broadcast on empty owner metadata would leak the workspace across tenants).
func (s *LocalRunnerService) workspaceOwner(ctx context.Context, wsID int64) (string, int64, bool) {
	if s.q == nil {
		return "", 0, false
	}
	ws, err := s.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return "", 0, false
	}
	return ws.OwnerType, ws.OwnerID, true
}

func rowToConfig(row store.WorkspaceLocalRunner) LocalRunnerConfig {
	cmds := []string{}
	if row.AllowedCommands != "" && row.AllowedCommands != "null" {
		_ = json.Unmarshal([]byte(row.AllowedCommands), &cmds)
		if cmds == nil {
			cmds = []string{}
		}
	}
	return LocalRunnerConfig{
		LocalDir:           row.LocalDir,
		PromptSnippet:      row.PromptSnippet,
		AllowedCommands:    cmds,
		AlwaysAllowPersist: row.AlwaysAllowPersist != 0,
	}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
