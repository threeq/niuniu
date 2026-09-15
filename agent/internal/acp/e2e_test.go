package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

func ioPipe() (io.Reader, io.Writer) { r, w := io.Pipe(); return r, w }

func systemPromptE2E() string {
	return "You are niuniu-agent. Reply in the user's language. Use tools to inspect local files; never invent file contents."
}

func buildModelE2E() (model.Model, error) {
	cfg, err := model.LoadConfig("", "")
	if err != nil {
		return nil, err
	}
	// Some harnesses export decorated model ids like "GLM-5.3-Flash[1m]";
	// the bracket segment is a context-tier marker the API rejects, so strip it.
	if i := strings.IndexByte(cfg.Model, '['); i > 0 {
		cfg.Model = cfg.Model[:i]
	}
	if cfg.Provider == model.ProviderOpenAI {
		return model.NewOpenAI(cfg), nil
	}
	return model.NewAnthropic(cfg), nil
}

// projectRootE2E is the agent module root (package dir is internal/acp).
func projectRootE2E() string {
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func containsE2E(s, sub string) bool { return strings.Contains(s, sub) }

// TestACPRealModelEndToEnd drives the full stack — ACP framing → loop → real
// model → tools — and requires live provider credentials; it skips when the
// environment has none (unit CI stays hermetic).
func TestACPRealModelEndToEnd(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		t.Skip("no provider credentials in env")
	}
	if os.Getenv("NIUNIU_AGENT_E2E") == "" {
		t.Skip("set NIUNIU_AGENT_E2E=1 to run the real-model e2e")
	}

	toSrvR, toSrvW := ioPipe()
	fromSrvR, fromSrvW := ioPipe()
	srv := New(toSrvR, fromSrvW, tools.NewRegistry(tools.LS{}, tools.Read{}, tools.Grep{}, tools.Glob{}), func(string) string { return systemPromptE2E() },
		func() (model.Model, error) { return buildModelE2E() })
	go func() { _ = srv.Serve(context.Background()) }()
	cl := newClient(t, toSrvW, fromSrvR, func(p requestPermissionParams) string { return "allow_once" })

	var initRes initializeResult
	if err := json.Unmarshal(cl.call("initialize", map[string]any{"protocolVersion": 1}), &initRes); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	var sess sessionNewResult
	if err := json.Unmarshal(cl.call("session/new", map[string]any{"cwd": projectRootE2E()}), &sess); err != nil {
		t.Fatalf("session/new: %v", err)
	}

	res := cl.call("session/prompt", map[string]any{
		"sessionId": sess.SessionID,
		"prompt": []map[string]string{{
			"type": "text",
			"text": "用 Read 工具读取 go.mod，然后只回复模块名本身，不要其他内容。",
		}},
	})
	var pr sessionPromptResult
	if err := json.Unmarshal(res, &pr); err != nil {
		t.Fatalf("session/prompt: %v (raw %s)", err, res)
	}
	if pr.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q", pr.StopReason)
	}

	sawToolCall, finalText := false, ""
	deadline := time.After(10 * time.Second)
collect:
	for {
		select {
		case u := <-cl.updates:
			switch u.SessionUpdate {
			case "tool_call":
				sawToolCall = true
			case "agent_message_chunk":
				if u.Content != nil {
					finalText += u.Content.Text
				}
			}
		case <-deadline:
			break collect
		}
	}
	if !sawToolCall {
		t.Error("no tool_call update observed — model did not call Read")
	}
	if finalText == "" || !containsE2E(finalText, "github.com/niuniu-dev/niuniu/agent") {
		t.Errorf("final text = %q, want the module name", finalText)
	}
}
