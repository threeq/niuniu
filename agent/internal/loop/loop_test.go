package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// fakeModel replays a scripted sequence of responses.
type fakeModel struct {
	script []*model.Response
	errs   map[int]error // call index → error to return instead of the script
	calls  int
	reqs   []model.Request
}

func (f *fakeModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	if f.calls >= len(f.script) {
		return nil, fmt.Errorf("script exhausted")
	}
	f.reqs = append(f.reqs, req)
	idx := f.calls
	f.calls++
	if err, ok := f.errs[idx]; ok {
		return nil, err
	}
	return f.script[idx], nil
}

// stubTool records its invocations.
type stubTool struct {
	executed []json.RawMessage
}

func (s *stubTool) Def() model.ToolDef {
	return model.ToolDef{Name: "Echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (s *stubTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	s.executed = append(s.executed, input)
	return "echo-out", nil
}

func textResp(s string) *model.Response {
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockText, Text: s}}}, StopReason: model.StopEndTurn}
}

func toolUseResp(id, name string, input json.RawMessage) *model.Response {
	return &model.Response{
		Message:    model.Message{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockToolUse, ID: id, Name: name, Input: input}}},
		StopReason: model.StopToolUse,
	}
}

func TestRunToolThenAnswer(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		toolUseResp("tu_1", "Echo", json.RawMessage(`{"x":1}`)),
		textResp("all done"),
	}}
	tool := &stubTool{}
	reg := tools.NewRegistry(tool)

	res, err := Run(context.Background(), fm, reg, "sys", "go look", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "all done" {
		t.Errorf("answer = %q", res.Text)
	}
	if res.Rounds != 2 {
		t.Errorf("rounds = %d, want 2", res.Rounds)
	}
	if fm.calls != 2 {
		t.Errorf("model calls = %d, want 2", fm.calls)
	}
	if len(tool.executed) != 1 || string(tool.executed[0]) != `{"x":1}` {
		t.Errorf("tool executions = %v", tool.executed)
	}
	// Second request must carry the tool_result back to the model.
	if len(fm.reqs[1].Messages) != 3 {
		t.Fatalf("second request has %d messages, want 3", len(fm.reqs[1].Messages))
	}
	tr := fm.reqs[1].Messages[2]
	if tr.Role != model.RoleUser || tr.Blocks[0].Type != model.BlockToolResult ||
		tr.Blocks[0].ToolUseID != "tu_1" || tr.Blocks[0].Text != "echo-out" || tr.Blocks[0].IsError {
		t.Errorf("tool_result message = %+v", tr)
	}
}

func TestRunUnknownToolRecovers(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		toolUseResp("tu_1", "NoSuchTool", nil),
		textResp("recovered"),
	}}
	reg := tools.NewRegistry(&stubTool{})

	answer, err := Run(context.Background(), fm, reg, "", "go", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer.Text != "recovered" {
		t.Errorf("answer = %q", answer.Text)
	}
	tr := fm.reqs[1].Messages[2].Blocks[0]
	if !tr.IsError || !strings.Contains(tr.Text, "unknown tool") {
		t.Errorf("error tool_result = %+v", tr)
	}
}

func TestRunBudgetExhausted(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		toolUseResp("tu_1", "Echo", nil),
		toolUseResp("tu_2", "Echo", nil),
	}}
	reg := tools.NewRegistry(&stubTool{})

	_, err := Run(context.Background(), fm, reg, "", "go", Options{MaxTurns: 2})
	if err == nil || !strings.Contains(err.Error(), "turn budget exhausted") {
		t.Fatalf("err = %v, want budget exhaustion", err)
	}
}

func TestRunAggregatesUsage(t *testing.T) {
	fm := &fakeModel{script: []*model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
			{Type: model.BlockToolUse, ID: "tu_1", Name: "Echo", Input: json.RawMessage(`{}`)}}},
			StopReason: model.StopToolUse,
			Usage:      model.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 50, CacheCreationTokens: 5}},
		{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
			{Type: model.BlockText, Text: "all done"}}},
			StopReason: model.StopEndTurn,
			Usage:      model.Usage{InputTokens: 200, OutputTokens: 20, CacheReadTokens: 120}},
	}}
	reg := tools.NewRegistry(&stubTool{})

	res, err := Run(context.Background(), fm, reg, "", "go", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := model.Usage{InputTokens: 300, OutputTokens: 30, CacheReadTokens: 170, CacheCreationTokens: 5}
	if res.Usage != want {
		t.Errorf("usage = %+v, want %+v", res.Usage, want)
	}
	if res.Rounds != 2 {
		t.Errorf("rounds = %d, want 2", res.Rounds)
	}
}
