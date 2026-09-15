package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
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
}

// New builds a Server over the given transport. newModel is called once per
// session to build the model backend (so config errors surface per session);
// systemFor builds the system prompt per session cwd (project context such
// as AGENTS.md is resolved against it).
func New(in io.Reader, out io.Writer, reg *tools.Registry, systemFor func(cwd string) string, newModel func() (model.Model, error)) *Server {
	return &Server{
		in:        in,
		out:       out,
		reg:       reg,
		systemFor: systemFor,
		newModel:  newModel,
		sessions:  make(map[string]*sessionState),
		pending:   make(map[int64]chan requestPermissionResult),
	}
}

// Serve reads and dispatches until the input stream ends.
func (s *Server) Serve(ctx context.Context) error {
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
		result, rpcErr := s.handle(ctx, req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		s.write(resp)
	}()
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
		s.sessMu.Lock()
		s.nextID++
		id := "s-" + strconv.Itoa(s.nextID)
		st := &sessionState{
			id:   id,
			cwd:  p.CWD,
			conv: loop.NewSession(m, s.reg, s.systemFor(sessionCwd(p.CWD))),
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

	case "session/set_mode", "session/set_model":
		// Accepted as no-ops; model selection is fixed at process env for now.
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

	var userParts []string
	for _, b := range p.Prompt {
		if b.Type == "text" && b.Text != "" {
			userParts = append(userParts, b.Text)
		}
	}
	if len(userParts) == 0 {
		return nil, &rpcError{Code: errInvalidParams, Message: "prompt has no text blocks"}
	}

	run := &promptRun{srv: s, sess: st}
	res, err := st.conv.Prompt(ctx, strings.Join(userParts, "\n"), loop.Options{
		OnEvent: run.onEvent,
		Perms:   run,
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			out, _ := json.Marshal(sessionPromptResult{StopReason: "cancelled"})
			return out, nil
		}
		return nil, &rpcError{Code: errInternal, Message: err.Error()}
	}
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

// promptRun carries per-prompt state for event streaming and permission
// escalation. All methods run on the single prompt goroutine except
// requestPermission's response delivery.
type promptRun struct {
	srv        *Server
	sess       *sessionState
	toolCallID string
}

func (r *promptRun) onEvent(e loop.Event) {
	switch e.Kind {
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
	case <-time.After(10 * time.Minute):
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
