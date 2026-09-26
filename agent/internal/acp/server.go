package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/loop"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Server is the ACP agent side over one newline-delimited JSON-RPC stdio
// transport. New(...) wires a transport; Serve reads requests until EOF or
// context cancellation.
type Server struct {
	in        io.Reader
	out       io.Writer
	reg       *tools.Registry
	systemFor func(cwd string) string
	newModel  func() (model.Model, error)
	// extend, when set, builds the per-session tool registry (base tools +
	// session-local capabilities such as MCP servers) and a closer released
	// when the session's lifetime ends. A nil registry + nil error keeps the
	// base registry.
	extend func(cwd string, m model.Model) (*tools.Registry, io.Closer, error)
	// modelFor resolves a session/set_model model name to a Model (profile
	// switch). Nil → set_model stays a no-op rejection.
	modelFor func(modelName string) (model.Model, error)

	writeMu sync.Mutex

	sessMu   sync.Mutex
	sessions map[string]*sessionState
	nextID   int

	pendingMu sync.Mutex
	reqNext   int64
	pending   map[int64]chan requestPermissionResult
}

// sessionState is one ACP session: conversation + cwd + cancellation.
type sessionState struct {
	id       string
	cwd      string
	conv     *loop.Session
	cancel   context.CancelFunc
	promptMu sync.Mutex // one in-flight prompt per session
	closer   io.Closer  // session-scoped resources (MCP servers)
}

// New builds a Server over the given transport. newModel is called once per
// session to build the model backend (so config errors surface per session);
// systemFor builds the system prompt per session cwd (project context such
// as AGENTS.md is resolved against it); extend (optional) builds the
// session's tool registry — used for MCP tool projection.
func New(in io.Reader, out io.Writer, reg *tools.Registry, systemFor func(cwd string) string, newModel func() (model.Model, error), extend func(cwd string, m model.Model) (*tools.Registry, io.Closer, error), modelFor func(string) (model.Model, error)) *Server {
	return &Server{
		in:        in,
		out:       out,
		reg:       reg,
		systemFor: systemFor,
		newModel:  newModel,
		extend:    extend,
		modelFor:  modelFor,
		sessions:  make(map[string]*sessionState),
		pending:   make(map[int64]chan requestPermissionResult),
	}
}

// Serve reads and dispatches until the input stream ends. On return every
// session's scoped resources (MCP servers) are shut down — the servers'
// lifetime is the process's, which in niuniu's deployment is one workspace.
// Serve reads and dispatches until the input stream ends. A panic in the
// loop itself (outside the per-request recover) is logged and surfaced as a
// clean error instead of killing the process without a trace.
func (s *Server) Serve(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("acp: serve loop panic", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("acp: serve loop panic: %v", r)
		}
		s.closeSessions()
	}()
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: errParse, Message: "parse error"}})
			continue
		}
		if req.Method == "" && len(req.ID) > 0 {
			// A JSON-RPC response to one of our agent-side requests
			// (currently: session/request_permission outcomes).
			if id, perr := strconv.ParseInt(string(req.ID), 10, 64); perr == nil {
				res := req.Result
				if req.Error != nil {
					res = json.RawMessage(`{}`) // client-side error → deny
				}
				s.deliverResponse(id, res)
			}
			continue
		}
		if len(req.ID) > 0 && string(req.ID) != "null" {
			s.dispatch(ctx, req)
		} else {
			s.dispatchNotification(req)
		}
	}
	return sc.Err()
}

// write emits one ndjson frame.
func (s *Server) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("acp: marshal frame", "err", err)
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.out.Write(append(data, '\n'))
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) {
	go func() {
		// A panic in a handler goroutine would kill the whole agent process
		// (stdio EOF → a hung turn on the host, with no trace). Recover into
		// a JSON-RPC error and log the stack instead.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("acp: handler panic", "method", req.Method, "panic", r, "stack", string(debug.Stack()))
				s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID,
					Error: &rpcError{Code: errInternal, Message: fmt.Sprintf("internal panic: %v", r)}})
			}
		}()
		started := time.Now()
		result, rpcErr := s.handle(ctx, req)
		slog.Info("acp: request handled", "method", req.Method, "id", string(req.ID),
			"ms", time.Since(started).Milliseconds(), "err", rpcErrString(rpcErr))
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		s.write(resp)
	}()
}

func rpcErrString(e *rpcError) string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (json.RawMessage, *rpcError) {
	switch req.Method {
	case "initialize":
		var p initializeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: errInvalidParams, Message: err.Error()}
		}
		out, _ := json.Marshal(initializeResult{
			ProtocolVersion:   protocolVersion,
			AgentCapabilities: agentCapabilities{LoadSession: false},
		})
		return out, nil

	case "session/new":
		var p sessionNewParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: errInvalidParams, Message: err.Error()}
		}
		m, err := s.newModel()
		if err != nil {
			return nil, &rpcError{Code: errInternal, Message: err.Error()}
		}
		// Tools resolve relative paths against the process working directory.
		// niuniu's deployment model is one agent process per workspace, so
		// adopting the session cwd via chdir is the honest, simple contract.
		if p.CWD != "" {
			if err := os.Chdir(p.CWD); err != nil {
				slog.Warn("acp: chdir to session cwd failed", "cwd", p.CWD, "err", err)
			}
		}
		// Per-session tool registry: base tools plus whatever extend projects
		// for this cwd (MCP servers). An extend failure degrades to the base
		// registry — never blocks the session.
		reg, closer := s.reg, io.Closer(nil)
		if s.extend != nil {
			if r, c, err := s.extend(sessionCwd(p.CWD), m); err != nil {
				slog.Warn("acp: session tool extension failed (using base tools)", "cwd", p.CWD, "err", err)
			} else if r != nil {
				reg, closer = r, c
			}
		}
		cwd := sessionCwd(p.CWD)
		sys := s.systemFor(cwd)
		// Auto-resume: continue the project's most recent conversation across
		// agent-process restarts (the server may respawn us any time).
		// Messages come from the snapshot; the system prompt is rebuilt fresh
		// (env/project context may have moved on). NIUNIU_AGENT_NO_RESUME=1
		// opts out; any failure falls back to a fresh session.
		conv := loop.NewSession(m, reg, sys)
		if os.Getenv("NIUNIU_AGENT_NO_RESUME") != "1" {
			if snapID, err := loop.LatestSessionID(tools.SessionsDir(cwd)); err == nil {
				if snap, err := loop.LoadSession(tools.SessionsDir(cwd), snapID); err == nil && len(snap.Messages) > 0 {
					conv = loop.RestoreSessionKeepingMessages(m, reg, snap, sys)
					slog.Info("acp: resumed previous session", "snapshot", snapID,
						"messages", len(snap.Messages))
				}
			}
		}
		s.sessMu.Lock()
		s.nextID++
		id := "s-" + strconv.Itoa(s.nextID)
		st := &sessionState{
			id:     id,
			cwd:    p.CWD,
			conv:   conv,
			closer: closer,
		}
		s.sessions[id] = st
		s.sessMu.Unlock()
		out, _ := json.Marshal(sessionNewResult{SessionID: id})
		return out, nil

	case "session/prompt":
		var p sessionPromptParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: errInvalidParams, Message: err.Error()}
		}
		s.sessMu.Lock()
		st := s.sessions[p.SessionID]
		s.sessMu.Unlock()
		if st == nil {
			return nil, &rpcError{Code: errInvalidParams, Message: "unknown session " + p.SessionID}
		}
		return s.runPrompt(ctx, st, p)

	case "session/set_mode":
		return json.RawMessage(`{}`), nil
	case "session/set_model":
		// Session-level profile switch: the client sends a model name; the
		// modelFor resolver maps it (config profiles → Model). Unknown names
		// error back so the client can surface them.
		var p struct {
			SessionID string `json:"sessionId"`
			Model     string `json:"model"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: errInvalidParams, Message: err.Error()}
		}
		s.sessMu.Lock()
		st := s.sessions[p.SessionID]
		s.sessMu.Unlock()
		if st == nil {
			return nil, &rpcError{Code: errInvalidParams, Message: "unknown session " + p.SessionID}
		}
		if s.modelFor == nil {
			return nil, &rpcError{Code: errMethodNotFound, Message: "set_model not supported (no model resolver configured)"}
		}
		m, err := s.modelFor(p.Model)
		if err != nil {
			slog.Warn("acp: set_model rejected", "model", p.Model, "err", err)
			return nil, &rpcError{Code: errInvalidParams, Message: err.Error()}
		}
		st.conv.SetModel(m)
		return json.RawMessage(`{}`), nil

	default:
		return nil, &rpcError{Code: errMethodNotFound, Message: "method not supported: " + req.Method}
	}
}

// runPrompt drives one user turn, streaming progress as session/update
// notifications and returning the stop reason.
func (s *Server) runPrompt(parent context.Context, st *sessionState, p sessionPromptParams) (json.RawMessage, *rpcError) {
	st.promptMu.Lock()
	defer st.promptMu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	st.cancel = cancel

	var userBlocks []model.Block
	for _, b := range p.Prompt {
		switch b.Type {
		case "text":
			if b.Text != "" {
				userBlocks = append(userBlocks, model.Block{Type: model.BlockText, Text: b.Text})
			}
		case "image":
			if b.Data != "" {
				userBlocks = append(userBlocks, model.Block{
					Type: model.BlockImage, Media: b.Data, MIME: b.MimeType,
				})
			}
		}
	}
	if len(userBlocks) == 0 {
		return nil, &rpcError{Code: errInvalidParams, Message: "prompt has no usable blocks"}
	}

	run := &promptRun{srv: s, sess: st}
	slog.Info("acp: prompt start", "session", st.id, "cwd", st.cwd,
		"blocks", len(userBlocks))
	promptStarted := time.Now()
	res, err := st.conv.PromptBlocks(ctx, userBlocks, loop.Options{
		OnEvent:          run.onEvent,
		Perms:            run,
		CompactStatePath: tools.CompactStatePath(st.cwd),
		ContextEditing:   os.Getenv("NIUNIU_AGENT_CONTEXT_EDITING") == "1",
		HistoryDir:       tools.HistoryDir(st.cwd),
		// SSE streaming: session/update chunks become incremental
		// (agent_message_chunk / agent_thought_chunk per delta).
		Stream: model.ParseStreamFlag(os.Getenv("NIUNIU_AGENT_STREAM")),
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			out, _ := json.Marshal(sessionPromptResult{StopReason: "cancelled"})
			return out, nil
		}
		return nil, &rpcError{Code: errInternal, Message: err.Error()}
	}
	// Persist the session snapshot after every completed turn — headless -p
	// has always saved, but ACP never did (so there was nothing on disk to
	// resume). Best-effort: a failed write must not fail the turn.
	if err := saveACPSession(st); err != nil {
		slog.Warn("acp: save session snapshot failed", "session", st.id, "err", err)
	}
	slog.Info("acp: prompt done", "session", st.id,
		"ms", time.Since(promptStarted).Milliseconds(), "rounds", res.Rounds,
		"out", res.Usage.OutputTokens, "in", res.Usage.InputTokens)
	out, _ := json.Marshal(sessionPromptResult{
		StopReason: "end_turn",
		Usage: &usageBody{
			InputTokens:     res.Usage.InputTokens,
			OutputTokens:    res.Usage.OutputTokens,
			CacheReadTokens: res.Usage.CacheReadTokens,
		},
	})
	return out, nil
}

// saveACPSession writes the session snapshot to the per-project state dir
// under the user home (~/.niuniu-agent/projects/<escaped-cwd>/sessions).
func saveACPSession(st *sessionState) error {
	return loop.SaveSession(tools.SessionsDir(st.cwd), st.conv.ExportState(st.id))
}

func (s *Server) dispatchNotification(req rpcRequest) {
	if req.Method == "session/cancel" {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			s.sessMu.Lock()
			st := s.sessions[p.SessionID]
			s.sessMu.Unlock()
			if st != nil && st.cancel != nil {
				st.cancel()
			}
		}
	}
	// Unknown notifications are ignored per JSON-RPC.
}

// closeSessions releases every session's scoped resources (MCP servers).
func (s *Server) closeSessions() {
	s.sessMu.Lock()
	sts := make([]*sessionState, 0, len(s.sessions))
	for _, st := range s.sessions {
		sts = append(sts, st)
	}
	s.sessMu.Unlock()
	for _, st := range sts {
		if st.closer != nil {
			_ = st.closer.Close()
		}
	}
}

// promptRun carries per-prompt state for event streaming and permission
// escalation. All methods run on the single prompt goroutine except
// requestPermission's response delivery.
type promptRun struct {
	srv        *Server
	sess       *sessionState
	toolCallID string
}

func (r *promptRun) onEvent(e loop.Event) {
	slog.Info("acp: agent event", "kind", e.Kind, "delta", e.Delta,
		"text", truncateForLog(e.Text), "tool", e.ToolName)
	switch e.Kind {
	case loop.EventThinking:
		r.srv.notify(r.sess.id, updateBody{
			SessionUpdate: "agent_thought_chunk",
			Content:       &contentBody{Type: "text", Text: e.Text},
		})
	case loop.EventText:
		r.srv.notify(r.sess.id, updateBody{
			SessionUpdate: "agent_message_chunk",
			Content:       &contentBody{Type: "text", Text: e.Text},
		})
	case loop.EventToolStart:
		r.toolCallID = e.ToolID
		r.srv.notify(r.sess.id, updateBody{
			SessionUpdate: "tool_call",
			ToolCallID:    e.ToolID,
			Title:         e.ToolName,
			Kind:          toolKind(e.ToolName),
			Status:        "in_progress",
			RawInput:      json.RawMessage(defaultJSON(e.ToolInput)),
		})
	case loop.EventToolEnd:
		status := "completed"
		if e.IsError {
			status = "failed"
		}
		r.srv.notify(r.sess.id, updateBody{
			SessionUpdate: "tool_call_update",
			ToolCallID:    e.ToolID,
			Status:        status,
			Output:        &contentBody{Type: "text", Text: e.ToolOutput},
		})
	}
}

// Check implements perm.Checker: read-only tools pass; mutating tools are
// escalated to the client via session/request_permission.
func (r *promptRun) Check(toolName string) perm.Decision {
	// Default: allow everything. The niuniu server / desktop app is a local
	// single-user host — mutating tools run without per-call approval,
	// matching claude's bypassPermissions contract. The ACP approval flow is
	// opt-in via NIUNIU_AGENT_PERMISSION_MODE=normal (IDE / multi-tenant
	// hosts that render the approval card).
	if os.Getenv("NIUNIU_AGENT_PERMISSION_MODE") != "normal" {
		return perm.Allow
	}
	if !perm.IsWrite(toolName) {
		return perm.Allow
	}
	res, err := r.srv.requestPermission(r.sess.id, r.toolCallID, toolName)
	if err != nil {
		slog.Warn("acp: permission request failed", "tool", toolName, "err", err)
		return perm.Deny
	}
	if res.Outcome.Outcome == "selected" && strings.HasPrefix(res.Outcome.OptionID, "allow") {
		return perm.Allow
	}
	return perm.Deny
}

// requestPermission sends session/request_permission and blocks for the
// client's outcome.
func (s *Server) requestPermission(sessionID, toolCallID, toolName string) (requestPermissionResult, error) {
	s.pendingMu.Lock()
	s.reqNext++
	id := s.reqNext
	ch := make(chan requestPermissionResult, 1)
	s.pending[id] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()

	params, _ := json.Marshal(requestPermissionParams{
		SessionID:  sessionID,
		ToolCallID: toolCallID,
		Title:      toolName,
		Kind:       toolKind(toolName),
		Options: []permissionOpt{
			{OptionID: "allow_once", Name: "Allow", Kind: "allow_once"},
			{OptionID: "reject_once", Name: "Reject", Kind: "reject_once"},
		},
	})
	s.write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: "session/request_permission", Params: params})

	select {
	case res := <-ch:
		return res, nil
	case <-time.After(120 * time.Second):
		// Host did not answer the approval card in time (e.g. the frontend
		// has no approval UI for this engine) — deny so the turn keeps
		// making progress instead of parking forever.
		slog.Warn("acp: permission request timed out — denying", "session", sessionID, "toolCallID", toolCallID)
		return requestPermissionResult{}, errors.New("permission request timed out")
	}
}

// deliverResponse routes a client response to a pending agent-side request.
func (s *Server) deliverResponse(id int64, raw json.RawMessage) {
	s.pendingMu.Lock()
	ch := s.pending[id]
	s.pendingMu.Unlock()
	if ch == nil {
		return
	}
	var res requestPermissionResult
	if err := json.Unmarshal(raw, &res); err != nil {
		ch <- requestPermissionResult{} // unparseable → deny
		return
	}
	ch <- res
}

func (s *Server) notify(sessionID string, upd updateBody) {
	params, _ := json.Marshal(sessionUpdateParams{SessionID: sessionID, Update: upd})
	s.write(rpcNotification{JSONRPC: "2.0", Method: "session/update", Params: params})
}

// toolKind maps a tool name to its ACP kind for UI rendering.
func toolKind(name string) string {
	switch name {
	case "Write", "Edit":
		return "edit"
	case "Bash":
		return "execute"
	case "TodoWrite":
		return "other"
	default:
		return "read"
	}
}

func defaultJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// sessionCwd resolves the directory a session's system prompt is built
// against: the announced cwd, or the process cwd when the client sent none.
func sessionCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func truncateForLog(s string) string {
	if len(s) <= 120 {
		return s
	}
	return s[:120] + "…"
}
