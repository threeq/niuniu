package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// echoModel 记录自己被使用的次数。
type echoModel struct {
	tag  string
	uses int
}

func (e *echoModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	e.uses++
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
		{Type: model.BlockText, Text: "from:" + e.tag}}}}, nil
}

// set_model 会话级切换：切换后下一轮 prompt 由新模型应答，旧模型不再被用；
// 未知名字以 RPC error 返回且不影响会话。
func TestACPSetModelSwitchesSessionModel(t *testing.T) {
	dir := t.TempDir()
	// session/new chdirs into dir; restore BEFORE dir's cleanup (LIFO) so
	// Windows can delete a directory that is still the process cwd.
	if orig, err := os.Getwd(); err == nil {
		t.Cleanup(func() { _ = os.Chdir(orig) })
	}
	toSrvR, toSrvW := io.Pipe()
	fromSrvR, fromSrvW := io.Pipe()
	oldM := &echoModel{tag: "old"}
	newM := &echoModel{tag: "new"}

	resolver := func(modelName string) (model.Model, error) {
		switch modelName {
		case "glm":
			return oldM, nil
		case "local":
			return newM, nil
		}
		return nil, &rpcError{Code: errInvalidParams, Message: "unknown model " + modelName}
	}
	srv := New(toSrvR, fromSrvW, tools.NewRegistry(tools.LS{}),
		func(string) string { return "sys" },
		func() (model.Model, error) { return oldM, nil }, nil, resolver)
	go func() { _ = srv.Serve(context.Background()) }()
	cl := newClient(t, toSrvW, fromSrvR, nil)

	var sessRes sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": dir}), &sessRes); err != nil {
		t.Fatal(err)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "q1"}},
	})
	if oldM.uses != 1 || newM.uses != 0 {
		t.Fatalf("before switch: old=%d new=%d", oldM.uses, newM.uses)
	}

	cl.call("session/set_model", map[string]any{
		"sessionId": sessRes.SessionID, "model": "local",
	})
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "q2"}},
	})
	if newM.uses == 0 {
		t.Error("new model never used after set_model")
	}
	if oldM.uses != 1 {
		t.Errorf("old model reused after switch (%d uses)", oldM.uses)
	}

	// 未知名字 → RPC error，会话保持原模型。
	raw := cl.call("session/set_model", map[string]any{
		"sessionId": sessRes.SessionID, "model": "nope",
	})
	if !strings.Contains(string(raw), "unknown model") {
		t.Errorf("unknown model reply = %s", raw)
	}
	cl.call("session/prompt", map[string]any{
		"sessionId": sessRes.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": "q3"}},
	})
	if oldM.uses != 1 {
		t.Errorf("session must keep its model after failed switch (%d uses)", oldM.uses)
	}
}
