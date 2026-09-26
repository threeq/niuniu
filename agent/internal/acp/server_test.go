package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/prompt"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// fakeModel scripts per-round assistant blocks and captures the last request.
type fakeModel struct {
	mu      sync.Mutex
	turn    int
	turns   [][]model.Block
	usage   []model.Usage // optional per-round usage; indexed with turns
	lastReq model.Request
}

func (f *fakeModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastReq = req
	if f.turn >= len(f.turns) {
		return nil, fmt.Errorf("script exhausted")
	}
	blocks := f.turns[f.turn]
	idx := f.turn
	f.turn++
	resp := &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: blocks}}
	if idx < len(f.usage) {
		resp.Usage = f.usage[idx]
	}
	return resp, nil
}

func (f *fakeModel) rounds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.turn
}

func (f *fakeModel) lastSystem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq.System
}

// client is the test-side ACP client.
type client struct {
	t       *testing.T
	w       io.Writer
	permAns func(requestPermissionParams) string
	updates chan updateBody
	ids     map[string]chan json.RawMessage
	mu      sync.Mutex
	nextID  int
}

func newClient(t *testing.T, w io.Writer, r io.Reader, permAns func(requestPermissionParams) string) *client {
	c := &client{
		t: t, w: w, permAns: permAns,
		updates: make(chan updateBody, 64),
		ids:     make(map[string]chan json.RawMessage),
	}
	go c.readLoop(r)
	return c
}

func (c *client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  json.RawMessage `json:"error,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if json.Unmarshal(sc.Bytes(), &frame) != nil {
			continue
		}
		switch {
		case frame.Method == "session/update":
			var p sessionUpdateParams
			if json.Unmarshal(frame.Params, &p) == nil {
				c.updates <- p.Update
			}
		case frame.Method == "session/request_permission":
			var p requestPermissionParams
			if json.Unmarshal(frame.Params, &p) == nil {
				opt := "reject_once"
				if c.permAns != nil {
					opt = c.permAns(p)
				}
				c.send(map[string]any{
					"jsonrpc": "2.0", "id": json.RawMessage(frame.ID),
					"result": map[string]any{
						"outcome": map[string]string{"outcome": "selected", "optionId": opt},
					},
				})
			}
		case len(frame.ID) > 0 && frame.Method == "":
			// frame.ID is raw JSON (keeps its quotes); normalize before lookup.
			id := strings.Trim(string(frame.ID), `"`)
			// Error 帧（result 为空）也要投递，让调用方看到 RPC 错误。
			payload := frame.Result
			if len(payload) == 0 && len(frame.Error) > 0 {
				payload = frame.Error
			}
			c.mu.Lock()
			ch, ok := c.ids[id]
			delete(c.ids, id)
			c.mu.Unlock()
			if ok && len(payload) > 0 {
				ch <- payload
			}
		}
	}
}

func (c *client) send(v any) {
	data, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.w.Write(append(data, '\n'))
}

func (c *client) call(method string, params any) json.RawMessage {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("client-%d", c.nextID)
	ch := make(chan json.RawMessage, 1)
	c.ids[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case res := <-ch:
		return res
	case <-time.After(120 * time.Second): // real-model e2e turns are slow; fakes return instantly
		c.t.Fatalf("timeout waiting for %s response", method)
		return nil
	}
}

func (c *client) nextUpdate(d time.Duration) updateBody {
	select {
	case u := <-c.updates:
		return u
	case <-time.After(d):
		c.t.Fatal("timeout waiting for session/update")
		return updateBody{}
	}
}

// startServer runs an in-process ACP server over pipes with a static system
// prompt.
func startServer(t *testing.T, turns [][]model.Block, permAns func(requestPermissionParams) string) (*client, *fakeModel) {
	t.Helper()
	return startServerWithSystem(t, turns, permAns, func(string) string { return "test-system" })
}

// startServerWithSystem runs an in-process ACP server with a custom
// per-session system prompt builder.
func startServerWithSystem(t *testing.T, turns [][]model.Block, permAns func(requestPermissionParams) string, systemFor func(string) string) (*client, *fakeModel) {
	t.Helper()
	// session/new chdirs into the session cwd; restore so TempDir cleanup
	// can remove it on Windows (a process cannot delete its own cwd there).
	if orig, err := os.Getwd(); err == nil {
		t.Cleanup(func() { _ = os.Chdir(orig) })
	}
	toSrvR, toSrvW := io.Pipe()     // client → server
	fromSrvR, fromSrvW := io.Pipe() // server → client
	fm := &fakeModel{turns: turns}
	srv := New(toSrvR, fromSrvW, tools.NewRegistry(tools.LS{}, tools.Read{}, tools.Write{}),
		systemFor,
		func() (model.Model, error) { return fm, nil }, nil, nil)
	go func() { _ = srv.Serve(context.Background()) }()
	return newClient(t, toSrvW, fromSrvR, permAns), fm
}

func TestACPHandshakeAndPrompt(t *testing.T) {
	// Allocate the temp dir BEFORE startServer so the cwd-restore cleanup
	// (registered later) runs before this dir's removal (LIFO).
	dir := t.TempDir()
	turns := [][]model.Block{
		{{Type: model.BlockToolUse, ID: "tu_1", Name: "LS", Input: json.RawMessage(`{}`)}},
		{{Type: model.BlockText, Text: "all done"}},
	}
	cl, fm := startServer(t, turns, nil)
	fm.mu.Lock()
	fm.usage = []model.Usage{
		{InputTokens: 10, OutputTokens: 2},
		{InputTokens: 20, OutputTokens: 3, CacheReadTokens: 7},
	}
	fm.mu.Unlock()

	var initRes initializeResult
	if err := json.Unmarshal(cl.call("initialize", map[string]any{"protocolVersion": 1}), &initRes); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if initRes.ProtocolVersion != protocolVersion {
		t.Errorf("protocolVersion = %d", initRes.ProtocolVersion)
	}

	var sessRes sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": dir}), &sessRes); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if sessRes.SessionID == "" {
		t.Fatal("empty sessionId")
	}

	promptRes := cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "list the directory"}},
	})
	var pr sessionPromptResult
	if err := json.Unmarshal(promptRes, &pr); err != nil {
		t.Fatalf("session/prompt result: %v (raw %s)", err, promptRes)
	}
	if pr.StopReason != "end_turn" {
		t.Errorf("stopReason = %q", pr.StopReason)
	}
	// Usage telemetry rides the prompt result (aggregated across rounds).
	if pr.Usage == nil {
		t.Fatal("session/prompt result missing usage")
	}
	wantUsage := usageBody{InputTokens: 30, OutputTokens: 5, CacheReadTokens: 7}
	if *pr.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", *pr.Usage, wantUsage)
	}
	if fm.rounds() != 2 {
		t.Fatalf("model rounds = %d, want 2", fm.rounds())
	}

	// Expect tool_call → tool_call_update → agent_message_chunk, in order.
	u1 := cl.nextUpdate(time.Second)
	if u1.SessionUpdate != "tool_call" || u1.ToolCallID != "tu_1" || u1.Kind != "read" {
		t.Errorf("update1 = %+v", u1)
	}
	u2 := cl.nextUpdate(time.Second)
	if u2.SessionUpdate != "tool_call_update" || u2.Status != "completed" {
		t.Errorf("update2 = %+v", u2)
	}
	u3 := cl.nextUpdate(time.Second)
	if u3.SessionUpdate != "agent_message_chunk" || u3.Content == nil || u3.Content.Text != "all done" {
		t.Errorf("update3 = %+v", u3)
	}
}

func TestACPPermissionAllowAndDeny(t *testing.T) {
	// Restore the approval flow for this test (default is bypass).
	t.Setenv("NIUNIU_AGENT_PERMISSION_MODE", "normal")
	// Same LIFO rule as TestACPHandshakeAndPrompt: temp dirs first.
	tmpA, tmpB := t.TempDir(), t.TempDir()
	target := filepath.Join(tmpA, "out.txt")
	turns := [][]model.Block{
		{{Type: model.BlockToolUse, ID: "w1", Name: "Write",
			Input: json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"hi"}`, target))}},
		{{Type: model.BlockText, Text: "wrote it"}},
	}

	// Allow: the write executes end-to-end.
	cl, _ := startServer(t, turns, func(requestPermissionParams) string { return "allow_once" })
	var sess sessionNewResult
	_ = json.Unmarshal(cl.call("session/new", map[string]any{"cwd": tmpB}), &sess)
	res := cl.call("session/prompt", map[string]any{
		"sessionId": sess.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "write the file"}},
	})
	if !strings_has(res, `"stopReason":"end_turn"`) {
		t.Errorf("allow path prompt result = %s", res)
	}
	u := cl.nextUpdate(time.Second) // tool_call
	if u.SessionUpdate != "tool_call" || u.Kind != "edit" {
		t.Errorf("allow tool_call = %+v", u)
	}
	u = cl.nextUpdate(time.Second) // tool_call_update
	if u.Status != "completed" {
		t.Errorf("allow tool_call_update status = %q, want completed", u.Status)
	}

	// Deny: the write must not run.
	denied := 0
	cl2, _ := startServer(t, turns, func(p requestPermissionParams) string {
		denied++
		return "reject_once"
	})
	var sess2 sessionNewResult
	_ = json.Unmarshal(cl2.call("session/new", map[string]any{"cwd": tmpB}), &sess2)
	_ = cl2.call("session/prompt", map[string]any{
		"sessionId": sess2.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "write the file"}},
	})
	u = cl2.nextUpdate(time.Second)
	if u.SessionUpdate != "tool_call" {
		t.Fatalf("deny update1 = %+v", u)
	}
	u = cl2.nextUpdate(time.Second)
	if u.Status != "failed" {
		t.Errorf("deny tool_call_update status = %q, want failed", u.Status)
	}
	if denied == 0 {
		t.Error("permission request was never sent to the client")
	}
}

// strings_has is a tiny helper avoiding repeated conversions in assertions.
func strings_has(b json.RawMessage, sub string) bool {
	return strings.Contains(string(b), sub)
}

func TestACPSessionLoadsProjectContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("SESSION-CTX-MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	turns := [][]model.Block{{{Type: model.BlockText, Text: "done"}}}
	cl, fm := startServerWithSystem(t, turns, nil, prompt.Build)

	cl.call("initialize", map[string]any{"protocolVersion": 1})
	sessRes := cl.call("session/new", map[string]any{"cwd": dir})
	var sess sessionNewResult
	if err := json.Unmarshal(sessRes, &sess); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sess.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "hi"}},
	})
	if sys := fm.lastSystem(); !strings.Contains(sys, "SESSION-CTX-MARKER") {
		t.Errorf("session system prompt missing AGENTS.md content:\n%.300s", sys)
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// probeTool 返回固定文本，用于验证 extend 注入的会话注册表。
type probePong struct{ name string }

func (p probePong) Def() model.ToolDef {
	return model.ToolDef{Name: p.name, Description: "probe", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (p probePong) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "pong", nil }

func TestACPExtendSessionRegistryAndLifecycle(t *testing.T) {
	// TempDir first: its cleanup must run AFTER the cwd-restore (LIFO), or
	// Windows cannot delete a directory that is still the process cwd.
	dir := t.TempDir()
	if orig, err := os.Getwd(); err == nil {
		t.Cleanup(func() { _ = os.Chdir(orig) })
	}
	toSrvR, toSrvW := io.Pipe()
	fromSrvR, fromSrvW := io.Pipe()
	fm := &fakeModel{turns: [][]model.Block{
		{{Type: model.BlockToolUse, ID: "tu_1", Name: "mcp__proj__ping", Input: json.RawMessage(`{}`)}},
		{{Type: model.BlockText, Text: "done"}},
	}}
	closed := make(chan struct{})
	extend := func(cwd string, m model.Model) (*tools.Registry, io.Closer, error) {
		reg := tools.NewRegistry(probePong{name: "mcp__proj__ping"})
		return reg, closerFunc(func() error { close(closed); return nil }), nil
	}
	srv := New(toSrvR, fromSrvW, tools.NewRegistry(tools.LS{}),
		func(string) string { return "sys" },
		func() (model.Model, error) { return fm, nil }, extend, nil)
	go func() { _ = srv.Serve(context.Background()) }()
	cl := newClient(t, toSrvW, fromSrvR, nil)

	var sessRes sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": dir}), &sessRes); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "ping"}},
	})

	// Extend 注入的工具被会话真实调用并回填。
	u1 := cl.nextUpdate(time.Second)
	if u1.SessionUpdate != "tool_call" || u1.ToolCallID != "tu_1" {
		t.Fatalf("update1 = %+v", u1)
	}
	u2 := cl.nextUpdate(time.Second)
	if u2.SessionUpdate != "tool_call_update" || u2.Status != "completed" ||
		u2.Output == nil || u2.Output.Text != "pong" {
		t.Fatalf("update2 = %+v, want completed/pong", u2)
	}
	// 基础注册表不可见：模型请求里只有 extend 注入的工具。
	if tools := fm.lastReq.Tools; len(tools) != 1 || tools[0].Name != "mcp__proj__ping" {
		t.Errorf("session tools = %+v, want only mcp__proj__ping", tools)
	}

	// 生命周期：输入流结束（进程退出）时会话资源被关闭。
	toSrvW.Close()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("session closer not closed after Serve returns")
	}
}

// P4 验收 e2e：niuniu 工作空间形态目录（.niuniu-agent/inject.md + 记忆）
// 下，session 的 system 必须包含注入内容与召回的记忆。
func TestACPWorkspaceInjectionAndMemoryRecall(t *testing.T) {
	// TempDir 先建（Windows cwd 清理顺序，见 TestACPExtendSessionRegistryAndLifecycle）。
	dir := t.TempDir()
	if orig, err := os.Getwd(); err == nil {
		t.Cleanup(func() { _ = os.Chdir(orig) })
	}
	os.MkdirAll(filepath.Join(dir, ".niuniu-agent"), 0o755)
	os.WriteFile(filepath.Join(dir, ".niuniu-agent", "inject.md"),
		[]byte("niuniu-mcp 工具族可用；收尾时输出 [AUTOHOST_DONE]。"), 0o644)
	// 预置一条记忆（直接落项目层）。
	memDir := filepath.Join(dir, ".niuniu-agent", "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "deploy-gate.md"),
		[]byte("---\ntitle: deploy-gate\ntype: decision\ntags: \ncreated: 2026-09-15T00:00:00Z\nupdated: 2026-09-15T00:00:00Z\n---\n\n发布前必须过 harness gate。\n"), 0o644)

	toSrvR, toSrvW := io.Pipe()
	fromSrvR, fromSrvW := io.Pipe()
	fm := &fakeModel{turns: [][]model.Block{
		{{Type: model.BlockText, Text: "ok"}},
	}}
	srv := New(toSrvR, fromSrvW, tools.NewRegistry(tools.LS{}),
		prompt.BuildSession, // 与 main/ACP 一致的完整组装（Build + 记忆召回）
		func() (model.Model, error) { return fm, nil }, nil, nil)
	go func() { _ = srv.Serve(context.Background()) }()
	cl := newClient(t, toSrvW, fromSrvR, nil)

	var sessRes sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": dir}), &sessRes); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "hi"}},
	})

	sys := fm.lastReq.System
	if !strings.Contains(sys, "# Host capabilities") || !strings.Contains(sys, "[AUTOHOST_DONE]") {
		t.Errorf("system missing inject.md content:\n%s", sys)
	}
	if !strings.Contains(sys, "deploy-gate") || !strings.Contains(sys, "harness gate") {
		t.Errorf("system missing memory recall:\n%s", sys)
	}
}

func TestACPThoughtChunk(t *testing.T) {
	// TempDir first: its cleanup must run AFTER startServer's cwd-restore
	// (LIFO), or Windows cannot delete a directory still in use as cwd.
	dir := t.TempDir()
	turns := [][]model.Block{
		{
			{Type: model.BlockThinking, Text: "reasoning about it"},
			{Type: model.BlockText, Text: "the answer"},
		},
	}
	cl, _ := startServer(t, turns, nil)
	var sessRes sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": dir}), &sessRes); err != nil {
		t.Fatal(err)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "q"}},
	})
	u1 := cl.nextUpdate(time.Second)
	if u1.SessionUpdate != "agent_thought_chunk" || u1.Content == nil || u1.Content.Text != "reasoning about it" {
		t.Fatalf("update1 = %+v, want agent_thought_chunk", u1)
	}
	u2 := cl.nextUpdate(time.Second)
	if u2.SessionUpdate != "agent_message_chunk" || u2.Content == nil || u2.Content.Text != "the answer" {
		t.Fatalf("update2 = %+v, want agent_message_chunk", u2)
	}
}

func TestACPImageBlockInPrompt(t *testing.T) {
	dir := t.TempDir()
	turns := [][]model.Block{
		{{Type: model.BlockText, Text: "seen"}},
	}
	cl, fm := startServer(t, turns, nil)
	var sessRes sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": dir}), &sessRes); err != nil {
		t.Fatal(err)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": "看这张图"},
			{"type": "image", "data": "aGk=", "mimeType": "image/png"},
		},
	})
	// user 消息 = text 块 + image 块（base64 透传）。
	blocks := fm.lastReq.Messages[0].Blocks
	if len(blocks) != 2 || blocks[0].Type != model.BlockText ||
		blocks[1].Type != model.BlockImage || blocks[1].Media != "aGk=" || blocks[1].MIME != "image/png" {
		t.Fatalf("user blocks = %+v", blocks)
	}
}
